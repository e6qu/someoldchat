package slack

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// codeChannelAPI serves the Web API over a workspace where U2 asked, in C1,
// for a change, and two agents can act on it:
//
//   - xoxb-agent: app A1's bot UB1, a member of C1, with code_channels:manage
//     and chat:write;
//   - xoxb-other: app A2's bot UB2, a member of C1, with the same scopes;
//   - xoxb-plain: app A3's bot UB3, a member of C1, with chat:write only;
//   - xoxp-user: U1's user token, with code_channels:manage.
func codeChannelAPI(t *testing.T) (*http.ServeMux, *memory.Store, domain.MessageTimestamp) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	repository.SeedConversationMember("C1", "U2")
	now := time.Now().UTC()
	for _, app := range []struct {
		id  domain.AppID
		bot domain.UserID
	}{{"A1", "UB1"}, {"A2", "UB2"}, {"A3", "UB3"}} {
		repository.SeedUser(domain.User{ID: app.bot, WorkspaceID: "T1", Name: "bot-" + string(app.id)})
		repository.SeedConversationMember("C1", app.bot)
		client := "client-" + string(app.id)
		if err := repository.CreateApp(ctx,
			domain.App{ID: app.id, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: string(app.id), ClientID: client, SigningSecretHash: "hash", SigningSecretCiphertext: "sealed", VerificationTokenCiphertext: "sealed", ManifestVersion: 1, CreatedAt: now, UpdatedAt: now},
			domain.AppManifestRevision{AppID: app.id, Version: 1, CreatedBy: "U1", CreatedAt: now,
				Manifest: `{"display_information":{"name":"` + string(app.id) + `"},"features":{"code_channels":{"enabled":true}},"settings":{"event_subscriptions":{"bot_events":["agent_session_stopped"]}}}`},
			domain.OAuthClient{ID: client, SecretHash: "secret", AppID: app.id},
		); err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateBot(ctx, domain.Bot{ID: domain.BotID("B" + string(app.id)), WorkspaceID: "T1", AppID: app.id, UserID: app.bot, Name: string(app.id), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.id, WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	manage := []string{string(auth.ScopeCodeChannelsManage), string(auth.ScopeChatWrite), string(auth.ScopeChannelsRead), string(auth.ScopeGroupsRead)}
	for token, record := range map[string]domain.TokenRecord{
		"xoxb-agent": {WorkspaceID: "T1", UserID: "UB1", AppID: "A1", BotID: "BA1", TokenType: "bot", Scopes: manage},
		"xoxb-other": {WorkspaceID: "T1", UserID: "UB2", AppID: "A2", BotID: "BA2", TokenType: "bot", Scopes: manage},
		"xoxb-plain": {WorkspaceID: "T1", UserID: "UB3", AppID: "A3", BotID: "BA3", TokenType: "bot", Scopes: []string{string(auth.ScopeChatWrite)}},
		"xoxp-user":  {WorkspaceID: "T1", UserID: "U1", TokenType: "user", Scopes: manage},
	} {
		if err := repository.SeedToken(ctx, token, record); err != nil {
			t.Fatal(err)
		}
	}
	messages := service.Messages{Store: repository}
	origin, err := messages.Post(ctx, "T1", "U2", "C1", "Fix the flaky billing test!", "", "")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStored(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux, repository, domain.NewMessageTimestamp(origin.CreatedAt)
}

func requireCodeOK(t *testing.T, what string, result map[string]any) {
	t.Helper()
	if result["ok"] != true {
		t.Fatalf("%s: %v", what, result)
	}
}

func requireCodeError(t *testing.T, what string, result map[string]any, code string) {
	t.Helper()
	if result["ok"] != false || result["error"] != code {
		t.Fatalf("%s answered %v, want %s", what, result, code)
	}
}

// TestCodeChannelsAreCreatedDescribedAndArchived drives Slack Code's
// agents.conversations.create, setProperties and archive over HTTP, with the
// code channel's own agent session (agents.sessions.setStatus and
// agents.sessions.rename without thread_ts) and its conversations.info
// properties.
func TestCodeChannelsAreCreatedDescribedAndArchived(t *testing.T) {
	ctx := context.Background()
	mux, repository, origin := codeChannelAPI(t)

	// Created from the message the work began from: named after it, and its
	// author is invited.
	created := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create",
		`{"session_id":"ses_1","origin_channel_id":"C1","origin_message_ts":"`+string(origin)+`"}`, false)
	requireCodeOK(t, "create from an origin", created)
	channel, _ := created["channel_id"].(string)
	conversation, err := repository.GetConversation(ctx, domain.ConversationID(channel))
	if err != nil || conversation.Name != "fix-the-flaky-billing-test" {
		t.Fatalf("code channel=%+v err=%v", conversation, err)
	}
	for _, member := range []domain.UserID{"UB1", "U2"} {
		if in, err := repository.IsConversationMember(ctx, conversation.ID, member); err != nil || !in {
			t.Fatalf("%s is not in the code channel: %v", member, err)
		}
	}
	// The session key makes create idempotent.
	again := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", `{"session_id":"ses_1","name":"Something else"}`, false)
	if again["channel_id"] != channel {
		t.Fatalf("the same session answered another channel: %v", again)
	}
	// A friendly name is folded to a channel name, and a taken one numbered.
	named := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", "name=Migrate+billing+cron&is_private=true", true)
	requireCodeOK(t, "create by name", named)
	numbered := callAgentSessionMethod(t, mux, "xoxb-other", "agents.conversations.create", `{"name":"Migrate billing cron"}`, false)
	requireCodeOK(t, "create with a taken name", numbered)
	if second, err := repository.GetConversation(ctx, domain.ConversationID(numbered["channel_id"].(string))); err != nil || second.Name != "migrate-billing-cron-2" {
		t.Fatalf("numbered channel=%+v err=%v", second, err)
	}
	if first, err := repository.GetConversation(ctx, domain.ConversationID(named["channel_id"].(string))); err != nil || !first.PrivateFlag() {
		t.Fatalf("is_private was not honoured: %+v err=%v", first, err)
	}
	requireCodeError(t, "create with neither a name nor an origin", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", `{}`, false), "invalid_arguments")
	requireCodeError(t, "create with half an origin", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", `{"name":"x","origin_channel_id":"C1"}`, false), "invalid_arguments")
	requireCodeError(t, "create from a missing message", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", `{"origin_channel_id":"C1","origin_message_ts":"1.000001"}`, false), "message_not_found")
	requireCodeError(t, "a user token", callAgentSessionMethod(t, mux, "xoxp-user", "agents.conversations.create", `{"name":"x"}`, false), "not_allowed_token_type")
	requireCodeError(t, "a bot without the scope", callAgentSessionMethod(t, mux, "xoxb-plain", "agents.conversations.create", `{"name":"x"}`, false), "missing_scope")

	// The code channel's session is the channel's own: no thread_ts.
	requireCodeOK(t, "the session channel's session", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus",
		`{"channel_id":"`+channel+`","status":"processing","title":"Billing test"}`, false))
	requireCodeError(t, "a thread in a session channel", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus",
		`{"channel_id":"`+channel+`","thread_ts":"`+string(origin)+`","status":"active"}`, false), "thread_ts_not_allowed")
	requireCodeError(t, "no thread outside a session channel", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus",
		`{"channel_id":"C1","status":"active"}`, false), "thread_ts_required")

	// The context bar: each agent's items, the documented bounds.
	requireCodeOK(t, "set the context bar", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setProperties", `{"channel_id":"`+channel+`",
		"code_channel":{"context_bar_items":[{"key":"repo","label":"borant/billing","icon":"folder","url":"https://github.com/borant/billing"},{"key":"ci","label":"Tests pending","icon":"terminal"}]},
		"agent_resource":{"url":"https://github.com/borant/billing/pull/42","resource_type":"pull_request","title":"Fix the flaky test","provider":"github"}}`, false))
	requireCodeError(t, "an unknown icon", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setProperties",
		`{"channel_id":"`+channel+`","code_channel":{"context_bar_items":[{"key":"k","label":"l","icon":"rocket"}]}}`, false), "invalid_arguments")
	tooMany := `{"channel_id":"` + channel + `","code_channel":{"context_bar_items":[`
	for index := 0; index < 6; index++ {
		if index > 0 {
			tooMany += ","
		}
		tooMany += `{"key":"k` + string(rune('a'+index)) + `","label":"l"}`
	}
	requireCodeError(t, "six context bar items", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setProperties", tooMany+`]}}`, false), "invalid_arguments")
	requireCodeError(t, "a channel that is not a code channel", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setProperties",
		`{"channel_id":"C1","agent_resource":{"title":"x"}}`, false), "channel_not_found")

	info := getAPIWithToken(mux, "/api/conversations.info?channel="+channel, "xoxb-agent")
	for _, want := range []string{`"code_channel":{"context_bar_items":[`, `"key":"repo"`, `"bot_user_id":"UB1"`, `"agent_session":`, `"status":"processing"`, `"title":"Billing test"`, `"origin_link":{"channel_id":"C1"`} {
		if !strings.Contains(info.Body.String(), want) {
			t.Fatalf("conversations.info lacks %s: %s", want, info.Body)
		}
	}

	// Renaming the session channel's session renames the channel.
	requireCodeOK(t, "rename the session channel", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.rename", `{"channel_id":"`+channel+`","title":"Billing flake fix"}`, false))
	if renamed, err := repository.GetConversation(ctx, domain.ConversationID(channel)); err != nil || renamed.Name != "billing-flake-fix" {
		t.Fatalf("renamed channel=%+v err=%v", renamed, err)
	}

	// Archiving shares the summary back as a reply on the origin message.
	summary, err := service.Messages{Store: repository}.Post(ctx, "T1", "UB1", domain.ConversationID(channel), "Fixed: the test now waits for the clock.", "", "")
	if err != nil {
		t.Fatal(err)
	}
	requireCodeOK(t, "archive with a summary", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.archive",
		`{"channel_id":"`+channel+`","summary_message_ts":"`+string(domain.NewMessageTimestamp(summary.CreatedAt))+`"}`, false))
	replies, err := repository.ListThreadMessages(ctx, "C1", origin, domain.ThreadRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	shared := false
	for _, reply := range replies.Messages {
		shared = shared || (reply.AuthorID == "UB1" && reply.Text == "Fixed: the test now waits for the clock.")
	}
	if !shared {
		t.Fatalf("the summary was not shared back on the origin message: %+v", replies.Messages)
	}
	if archived, err := repository.GetConversation(ctx, domain.ConversationID(channel)); err != nil || !archived.Archived {
		t.Fatalf("the code channel was not archived: %+v err=%v", archived, err)
	}
	requireCodeError(t, "archive again", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.archive", `{"channel_id":"`+channel+`"}`, false), "already_archived")
	requireCodeError(t, "a summary without an origin", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.archive",
		`{"channel_id":"`+named["channel_id"].(string)+`","summary_message_ts":"1.000001"}`, false), "invalid_arguments")
}
