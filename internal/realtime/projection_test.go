package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	"golang.org/x/net/websocket"
)

// streamAs opens /events as one user against the real service projection and
// returns what the stream wrote before its context ended.
func streamAs(t *testing.T, source UserEventSource, user domain.UserID, lastEventID string) string {
	t.Helper()
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: user, Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, authenticator, &testTypingSource{}, &testConnectionTracker{}, noCanvasPresence{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	recorder := httptest.NewRecorder()
	handler.events(recorder, request)
	return recorder.Body.String()
}

// The SSE stream used to read the raw workspace journal, so every member of a
// workspace was sent the name of every private channel and the identifiers of
// every direct and private message as they happened, and Last-Event-ID: 0
// replayed all of it. It now reads the same per-user projection RTM does.
func TestEventStreamWithholdsConversationsTheReaderIsNotIn(t *testing.T) {
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	for _, user := range []domain.User{{ID: "U1", Name: "alice"}, {ID: "U2", Name: "bob"}, {ID: "U3", Name: "eve"}} {
		user.WorkspaceID = "T1"
		state.SeedUser(user)
	}
	messages := service.Messages{Store: state, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	ctx := context.Background()
	private, err := messages.CreateConversation(ctx, "T1", "U1", "acquisition-of-acme", true)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := messages.Post(ctx, "T1", "U1", private.ID, "top secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	whisper, err := messages.Post(ctx, "T1", "U1", direct.Conversation.ID, "just between us", "", "")
	if err != nil {
		t.Fatal(err)
	}

	outsider := streamAs(t, messages, "U3", "0")
	for _, leaked := range []string{"acquisition-of-acme", string(private.ID), string(secret.ID), "top secret", string(direct.Conversation.ID), string(whisper.ID), "just between us"} {
		if strings.Contains(outsider, leaked) {
			t.Errorf("a non-member's stream carried %q:\n%s", leaked, outsider)
		}
	}
	// The member's stream is the control: the filter withholds by membership,
	// not by withholding everything.
	member := streamAs(t, messages, "U1", "0")
	for _, expected := range []string{"acquisition-of-acme", "top secret", string(direct.Conversation.ID), "just between us"} {
		if !strings.Contains(member, expected) {
			t.Errorf("a member's stream is missing %q:\n%s", expected, member)
		}
	}
}

// throughSource withholds everything it examines, as the projection does for
// a reader who is in none of the busy conversations.
type throughSource struct {
	mu    sync.Mutex
	after []uint64
}

func (s *throughSource) ListUserEventsAfter(_ context.Context, _ domain.WorkspaceID, _ domain.UserID, after uint64, _ int) (events.UserEventPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.after = append(s.after, after)
	return events.UserEventPage{Through: after + 100}, nil
}

func (s *throughSource) cursors() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.after...)
}

func assertCursorsAdvanceByHundred(t *testing.T, cursors []uint64) {
	t.Helper()
	if len(cursors) < 2 {
		t.Fatalf("the stream polled %d times, want at least two", len(cursors))
	}
	for index, after := range cursors {
		if want := uint64(index) * 100; after != want {
			t.Fatalf("poll %d read after %d, want %d: the cursor did not advance past withheld records (%v)", index, after, want, cursors)
		}
	}
}

// A stream resumes after the last record the projection examined, not after
// the last one it delivered. Resuming from the delivered one re-read and
// re-authorized the whole invisible tail on every poll, for every reader.
func TestEventStreamAdvancesPastWithheldRecords(t *testing.T) {
	source := &throughSource{}
	_ = streamAs(t, source, "U1", "")
	assertCursorsAdvanceByHundred(t, source.cursors())
}

func TestRTMStreamAdvancesPastWithheldRecords(t *testing.T) {
	source := &throughSource{}
	handler, err := NewRTMHandler(source, testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, &testRTMMessageService{}, &testTypingSource{}, &testConnectionTracker{})
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
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(source.cursors()) < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	assertCursorsAdvanceByHundred(t, source.cursors())
}

func (*throughSource) LatestEventSequence(context.Context, domain.WorkspaceID, domain.UserID) (uint64, error) {
	return 0, nil
}
