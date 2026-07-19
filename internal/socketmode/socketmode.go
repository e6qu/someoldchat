package socketmode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

const (
	connectionLifetime = 30 * time.Second
	maxEnvelopeBytes   = 1 << 20
)

var ErrInvalidAppID = errors.New("Socket Mode app ID is required")

type ConnectionStore interface {
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
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
	id, err := domain.NewSocketModeConnectionID()
	if err != nil {
		return OpenResult{}, err
	}
	connection := domain.SocketModeConnection{ID: id, AppID: appID, ExpiresAt: time.Now().UTC().Add(connectionLifetime)}
	if err := s.Store.CreateSocketModeConnection(ctx, connection); err != nil {
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
		http.Error(w, "connection is invalid or expired", http.StatusUnauthorized)
		return
	}
	upgrader := h.Upgrader
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxEnvelopeBytes)
	var cursor uint64
	if h.Cursors != nil {
		cursor, err = h.Cursors.GetSocketModeCursor(r.Context(), connection.AppID)
		if err != nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "cursor store unavailable"), time.Now().Add(time.Second))
			return
		}
	}
	if err := conn.WriteJSON(map[string]any{"type": "hello", "num_connections": 1, "debug_info": map[string]string{"host": string(connection.AppID)}}); err != nil {
		return
	}
	readErrors := make(chan error, 1)
	readMessages := make(chan []byte, 1)
	go func() {
		for {
			messageType, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				readErrors <- readErr
				return
			}
			if messageType != websocket.TextMessage {
				readErrors <- errors.New("Socket Mode requires text messages")
				return
			}
			readMessages <- payload
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	pending := make(map[string]uint64, 1)
	for {
		select {
		case err := <-readErrors:
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, err.Error()), time.Now().Add(time.Second))
			return
		case payload := <-readMessages:
			var envelope struct {
				EnvelopeID string          `json:"envelope_id"`
				Payload    json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil || strings.TrimSpace(envelope.EnvelopeID) == "" {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, "envelope_id is required"), time.Now().Add(time.Second))
				return
			}
			if h.Events == nil {
				if err := conn.WriteJSON(map[string]string{"envelope_id": envelope.EnvelopeID}); err != nil {
					return
				}
				continue
			}
			sequence, exists := pending[envelope.EnvelopeID]
			if !exists {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, "unknown envelope_id"), time.Now().Add(time.Second))
				return
			}
			if len(envelope.Payload) != 0 {
				var responsePayload map[string]json.RawMessage
				if json.Unmarshal(envelope.Payload, &responsePayload) != nil || responsePayload == nil {
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, "response payload must be a JSON object"), time.Now().Add(time.Second))
					return
				}
				if h.Responses == nil {
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "response payload routing is unavailable"), time.Now().Add(time.Second))
					return
				}
				if err := h.Responses.HandleSocketModeResponse(r.Context(), connection.AppID, envelope.EnvelopeID, envelope.Payload); err != nil {
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "response payload routing failed"), time.Now().Add(time.Second))
					return
				}
			}
			delete(pending, envelope.EnvelopeID)
			if sequence > cursor {
				if h.Cursors != nil {
					if err := h.Cursors.SetSocketModeCursor(r.Context(), connection.AppID, sequence); err != nil {
						_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "cursor store unavailable"), time.Now().Add(time.Second))
						return
					}
				}
				cursor = sequence
			}
		case <-ticker.C:
			if h.Events == nil || len(pending) != 0 {
				continue
			}
			records, err := h.Events.ListAppEventsAfter(r.Context(), connection.AppID, cursor, 1)
			if err != nil {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "event source unavailable"), time.Now().Add(time.Second))
				return
			}
			if len(records) == 0 {
				continue
			}
			record := records[0]
			encoded, err := encodeEvent(record)
			if err != nil {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "event payload is invalid"), time.Now().Add(time.Second))
				return
			}
			if err := conn.WriteJSON(encoded); err != nil {
				return
			}
			pending[string(record.Event.ID)] = record.Sequence
		}
	}
}

func encodeEvent(record events.Record) (map[string]any, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(record.Event.Payload), &payload); err != nil || payload == nil {
		typeValue, _ := json.Marshal(record.Event.Topic)
		dataValue, _ := json.Marshal(record.Event.Payload)
		payload = map[string]json.RawMessage{"type": typeValue, "data": dataValue}
	}
	return map[string]any{"envelope_id": string(record.Event.ID), "payload": payload, "type": "events_api", "accepts_response_payload": true}, nil
}
