//go:build huddlemedia

// These are the SFU's real-media forwarding tests: two or three pion peer
// connections per test complete an actual ICE/DTLS/RTP handshake over UDP. That
// handshake is reliable on a developer machine but not inside `go test ./...` on
// a shared CI runner, where a hundred package binaries running at once starve
// pion's ICE connectivity checks until a peer is declared failed and reaped — the
// instrumentation caught the publisher vanishing mid-test with the receiver still
// healthy. They are therefore kept behind the `huddlemedia` build tag and out of
// the default suite, and the Makefile runs them in a dedicated pass of their own,
// where nothing else on the runner competes for the handshake. The forwarding
// logic they cover still runs on every CI build; only its isolation changes.
package huddlesfu

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// testAPI builds the WebRTC API the test's peers use — the SFU's default codecs
// and interceptors, but with ICE given far longer than pion's defaults to finish
// its connectivity checks. Under `go test -race ./...` on a shared runner, those
// checks (STUN over loopback) can be starved past the default failed timeout, at
// which point pion declares the connection failed; the SFU reaps a failed peer,
// so the publisher would simply vanish and nothing was forwarded — which the
// timeout diagnostics showed as a room left with only the receiver in it. The
// generous window lets a starved handshake complete instead of being killed. It
// is test-only: a real deployment wants the default timeouts so a genuinely dead
// connection is cleaned up promptly.
func testAPI(t *testing.T) *webrtc.API {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		t.Fatalf("register codecs: %v", err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, registry); err != nil {
		t.Fatalf("register interceptors: %v", err)
	}
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetICETimeouts(30*time.Second, 60*time.Second, 2*time.Second)
	// Offer the loopback address as an ICE candidate, which pion excludes by
	// default. Both peers then hold a 127.0.0.1 host candidate that pairs
	// directly, so the handshake does not depend on a routable interface or on
	// mDNS resolving a .local candidate — neither of which a CI runner reliably
	// provides, which is why the publisher's connection kept failing there while
	// it always connected on a developer machine.
	settingEngine.SetIncludeLoopbackCandidate(true)
	return webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(settingEngine),
	)
}

// debugState snapshots what the room and every peer look like right now. A
// forwarding test that times out prints it so an intermittent CI stall reports
// which half wedged — a peer stuck negotiating or dirty is the SFU's own state
// machine, a peer whose ICE never left "checking" is the handshake or the runner
// — rather than only "a lane did not arrive".
func (r *Room) debugState() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := make([]string, 0, len(r.forwards))
	for key := range r.forwards {
		owners = append(owners, forwardOwner(key))
	}
	sort.Strings(owners)
	var b strings.Builder
	fmt.Fprintf(&b, "room closed=%v forwards=%d owners=%v peers=%d\n", r.closed, len(r.forwards), owners, len(r.peers))
	for id, current := range r.peers {
		current.mu.Lock()
		ready, negotiating, dirty, screenID := current.ready, current.negotiating, current.dirty, current.screenStreamID
		current.mu.Unlock()
		senders := 0
		for _, sender := range current.pc.GetSenders() {
			if sender.Track() != nil {
				senders++
			}
		}
		fmt.Fprintf(&b, "  peer %q ready=%v negotiating=%v dirty=%v conn=%s ice=%s sig=%s senders=%d screenID=%q\n",
			id, ready, negotiating, dirty, current.pc.ConnectionState(), current.pc.ICEConnectionState(), current.pc.SignalingState(), senders, screenID)
	}
	return b.String()
}

// forwardDeadline bounds how long a test waits for the ICE/DTLS handshake and
// the first forwarded RTP to complete. Locally each test finishes in a second or
// two. The margin absorbs ordinary scheduling contention on a shared CI runner —
// generously, because the failure it guards against is only ever a genuine stall,
// and the package's own 30m timeout still bounds that. It sits well above the
// worst honest handshake seen once the leaked peer connections (see newBrowser)
// were closed.
const forwardDeadline = 120 * time.Second

// browser stands in for a participant's browser: a real pion peer connection
// wired to the SFU over the same offer/answer/candidate signals the web client
// exchanges, so the test exercises the actual media path — ICE, DTLS-SRTP and
// RTP forwarding — not a mock.
type browser struct {
	t    *testing.T
	id   string
	room *Room
	pc   *webrtc.PeerConnection

	screenStreamID string // declared to the SFU in the publish offer, empty for none

	mu         sync.Mutex
	haveRemote bool
	pending    []webrtc.ICECandidateInit
}

func newBrowser(t *testing.T, api *webrtc.API, room *Room, id string) *browser {
	t.Helper()
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new browser pc: %v", err)
	}
	// Close the browser's own connection when the test ends. Only the room's side
	// of each peer was ever torn down (room.Close), so every test leaked two live
	// PeerConnections — each holding a UDP socket and a running ICE agent. Under
	// `go test ./...` those accumulate across the package's tests and, on a
	// resource-constrained CI runner, starve the very handshakes these tests time,
	// which showed up as a forwarding test blowing past its deadline while passing
	// on an unloaded developer machine.
	t.Cleanup(func() { _ = pc.Close() })
	b := &browser{t: t, id: id, room: room, pc: pc}
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		data, err := json.Marshal(candidate.ToJSON())
		if err == nil {
			_ = room.Signal(id, Signal{Kind: "candidate", Candidate: string(data)})
		}
	})
	// The sink is how the SFU talks back to this browser: a renegotiation offer,
	// or a trickled candidate. Candidates that arrive before the browser has a
	// remote description are queued and flushed once it does.
	if err := room.Join(id, b.sink, webrtc.Configuration{}); err != nil {
		t.Fatalf("join %s: %v", id, err)
	}
	return b
}

func (b *browser) sink(signal Signal) {
	switch signal.Kind {
	case "offer":
		if err := b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: signal.SDP}); err != nil {
			return
		}
		b.flush()
		answer, err := b.pc.CreateAnswer(nil)
		if err != nil {
			return
		}
		if err := b.pc.SetLocalDescription(answer); err != nil {
			return
		}
		_ = b.room.Signal(b.id, Signal{Kind: "answer", SDP: answer.SDP})
	case "candidate":
		b.addCandidate(webrtc.ICECandidateInit{Candidate: signal.Candidate})
	}
}

func (b *browser) addCandidate(candidate webrtc.ICECandidateInit) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.haveRemote {
		b.pending = append(b.pending, candidate)
		return
	}
	_ = b.pc.AddICECandidate(candidate)
}

func (b *browser) flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.haveRemote = true
	for _, candidate := range b.pending {
		_ = b.pc.AddICECandidate(candidate)
	}
	b.pending = nil
}

// publishAndConnect adds one or more tracks, makes the initial publish offer,
// and completes the exchange with the SFU's answer.
func (b *browser) publish(tracks ...webrtc.TrackLocal) {
	b.t.Helper()
	for _, track := range tracks {
		if _, err := b.pc.AddTrack(track); err != nil {
			b.t.Fatalf("%s add track: %v", b.id, err)
		}
	}
	b.connect()
}

// connect makes an initial offer even without a published track, so a
// receive-only participant still establishes its connection.
func (b *browser) connect() {
	b.t.Helper()
	if len(b.pc.GetTransceivers()) == 0 {
		if _, err := b.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			b.t.Fatalf("%s add recv transceiver: %v", b.id, err)
		}
	}
	offer, err := b.pc.CreateOffer(nil)
	if err != nil {
		b.t.Fatalf("%s create offer: %v", b.id, err)
	}
	if err := b.pc.SetLocalDescription(offer); err != nil {
		b.t.Fatalf("%s set local offer: %v", b.id, err)
	}
	answerSDP, err := b.room.Answer(b.id, offer.SDP, b.screenStreamID)
	if err != nil {
		b.t.Fatalf("%s answer: %v", b.id, err)
	}
	if err := b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}); err != nil {
		b.t.Fatalf("%s set remote answer: %v", b.id, err)
	}
	b.flush()
}

// TestRoomForwardsAPublishedTrackToAnotherParticipant is the load-bearing proof
// that the SFU relays media: one browser publishes a video track, and a second
// browser — which opened exactly one connection, to the SFU — receives that
// track forwarded through this process, attributed to the publisher.
func TestRoomForwardsAPublishedTrackToAnotherParticipant(t *testing.T) {
	api := testAPI(t)
	room := newRoomWithAPI(api)
	defer room.Close()

	// The receiver joins first and reports the track it is handed.
	receiver := newBrowser(t, api, room, "receiver")
	received := make(chan *webrtc.TrackRemote, 1)
	receiver.pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case received <- remote:
		default:
		}
	})
	receiver.connect()

	// The publisher joins and sends VP8 video.
	publisher := newBrowser(t, api, room, "publisher")
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "publisher-camera")
	if err != nil {
		t.Fatal(err)
	}
	publisher.publish(track)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = track.WriteSample(media.Sample{Data: []byte{0x00, 0x01, 0x02, 0x03, 0x04}, Duration: 20 * time.Millisecond})
			}
		}
	}()

	var remote *webrtc.TrackRemote
	select {
	case remote = <-received:
	case <-time.After(forwardDeadline):
		t.Fatal("the receiver never got the publisher's forwarded track")
	}

	// The forwarded stream id names the participant it came from, so the browser
	// can place it on the right tile.
	if owner := ForwardedOwner(remote.StreamID()); owner != "publisher" {
		t.Fatalf("forwarded track attributed to %q, want publisher", owner)
	}

	// And real RTP flows: at least one packet crosses the forwarded path.
	done := make(chan error, 1)
	go func() {
		_ = remote.SetReadDeadline(time.Now().Add(forwardDeadline))
		_, _, readErr := remote.ReadRTP()
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr != nil {
			t.Fatalf("reading a forwarded RTP packet: %v", readErr)
		}
	case <-time.After(forwardDeadline):
		t.Fatal("no forwarded RTP packet arrived")
	}
}

// pump writes VP8 sample data on a track every 20ms until the test ends, so a
// forwarded track has real RTP to carry.
func pump(t *testing.T, track *webrtc.TrackLocalStaticSample) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = track.WriteSample(media.Sample{Data: []byte{0x00, 0x01, 0x02, 0x03, 0x04}, Duration: 20 * time.Millisecond})
			}
		}
	}()
}

// TestRoomForwardsCameraAndScreenAsDistinctStreams proves the SFU relays a
// participant's camera and screen simultaneously on separate streams, so a
// subscriber can show a screen share alongside the sharer's camera rather than
// in place of it. One browser publishes two video tracks in two media streams
// and declares which is its screen; the other receives both, correctly
// attributed, with real RTP flowing on each.
func TestRoomForwardsCameraAndScreenAsDistinctStreams(t *testing.T) {
	api := testAPI(t)
	room := newRoomWithAPI(api)
	defer room.Close()

	receiver := newBrowser(t, api, room, "receiver")
	type arrival struct {
		owner  string
		screen bool
	}
	arrivals := make(chan arrival, 4)
	rtp := make(chan bool, 4) // true when the screen stream carried a packet
	receiver.pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		screen := ForwardedIsScreen(remote.StreamID())
		arrivals <- arrival{owner: ForwardedOwner(remote.StreamID()), screen: screen}
		go func() {
			_ = remote.SetReadDeadline(time.Now().Add(forwardDeadline))
			if _, _, readErr := remote.ReadRTP(); readErr == nil {
				rtp <- screen
			}
		}()
	})
	receiver.connect()

	// The publisher sends its camera and its screen as two separate streams and
	// tells the SFU which media stream is the screen.
	publisher := newBrowser(t, api, room, "publisher")
	cameraTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "camera", "publisher-camera")
	if err != nil {
		t.Fatal(err)
	}
	screenTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "screen", "publisher-screen")
	if err != nil {
		t.Fatal(err)
	}
	publisher.screenStreamID = "publisher-screen"
	publisher.publish(cameraTrack, screenTrack)
	pump(t, cameraTrack)
	pump(t, screenTrack)

	// Both lanes reach the receiver, attributed to the publisher, one camera and
	// one screen.
	sawCamera, sawScreen := false, false
	deadline := time.After(forwardDeadline)
	for !sawCamera || !sawScreen {
		select {
		case got := <-arrivals:
			if got.owner != "publisher" {
				t.Fatalf("forwarded track attributed to %q, want publisher", got.owner)
			}
			if got.screen {
				sawScreen = true
			} else {
				sawCamera = true
			}
		case <-deadline:
			t.Fatalf("did not receive both lanes: camera=%v screen=%v\n%s", sawCamera, sawScreen, room.debugState())
		}
	}

	// The screen lane carries real RTP, so it is a forwarded media path and not a
	// negotiated-but-silent transceiver.
	select {
	case screen := <-rtp:
		for !screen {
			select {
			case screen = <-rtp:
			case <-time.After(forwardDeadline):
				t.Fatalf("no RTP packet arrived on the forwarded screen lane\n%s", room.debugState())
			}
		}
	case <-time.After(forwardDeadline):
		t.Fatalf("no forwarded RTP packet arrived\n%s", room.debugState())
	}
}

// activeSenders counts how many of a peer's outbound senders still carry a live
// track, so a test can watch the SFU stop forwarding a departed participant.
func (r *Room) activeSenders(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.peers[id]
	if !ok {
		return -1
	}
	count := 0
	for _, sender := range current.pc.GetSenders() {
		if sender.Track() != nil {
			count++
		}
	}
	return count
}

// TestRoomStopsForwardingAParticipantWhoLeaves proves the SFU removes a departed
// participant's tracks from everyone still in the room. synchronize used only to
// add forwards, so a publisher who left stayed bound to every other peer as a
// frozen tile that never cleared; reconciling removals as well as additions is
// what lets a leaver's media actually stop.
func TestRoomStopsForwardingAParticipantWhoLeaves(t *testing.T) {
	api := testAPI(t)
	room := newRoomWithAPI(api)
	defer room.Close()

	receiver := newBrowser(t, api, room, "receiver")
	received := make(chan struct{}, 1)
	receiver.pc.OnTrack(func(_ *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case received <- struct{}{}:
		default:
		}
	})
	receiver.connect()

	publisher := newBrowser(t, api, room, "publisher")
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "leaver-camera")
	if err != nil {
		t.Fatal(err)
	}
	publisher.publish(track)
	pump(t, track)

	select {
	case <-received:
	case <-time.After(forwardDeadline):
		t.Fatalf("the receiver never got the forwarded track\n%s", room.debugState())
	}
	if senders := room.activeSenders("receiver"); senders != 1 {
		t.Fatalf("receiver has %d active senders before the publisher leaves, want 1", senders)
	}

	// The publisher leaves; the SFU must drop its track from the receiver.
	room.Leave("publisher")

	deadline := time.After(forwardDeadline)
	for room.activeSenders("receiver") != 0 {
		select {
		case <-deadline:
			t.Fatalf("the receiver still forwards the departed publisher: %d senders\n%s", room.activeSenders("receiver"), room.debugState())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// waitForSenders blocks until a peer has exactly the wanted number of live
// outbound senders, failing with the room's state if it never settles there.
func waitForSenders(t *testing.T, room *Room, id string, want int) {
	t.Helper()
	deadline := time.After(forwardDeadline)
	for room.activeSenders(id) != want {
		select {
		case <-deadline:
			t.Fatalf("peer %q has %d active senders, want %d\n%s", id, room.activeSenders(id), want, room.debugState())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestRoomPausesAndResumesAScreenShare proves that stopping a screen share takes
// it off other participants' tiles and restarting it puts it back. A browser
// that stops sharing only replaces its outgoing track with nothing, which the
// SFU cannot distinguish from a static screen, so it says so with a signal; the
// forwarded track is kept across the pause so resuming re-attaches it without a
// fresh publish.
func TestRoomPausesAndResumesAScreenShare(t *testing.T) {
	api := testAPI(t)
	room := newRoomWithAPI(api)
	defer room.Close()

	receiver := newBrowser(t, api, room, "receiver")
	receiver.pc.OnTrack(func(_ *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {})
	receiver.connect()

	publisher := newBrowser(t, api, room, "publisher")
	cameraTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "camera", "publisher-camera")
	if err != nil {
		t.Fatal(err)
	}
	screenTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "screen", "publisher-screen")
	if err != nil {
		t.Fatal(err)
	}
	publisher.screenStreamID = "publisher-screen"
	publisher.publish(cameraTrack, screenTrack)
	pump(t, cameraTrack)
	pump(t, screenTrack)

	// Camera and screen both forwarded.
	waitForSenders(t, room, "receiver", 2)

	// Stopping the share drops the screen lane; the camera stays.
	room.setScreenActive("publisher", false)
	waitForSenders(t, room, "receiver", 1)

	// Restarting it puts the screen back.
	room.setScreenActive("publisher", true)
	waitForSenders(t, room, "receiver", 2)
}
