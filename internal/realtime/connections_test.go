package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// testConnectionTracker records the connections streams open, renew and
// close.
type testConnectionTracker struct {
	mu      sync.Mutex
	opened  []domain.UserID
	renewed int
	closed  []domain.ClientConnectionID
	openErr error
}

func (t *testConnectionTracker) OpenClientConnection(_ context.Context, workspace domain.WorkspaceID, user domain.UserID) (domain.ClientConnection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openErr != nil {
		return domain.ClientConnection{}, t.openErr
	}
	t.opened = append(t.opened, user)
	return domain.ClientConnection{ID: "cc-1", WorkspaceID: workspace, UserID: user, ExpiresAt: time.Now().Add(domain.ClientConnectionLease)}, nil
}

func (t *testConnectionTracker) RenewClientConnection(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ClientConnectionID) (domain.ClientConnection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.renewed++
	return domain.ClientConnection{ID: id, WorkspaceID: workspace, UserID: user, ExpiresAt: time.Now().Add(domain.ClientConnectionLease)}, nil
}

func (t *testConnectionTracker) CloseClientConnection(_ context.Context, _ domain.WorkspaceID, _ domain.UserID, id domain.ClientConnectionID) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = append(t.closed, id)
	return nil
}

func (t *testConnectionTracker) snapshot() ([]domain.UserID, []domain.ClientConnectionID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]domain.UserID(nil), t.opened...), append([]domain.ClientConnectionID(nil), t.closed...)
}

// Each stream is one of its member's client connections: it opens one before
// it delivers anything and closes it when it ends, which is what makes the
// member online while they read and offline once they stop.
func TestAStreamIsAClientConnectionWhileItIsOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &testSource{cancel: cancel}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	tracker := &testConnectionTracker{}
	handler, err := NewHandler(source, authenticator, &testTypingSource{}, tracker, noCanvasPresence{})
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
	opened, closed := tracker.snapshot()
	if len(opened) != 1 || opened[0] != "U1" || len(closed) != 1 || closed[0] != "cc-1" {
		t.Fatalf("SSE opened %v and closed %v, want one connection for U1 opened and closed", opened, closed)
	}

	rtmTracker := &testConnectionTracker{}
	rtm, err := NewRTMHandler(emptyEventSource{}, testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, &testRTMMessageService{}, &testTypingSource{}, rtmTracker)
	if err != nil {
		t.Fatal(err)
	}
	client := dialRTM(t, rtm, nil)
	if _, frame, err := client.ReadMessage(); err != nil || string(frame) != `{"type":"hello"}` {
		t.Fatalf("first frame=%s err=%v", frame, err)
	}
	if opened, _ := rtmTracker.snapshot(); len(opened) != 1 || opened[0] != "U1" {
		t.Fatalf("RTM opened %v before hello, want U1", opened)
	}
	_ = client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, closed := rtmTracker.snapshot(); len(closed) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RTM never closed its client connection after the socket ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stream that cannot record its connection is refused rather than left
// delivering to a member everyone else sees as offline.
func TestAStreamThatCannotOpenItsConnectionIsRefused(t *testing.T) {
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(emptyEventSource{}, authenticator, &testTypingSource{}, &testConnectionTracker{openErr: errors.New("store unavailable")}, noCanvasPresence{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/events", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", response.Code)
	}

	rtm, err := NewRTMHandler(emptyEventSource{}, testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, &testRTMMessageService{}, &testTypingSource{}, &testConnectionTracker{openErr: errors.New("store unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	client := dialRTM(t, rtm, nil)
	if _, frame, err := client.ReadMessage(); err != nil || string(frame) != `{"type":"goodbye"}` {
		t.Fatalf("first frame=%s err=%v, want goodbye", frame, err)
	}
	if _, err := NewHandler(emptyEventSource{}, authenticator, &testTypingSource{}, nil, noCanvasPresence{}); err == nil {
		t.Fatal("an SSE handler was built without a connection tracker")
	}
}
