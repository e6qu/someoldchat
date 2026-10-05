package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

func getWithCookies(t *testing.T, mux *http.ServeMux, target string, cookies ...*http.Cookie) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	addBrowserCookies(request)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, response.Code, response.Body)
	}
	return response.Body.String()
}

// articleFor returns one rendered message article by its data-ts.
func articleFor(t *testing.T, body string, timestamp domain.MessageTimestamp) string {
	t.Helper()
	marker := `data-ts="` + string(timestamp) + `"`
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("no message rendered for %s", timestamp)
	}
	start := strings.LastIndex(body[:at], "<article")
	end := strings.Index(body[at:], "</article>")
	return body[start : at+end]
}

// MSG-01: consecutive messages from one author within a few minutes are one
// group; the name and avatar come back after a pause, and the clock time and
// day divider follow the reader's own time zone.
func TestTimelineGroupsMessagesAndUsesTheReadersTimeZone(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	first := time.Date(2026, 9, 26, 23, 50, 0, 0, time.UTC)
	seedMessage(t, s, "M1", "first", first)
	seedMessage(t, s, "M2", "right after", first.Add(time.Minute))
	seedMessage(t, s, "M3", "much later", first.Add(20*time.Minute))

	body := getWithCookies(t, mux, "/app?channel=Cdev", &http.Cookie{Name: timezoneCookie, Value: "America/Los_Angeles"})
	firstArticle := articleFor(t, body, domain.NewMessageTimestamp(first))
	grouped := articleFor(t, body, domain.NewMessageTimestamp(first.Add(time.Minute)))
	later := articleFor(t, body, domain.NewMessageTimestamp(first.Add(20*time.Minute)))
	requireMissing(t, "first message", firstArticle, "is-continuation")
	requireContains(t, "grouped message", grouped, `class="message is-continuation"`, `class="gutter-time"`)
	requireMissing(t, "message after a pause", later, "is-continuation")
	// 23:50 UTC on the 26th is 4:50 PM on Saturday the 26th in Los Angeles.
	requireContains(t, "clock time", firstArticle, `title="Saturday, September 26th at 4:50:00 PM" data-format="time">4:50 PM<`)
	requireContains(t, "day divider", body, `datetime="2026-09-26" data-format="day"`)
	requireMissing(t, "day divider", body, `datetime="2026-09-27" data-format="day"`)
	requireMissing(t, "day divider", body, "12:00 AM")
}

// MSG-03: the editor opens in place of the message body, with Slack's Cancel
// and Save, for a reader following the no-script Edit message link too.
func TestEditMessageLinkOpensTheEditorInPlace(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "draft *text*", created)
	timestamp := domain.NewMessageTimestamp(created)

	closed := articleFor(t, get(t, mux, "/app?channel=Cdev").Body.String(), timestamp)
	requireContains(t, "closed editor", closed, `data-message-editor aria-label="Edit message" hidden`, `href="/app?channel=Cdev&amp;edit=`+url.QueryEscape(string(timestamp)))
	open := articleFor(t, get(t, mux, "/app?channel=Cdev&edit="+url.QueryEscape(string(timestamp))).Body.String(), timestamp)
	requireContains(t, "open editor", open, `class="message is-editing"`, `<div class="message-content" hidden>`, `>draft *text*</textarea>`, ">Cancel<", ">Save<", "autofocus")
	requireMissing(t, "open editor", open, `data-message-editor aria-label="Edit message" hidden`)
}

// MSG-04: Delete message is Slack's confirmation dialog, quoting the message.
// The page script opens it in place; the link opens it for a reader without
// script, and only for a message the reader may delete.
func TestDeleteAndForwardDialogsOpenForTheirMessage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "delete me", created)
	timestamp := url.QueryEscape(string(domain.NewMessageTimestamp(created)))

	body := get(t, mux, "/app?channel=Cdev&delete="+timestamp).Body.String()
	requireContains(t, "delete dialog", body,
		`id="delete-message-dialog"`, `aria-describedby="delete-message-body" open`,
		"Are you sure you want to delete this message? This cannot be undone.",
		`action="/app/message/delete?channel=Cdev&amp;ts=`+timestamp,
		`<div class="message-text">delete me</div>`, ">Cancel<", ">Delete<")

	forward := get(t, mux, "/app?channel=Cdev&forward="+timestamp).Body.String()
	requireContains(t, "forward dialog", forward,
		`id="forward-message-dialog"`, "Forward message", `placeholder="Search for channel or person"`,
		`<optgroup label="Channels"><option value="Cdev">#general</option>`, "Add a message", ">Copy link<", ">Forward<",
		`action="/app/message/forward?channel=Cdev&amp;ts=`+timestamp)

	// Someone else's message cannot be deleted, so its link opens nothing.
	other := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "not yours", CreatedAt: created.Add(time.Second)}
	if err := s.CreateMessage(context.Background(), other, events.Event{ID: "EM2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: other.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	refused := get(t, mux, "/app?channel=Cdev&delete="+url.QueryEscape(string(domain.NewMessageTimestamp(other.CreatedAt)))).Body.String()
	requireMissing(t, "someone else's delete dialog", refused, `aria-describedby="delete-message-body" open`)
}

// ACT-03: forwarding to a person opens the DM with them, and an in-place
// forward is confirmed where the member is instead of navigating away.
func TestForwardToAPersonStaysInPlace(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "grace", RealName: "Grace Hopper"})
	s.SeedConversationMember("Cdev", "U2")
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "worth sharing", created)
	timestamp := url.QueryEscape(string(domain.NewMessageTimestamp(created)))

	response := postForm(t, mux, "/app/message/forward?channel=Cdev&ts="+timestamp, url.Values{"_csrf": {auth.CSRFToken("session")}, "destination": {"user:U2"}, "comment": {"for you"}}.Encode(), true)
	if response.Code != http.StatusNoContent {
		t.Fatalf("forward=%d: %s", response.Code, response.Body)
	}
	if notice := response.Header().Get("X-SameOldChat-Notice"); !strings.HasPrefix(notice, "Message%20forwarded%20to%20") {
		t.Fatalf("forward notice=%q", notice)
	}
	opening, err := service.Messages{Store: s}.OpenConversation(context.Background(), "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	requireContains(t, "the DM", get(t, mux, "/app?channel="+string(opening.Conversation.ID)).Body.String(), "for you", "worth sharing")

	invalid := postForm(t, mux, "/app/message/forward?channel=Cdev&ts="+timestamp, url.Values{"_csrf": {auth.CSRFToken("session")}, "destination": {"user:"}}.Encode(), true)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("empty person forward=%d", invalid.Code)
	}
}

// ACT-01: Remind me about this offers Slack's times, and a reminder set from a
// message is confirmed in place; marking unread and following replies stay in
// place too.
func TestMessageMenuActionsCompleteInPlace(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Now().UTC().Add(-time.Minute)
	seedMessage(t, s, "M1", "remind me", created)
	timestamp := url.QueryEscape(string(domain.NewMessageTimestamp(created)))

	article := articleFor(t, get(t, mux, "/app?channel=Cdev").Body.String(), domain.NewMessageTimestamp(created))
	body := article[strings.Index(article, `class="message-actions"`):]
	requireOrdered(t, "reminder times", body, ">In 20 minutes<", ">In 1 hour<", ">In 3 hours<", ">Tomorrow<", ">Next week<", ">Custom…<")
	requireOrdered(t, "More actions", body, ">Mark unread<", ">Remind me about this<", ">Copy link<", ">Pin to channel<", ">Edit message<", ">Delete message…<")

	for _, preset := range []string{"3h", "nextweek"} {
		response := postForm(t, mux, "/app/reminders/create?channel=Cdev&ts="+timestamp, url.Values{"_csrf": {auth.CSRFToken("session")}, "preset": {preset}, "timezone": {"UTC"}}.Encode(), true)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s reminder=%d: %s", preset, response.Code, response.Body)
		}
		notice, _ := url.PathUnescape(response.Header().Get("X-SameOldChat-Notice"))
		if !strings.HasPrefix(notice, "Got it! We'll remind you about this message") {
			t.Fatalf("%s reminder notice=%q", preset, notice)
		}
	}
	unread := postForm(t, mux, "/app/read/unread?channel=Cdev&ts="+timestamp, url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), true)
	if unread.Code != http.StatusNoContent || unread.Header().Get("HX-Redirect") != "" {
		t.Fatalf("mark unread=%d redirect=%q", unread.Code, unread.Header().Get("HX-Redirect"))
	}
	follow := postForm(t, mux, "/app/thread/follow?channel=Cdev&thread="+timestamp, url.Values{"_csrf": {auth.CSRFToken("session")}, "followed": {"false"}}.Encode(), true)
	if follow.Code != http.StatusNoContent {
		t.Fatalf("unfollow=%d: %s", follow.Code, follow.Body)
	}
}

func TestNextWeekIsTheComingMondayMorning(t *testing.T) {
	sunday := time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC)
	request, err := personalReminderRequest(map[string]string{"preset": "nextweek", "timezone": "UTC"}, sunday)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC); !request.DueAt.Equal(want) {
		t.Fatalf("next week from Sunday=%s want %s", request.DueAt, want)
	}
	monday := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	request, _ = personalReminderRequest(map[string]string{"preset": "nextweek", "timezone": "UTC"}, monday)
	if want := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC); !request.DueAt.Equal(want) {
		t.Fatalf("next week from Monday=%s want %s", request.DueAt, want)
	}
}

// ACT-02: reaction pills keep the order emoji were first used and name who
// reacted; the toolbar leads with the reader's recent emoji.
func TestReactionPillsNameReactorsAndToolbarLeadsWithRecentEmoji(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "grace", RealName: "Grace Hopper"})
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "react to me", created)
	for index, reaction := range []domain.Reaction{{UserID: "U2", Name: "zap"}, {UserID: "U1", Name: "eyes"}, {UserID: "U2", Name: "eyes"}} {
		reaction.Message = "M1"
		reaction.CreatedAt = created.Add(time.Duration(index+1) * time.Second)
		if err := s.AddReaction(context.Background(), reaction, events.Event{ID: domain.EventID("ER" + string(rune('a'+index))), WorkspaceID: "T1", Topic: "reaction.added", Payload: "M1", CreatedAt: reaction.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	body := getWithCookies(t, mux, "/app?channel=Cdev", &http.Cookie{Name: recentEmojiCookie, Value: "rocket.%2B1.not-an-emoji-name-at-all"})
	article := articleFor(t, body, domain.NewMessageTimestamp(created))
	requireOrdered(t, "pills in first-use order", article, `title="Grace Hopper reacted with :zap:"`, `title="Grace Hopper and you reacted with :eyes:"`)
	// The member's own reactions lead, as Slack's follow the account; this
	// browser's recent emoji come after them.
	requireOrdered(t, "toolbar", article[strings.Index(article, `class="message-actions"`):], `data-quick-reaction aria-label="React with :eyes:"`, `aria-label="React with :rocket:"`, `aria-label="React with :thumbsup:"`, `aria-label="Add reaction"`, `aria-label="Reply in thread"`, `aria-label="Forward message"`, `aria-label="Save for later"`, `aria-label="More actions"`)
	requireContains(t, "add reaction pill", article, `class="chip add-reaction-chip"`)
}

// A browser the member has never used still offers their own recent
// reactions first, then Slack's defaults without repeating one.
func TestToolbarReactionsFollowTheMemberToANewBrowser(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "react to me", created)
	if err := s.AddReaction(context.Background(), domain.Reaction{Message: "M1", Name: "eyes", UserID: "U1", CreatedAt: created.Add(time.Second)},
		events.Event{ID: "ER1", WorkspaceID: "T1", Topic: "reaction.added", Payload: "M1", CreatedAt: created.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	article := articleFor(t, get(t, mux, "/app?channel=Cdev").Body.String(), domain.NewMessageTimestamp(created))
	toolbar := article[strings.Index(article, `class="message-actions"`):]
	requireOrdered(t, "toolbar", toolbar, `data-quick-reaction aria-label="React with :eyes:"`, `aria-label="React with :white_check_mark:"`, `aria-label="React with :raised_hands:"`, `aria-label="Add reaction"`)
	if strings.Count(toolbar, `data-quick-reaction aria-label="React with :eyes:"`) != 1 {
		t.Fatal("the toolbar offered :eyes: twice")
	}
}

// THREAD-01: the Threads view lists threads — messages with replies — with
// the root rendered as the timeline renders it and Slack's pluralisation.
func TestThreadsViewListsThreadsWithTheirLatestReplies(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	ctx := context.Background()
	if _, err := messages.Post(ctx, "T1", "U1", "Cdev", "a lonely message", "", ""); err != nil {
		t.Fatal(err)
	}
	root, err := messages.Post(ctx, "T1", "U1", "Cdev", "*ship* it?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "Cdev", Text: "yes, `today`", ThreadTimestamp: domain.NewMessageTimestamp(root.CreatedAt)}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app/threads?channel=Cdev").Body.String()
	requireContains(t, "threads view", body, "<strong>ship</strong> it?", "yes, <code>today</code>", "1 reply", "Last reply", "data-thread-reply-slot", `placeholder="Reply…"`, "#general", "Reply to the thread in #general")
	requireMissing(t, "threads view", body, "a lonely message", "0 replies", "Jan 1", "1 replies")
}

// NAV-07: a Threads card hosts the thread composer, as Slack's Threads view
// does: its own saved draft, mention suggestions from the card's conversation,
// and attachments. The reply lands in the thread and the post answers with
// the Threads view at that card, with script and without; a return outside
// this application is not followed.
func TestThreadsCardRepliesInPlace(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	ctx := context.Background()
	root, err := messages.Post(ctx, "T1", "U1", "Cdev", "ship it?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "Cdev", Text: "first", ThreadTimestamp: thread}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SaveDraft(ctx, "T1", "U1", "Cdev", thread, "half a thought"); err != nil {
		t.Fatal(err)
	}
	anchor := "thread-Cdev-" + string(thread)
	body := get(t, mux, "/app/threads?channel=Cdev").Body.String()
	form := body[strings.Index(body, `id="`+anchor+`-composer"`):]
	form = form[:strings.Index(form, "</form>")]
	action := "/app/message?channel=Cdev&amp;thread=" + url.QueryEscape(string(thread))
	returnTo := "/app/threads?channel=Cdev#" + anchor
	requireContains(t, "card composer", form, `method="post"`, `action="`+action+`"`, `data-composer="thread"`, `data-directory="composer-directory-Cdev"`, "data-composer-quiet",
		`name="thread_ts" value="`+string(thread)+`"`, `name="return" value="`+returnTo+`"`, `name="text"`, `for="`+anchor+`-text"`, "Reply to the thread in #general",
		">half a thought</textarea>", `data-draft-url="/app/draft?channel=Cdev&amp;thread=`, `data-composer-action="upload"`)
	requireMissing(t, "card composer", form, "autofocus", `aria-controls="emoji-picker"`, `aria-controls="shortcut-browser"`)
	// The card's mention suggestions are its conversation's, rendered once.
	requireContains(t, "threads page", body, `<template id="composer-directory-Cdev"`, `id="composer-link-dialog"`, `id="live-status"`)
	if strings.Count(body, `<template id="composer-directory-Cdev"`) != 1 {
		t.Fatal("a conversation's suggestions were rendered more than once")
	}

	// With script, the composer posts as htmx does and is sent back to the
	// card rather than handed a timeline fragment for a page with no timeline.
	target := "/app/message?channel=Cdev&thread=" + url.QueryEscape(string(thread))
	viaScript := postForm(t, mux, target, url.Values{"_csrf": {auth.CSRFToken("session")}, "thread_ts": {string(thread)}, "return": {returnTo}, "text": {"from the card's composer"}}.Encode(), true)
	if viaScript.Code != http.StatusNoContent || viaScript.Header().Get("HX-Redirect") != returnTo {
		t.Fatalf("scripted reply = %d to %q: %s", viaScript.Code, viaScript.Header().Get("HX-Redirect"), viaScript.Body)
	}
	if _, err := messages.Draft(ctx, "T1", "U1", "Cdev", thread); err == nil {
		t.Fatal("the card's draft outlived the reply it became")
	}

	sent := postForm(t, mux, target, url.Values{"_csrf": {auth.CSRFToken("session")}, "thread_ts": {string(thread)}, "return": {returnTo}, "text": {"from the card"}}.Encode(), false)
	if sent.Code != http.StatusSeeOther || sent.Header().Get("Location") != returnTo {
		t.Fatalf("reply = %d to %q: %s", sent.Code, sent.Header().Get("Location"), sent.Body)
	}
	replies, err := messages.Replies(ctx, "T1", "U1", "Cdev", thread, domain.ThreadRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if last := replies.Messages[len(replies.Messages)-1]; last.Text != "from the card" || last.ThreadTimestamp != thread {
		t.Fatalf("the card's reply did not land in the thread: %+v", last)
	}

	offsite := postForm(t, mux, target, url.Values{"_csrf": {auth.CSRFToken("session")}, "thread_ts": {string(thread)}, "return": {"//evil.example/app"}, "text": {"again"}}.Encode(), false)
	if location := offsite.Header().Get("Location"); offsite.Code != http.StatusSeeOther || !strings.HasPrefix(location, "/app?") {
		t.Fatalf("an off-site return was followed: %d to %q", offsite.Code, location)
	}
}

// The emoji picker browses a whole category, and names the thumbs as Slack
// does while submitting the catalog name.
func TestEmojiOptionsBrowseACategory(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := get(t, mux, "/app/emoji/options?category="+url.QueryEscape("People & Body")+"&limit=2000")
	if response.Code != http.StatusOK {
		t.Fatalf("options=%d: %s", response.Code, response.Body)
	}
	var payload struct {
		Options []emojiOptionView `json:"options"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Options) <= 60 {
		t.Fatalf("a category page held %d emoji; the picker must be able to browse all of it", len(payload.Options))
	}
	found := false
	for _, option := range payload.Options {
		if option.Name == "+1" {
			found = option.Label == "thumbsup"
		}
	}
	if !found {
		t.Fatal(`"+1" is not labelled :thumbsup:`)
	}
	if bad := get(t, mux, "/app/emoji/options?limit=0"); bad.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 status=%d", bad.Code)
	}
}

// A bot's icon_emoji is its avatar, drawn as the emoji rather than its code.
func TestCustomIconEmojiIsRenderedAsTheAvatar(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	state, _ := json.Marshal(domain.MessageStreamState{Username: "Release Train", IconEmoji: ":steam_locomotive:"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: "choo", CreatedAt: created, StreamState: string(state)}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "EM1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	article := articleFor(t, get(t, mux, "/app?channel=Cdev").Body.String(), domain.NewMessageTimestamp(created))
	requireContains(t, "icon emoji avatar", article, `class="avatar avatar-emoji"`, `aria-label=":steam_locomotive:">🚂</span>`, "Release Train")
	requireMissing(t, "icon emoji avatar", article, `aria-hidden="true">:steam_locomotive:`)
}

// A message mentioning the reader is highlighted, and so is the mention.
func TestMentionsOfTheReaderAreHighlighted(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "hey <@U1>, see <!here>", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "EM1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	article := articleFor(t, get(t, mux, "/app?channel=Cdev").Body.String(), domain.NewMessageTimestamp(created))
	requireContains(t, "mention", article, "mentions-me", `data-user-id="U1" data-self="true">@Ada Developer</a>`, `class="slack-mention mention-broadcast">@here<`)
}

// ACT-02: "Add emoji" in the picker leads an administrator to the workspace's
// custom emoji, where one can be added and is then usable in a message; a
// member sees the list and is refused the change.
func TestCustomEmojiAreManagedFromTheWorkspace(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	member := get(t, mux, "/app/customize/emoji?channel=Cdev").Body.String()
	requireContains(t, "member view", member, "Only workspace admins and owners can add or remove custom emoji.")
	refused := postForm(t, mux, "/app/customize/emoji/add?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "name": {"partyparrot"}, "url": {"https://cdn.example/parrot.gif"}}.Encode(), false)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("member add=%d", refused.Code)
	}
	requireMissing(t, "member picker", get(t, mux, "/app?channel=Cdev").Body.String(), ">Add emoji<")

	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	requireContains(t, "admin picker", get(t, mux, "/app?channel=Cdev").Body.String(), `href="/app/customize/emoji?channel=Cdev">Add emoji<`)
	added := postForm(t, mux, "/app/customize/emoji/add?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "name": {":partyparrot:"}, "url": {"https://cdn.example/parrot.gif"}}.Encode(), false)
	if added.Code != http.StatusSeeOther {
		t.Fatalf("admin add=%d: %s", added.Code, added.Body)
	}
	requireContains(t, "emoji page", get(t, mux, "/app/customize/emoji?channel=Cdev&notice=Added").Body.String(), `alt=":partyparrot:"`, `aria-label="Remove :partyparrot:"`)
	invalid := postForm(t, mux, "/app/customize/emoji/add", url.Values{"_csrf": {auth.CSRFToken("session")}, "name": {"bad"}, "url": {"javascript:alert(1)"}}.Encode(), false)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid add=%d", invalid.Code)
	}
	seedMessage(t, s, "M1", "celebrate :partyparrot:", time.Unix(1700000000, 0).UTC())
	requireContains(t, "custom emoji in a message", get(t, mux, "/app?channel=Cdev").Body.String(), `class="custom-emoji" src="https://cdn.example/parrot.gif" alt=":partyparrot:"`)
	removed := postForm(t, mux, "/app/customize/emoji/remove", url.Values{"_csrf": {auth.CSRFToken("session")}, "name": {"partyparrot"}}.Encode(), false)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("remove=%d: %s", removed.Code, removed.Body)
	}
}
