package socketmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const (
	connectionLifetime = 30 * time.Second
	maxEnvelopeBytes   = 1 << 20
	// Slack's Socket Mode contract keeps a connection alive with ping/pong. A
	// peer that disappears without a TCP FIN is otherwise undetectable, and the
	// connection plus its reader goroutine and its connection-limit slot stay
	// pinned until the operating system gives up on the socket.
	pingPeriod = 10 * time.Second
	// readTimeout allows one missed ping before the peer is declared gone.
	readTimeout = 2 * pingPeriod
	// writeTimeout bounds every write, so a peer that stops reading cannot
	// block the handler once the send buffer fills.
	writeTimeout = 10 * time.Second
	// envelopeTimeout bounds how long delivery waits for an app to acknowledge
	// an envelope. Waiting forever stalls the app's whole event stream with no
	// error anywhere.
	envelopeTimeout = 30 * time.Second
)

var ErrInvalidAppID = errors.New("Socket Mode app ID is required")
var ErrConnectionLimit = errors.New("Socket Mode connection limit reached")

type ConnectionStore interface {
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
	RenewSocketModeConnection(context.Context, string, time.Time) error
	ReleaseSocketModeConnection(context.Context, string) error
	CountSocketModeConnections(context.Context, domain.AppID) (int, error)
}

type EventQueue interface {
	ClaimAppEvent(context.Context, domain.AppID, string, string, time.Duration) (events.Record, int, string, bool, error)
	AckAppEvent(context.Context, domain.AppID, string, string, uint64) error
	ReleaseAppEvent(context.Context, domain.AppID, string, string, uint64, string, time.Time) error
}

type InteractionQueue interface {
	ClaimSocketModeInteraction(context.Context, domain.AppID, string, time.Duration) (domain.SocketModeInteraction, bool, error)
	AckSocketModeInteraction(context.Context, domain.AppID, string, string) error
	ReleaseSocketModeInteraction(context.Context, domain.AppID, string, string, string, time.Time) error
}

type ResponseSink interface {
	HandleSocketModeResponse(context.Context, domain.AppID, string, []byte) error
}

type ResponseRecorder struct {
	Store interface {
		RecordSocketModeResponse(context.Context, domain.SocketModeResponse) error
	}
	Now func() time.Time
}

func (r ResponseRecorder) HandleSocketModeResponse(ctx context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	if r.Store == nil {
		return errors.New("Socket Mode response recorder requires a store")
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return r.Store.RecordSocketModeResponse(ctx, domain.SocketModeResponse{
		AppID:      appID,
		EnvelopeID: envelopeID,
		Payload:    string(payload),
		ReceivedAt: now().UTC(),
	})
}

type Service struct {
	Store ConnectionStore
	Host  string
	TLS   bool
}

type OpenResult struct {
	URL string
}

func (s Service) Open(ctx context.Context, appID domain.AppID) (OpenResult, error) {
	if s.Store == nil {
		return OpenResult{}, errors.New("Socket Mode requires a connection store")
	}
	if strings.TrimSpace(string(appID)) == "" {
		return OpenResult{}, ErrInvalidAppID
	}
	if strings.TrimSpace(s.Host) == "" {
		return OpenResult{}, errors.New("Socket Mode requires a public host")
	}
	active, err := s.Store.CountSocketModeConnections(ctx, appID)
	if err != nil {
		return OpenResult{}, err
	}
	if active >= domain.SocketModeConnectionLimit {
		return OpenResult{}, ErrConnectionLimit
	}
	id, err := domain.NewSocketModeConnectionID()
	if err != nil {
		return OpenResult{}, err
	}
	connection := domain.SocketModeConnection{ID: id, AppID: appID, ExpiresAt: time.Now().UTC().Add(connectionLifetime)}
	if err := s.Store.CreateSocketModeConnection(ctx, connection); err != nil {
		if errors.Is(err, store.ErrSocketModeConnectionLimit) {
			return OpenResult{}, ErrConnectionLimit
		}
		return OpenResult{}, err
	}
	scheme := "ws"
	if s.TLS {
		scheme = "wss"
	}
	return OpenResult{URL: (&url.URL{Scheme: scheme, Host: s.Host, Path: "/socket-mode", RawQuery: url.Values{"connection_id": []string{id}}.Encode()}).String()}, nil
}

type Handler struct {
	Store ConnectionStore
	// Queue leases each durable record to one connection. An application may
	// keep up to ten connections open, but Slack delivers each Socket Mode
	// envelope to only one of them.
	Queue        EventQueue
	Interactions InteractionQueue
	Responses    ResponseSink
	Upgrader     websocket.Upgrader
	// Logger records connection-level failures that are handled rather than
	// returned to a caller: a released connection slot that could not be
	// released, and a durable record that can never be delivered to an app.
	// Both are invisible without it.
	Logger *slog.Logger
}

func (h Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("connection_id"))
	if id == "" {
		http.Error(w, "connection_id is required", http.StatusBadRequest)
		return
	}
	if h.Store == nil {
		http.Error(w, "Socket Mode is unavailable", http.StatusServiceUnavailable)
		return
	}
	connection, err := h.Store.ConsumeSocketModeConnection(r.Context(), id)
	if err != nil {
		// At the limit the ticket is perfectly valid; the app is simply holding
		// as many connections as it may. Answering 401 would send a client off
		// to re-authenticate instead of releasing a connection and retrying.
		if errors.Is(err, store.ErrSocketModeConnectionLimit) {
			http.Error(w, "Socket Mode connection limit reached", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "connection is invalid or expired", http.StatusUnauthorized)
		return
	}
	defer func() {
		if releaseErr := h.Store.ReleaseSocketModeConnection(context.Background(), connection.ID); releaseErr != nil {
			h.logger().Error("Socket Mode connection slot was not released", "connection", connection.ID, "app", connection.AppID, "error", releaseErr)
		}
	}()
	upgrader := h.Upgrader
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxEnvelopeBytes)
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	writeJSON := func(value any) error {
		if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return err
		}
		return conn.WriteJSON(value)
	}
	closeWith := func(code int, reason string) {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(writeTimeout))
	}
	delivery := h.newDelivery(connection)
	if delivery != nil {
		defer func() {
			if releaseErr := delivery.release(context.Background(), "connection_closed", time.Now().UTC()); releaseErr != nil &&
				!errors.Is(releaseErr, store.ErrLeaseConflict) && !errors.Is(releaseErr, store.ErrNotFound) {
				h.logger().Error("Socket Mode event lease was not released", "connection", connection.ID, "app", connection.AppID, "error", releaseErr)
			}
		}()
	}
	interactionDelivery := h.newInteractionDelivery(connection)
	if interactionDelivery != nil {
		defer func() {
			if releaseErr := interactionDelivery.release(context.Background(), "connection_closed", time.Now().UTC()); releaseErr != nil &&
				!errors.Is(releaseErr, store.ErrLeaseConflict) && !errors.Is(releaseErr, store.ErrNotFound) {
				h.logger().Error("Socket Mode interaction lease was not released", "connection", connection.ID, "app", connection.AppID, "error", releaseErr)
			}
		}()
	}
	connectionCount, err := h.Store.CountSocketModeConnections(r.Context(), connection.AppID)
	if err != nil {
		closeWith(websocket.CloseInternalServerErr, "connection state unavailable")
		return
	}
	// debug_info.host identifies the host serving the connection. Reporting the
	// app ID there makes SDK diagnostics and reconnect logs describe the client
	// instead of the replica the client is talking to.
	if err := writeJSON(map[string]any{"type": "hello", "num_connections": connectionCount, "debug_info": map[string]string{"host": servingHost(r)}}); err != nil {
		return
	}
	// done is closed before the connection is closed, so the reader goroutine
	// is woken whether it is blocked in ReadMessage or blocked handing a
	// pipelined frame to this loop. Without it a client that pipelines frames
	// leaks the goroutine, the connection and its buffers for the process
	// lifetime.
	done := make(chan struct{})
	defer close(done)
	readErrors := make(chan error, 1)
	readMessages := make(chan []byte, 1)
	go func() {
		for {
			messageType, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				select {
				case readErrors <- readErr:
				case <-done:
				}
				return
			}
			if messageType != websocket.TextMessage {
				select {
				case readErrors <- errors.New("Socket Mode requires text messages"):
				case <-done:
				}
				return
			}
			select {
			case readMessages <- payload:
			case <-done:
				return
			}
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	leaseTicker := time.NewTicker(connectionLifetime / 3)
	defer leaseTicker.Stop()
	pingTicker := time.NewTicker(pingPeriod)
	defer pingTicker.Stop()
	type pendingEnvelope struct {
		sequence    uint64
		interaction bool
	}
	pending := make(map[string]pendingEnvelope, 1)
	var pendingSince time.Time
	for {
		select {
		case err := <-readErrors:
			closeWith(websocket.CloseProtocolError, err.Error())
			return
		case payload := <-readMessages:
			var envelope struct {
				EnvelopeID string          `json:"envelope_id"`
				Payload    json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil || strings.TrimSpace(envelope.EnvelopeID) == "" {
				closeWith(websocket.CloseProtocolError, "envelope_id is required")
				return
			}
			if delivery == nil && interactionDelivery == nil {
				if err := writeJSON(map[string]string{"envelope_id": envelope.EnvelopeID}); err != nil {
					return
				}
				continue
			}
			current, exists := pending[envelope.EnvelopeID]
			if !exists {
				closeWith(websocket.CloseProtocolError, "unknown envelope_id")
				return
			}
			if len(envelope.Payload) != 0 {
				var responsePayload map[string]json.RawMessage
				if json.Unmarshal(envelope.Payload, &responsePayload) != nil || responsePayload == nil {
					closeWith(websocket.CloseProtocolError, "response payload must be a JSON object")
					return
				}
				if h.Responses == nil {
					closeWith(websocket.ClosePolicyViolation, "response payload routing is unavailable")
					return
				}
				if err := h.Responses.HandleSocketModeResponse(r.Context(), connection.AppID, envelope.EnvelopeID, envelope.Payload); err != nil {
					closeWith(websocket.CloseInternalServerErr, "response payload routing failed")
					return
				}
			}
			delete(pending, envelope.EnvelopeID)
			// One durable record can be several envelopes — a single
			// conversation.members_invited record is one member_joined_channel
			// per invited user — and the delivery position names the record.
			// Advancing it after the first acknowledgement would drop every
			// envelope behind it, so the record is consumed only once the app
			// has acknowledged all of them.
			if len(pending) != 0 {
				continue
			}
			if current.interaction {
				if err := interactionDelivery.consume(r.Context(), envelope.EnvelopeID); err != nil {
					closeWith(deliveryCloseCode(err))
					return
				}
			} else {
				if err := delivery.consume(r.Context(), current.sequence); err != nil {
					closeWith(deliveryCloseCode(err))
					return
				}
			}
		case <-ticker.C:
			if delivery == nil && interactionDelivery == nil {
				continue
			}
			if len(pending) != 0 {
				if time.Since(pendingSince) < envelopeTimeout {
					continue
				}
				h.logger().Warn("Socket Mode envelope was not acknowledged", "app", connection.AppID, "connection", connection.ID, "timeout", envelopeTimeout)
				var releaseErr error
				for _, current := range pending {
					if current.interaction {
						releaseErr = interactionDelivery.release(r.Context(), "ack_timeout", time.Now().UTC())
					} else {
						releaseErr = delivery.release(r.Context(), "ack_timeout", time.Now().UTC())
					}
					break
				}
				if releaseErr != nil {
					closeWith(deliveryCloseCode(releaseErr))
					return
				}
				closeWith(websocket.CloseTryAgainLater, "envelope was not acknowledged")
				return
			}
			if interactionDelivery != nil {
				interaction, ok, err := interactionDelivery.next(r.Context())
				if err != nil {
					closeWith(websocket.CloseInternalServerErr, "interaction source unavailable")
					return
				}
				if ok {
					var payload json.RawMessage = []byte(interaction.Payload)
					if err := writeJSON(map[string]any{
						"envelope_id": interaction.EnvelopeID, "type": interaction.Type,
						"payload": payload, "accepts_response_payload": true,
					}); err != nil {
						return
					}
					pending[interaction.EnvelopeID] = pendingEnvelope{interaction: true}
					pendingSince = time.Now()
					continue
				}
			}
			if delivery == nil {
				continue
			}
			record, ok, err := delivery.next(r.Context())
			if err != nil {
				closeWith(websocket.CloseInternalServerErr, "event source unavailable")
				return
			}
			if !ok {
				continue
			}
			envelopes, err := encodeEvent(record, connection.AppID)
			if err != nil {
				// An internal worker record, a record addressed to a single user,
				// or a payload written before the typed payload contract can never
				// be delivered to an app. Closing here would leave the record in
				// place and reconnect into it forever, so it is consumed durably
				// and reported instead.
				//
				// A record with no identifier is reported separately and at Error:
				// the payload may be a perfectly deliverable event, and what is
				// wrong is the record's own identity, which no reconnect recovers.
				// A payload that names a Slack event this system knows but cannot
				// fill in is a defect in the producer, and it is reported as one:
				// it is not an app problem, and no reconnect fixes it.
				if errors.Is(err, events.ErrEventIncomplete) {
					h.logger().Error("Socket Mode dropped a record with no event ID", "app", connection.AppID, "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
				} else if errors.Is(err, events.ErrSlackEventIncomplete) || errors.Is(err, events.ErrPayloadFieldInvalid) {
					h.logger().Error("Socket Mode dropped a record whose payload cannot fill its Slack event", "app", connection.AppID, "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
				} else if !errors.Is(err, events.ErrPayloadInternal) && !errors.Is(err, events.ErrPayloadMalformed) && !errors.Is(err, events.ErrPayloadRecipientScoped) {
					closeWith(websocket.CloseInternalServerErr, "event payload is invalid")
					return
				} else {
					h.logger().Warn("Socket Mode skipped an undeliverable event", "app", connection.AppID, "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
				}
				if consumeErr := delivery.consume(r.Context(), record.Sequence); consumeErr != nil {
					closeWith(deliveryCloseCode(consumeErr))
					return
				}
				continue
			}
			if len(envelopes) == 0 {
				// The record's topic maps to no Slack event, or to one whose
				// inner shape this repository has not modelled. Shipping the
				// durable record instead is what made every official client
				// unable to parse this stream, so the record is stepped over
				// durably. Debug rather than Warn: for most topics this is the
				// table's deliberate answer, not an incident.
				h.logger().Debug("Socket Mode has no Slack event for this topic", "app", connection.AppID, "sequence", record.Sequence, "topic", record.Event.Topic)
				if consumeErr := delivery.consume(r.Context(), record.Sequence); consumeErr != nil {
					closeWith(deliveryCloseCode(consumeErr))
					return
				}
				continue
			}
			written := true
			for _, envelope := range envelopes {
				if err := writeJSON(envelope.Frame); err != nil {
					written = false
					break
				}
				pending[envelope.ID] = pendingEnvelope{sequence: record.Sequence}
			}
			if !written {
				return
			}
			pendingSince = time.Now()
		case <-pingTicker.C:
			if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return
			}
		case <-leaseTicker.C:
			if err := h.Store.RenewSocketModeConnection(r.Context(), connection.ID, time.Now().UTC().Add(connectionLifetime)); err != nil {
				closeWith(websocket.CloseInternalServerErr, "connection lease unavailable")
				return
			}
		}
	}
}

func servingHost(r *http.Request) string {
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return name
	}
	return r.Host
}

// newDelivery prepares this connection's lease owner. A handler with no queue
// delivers nothing and answers
// acknowledgements, which is what the response-only deployments use.
func (h Handler) newDelivery(connection domain.SocketModeConnection) *leasedDelivery {
	if h.Queue == nil {
		return nil
	}
	return &leasedDelivery{queue: h.Queue, appID: connection.AppID, owner: connection.ID}
}

func (h Handler) newInteractionDelivery(connection domain.SocketModeConnection) *leasedInteractionDelivery {
	if h.Interactions == nil {
		return nil
	}
	return &leasedInteractionDelivery{queue: h.Interactions, appID: connection.AppID, owner: connection.ID}
}

type leasedInteractionDelivery struct {
	queue      InteractionQueue
	appID      domain.AppID
	owner      string
	envelopeID string
}

func (d *leasedInteractionDelivery) next(ctx context.Context) (domain.SocketModeInteraction, bool, error) {
	if d.envelopeID != "" {
		return domain.SocketModeInteraction{}, false, errors.New("Socket Mode delivery already owns an interaction")
	}
	value, found, err := d.queue.ClaimSocketModeInteraction(ctx, d.appID, d.owner, envelopeTimeout+writeTimeout)
	if errors.Is(err, store.ErrNotFound) {
		return domain.SocketModeInteraction{}, false, nil
	}
	if err != nil || !found {
		return domain.SocketModeInteraction{}, false, err
	}
	d.envelopeID = value.EnvelopeID
	return value, true, nil
}

func (d *leasedInteractionDelivery) consume(ctx context.Context, envelopeID string) error {
	if d.envelopeID != envelopeID {
		return store.ErrLeaseConflict
	}
	if err := d.queue.AckSocketModeInteraction(ctx, d.appID, envelopeID, d.owner); err != nil {
		return err
	}
	d.envelopeID = ""
	return nil
}

func (d *leasedInteractionDelivery) release(ctx context.Context, reason string, retryAt time.Time) error {
	if d.envelopeID == "" {
		return nil
	}
	if err := d.queue.ReleaseSocketModeInteraction(ctx, d.appID, d.envelopeID, d.owner, reason, retryAt); err != nil {
		return err
	}
	d.envelopeID = ""
	return nil
}

type leasedDelivery struct {
	queue    EventQueue
	appID    domain.AppID
	owner    string
	sequence uint64
}

func (d *leasedDelivery) next(ctx context.Context) (events.Record, bool, error) {
	if d.sequence != 0 {
		return events.Record{}, false, errors.New("Socket Mode delivery already owns an event")
	}
	record, _, _, found, err := d.queue.ClaimAppEvent(ctx, d.appID, "socket", d.owner, envelopeTimeout+writeTimeout)
	if errors.Is(err, store.ErrNotFound) {
		return events.Record{}, false, nil
	}
	if err != nil || !found {
		return events.Record{}, false, err
	}
	d.sequence = record.Sequence
	return record, true, nil
}

func (d *leasedDelivery) consume(ctx context.Context, sequence uint64) error {
	if d.sequence != sequence {
		return store.ErrLeaseConflict
	}
	if err := d.queue.AckAppEvent(ctx, d.appID, "socket", d.owner, sequence); err != nil {
		return err
	}
	d.sequence = 0
	return nil
}

func (d *leasedDelivery) release(ctx context.Context, reason string, retryAt time.Time) error {
	if d.sequence == 0 {
		return nil
	}
	if err := d.queue.ReleaseAppEvent(ctx, d.appID, "socket", d.owner, d.sequence, reason, retryAt); err != nil {
		return err
	}
	d.sequence = 0
	return nil
}

// deliveryCloseCode distinguishes losing a race for an envelope from the store
// being unable to answer. A connection that acknowledges an envelope another
// connection already moved past has not caused a server error, and closing it
// with one sends an SDK looking for a fault that does not exist.
func deliveryCloseCode(err error) (int, string) {
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrLeaseConflict) || errors.Is(err, store.ErrNotFound) {
		return websocket.CloseTryAgainLater, "another connection owns this envelope"
	}
	return websocket.CloseInternalServerErr, "event delivery state unavailable"
}

// encodeEvent renders a durable record as the Socket Mode envelopes it becomes.
//
// The shape is built by events.SocketModeEnvelopes, not here: an official
// Socket Mode client indexes payload.event and dispatches on the inner event's
// type, and this transport used to ship the durable payload verbatim, so every
// official client received a payload with no "event" member and a type that is
// not a Slack event name. A transport must not decide what a Slack event looks
// like, so the whole shape — envelope, wrapper and inner event — comes from the
// one translation site.
//
// An empty result is not an error. It means the record's topic maps to no Slack
// event, or to one whose inner shape is not modelled yet; the record is
// consumed and reported rather than delivered in a shape no client can parse.
func encodeEvent(record events.Record, appID domain.AppID) ([]events.SocketModeEnvelope, error) {
	// A missing identifier is a defect in the record's identity, not in its
	// payload: the payload may be a perfectly deliverable event. Classifying it
	// as a malformed payload made it indistinguishable from an undeliverable one
	// everywhere the two are handled together, including the outbox worker's
	// drop-and-acknowledge path.
	if strings.TrimSpace(string(record.Event.ID)) == "" {
		return nil, fmt.Errorf("%w: Socket Mode envelope ID comes from the event ID, which is empty", events.ErrEventIncomplete)
	}
	return events.SocketModeEnvelopes(record, string(appID))
}
