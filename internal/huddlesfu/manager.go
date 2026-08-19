package huddlesfu

import (
	"context"
	"io"
	"sync"

	"github.com/pion/webrtc/v4"
)

// Emitter delivers a server→browser signal (an SFU offer or ICE candidate) to
// one participant, over whatever realtime channel the host uses — in this
// deployment the server-sent event stream the huddle already listens on. The
// SFU is deliberately ignorant of that transport.
type Emitter interface {
	EmitHuddleSignal(ctx context.Context, workspaceID, callID, recipient string, signal Signal) error
}

// Manager owns one Room per live huddle and routes browser negotiation to it.
// It is the single object the web layer holds; rooms are created lazily on first
// join and disposed when their last participant leaves.
type Manager struct {
	emitter Emitter
	config  webrtc.Configuration
	api     *webrtc.API
	closer  io.Closer

	mu    sync.Mutex
	rooms map[string]*Room
}

// NewManager builds a manager whose rooms share one WebRTC API assembled from
// the deployment config — the advertised public address and shared UDP port, if
// any. It returns an error only if that config cannot be honoured (a UDP port
// that will not bind). The returned Close releases the shared UDP socket.
func NewManager(emitter Emitter, config Config) (*Manager, error) {
	api, closer, err := buildAPI(config)
	if err != nil {
		return nil, err
	}
	return &Manager{emitter: emitter, config: webrtc.Configuration{}, api: api, closer: closer, rooms: make(map[string]*Room)}, nil
}

// Close disposes every room and releases the shared media socket.
func (m *Manager) Close() error {
	m.mu.Lock()
	rooms := m.rooms
	m.rooms = map[string]*Room{}
	closer := m.closer
	m.mu.Unlock()
	for _, room := range rooms {
		room.Close()
	}
	if closer != nil {
		return closer.Close()
	}
	return nil
}

// Offer takes a participant's initial publish offer for a huddle, joining the
// room if this is their first message, and returns the SFU's answer.
func (m *Manager) Offer(workspaceID, callID, participant, offerSDP string) (string, error) {
	room, err := m.room(workspaceID, callID, participant)
	if err != nil {
		return "", err
	}
	return room.Answer(participant, offerSDP)
}

// Signal feeds a participant's answer or trickled candidate to their room.
func (m *Manager) Signal(callID, participant string, signal Signal) error {
	m.mu.Lock()
	room, ok := m.rooms[callID]
	m.mu.Unlock()
	if !ok {
		return ErrClosed
	}
	return room.Signal(participant, signal)
}

// Leave removes a participant and disposes the room once it is empty, so a huddle
// that ends leaves nothing running.
func (m *Manager) Leave(callID, participant string) {
	m.mu.Lock()
	room, ok := m.rooms[callID]
	m.mu.Unlock()
	if !ok {
		return
	}
	room.Leave(participant)
	m.mu.Lock()
	if room.empty() {
		delete(m.rooms, callID)
		room.Close()
	}
	m.mu.Unlock()
}

// room returns the participant's room, creating it and joining them if needed.
func (m *Manager) room(workspaceID, callID, participant string) (*Room, error) {
	m.mu.Lock()
	room, ok := m.rooms[callID]
	if !ok {
		room = newRoomWithAPI(m.api)
		m.rooms[callID] = room
	}
	m.mu.Unlock()

	if room.has(participant) {
		return room, nil
	}
	// The sink forwards each server→browser signal for this participant through
	// the host's realtime channel. It runs asynchronously — a server-initiated
	// renegotiation happens well after the join request that created it — so it
	// carries its own background context rather than a request one that would be
	// cancelled underneath it. A best-effort emit: a browser that has gone away
	// stops answering and its connection fails, which the room already reaps.
	sink := func(signal Signal) {
		_ = m.emitter.EmitHuddleSignal(context.Background(), workspaceID, callID, participant, signal)
	}
	if err := room.Join(participant, sink, m.config); err != nil {
		return nil, err
	}
	return room, nil
}
