package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"golang.org/x/net/websocket"
)

type testSource struct {
	cancel context.CancelFunc
	calls  int
}

type emptyEventSource struct{}

func (emptyEventSource) ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error) {
	return nil, nil
}

type testRTMMessageService struct {
	workspace domain.WorkspaceID
	user      domain.UserID
	channel   domain.ConversationID
	text      string
	thread    domain.MessageTimestamp
	err       error
}

func (s *testRTMMessageService) Post(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, text string, thread domain.MessageTimestamp, _ string) (domain.Message, error) {
	s.workspace, s.user, s.channel, s.text, s.thread = workspace, user, channel, text, thread
	if s.err != nil {
		return domain.Message{}, s.err
	}
	return domain.Message{Conversation: channel, Text: text, CreatedAt: time.Unix(12, 0)}, nil
}

type testRTMConnectionSource struct {
	connection domain.RTMConnection
}

func (s testRTMConnectionSource) ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error) {
	return s.connection, nil
}

func (s *testSource) ListEventsAfter(_ context.Context, _ domain.WorkspaceID, _ uint64, _ int) ([]events.Record, error) {
	s.calls++
	if s.calls == 1 {
		return []events.Record{{Sequence: 7, Event: events.Event{ID: "evt_7", WorkspaceID: "T1", Topic: "message.created", Payload: "msg_7", CreatedAt: time.Unix(1, 0)}}}, nil
	}
	s.cancel()
	return nil, nil
}

func TestSSEReplaysFromDurableSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &testSource{cancel: cancel}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	if !strings.Contains(body, "id: 7\n") || !strings.Contains(body, "event: message.created\n") || !strings.Contains(body, "data: msg_7\n") {
		t.Fatalf("body=%q", body)
	}
}

func TestRTMEventEncodingIsJSONProtocol(t *testing.T) {
	payload, err := encodeRTMEvent(events.Record{Sequence: 7, Event: events.Event{Topic: "message.created", Payload: `{"text":"hello"}`}})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	if value["type"] != "message.created" || value["text"] != "hello" {
		t.Fatalf("event=%s", payload)
	}
}

func TestRTMPongPreservesReplyIDAndScalarFields(t *testing.T) {
	payload, err := encodeRTMPong(`{"id":1234,"type":"ping","time":1403299273342,"label":"probe","enabled":true}`)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	if value["type"] != "pong" || value["reply_to"] != float64(1234) || value["time"] != float64(1403299273342) || value["label"] != "probe" || value["enabled"] != true {
		t.Fatalf("pong=%s", payload)
	}
}

func TestRTMPongRejectsInvalidIDsAndNestedFields(t *testing.T) {
	for _, message := range []string{
		`{"id":0,"type":"ping"}`,
		`{"id":-1,"type":"ping"}`,
		`{"id":1.5,"type":"ping"}`,
		`{"id":1,"type":"ping","nested":{"value":true}}`,
		`{"id":1,"type":"ping","list":[1,2]}`,
	} {
		if _, err := encodeRTMPong(message); err == nil {
			t.Fatalf("encodeRTMPong(%s) succeeded, want error", message)
		}
	}
}

func TestRTMMessagePostsThroughServiceAndCorrelatesReply(t *testing.T) {
	service := &testRTMMessageService{}
	connection := domain.RTMConnection{WorkspaceID: "T1", UserID: "U1"}
	payload, err := encodeRTMMessage(context.Background(), connection, service, `{"id":7,"type":"message","channel":"C1","text":" hello ","thread_ts":"12.3"}`)
	if err != nil {
		t.Fatal(err)
	}
	if service.workspace != "T1" || service.user != "U1" || service.channel != "C1" || service.text != " hello " || service.thread != "12.3" {
		t.Fatalf("service call=%+v", service)
	}
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if response["ok"] != true || response["reply_to"] != float64(7) || response["text"] != " hello " || response["ts"] != "12.000000" {
		t.Fatalf("response=%s", payload)
	}
}

func TestRTMMessageRejectsInvalidCommands(t *testing.T) {
	service := &testRTMMessageService{}
	connection := domain.RTMConnection{WorkspaceID: "T1", UserID: "U1"}
	for _, raw := range []string{
		`{"type":"message"}`,
		`{"id":1,"type":"message","channel":"","text":"hello"}`,
		`{"id":1,"type":"message","channel":"C1","text":""}`,
	} {
		if _, err := encodeRTMMessage(context.Background(), connection, service, raw); err == nil {
			t.Fatalf("encodeRTMMessage(%s) succeeded, want error", raw)
		}
	}
}

func TestRTMWebSocketDispatchesMessageAndCorrelatesReply(t *testing.T) {
	service := &testRTMMessageService{}
	handler, err := NewRTMHandler(emptyEventSource{}, "T1", testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, service)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRTM(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/rtm?session_id=session-1"
	config, err := websocket.NewConfig(websocketURL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	var hello map[string]any
	if err := websocket.JSON.Receive(connection, &hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
	if err := websocket.Message.Send(connection, `{"id":9,"type":"message","channel":"C1","text":"hello"}`); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := websocket.JSON.Receive(connection, &response); err != nil {
		t.Fatal(err)
	}
	if response["ok"] != true || response["reply_to"] != float64(9) || response["ts"] != "12.000000" || response["text"] != "hello" {
		t.Fatalf("response=%v", response)
	}
	if service.workspace != "T1" || service.user != "U1" || service.channel != "C1" || service.text != "hello" {
		t.Fatalf("service=%+v", service)
	}
}

func TestRTMWebSocketCorrelatesMessageFailure(t *testing.T) {
	service := &testRTMMessageService{err: errors.New("store unavailable")}
	handler, err := NewRTMHandler(emptyEventSource{}, "T1", testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, service)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRTM(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/rtm?session_id=session-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var hello map[string]any
	if err := websocket.JSON.Receive(connection, &hello); err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(connection, `{"id":11,"type":"message","channel":"C1","text":"hello"}`); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := websocket.JSON.Receive(connection, &response); err != nil {
		t.Fatal(err)
	}
	errorValue, ok := response["error"].(map[string]any)
	if !ok || response["ok"] != false || response["reply_to"] != float64(11) || errorValue["code"] != float64(2) {
		t.Fatalf("response=%v", response)
	}
}

func TestRTMMessageLimitMatchesSlackProtocol(t *testing.T) {
	if maxRTMMessageBytes != 16<<10 {
		t.Fatalf("RTM message limit=%d, want %d", maxRTMMessageBytes, 16<<10)
	}
}
