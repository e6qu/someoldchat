package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// TestMarkAllReadClearsEveryConversation is the backend half of Shift+Escape.
// The sidebar's unread badges are the only place a member sees the result, so
// the assertion is on the rendered sidebar rather than on the store.
func TestMarkAllReadClearsEveryConversation(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedConversation(domain.Conversation{ID: "Csecond", WorkspaceID: "T1", Name: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Csecond", "U1"); err != nil {
		t.Fatal(err)
	}
	// A public channel the member can see but has not joined. It shows an unread
	// badge in the sidebar, so "mark everything read" has to clear it too.
	if err := s.SeedConversation(domain.Conversation{ID: "Cunjoined", WorkspaceID: "T1", Name: "release-notes"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000300, 0).UTC()
	for index, conversation := range []domain.ConversationID{"Cdev", "Csecond", "Cunjoined"} {
		message := domain.Message{ID: domain.MessageID("Munread" + string(rune('a'+index))), WorkspaceID: "T1", Conversation: conversation, AuthorID: "U2", Text: "unread", CreatedAt: at}
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID("Eunread" + string(rune('a'+index))), WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created"}`, CreatedAt: at}, ""); err != nil {
			t.Fatal(err)
		}
	}

	if body := sidebar(t, mux); !strings.Contains(body, "unread messages") {
		t.Fatal("nothing was unread before the test acted, so the assertion below would prove nothing")
	}

	post := httptest.NewRequest(http.MethodPost, "/app/read/all?channel=Cdev", strings.NewReader("_csrf="+csrfFor(t, mux)))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	post.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, post)
	if recorder.Code != http.StatusSeeOther && recorder.Code != http.StatusOK {
		t.Fatalf("POST /app/read/all returned %d: %s", recorder.Code, recorder.Body)
	}

	if body := sidebar(t, mux); strings.Contains(body, "unread messages") {
		t.Errorf("a conversation still reports unread messages after marking everything read")
	}
}

func sidebar(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/app?channel=Cdev", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /app returned %d", recorder.Code)
	}
	body := recorder.Body.String()
	start := strings.Index(body, `aria-label="Channels"`)
	if start < 0 {
		t.Fatal("the page has no Channels section")
	}
	end := strings.Index(body[start:], "</nav>")
	return body[start : start+end]
}

func csrfFor(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/app?channel=Cdev", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	marker := `name="_csrf" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("no CSRF token in the page")
	}
	start += len(marker)
	return body[start : start+strings.Index(body[start:], `"`)]
}
