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
	"errors"
	"fmt"
	"sync"

	"github.com/pion/interceptor"
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

	mu          sync.Mutex
	ready       bool // the initial publish offer has been answered
	negotiating bool // a server offer is outstanding, so hold the next one
}

// NewRoom builds an empty room with the default codecs and the standard
// RTCP/NACK/report interceptors a browser expects.
func NewRoom() (*Room, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("register codecs: %w", err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, registry); err != nil {
		return nil, fmt.Errorf("register interceptors: %w", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(registry))
	return &Room{
		api:      api,
		peers:    make(map[string]*peer),
		forwards: make(map[string]*webrtc.TrackLocalStaticRTP),
	}, nil
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
		if candidate != nil {
			sink(Signal{Kind: "candidate", Candidate: candidate.ToJSON().Candidate})
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
// own microphone, camera and screen transceivers — and returns the SFU's answer.
// After it, only the SFU offers, so a later subscription change cannot collide
// with the browser.
func (r *Room) Answer(id, offerSDP string) (string, error) {
	r.mu.Lock()
	current, ok := r.peers[id]
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("participant %q is not in the room", id)
	}
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
		if err := current.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: signal.SDP}); err != nil {
			return err
		}
		current.mu.Lock()
		current.negotiating = false
		current.mu.Unlock()
		// A subscription may have arrived while this offer was in flight.
		r.synchronize()
		return nil
	case "candidate":
		if signal.Candidate == "" {
			return nil
		}
		return current.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: signal.Candidate})
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
	local, err := webrtc.NewTrackLocalStaticRTP(remote.Codec().RTPCodecCapability, remote.ID(), forwardStreamID(owner, remote.StreamID()))
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
// set of forwarded tracks and offers a fresh description to any peer whose
// senders changed. A peer already in sync, not yet ready, or with an offer still
// outstanding is left alone, so offers never collide.
func (r *Room) synchronize() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	type work struct {
		current *peer
		offer   bool
	}
	items := make([]work, 0, len(r.peers))
	for _, current := range r.peers {
		existing := map[string]struct{}{}
		for _, sender := range current.pc.GetSenders() {
			if track := sender.Track(); track != nil {
				existing[track.ID()] = struct{}{}
			}
		}
		changed := false
		for key, local := range r.forwards {
			if forwardOwner(key) == current.id {
				continue
			}
			if _, have := existing[local.ID()]; have {
				continue
			}
			if _, err := current.pc.AddTrack(local); err == nil {
				changed = true
			}
		}
		items = append(items, work{current: current, offer: changed})
	}
	r.mu.Unlock()

	for _, item := range items {
		if !item.offer {
			continue
		}
		r.offer(item.current)
	}
}

// offer sends one server-initiated offer to a peer, unless it is not ready yet
// or already has an offer in flight (in which case the pending change is picked
// up when that offer is answered).
func (r *Room) offer(current *peer) {
	current.mu.Lock()
	if !current.ready || current.negotiating {
		current.mu.Unlock()
		return
	}
	current.negotiating = true
	current.mu.Unlock()

	offer, err := current.pc.CreateOffer(nil)
	if err == nil {
		err = current.pc.SetLocalDescription(offer)
	}
	if err != nil {
		current.mu.Lock()
		current.negotiating = false
		current.mu.Unlock()
		return
	}
	current.sink(Signal{Kind: "offer", SDP: offer.SDP})
}
