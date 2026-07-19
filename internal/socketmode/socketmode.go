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
	Store    ConnectionStore
	Upgrader websocket.Upgrader
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
	if err := conn.WriteJSON(map[string]any{"type": "hello", "num_connections": 1, "debug_info": map[string]string{"host": string(connection.AppID)}}); err != nil {
		return
	}
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "text messages required"), time.Now().Add(time.Second))
			return
		}
		var envelope struct {
			EnvelopeID string `json:"envelope_id"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil || strings.TrimSpace(envelope.EnvelopeID) == "" {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, "envelope_id is required"), time.Now().Add(time.Second))
			return
		}
		if err := conn.WriteJSON(map[string]string{"envelope_id": envelope.EnvelopeID}); err != nil {
			return
		}
	}
}
