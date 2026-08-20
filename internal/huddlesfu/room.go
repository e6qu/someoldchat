// Package huddlesfu is an in-process selective forwarding unit for huddles.
//
// The huddle used to be a full mesh: every browser opened a peer connection to
// every other browser and uploaded one copy of its audio and video per other
// participant. That breaks behind NAT (there was not even a STUN server) and
// does not scale — a six-person huddle asked each browser for five uploads.
//
// An SFU inverts that. Each browser holds ONE peer connection, to this binary.
// It publishes its microphone, camera and screen once; the SFU forwards each of
// those tracks to every other participant. One upload per participant, however
// many people are in the huddle, and the media flows through a server the
// browser can always reach rather than depending on a direct peer path.
//
// It runs inside the existing server process — no second service — as one more
// listener. Negotiation has two halves: the browser publishes its own tracks
// with a single initial offer the SFU answers, and thereafter the SFU drives
// renegotiation, offering the browser a fresh description whenever the set of
// tracks it should be receiving changes. Only the SFU offers after the first
// exchange, so the two sides never collide.
package huddlesfu

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"
)

// ErrClosed is returned by a room that has been shut down.
var ErrClosed = errors.New("huddle room is closed")

// Signal is one negotiation message toward a participant's browser. The payload
// is an opaque SDP or ICE-candidate string, exactly as the browser's
// RTCPeerConnection produces and consumes it, so the transport in front of the
// SFU never has to understand it.
type Signal struct {
	Kind      string `json:"kind"` // "offer" or "candidate"
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}

// Sink receives the offers and ICE candidates the SFU sends toward one browser.
type Sink func(Signal)

// Room is one huddle's forwarding unit. Every participant publishes to it and
// subscribes to everyone else through it.
type Room struct {
	api *webrtc.API

	mu       sync.Mutex
	peers    map[string]*peer
	forwards map[string]*webrtc.TrackLocalStaticRTP
	closed   bool
}

// peer is one participant's connection to the SFU.
type peer struct {
	id   string
	pc   *webrtc.PeerConnection
	sink Sink

	mu             sync.Mutex
	ready          bool   // the initial publish offer has been answered
	negotiating    bool   // a server offer is outstanding, so hold the next one
	dirty          bool   // tracks were added since the last offer was sent
	screenStreamID string // the browser-declared msid of this peer's screen lane, if any
}

// NewRoom builds an empty room with its own default WebRTC API — host
// candidates, the default codecs and the standard interceptors. The manager
// builds rooms over a shared, deployment-configured API instead; this
// constructor is for callers and tests that want a standalone room.
func NewRoom() (*Room, error) {
	api, _, err := buildAPI(Config{})
	if err != nil {
		return nil, err
	}
	return newRoomWithAPI(api), nil
}

// newRoomWithAPI builds a room over an already-assembled API, so every room in a
// manager shares one media engine, interceptor set and UDP mux.
func newRoomWithAPI(api *webrtc.API) *Room {
	return &Room{
		api:      api,
		peers:    make(map[string]*peer),
		forwards: make(map[string]*webrtc.TrackLocalStaticRTP),
	}
}

// Join registers a participant and its signalling sink but does not offer yet:
// the browser publishes first with Answer. sink delivers the SFU's later offers
// and ICE candidates for this browser.
func (r *Room) Join(id string, sink Sink, config webrtc.Configuration) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if _, exists := r.peers[id]; exists {
		r.mu.Unlock()
		return fmt.Errorf("participant %q is already in the room", id)
	}
	r.mu.Unlock()

	pc, err := r.api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("new peer connection: %w", err)
	}
	current := &peer{id: id, pc: pc, sink: sink}

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		// The full candidate init — candidate string, sdpMid and
		// sdpMLineIndex — is carried as JSON so the browser can add it directly;
		// a bare candidate string with no media line is rejected by some browsers.
		data, err := json.Marshal(candidate.ToJSON())
		if err == nil {
			sink(Signal{Kind: "candidate", Candidate: string(data)})
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			r.Leave(id)
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		r.publish(id, remote)
	})

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		pc.Close()
		return ErrClosed
	}
	r.peers[id] = current
	r.mu.Unlock()
	return nil
}

// Answer accepts a participant's initial publish offer — the browser sending its
// microphone, camera and a dedicated screen transceiver — and returns the SFU's
// answer. screenStreamID is the msid the browser assigned that screen lane, so
// the SFU can tell a screen track apart from a camera track when either starts
// flowing; it is empty for a browser that publishes no screen lane. After the
// answer, only the SFU offers, so a later subscription change cannot collide
// with the browser.
func (r *Room) Answer(id, offerSDP, screenStreamID string) (string, error) {
	r.mu.Lock()
	current, ok := r.peers[id]
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("participant %q is not in the room", id)
	}
	current.mu.Lock()
	current.screenStreamID = screenStreamID
	current.mu.Unlock()
	if err := current.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return "", fmt.Errorf("set remote offer: %w", err)
	}
	answer, err := current.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := current.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local answer: %w", err)
	}
	current.mu.Lock()
	current.ready = true
	current.mu.Unlock()

	// Now that the browser can receive, hand it everyone else's tracks.
	r.synchronize()
	return answer.SDP, nil
}

// Signal feeds a client→server message — an answer to one of the SFU's offers,
// or a trickled ICE candidate — to the named participant's connection.
func (r *Room) Signal(id string, signal Signal) error {
	r.mu.Lock()
	current, ok := r.peers[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("participant %q is not in the room", id)
	}
	switch signal.Kind {
	case "answer":
		err := current.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: signal.SDP})
		// Clear the outstanding-offer flag whether or not the answer applied.
		// Leaving it set on an error would wedge the peer for good: offer() would
		// never send another description, so a track added while this offer was in
		// flight — marked dirty and waiting for the next offer — would never be
		// renegotiated onto the peer, and the subscriber would simply never receive
		// that stream. Clearing it and re-synchronising lets the peer recover: any
		// still-dirty change is re-offered from whatever state the connection is in.
		current.mu.Lock()
		current.negotiating = false
		current.mu.Unlock()
		r.synchronize()
		return err
	case "candidate":
		if signal.Candidate == "" {
			return nil
		}
		var init webrtc.ICECandidateInit
		if err := json.Unmarshal([]byte(signal.Candidate), &init); err != nil {
			// Tolerate a bare candidate string as well as the JSON init.
			init = webrtc.ICECandidateInit{Candidate: signal.Candidate}
		}
		return current.pc.AddICECandidate(init)
	default:
		return fmt.Errorf("unexpected signal kind %q from a participant", signal.Kind)
	}
}

// Leave removes a participant and stops forwarding its tracks to the rest.
func (r *Room) Leave(id string) {
	r.mu.Lock()
	current, ok := r.peers[id]
	if ok {
		delete(r.peers, id)
		for key := range r.forwards {
			if forwardOwner(key) == id {
				delete(r.forwards, key)
			}
		}
	}
	r.mu.Unlock()
	if ok {
		current.pc.Close()
		r.synchronize()
	}
}

// has reports whether a participant is already in the room.
func (r *Room) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.peers[id]
	return ok
}

// empty reports whether the room has no participants left.
func (r *Room) empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.peers) == 0
}

// Close tears the whole room down.
func (r *Room) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	peers := r.peers
	r.peers = map[string]*peer{}
	r.forwards = map[string]*webrtc.TrackLocalStaticRTP{}
	r.mu.Unlock()
	for _, current := range peers {
		current.pc.Close()
	}
}

// publish turns one incoming remote track into a forwarded local track and
// copies its packets until the remote track ends.
func (r *Room) publish(owner string, remote *webrtc.TrackRemote) {
	key := forwardKey(owner, remote.ID())
	// A screen track is the one whose msid the browser declared as its screen
	// lane; everything else is the participant's camera or microphone. The two
	// are forwarded under distinct stream ids so a subscriber can play a screen
	// share alongside the sharer's camera instead of in place of it.
	r.mu.Lock()
	current := r.peers[owner]
	r.mu.Unlock()
	screen := false
	if current != nil {
		current.mu.Lock()
		screen = current.screenStreamID != "" && remote.StreamID() == current.screenStreamID
		current.mu.Unlock()
	}
	local, err := webrtc.NewTrackLocalStaticRTP(remote.Codec().RTPCodecCapability, remote.ID(), forwardStreamID(owner, screen))
	if err != nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.forwards[key] = local
	r.mu.Unlock()

	r.synchronize()

	// WriteRTP to a local track fans out to every sender bound to it, so this one
	// loop feeds every subscriber. A read error means the track ended.
	buffer := make([]byte, 1500)
	for {
		n, _, readErr := remote.Read(buffer)
		if readErr != nil {
			break
		}
		if _, writeErr := local.Write(buffer[:n]); writeErr != nil {
			break
		}
	}

	r.mu.Lock()
	if stored, ok := r.forwards[key]; ok && stored == local {
		delete(r.forwards, key)
	}
	r.mu.Unlock()
	r.synchronize()
}

// synchronize reconciles every ready peer's outbound senders with the current
// set of forwarded tracks in both directions: it adds a forward the peer should
// be receiving and is not, and removes a sender whose forward is gone because the
// publisher left or its track ended. A peer whose senders changed is marked dirty
// and offered a fresh description; offer decides whether to send now or after an
// outstanding offer is answered, so offers never collide and a change made
// mid-negotiation is not lost. Removal is what stops a participant who leaves from
// lingering as a frozen tile on everyone else — before it, synchronize only ever
// added, so a departed publisher's tracks stayed bound to every other peer.
func (r *Room) synchronize() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	type work struct {
		current *peer
		changed bool
	}
	items := make([]work, 0, len(r.peers))
	for _, current := range r.peers {
		desired := map[string]*webrtc.TrackLocalStaticRTP{}
		for key, local := range r.forwards {
			if forwardOwner(key) == current.id {
				continue
			}
			desired[local.ID()] = local
		}
		existing := map[string]*webrtc.RTPSender{}
		for _, sender := range current.pc.GetSenders() {
			if track := sender.Track(); track != nil {
				existing[track.ID()] = sender
			}
		}
		changed := false
		for id, sender := range existing {
			if _, want := desired[id]; want {
				continue
			}
			if err := current.pc.RemoveTrack(sender); err == nil {
				changed = true
			}
		}
		for id, local := range desired {
			if _, have := existing[id]; have {
				continue
			}
			if _, err := current.pc.AddTrack(local); err == nil {
				changed = true
			}
		}
		items = append(items, work{current: current, changed: changed})
	}
	r.mu.Unlock()

	for _, item := range items {
		if item.changed {
			item.current.mu.Lock()
			item.current.dirty = true
			item.current.mu.Unlock()
		}
		r.offer(item.current)
	}
}

// offer sends one server-initiated offer to a peer when it has unoffered track
// changes, is ready, and has no offer already in flight. The dirty flag persists
// across a negotiation, so a track that AddTrack put in the senders list while an
// earlier offer was outstanding — and so was absent from that offer's SDP — is
// still described by the next offer rather than stranded. It is cleared only when
// an offer is actually sent, and restored if that send fails so a later
// synchronize retries.
func (r *Room) offer(current *peer) {
	current.mu.Lock()
	if !current.ready || current.negotiating || !current.dirty {
		current.mu.Unlock()
		return
	}
	current.dirty = false
	current.negotiating = true
	current.mu.Unlock()

	// Build the offer, retrying a transient failure a couple of times before
	// giving up. A failed CreateOffer/SetLocalDescription is otherwise a dead end:
	// the flag is put back, but nothing re-drives negotiation until the next
	// publish or answer, so a peer could sit with a track it should be receiving
	// and never be offered it.
	var offer webrtc.SessionDescription
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		offer, err = current.pc.CreateOffer(nil)
		if err == nil {
			err = current.pc.SetLocalDescription(offer)
		}
		if err == nil {
			break
		}
	}
	if err != nil {
		current.mu.Lock()
		current.negotiating = false
		current.dirty = true
		current.mu.Unlock()
		return
	}
	current.sink(Signal{Kind: "offer", SDP: offer.SDP})
}
