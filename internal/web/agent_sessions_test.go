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
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedAgentApp installs an agent app whose bot is a member of conversation.
// subscribed decides whether its manifest subscribes to agent_session_stopped.
func seedAgentApp(t *testing.T, s *memory.Store, app domain.AppID, bot domain.UserID, conversation domain.ConversationID, subscribed bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	subscriptions := `["message.channels"]`
	if subscribed {
		subscriptions = `["agent_session_stopped","agent_session_title_changed"]`
	}
	s.SeedUser(domain.User{ID: bot, WorkspaceID: "T1", Name: "bot-" + string(app)})
	s.SeedConversationMember(conversation, bot)
	client := "client-" + string(app)
	if err := s.CreateApp(ctx,
		domain.App{ID: app, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Trip Agent", ClientID: client, SigningSecretHash: "hash", SigningSecretCiphertext: "sealed", VerificationTokenCiphertext: "sealed", ManifestVersion: 1, CreatedAt: now, UpdatedAt: now},
		domain.AppManifestRevision{AppID: app, Version: 1, CreatedBy: "U1", CreatedAt: now,
			Manifest: `{"display_information":{"name":"Trip Agent"},"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":` + subscriptions + `}}}`},
		domain.OAuthClient{ID: client, SecretHash: "secret", AppID: app},
	); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, domain.Bot{ID: domain.BotID("B" + string(app)), WorkspaceID: "T1", AppID: app, UserID: bot, Name: string(app), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app, WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

// emitAgentSessionTopics drives every agent session mutation once, so a test
// over the journal sees each topic a real mutation publishes.
func emitAgentSessionTopics(t *testing.T, s *memory.Store, chat service.Messages, conversation domain.ConversationID, member domain.UserID) {
	t.Helper()
	ctx := context.Background()
	seedAgentApp(t, s, "AGLIVE", "UBLIVE", conversation, true)
	root, err := chat.Post(ctx, "T1", member, conversation, "plan a trip", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	if _, err := chat.SetAgentSessionStatus(ctx, "T1", "UBLIVE", "AGLIVE", conversation, thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.RenameAgentSession(ctx, "T1", "UBLIVE", "AGLIVE", conversation, thread, "Trip"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ChangeAgentSessionTitle(ctx, "T1", member, conversation, thread, "Bora Bora"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.StopAgentSession(ctx, "T1", member, conversation, thread); err != nil {
		t.Fatal(err)
	}
}

// TestTheThreadPaneShowsTheAgentSessionAndItsControls is the member's half
// of the agent session journey: the thread pane shows the session's title and
// status, the stop control appears only while an agent whose app subscribes to
// agent_session_stopped is processing, the stop and retitle forms reach the
// service, and the region is its own live fragment.
func TestTheThreadPaneShowsTheAgentSessionAndItsControls(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	chat := service.Messages{Store: s}
	seedAgentApp(t, s, "AG1", "UB1", "Cdev", true)
	seedAgentApp(t, s, "AG2", "UB2", "Cdev", false)
	root, err := chat.Post(ctx, "T1", "U1", "Cdev", "plan a trip", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := string(domain.NewMessageTimestamp(root.CreatedAt))
	fragment := "/app/agent-session?" + url.Values{"channel": {"Cdev"}, "thread": {thread}}.Encode()

	page := get(t, mux, "/app?channel=Cdev&thread="+thread).Body.String()
	requireContains(t, "thread without a session", page, `id="agent-session"`, `data-fragment="/app/agent-session?channel=Cdev&amp;thread=`)
	requireMissing(t, "thread without a session", page, `class="agent-session"`)

	// An agent that does not subscribe gets a loading state with no control.
	if _, err := chat.SetAgentSessionStatus(ctx, "T1", "UB2", "AG2", "Cdev", domain.MessageTimestamp(thread), domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing, Title: "Scuba diving research"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, fragment).Body.String()
	requireContains(t, "unsubscribed processing", body, "Scuba diving research", `data-agent-session-status="processing"`, "Working…", "Trip Agent", `role="status"`, `<label for="agent-session-title-input">Session title</label>`)
	requireMissing(t, "unsubscribed processing", body, `class="agent-session-stop"`)
	if response := postForm(t, mux, "/app/agent-session/stop?channel=Cdev&thread="+thread, url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), true); response.Code != http.StatusConflict {
		t.Fatalf("stopping with no stop control = %d: %s", response.Code, response.Body)
	}

	if _, err := chat.SetAgentSessionStatus(ctx, "T1", "UB1", "AG1", "Cdev", domain.MessageTimestamp(thread), domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing, Identity: domain.AgentIdentity{Username: "Dive Buddy", IconEmoji: ":robot_face:"}}); err != nil {
		t.Fatal(err)
	}
	body = get(t, mux, fragment).Body.String()
	requireContains(t, "subscribed processing", body, `class="agent-session-stop"`, "Dive Buddy", "🤖")

	stop := postForm(t, mux, "/app/agent-session/stop?channel=Cdev&thread="+thread, url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), true)
	if stop.Code != http.StatusNoContent {
		t.Fatalf("stop = %d: %s", stop.Code, stop.Body)
	}
	retitle := postForm(t, mux, "/app/agent-session/title?channel=Cdev&thread="+thread, url.Values{"_csrf": {auth.CSRFToken("session")}, "title": {"Bora Bora trip prep"}}.Encode(), false)
	if retitle.Code != http.StatusSeeOther || !strings.Contains(retitle.Header().Get("Location"), "thread="+url.QueryEscape(thread)) {
		t.Fatalf("retitle = %d %s: %s", retitle.Code, retitle.Header().Get("Location"), retitle.Body)
	}
	empty := postForm(t, mux, "/app/agent-session/title?channel=Cdev&thread="+thread, url.Values{"_csrf": {auth.CSRFToken("session")}, "title": {"  "}}.Encode(), true)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("an empty title = %d: %s", empty.Code, empty.Body)
	}
	var stopped, retitled int
	for _, event := range s.Outbox() {
		switch event.Topic {
		case events.AgentSessionStoppedTopic:
			stopped++
		case events.AgentSessionTitleChangedTopic:
			retitled++
		}
	}
	if stopped != 1 || retitled != 2 {
		t.Fatalf("stopped records=%d retitled records=%d, want 1 and one per agent", stopped, retitled)
	}
	requireContains(t, "after the retitle", get(t, mux, fragment).Body.String(), "Bora Bora trip prep")

	// Someone outside the conversation sees the session without controls.
	s.SeedConversation(domain.Conversation{ID: "Cother", WorkspaceID: "T1", Name: "other"})
	if err := s.SeedSession(ctx, "outsider", domain.SessionRecord{WorkspaceID: "T1", UserID: "U9", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "U9", WorkspaceID: "T1", Name: "outsider"})
	request, _ := http.NewRequest(http.MethodGet, fragment, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "outsider"})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	requireContains(t, "outsider", recorder.Body.String(), "Bora Bora trip prep")
	requireMissing(t, "outsider", recorder.Body.String(), `class="agent-session-stop"`, `agent-session-rename`)
}
