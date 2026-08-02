package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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
	// WriteTimeout bounds one write to one client. Zero selects writeTimeout.
	WriteTimeout time.Duration
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

func (h Handler) writeTimeout() time.Duration {
	if h.WriteTimeout > 0 {
		return h.WriteTimeout
	}
	return writeTimeout
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
	// writeTimeout bounds one write to one client. The listener deliberately sets
	// no WriteTimeout because it also serves this stream, and delegates the
	// per-response deadline here; without one, a client that stops reading fills
	// the socket buffer and parks this handler inside Write for as long as the
	// kernel keeps the connection. IdleTimeout does not apply, because the
	// response is in flight rather than idle, and the heartbeat guarantees the
	// handler keeps writing to a suspended tab even in a silent workspace.
	writeTimeout = 10 * time.Second
	// unresolvedReauthorizations bounds how many consecutive re-authorization
	// checks may fail to reach a verdict before the stream is closed anyway. A
	// session store that cannot answer must not disconnect every live stream, but
	// it must not keep an unverifiable session streaming forever either.
	unresolvedReauthorizations = 3
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

type RTMUserEventSource interface {
	ListUserEventsAfter(context.Context, domain.WorkspaceID, domain.UserID, uint64, int) ([]events.Record, error)
}

const maxRTMMessageBytes = 16 << 10

var errUnsupportedRTMCommand = errors.New("unsupported RTM command")

// NewHandler and NewRTMHandler no longer take a workspace. Each stream reads
// the workspace from the credential that opened it — the session principal for
// SSE, the connection ticket for RTM — so one process serves every workspace a
// reader may switch into. A construction-time workspace both decided nothing
// once the streams followed their credentials and, while it did decide, made a
// switch unserviceable from the same process.
func NewHandler(source events.Source, authenticator auth.Authenticator) (Handler, error) {
	if source == nil {
		return Handler{}, errors.New("SSE requires an event source")
	}
	if authenticator == nil {
		return Handler{}, errors.New("SSE requires an authenticator")
	}
	return Handler{Source: source, Authenticator: authenticator}, nil
}

func NewRTMHandler(source events.Source, connections RTMConnectionSource, messages RTMMessageService) (Handler, error) {
	if source == nil {
		return Handler{}, errors.New("RTM requires an event source")
	}
	if connections == nil {
		return Handler{}, errors.New("RTM requires a connection store")
	}
	if messages == nil {
		return Handler{}, errors.New("RTM requires a message service")
	}
	return Handler{Source: source, RTMConnections: connections, Messages: messages}, nil
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
	if err != nil || connection.WorkspaceID == "" {
		_ = websocket.Message.Send(conn, `{"type":"error","error":{"code":1,"msg":"invalid_auth"}}`)
		return
	}
	// The stream follows the connection's workspace, for the same reason the
	// SSE stream follows the principal's: the ticket already names which
	// workspace it was issued for, and a construction-time workspace bound
	// every connection in the process to one of them.
	workspace := connection.WorkspaceID
	if err := websocket.Message.Send(conn, `{"type":"hello"}`); err != nil {
		return
	}
	// The ticket's cursor is where this stream starts. An explicit
	// last_event_id still wins, because a client that knows where it left off
	// is a better authority than the ticket — but an official RTM client sends
	// none and has no argument to pass one, which is exactly why the ticket
	// carries it. Before it did, that client resumed at zero and was sent the
	// whole workspace journal as live events on every connect.
	after := connection.Cursor
	requested, err := lastEventID(request)
	if err != nil {
		_ = websocket.Message.Send(conn, `{"type":"error","error":{"code":3,"msg":"invalid_event_cursor"}}`)
		return
	}
	if requested > 0 {
		after = requested
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
	// goodbye is Slack's RTM farewell frame: the server announces an
	// intentional end of the stream so an official client reconnects instead
	// of treating the close as a failure. Best effort by design — the peer
	// that is already gone cannot be told goodbye.
	sayGoodbye := func() { _ = websocket.Message.Send(conn, `{"type":"goodbye"}`) }
	for {
		var records []events.Record
		var listErr error
		if projected, ok := h.Source.(RTMUserEventSource); ok {
			records, listErr = projected.ListUserEventsAfter(request.Context(), workspace, connection.UserID, after, 100)
		} else {
			records, listErr = h.Source.ListEventsAfter(request.Context(), workspace, after, 100)
		}
		if listErr != nil {
			if request.Context().Err() == nil {
				h.logger().Error("RTM stream ended on an event source failure", "workspace", workspace, "user", connection.UserID, "error", listErr)
			}
			sayGoodbye()
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
			// The frame is the bare Slack inner event, built by the one
			// translation site. This loop used to send the durable payload
			// verbatim, so an official RTM client received {"type":"message.created",
			// "message_id":…} and its "message" listener never fired: the client
			// emits by the event's type, and our topics are not Slack event
			// names. A record whose topic maps to no Slack event yields no frame
			// and is stepped over, because an unparseable frame is not delivery.
			inners, translateErr := events.RTMEvents(record.Event.Topic, delivered)
			if translateErr != nil {
				h.logger().Warn("RTM skipped a record whose payload cannot fill its Slack event", "workspace", workspace, "sequence", record.Sequence, "topic", record.Event.Topic, "error", translateErr)
				continue
			}
			for _, inner := range inners {
				payload, encodeErr := inner.Encode()
				if encodeErr != nil {
					h.logger().Warn("RTM skipped a record it could not encode", "workspace", workspace, "sequence", record.Sequence, "topic", record.Event.Topic, "error", encodeErr)
					continue
				}
				if websocket.Message.Send(conn, payload) != nil {
					return
				}
			}
		}
		select {
		case <-request.Context().Done():
			sayGoodbye()
			return
		case <-readerDone:
			sayGoodbye()
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
	if !principal.HasScope(scope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// The stream follows the principal's workspace rather than the one this
	// handler was constructed with. A session is issued for exactly one
	// workspace, so the principal already names which events it may see; the
	// construction-time workspace bound every reader to one, which is what
	// made a workspace switch impossible to serve from the same process.
	workspace := principal.WorkspaceID
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
	control := http.NewResponseController(w)
	if err := h.armWrite(control); err != nil {
		h.logger().Error("event stream could not set a write deadline", "workspace", workspace, "user", principal.UserID, "error", err)
		http.Error(w, "streaming is unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, err := fmt.Fprintf(w, "retry: %d\n\n: connected\n\n", clientRetryInterval.Milliseconds()); err != nil {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	lastWrite := time.Now()
	lastAuthorized := time.Now()
	unresolved := 0
	for {
		records, err := h.Source.ListEventsAfter(r.Context(), workspace, after, 100)
		if err != nil {
			if r.Context().Err() == nil {
				h.logger().Error("event stream ended on an event source failure", "workspace", workspace, "user", principal.UserID, "error", err)
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
				h.logger().Warn("event stream skipped an undeliverable record", "workspace", workspace, "sequence", record.Sequence, "topic", record.Event.Topic, "error", decodeErr)
				continue
			}
			if !addressedTo(record.Event.Topic, delivered, principal.UserID) {
				continue
			}
			// A record that cannot be re-encoded is a defect in the record, not in
			// the client, and the RTM loop already skips it. Encoding here rather
			// than inside the write keeps writeEvent's errors purely transport
			// errors, so one corrupt record cannot end an SSE stream that RTM
			// survives, and the log line names the record instead of blaming the
			// reader.
			encoded, encodeErr := delivered.Encode()
			if encodeErr != nil {
				h.logger().Warn("event stream skipped a record it could not re-encode", "workspace", workspace, "sequence", record.Sequence, "topic", record.Event.Topic, "error", encodeErr)
				continue
			}
			if err := h.armWrite(control); err != nil {
				return
			}
			if err := writeEvent(w, record.Sequence, record.Event.Topic, encoded); err != nil {
				h.reportWriteFailure(r, workspace, principal.UserID, err)
				return
			}
			wrote = true
		}
		if wrote {
			flusher.Flush()
			lastWrite = time.Now()
		}
		if time.Since(lastWrite) >= h.heartbeat() {
			if err := h.armWrite(control); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				h.reportWriteFailure(r, workspace, principal.UserID, err)
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
			switch {
			// The re-check is against the workspace this stream started in, not
			// the handler's: a session switched to another workspace is a
			// different session, and this one must end rather than quietly
			// start delivering somewhere else.
			case authErr == nil && current.WorkspaceID == workspace && current.HasScope(scope):
				lastAuthorized = time.Now()
				unresolved = 0
			case authErr == nil || credentialWithdrawn(authErr):
				h.logger().Info("event stream ended because the session is no longer authorized", "workspace", workspace, "user", principal.UserID)
				return
			default:
				// The check did not reach a verdict: the session store did not
				// answer (auth.ErrCredentialStoreUnavailable). Ending the stream
				// here turns a sub-second store failover into a full
				// live-delivery outage: every stream in the workspace ends
				// within one re-authorization interval, blames the user's
				// session in the log, and reconnects together at the advertised
				// retry delay.
				unresolved++
				if unresolved >= unresolvedReauthorizations {
					h.logger().Warn("event stream ended after repeated inconclusive re-authorization", "workspace", workspace, "user", principal.UserID, "attempts", unresolved, "error", authErr)
					return
				}
				h.logger().Warn("event stream could not confirm its session and kept the stream open", "workspace", workspace, "user", principal.UserID, "attempts", unresolved, "error", authErr)
				lastAuthorized = time.Now()
			}
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

// armWrite bounds the next write to this client. A ResponseWriter that cannot
// carry a deadline — the in-memory recorder a test uses — reports
// http.ErrNotSupported, which is not a stream failure.
func (h Handler) armWrite(control *http.ResponseController) error {
	if control == nil {
		return nil
	}
	if err := control.SetWriteDeadline(time.Now().Add(h.writeTimeout())); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// reportWriteFailure separates a reader that stopped reading from a reader that
// went away. A stalled consumer is a capacity fact an operator has to be able to
// see: it holds a goroutine, a connection and a poll buffer until the deadline
// fires.
func (h Handler) reportWriteFailure(r *http.Request, workspace domain.WorkspaceID, user domain.UserID, err error) {
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		h.logger().Warn("event stream dropped a consumer that stopped reading", "workspace", workspace, "user", user, "timeout", h.writeTimeout())
		return
	}
	h.logger().Error("event stream ended on a write failure", "workspace", workspace, "user", user, "error", err)
}

// credentialWithdrawn reports whether a re-authorization failure means the
// credential is gone rather than that the session store could not answer.
//
// Every authentication sentinel wraps auth.ErrNotAuthenticated, and each one
// is a verdict the store delivered: no cookie, a session looked up and found
// revoked, an account with nothing behind it, or a session the store answered
// "not found" for. An unreachable store is auth.ErrCredentialStoreUnavailable,
// which deliberately does not wrap the class — so the class itself is the
// predicate. ErrInvalidToken used to be excluded here because auth.Browser
// collapsed outages onto it, which kept a signed-out user's stream open for
// three grace intervals; that ambiguity is gone.
func credentialWithdrawn(err error) bool {
	return errors.Is(err, auth.ErrNotAuthenticated)
}

// addressedTo reports whether a decoded record may be shown to one recipient.
// A recipient-scoped topic whose payload names no recipient is addressed to
// nobody, so it is withheld rather than broadcast or treated as a stream error.
//
// The predicate is asked rather than one topic being named here: a second
// recipient-scoped topic added to events.RecipientScoped would otherwise be
// refused correctly by Socket Mode and the webhook and broadcast to every member
// of the workspace by this stream and by RTM.
//
// It asks about both names the record carries. Topic and the payload's "type"
// are separate storage columns and separate wire fields, and the seam codec
// rebuilds a record from them independently, so a record whose topic is
// message.created can carry a payload that is addressed to exactly one user.
// Reading the topic alone made that record look addressed to nobody, and this
// stream wrote one user's text to every subscriber in the workspace.
func addressedTo(topic string, delivered events.Delivered, recipient domain.UserID) bool {
	if !events.AddressedToOneRecipient(topic, delivered) {
		return true
	}
	userID, ok := delivered.Field("user_id")
	return ok && userID != "" && userID == string(recipient)
}

func writeEvent(w io.Writer, sequence uint64, topic, encoded string) error {
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\n", sequence, topic); err != nil {
		return err
	}
	for _, line := range strings.Split(encoded, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err
}
