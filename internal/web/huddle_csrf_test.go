package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func huddlePresenceFixture(t *testing.T) (*memory.Store, *http.ServeMux, domain.CallID) {
	t.Helper()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	state.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	state.SeedConversationMember("Cdev", "U1")
	if err := state.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"channels:history", "chat:write"}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(state)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: state, Blob: objects, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	handler, err := NewHandler(messages, authenticator, state, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	handler.HuddleStore = state
	mux := http.NewServeMux()
	handler.Register(mux)
	call, err := messages.StartHuddle(context.Background(), "T1", "U1", "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	return state, mux, call.ID
}

func presenceRequest(body, site, token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/app/huddle/presence", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Sec-Fetch-Site", site)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	if token != "" {
		request.Header.Set(auth.CSRFTokenHeaderName, token)
	}
	return request
}

func journalLength(t *testing.T, state *memory.Store) int {
	t.Helper()
	records, err := state.ListEventsAfter(context.Background(), "T1", 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(records)
}

// Huddle presence is broadcast to everyone in the call, so a forged request
// could mute or unmute a member on every tile. It must prove its origin like
// every other mutation, and a body it cannot read is the caller's error, not
// an empty success.
func TestHuddlePresenceRequiresTheCSRFTokenAndRejectsAnUnreadableBody(t *testing.T) {
	state, mux, callID := huddlePresenceFixture(t)
	before := journalLength(t, state)

	forged := httptest.NewRecorder()
	mux.ServeHTTP(forged, presenceRequest("call_id="+string(callID)+"&muted=true", "cross-site", ""))
	if forged.Code != http.StatusForbidden || !strings.Contains(forged.Body.String(), "invalid_csrf") {
		t.Fatalf("a cross-site presence broadcast answered %d %q, want a CSRF refusal", forged.Code, forged.Body.String())
	}
	if journalLength(t, state) != before {
		t.Fatal("a forged presence broadcast was journalled")
	}

	unreadable := httptest.NewRecorder()
	mux.ServeHTTP(unreadable, presenceRequest("call_id=a&call_id=b", "same-origin", auth.CSRFToken("session")))
	if unreadable.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable presence body answered %d %q, want 400", unreadable.Code, unreadable.Body.String())
	}

	genuine := httptest.NewRecorder()
	mux.ServeHTTP(genuine, presenceRequest("call_id="+string(callID)+"&muted=true", "same-origin", auth.CSRFToken("session")))
	if genuine.Code != http.StatusNoContent {
		t.Fatalf("a genuine presence broadcast answered %d %q", genuine.Code, genuine.Body.String())
	}
	if journalLength(t, state) != before+1 {
		t.Fatal("a genuine presence broadcast was not journalled")
	}
}
