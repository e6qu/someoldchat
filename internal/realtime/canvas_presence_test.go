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
	"github.com/sameoldchat/sameoldchat/internal/crdt"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type scriptedCanvasPresence struct {
	mu      sync.Mutex
	answers [][]domain.CanvasPresence
	err     error
	calls   int
	canvas  domain.CanvasID
	cancel  context.CancelFunc
}

func (s *scriptedCanvasPresence) CanvasPresence(_ context.Context, _ domain.WorkspaceID, _ domain.UserID, canvas domain.CanvasID) ([]domain.CanvasPresence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.canvas = canvas
	if s.err != nil {
		return nil, s.err
	}
	if s.calls > len(s.answers) {
		s.cancel()
		return s.answers[len(s.answers)-1], nil
	}
	return s.answers[s.calls-1], nil
}

func canvasPresenceStream(t *testing.T, ctx context.Context, presence CanvasPresenceSource, target string) string {
	t.Helper()
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(emptyEventSource{}, authenticator, &testTypingSource{}, &testConnectionTracker{}, presence)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Last-Event-ID", "0")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response.Body.String()
}

// A canvas page's stream says who is on the canvas, and says it again only
// when that changes: a renewal that moves nobody's cursor sends nothing. The
// frame carries no id, so it cannot move the cursor the page resumes from.
func TestCanvasPageStreamAnnouncesPresenceWhenItChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	here := domain.CanvasPresence{UserID: "U2", Name: "Bea", Session: "bea-first-tab", Caret: crdt.ID{Replica: "U2.tab", Clock: 4}, ExpiresAt: time.Now().Add(time.Minute)}
	renewed := here
	renewed.ExpiresAt = here.ExpiresAt.Add(5 * time.Second)
	moved := renewed
	moved.Caret = crdt.ID{Replica: "U2.tab", Clock: 9}
	presence := &scriptedCanvasPresence{answers: [][]domain.CanvasPresence{{here}, {renewed}, {moved}}, cancel: cancel}
	body := canvasPresenceStream(t, ctx, presence, "/events?canvas=F1")

	if presence.canvas != "F1" {
		t.Fatalf("presence was read for %q", presence.canvas)
	}
	frames := strings.Split(body, "event: canvas.presence\n")[1:]
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want one for arriving and one for moving: %q", len(frames), body)
	}
	for _, frame := range frames {
		frame = frame[:strings.Index(frame, "\n\n")]
		if strings.Contains(frame, "id: ") {
			t.Fatalf("a presence frame carries a cursor: %q", frame)
		}
	}
	if !strings.Contains(frames[0], `{"canvas_id":"F1","present":[{"user_id":"U2","name":"Bea","session":"bea-first-tab","caret":{"r":"U2.tab","c":4}}]}`) || !strings.Contains(frames[1], `"c":9`) {
		t.Fatalf("frames = %q", frames)
	}
}

// A stream that is not a canvas page's reads no presence, and one whose
// reader cannot see the canvas stops asking without ending the stream.
func TestCanvasPresenceIsReadOnlyForACanvasTheReaderCanSee(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	unread := &scriptedCanvasPresence{answers: [][]domain.CanvasPresence{nil}, cancel: func() {}}
	if body := canvasPresenceStream(t, ctx, unread, "/events"); unread.calls != 0 || strings.Contains(body, "canvas.presence") {
		t.Fatalf("a stream no canvas page opened read presence %d times", unread.calls)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	hidden := &scriptedCanvasPresence{err: store.ErrNotFound, cancel: func() {}}
	body := canvasPresenceStream(t, ctx, hidden, "/events?canvas=FHIDDEN")
	if hidden.calls != 1 || strings.Contains(body, "canvas.presence") {
		t.Fatalf("a canvas the reader cannot see was asked about %d times", hidden.calls)
	}
	if !strings.Contains(body, ": connected") {
		t.Fatalf("the stream did not stay open: %q", body)
	}
}
