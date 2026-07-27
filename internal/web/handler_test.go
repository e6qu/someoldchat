package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// addBrowserCookies makes a request look like the one a current browser sends.
//
// Sec-Fetch-Site is part of that shape and it is set here rather than in the
// two tests that care: without it every request in this package travelled the
// "neither Sec-Fetch-Site nor Origin, so this is not a browser" fall-through in
// auth.validateRequestSite, and the branch a real browser takes was exercised
// by exactly one test. A caller that is deliberately testing another site sets
// the header first, and this leaves it alone.
func addBrowserCookies(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	token := auth.CSRFToken("session")
	request.Header.Set(auth.CSRFTokenHeaderName, token)
	if request.Header.Get("Sec-Fetch-Site") == "" {
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
}

// browserWorkspace seeds the smallest workspace a browser journey needs and
// returns a mux with the web handler registered.
func browserWorkspace(t *testing.T, scopes []string) (*memory.Store, *http.ServeMux) {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer", RealName: "Ada Developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general", Topic: "Everything else"})
	// Posting, and marking read, require membership of the conversation.
	s.SeedConversationMember("Cdev", "U1")
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: scopes, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return s, mux
}

func seedMessage(t *testing.T, s *memory.Store, id domain.MessageID, text string, createdAt time.Time) domain.Message {
	t.Helper()
	message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: text, CreatedAt: createdAt}
	event := events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: createdAt}
	if err := s.CreateMessage(context.Background(), message, event, ""); err != nil {
		t.Fatal(err)
	}
	return message
}

func get(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func postForm(t *testing.T, mux *http.ServeMux, target, body string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func requireContains(t *testing.T, what, body string, expected ...string) {
	t.Helper()
	for _, value := range expected {
		if !strings.Contains(body, value) {
			t.Fatalf("%s is missing %q: %s", what, value, body)
		}
	}
}

func requireMissing(t *testing.T, what, body string, unexpected ...string) {
	t.Helper()
	for _, value := range unexpected {
		if strings.Contains(body, value) {
			t.Fatalf("%s still contains %q: %s", what, value, body)
		}
	}
}

// TestThreadViewRendersTheThreadAndItsComposer covers the defect that made
// every "Reply in thread" link answer 503: the message partial was invoked with
// one type for the timeline and another for the thread pane, so the pane could
// not resolve the CSRF token and the whole page render failed.
func TestThreadViewRendersTheThreadAndItsComposer(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "thread root", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	response := get(t, mux, "/app?channel=Cdev&thread="+timestamp)
	if response.Code != http.StatusOK {
		t.Fatalf("thread view status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "thread view", body,
		`<h2 id="thread-heading">Thread</h2>`,
		`id="thread-messages"`,
		`name="thread_ts" value="`+timestamp+`"`,
		`<button class="send" type="submit">Send</button>`,
		"thread root",
	)
	// The reply composer has to post into the thread pane, not the channel
	// timeline, or a reply never appears where it was written.
	requireContains(t, "thread composer", body, `hx-target="#thread-messages"`)
	// The pane repeats a message the timeline already shows, so the document must
	// not carry the same identifier twice.
	requireContains(t, "thread pane", body, `id="thread-message-M1"`)
	if strings.Count(body, `id="message-M1"`) != 1 {
		t.Fatalf("message anchor is duplicated %d times: %s", strings.Count(body, `id="message-M1"`), body)
	}
}

// TestThreadRepliesRenderThroughTheSameTypeAsTheTimeline pins the invariant
// behind the fix: one type feeds every message region, so no region can be
// rendered with a value that is missing a field the partial needs.
func TestThreadRepliesRenderThroughTheSameTypeAsTheTimeline(t *testing.T) {
	list := messageList{
		Messages:    []messageView{{ID: "M1", AuthorName: "developer", AuthorInitial: "D", Text: "hello", Anchor: "message-M1"}},
		ChannelName: "general",
		CSRFToken:   "token",
		CanReact:    true,
		CanPin:      true,
	}
	var timeline, thread bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&timeline, "messages", list); err != nil {
		t.Fatalf("timeline fragment: %v", err)
	}
	if err := pageTemplate.ExecuteTemplate(&thread, "messages", list); err != nil {
		t.Fatalf("thread fragment: %v", err)
	}
	if timeline.String() != thread.String() || !strings.Contains(timeline.String(), `value="token"`) {
		t.Fatalf("timeline=%s thread=%s", timeline.String(), thread.String())
	}
}

// TestTimelineRendersTheNewestMessagesWithWorkingPagination covers the defect
// that made recent history unreachable: the page asked for the first 100
// messages of an ascending conversation and discarded the cursor.
func TestTimelineRendersTheNewestMessagesWithWorkingPagination(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChannelsHistory)})
	base := time.Unix(1700000000, 0).UTC()
	for index := 1; index <= 130; index++ {
		seedMessage(t, s, domain.MessageID(fmt.Sprintf("M%03d", index)), fmt.Sprintf("note-%03d", index), base.Add(time.Duration(index)*time.Second))
	}
	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "latest timeline", body, "note-130", "note-081", "Show older messages")
	requireMissing(t, "latest timeline", body, "note-001", "note-080", "Jump to the latest messages")

	older := regexp.MustCompile(`<a href="(/app\?before=[^"]+)">Show older messages</a>`).FindStringSubmatch(body)
	if older == nil {
		t.Fatalf("no older-history link: %s", body)
	}
	previous := get(t, mux, strings.ReplaceAll(older[1], "&amp;", "&"))
	if previous.Code != http.StatusOK {
		t.Fatalf("older history status=%d body=%s", previous.Code, previous.Body)
	}
	requireContains(t, "older history", previous.Body.String(), "note-031", "note-080", "Jump to the latest messages", "Show older messages")
	requireMissing(t, "older history", previous.Body.String(), "note-130", "note-081")
}

// TestUnknownChannelIsReportedAsNotFound covers the defect that answered a
// stale bookmark with a 503 blaming the message store.
func TestUnknownChannelIsReportedAsNotFound(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := get(t, mux, "/app?channel=Cmissing")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "not-found page", response.Body.String(), "That conversation is not available", `href="/app"`)
}

// TestWorkspaceShellNamesConversationsAndAuthors covers the defect that put raw
// identifiers where names belong: the header, the document title, the composer
// placeholder, the author line and the avatar tile.
func TestWorkspaceShellNamesConversationsAndAuthors(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "workspace shell", body,
		`<h1 class="channel-title"># general</h1>`,
		"<title>#general · SameOldChat</title>",
		`placeholder="Message #general"`,
		`<span class="author">Ada Developer</span>`,
		`<div class="avatar" aria-hidden="true">A</div>`,
		`<span class="signed-in-avatar" aria-hidden="true">A</span>`,
	)
	requireMissing(t, "workspace shell", body, "# Cdev", "Message #Cdev", ">U1<")
	// The machine timestamp stays in datetime= while the reader sees a short time.
	requireContains(t, "message time", body, `datetime="2023-11-14T22:13:20Z">Nov 14, 22:13 UTC<`)
}

// A public channel may be read before it is joined, but every conversational
// mutation requires membership. The workspace used to ignore that distinction:
// it rendered a working-looking composer, reaction inputs, pin controls, and an
// automatic read marker, then refused each action after the user tried it.
func TestPublicChannelPreviewJoinsBeforeOfferingMutationControls(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "readable before joining", time.Unix(1700000000, 0).UTC())
	if err := (service.Messages{Store: s}).LeaveConversation(context.Background(), "T1", "U1", "Cdev"); err != nil {
		t.Fatal(err)
	}

	preview := get(t, mux, "/app?channel=Cdev")
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body)
	}
	requireContains(t, "public-channel preview", preview.Body.String(),
		"readable before joining",
		`<span class="membership-pill">Not joined</span>`,
		`action="/app/join?channel=Cdev"`,
		"Join channel",
		"View thread",
	)
	requireMissing(t, "public-channel preview", preview.Body.String(),
		`id="composer"`,
		`id="mark-read"`,
		`aria-label="Add reaction"`,
		`>Pin</button>`,
	)

	joined := postForm(t, mux, "/app/join?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if joined.Code != http.StatusSeeOther {
		t.Fatalf("join status=%d body=%s", joined.Code, joined.Body)
	}
	workspace := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "joined channel", workspace.Body.String(),
		`<span class="membership-pill joined">Joined</span>`,
		`id="composer"`,
		"Reply in thread",
	)
}

// TestSidebarSeparatesDirectMessagesAndClearsTheOpenChannelBadge covers two
// defects: every direct conversation was listed as "# direct" among the
// channels, and the channel being read kept its own unread badge.
func TestSidebarSeparatesDirectMessagesAndClearsTheOpenChannelBadge(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	s.SeedConversation(domain.Conversation{ID: "Cother", WorkspaceID: "T1", Name: "release"})
	s.SeedConversation(domain.Conversation{ID: "Cdm", WorkspaceID: "T1", Name: "direct", IsDirect: true, IsPrivate: true})
	s.SeedConversationMember("Cdm", "U1")
	s.SeedConversationMember("Cdm", "U2")
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	other := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cother", AuthorID: "U1", Text: "unread one", CreatedAt: time.Unix(1700000100, 0).UTC()}
	if err := s.CreateMessage(context.Background(), other, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: other.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	response := get(t, mux, "/app?channel=Cdev")
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	requireContains(t, "sidebar", body,
		`aria-label="Direct messages"`,
		`aria-label="Bob Builder"`,
		`aria-label="release, 1 unread messages"`,
	)
	requireMissing(t, "sidebar", body, `>direct<`, `aria-label="general, `)
}

// TestSidebarLinksKeepAnAccessibleNameWhenTheyCollapse replaces an assertion on
// a literal CSS string with the contract that assertion was standing in for: at
// a narrow viewport the sidebar hides its labels, so every link and the sign-out
// control must carry a name of its own, and the icons must survive.
func TestSidebarLinksKeepAnAccessibleNameWhenTheyCollapse(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	tags := regexp.MustCompile(`<(?:a|button)[^>]*class="side-link"[^>]*>`).FindAllString(body, -1)
	if len(tags) < 3 {
		t.Fatalf("expected the members, channel and sign-out controls, found %d: %s", len(tags), body)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, "aria-label=") {
			t.Fatalf("collapsed sidebar control has no accessible name: %s", tag)
		}
	}
	narrow := body[strings.Index(body, "@media(max-width:800px)"):]
	narrow = narrow[:strings.Index(narrow, "</style>")]
	collapse := regexp.MustCompile(`(?m)^([^{}\n]+)\{display:none\}`).FindStringSubmatch(narrow)
	if collapse == nil || !strings.Contains(collapse[1], ".side-text") {
		t.Fatalf("narrow viewport does not collapse the sidebar labels: %q", narrow)
	}
	if strings.Contains(collapse[1], ".side-icon") {
		t.Fatalf("narrow viewport hides the sidebar icons as well: %q", collapse[1])
	}
	if strings.Contains(body, ".thread{display:none}") {
		t.Fatal("narrow viewports delete the thread pane instead of reflowing it")
	}
}

// TestReactionsAndPinsAreRenderedAndReversible covers the defect where both
// controls mutated durable state that no template ever displayed.
func TestReactionsAndPinsAreRenderedAndReversible(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	reaction := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=%3Awave%3A", true)
	if reaction.Code != http.StatusNoContent {
		t.Fatalf("reaction status=%d body=%s", reaction.Code, reaction.Body)
	}
	pin := postForm(t, mux, "/app/pin?channel=Cdev&ts="+timestamp, "", false)
	if pin.Code != http.StatusSeeOther {
		t.Fatalf("pin status=%d body=%s", pin.Code, pin.Body)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "message", body,
		`aria-pressed="true"`,
		":wave:",
		`<span class="chip-count">1</span>`,
		`<span class="pinned">Pinned</span>`,
		">Unpin<",
	)

	removal := postForm(t, mux, "/app/reaction/remove?channel=Cdev&ts="+timestamp, "name=%3Awave%3A", true)
	if removal.Code != http.StatusNoContent {
		t.Fatalf("reaction removal status=%d body=%s", removal.Code, removal.Body)
	}
	unpin := postForm(t, mux, "/app/pin/remove?channel=Cdev&ts="+timestamp, "", true)
	if unpin.Code != http.StatusNoContent {
		t.Fatalf("unpin status=%d body=%s", unpin.Code, unpin.Body)
	}
	after := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "message", after, `<span class="chip-count">`, `<span class="pinned">Pinned</span>`)
	reactions, _, _, err := s.ListReactions(context.Background(), "M1", domain.PageRequest{Limit: 10})
	if err != nil || len(reactions) != 0 {
		t.Fatalf("reactions=%+v err=%v", reactions, err)
	}
}

// Editing and deleting were complete Slack API operations with no browser
// journey. Rendering the controls only on the signed-in author's messages is a
// convenience; the mutation handlers also enforce ownership server-side.
func TestOwnMessageCanBeEditedAndDeleted(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "before", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "own message", body,
		`aria-label="Edit message"`,
		`action="/app/message/update?channel=Cdev&amp;ts=`+timestamp,
		`aria-label="Delete message"`,
	)

	updated := postForm(t, mux, "/app/message/update?channel=Cdev&ts="+timestamp, "text=after", true)
	if updated.Code != http.StatusNoContent {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	message, err := s.GetMessage(context.Background(), "M1")
	if err != nil || message.Text != "after" {
		t.Fatalf("updated message=%+v err=%v", message, err)
	}

	deleted := postForm(t, mux, "/app/message/delete?channel=Cdev&ts="+timestamp, "", true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body)
	}
	after := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "deleted message", after, "after", `data-message-id="M1"`)
}

func TestAnotherMembersMessageCannotBeChanged(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	created := time.Unix(1700000000, 123456000).UTC()
	message := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "belongs to Bob", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	timestamp := string(domain.NewMessageTimestamp(created))

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "another member's message", body, `action="/app/message/update?channel=Cdev&amp;ts=`+timestamp, `action="/app/message/delete?channel=Cdev&amp;ts=`+timestamp)
	refused := postForm(t, mux, "/app/message/update?channel=Cdev&ts="+timestamp, "text=stolen", true)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("update status=%d body=%s", refused.Code, refused.Body)
	}
	stored, err := s.GetMessage(context.Background(), "M2")
	if err != nil || stored.Text != "belongs to Bob" {
		t.Fatalf("message=%+v err=%v", stored, err)
	}
}

// Channel creation existed at /api/conversations.create but the signed-in
// workspace did not expose it, leaving users dependent on an API client for a
// basic product journey.
func TestWorkspaceCanCreateAChannel(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "workspace", body, `action="/app/conversation/create"`, `name="is_private"`, "Add channel")

	created := postForm(t, mux, "/app/conversation/create", "name=Product+Launch&is_private=true", false)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	location := created.Header().Get("Location")
	target, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	channel := domain.ConversationID(target.Query().Get("channel"))
	conversation, err := s.GetConversation(context.Background(), channel)
	if err != nil || conversation.Name != "product-launch" || !conversation.IsPrivate {
		t.Fatalf("conversation=%+v err=%v", conversation, err)
	}
	member, err := s.IsConversationMember(context.Background(), channel, "U1")
	if err != nil || !member {
		t.Fatalf("member=%t err=%v", member, err)
	}
}

// TestReactionKeepsTheOpenViewInsteadOfNavigating covers the defect where a
// reaction always answered with a full-page redirect that dropped the open
// thread and the composer draft, while declaring HTMX attributes that the
// server never honoured.
func TestReactionKeepsTheOpenViewInsteadOfNavigating(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	enhanced := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp+"&thread="+timestamp, "name=%3Awave%3A", true)
	if enhanced.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", enhanced.Code, enhanced.Body)
	}
	if redirect := enhanced.Header().Get("HX-Redirect"); redirect != "" {
		t.Fatalf("enhanced reaction still navigates the whole page to %q", redirect)
	}
	plain := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp+"&thread="+timestamp, "name=%3Atada%3A", false)
	if plain.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", plain.Code, plain.Body)
	}
	location := plain.Header().Get("Location")
	if !strings.Contains(location, "thread="+timestamp) || !strings.Contains(location, "channel=Cdev") {
		t.Fatalf("reaction redirect dropped the open thread: %q", location)
	}
}

// TestTimelineFragmentServesTheLiveRegion covers the live-delivery fix: the page
// re-renders the message regions the server owns instead of reloading and
// discarding the composer draft and the scroll position.
func TestTimelineFragmentServesTheLiveRegion(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	page := get(t, mux, "/app?channel=Cdev&thread="+timestamp).Body.String()
	for _, fragment := range regexp.MustCompile(`data-fragment="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		target := strings.ReplaceAll(fragment[1], "&amp;", "&")
		response := get(t, mux, target)
		if response.Code != http.StatusOK {
			t.Fatalf("fragment %s status=%d body=%s", target, response.Code, response.Body)
		}
		requireContains(t, "fragment "+target, response.Body.String(), `class="message"`, "hello")
		requireMissing(t, "fragment "+target, response.Body.String(), "<html", "<body")
	}
	if !strings.Contains(page, `data-live="true"`) {
		t.Fatalf("the timeline is not marked live: %s", page)
	}
}

// TestLiveUpdatesSubscribeToExactlyTheEmittedTopics covers the defect where the
// page listened for "message.updated", which nothing publishes, and therefore
// never saw an edit.
func TestLiveUpdatesSubscribeToExactlyTheEmittedTopics(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("Cdev", "U1")
	chat := service.Messages{Store: s}
	message, err := chat.Post(ctx, "T1", "U1", "Cdev", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	if _, err := chat.Update(ctx, "T1", "U1", "Cdev", timestamp, "hello again"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Unfurl(ctx, "T1", "U1", "Cdev", timestamp, map[string]string{"https://example.test": `{"title":"x"}`}); err != nil {
		t.Fatal(err)
	}
	if err := chat.AddReaction(ctx, "T1", "U1", "Cdev", timestamp, ":wave:"); err != nil {
		t.Fatal(err)
	}
	if err := chat.RemoveReaction(ctx, "T1", "U1", "Cdev", timestamp, ":wave:"); err != nil {
		t.Fatal(err)
	}
	if err := chat.AddPin(ctx, "T1", "U1", "Cdev", timestamp); err != nil {
		t.Fatal(err)
	}
	if err := chat.RemovePin(ctx, "T1", "U1", "Cdev", timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Delete(ctx, "T1", "U1", "Cdev", timestamp); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]bool{}
	for _, record := range records {
		topic := record.Event.Topic
		if strings.HasPrefix(topic, "message.") || strings.HasPrefix(topic, "reaction.") || strings.HasPrefix(topic, "pin.") {
			emitted[topic] = true
		}
	}
	subscribed := map[string]bool{}
	for _, topic := range liveEventTopics {
		subscribed[topic] = true
		if !strings.Contains(progressiveEnhancementScript, "'"+topic+"'") {
			t.Fatalf("the page script does not subscribe to %q", topic)
		}
		if !emitted[topic] {
			t.Fatalf("the page subscribes to %q, which no chat mutation publishes", topic)
		}
	}
	for topic := range emitted {
		if !subscribed[topic] {
			t.Fatalf("the chat service publishes %q and the page ignores it", topic)
		}
	}
}

// TestTheDocumentAndItsContentSecurityPolicyAgree replaces a test that grepped
// the JavaScript source for twelve substrings.
//
// That test proved no behaviour: it passed on a script with no submit lock, no
// coalescing and no stream error handling, and it would have failed on the fix
// for any of them, because renaming a variable broke it. What it should have
// asserted is the contract the page depends on — that the script the document
// carries is the script the policy allows to run. A hash the policy does not
// cover silently disables the whole client in the browser and in nothing else,
// which is precisely the failure no unit test would otherwise see.
func TestTheDocumentAndItsContentSecurityPolicyAgree(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, target := range []string{"/app?channel=Cdev", "/app/members", "/app/search?q=hello"} {
		response := get(t, mux, target)
		policy := response.Header().Get("Content-Security-Policy")
		if policy == "" {
			t.Fatalf("%s carries no content security policy", target)
		}
		bodies := inlineScriptBodies(response.Body.String())
		if len(bodies) == 0 {
			t.Fatalf("%s renders no inline script", target)
		}
		for _, body := range bodies {
			digest := sha256.Sum256([]byte(body))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if !strings.Contains(policy, hash) {
				t.Fatalf("%s serves an inline script the policy blocks: %s\npolicy=%s", target, hash, policy)
			}
		}
		if strings.Contains(policy, "'unsafe-inline'; script-src") || strings.Contains(policy, "script-src 'unsafe-inline'") {
			t.Fatalf("%s allows any inline script: %s", target, policy)
		}
	}
}

// TestTheInlineScriptParserRefusesWhatItCannotHash covers the shared blind spot
// of the policy builder and the test above: both read the document through
// inlineScriptBodies, which searched for the literal "<script>". A future
// `<script type="module">` would have been invisible to both, so the policy
// would omit its hash, the test would stay green, and the browser would block
// the script. A tag this cannot hash is now a build-time failure.
func TestTheInlineScriptParserRefusesWhatItCannotHash(t *testing.T) {
	for _, document := range []string{
		`<body><script type="module">alert(1)</script></body>`,
		`<body><script defer>alert(1)</script></body>`,
		`<body><script src="/x.js"></script></body>`,
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("inlineScriptBodies silently skipped a script it cannot hash: %s", document)
				}
			}()
			inlineScriptBodies(document)
		}()
	}
	if bodies := inlineScriptBodies(`<script>one</script>text<script>two</script>`); len(bodies) != 2 || bodies[0] != "one" || bodies[1] != "two" {
		t.Fatalf("plain inline scripts no longer parse: %q", bodies)
	}
}

// TestNewActivityIsAnnouncedForTheRegionItLandedIn covers two defects in the
// live-status announcement. messageCount() counted `#timeline .message` while
// refresh() re-renders every live region, so a reply arriving in an open thread
// pane — the one region that is live unconditionally — measured zero arrivals
// and was never announced. And the "New activity is available" fallback ran
// only when *no* region was live, so a reader in older history with a thread
// open (thread live, timeline not) got neither the count nor the fallback.
func TestNewActivityIsAnnouncedForTheRegionItLandedIn(t *testing.T) {
	requireContains(t, "client", progressiveEnhancementScript,
		`function messageCount(){return document.querySelectorAll('[data-fragment] .message').length}`,
		`var behind=document.querySelectorAll('[data-fragment]:not([data-live="true"])').length>0;`,
		`if(behind)announce('New activity is available in this conversation.');`,
	)
	requireMissing(t, "client", progressiveEnhancementScript, `document.querySelectorAll('#timeline .message')`)
}

// TestTheClientNeverFetchesAnOriginItWasNotGiven pins the one property of the
// client that no browser test can observe until it is already too late: every
// URL it fetches with credentials has to be a path on this origin. `hx-post`
// and `data-fragment` are not URL attributes to html/template and receive no
// filtering, so an absolute value would become a credentialed request to
// another host.
func TestTheClientNeverFetchesAnOriginItWasNotGiven(t *testing.T) {
	if !strings.Contains(progressiveEnhancementScript, "function ownPath(value){return typeof value==='string'&&value.charAt(0)==='/'&&value.charAt(1)!=='/'}") {
		t.Fatal("the client does not constrain the URLs it fetches")
	}
	_, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	for _, attribute := range []string{"hx-post", "data-fragment", "data-newest"} {
		for _, match := range regexp.MustCompile(attribute+`="([^"]*)"`).FindAllStringSubmatch(body, -1) {
			value := match[1]
			if value == "" {
				continue
			}
			if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
				t.Fatalf("%s renders an off-origin target: %q", attribute, value)
			}
		}
	}
}

// TestWorkspacePagesCarryTheSameProtectionAsTheAdminPage covers the defect that
// left every workspace page framable and cacheable: the sibling admin page in
// this package set all five headers and the pages that render a conversation
// and a live CSRF token set none, so one click in an invisible frame landed on
// Sign out or Pin, and Back after sign-out replayed the conversation.
//
// It used to visit successful pages and one 404 only, which is how three whole
// classes of response kept answering with no headers at all: every fragment
// failure and every insufficient-scope refusal (bare http.Error), and the two
// signed-out entry pages, /login and /signed-out — the second of which carries
// a "Sign in with Shauth" link, so framing it starts an authorization flow the
// victim did not choose. Failures are now covered alongside successes.
func TestWorkspacePagesCarryTheSameProtectionAsTheAdminPage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	requireProtected := func(target string, header http.Header) {
		t.Helper()
		for name, expected := range map[string]string{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"Cache-Control":          "no-store",
		} {
			if header.Get(name) != expected {
				t.Fatalf("%s %s=%q, want %q", target, name, header.Get(name), expected)
			}
		}
		if !strings.Contains(header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is framable: %q", target, header.Get("Content-Security-Policy"))
		}
	}
	for _, target := range []string{
		"/app?channel=Cdev",
		"/app/timeline?channel=Cdev",
		"/app/members",
		"/app/search?q=hello&channel=Cdev",
		"/app?channel=Cmissing",
		// Failures. Each of these answered with a bare line of text and no
		// header at all.
		"/app/timeline?channel=Cmissing",
		"/app/timeline?channel=Cdev&thread=not-a-timestamp",
		"/app?channel=Cdev&before=not-a-cursor",
		"/signed-out",
	} {
		requireProtected(target, get(t, mux, target).Header())
	}

	// An insufficient scope is refused by writeAuthError, which is the same
	// bare-text path.
	_, limited := browserWorkspace(t, []string{string(auth.ScopeChatWrite)})
	response := get(t, limited, "/app/timeline?channel=Cdev")
	if response.Code != http.StatusForbidden {
		t.Fatalf("insufficient scope status=%d", response.Code)
	}
	requireProtected("/app/timeline with no history scope", response.Header())
}

// TestTheSignedOutEntryPagesAreNotFramable covers /login, which is only
// registered when a provider is configured and therefore is not reachable from
// browserWorkspace.
func TestTheSignedOutEntryPagesAreNotFramable(t *testing.T) {
	login, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret",
		AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token",
		UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	login.Register(mux)
	for _, target := range []string{"/login"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", target, response.Code)
		}
		header := response.Header()
		if header.Get("X-Frame-Options") != "DENY" || !strings.Contains(header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is framable: X-Frame-Options=%q CSP=%q", target, header.Get("X-Frame-Options"), header.Get("Content-Security-Policy"))
		}
		if header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s does not forbid sniffing", target)
		}
	}
}

// TestMutationsAreRefusedWhenTheBrowserReportsAnotherSite is the web half of
// the CSRF fix. The token is derived from the session cookie, so a page on a
// sibling host that could read the old, script-readable CSRF cookie held
// everything the server checked. Fetch metadata is the part it cannot forge.
func TestMutationsAreRefusedWhenTheBrowserReportsAnotherSite(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	for _, site := range []string{"same-site", "cross-site"} {
		request := httptest.NewRequest(http.MethodPost, "/app/message?channel=Cdev", strings.NewReader("text=forged"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		request.Header.Set("Sec-Fetch-Site", site)
		addBrowserCookies(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("Sec-Fetch-Site=%s status=%d body=%s", site, response.Code, response.Body)
		}
		requireContains(t, "refusal", response.Body.String(), "not made from SameOldChat")
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("a forged request was stored: %+v err=%v", page, err)
	}
	// The token is bound to the session and needs no cookie of its own, so a
	// page open longer than any cookie lifetime still posts.
	if header := get(t, mux, "/app?channel=Cdev").Header().Get("Set-Cookie"); strings.Contains(header, "sameoldchat_csrf") {
		t.Fatalf("the workspace still publishes a CSRF cookie: %q", header)
	}
	accepted := httptest.NewRequest(http.MethodPost, "/app/message?channel=Cdev", strings.NewReader("text=hello&_csrf="+auth.CSRFToken("session")))
	accepted.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	accepted.Header.Set("HX-Request", "true")
	accepted.Header.Set("Sec-Fetch-Site", "same-origin")
	accepted.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, accepted)
	if response.Code != http.StatusOK {
		t.Fatalf("a same-origin post with only the session cookie was refused: %d %s", response.Code, response.Body)
	}
}

// TestPostMessageNeverRendersHistoryItCannotRead covers the authorization
// bypass: postMessage gates on chat:write and answered a rejected post with the
// whole workspace render, so a principal that GET /app answers with 403 read
// the conversation, every author, the sidebar and a live CSRF token out of a
// 400 body.
func TestPostMessageNeverRendersHistoryItCannotRead(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChatWrite)})
	seedMessage(t, s, "M1", "confidential history", time.Unix(1700000000, 0).UTC())

	if denied := get(t, mux, "/app?channel=Cdev"); denied.Code != http.StatusForbidden {
		t.Fatalf("GET /app status=%d", denied.Code)
	}
	rejected := postForm(t, mux, "/app/message?channel=Cdev", "text=%20%20", false)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rejected.Code, rejected.Body)
	}
	requireMissing(t, "rejected post", rejected.Body.String(),
		"confidential history",
		`class="sidebar"`,
		`name="_csrf"`,
	)
	requireContains(t, "rejected post", rejected.Body.String(), "A message needs some text", `href="/app"`)
}

// TestReadingIsSafeAndMarkingReadIsAMutation covers the round-1 finding that
// #86 did not fix: GET /app advanced the read cursor for a channel named in the
// query string with no token, so `<img src="/app?channel=C…">` in any message
// silently wiped a victim's unread state under SameSite=Lax.
func TestReadingIsSafeAndMarkingReadIsAMutation(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "hello", created)

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err == nil {
		t.Fatal("a GET advanced the read cursor")
	}
	// The page offers the write as a form instead, so a reader without
	// JavaScript still has the control and every path through it is checked.
	requireContains(t, "workspace page", body, `action="/app/read?channel=Cdev"`, `name="ts"`, ">Mark as read<")

	timestamp := string(domain.NewMessageTimestamp(created))
	unchecked := httptest.NewRequest(http.MethodPost, "/app/read?channel=Cdev", strings.NewReader("ts="+timestamp))
	unchecked.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unchecked.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	refused := httptest.NewRecorder()
	mux.ServeHTTP(refused, unchecked)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("mark-read without a token status=%d", refused.Code)
	}
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err == nil {
		t.Fatal("a request with no CSRF token advanced the read cursor")
	}

	marked := postForm(t, mux, "/app/read?channel=Cdev", "ts="+timestamp, true)
	if marked.Code != http.StatusNoContent {
		t.Fatalf("mark-read status=%d body=%s", marked.Code, marked.Body)
	}
	cursor, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev")
	if err != nil || cursor.LastRead != domain.MessageTimestamp(timestamp) {
		t.Fatalf("read cursor=%+v err=%v", cursor, err)
	}
}

// TestDeletedMessagesStopRendering covers the defect that kept a deleted
// message on screen forever. Deletion is soft and leaves the text in place, and
// message.deleted is a subscribed live topic, so deleting a password or a
// customer's name refreshed it back into every open tab.
func TestDeletedMessagesStopRendering(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "a secret nobody should keep", created)
	seedMessage(t, s, "M2", "a message that stays", created.Add(time.Second))
	if _, err := (service.Messages{Store: s}).Delete(context.Background(), "T1", "U1", "Cdev", domain.NewMessageTimestamp(created)); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/app?channel=Cdev", "/app/timeline?channel=Cdev"} {
		response := get(t, mux, target)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body)
		}
		requireMissing(t, target, response.Body.String(), "a secret nobody should keep")
		requireContains(t, target, response.Body.String(), "a message that stays")
	}
}

// TestThreadViewCarriesNoDuplicateIdentifiers covers the half of the
// duplicate-id defect this package owns: the thread pane namespaced its anchors
// and nothing else, so `id="reaction-<message>"` and its `for=` label existed
// twice in one document and pointed at the wrong control.
func TestThreadViewCarriesNoDuplicateIdentifiers(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "thread root", created)
	timestamp := string(domain.NewMessageTimestamp(created))
	reply := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: "a reply", ThreadTimestamp: domain.MessageTimestamp(timestamp), CreatedAt: created.Add(time.Second)}
	if err := s.CreateMessage(context.Background(), reply, events.Event{ID: "EM2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: reply.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev&thread="+timestamp).Body.String()
	seen := map[string]int{}
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		seen[match[1]]++
		if seen[match[1]] > 1 {
			t.Fatalf("the document carries id=%q %d times", match[1], seen[match[1]])
		}
	}
	for _, match := range regexp.MustCompile(`for="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		if seen[match[1]] != 1 {
			t.Fatalf("label for=%q has %d targets", match[1], seen[match[1]])
		}
	}
}

// TestReactionChipsAreNotControlsForAReaderWhoCannotReact covers the defect
// where a read-only guest saw chips that looked like toggles, and clicking one
// answered a bare "forbidden".
func TestReactionChipsAreNotControlsForAReaderWhoCannotReact(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChannelsHistory), string(auth.ScopeReactionsRead)})
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	if err := s.AddReaction(context.Background(), domain.Reaction{Message: "M1", UserID: "U1", Name: ":wave:"}, events.Event{ID: "ER1", WorkspaceID: "T1", Topic: "reaction.added", Payload: "M1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "read-only chip", body, `<span class="chip" role="img" aria-label=":wave:, 1 reactions">`)
	requireMissing(t, "read-only chip", body, `<button class="chip"`, `aria-label="Add reaction"`)
}

// TestMutationFailuresWithoutJavaScriptAreNavigablePages covers the plain-text
// dead ends: a reader with JavaScript off landed on a white page reading
// "that message timestamp is not valid" with no way back.
func TestMutationFailuresWithoutJavaScriptAreNavigablePages(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	for _, mutation := range []struct{ target, body string }{
		{target: "/app/pin?channel=Cdev&ts=not-a-timestamp"},
		{target: "/app/reaction?channel=Cdev&ts=not-a-timestamp", body: "name=%3Awave%3A"},
		{target: "/app/reaction?channel=Cdev&ts=1700000000.000000", body: "name="},
		{target: "/app/conversation/open", body: "users="},
	} {
		page := postForm(t, mux, mutation.target, mutation.body, false)
		if page.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", mutation.target, page.Code, page.Body)
		}
		if contentType := page.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("%s answered %s with no way back: %s", mutation.target, contentType, page.Body)
		}
		requireContains(t, mutation.target, page.Body.String(), `<a href="/app">Back to chat</a>`)
		if page.Header().Get("Vary") != "HX-Request" {
			t.Fatalf("%s does not vary on the header that decides its shape", mutation.target)
		}
		fragment := postForm(t, mux, mutation.target, mutation.body, true)
		if fragment.Code != http.StatusBadRequest || !strings.HasPrefix(fragment.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s enhanced status=%d type=%s", mutation.target, fragment.Code, fragment.Header().Get("Content-Type"))
		}
	}
}

// countingChat counts the History calls one render costs.
type countingChat struct {
	chatapi.Service
	calls *int
}

func (c countingChat) History(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	*c.calls++
	return c.Service.History(ctx, workspace, user, conversation, request)
}

// longTimelineWorkspace seeds enough messages to prove that timeline cost and
// correctness do not depend on walking from the first row.
func longTimelineWorkspace(t *testing.T, calls *int) *http.ServeMux {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("Cdev", "U1")
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1700000000, 0).UTC()
	for index := 0; index < timelineWindow*4; index++ {
		message := domain.Message{ID: domain.MessageID(fmt.Sprintf("M%06d", index)), WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: fmt.Sprintf("note-%06d", index), CreatedAt: base.Add(time.Duration(index) * time.Second)}
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID(fmt.Sprintf("E%06d", index)), WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: message.CreatedAt}, ""); err != nil {
			t.Fatal(err)
		}
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	var service chatapi.Service = service.Messages{Store: s}
	if calls != nil {
		service = countingChat{Service: service, calls: calls}
	}
	handler, err := NewHandler(service, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux
}

// The old bounded forward scan made the newest rows unreachable once a channel
// crossed its budget. A descending keyset page must make the same long channel
// an ordinary current view.
func TestALongTimelineReachesTheNewestMessages(t *testing.T) {
	mux := longTimelineWorkspace(t, nil)

	page := get(t, mux, "/app?channel=Cdev")
	if page.Code != http.StatusOK {
		t.Fatalf("status=%d", page.Code)
	}
	body := page.Body.String()
	requireContains(t, "long page", body,
		"note-000199",
		"note-000150",
		`data-live="true"`,
		"Show older messages",
	)
	requireMissing(t, "long page", body,
		"note-000149",
		"Jump to the latest messages",
	)

	fragment := get(t, mux, "/app/timeline?channel=Cdev")
	if fragment.Code != http.StatusOK {
		t.Fatalf("fragment status=%d", fragment.Code)
	}
	requireContains(t, "long fragment", fragment.Body.String(), "note-000199")
}

// One timeline render is one service call in both local and distributed
// composition; conversation length cannot multiply network round trips.
func TestTheTimelineUsesOneDescendingRead(t *testing.T) {
	calls := 0
	mux := longTimelineWorkspace(t, &calls)

	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if calls != 1 {
		t.Fatalf("one render issued %d history calls, want 1", calls)
	}

	calls = 0
	if fragment := get(t, mux, "/app/timeline?channel=Cdev"); fragment.Code != http.StatusOK || calls != 1 {
		t.Fatalf("fragment status=%d calls=%d", fragment.Code, calls)
	}
}

// TestActionSurfacesMeetContrastInBothThemes computes the ratios rather than
// asserting a colour literal, so the palette can change and the contract
// cannot. Every value below was measured failing before the fix: the Send
// button at 2.31:1 and the /me Sign out button at 1.96:1 in the dark theme, and
// the focus ring over the light purple chrome at 1.65:1.
func TestActionSurfacesMeetContrastInBothThemes(t *testing.T) {
	for _, theme := range []struct {
		name   string
		tokens string
	}{{name: "light", tokens: lightTokens}, {name: "dark", tokens: darkTokens}} {
		token := func(name string) string {
			match := regexp.MustCompile(`--` + name + `:#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`).FindStringSubmatch(theme.tokens)
			if match == nil {
				t.Fatalf("%s theme has no --%s", theme.name, name)
			}
			value := match[1]
			if len(value) == 3 {
				value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
			}
			return "#" + value
		}
		// The two action buttons are read out of the stylesheet, so the
		// measurement follows the rule the browser applies rather than a token
		// the rule might stop using.
		resolve := func(value string) string {
			if strings.HasPrefix(value, "var(--") {
				return token(strings.TrimSuffix(strings.TrimPrefix(value, "var(--"), ")"))
			}
			if len(value) == 4 {
				return "#" + string([]byte{value[1], value[1], value[2], value[2], value[3], value[3]})
			}
			return value
		}
		for _, pair := range []struct {
			what       string
			foreground string
			background string
			minimum    float64
		}{
			{what: "Send button label", foreground: resolve(declaration(t, pageStyle, ".send", "color")), background: resolve(declaration(t, pageStyle, ".send", "background")), minimum: 4.5},
			{what: "Sign out button label", foreground: resolve(declaration(t, identityMarkup, ".button", "color")), background: resolve(declaration(t, identityMarkup, ".button", "background")), minimum: 4.5},
			{what: "focus ring on the chrome", foreground: token("focus-chrome"), background: token("accent"), minimum: 3},
			{what: "focus ring on the page", foreground: token("focus"), background: token("bg"), minimum: 3},
			{what: "control border", foreground: token("field-line"), background: token("panel"), minimum: 3},
			{what: "body text", foreground: token("text"), background: token("bg"), minimum: 4.5},
			{what: "secondary text", foreground: token("muted"), background: token("bg"), minimum: 4.5},
		} {
			if ratio := contrastRatio(t, pair.foreground, pair.background); ratio < pair.minimum {
				t.Fatalf("%s theme: %s is %.2f:1 on %s, needs %.1f:1", theme.name, pair.what, ratio, pair.background, pair.minimum)
			}
		}
	}
}

// declaration reads one property out of one CSS rule.
func declaration(t *testing.T, stylesheet, selector, property string) string {
	t.Helper()
	match := regexp.MustCompile(regexp.QuoteMeta(selector) + `\{([^}]*)\}`).FindStringSubmatch(stylesheet)
	if match == nil {
		t.Fatalf("no rule for %s", selector)
	}
	value := regexp.MustCompile(`(?:^|;)` + regexp.QuoteMeta(property) + `:([^;]+)`).FindStringSubmatch(match[1])
	if value == nil {
		t.Fatalf("%s declares no %s: %s", selector, property, match[1])
	}
	return strings.TrimSpace(value[1])
}

func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	lighter, darker := relativeLuminance(t, foreground), relativeLuminance(t, background)
	if darker > lighter {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(t *testing.T, colour string) float64 {
	t.Helper()
	value, err := strconv.ParseUint(strings.TrimPrefix(colour, "#"), 16, 32)
	if err != nil {
		t.Fatalf("colour %q: %v", colour, err)
	}
	channel := func(shift uint) float64 {
		part := float64((value>>shift)&0xff) / 255
		if part <= 0.03928 {
			return part / 12.92
		}
		return math.Pow((part+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(16) + 0.7152*channel(8) + 0.0722*channel(0)
}

// TestDeactivatedMembersAreNotOfferedAsPeople covers the defect where a
// colleague who left six months ago was listed with no indication and offered
// a "Message <them>" button that opened a dead conversation.
func TestDeactivatedMembersAreNotOfferedAsPeople(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "gone", RealName: "Gone Person", Deleted: true})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "here", RealName: "Still Here"})
	body := get(t, mux, "/app/members").Body.String()
	requireContains(t, "members page", body, "Still Here", `name="users" value="U3"`)
	requireMissing(t, "members page", body, "Gone Person", `name="users" value="U2"`)
}

// TestFailedPostKeepsTheDraftAndExplainsTheFailure covers the defect where a
// rejected post produced no visible feedback and silently lost the message.
func TestFailedPostKeepsTheDraftAndExplainsTheFailure(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	enhanced := postForm(t, mux, "/app/message?channel=Cdev", "text=%20%20", true)
	if enhanced.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", enhanced.Code, enhanced.Body)
	}
	if !strings.Contains(enhanced.Body.String(), "A message needs some text") {
		t.Fatalf("enhanced failure body=%q", enhanced.Body.String())
	}
	plain := postForm(t, mux, "/app/message?channel=Cdev", "text=%20%20", false)
	if plain.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", plain.Code, plain.Body)
	}
	body := plain.Body.String()
	requireContains(t, "rejected post", body, `class="form-error" id="composer-error" role="alert"`, "A message needs some text", "<textarea")
	// The server-rendered failure has to match the enhanced one: the error is
	// visible, it is what the caret lands on, and the composer carries the
	// state its own stylesheet defines.
	region := regexp.MustCompile(`<p class="form-error" id="composer-error"[^>]*>`).FindString(body)
	if strings.Contains(region, "hidden") {
		t.Fatalf("the error region stayed hidden after a rejected post: %s", region)
	}
	if !strings.Contains(region, "autofocus") {
		t.Fatalf("the error region is not what the caret lands on: %s", region)
	}
	requireContains(t, "rejected post", body, `class="composer is-error"`)
	if strings.Contains(body, `name="text" required autofocus`) {
		t.Fatal("the composer takes focus past the error it just rendered")
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("messages=%+v err=%v", page, err)
	}
}

// TestFormBodyLimitIsInstalledBeforeCSRFValidation covers the defect where the
// declared 4 MB limit was unreachable: CSRF validation read a form value first,
// which parsed the whole body with net/http's own 32 MB default.
func TestFormBodyLimitIsInstalledBeforeCSRFValidation(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	body := "text=" + strings.Repeat("a", 5<<20)
	response := postForm(t, mux, "/app/message?channel=Cdev", body, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status=%d", response.Code)
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("an oversized body was still stored: messages=%+v err=%v", page, err)
	}
}

// TestThemeIsResolvedBeforeTheFirstPaint covers the defect where a dark-theme
// reader saw a full flash of the light theme on every navigation, and the toggle
// never reported which theme was active.
func TestThemeIsResolvedBeforeTheFirstPaint(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, target := range []string{"/app?channel=Cdev", "/app/members", "/app/search?q=hello"} {
		body := get(t, mux, target).Body.String()
		bootstrap := strings.Index(body, "localStorage.getItem('sameoldchat-theme')")
		if bootstrap < 0 {
			t.Fatalf("%s does not bootstrap the theme: %s", target, body)
		}
		if bootstrap > strings.Index(body, "<body") {
			t.Fatalf("%s resolves the theme after the first paint", target)
		}
		requireContains(t, target, body, "prefers-color-scheme: dark", `id="theme-toggle"`, `aria-pressed="false"`)
	}
}

func TestHTMXPostMessage(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChatWrite), string(auth.ScopeChannelsHistory)})
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("text", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("_csrf", auth.CSRFToken("session")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/app/message", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	// The browser shim sends the token as a form field, not a header: that is the
	// path the body limit has to survive.
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "hello") {
		t.Fatalf("body = %s", res.Body)
	}
	indexResult := get(t, mux, "/app")
	body := indexResult.Body.String()
	if indexResult.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%s", indexResult.Code, body)
	}
	requireContains(t, "index", body,
		"general",
		"hello",
		"theme-toggle",
		`data-theme="light"`,
		"HX-Request",
		"last_event_id",
		"sessionStorage",
		`method="get" action="/app/search"`,
		`name="q"`,
	)
	// The page is rendered from the template, not patched afterwards: the search
	// control is a real form and there is no second, superseded event stream.
	requireMissing(t, "index", body, `href="/me"`, `<label class="search"`)
	// Reading is a safe method and does not write; the read cursor is advanced
	// by the explicit, CSRF-checked POST the page carries a form for.
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err == nil {
		t.Fatal("GET /app advanced the read cursor")
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%+v err=%v", page, err)
	}
	thread := domain.NewMessageTimestamp(page.Messages[0].CreatedAt)
	replyResult := postForm(t, mux, "/app/message?channel=Cdev", "text=reply&thread_ts="+string(thread), false)
	if replyResult.Code != http.StatusSeeOther || !strings.Contains(replyResult.Header().Get("Location"), "thread=") {
		t.Fatalf("reply status=%d location=%s", replyResult.Code, replyResult.Header().Get("Location"))
	}
}

func TestApplicationRedirectsUnauthenticatedBrowserToLogin(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "google", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated application response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
}

func TestApplicationStartsShauthForUnauthenticatedDirectEntry(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/auth/oidc" {
		t.Fatalf("unauthenticated application response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
	// A provider the workspace has disabled cannot complete a sign-in, so entry
	// falls back to the provider list instead of a bare "disabled" page.
	if err := store.SetAuthMethod(context.Background(), domain.AuthMethod{WorkspaceID: "T1", Provider: "oidc", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRecorder()
	mux.ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/app", nil))
	if disabled.Code != http.StatusSeeOther || disabled.Header().Get("Location") != "/login" {
		t.Fatalf("disabled provider response = %d location=%q", disabled.Code, disabled.Header().Get("Location"))
	}
}

func TestShauthValidationAndMyProfileExposeVerifiedIdentityAndLogout(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "developer@example.test", Name: "developer"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := store.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	if err := handler.SetReleaseRevision("0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	for _, path := range []string{"/auth/validation", "/me"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		body := response.Body.String()
		for _, expected := range []string{`data-testid="validation-username">developer`, `data-testid="validation-email">developer@example.test`, `data-testid="validation-role">developer`, `data-testid="validation-release">0123456789abcdef0123456789abcdef01234567`, `aria-label="Avatar for developer">D</span>`, `action="/logout"`, `>Sign out</button>`} {
			if response.Code != http.StatusOK || !strings.Contains(body, expected) {
				t.Fatalf("%s status=%d missing %q body=%s", path, response.Code, expected, body)
			}
		}
	}
	applicationRequest := httptest.NewRequest(http.MethodGet, "/app", nil)
	applicationRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	applicationResponse := httptest.NewRecorder()
	mux.ServeHTTP(applicationResponse, applicationRequest)
	// The workspace shell itself has to name the signed-in user and expose the
	// real sign-out control: post-deployment qualification reads them there, not
	// on the separate validation page.
	for _, expected := range []string{
		`href="/me" aria-label="My profile"`,
		`data-shauth-user="developer"`,
		`<span class="signed-in-name">developer</span>`,
		`data-shauth-sign-out`,
	} {
		if applicationResponse.Code != http.StatusOK || !strings.Contains(applicationResponse.Body.String(), expected) {
			t.Fatalf("authenticated application status=%d missing %q body=%s", applicationResponse.Code, expected, applicationResponse.Body)
		}
	}
	anonymous := httptest.NewRecorder()
	mux.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/auth/validation", nil))
	if anonymous.Code != http.StatusSeeOther || anonymous.Header().Get("Location") != "/signed-out" {
		t.Fatalf("anonymous validation status=%d location=%q", anonymous.Code, anonymous.Header().Get("Location"))
	}
	if err := handler.SetReleaseRevision("latest"); err == nil {
		t.Fatal("mutable release revision was accepted")
	}
}

// TestProfileControlOnlyAppearsWhenTheIdentityPageCanRender covers the defect
// where the shell offered "My profile" on deployments whose /me always answered
// with a service-unavailable page.
func TestProfileControlOnlyAppearsWhenTheIdentityPageCanRender(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	// A Google-only deployment has no Shauth identity to validate.
	login, err := NewLoginHandler(service.Messages{Store: s}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "google", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "workspace shell", body, `href="/me"`)
	profile := get(t, mux, "/me")
	if profile.Code != http.StatusServiceUnavailable {
		t.Fatalf("identity status=%d body=%s", profile.Code, profile.Body)
	}
	requireContains(t, "identity page", profile.Body.String(), "Identity validation is unavailable", `href="/app"`)
}

// TestAuthorizationAdminIsReachableFromTheShell covers the defect where the
// documented administration page had no entry point in the interface.
func TestAuthorizationAdminIsReachableFromTheShell(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: s}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	requireContains(t, "workspace shell", get(t, mux, "/app?channel=Cdev").Body.String(), `href="/app/admin/auth"`, "Authorization")
}

func TestSearchPageUsesMessageSearchAndLinksToConversation(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeSearchRead)})
	seedMessage(t, s, "M1", "searchable hello", time.Unix(1700000000, 123456000).UTC())
	res := get(t, mux, "/app/search?q=hello&channel=Cdev")
	body := res.Body.String()
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, body)
	}
	requireContains(t, "search page", body,
		"searchable hello",
		"Search results",
		`href="/app?channel=Cdev"`,
		"Ada Developer",
		"#general",
	)
	// A result opens the message where it lives, anchored, instead of opening it
	// as an empty thread.
	result := regexp.MustCompile(`<a class="result" href="([^"]+)">`).FindStringSubmatch(body)
	if result == nil {
		t.Fatalf("no result link: %s", body)
	}
	link := strings.ReplaceAll(result[1], "&amp;", "&")
	if !strings.Contains(link, "channel=Cdev") || !strings.HasSuffix(link, "#message-M1") || strings.Contains(link, "thread=") {
		t.Fatalf("result link=%q", link)
	}
	// The link positions the timeline on the window that ends at the hit, so the
	// anchor is on the page it opens.
	if !strings.Contains(link, "before=") {
		t.Fatalf("result link does not position the timeline: %q", link)
	}
}

func TestSearchNamesDirectMessagesAfterTheirParticipants(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	s.SeedConversation(domain.Conversation{ID: "Cdm", WorkspaceID: "T1", Name: "direct", IsDirect: true, IsPrivate: true})
	s.SeedConversationMember("Cdm", "U1")
	s.SeedConversationMember("Cdm", "U2")
	created := time.Unix(1700000000, 123456000).UTC()
	message := domain.Message{ID: "Mdm", WorkspaceID: "T1", Conversation: "Cdm", AuthorID: "U2", Text: "private needle", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Edm", WorkspaceID: "T1", Topic: "message.created", Payload: "Mdm", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/app/search?q=needle&channel=Cdm").Body.String()
	requireContains(t, "direct-message search result", body, `<span class="channel">Bob Builder</span>`, "private needle")
	requireMissing(t, "direct-message search result", body, "#direct")
}

// TestSearchReportsValidationInsteadOfAnOutage covers the defect where an
// over-long query answered with a bare "search unavailable" page.
func TestSearchReportsValidationInsteadOfAnOutage(t *testing.T) {
	_, mux := browserWorkspace(t, []string{string(auth.ScopeSearchRead)})
	response := get(t, mux, "/app/search?q="+strings.Repeat("a", 600)+"&channel=Cdev")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "search page", response.Body.String(), "Enter between one and 500 characters", `maxlength="500"`, `href="/app?channel=Cdev"`)
}

func TestProgressiveEnhancementHandlesRedirectResponses(t *testing.T) {
	if !strings.Contains(progressiveEnhancementScript, "document.addEventListener('submit'") {
		t.Fatal("progressive enhancement does not delegate form handling")
	}
	if !strings.Contains(progressiveEnhancementScript, "response.headers.get('HX-Redirect')") {
		t.Fatal("progressive enhancement does not handle HX-Redirect")
	}
	if !strings.Contains(progressiveEnhancementScript, "if(response.status===204)return ''") {
		t.Fatal("progressive enhancement does not handle empty 204 responses")
	}
}

func TestWebFormRejectsRepeatedFields(t *testing.T) {
	_, mux := browserWorkspace(t, []string{string(auth.ScopeChatWrite), string(auth.ScopeChannelsHistory)})
	res := postForm(t, mux, "/app/message", "text=one&text=two", false)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestWebSessionRevocationClearsCookieAndDurablyInvalidates(t *testing.T) {
	s := memory.New()
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	addBrowserCookies(req)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/signed-out" {
		t.Fatalf("status=%d location=%q", res.Code, res.Header().Get("Location"))
	}
	if !strings.Contains(res.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("session cookie was not cleared: %q", res.Header().Get("Set-Cookie"))
	}
	if !strings.Contains(res.Header().Get("Set-Cookie"), "Domain=example.com") {
		t.Fatalf("shared session cookie domain was not cleared: %q", res.Header().Get("Set-Cookie"))
	}
	record, err := s.LookupSession(context.Background(), "session")
	if err != nil || !record.Revoked {
		t.Fatalf("session=%+v err=%v", record, err)
	}
}

type stubRevoker func(context.Context, string) error

func (f stubRevoker) RevokeSession(ctx context.Context, token string) error { return f(ctx, token) }

// TestSignOutAlwaysEndsTheBrowserSession covers the defect where a revocation
// failure left the session cookie in place and answered with bare status text,
// stranding the user in a session they asked to end.
func TestSignOutAlwaysEndsTheBrowserSession(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		err      error
		status   int
		location string
	}{
		{name: "store outage", err: errors.New("session store is down"), status: http.StatusServiceUnavailable},
		{name: "revoked", err: nil, status: http.StatusSeeOther, location: "/signed-out"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := memory.New()
			if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			authenticator, err := auth.NewBrowser(s)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler(service.Messages{Store: s}, authenticator, stubRevoker(func(context.Context, string) error { return testCase.err }), "C1", "")
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			handler.Register(mux)
			request := httptest.NewRequest(http.MethodPost, "/logout", nil)
			addBrowserCookies(request)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if !strings.Contains(response.Header().Get("Set-Cookie"), "Max-Age=0") {
				t.Fatalf("the session cookie survived sign-out: %q", response.Header().Get("Set-Cookie"))
			}
			if testCase.location != "" && response.Header().Get("Location") != testCase.location {
				t.Fatalf("location=%q", response.Header().Get("Location"))
			}
			if testCase.status == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "You are signed out of this browser") {
				t.Fatalf("failed sign-out did not explain itself: %s", response.Body)
			}
		})
	}
}

// TestSignOutTreatsAnAbsentSessionRecordAsSuccess is the case the previous
// implementation reported as an outage.
func TestSignOutTreatsAnAbsentSessionRecordAsSuccess(t *testing.T) {
	s := memory.New()
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, stubRevoker(func(context.Context, string) error { return store.ErrNotFound }), "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/signed-out" {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body)
	}
	if !strings.Contains(response.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("the session cookie survived sign-out: %q", response.Header().Get("Set-Cookie"))
	}
}

func TestMembersPageRendersDurableProfiles(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", RealName: "Alice Example", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeUsersRead), string(auth.ScopeUsersWrite)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	res := get(t, mux, "/app/members")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Available") || !strings.Contains(res.Body.String(), ":wave:") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	// The form mirrors the limits the service enforces without exposing the
	// seven size-specific image fields in Slack's API model.
	requireContains(t, "profile form", res.Body.String(), `maxlength="80"`, `maxlength="100"`, `name="avatar_url"`, `type="url" maxlength="2048"`)
	requireMissing(t, "profile form", res.Body.String(), `name="image_24"`, `name="image_1024"`, `required`)
	updateResult := postForm(t, mux, "/app/profile", "display_name=updated&status_text=Ready&status_emoji=%3Aok%3A&avatar_url=https%3A%2F%2Fexample.test%2Favatar.png", false)
	if updateResult.Code != http.StatusSeeOther {
		t.Fatalf("profile update status=%d body=%s", updateResult.Code, updateResult.Body)
	}
	stored, err := s.GetUser(context.Background(), "U1")
	if err != nil || stored.Profile.DisplayName != "updated" || stored.Profile.StatusText != "Ready" || stored.Profile.Image24 != "https://example.test/avatar.png" || stored.Profile.Image1024 != "https://example.test/avatar.png" {
		t.Fatalf("updated profile=%+v err=%v", stored.Profile, err)
	}
}

// TestRejectedProfileKeepsEveryFieldAndExplainsTheLimit covers the defect where
// a rejected save answered with bare status text and lost every field.
func TestRejectedProfileKeepsEveryFieldAndExplainsTheLimit(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := postForm(t, mux, "/app/profile", "display_name="+strings.Repeat("n", 81)+"&status_text=Ready&status_emoji=%3Aok%3A&avatar_url=https%3A%2F%2Fexample.test%2Favatar.png", false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "rejected profile", body,
		"at most 80 characters",
		`value="`+strings.Repeat("n", 81)+`"`,
		`value="Ready"`,
		`value=":ok:"`,
		`value="https://example.test/avatar.png"`,
	)
}

// TestMembersPageOffersADirectMessageAction covers the dead
// POST /app/conversation/open route: nothing in the interface reached it.
func TestMembersPageOffersADirectMessageAction(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	body := get(t, mux, "/app/members").Body.String()
	requireContains(t, "members page", body, `action="/app/conversation/open"`, `name="users" value="U2"`, "Message Bob Builder")
	if strings.Contains(body, `name="users" value="U1"`) {
		t.Fatal("the members page offers to open a direct conversation with the signed-in user")
	}
}

// TestTruncatedListsExposeTheirRemainder covers the defect where the sidebar,
// the member directory and the search results silently stopped at their page
// limit with no way to reach the rest.
func TestTruncatedListsExposeTheirRemainder(t *testing.T) {
	t.Run("members", func(t *testing.T) {
		s, mux := browserWorkspace(t, []string{string(auth.ScopeUsersRead)})
		for index := 0; index < memberWindow+1; index++ {
			s.SeedUser(domain.User{ID: domain.UserID(fmt.Sprintf("U1%03d", index)), WorkspaceID: "T1", Name: fmt.Sprintf("member-%03d", index)})
		}
		body := get(t, mux, "/app/members").Body.String()
		more := regexp.MustCompile(`<a href="(/app/members\?cursor=[^"]+)">Show more members</a>`).FindStringSubmatch(body)
		if more == nil {
			t.Fatalf("the member directory hides its remainder: %s", body)
		}
		second := get(t, mux, strings.ReplaceAll(more[1], "&amp;", "&"))
		if second.Code != http.StatusOK {
			t.Fatalf("second member page status=%d body=%s", second.Code, second.Body)
		}
	})
	t.Run("search", func(t *testing.T) {
		s, mux := browserWorkspace(t, []string{string(auth.ScopeSearchRead)})
		base := time.Unix(1700000000, 0).UTC()
		for index := 0; index < searchWindow+1; index++ {
			seedMessage(t, s, domain.MessageID(fmt.Sprintf("M%03d", index)), "needle", base.Add(time.Duration(index)*time.Second))
		}
		body := get(t, mux, "/app/search?q=needle&channel=Cdev").Body.String()
		more := regexp.MustCompile(`<a href="(/app/search\?[^"]+)">Show more results</a>`).FindStringSubmatch(body)
		if more == nil {
			t.Fatalf("search hides its remainder: %s", body)
		}
		second := get(t, mux, strings.ReplaceAll(more[1], "&amp;", "&"))
		if second.Code != http.StatusOK {
			t.Fatalf("second results page status=%d body=%s", second.Code, second.Body)
		}
	})
	t.Run("conversations", func(t *testing.T) {
		s, mux := browserWorkspace(t, []string{string(auth.ScopeChannelsHistory)})
		for index := 0; index < conversationWindow+1; index++ {
			s.SeedConversation(domain.Conversation{ID: domain.ConversationID(fmt.Sprintf("C%03d", index)), WorkspaceID: "T1", Name: fmt.Sprintf("channel-%03d", index)})
		}
		body := get(t, mux, "/app?channel=Cdev").Body.String()
		more := regexp.MustCompile(`<a class="side-more" href="(/app\?[^"]+)">More conversations</a>`).FindStringSubmatch(body)
		if more == nil {
			t.Fatalf("the sidebar hides its remainder: %s", body)
		}
		second := get(t, mux, strings.ReplaceAll(more[1], "&amp;", "&"))
		if second.Code != http.StatusOK {
			t.Fatalf("second sidebar page status=%d body=%s", second.Code, second.Body)
		}
	})
}

func TestReactionAndPinMutationsPersist(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))
	reaction := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=%3Awave%3A", true)
	if reaction.Code != http.StatusNoContent {
		t.Fatalf("reaction status=%d body=%s", reaction.Code, reaction.Body)
	}
	reactions, _, _, err := s.ListReactions(context.Background(), "M1", domain.PageRequest{Limit: 10})
	if err != nil || len(reactions) != 1 || reactions[0].Name != ":wave:" {
		t.Fatalf("reactions=%+v err=%v", reactions, err)
	}
	pin := postForm(t, mux, "/app/pin?channel=Cdev&ts="+timestamp, "", false)
	if pin.Code != http.StatusSeeOther {
		t.Fatalf("pin status=%d body=%s", pin.Code, pin.Body)
	}
	pins, _, _, err := s.ListPins(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(pins) != 1 || pins[0].Message != "M1" {
		t.Fatalf("pins=%+v err=%v", pins, err)
	}
}

func TestWebOpensNormalizedDirectConversation(t *testing.T) {
	ctx := context.Background()
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	res := postForm(t, mux, "/app/conversation/open", "users=U2%2C%20U2", false)
	if res.Code != http.StatusSeeOther || !strings.Contains(res.Header().Get("Location"), "channel=") {
		t.Fatalf("status=%d location=%q body=%s", res.Code, res.Header().Get("Location"), res.Body)
	}
	conversations, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 2 {
		t.Fatalf("conversations=%+v err=%v", conversations, err)
	}
	var direct domain.Conversation
	for _, conversation := range conversations.Conversations {
		if conversation.IsDirect {
			direct = conversation
		}
	}
	if direct.ID == "" || !direct.IsPrivate {
		t.Fatalf("direct conversation=%+v", direct)
	}
}

// TestReadCursorFailureStillRendersTheConversation covers the defect where a
// failure to persist unread bookkeeping made the channel unreadable.
func TestReadCursorFailureStillRendersTheConversation(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("Cdev", "U1")
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: readCursorOutage{Store: s}}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "degraded page", response.Body.String(), "hello")
	// A read-cursor outage is reported where the write happens, in a sentence
	// that says nothing else was lost. It never blocks reading.
	marked := postForm(t, mux, "/app/read?channel=Cdev", "ts="+string(domain.NewMessageTimestamp(time.Unix(1700000000, 0).UTC())), true)
	if marked.Code != http.StatusServiceUnavailable {
		t.Fatalf("mark-read status=%d body=%s", marked.Code, marked.Body)
	}
	requireContains(t, "mark-read failure", marked.Body.String(), "The unread marker could not be moved")
}

type readCursorOutage struct {
	*memory.Store
}

func (readCursorOutage) SetReadCursor(context.Context, domain.ReadCursor, events.Event) error {
	return errors.New("read cursor store is unavailable")
}
