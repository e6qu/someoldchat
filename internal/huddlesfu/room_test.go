package huddlesfu

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// browser stands in for a participant's browser: a real pion peer connection
// wired to the SFU over the same offer/answer/candidate signals the web client
// exchanges, so the test exercises the actual media path — ICE, DTLS-SRTP and
// RTP forwarding — not a mock.
type browser struct {
	t    *testing.T
	id   string
	room *Room
	pc   *webrtc.PeerConnection

	mu         sync.Mutex
	haveRemote bool
	pending    []webrtc.ICECandidateInit
}

func newBrowser(t *testing.T, room *Room, id string) *browser {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new browser pc: %v", err)
	}
	b := &browser{t: t, id: id, room: room, pc: pc}
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			_ = room.Signal(id, Signal{Kind: "candidate", Candidate: candidate.ToJSON().Candidate})
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

// publishAndConnect adds a video track, makes the initial publish offer, and
// completes the exchange with the SFU's answer.
func (b *browser) publish(track webrtc.TrackLocal) {
	b.t.Helper()
	if _, err := b.pc.AddTrack(track); err != nil {
		b.t.Fatalf("%s add track: %v", b.id, err)
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
	answerSDP, err := b.room.Answer(b.id, offer.SDP)
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
	room, err := NewRoom()
	if err != nil {
		t.Fatal(err)
	}
	defer room.Close()

	// The receiver joins first and reports the track it is handed.
	receiver := newBrowser(t, room, "receiver")
	received := make(chan *webrtc.TrackRemote, 1)
	receiver.pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case received <- remote:
		default:
		}
	})
	receiver.connect()

	// The publisher joins and sends VP8 video.
	publisher := newBrowser(t, room, "publisher")
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
	case <-time.After(20 * time.Second):
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
		_ = remote.SetReadDeadline(time.Now().Add(20 * time.Second))
		_, _, readErr := remote.ReadRTP()
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr != nil {
			t.Fatalf("reading a forwarded RTP packet: %v", readErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("no forwarded RTP packet arrived")
	}
}
