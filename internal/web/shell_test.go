package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// sidebarRow returns the sidebar row for one conversation.
func sidebarRow(t *testing.T, body, id string) string {
	t.Helper()
	start := strings.Index(body, `<div class="side-row" data-conversation="`+id+`"`)
	if start < 0 {
		t.Fatalf("the sidebar has no row for %s", id)
	}
	end := strings.Index(body[start:], "<details")
	return body[start : start+end]
}

// NAV-01: every workspace destination renders inside one frame — the rail, the
// top bar and the shared dialogs — and keeps the conversation the member came
// from, instead of a separate page with a "Back to chat" link that dropped it.
func TestEveryDestinationRendersInsideTheWorkspaceFrame(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, target := range []struct{ path, current string }{
		{"/app?channel=Cdev", "Home"},
		{"/app/dms?channel=Cdev", "DMs"},
		{"/app/activity?channel=Cdev", "Activity"},
		{"/app/todos?channel=Cdev", "To-dos"},
		{"/app/saved?channel=Cdev", "Home"},
		{"/app/members?channel=Cdev", ""},
		{"/app/threads?channel=Cdev", "Home"},
		{"/app/unreads?channel=Cdev", "Home"},
		{"/app/search?q=hello&channel=Cdev", ""},
		{"/app/notifications?channel=Cdev", ""},
		{"/app/apps?channel=Cdev", ""},
		{"/app/drafts?channel=Cdev", "Home"},
		{"/app/canvases?channel=Cdev", ""},
		{"/app/lists?channel=Cdev", ""},
		{"/app/workflows?channel=Cdev", ""},
		{"/app/remote-files?channel=Cdev", ""},
		{"/app/channels?channel=Cdev", "Home"},
		{"/app/preferences?channel=Cdev", "Home"},
	} {
		response := get(t, mux, target.path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target.path, response.Code, response.Body)
		}
		body := response.Body.String()
		requireContains(t, target.path, body,
			`<nav class="rail" id="workspace-rail" aria-label="Workspace">`,
			`id="workspace-search"`,
			`id="conversation-switcher"`,
			`id="keyboard-help"`,
			`id="preferences"`,
			// Home keeps the conversation the member was reading.
			`href="/app?channel=Cdev"`,
			// An empty icon, so no page asks for a /favicon.ico that 404s.
			`<link rel="icon" href="data:,">`,
		)
		requireMissing(t, target.path, body, "Back to chat")
		if target.current != "" {
			current := regexp.MustCompile(`<a class="rail-item"[^>]*aria-current="page"[^>]*>.*?<span class="rail-label">([^<]+)</span>`).FindStringSubmatch(body)
			if current == nil || current[1] != target.current {
				t.Fatalf("%s marks %v as the current rail destination, want %s", target.path, current, target.current)
			}
		}
	}
}

// NAV-01/CONV-01: the sidebar lists only conversations the member belongs to,
// in alphabetical order; a public channel the member has not joined is found
// through the switcher and Browse channels, and opens in preview.
func TestSidebarListsJoinedConversationsAndBrowseFindsTheRest(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	for _, conversation := range []domain.Conversation{
		{ID: "Czeta", WorkspaceID: "T1", Name: "zeta"},
		{ID: "Calpha", WorkspaceID: "T1", Name: "alpha"},
		{ID: "Cnotjoined", WorkspaceID: "T1", Name: "not-joined", Topic: "Somewhere else"},
		{ID: "Csecret", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate},
	} {
		if err := s.SeedConversation(conversation); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []domain.ConversationID{"Czeta", "Calpha", "Csecret"} {
		if err := s.SeedConversationMember(id, "U1"); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	sidebar := body[strings.Index(body, `aria-label="Channels"`):]
	sidebar = sidebar[:strings.Index(sidebar, "</nav>")]
	requireOrdered(t, "alphabetical sidebar", sidebar, `data-name="alpha"`, `data-name="general"`, `data-name="secret"`, `data-name="zeta"`)
	requireMissing(t, "sidebar", sidebar, "not-joined")

	// A private channel carries a lock, not a #, in the sidebar, the switcher
	// and its own header.
	requireContains(t, "private sidebar row", sidebarRow(t, body, "Csecret"), `<use href="#i-lock">`, `aria-label="secret, private channel"`)
	requireContains(t, "switcher", body,
		`data-conversation-name="not-joined" data-kind="channel"`,
		`<span class="switcher-type">Channel</span><span class="switcher-context">Not a member`,
		`data-conversation-name="secret" data-kind="private"`,
		`<span class="switcher-type">Private channel</span>`,
	)
	header := get(t, mux, "/app?channel=Csecret").Body.String()
	requireContains(t, "private header", header, `<span class="visually-hidden">Private channel </span><span class="channel-name-text">secret</span>`)

	browseList := func(target string) string {
		t.Helper()
		body := get(t, mux, target).Body.String()
		start := strings.Index(body, `<ul class="browse-list">`)
		if start < 0 {
			t.Fatalf("%s has no channel list", target)
		}
		return body[start : start+strings.Index(body[start:], "</ul>")]
	}
	browse := browseList("/app/channels?channel=Cdev&q=not")
	requireContains(t, "browse channels", browse, `href="/app?channel=Cnotjoined"`, `action="/app/join?channel=Cnotjoined"`, "Somewhere else")
	requireMissing(t, "browse channels search", browse, `href="/app?channel=Czeta"`)
	mine := browseList("/app/channels?channel=Cdev&hide_mine=1")
	requireMissing(t, "browse hides my channels", mine, `href="/app?channel=Calpha"`)
	requireContains(t, "browse hides my channels", mine, `href="/app?channel=Cnotjoined"`)

	preview := get(t, mux, "/app?channel=Cnotjoined").Body.String()
	requireContains(t, "preview", preview, "You are viewing #not-joined", `action="/app/join?channel=Cnotjoined"`, "Browse channels")
	requireMissing(t, "preview", preview, `id="composer"`)
}

// NAV-04: an unread channel is bold and carries no badge; a mention gives it a
// count; a DM's badge counts every unread message; a muted channel is greyed
// and not bold.
func TestSidebarUnreadMentionAndMutedStates(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []domain.Conversation{
		{ID: "Cquiet", WorkspaceID: "T1", Name: "quiet"},
		{ID: "Cping", WorkspaceID: "T1", Name: "ping"},
		{ID: "Cmuted", WorkspaceID: "T1", Name: "muted"},
		{ID: "Cdm", WorkspaceID: "T1", Kind: domain.ConversationTypeIM},
	} {
		if err := s.SeedConversation(conversation); err != nil {
			t.Fatal(err)
		}
		for _, user := range []domain.UserID{"U1", "U2"} {
			if err := s.SeedConversationMember(conversation.ID, user); err != nil {
				t.Fatal(err)
			}
		}
	}
	at := time.Unix(1700000500, 0).UTC()
	post := func(id domain.MessageID, conversation domain.ConversationID, text string) {
		t.Helper()
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: conversation, AuthorID: "U2", Text: text, CreatedAt: at}
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: at}, ""); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Second)
	}
	post("Mquiet", "Cquiet", "nothing for you")
	post("Mping", "Cping", "hey <@U1> look")
	post("Mping2", "Cping", "and another")
	post("Mmuted", "Cmuted", "muted chatter")
	post("Mdm1", "Cdm", "one")
	post("Mdm2", "Cdm", "two")
	if _, err := (service.Messages{Store: s}).SetConversationNotificationPreferences(context.Background(), "T1", "U1", "Cmuted", domain.NotificationMute, false); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()

	quiet := sidebarRow(t, body, "Cquiet")
	requireContains(t, "unread channel", quiet, `class="side-link is-unread"`, `aria-label="quiet, 1 unread message"`)
	requireMissing(t, "unread channel", quiet, `class="badge"`)

	ping := sidebarRow(t, body, "Cping")
	requireContains(t, "mentioned channel", ping, `class="side-link is-unread"`, `<span class="badge" aria-hidden="true">1</span>`, `aria-label="ping, 2 unread messages, 1 mention"`)

	muted := sidebarRow(t, body, "Cmuted")
	requireContains(t, "muted channel", muted, `class="side-link is-muted"`, `, muted"`)
	requireMissing(t, "muted channel", muted, "is-unread")

	direct := sidebarRow(t, body, "Cdm")
	requireContains(t, "DM", direct, `<span class="badge" aria-hidden="true">2</span>`, `aria-label="Bob Builder, 2 unread messages"`)
}

// DM-01: a self-DM is named after the member, not "direct".
func TestSelfDirectMessageIsNamedAfterTheMember(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedConversation(domain.Conversation{ID: "Cself", WorkspaceID: "T1", Name: "direct", Kind: domain.ConversationTypeIM}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cself", "U1"); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cself").Body.String()
	requireContains(t, "self DM", body,
		`<span class="side-text">Ada Developer (you)</span>`,
		`<span class="channel-name-text">Ada Developer (you)</span>`,
		`data-conversation-name="Ada Developer (you)"`,
	)
	requireMissing(t, "self DM", body, `<span class="side-text">direct</span>`, `<span class="channel-name-text">direct</span>`)
}

// NAV-01: starring moves a conversation to the Starred section; starring and
// unstarring are idempotent.
func TestStarringMovesAConversationToStarred(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	for range 2 {
		starred := postForm(t, mux, "/app/conversation/star?channel=Cdev", url.Values{"_csrf": {csrf}, "starred": {"true"}, "return": {"/app/threads?channel=Cdev"}}.Encode(), false)
		if starred.Code != http.StatusSeeOther || starred.Header().Get("Location") != "/app/threads?channel=Cdev" {
			t.Fatalf("star status=%d location=%q body=%s", starred.Code, starred.Header().Get("Location"), starred.Body)
		}
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "starred section", body, `aria-label="Starred" data-section-key="starred"`, "Unstar channel")
	for range 2 {
		if unstarred := postForm(t, mux, "/app/conversation/star?channel=Cdev", url.Values{"_csrf": {csrf}, "starred": {"false"}}.Encode(), false); unstarred.Code != http.StatusSeeOther {
			t.Fatalf("unstar status=%d body=%s", unstarred.Code, unstarred.Body)
		}
	}
	requireMissing(t, "starred section", get(t, mux, "/app?channel=Cdev").Body.String(), `data-section-key="starred"`)
}

// A form in the frame comes back to the page it was used on, and only to this
// application's own pages: anything else would be an open redirect.
func TestReturnTargetAcceptsOnlyThisApplicationsPages(t *testing.T) {
	for value, want := range map[string]string{
		"/app/todos?channel=Cdev":   "/app/todos?channel=Cdev",
		"/app":                      "/app",
		"https://evil.example/app":  "/fallback",
		"//evil.example/app":        "/fallback",
		"/apple":                    "/fallback",
		"/api/chat.postMessage":     "/fallback",
		"/app/\\evil":               "/fallback",
		"":                          "/fallback",
		"/app?channel=C1#composer":  "/app?channel=C1#composer",
		"javascript:alert(1)//app/": "/fallback",
	} {
		if got := returnTarget(map[string]string{"return": value}, "/fallback"); got != want {
			t.Errorf("returnTarget(%q) = %q, want %q", value, got, want)
		}
	}
}

// PROFILE-02/STATUS-01: the status dialog sets and clears a status without
// touching the display name, and "Clear after" becomes an expiry.
func TestStatusDialogSetsAndClearsTheStatus(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	set := postForm(t, mux, "/app/status", url.Values{"_csrf": {csrf}, "status_emoji": {"palm_tree"}, "status_text": {"Vacationing"}, "clear_after": {"60"}, "return": {"/app?channel=Cdev"}}.Encode(), false)
	if set.Code != http.StatusSeeOther || set.Header().Get("Location") != "/app?channel=Cdev" {
		t.Fatalf("set status=%d location=%q body=%s", set.Code, set.Header().Get("Location"), set.Body)
	}
	user, err := s.GetUser(context.Background(), "U1")
	if err != nil {
		t.Fatal(err)
	}
	if user.Profile.StatusEmoji != ":palm_tree:" || user.Profile.StatusText != "Vacationing" || user.Profile.StatusExpiration.IsZero() || user.RealName != "Ada Developer" {
		t.Fatalf("profile after status = %+v", user)
	}
	requireContains(t, "avatar menu", get(t, mux, "/app?channel=Cdev").Body.String(), "<span>Vacationing</span>")
	past := postForm(t, mux, "/app/status", url.Values{"_csrf": {csrf}, "status_text": {"Later"}, "clear_after": {"custom"}, "clear_at": {"2001-01-01T10:00"}}.Encode(), false)
	if past.Code != http.StatusBadRequest {
		t.Fatalf("a past clear time status=%d", past.Code)
	}
	cleared := postForm(t, mux, "/app/status", url.Values{"_csrf": {csrf}, "clear_status": {"1"}}.Encode(), false)
	if cleared.Code != http.StatusSeeOther {
		t.Fatalf("clear status=%d body=%s", cleared.Code, cleared.Body)
	}
	if user, _ := s.GetUser(context.Background(), "U1"); user.Profile.StatusText != "" || user.Profile.StatusEmoji != "" {
		t.Fatalf("status after clear = %+v", user.Profile)
	}
}

func TestStatusExpirationFollowsTheMembersClock(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC) // a Wednesday
	for _, tc := range []struct {
		fields map[string]string
		want   time.Time
		fails  bool
	}{
		{fields: map[string]string{"clear_after": "never"}},
		{fields: map[string]string{"clear_after": "30"}, want: now.Add(30 * time.Minute)},
		{fields: map[string]string{"clear_after": "today", "timezone": "America/New_York"}, want: time.Date(2026, 9, 24, 3, 59, 0, 0, time.UTC)},
		{fields: map[string]string{"clear_after": "week"}, want: time.Date(2026, 9, 27, 23, 59, 0, 0, time.UTC)},
		{fields: map[string]string{"clear_after": "custom", "clear_at": "2026-09-24T09:00"}, want: time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)},
		{fields: map[string]string{"clear_after": "custom", "clear_at": "2026-09-22T09:00"}, fails: true},
		{fields: map[string]string{"clear_after": "-5"}, fails: true},
	} {
		got, reason := statusExpiration(tc.fields, now)
		if (reason != "") != tc.fails || !got.Equal(tc.want) {
			t.Errorf("statusExpiration(%v) = %v %q, want %v (fails=%t)", tc.fields, got, reason, tc.want, tc.fails)
		}
	}
}

// CONV-02: the create-a-channel dialog adds the people chosen in its last step.
func TestCreatingAChannelAddsThePeopleChosen(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	created := postForm(t, mux, "/app/conversation/create", url.Values{"_csrf": {auth.CSRFToken("session")}, "name": {"launch"}, "is_private": {"false"}, "user_U2": {"1"}}.Encode(), false)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	location, _ := url.Parse(created.Header().Get("Location"))
	channel := domain.ConversationID(location.Query().Get("channel"))
	if member, err := s.IsConversationMember(context.Background(), channel, "U2"); err != nil || !member {
		t.Fatalf("the chosen person was not added: member=%t err=%v", member, err)
	}
	requireContains(t, "create dialog", get(t, mux, "/app/channels/new?channel=Cdev").Body.String(), "Step 1 of 3", "Visibility", "Add people", "Skip for now")
}

// CONV-03: the details dialog is a real modal <dialog> with Slack's tabs, and
// no second banner landmark; a member can be removed from the Members tab.
func TestConversationDetailsIsATabbedDialog(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "U2"); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev&details=1&tab=members").Body.String()
	details := body[strings.Index(body, `<dialog class="shell-dialog conversation-details"`):]
	requireContains(t, "details", details,
		`data-initial-tab="members"`,
		`role="tab" id="details-tab-about"`, `role="tab" id="details-tab-members"`, `role="tab" id="details-tab-integrations"`, `role="tab" id="details-tab-settings"`,
		`data-dialog-open="details-edit-name"`, `data-dialog-open="details-edit-topic"`, `data-dialog-open="details-edit-purpose"`,
		`<dialog class="shell-dialog details-edit" id="details-edit-topic"`,
		`data-member-filter`,
		`action="/app/conversation/remove?channel=Cdev"`,
		// The requested tab is the server's rendering, so it is right
		// without script and before script runs: each tab is a link to its
		// own rendering, and only the selected panel is shown.
		`href="/app?channel=Cdev&amp;details=1&amp;tab=settings" aria-controls="details-settings" aria-selected="false" tabindex="-1"`,
		`aria-controls="details-members" aria-selected="true"`,
		`id="details-members" aria-labelledby="details-tab-members">`,
		`id="details-about" aria-labelledby="details-tab-about" hidden>`,
	)
	// A header inside a dialog, a sectioning root, is not a banner landmark;
	// outside one, only the top bar may be.
	outside := withoutDialogs(body)
	if count := strings.Count(outside, "<header"); count != 1 {
		t.Fatalf("the page has %d header elements outside dialogs; only the top bar may be a banner", count)
	}
	removed := postForm(t, mux, "/app/conversation/remove?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "user": {"U2"}}.Encode(), false)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body)
	}
	if got := removed.Header().Get("Location"); got != "/app?channel=Cdev&details=1&tab=members" {
		t.Fatalf("removing a member returned to %q, want the Members tab", got)
	}
	if member, _ := s.IsConversationMember(context.Background(), "Cdev", "U2"); member {
		t.Fatal("the member was not removed")
	}
	self := postForm(t, mux, "/app/conversation/remove?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "user": {"U1"}}.Encode(), false)
	if self.Code != http.StatusBadRequest {
		t.Fatalf("removing yourself status=%d, want 400", self.Code)
	}
}

// CONV-04: a channel's bookmarks bar adds a web link and refuses anything else;
// the Pins tab lists pinned messages.
func TestBookmarksBarAndPinsTab(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	refused := postForm(t, mux, "/app/bookmarks/add?channel=Cdev", url.Values{"_csrf": {csrf}, "title": {"Bad"}, "link": {"javascript:alert(1)"}}.Encode(), false)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("a script link status=%d, want 400", refused.Code)
	}
	added := postForm(t, mux, "/app/bookmarks/add?channel=Cdev", url.Values{"_csrf": {csrf}, "title": {"Handbook"}, "link": {"https://example.com/handbook"}}.Encode(), false)
	if added.Code != http.StatusSeeOther {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "bookmarks bar", body, `<ul class="bookmarks-bar" aria-label="Bookmarks">`, `href="https://example.com/handbook"`, "Handbook")
	id := regexp.MustCompile(`name="bookmark" value="([^"]+)"`).FindStringSubmatch(body)
	if id == nil {
		t.Fatal("the bookmark has no remove control")
	}
	if removed := postForm(t, mux, "/app/bookmarks/remove?channel=Cdev", url.Values{"_csrf": {csrf}, "bookmark": {id[1]}}.Encode(), false); removed.Code != http.StatusSeeOther {
		t.Fatalf("remove status=%d", removed.Code)
	}
	requireMissing(t, "bookmarks bar", get(t, mux, "/app?channel=Cdev").Body.String(), `<ul class="bookmarks-bar"`)

	message := seedMessage(t, s, "Mpinned", "pin me please", time.Unix(1700000000, 0).UTC())
	if err := (service.Messages{Store: s}).AddPin(context.Background(), "T1", "U1", "Cdev", domain.NewMessageTimestamp(message.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	pins := get(t, mux, "/app?channel=Cdev&tab=pins").Body.String()
	requireContains(t, "pins tab", pins, `id="pins"`, "pin me please", `href="/app?channel=Cdev&amp;tab=pins" aria-current="page"`)
	requireMissing(t, "pins tab", pins, `id="composer"`)
}

// withoutDialogs removes every <dialog> element, nested ones included, from
// the inside out: each closing tag pairs with the last opening tag before it.
func withoutDialogs(markup string) string {
	for {
		end := strings.Index(markup, "</dialog>")
		if end < 0 {
			return markup
		}
		start := strings.LastIndex(markup[:end], "<dialog")
		if start < 0 {
			return markup
		}
		markup = markup[:start] + markup[end+len("</dialog>"):]
	}
}
