package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mux.Handle("/rtm", websocket.Handler(h.rtmWebSocket))
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
			return
		}
		for _, record := range records {
			after = record.Sequence
			if record.Event.Topic == events.EphemeralMessageTopic {
				recipientMatches, recipientErr := eventRecipient(record.Event.Payload, connection.UserID)
				if recipientErr != nil {
					return
				}
				if !recipientMatches {
					continue
				}
			}
			payload, encodeErr := encodeRTMEvent(record)
			if encodeErr != nil || websocket.Message.Send(conn, string(payload)) != nil {
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

func eventRecipient(payload string, recipient domain.UserID) (bool, error) {
	var value struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return false, err
	}
	if value.UserID == "" {
		return false, errors.New("ephemeral event recipient is required")
	}
	return value.UserID == string(recipient), nil
}

func encodeRTMEvent(record events.Record) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(record.Event.Payload), &object); err == nil && object != nil {
		if _, exists := object["type"]; !exists {
			encodedType, encodeErr := json.Marshal(record.Event.Topic)
			if encodeErr != nil {
				return nil, encodeErr
			}
			object["type"] = encodedType
		}
		return json.Marshal(object)
	}
	return json.Marshal(map[string]string{"type": record.Event.Topic, "data": record.Event.Payload})
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
	w.Header().Set("Connection", "keep-alive")
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := h.Source.ListEventsAfter(r.Context(), h.Workspace, after, 100)
		if err != nil {
			return
		}
		for _, record := range records {
			if err := writeEvent(w, record, principal.UserID); err != nil {
				return
			}
			after = record.Sequence
		}
		if len(records) > 0 {
			flusher.Flush()
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

func writeEvent(w http.ResponseWriter, record events.Record, recipient domain.UserID) error {
	if record.Event.Topic == events.EphemeralMessageTopic {
		var payload struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal([]byte(record.Event.Payload), &payload); err != nil || payload.UserID == "" {
			if err != nil {
				return err
			}
			return errors.New("ephemeral event recipient is required")
		}
		if payload.UserID != string(recipient) {
			return nil
		}
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\n", record.Sequence, record.Event.Topic); err != nil {
		return err
	}
	for _, line := range strings.Split(record.Event.Payload, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err
}
