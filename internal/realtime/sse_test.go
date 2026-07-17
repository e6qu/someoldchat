package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

type testSource struct {
	cancel context.CancelFunc
	calls  int
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

func TestRTMEventEncodingRejectsMalformedPayload(t *testing.T) {
	if _, err := encodeRTMEvent(events.Record{Event: events.Event{Topic: "message.created", Payload: "not-json"}}); err == nil {
		t.Fatal("expected malformed RTM payload to fail")
	}
	if _, err := encodeRTMEvent(events.Record{Event: events.Event{Topic: "message.created", Payload: "null"}}); err == nil {
		t.Fatal("expected non-object RTM payload to fail")
	}
}
