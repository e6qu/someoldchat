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
)

func getFragment(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	request.Header.Set("X-SameOldChat-Fragment", "profile")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

// TestProfilePanelShowsTheAuthorizedIdentity covers PROFILE-01: the panel a
// name, a People card or a search hit opens names the member, their title,
// pronouns, status and local time, offers Message, Huddle and the ⋮ actions,
// and carries the e-mail address only for a reader holding users:read.email.
func TestProfilePanelShowsTheAuthorizedIdentity(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "ana", RealName: "Ana Lima", Email: "ana@example.test",
		Profile: domain.UserProfile{Title: "Design lead", Pronouns: "she/her", Timezone: "America/Sao_Paulo", StatusText: "In meetings", StatusEmoji: ":calendar:"}}); err != nil {
		t.Fatal(err)
	}
	response := getFragment(t, mux, "/app/members/profile?user=U2")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	location, _ := time.LoadLocation("America/Sao_Paulo")
	requireContains(t, "profile panel", body,
		`id="profile-panel-heading"`, "Ana Lima", "(she/her)", "Design lead", "In meetings",
		`data-profile-tz="America/Sao_Paulo"`, "local time", time.Now().In(location).Format("PM"),
		`name="users" value="U2"`, "Message Ana Lima", `name="huddle" value="1"`,
		`data-copy-text="U2"`, "Copy member ID", "/app/files?from=U2", "View files",
		`href="mailto:ana@example.test"`, "Add to VIPs")
	if strings.Contains(body, "<html") {
		t.Fatal("the profile fragment rendered a whole page")
	}

	// A navigation, not the panel script, lands on People with the panel open.
	navigation := get(t, mux, "/app/members/profile?user=U2")
	if navigation.Code != http.StatusSeeOther || navigation.Header().Get("Location") != "/app/members?user=U2" {
		t.Fatalf("navigation status=%d location=%q", navigation.Code, navigation.Header().Get("Location"))
	}
	page := get(t, mux, "/app/members?user=U2").Body.String()
	requireContains(t, "people with the panel open", page, `<aside id="profile-panel" class="profile-panel"`, "Design lead")

	// A member who is not here is a handled 404, not a server error.
	missing := getFragment(t, mux, "/app/members/profile?user=Unobody")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `role="alert"`) {
		t.Fatalf("missing member status=%d body=%s", missing.Code, missing.Body)
	}
}

// TestProfilePanelWithholdsEmailWithoutTheScope pins PROFILE-01's privacy
// rule: a field the reader may not see is absent from the HTML.
func TestProfilePanelWithholdsEmailWithoutTheScope(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeUsersRead)})
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "ana", Email: "ana@example.test"}); err != nil {
		t.Fatal(err)
	}
	body := getFragment(t, mux, "/app/members/profile?user=U2").Body.String()
	requireMissing(t, "profile without users:read.email", body, "ana@example.test")
	requireContains(t, "profile without users:read.email", body, "No contact details are shared with you.")
}

// TestProfileHuddleOpensTheDirectMessageWithAHuddle covers the profile's
// Huddle button: one action opens the DM and starts its huddle.
func TestProfileHuddleOpensTheDirectMessageWithAHuddle(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "ana"}); err != nil {
		t.Fatal(err)
	}
	response := postForm(t, mux, "/app/conversation/open", url.Values{"_csrf": {auth.CSRFToken("session")}, "users": {"U2"}, "huddle": {"1"}}.Encode(), false)
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "notice=Huddle+started") {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body)
	}
	channel, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveHuddle(context.Background(), "T1", domain.ConversationID(channel.Query().Get("channel"))); err != nil {
		t.Fatalf("no huddle is running in the opened DM: %v", err)
	}
}

// TestFaviconIsServed covers the favicon every workspace page used to 404 on.
func TestFaviconIsServed(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, path := range []string{"/favicon.ico", "/favicon.svg"} {
		response := get(t, mux, path)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(response.Body.String(), "<svg") {
			t.Fatalf("%s status=%d type=%q", path, response.Code, response.Header().Get("Content-Type"))
		}
	}
}

// TestHeartbeatRecordsTheBrowserTimezone covers the automatic time zone the
// profile's local time is computed from: the presence heartbeat carries the
// browser's zone and the profile follows it; a zone nothing can load is
// ignored rather than stored.
func TestHeartbeatRecordsTheBrowserTimezone(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	if response := postForm(t, mux, "/app/active", url.Values{"_csrf": {csrf}, "timezone": {"Asia/Tokyo"}}.Encode(), false); response.Code != http.StatusNoContent {
		t.Fatalf("heartbeat status=%d", response.Code)
	}
	user, err := s.GetUser(context.Background(), "U1")
	if err != nil || user.Profile.Timezone != "Asia/Tokyo" {
		t.Fatalf("profile zone=%q err=%v", user.Profile.Timezone, err)
	}
	if response := postForm(t, mux, "/app/active", url.Values{"_csrf": {csrf}, "timezone": {"Nowhere/Nothing"}}.Encode(), false); response.Code != http.StatusNoContent {
		t.Fatalf("heartbeat with a bad zone status=%d", response.Code)
	}
	if user, _ := s.GetUser(context.Background(), "U1"); user.Profile.Timezone != "Asia/Tokyo" {
		t.Fatalf("a bad zone replaced the stored one: %q", user.Profile.Timezone)
	}
}
