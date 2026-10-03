package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// A member adds a Slackbot response from "Customize workspace", sees it
// listed, and removes it; a response without a reply is refused with the
// rule, and a guest may read the list but not change it.
func TestSlackbotResponsesAreManagedFromCustomizeWorkspace(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	requireContains(t, "empty page", get(t, mux, "/app/customize/slackbot?channel=Cdev").Body.String(),
		`aria-current="page">Slackbot<`, "This workspace has no Slackbot responses yet.")
	requireContains(t, "tools menu", get(t, mux, "/app?channel=Cdev").Body.String(), `href="/app/customize/emoji"><span>Customize workspace<`)

	added := postForm(t, mux, "/app/customize/slackbot/add?channel=Cdev", url.Values{"_csrf": {csrf}, "triggers": {"lunch, what's for lunch"}, "replies": {"Tacos at noon!\r\n\r\nAsk Ada"}}.Encode(), false)
	if added.Code != http.StatusSeeOther || !strings.Contains(added.Header().Get("Location"), "channel=Cdev") {
		t.Fatalf("add=%d location=%q: %s", added.Code, added.Header().Get("Location"), added.Body)
	}
	responses, err := s.ListSlackbotResponses(context.Background(), "T1")
	if err != nil || len(responses) != 1 {
		t.Fatalf("responses=%+v err=%v", responses, err)
	}
	page := get(t, mux, "/app/customize/slackbot?channel=Cdev&notice=Added+the+response.").Body.String()
	requireContains(t, "listed", page, "Added the response.", "lunch, what&#39;s for lunch", "<li>Tacos at noon!</li><li>Ask Ada</li>",
		`name="id" value="`+string(responses[0].ID)+`"`, `aria-label="Remove the response to lunch"`)

	if invalid := postForm(t, mux, "/app/customize/slackbot/add", url.Values{"_csrf": {csrf}, "triggers": {"lunch"}, "replies": {"  "}}.Encode(), false); invalid.Code != http.StatusBadRequest {
		t.Fatalf("a response without a reply=%d", invalid.Code)
	}
	removed := postForm(t, mux, "/app/customize/slackbot/remove", url.Values{"_csrf": {csrf}, "id": {string(responses[0].ID)}}.Encode(), false)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("remove=%d: %s", removed.Code, removed.Body)
	}
	if again := postForm(t, mux, "/app/customize/slackbot/remove", url.Values{"_csrf": {csrf}, "id": {string(responses[0].ID)}}.Encode(), false); again.Code != http.StatusNotFound {
		t.Fatalf("removing it again=%d", again.Code)
	}

	guest := domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.com", Name: "guest"}
	if err := s.CreateUser(context.Background(), guest, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "UG", Role: domain.WorkspaceRoleMember, Active: true, Restricted: true},
		events.Event{ID: "E-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(context.Background(), "guest-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "UG", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	asGuest := func(method, target, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "guest-session"})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	if page := asGuest(http.MethodGet, "/app/customize/slackbot", ""); page.Code != http.StatusOK {
		t.Fatalf("a guest reading the list=%d", page.Code)
	}
	refused := asGuest(http.MethodPost, "/app/customize/slackbot/add", url.Values{"_csrf": {auth.CSRFToken("guest-session")}, "triggers": {"lunch"}, "replies": {"Tacos"}}.Encode())
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "Guests cannot change Slackbot") {
		t.Fatalf("a guest adding=%d: %s", refused.Code, refused.Body)
	}
}
