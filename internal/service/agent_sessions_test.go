package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// agentSessionFixture is a workspace with two agent apps in channel C1:
//
//   - A1 (bot UB1) subscribes to agent_session_stopped and
//     agent_session_title_changed;
//   - A2 (bot UB2) subscribes to neither;
//   - A3 (bot UB3) is installed but is not in C1.
//
// U1 and U2 are members of C1; U3 is a member of the workspace only. root is a
// top-level message in C1 that sessions are scoped to.
type agentSessionFixture struct {
	ctx      context.Context
	state    *memory.Store
	messages Messages
	root     domain.MessageTimestamp
	reply    domain.MessageTimestamp
}

func newAgentSessionFixture(t *testing.T) agentSessionFixture {
	t.Helper()
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	for _, user := range []domain.UserID{"U1", "U2", "U3", "UB1", "UB2", "UB3"} {
		state.SeedUser(domain.User{ID: user, WorkspaceID: "T1", Name: string(user)})
	}
	state.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	for _, user := range []domain.UserID{"U1", "U2", "UB1", "UB2"} {
		state.SeedConversationMember("C1", user)
	}
	now := time.Now().UTC()
	for _, app := range []struct {
		id     domain.AppID
		bot    domain.UserID
		events string
	}{
		{"A1", "UB1", `["agent_session_stopped","agent_session_title_changed"]`},
		{"A2", "UB2", `["message.channels"]`},
		{"A3", "UB3", `["agent_session_stopped"]`},
	} {
		manifest := `{"display_information":{"name":"Agent ` + string(app.id) + `"},"oauth_config":{"scopes":{"bot":["chat:write"]}},` +
			`"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":` + app.events + `}}}`
		client := "client-" + string(app.id)
		if err := state.CreateApp(ctx,
			domain.App{ID: app.id, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Agent " + string(app.id), ClientID: client, SigningSecretHash: "hash", SigningSecretCiphertext: "sealed", VerificationTokenCiphertext: "sealed", ManifestVersion: 1, CreatedAt: now, UpdatedAt: now},
			domain.AppManifestRevision{AppID: app.id, Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
			domain.OAuthClient{ID: client, SecretHash: "secret", AppID: app.id},
		); err != nil {
			t.Fatal(err)
		}
		botID := domain.BotID("B" + string(app.id))
		if err := state.CreateBot(ctx, domain.Bot{ID: botID, WorkspaceID: "T1", AppID: app.id, UserID: app.bot, Name: string(app.id), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := state.SeedToken(ctx, "xoxb-"+string(app.id), domain.TokenRecord{WorkspaceID: "T1", UserID: app.bot, AppID: app.id, BotID: botID, TokenType: "bot", Scopes: []string{"chat:write"}}); err != nil {
			t.Fatal(err)
		}
		if err := state.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.id, WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: state, AppCredentialKey: appEventTestKey}
	root, err := messages.Post(ctx, "T1", "U1", "C1", "Plan the trip", "", "")
	if err != nil {
		t.Fatal(err)
	}
	rootTS := domain.NewMessageTimestamp(root.CreatedAt)
	reply, err := messages.Post(ctx, "T1", "U2", "C1", "a reply", rootTS, "")
	if err != nil {
		t.Fatal(err)
	}
	return agentSessionFixture{ctx: ctx, state: state, messages: messages, root: rootTS, reply: domain.NewMessageTimestamp(reply.CreatedAt)}
}

func (f agentSessionFixture) setStatus(t *testing.T, app domain.AppID, bot domain.UserID, request domain.AgentSessionStatusRequest) domain.AgentSessionStatusResult {
	t.Helper()
	result, err := f.messages.SetAgentSessionStatus(f.ctx, "T1", bot, app, "C1", f.root, request)
	if err != nil {
		t.Fatalf("setStatus %s %s: %v", app, request.Status, err)
	}
	return result
}

// topicEvents is the journal's records of one topic.
func (f agentSessionFixture) topicEvents(topic string) []events.Event {
	var found []events.Event
	for _, event := range f.state.Outbox() {
		if event.Topic == topic {
			found = append(found, event)
		}
	}
	return found
}

// slackEvent projects one record for one app and decodes the inner event it
// would be delivered, reporting false when the app is not sent it.
func (f agentSessionFixture) slackEvent(t *testing.T, app domain.AppID, event events.Event) (map[string]any, bool) {
	t.Helper()
	prepared, visible, err := PrepareAppEvent(f.ctx, f.state, appEventTestKey, "", app, events.Record{Sequence: 1, Event: event})
	if err != nil {
		t.Fatalf("%s: %v", app, err)
	}
	if !visible {
		return nil, false
	}
	bodies, err := events.SlackEventBodies(prepared, string(app))
	if err != nil {
		t.Fatalf("%s: %v", app, err)
	}
	if len(bodies) != 1 {
		return nil, false
	}
	var envelope struct {
		TeamID string         `json:"team_id"`
		Event  map[string]any `json:"event"`
	}
	if err := json.Unmarshal(bodies[0], &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Event["envelope_team_id"] = envelope.TeamID
	return envelope.Event, true
}

// TestAgentSessionSetStatusFollowsTheReference drives agents.sessions.setStatus
// through what its page states: it creates the session on first use with the
// title and initiator, ignores both afterwards, accepts the four statuses in
// any order, derives the session status suspended > processing > active >
// closed across agents, and answers the missing-subscription warning to an
// app that does not subscribe to agent_session_stopped.
func TestAgentSessionSetStatusFollowsTheReference(t *testing.T) {
	f := newAgentSessionFixture(t)
	created := f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing, Title: "Scuba diving research", InitiatorUserID: "U1"})
	if created.Session.Title != "Scuba diving research" || created.Session.InitiatorUserID != "U1" ||
		created.Session.Status() != domain.AgentSessionProcessing || created.AgentStatus != domain.AgentSessionProcessing || len(created.Warnings) != 0 {
		t.Fatalf("created=%+v", created)
	}
	// Title and initiator are creation arguments; the existing session keeps
	// its own, and an over-long title is not even examined.
	ignored := f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing, Title: string(make([]byte, 300)), InitiatorUserID: "U2"})
	if ignored.Session.Title != "Scuba diving research" || ignored.Session.InitiatorUserID != "U1" {
		t.Fatalf("existing session changed: %+v", ignored.Session)
	}
	// The page's own example: another agent is processing when this one sets
	// active, so the session is still processing.
	second := f.setStatus(t, "A2", "UB2", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive})
	if second.Session.Status() != domain.AgentSessionProcessing || second.AgentStatus != domain.AgentSessionActive || len(second.Session.Agents) != 2 {
		t.Fatalf("second agent=%+v", second)
	}
	if len(second.Warnings) != 1 || second.Warnings[0] != "missing_agent_session_stopped_event_subscription" {
		t.Fatalf("an app without the subscription is warned: %v", second.Warnings)
	}
	for _, step := range []struct {
		app   domain.AppID
		bot   domain.UserID
		set   domain.AgentSessionStatus
		after domain.AgentSessionStatus
	}{
		{"A2", "UB2", domain.AgentSessionSuspended, domain.AgentSessionSuspended},
		{"A1", "UB1", domain.AgentSessionClosed, domain.AgentSessionSuspended},
		{"A2", "UB2", domain.AgentSessionActive, domain.AgentSessionActive},
		{"A2", "UB2", domain.AgentSessionClosed, domain.AgentSessionClosed},
		// A closed agent may come back; the page names no forbidden move.
		{"A1", "UB1", domain.AgentSessionActive, domain.AgentSessionActive},
	} {
		result := f.setStatus(t, step.app, step.bot, domain.AgentSessionStatusRequest{Status: step.set})
		if result.Session.Status() != step.after || result.AgentStatus != step.set {
			t.Fatalf("%s set %s: session=%s agent=%s, want session %s", step.app, step.set, result.Session.Status(), result.AgentStatus, step.after)
		}
	}
	if records := f.topicEvents(events.AgentSessionStatusSetTopic); len(records) != 8 {
		t.Fatalf("every write journals its record: %d", len(records))
	}

	for name, test := range map[string]struct {
		app     domain.AppID
		actor   domain.UserID
		thread  domain.MessageTimestamp
		request domain.AgentSessionStatusRequest
		want    error
	}{
		"invalid status":         {"A1", "UB1", f.root, domain.AgentSessionStatusRequest{Status: "thinking"}, domain.ErrInvalidAgentSessionStatus},
		"no thread":              {"A1", "UB1", "", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive}, domain.ErrAgentSessionThreadRequired},
		"reply is not a root":    {"A1", "UB1", f.reply, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive}, domain.ErrInvalidAgentSession},
		"no such message":        {"A1", "UB1", "1000000000.000001", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive}, domain.ErrInvalidAgentSession},
		"app not in the channel": {"A3", "UB3", f.root, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive}, domain.ErrNotInConversation},
		"username over 200":      {"A1", "UB1", f.root, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, Identity: domain.AgentIdentity{Username: string(make([]rune, 201))}}, domain.ErrInvalidAgentSession},
		"icon_url not a URL":     {"A1", "UB1", f.root, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, Identity: domain.AgentIdentity{IconURL: "not a url"}}, domain.ErrInvalidAgentSession},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.messages.SetAgentSessionStatus(f.ctx, "T1", test.actor, test.app, "C1", test.thread, test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v, want %v", err, test.want)
			}
		})
	}

	// Creation arguments are validated when they create: a second thread.
	second2, err := f.messages.Post(f.ctx, "T1", "U1", "C1", "Another question", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(second2.CreatedAt)
	if _, err := f.messages.SetAgentSessionStatus(f.ctx, "T1", "UB1", "A1", "C1", thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, InitiatorUserID: "UNOBODY"}); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("unknown initiator: %v", err)
	}
	if _, err := f.messages.SetAgentSessionStatus(f.ctx, "T1", "UB1", "A1", "C1", thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, InitiatorUserID: "U3"}); !errors.Is(err, domain.ErrInvalidAgentSession) {
		t.Fatalf("initiator outside the channel: %v", err)
	}
	long := make([]rune, 201)
	for index := range long {
		long[index] = 'x'
	}
	if _, err := f.messages.SetAgentSessionStatus(f.ctx, "T1", "UB1", "A1", "C1", thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, Title: string(long)}); !errors.Is(err, domain.ErrInvalidAgentSession) {
		t.Fatalf("over-long creating title: %v", err)
	}
	if _, err := f.state.GetAgentSession(f.ctx, "T1", "C1", thread); err == nil {
		t.Fatal("a refused creation left a session behind")
	}
}

// TestAgentSessionIdentityOverridesTravelAsOneSet is the page's rule for
// icon_emoji, icon_url and username: a call that sets none leaves the set in
// place, and a call that sets any replaces all three.
func TestAgentSessionIdentityOverridesTravelAsOneSet(t *testing.T) {
	f := newAgentSessionFixture(t)
	identity := func(result domain.AgentSessionStatusResult) domain.AgentIdentity {
		agent, _ := result.Session.Agent("A1")
		return agent.Identity
	}
	set := f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing, Identity: domain.AgentIdentity{IconEmoji: ":robot_face:", Username: "Custom Agent Name"}})
	if got := identity(set); got != (domain.AgentIdentity{IconEmoji: ":robot_face:", Username: "Custom Agent Name"}) {
		t.Fatalf("set=%+v", got)
	}
	kept := f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive})
	if got := identity(kept); got != (domain.AgentIdentity{IconEmoji: ":robot_face:", Username: "Custom Agent Name"}) {
		t.Fatalf("a call setting none must keep the set: %+v", got)
	}
	replaced := f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, Identity: domain.AgentIdentity{IconURL: "https://example.com/agent.png"}})
	if got := identity(replaced); got != (domain.AgentIdentity{IconURL: "https://example.com/agent.png"}) {
		t.Fatalf("a call setting one must clear the others: %+v", got)
	}
}

// TestAgentSessionRenameIsTheAgentsOwn covers agents.sessions.rename: an
// agent of the session renames it and is not sent agent_session_title_changed
// for its own rename; an app that is not an agent, a thread with no session
// and a title outside 1-200 characters are each refused with their own code.
func TestAgentSessionRenameIsTheAgentsOwn(t *testing.T) {
	f := newAgentSessionFixture(t)
	if _, err := f.messages.RenameAgentSession(f.ctx, "T1", "UB1", "A1", "C1", f.root, "Bora Bora trip prep"); !errors.Is(err, domain.ErrAgentSessionNotFound) {
		t.Fatalf("no session yet: %v", err)
	}
	f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing, Title: "Scuba diving research"})
	renamed, err := f.messages.RenameAgentSession(f.ctx, "T1", "UB1", "A1", "C1", f.root, "  Bora Bora trip prep  ")
	if err != nil || renamed.Title != "Bora Bora trip prep" {
		t.Fatalf("renamed=%+v err=%v", renamed, err)
	}
	if got := len(f.topicEvents(events.AgentSessionRenamedTopic)); got != 1 {
		t.Fatalf("renamed records=%d", got)
	}
	if got := len(f.topicEvents(events.AgentSessionTitleChangedTopic)); got != 0 {
		t.Fatalf("an app's own rename is not agent_session_title_changed: %d", got)
	}
	if _, err := f.messages.RenameAgentSession(f.ctx, "T1", "UB2", "A2", "C1", f.root, "Hijack"); !errors.Is(err, domain.ErrAgentSessionNotAgent) {
		t.Fatalf("an app that is not an agent: %v", err)
	}
	if _, err := f.messages.RenameAgentSession(f.ctx, "T1", "UB3", "A3", "C1", f.root, "Outsider"); !errors.Is(err, domain.ErrNotInConversation) {
		t.Fatalf("an app outside the channel: %v", err)
	}
	long := make([]rune, 201)
	for index := range long {
		long[index] = 'y'
	}
	for _, title := range []string{"", "   ", string(long)} {
		if _, err := f.messages.RenameAgentSession(f.ctx, "T1", "UB1", "A1", "C1", f.root, title); !errors.Is(err, domain.ErrInvalidAgentSession) {
			t.Fatalf("title %q: %v", title, err)
		}
	}
	if _, err := f.messages.RenameAgentSession(f.ctx, "T1", "UB1", "A1", "C1", "", "x"); !errors.Is(err, domain.ErrAgentSessionThreadRequired) {
		t.Fatalf("no thread: %v", err)
	}
}

// TestMemberRetitleSendsAgentSessionTitleChangedToEveryAgent is the
// agent_session_title_changed page: a member's change reaches every agent of
// the session — even the one that set the replaced title — with title,
// previous_title (omitted when there was none), user, channel, thread_ts and
// team_id, and only an app holding chat:write in the channel receives it.
func TestMemberRetitleSendsAgentSessionTitleChangedToEveryAgent(t *testing.T) {
	f := newAgentSessionFixture(t)
	f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing})
	f.setStatus(t, "A2", "UB2", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive})

	if _, err := f.messages.ChangeAgentSessionTitle(f.ctx, "T1", "U2", "C1", f.root, "Scuba diving research"); err != nil {
		t.Fatal(err)
	}
	first := f.topicEvents(events.AgentSessionTitleChangedTopic)
	if len(first) != 2 {
		t.Fatalf("one record per agent: %d", len(first))
	}
	inner, ok := f.slackEvent(t, "A1", first[0])
	if !ok {
		t.Fatal("A1 is not sent its agent_session_title_changed")
	}
	if inner["type"] != "agent_session_title_changed" || inner["channel"] != "C1" || inner["thread_ts"] != string(f.root) ||
		inner["user"] != "U2" || inner["title"] != "Scuba diving research" || inner["team_id"] != "T1" || inner["event_ts"] == "" {
		t.Fatalf("inner=%v", inner)
	}
	if _, present := inner["previous_title"]; present {
		t.Fatalf("previous_title must be omitted when the session had no title: %v", inner)
	}
	// Each record is addressed to its own agent: A1's is not A2's.
	if _, ok := f.slackEvent(t, "A2", first[0]); ok {
		t.Fatal("A1's record reached A2")
	}
	if _, ok := f.slackEvent(t, "A2", first[1]); !ok {
		t.Fatal("A2's record does not project")
	}
	// Delivery then applies each app's manifest, the rule HTTP and Socket
	// Mode share: A1 subscribes and keeps its callback, A2 does not and is
	// sent nothing.
	for _, test := range []struct {
		app    domain.AppID
		record events.Event
		want   int
	}{{"A1", first[0], 1}, {"A2", first[1], 0}} {
		_, parsed, err := f.messages.installedApp(f.ctx, "T1", test.app)
		if err != nil {
			t.Fatal(err)
		}
		prepared, _, err := PrepareAppEvent(f.ctx, f.state, appEventTestKey, "", test.app, events.Record{Sequence: 1, Event: test.record})
		if err != nil {
			t.Fatal(err)
		}
		bodies, err := events.SlackEventBodies(prepared, string(test.app))
		if err != nil {
			t.Fatal(err)
		}
		filtered, err := events.FilterSubscribedSlackEventBodies(f.ctx, bodies, parsed.BotEvents, parsed.UserEvents, f.state.GetConversation)
		if err != nil || len(filtered) != test.want {
			t.Fatalf("%s: %d callbacks after the subscription filter, want %d (err=%v)", test.app, len(filtered), test.want, err)
		}
	}

	if _, err := f.messages.ChangeAgentSessionTitle(f.ctx, "T1", "U1", "C1", f.root, "Bora Bora trip prep"); err != nil {
		t.Fatal(err)
	}
	second := f.topicEvents(events.AgentSessionTitleChangedTopic)[2]
	inner, _ = f.slackEvent(t, "A1", second)
	if inner["previous_title"] != "Scuba diving research" || inner["title"] != "Bora Bora trip prep" || inner["user"] != "U1" {
		t.Fatalf("inner=%v", inner)
	}
	// The same title again is no change and is reported to nobody.
	if _, err := f.messages.ChangeAgentSessionTitle(f.ctx, "T1", "U1", "C1", f.root, "Bora Bora trip prep"); err != nil {
		t.Fatal(err)
	}
	if got := len(f.topicEvents(events.AgentSessionTitleChangedTopic)); got != 4 {
		t.Fatalf("records=%d", got)
	}
	if _, err := f.messages.ChangeAgentSessionTitle(f.ctx, "T1", "U3", "C1", f.root, "Outsider"); !errors.Is(err, domain.ErrNotInConversation) {
		t.Fatalf("a member outside the channel: %v", err)
	}

	// The event needs chat:write: an agent whose grant lacks it is not sent it.
	if err := f.state.RevokeToken(f.ctx, "xoxb-A1"); err != nil {
		t.Fatal(err)
	}
	if err := f.state.SeedToken(f.ctx, "xoxb-A1-narrow", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB1", AppID: "A1", BotID: "BA1", TokenType: "bot", Scopes: []string{"channels:read"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.slackEvent(t, "A1", second); ok {
		t.Fatal("an agent without chat:write received agent_session_title_changed")
	}
}

// TestStoppingASessionSendsAgentSessionStoppedAndLeavesTheStatus is the
// agent_session_stopped page: a member stops a processing session, each
// processing agent whose app subscribes is sent the event with the
// timestamps of its streams Slack stopped (an empty list when none was
// running), those streams stop, and the status is left for the app to move.
func TestStoppingASessionSendsAgentSessionStoppedAndLeavesTheStatus(t *testing.T) {
	f := newAgentSessionFixture(t)
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U1", "C1", f.root); !errors.Is(err, domain.ErrAgentSessionNotFound) {
		t.Fatalf("no session: %v", err)
	}
	f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive})
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U1", "C1", f.root); !errors.Is(err, domain.ErrAgentSessionNotStoppable) {
		t.Fatalf("an active session has no stop control: %v", err)
	}
	// A2 processes but does not subscribe: Slack shows it no stop control.
	f.setStatus(t, "A2", "UB2", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing})
	view, err := f.messages.AgentSession(f.ctx, "T1", "U1", "C1", f.root)
	if err != nil || view.Stoppable {
		t.Fatalf("an unsubscribed processing agent is not stoppable: %+v err=%v", view, err)
	}
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U1", "C1", f.root); !errors.Is(err, domain.ErrAgentSessionNotStoppable) {
		t.Fatalf("unsubscribed: %v", err)
	}

	stream, err := f.messages.StartMessageStream(f.ctx, "T1", "UB1", domain.MessageStreamStart{
		Conversation: "C1", ThreadTimestamp: f.root, AppID: "A1", BotID: "BA1", MarkdownText: "Looking up dive sites",
		RecipientTeamID: "T1", RecipientUserID: "U1",
	})
	if err != nil {
		t.Fatal(err)
	}
	streamTS := domain.NewMessageTimestamp(stream.CreatedAt)
	otherStream, err := f.messages.StartMessageStream(f.ctx, "T1", "UB2", domain.MessageStreamStart{
		Conversation: "C1", ThreadTimestamp: f.root, AppID: "A2", BotID: "BA2", MarkdownText: "Not stopped",
		RecipientTeamID: "T1", RecipientUserID: "U1",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionProcessing})
	view, err = f.messages.AgentSession(f.ctx, "T1", "U2", "C1", f.root)
	if err != nil || !view.Stoppable {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U3", "C1", f.root); !errors.Is(err, domain.ErrNotInConversation) {
		t.Fatalf("a member outside the channel: %v", err)
	}
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U2", "C1", f.root); err != nil {
		t.Fatal(err)
	}
	stopped := f.topicEvents(events.AgentSessionStoppedTopic)
	if len(stopped) != 1 {
		t.Fatalf("only the subscribed processing agent is stopped: %d records", len(stopped))
	}
	inner, ok := f.slackEvent(t, "A1", stopped[0])
	if !ok {
		t.Fatal("A1 is not sent agent_session_stopped")
	}
	streams, _ := inner["streaming_message_ts"].([]any)
	if inner["type"] != "agent_session_stopped" || inner["channel"] != "C1" || inner["thread_ts"] != string(f.root) || inner["user"] != "U2" ||
		len(streams) != 1 || streams[0] != string(streamTS) || inner["event_ts"] == "" {
		t.Fatalf("inner=%v", inner)
	}
	if _, present := inner["team_id"]; present {
		t.Fatalf("team_id belongs on the envelope, not the event: %v", inner)
	}
	if inner["envelope_team_id"] != "T1" {
		t.Fatalf("envelope team_id=%v", inner["envelope_team_id"])
	}
	if _, ok := f.slackEvent(t, "A2", stopped[0]); ok {
		t.Fatal("A1's stop reached A2")
	}
	// End to end through the Socket Mode claim, which applies the projection
	// and the manifest filter: A1's first deliverable record is its stop, and
	// A2, subscribed to neither event, is handed nothing of the session.
	claim, found, err := f.messages.ClaimAppEvent(f.ctx, "A1", "socket", "agent-test", time.Minute)
	if err != nil || !found || claim.Record.Event.Topic != events.AgentSessionStoppedTopic {
		t.Fatalf("A1's first claim=%+v found=%t err=%v", claim.Record.Event.Topic, found, err)
	}
	for {
		other, found, err := f.messages.ClaimAppEvent(f.ctx, "A2", "socket", "agent-test", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if strings.HasPrefix(other.Record.Event.Topic, "agent_session.") {
			t.Fatalf("A2 was handed %s", other.Record.Event.Topic)
		}
		if err := f.messages.AckAppEvent(f.ctx, "A2", "socket", "agent-test", other.Record.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	// The stream Slack stopped no longer accepts appends; the other app's
	// stream runs on.
	if _, err := f.messages.AppendMessageStream(f.ctx, "T1", "UB1", domain.MessageStreamMutation{Conversation: "C1", Timestamp: streamTS, AppID: "A1", MarkdownText: "more"}); !errors.Is(err, domain.ErrMessageNotStreaming) {
		t.Fatalf("the stopped stream still appends: %v", err)
	}
	if _, err := f.messages.AppendMessageStream(f.ctx, "T1", "UB2", domain.MessageStreamMutation{Conversation: "C1", Timestamp: domain.NewMessageTimestamp(otherStream.CreatedAt), AppID: "A2", MarkdownText: "more"}); err != nil {
		t.Fatalf("another app's stream was stopped: %v", err)
	}
	// The status is the app's to move.
	session, err := f.state.GetAgentSession(f.ctx, "T1", "C1", f.root)
	if err != nil {
		t.Fatal(err)
	}
	if agent, _ := session.Agent("A1"); agent.Status != domain.AgentSessionProcessing {
		t.Fatalf("the stop moved the status: %s", agent.Status)
	}
	// Stopped again with nothing streaming, the list is empty, not absent.
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U1", "C1", f.root); err != nil {
		t.Fatal(err)
	}
	inner, _ = f.slackEvent(t, "A1", f.topicEvents(events.AgentSessionStoppedTopic)[1])
	if streams, ok := inner["streaming_message_ts"].([]any); !ok || len(streams) != 0 {
		t.Fatalf("streaming_message_ts=%#v", inner["streaming_message_ts"])
	}
	// Once the app moves the status, there is nothing left to stop.
	f.setStatus(t, "A1", "UB1", domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive})
	if _, err := f.messages.StopAgentSession(f.ctx, "T1", "U1", "C1", f.root); !errors.Is(err, domain.ErrAgentSessionNotStoppable) {
		t.Fatalf("after the app moved on: %v", err)
	}
}
