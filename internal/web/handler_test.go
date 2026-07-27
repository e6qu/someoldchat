package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func addBrowserCookies(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	token := auth.CSRFToken("session")
	request.AddCookie(&http.Cookie{Name: auth.CSRFTokenCookieName, Value: token})
	request.Header.Set(auth.CSRFTokenHeaderName, token)
}

// browserWorkspace seeds the smallest workspace a browser journey needs and
// returns a mux with the web handler registered.
func browserWorkspace(t *testing.T, scopes []string) (*memory.Store, *http.ServeMux) {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer", RealName: "Ada Developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general", Topic: "Everything else"})
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

// TestProgressiveEnhancementKeepsTheStreamComposerAndErrors covers the defects
// in the page script: the event stream was closed on the first submit and never
// reopened, every event was suppressed while any form had focus, the composer
// was never cleared, and a failure only added a CSS class that no stylesheet
// defined.
func TestProgressiveEnhancementKeepsTheStreamComposerAndErrors(t *testing.T) {
	script := progressiveEnhancementScript
	for _, expected := range []string{
		"document.addEventListener('submit'",
		"response.headers.get('HX-Redirect')",
		"if(response.status===204)return ''",
		"form.reset()",
		"text.focus()",
		"errorBox.hidden=false",
		"classList.add('is-error')",
		"addEventListener('keydown'",
		"requestSubmit",
		"new EventSource('/events'",
		"last_event_id",
		"sessionStorage",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("page script is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"window.location.reload",
		"events.close()",
		"stream.close()",
		"document.activeElement.form",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("page script still contains %q", forbidden)
		}
	}
	// A live event refreshes a region only when focus is not inside it, so
	// typing in the composer no longer blocks delivery to the timeline.
	if !strings.Contains(script, "region.contains(document.activeElement)") {
		t.Fatalf("page script suppresses live updates too broadly: %s", script)
	}
	_, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "page", body,
		"Enter to send · Shift+Enter for a new line",
		`id="composer-error"`,
		`role="alert"`,
		".composer.is-error{",
	)
	if strings.Count(body, "new EventSource(") != 1 {
		t.Fatalf("the page opens %d event streams", strings.Count(body, "new EventSource("))
	}
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
	if strings.Contains(body, `id="composer-error" role="alert" hidden`) {
		t.Fatalf("the error region stayed hidden after a rejected post: %s", body)
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
	req.AddCookie(&http.Cookie{Name: auth.CSRFTokenCookieName, Value: auth.CSRFToken("session")})
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
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err != nil {
		t.Fatalf("read cursor was not persisted: %v", err)
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
	// The form mirrors the limits the service enforces.
	requireContains(t, "profile form", res.Body.String(), `maxlength="80"`, `maxlength="100"`, `type="url" maxlength="2048"`)
	updateResult := postForm(t, mux, "/app/profile", "display_name=updated&status_text=Ready&status_emoji=%3Aok%3A&image_24=&image_32=&image_48=&image_72=&image_192=&image_512=&image_1024=", false)
	if updateResult.Code != http.StatusSeeOther {
		t.Fatalf("profile update status=%d body=%s", updateResult.Code, updateResult.Body)
	}
	stored, err := s.GetUser(context.Background(), "U1")
	if err != nil || stored.Profile.DisplayName != "updated" || stored.Profile.StatusText != "Ready" {
		t.Fatalf("updated profile=%+v err=%v", stored.Profile, err)
	}
}

// TestRejectedProfileKeepsEveryFieldAndExplainsTheLimit covers the defect where
// a rejected save answered with bare status text and lost all ten fields.
func TestRejectedProfileKeepsEveryFieldAndExplainsTheLimit(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := postForm(t, mux, "/app/profile", "display_name="+strings.Repeat("n", 81)+"&status_text=Ready&status_emoji=%3Aok%3A&image_24=&image_32=&image_48=&image_72=&image_192=&image_512=&image_1024=", false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "rejected profile", body,
		"at most 80 characters",
		`value="`+strings.Repeat("n", 81)+`"`,
		`value="Ready"`,
		`value=":ok:"`,
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
	requireContains(t, "degraded page", response.Body.String(), "hello", "Unread counts are temporarily out of date.")
}

type readCursorOutage struct {
	*memory.Store
}

func (readCursorOutage) SetReadCursor(context.Context, domain.ReadCursor, events.Event) error {
	return errors.New("read cursor store is unavailable")
}
