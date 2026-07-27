package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"golang.org/x/net/websocket"
)

type Handler struct {
	Source         events.Source
	Workspace      domain.WorkspaceID
	Authenticator  auth.Authenticator
	RTMConnections RTMConnectionSource
	Messages       RTMMessageService
	// Logger records why a stream ended and which records were skipped.
	// Without it a store outage is indistinguishable from a client disconnect.
	Logger *slog.Logger
	// Heartbeat is how often a quiet stream emits a comment so intermediaries
	// do not treat it as idle. Zero selects heartbeatInterval, which is chosen
	// against the common 60 s proxy default.
	Heartbeat time.Duration
	// Reauthorize is how often an open stream re-checks that its session is
	// still valid. Zero selects reauthorizeInterval.
	Reauthorize time.Duration
}

func (h Handler) heartbeat() time.Duration {
	if h.Heartbeat > 0 {
		return h.Heartbeat
	}
	return heartbeatInterval
}

func (h Handler) reauthorize() time.Duration {
	if h.Reauthorize > 0 {
		return h.Reauthorize
	}
	return reauthorizeInterval
}

const (
	streamPollInterval = 250 * time.Millisecond
	// heartbeatInterval keeps a quiet stream producing bytes well inside the
	// 60 s idle timeout that nginx, AWS ALB and Cloud Run all default to.
	// Without it an idle stream is severed roughly every minute.
	heartbeatInterval = 15 * time.Second
	// reauthorizeInterval bounds how long a revoked session keeps receiving
	// events. The check is the same store lookup every other request performs.
	reauthorizeInterval = 5 * time.Second
	// clientRetryInterval is advertised to the browser as its reconnect delay,
	// so the cadence is a deployment decision rather than a browser default.
	clientRetryInterval = 2 * time.Second
)

func (h Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

type RTMConnectionSource interface {
	ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error)
}

type RTMMessageService interface {
	Post(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, domain.MessageTimestamp, string) (domain.Message, error)
}

const maxRTMMessageBytes = 16 << 10

var errUnsupportedRTMCommand = errors.New("unsupported RTM command")

func NewHandler(source events.Source, workspace domain.WorkspaceID, authenticator auth.Authenticator) (Handler, error) {
	if source == nil {
		return Handler{}, errors.New("SSE requires an event source")
	}
	if workspace == "" {
		return Handler{}, errors.New("SSE requires a workspace")
	}
	if authenticator == nil {
		return Handler{}, errors.New("SSE requires an authenticator")
	}
	return Handler{Source: source, Workspace: workspace, Authenticator: authenticator}, nil
}

func NewRTMHandler(source events.Source, workspace domain.WorkspaceID, connections RTMConnectionSource, messages RTMMessageService) (Handler, error) {
	if source == nil {
		return Handler{}, errors.New("RTM requires an event source")
	}
	if workspace == "" {
		return Handler{}, errors.New("RTM requires a workspace")
	}
	if connections == nil {
		return Handler{}, errors.New("RTM requires a connection store")
	}
	if messages == nil {
		return Handler{}, errors.New("RTM requires a message service")
	}
	return Handler{Source: source, Workspace: workspace, RTMConnections: connections, Messages: messages}, nil
}

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /events", h.events)
}

func (h Handler) RegisterRTM(mux *http.ServeMux) {
	mux.Handle("/rtm", websocket.Server{
		Handler: websocket.Handler(h.rtmWebSocket),
		Handshake: func(*websocket.Config, *http.Request) error {
			return nil
		},
	})
}

func (h Handler) events(w http.ResponseWriter, r *http.Request) {
	h.stream(w, r, auth.ScopeChannelsHistory)
}

func (h Handler) rtmWebSocket(conn *websocket.Conn) {
	request := conn.Request()
	conn.MaxPayloadBytes = maxRTMMessageBytes
	if h.RTMConnections == nil {
		_ = websocket.Message.Send(conn, `{"type":"error","error":{"code":1,"msg":"invalid_auth"}}`)
		return
	}
	connectionID := strings.TrimSpace(request.URL.Query().Get("session_id"))
	connection, err := h.RTMConnections.ConsumeRTMConnection(request.Context(), connectionID)
	if err != nil || connection.WorkspaceID != h.Workspace {
		_ = websocket.Message.Send(conn, `{"type":"error","error":{"code":1,"msg":"invalid_auth"}}`)
		return
	}
	if err := websocket.Message.Send(conn, `{"type":"hello"}`); err != nil {
		return
	}
	after, err := lastEventID(request)
	if err != nil {
		_ = websocket.Message.Send(conn, `{"type":"error","error":{"code":3,"msg":"invalid_event_cursor"}}`)
		return
	}
	commands := make(chan string)
	readerDone := make(chan error, 1)
	go func() {
		for {
			var message string
			if receiveErr := websocket.Message.Receive(conn, &message); receiveErr != nil {
				readerDone <- receiveErr
				return
			}
			select {
			case commands <- message:
			case <-request.Context().Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, listErr := h.Source.ListEventsAfter(request.Context(), h.Workspace, after, 100)
		if listErr != nil {
			if request.Context().Err() == nil {
				h.logger().Error("RTM stream ended on an event source failure", "workspace", h.Workspace, "user", connection.UserID, "error", listErr)
			}
			return
		}
		for _, record := range records {
			after = record.Sequence
			// The durable journal also carries internal worker records and
			// records written before the typed payload contract; neither is a
			// deliverable event, and neither may end the stream.
			delivered, decodeErr := events.Deliverable(record.Event)
			if decodeErr != nil {
				continue
			}
			if !addressedTo(record.Event.Topic, delivered, connection.UserID) {
				continue
			}
			payload, encodeErr := delivered.Encode()
			if encodeErr != nil {
				continue
			}
			if websocket.Message.Send(conn, payload) != nil {
				return
			}
		}
		select {
		case <-request.Context().Done():
			return
		case <-readerDone:
			return
		case message := <-commands:
			if err := handleRTMCommand(request.Context(), conn, connection, h.Messages, message); err != nil {
				return
			}
		case <-ticker.C:
		}
	}
}

func handleRTMCommand(ctx context.Context, conn *websocket.Conn, connection domain.RTMConnection, messages RTMMessageService, raw string) error {
	var command struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &command); err != nil || strings.TrimSpace(command.Type) == "" {
		return websocket.Message.Send(conn, `{"type":"error","error":{"code":4,"msg":"invalid_message"}}`)
	}
	switch command.Type {
	case "ping":
		payload, err := encodeRTMPong(raw)
		if err != nil {
			return websocket.Message.Send(conn, `{"type":"error","error":{"code":4,"msg":"invalid_message"}}`)
		}
		return websocket.Message.Send(conn, string(payload))
	case "message":
		payload, err := encodeRTMMessage(ctx, connection, messages, raw)
		if err != nil {
			if commandID := rtmCommandID(raw); commandID > 0 {
				message := err.Error()
				if errors.Is(err, errRTMMessageFailed) {
					message = "message_failed"
				}
				return sendRTMMessageError(conn, commandID, message)
			}
			if errors.Is(err, errRTMMessageFailed) {
				return websocket.Message.Send(conn, `{"ok":false,"error":{"code":2,"msg":"message_failed"}}`)
			}
			return websocket.Message.Send(conn, `{"type":"error","error":{"code":4,"msg":"invalid_message"}}`)
		}
		return websocket.Message.Send(conn, string(payload))
	default:
		return websocket.Message.Send(conn, `{"type":"error","error":{"code":5,"msg":"unsupported_message"}}`)
	}
}

var errRTMMessageFailed = errors.New("RTM message failed")

func encodeRTMMessage(ctx context.Context, connection domain.RTMConnection, messages RTMMessageService, raw string) ([]byte, error) {
	var command struct {
		ID       int64  `json:"id"`
		Type     string `json:"type"`
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := json.Unmarshal([]byte(raw), &command); err != nil || command.Type != "message" || command.ID <= 0 {
		return nil, errors.New("invalid RTM message")
	}
	channel := strings.TrimSpace(command.Channel)
	if channel == "" {
		return nil, errors.New("message channel is missing")
	}
	if strings.TrimSpace(command.Text) == "" {
		return nil, errors.New("message text is missing")
	}
	if messages == nil {
		return nil, errors.New("message service is missing")
	}
	posted, err := messages.Post(ctx, connection.WorkspaceID, connection.UserID, domain.ConversationID(channel), command.Text, domain.MessageTimestamp(strings.TrimSpace(command.ThreadTS)), "")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRTMMessageFailed, err)
	}
	return json.Marshal(map[string]any{
		"ok":       true,
		"reply_to": command.ID,
		"ts":       domain.NewMessageTimestamp(posted.CreatedAt),
		"text":     posted.Text,
	})
}

func rtmCommandID(raw string) int64 {
	var command struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		return 0
	}
	return command.ID
}

func sendRTMMessageError(conn *websocket.Conn, id int64, message string) error {
	payload, err := json.Marshal(map[string]any{
		"ok":       false,
		"reply_to": id,
		"error": map[string]any{
			"code": 2,
			"msg":  message,
		},
	})
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(payload))
}

func encodeRTMPong(message string) ([]byte, error) {
	var command struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(message), &command); err != nil || strings.TrimSpace(command.Type) == "" {
		return nil, errors.New("invalid RTM command")
	}
	if command.Type != "ping" {
		return nil, errUnsupportedRTMCommand
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &fields); err != nil {
		return nil, errors.New("invalid RTM command")
	}
	pong := map[string]json.RawMessage{"type": json.RawMessage(`"pong"`)}
	if rawID, exists := fields["id"]; exists {
		var id int64
		if err := json.Unmarshal(rawID, &id); err != nil || id <= 0 {
			return nil, errors.New("RTM ping id must be a positive integer")
		}
		pong["reply_to"] = rawID
	}
	for name, raw := range fields {
		if name == "type" || name == "id" {
			continue
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid RTM ping field")
		}
		switch value.(type) {
		case string, bool, json.Number:
			pong[name] = raw
		default:
			return nil, errors.New("RTM ping fields must be scalar")
		}
	}
	return json.Marshal(pong)
}

func (h Handler) stream(w http.ResponseWriter, r *http.Request, scope auth.Scope) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if principal.WorkspaceID != h.Workspace || !principal.HasScope(scope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	after, err := lastEventID(r)
	if err != nil {
		http.Error(w, "invalid event cursor", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// nginx buffers proxied responses by default, which withholds a stream's
	// bytes until its buffer fills. Connection: keep-alive was meaningless
	// here: HTTP/1.1 is persistent by default and HTTP/2 forbids the header.
	w.Header().Set("X-Accel-Buffering", "no")
	if _, err := fmt.Fprintf(w, "retry: %d\n\n: connected\n\n", clientRetryInterval.Milliseconds()); err != nil {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	lastWrite := time.Now()
	lastAuthorized := time.Now()
	for {
		records, err := h.Source.ListEventsAfter(r.Context(), h.Workspace, after, 100)
		if err != nil {
			if r.Context().Err() == nil {
				h.logger().Error("event stream ended on an event source failure", "workspace", h.Workspace, "user", principal.UserID, "error", err)
			}
			return
		}
		wrote := false
		for _, record := range records {
			after = record.Sequence
			delivered, decodeErr := events.Deliverable(record.Event)
			if decodeErr != nil {
				// One undeliverable record must not end a durable stream: the
				// client would reconnect with the same cursor, land on the same
				// record and break the stream for every user in the workspace.
				h.logger().Warn("event stream skipped an undeliverable record", "workspace", h.Workspace, "sequence", record.Sequence, "topic", record.Event.Topic, "error", decodeErr)
				continue
			}
			if !addressedTo(record.Event.Topic, delivered, principal.UserID) {
				continue
			}
			if err := writeEvent(w, record, delivered); err != nil {
				if r.Context().Err() == nil {
					h.logger().Error("event stream ended on a write failure", "workspace", h.Workspace, "user", principal.UserID, "error", err)
				}
				return
			}
			wrote = true
		}
		if wrote {
			flusher.Flush()
			lastWrite = time.Now()
		}
		if time.Since(lastWrite) >= h.heartbeat() {
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			lastWrite = time.Now()
		}
		if time.Since(lastAuthorized) >= h.reauthorize() {
			// A stream that authenticated once outlives its own session:
			// sign-out, back-channel logout and administrative revocation all
			// leave it delivering every workspace event indefinitely.
			current, authErr := h.Authenticator.Authenticate(r)
			if authErr != nil || current.WorkspaceID != h.Workspace || !current.HasScope(scope) {
				h.logger().Info("event stream ended because the session is no longer authorized", "workspace", h.Workspace, "user", principal.UserID)
				return
			}
			lastAuthorized = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func lastEventID(r *http.Request) (uint64, error) {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

// addressedTo reports whether a decoded record may be shown to one recipient.
// A recipient-scoped topic whose payload names no recipient is addressed to
// nobody, so it is withheld rather than broadcast or treated as a stream error.
func addressedTo(topic string, delivered events.Delivered, recipient domain.UserID) bool {
	if topic != events.EphemeralMessageTopic {
		return true
	}
	userID, ok := delivered.Field("user_id")
	return ok && userID != "" && userID == string(recipient)
}

func writeEvent(w io.Writer, record events.Record, delivered events.Delivered) error {
	encoded, err := delivered.Encode()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\n", record.Sequence, record.Event.Topic); err != nil {
		return err
	}
	for _, line := range strings.Split(encoded, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = fmt.Fprint(w, "\n")
	return err
}
