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

type CursorStore interface {
	GetSocketModeCursor(context.Context, domain.AppID) (uint64, error)
	SetSocketModeCursor(context.Context, domain.AppID, uint64) error
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
	Store     ConnectionStore
	Events    EventSource
	Cursors   CursorStore
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
	var cursor uint64
	if h.Cursors != nil {
		cursor, err = h.Cursors.GetSocketModeCursor(r.Context(), connection.AppID)
		if err != nil {
			closeWith(websocket.CloseInternalServerErr, "cursor store unavailable")
			return
		}
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
			if h.Events == nil {
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
			if err := h.advanceCursor(r.Context(), connection.AppID, sequence, &cursor); err != nil {
				closeWith(websocket.CloseInternalServerErr, "cursor store unavailable")
				return
			}
		case <-ticker.C:
			if h.Events == nil {
				continue
			}
			if len(pending) != 0 {
				if time.Since(pendingSince) < envelopeTimeout {
					continue
				}
				// The app never answered. The cursor has not advanced, so
				// closing hands the envelope to the next connection instead of
				// stalling this app's stream with no error anywhere.
				h.logger().Warn("Socket Mode envelope was not acknowledged", "app", connection.AppID, "timeout", envelopeTimeout)
				closeWith(websocket.CloseTryAgainLater, "envelope was not acknowledged")
				return
			}
			records, err := h.Events.ListAppEventsAfter(r.Context(), connection.AppID, cursor, 1)
			if err != nil {
				closeWith(websocket.CloseInternalServerErr, "event source unavailable")
				return
			}
			if len(records) == 0 {
				continue
			}
			record := records[0]
			encoded, err := encodeEvent(record)
			if err != nil {
				// An internal worker record, or a payload written before the
				// typed payload contract, can never be delivered to an app.
				// Closing here would leave the cursor in place and reconnect
				// into the same record forever, so it is skipped durably and
				// reported instead.
				if errors.Is(err, events.ErrPayloadInternal) || errors.Is(err, events.ErrPayloadMalformed) || errors.Is(err, events.ErrPayloadRecipientScoped) {
					h.logger().Warn("Socket Mode skipped an undeliverable event", "app", connection.AppID, "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
					if cursorErr := h.advanceCursor(r.Context(), connection.AppID, record.Sequence, &cursor); cursorErr != nil {
						closeWith(websocket.CloseInternalServerErr, "cursor store unavailable")
						return
					}
					continue
				}
				closeWith(websocket.CloseInternalServerErr, "event payload is invalid")
				return
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
		}
	}
}

func servingHost(r *http.Request) string {
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return name
	}
	return r.Host
}

func (h Handler) advanceCursor(ctx context.Context, appID domain.AppID, sequence uint64, cursor *uint64) error {
	if sequence <= *cursor {
		return nil
	}
	if h.Cursors != nil {
		if err := h.Cursors.SetSocketModeCursor(ctx, appID, sequence); err != nil {
			return err
		}
	}
	*cursor = sequence
	return nil
}

// encodeEvent renders a durable record as a Socket Mode envelope. The payload
// is read through events.Broadcastable, so neither an internal worker record nor
// a record addressed to a single user can be sent to an app, and a payload that
// is not self-describing is refused with a sentinel the caller can act on.
func encodeEvent(record events.Record) (map[string]any, error) {
	if strings.TrimSpace(string(record.Event.ID)) == "" {
		return nil, fmt.Errorf("%w: Socket Mode event ID is required", events.ErrPayloadMalformed)
	}
	delivered, err := events.Broadcastable(record.Event)
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelope_id": string(record.Event.ID), "payload": delivered.Object, "type": "events_api", "accepts_response_payload": true}, nil
}
