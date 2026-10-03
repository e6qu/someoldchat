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
	"github.com/sameoldchat/sameoldchat/internal/service"
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
	// A public channel the member can see but has not joined. It is not in the
	// sidebar (Slack lists only joined conversations), but "mark everything
	// read" still has to leave nothing unread behind.
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

	if body := sidebar(t, mux); !strings.Contains(body, "unread message") {
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

	if body := sidebar(t, mux); strings.Contains(body, "unread message") {
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

// A section's "Mark all as read" names the section's unread rows as the page
// drew them, and clears those alone: another section's unread conversation
// keeps its state.
func TestMarkSectionReadClearsOnlyTheNamedRows(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000300, 0).UTC()
	for index, name := range []string{"second", "third"} {
		id := domain.ConversationID("C" + name)
		if err := s.SeedConversation(domain.Conversation{ID: id, WorkspaceID: "T1", Name: name}); err != nil {
			t.Fatal(err)
		}
		if err := s.SeedConversationMember(id, "U1"); err != nil {
			t.Fatal(err)
		}
		message := domain.Message{ID: domain.MessageID("Msection" + name), WorkspaceID: "T1", Conversation: id, AuthorID: "U2", Text: "unread", CreatedAt: at}
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID("Esection" + string(rune('a'+index))), WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created"}`, CreatedAt: at}, ""); err != nil {
			t.Fatal(err)
		}
	}
	body := sidebar(t, mux)
	for _, want := range []string{`action="/app/read/section?channel=Cdev"`, `name="conversation" value="Csecond"`, `name="conversation" value="Cthird"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the Channels section menu lacks %s", want)
		}
	}

	post := httptest.NewRequest(http.MethodPost, "/app/read/section?channel=Cdev", strings.NewReader("_csrf="+csrfFor(t, mux)+"&conversation=Csecond"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	post.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, post)
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "Marked+1+conversation+read") {
		t.Fatalf("POST /app/read/section returned %d to %q: %s", recorder.Code, recorder.Header().Get("Location"), recorder.Body)
	}

	body = sidebar(t, mux)
	if strings.Contains(body, `name="conversation" value="Csecond"`) || !strings.Contains(body, `name="conversation" value="Cthird"`) {
		t.Fatalf("after marking #second read the menu names: %s", body)
	}
}

// A row with both a draft and a mention shows the draft marker beside the
// badge, as Slack does, and names both to assistive technology.
func TestASidebarRowShowsItsDraftBesideItsBadge(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "Csecond", WorkspaceID: "T1", Name: "second"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedConversationMember("Csecond", user); err != nil {
			t.Fatal(err)
		}
	}
	messages := service.Messages{Store: s}
	if _, err := messages.Post(context.Background(), "T1", "U2", "Csecond", "<@U1> have a look", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SaveDraft(context.Background(), "T1", "U1", "Csecond", "", "half a reply"); err != nil {
		t.Fatal(err)
	}
	body := sidebar(t, mux)
	start := strings.Index(body, `data-conversation="Csecond"`)
	if start < 0 {
		t.Fatalf("#second is not in the Channels section: %s", body)
	}
	row := body[start : start+strings.Index(body[start:], "</a>")]
	for _, want := range []string{"1 mention", "has a draft", `<span class="draft-badge" aria-hidden="true">`, `<span class="badge" aria-hidden="true">1</span>`} {
		if !strings.Contains(row, want) {
			t.Errorf("the row lacks %s: %s", want, row)
		}
	}
}
