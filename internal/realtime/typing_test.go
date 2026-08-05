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
	"golang.org/x/net/websocket"
)

// The announcer is where "repeat while composing continues, say nothing when it
// stops" is actually implemented, and it is the piece with no server round trip
// to hide behind. Slack pairs no "stopped typing" frame with user_typing, so a
// member who falls silent must produce no frame at all — the reader's own clock
// clears them.
func TestTypingIsAnnouncedOncePerRenewalAndNeverRetracted(t *testing.T) {
	announcer := newTypingAnnouncer()
	first := time.Unix(1700000000, 0).UTC()
	signal := domain.TypingSignal{Conversation: "C1", UserID: "U2", ExpiresAt: first}

	if due := announcer.due([]domain.TypingSignal{signal}); len(due) != 1 {
		t.Fatalf("a new signal produced %d frames, want one", len(due))
	}
	// The same signal observed again is the same fact. Announcing it every poll
	// would put a frame on the wire every second for as long as someone thinks
	// about a sentence.
	if due := announcer.due([]domain.TypingSignal{signal}); len(due) != 0 {
		t.Fatalf("an unchanged signal produced %d frames, want none", len(due))
	}
	renewed := signal
	renewed.ExpiresAt = first.Add(3 * time.Second)
	if due := announcer.due([]domain.TypingSignal{renewed}); len(due) != 1 {
		t.Fatalf("a renewed signal produced %d frames, want one", len(due))
	}
	// Falling silent is not an event. It is the absence of one.
	if due := announcer.due(nil); len(due) != 0 {
		t.Fatalf("a member who stopped typing produced %d frames, want none", len(due))
	}
	// Coming back after a pause must be announced again rather than suppressed
	// by the memory of the signal that already expired.
	if due := announcer.due([]domain.TypingSignal{renewed}); len(due) != 1 {
		t.Fatalf("a returning member produced %d frames, want one", len(due))
	}
}

// The frame is Slack's, not this product's. An official RTM client dispatches
// on "type" and reads "channel" and "user"; a frame carrying our own field
// names would arrive and fire no listener at all.
func TestTypingFrameIsSlackShaped(t *testing.T) {
	frame, err := typingFrame(domain.TypingSignal{Conversation: "C024BE7LR", UserID: "U024BE7LG"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "user_typing" || decoded["channel"] != "C024BE7LR" || decoded["user"] != "U024BE7LG" {
		t.Fatalf("frame = %v, want Slack's user_typing shape", decoded)
	}
	if len(decoded) != 3 {
		t.Fatalf("frame carries %d fields: %v — Slack's user_typing has three", len(decoded), decoded)
	}
}

func TestTypingCommandIsParsedAndOtherwiseIgnored(t *testing.T) {
	if channel, ok := typingCommand(`{"type":"typing","channel":"C1"}`); !ok || channel != "C1" {
		t.Fatalf("typingCommand = %q %v, want C1 true", channel, ok)
	}
	for _, raw := range []string{
		`{"type":"typing"}`,
		`{"type":"typing","channel":""}`,
		`{"type":"ping"}`,
		`not json`,
	} {
		if _, ok := typingCommand(raw); ok {
			t.Fatalf("typingCommand(%s) was accepted", raw)
		}
	}
}

// End to end over a real socket: a client says it is typing, the server records
// it, and the same stream delivers someone else's signal as Slack's frame.
func TestRTMWebSocketRecordsTypingAndDeliversUserTyping(t *testing.T) {
	typing := &testTypingSource{}
	typing.stage(domain.TypingSignal{WorkspaceID: "T1", Conversation: "C1", UserID: "U2", ExpiresAt: time.Now().Add(time.Minute)})
	handler, err := NewRTMHandler(emptyEventSource{}, testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, &testRTMMessageService{}, typing)
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

	if err := websocket.Message.Send(connection, `{"type":"typing","channel":"C1"}`); err != nil {
		t.Fatal(err)
	}
	// The staged signal is delivered as Slack's frame. Reading until it arrives
	// rather than assuming the first frame is it keeps the test honest about a
	// stream that also carries heartbeats and journal records.
	if err := connection.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var delivered map[string]any
	for {
		if err := websocket.JSON.Receive(connection, &delivered); err != nil {
			t.Fatalf("no user_typing frame arrived: %v", err)
		}
		if delivered["type"] == "user_typing" {
			break
		}
	}
	if delivered["channel"] != "C1" || delivered["user"] != "U2" {
		t.Fatalf("user_typing = %v, want U2 in C1", delivered)
	}

	// The command the client sent reached the service. Slack acknowledges a
	// typing command with nothing at all, so the write is the only evidence.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		recorded := typing.recorded()
		if len(recorded) == 1 && recorded[0].Conversation == "C1" && recorded[0].UserID == "U1" && recorded[0].WorkspaceID == "T1" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the typing command was not recorded: %+v", typing.recorded())
}

// A typing frame must carry no id, because the SSE client persists the last id
// it saw and resumes from it. Stamping these frames with the sequence a record
// without one would have — zero — would rewind every reader that saw one to the
// start of the journal and replay the whole workspace to them as live events.
func TestSSETypingFrameCarriesNoCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &testSource{cancel: cancel}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	typing := &testTypingSource{}
	typing.stage(domain.TypingSignal{WorkspaceID: "T1", Conversation: "C1", UserID: "U2", ExpiresAt: time.Now().Add(time.Minute)})
	handler, err := NewHandler(source, authenticator, typing)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	body := response.Body.String()
	index := strings.Index(body, "event: typing\n")
	if index < 0 {
		t.Fatalf("no typing frame was delivered: body=%q", body)
	}
	frame := body[index:]
	if end := strings.Index(frame, "\n\n"); end >= 0 {
		frame = frame[:end]
	}
	if strings.Contains(frame, "id: ") {
		t.Fatalf("the typing frame carries a cursor: %q", frame)
	}
	if !strings.Contains(frame, `"type":"user_typing"`) || !strings.Contains(frame, `"channel":"C1"`) || !strings.Contains(frame, `"user":"U2"`) {
		t.Fatalf("typing frame = %q, want Slack's user_typing body", frame)
	}
	// The journal record on the same stream still carries its own id: the
	// unsequenced frame must not have disturbed the cursor contract.
	if !strings.Contains(body, "id: 7\n") {
		t.Fatalf("the journal record lost its cursor: body=%q", body)
	}
}
