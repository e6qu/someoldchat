package socketmode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type testEventSource struct {
	record events.Record
}

type testResponseSink struct {
	appID      domain.AppID
	envelopeID string
	payload    []byte
}

func (s *testResponseSink) HandleSocketModeResponse(_ context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	s.appID = appID
	s.envelopeID = envelopeID
	s.payload = append([]byte(nil), payload...)
	return nil
}

func (s testEventSource) ListAppEventsAfter(_ context.Context, _ domain.AppID, after uint64, _ int) ([]events.Record, error) {
	if s.record.Sequence <= after {
		return nil, nil
	}
	return []events.Record{s.record}, nil
}

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

func TestHandlerDeliversEventAndAdvancesOnlyAfterAcknowledgement(t *testing.T) {
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
	responses := new(testResponseSink)
	server := httptest.NewServer(Handler{Store: connections, Events: testEventSource{record: events.Record{Sequence: 4, Event: events.Event{ID: "event-4", Topic: "message.created", Payload: `{"text":"hello"}`}}}, Cursors: connections, Responses: responses})
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
	var envelope struct {
		EnvelopeID string `json:"envelope_id"`
		Payload    struct {
			Text string `json:"text"`
		} `json:"payload"`
	}
	if err := client.ReadJSON(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.EnvelopeID != "event-4" || envelope.Payload.Text != "hello" {
		t.Fatalf("event envelope=%+v", envelope)
	}
	if err := client.WriteJSON(map[string]any{"envelope_id": envelope.EnvelopeID, "payload": map[string]string{"ok": "true"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		cursor, cursorErr := connections.GetSocketModeCursor(context.Background(), "A123")
		if cursorErr != nil {
			t.Fatal(cursorErr)
		}
		if cursor == 4 && responses.appID == "A123" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cursor=%d, want 4", cursor)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if responses.envelopeID != "event-4" || string(responses.payload) != `{"ok":"true"}` {
		t.Fatalf("response=%+v", responses)
	}
}

var _ http.Handler = Handler{}
