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

type EventSource interface {
	ListAppEventsAfter(context.Context, domain.AppID, uint64, int) ([]events.Record, error)
}

// CursorStore is the legacy per-app delivery position.
//
// It is keyed on the application alone while domain.SocketModeConnectionLimit
// deliberately permits ten concurrent connections per app, so every connection
// seeds its own copy from the same row and every envelope is delivered once per
// open connection — ten times for an app that holds the limit open during a
// redeploy, which is what the limit exists for. Slack's Socket Mode contract
// delivers each envelope to exactly one connection. A deployment that provides
// an EnvelopeQueue gets that contract; this interface remains for one that has
// not migrated yet, and is retired with the store methods behind it.
type CursorStore interface {
	GetSocketModeCursor(context.Context, domain.AppID) (uint64, error)
	SetSocketModeCursor(context.Context, domain.AppID, uint64) error
}

// EnvelopeQueue gives each durable record to exactly one of an application's
// connections, using the same claim/lease/acknowledge model the response
// direction already uses. A shared cursor cannot express that: two connections
// sitting at the same position both read the same record, and the connection
// that acknowledges second writes a cursor that has already moved.
//
// The owner is the connection identifier, so a connection that disappears takes
// no envelope with it: its claim lapses with its lease and the next connection
// takes the record.
type EnvelopeQueue interface {
	// ClaimAppEvent leases the oldest record the application has not
	// acknowledged and no connection currently holds, and reports ok=false when
	// there is none. It applies the same visibility rules as
	// ListAppEventsAfter: the application's installed workspaces only, and never
	// an internal topic.
	ClaimAppEvent(ctx context.Context, appID domain.AppID, owner string, lease time.Duration) (record events.Record, ok bool, err error)
	// RenewAppEvent extends the lease the owner holds on a claimed record. It
	// fails with store.ErrLeaseConflict when the owner no longer holds it.
	RenewAppEvent(ctx context.Context, owner string, sequence uint64, lease time.Duration) error
	// AckAppEvent records that the application is finished with the record, so
	// no connection is offered it again.
	AckAppEvent(ctx context.Context, owner string, sequence uint64) error
	// ReleaseAppEvent returns a claimed record to the queue, claimable again at
	// retryAt, so another connection takes it instead of waiting out the lease.
	ReleaseAppEvent(ctx context.Context, owner string, sequence uint64, retryAt time.Time) error
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
	// Events and Cursors are the shared-cursor delivery path. Envelopes replaces
	// both: when it is set it is used, and each envelope reaches exactly one of
	// the application's connections.
	Events    EventSource
	Cursors   CursorStore
	Envelopes EnvelopeQueue
	Responses ResponseSink
	Upgrader  websocket.Upgrader
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
	delivery, err := h.newDelivery(r.Context(), connection)
	if err != nil {
		closeWith(websocket.CloseInternalServerErr, "event delivery state unavailable")
		return
	}
	if delivery != nil {
		// A connection that goes away must not take an envelope with it. Handing
		// the claim back here means the next connection sees it immediately
		// rather than after the lease lapses.
		defer func() {
			if abandonErr := delivery.abandon(context.Background()); abandonErr != nil {
				h.logger().Error("Socket Mode envelope was not returned to the queue", "connection", connection.ID, "app", connection.AppID, "error", abandonErr)
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
	pending := make(map[string]uint64, 1)
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
			if delivery == nil {
				if err := writeJSON(map[string]string{"envelope_id": envelope.EnvelopeID}); err != nil {
					return
				}
				continue
			}
			sequence, exists := pending[envelope.EnvelopeID]
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
			if err := delivery.consume(r.Context(), sequence); err != nil {
				closeWith(deliveryCloseCode(err))
				return
			}
		case <-ticker.C:
			if delivery == nil {
				continue
			}
			if len(pending) != 0 {
				if time.Since(pendingSince) < envelopeTimeout {
					continue
				}
				// The app never answered. The record is handed back rather than
				// acknowledged, so the next connection receives it instead of
				// this app's stream stalling with no error anywhere.
				h.logger().Warn("Socket Mode envelope was not acknowledged", "app", connection.AppID, "connection", connection.ID, "timeout", envelopeTimeout)
				closeWith(websocket.CloseTryAgainLater, "envelope was not acknowledged")
				return
			}
			record, ok, err := delivery.next(r.Context())
			if err != nil {
				closeWith(websocket.CloseInternalServerErr, "event source unavailable")
				return
			}
			if !ok {
				continue
			}
			encoded, err := encodeEvent(record)
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
				if errors.Is(err, events.ErrEventIncomplete) {
					h.logger().Error("Socket Mode dropped a record with no event ID", "app", connection.AppID, "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
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
			if err := writeJSON(encoded); err != nil {
				return
			}
			pending[string(record.Event.ID)] = record.Sequence
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
			if delivery != nil {
				if err := delivery.renew(r.Context()); err != nil {
					// The claim is gone, so the envelope in flight belongs to
					// another connection now. Continuing would let both deliver
					// it.
					h.logger().Warn("Socket Mode lost the lease on an envelope in flight", "app", connection.AppID, "connection", connection.ID, "error", err)
					closeWith(deliveryCloseCode(err))
					return
				}
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

// envelopeDelivery is how one connection obtains the next record and records
// that the application is finished with it. It exists so the delivery loop does
// not have to know whether ownership is per envelope or shared.
type envelopeDelivery interface {
	// next returns the record this connection should deliver, if any.
	next(ctx context.Context) (events.Record, bool, error)
	// consume records that the application is finished with the record, whether
	// it acknowledged the envelope or the record was undeliverable.
	consume(ctx context.Context, sequence uint64) error
	// renew extends this connection's hold on the record in flight.
	renew(ctx context.Context) error
	// abandon returns the record in flight to the queue for another connection.
	abandon(ctx context.Context) error
}

// envelopeLease has to outlast envelopeTimeout, or the claim lapses while the
// handler is still waiting for the application to acknowledge the envelope it
// was sent, and a second connection is handed a record that is already in
// flight. It is renewed on the connection lease tick.
const envelopeLease = 2 * envelopeTimeout

func (h Handler) newDelivery(ctx context.Context, connection domain.SocketModeConnection) (envelopeDelivery, error) {
	if h.Envelopes != nil {
		return &claimDelivery{queue: h.Envelopes, appID: connection.AppID, owner: connection.ID}, nil
	}
	if h.Events == nil {
		return nil, nil
	}
	delivery := &cursorDelivery{events: h.Events, cursors: h.Cursors, appID: connection.AppID}
	if h.Cursors != nil {
		cursor, err := h.Cursors.GetSocketModeCursor(ctx, connection.AppID)
		if err != nil {
			return nil, err
		}
		delivery.cursor = cursor
	}
	return delivery, nil
}

// claimDelivery gives the record to exactly one connection: the claim is the
// ownership, and it is keyed on this connection rather than on the application.
type claimDelivery struct {
	queue EnvelopeQueue
	appID domain.AppID
	owner string
	held  uint64
}

func (d *claimDelivery) next(ctx context.Context) (events.Record, bool, error) {
	record, ok, err := d.queue.ClaimAppEvent(ctx, d.appID, d.owner, envelopeLease)
	if err != nil || !ok {
		return events.Record{}, false, err
	}
	d.held = record.Sequence
	return record, true, nil
}

func (d *claimDelivery) consume(ctx context.Context, sequence uint64) error {
	if err := d.queue.AckAppEvent(ctx, d.owner, sequence); err != nil {
		return err
	}
	if d.held == sequence {
		d.held = 0
	}
	return nil
}

func (d *claimDelivery) renew(ctx context.Context) error {
	if d.held == 0 {
		return nil
	}
	return d.queue.RenewAppEvent(ctx, d.owner, d.held, envelopeLease)
}

func (d *claimDelivery) abandon(ctx context.Context) error {
	if d.held == 0 {
		return nil
	}
	sequence := d.held
	d.held = 0
	return d.queue.ReleaseAppEvent(ctx, d.owner, sequence, time.Now().UTC())
}

// cursorDelivery is the shared per-application position. Every connection of an
// application reads and writes the same row, so each of them delivers every
// envelope; see CursorStore.
type cursorDelivery struct {
	events  EventSource
	cursors CursorStore
	appID   domain.AppID
	cursor  uint64
}

func (d *cursorDelivery) next(ctx context.Context) (events.Record, bool, error) {
	records, err := d.events.ListAppEventsAfter(ctx, d.appID, d.cursor, 1)
	if err != nil || len(records) == 0 {
		return events.Record{}, false, err
	}
	return records[0], true, nil
}

func (d *cursorDelivery) consume(ctx context.Context, sequence uint64) error {
	if sequence <= d.cursor {
		return nil
	}
	if d.cursors != nil {
		if err := d.cursors.SetSocketModeCursor(ctx, d.appID, sequence); err != nil {
			return err
		}
	}
	d.cursor = sequence
	return nil
}

func (d *cursorDelivery) renew(context.Context) error { return nil }

func (d *cursorDelivery) abandon(context.Context) error { return nil }

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

// encodeEvent renders a durable record as a Socket Mode envelope. The payload
// is read through events.Broadcastable, so neither an internal worker record nor
// a record addressed to a single user can be sent to an app, and a payload that
// is not self-describing is refused with a sentinel the caller can act on.
func encodeEvent(record events.Record) (map[string]any, error) {
	// A missing identifier is a defect in the record's identity, not in its
	// payload: the payload may be a perfectly deliverable event. Classifying it
	// as a malformed payload made it indistinguishable from an undeliverable one
	// everywhere the two are handled together, including the outbox worker's
	// drop-and-acknowledge path.
	if strings.TrimSpace(string(record.Event.ID)) == "" {
		return nil, fmt.Errorf("%w: Socket Mode envelope ID comes from the event ID, which is empty", events.ErrEventIncomplete)
	}
	delivered, err := events.Broadcastable(record.Event)
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelope_id": string(record.Event.ID), "payload": delivered.Object, "type": "events_api", "accepts_response_payload": true}, nil
}
