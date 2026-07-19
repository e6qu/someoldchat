package socketmode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestOpenRequiresExplicitAppAndHost(t *testing.T) {
	service := Service{Store: memory.New(), Host: "example.test"}
	if _, err := service.Open(context.Background(), ""); err != ErrInvalidAppID {
		t.Fatalf("Open empty app ID error=%v, want %v", err, ErrInvalidAppID)
	}
	service.Host = ""
	if _, err := service.Open(context.Background(), "A123"); err == nil {
		t.Fatal("Open without public host succeeded")
	}
}

func TestConnectionIsSingleUseAndExpires(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test", TLS: true}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.URL, "wss://example.test/socket-mode?") {
		t.Fatalf("URL=%q", result.URL)
	}
	id, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := connections.ConsumeSocketModeConnection(context.Background(), id.Query().Get("connection_id"))
	if err != nil || connection.AppID != "A123" {
		t.Fatalf("connection=%+v error=%v", connection, err)
	}
	if _, err := connections.ConsumeSocketModeConnection(context.Background(), connection.ID); err != store.ErrNotFound {
		t.Fatalf("replay error=%v, want %v", err, store.ErrNotFound)
	}
}

func TestHandlerSendsHelloAndAcknowledgesEnvelope(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Store: connections})
	defer server.Close()
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var hello map[string]any
	if err := client.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
	if err := client.WriteJSON(map[string]string{"envelope_id": "env-1", "payload": "{}"}); err != nil {
		t.Fatal(err)
	}
	var acknowledgement map[string]string
	if err := client.ReadJSON(&acknowledgement); err != nil {
		t.Fatal(err)
	}
	if acknowledgement["envelope_id"] != "env-1" {
		t.Fatalf("acknowledgement=%v", acknowledgement)
	}
	if _, err := json.Marshal(hello); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerRejectsEnvelopeWithoutID(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Store: connections})
	defer server.Close()
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(map[string]string{"payload": "{}"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("malformed envelope did not close the connection")
	}
}

var _ http.Handler = Handler{}
