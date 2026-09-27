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
	"github.com/sameoldchat/sameoldchat/internal/service"
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

// TestActivityGroupsReactionsAndKeepsTheReactor covers the Activity rows Slack
// shows for reactions: the reactor (not the message's author) is named, the
// emoji is a glyph, several reactions with the same emoji on one message are
// one row naming everyone, and Mark all as read clears every unread item.
func TestActivityGroupsReactionsAndKeepsTheReactor(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	for _, user := range []domain.User{{ID: "U2", WorkspaceID: "T1", Name: "ana", RealName: "Ana Lima"}, {ID: "U3", WorkspaceID: "T1", Name: "dana", RealName: "Dana Park"}} {
		if err := s.SeedUser(user); err != nil {
			t.Fatal(err)
		}
		if err := s.SeedConversationMember("Cdev", user.ID); err != nil {
			t.Fatal(err)
		}
	}
	message := seedMessage(t, s, "M1", "Proposal: new onboarding flow", time.Unix(1700000000, 0).UTC())
	messages := service.Messages{Store: s}
	for _, reactor := range []domain.UserID{"U2", "U3"} {
		if err := messages.AddReaction(context.Background(), "T1", reactor, "Cdev", domain.NewMessageTimestamp(message.CreatedAt), "tada"); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, mux, "/app/activity?channel=Cdev&kind=reaction").Body.String()
	requireContains(t, "grouped reaction row", body,
		"Reaction in #general",
		"<strong>Dana Park</strong> and <strong>Ana Lima</strong> reacted",
		`aria-label=":tada:">🎉</span> to your message`)
	requireMissing(t, "grouped reaction row", body, "REACTION :TADA:", "Reaction :tada:", "<strong>Ada Developer</strong> reacted")
	if rows := strings.Count(body, "data-activity-row data-activity-id"); rows != 1 {
		t.Fatalf("reaction rows = %d, want the two reactions folded into one", rows)
	}

	marked := postForm(t, mux, "/app/activity/mutate?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "mutation": {"read_all"}}.Encode(), false)
	if marked.Code != http.StatusSeeOther {
		t.Fatalf("mark all read status=%d body=%s", marked.Code, marked.Body)
	}
	unread := get(t, mux, "/app/activity?channel=Cdev&unread=1").Body.String()
	requireContains(t, "after mark all read", unread, "You’re all caught up.")
}

// TestNotificationPausePresetsResolveInTheMembersZone covers NOTIFY-03's
// presets: until tomorrow and until next week are 9:00 in the member's own
// zone (past dnd.setSnooze's one-day limit), a custom time is read in that
// zone, and a time already past is a handled refusal.
func TestNotificationPausePresetsResolveInTheMembersZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 22, 30, 0, 0, tokyo) // a Friday evening in Tokyo
	for preset, want := range map[string]time.Time{
		"tomorrow":  time.Date(2026, 9, 26, 9, 0, 0, 0, tokyo),
		"next_week": time.Date(2026, 9, 28, 9, 0, 0, 0, tokyo),
	} {
		got, ok := notificationPauseEnd(preset, "", tokyo, now.UTC())
		if !ok || !got.Equal(want) {
			t.Fatalf("%s = %v (%v), want %v", preset, got, ok, want)
		}
	}
	if got, ok := notificationPauseEnd("custom", "2026-09-30T14:15", tokyo, now); !ok || !got.Equal(time.Date(2026, 9, 30, 14, 15, 0, 0, tokyo)) {
		t.Fatalf("custom = %v %v", got, ok)
	}
	if _, ok := notificationPauseEnd("custom", "not a time", tokyo, now); ok {
		t.Fatal("an unreadable custom time was accepted")
	}

	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	paused := postForm(t, mux, "/app/notifications/dnd?channel=Cdev", url.Values{"_csrf": {csrf}, "action": {"pause"}, "preset": {"next_week"}, "timezone": {"Asia/Tokyo"}}.Encode(), false)
	if paused.Code != http.StatusSeeOther {
		t.Fatalf("pause status=%d body=%s", paused.Code, paused.Body)
	}
	dnd, err := s.GetDoNotDisturb(context.Background(), "T1", "U1")
	if err != nil || !dnd.SnoozeUntil.After(time.Now()) || dnd.SnoozeUntil.In(tokyo).Hour() != 9 || dnd.SnoozeUntil.In(tokyo).Weekday() != time.Monday {
		t.Fatalf("paused until %v err=%v, want 9:00 next Monday in Tokyo", dnd.SnoozeUntil.In(tokyo), err)
	}
	past := postForm(t, mux, "/app/notifications/dnd?channel=Cdev", url.Values{"_csrf": {csrf}, "action": {"pause"}, "preset": {"custom"}, "until": {"2001-01-01T09:00"}, "timezone": {"UTC"}}.Encode(), false)
	if past.Code != http.StatusBadRequest {
		t.Fatalf("a past custom pause status=%d, want a handled 400", past.Code)
	}

	// "Nothing" is a workspace trigger, and it turns desktop notifications off.
	saved := postForm(t, mux, "/app/notifications/preferences?channel=Cdev", url.Values{"_csrf": {csrf}, "level": {"mute"}, "browser_notifications": {"true"}}.Encode(), false)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save Nothing status=%d body=%s", saved.Code, saved.Body)
	}
	requireContains(t, "workspace with Nothing", get(t, mux, "/app?channel=Cdev").Body.String(), `data-browser-notifications="false"`)
}

// TestDirectMessagesListShowsFacesPreviewAndUnread covers DM-01's list rows:
// each conversation shows its latest message as a one-line preview ("You:"
// for the reader's own, the author's name in a group DM) with mentions
// resolved and markup stripped, unread conversations are marked, and a group
// DM's rename lives in its ⋮ menu instead of an always-visible form.
func TestDirectMessagesListShowsFacesPreviewAndUnread(t *testing.T) {
	ctx := context.Background()
	s, mux := browserWorkspace(t, auth.AllScopes())
	for _, user := range []domain.User{{ID: "U2", WorkspaceID: "T1", Name: "ana", RealName: "Ana Lima"}, {ID: "U3", WorkspaceID: "T1", Name: "ben", RealName: "Ben Ortiz"}} {
		if err := s.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	messages := service.Messages{Store: s}
	direct, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2", "U3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", direct.Conversation.ID, "see *you* at <@U2>", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U3", group.Conversation.ID, "lunch?", "", ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app/dms").Body.String()
	requireContains(t, "DMs list", body,
		"You: see you at @Ana Lima", "Ben Ortiz: lunch?", `class="v-row unread"`,
		"New message", `aria-label="More actions for Ana Lima, Ben Ortiz"`, "Rename group DM")
	requireMissing(t, "DMs list", body, "*you*", "&lt;@U2&gt;")
	if unread := strings.Count(body, `class="v-row unread"`); unread != 1 {
		t.Fatalf("unread rows = %d, want only the group DM (own messages are never unread)", unread)
	}
}

// TestHuddleInADirectMessageIsNamedForThePerson covers the huddle window's
// title: a DM's huddle is "with" the other person, never the conversation ID.
func TestHuddleInADirectMessageIsNamedForThePerson(t *testing.T) {
	ctx := context.Background()
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "ana", RealName: "Ana Lima"}); err != nil {
		t.Fatal(err)
	}
	opened, err := service.Messages{Store: s}.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	channel := string(opened.Conversation.ID)
	if response := postForm(t, mux, "/app/huddle/start?channel="+channel, url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false); response.Code != http.StatusSeeOther {
		t.Fatalf("start=%d: %s", response.Code, response.Body)
	}
	body := get(t, mux, "/app/huddle?channel="+channel).Body.String()
	requireContains(t, "DM huddle", body, "Huddle with Ana Lima")
	requireMissing(t, "DM huddle", body, "Huddle in "+channel, "Huddle in #")
}
