package slack

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
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

// TestCodeChannelViewsAreTabsAnAgentKeepsCurrent drives
// agents.conversations.setView, listViews and removeView over HTTP: a view is
// upserted by the agent's key and versioned, a diff is the channel's one diff,
// each kind requires its own argument, and a view is removed by its key or tab.
func TestCodeChannelViewsAreTabsAnAgentKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	mux, repository, _ := codeChannelAPI(t)
	created := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", `{"name":"Coverage work"}`, false)
	channel := created["channel_id"].(string)
	set := func(body string) map[string]any {
		return callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setView", `{"channel_id":"`+channel+`",`+body+`}`, false)
	}

	html := set(`"view_key":"reports/coverage.html","content":"<!doctype html><p>81%</p>","csp":{"resource_domains":["https://cdn.jsdelivr.net"]}`)
	requireCodeOK(t, "an html view", html)
	if html["type"] != "html" || html["content_version"] != float64(1) || !strings.HasPrefix(html["view_id"].(string), "Ct") || !strings.HasPrefix(html["file_id"].(string), "F") {
		t.Fatalf("html view=%v", html)
	}
	updated := set(`"view_key":"reports/coverage.html","content":"<!doctype html><p>84%</p>"`)
	if updated["view_id"] != html["view_id"] || updated["file_id"] != html["file_id"] || updated["content_version"] != float64(2) {
		t.Fatalf("an update is not the same view, one version on: %v then %v", html, updated)
	}
	diff := set(`"type":"diff","content":"--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n","base_branch":"main","head_branch":"agent/fix"`)
	again := set(`"type":"diff","view_key":"ignored","content":"--- a/y\n+++ b/y\n"`)
	if again["view_id"] != diff["view_id"] || again["content_version"] != float64(2) {
		t.Fatalf("a diff is not the channel's one diff: %v then %v", diff, again)
	}
	requireCodeOK(t, "a block_kit view", set(`"type":"block_kit","view_key":"status","name":"Status","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*Green*"}}]`))
	requireCodeOK(t, "a pull_request view", set(`"type":"pull_request","view_key":"pr","pr_url":"https://github.com/borant/billing/pull/42"`))
	canvas, err := service.Messages{Store: repository}.CreateCanvas(ctx, "T1", "UB1", "Plan", "", "")
	if err != nil {
		t.Fatal(err)
	}
	canvasView := set(`"type":"canvas","view_key":"plan","canvas_id":"` + string(canvas.ID) + `","access_level":"comment"`)
	if canvasView["canvas_id"] != string(canvas.ID) {
		t.Fatalf("canvas view=%v", canvasView)
	}

	requireCodeError(t, "content over the cap", set(`"view_key":"big.html","content":"`+strings.Repeat("x", domain.CodeChannelViewContentLimit+1)+`"`), "content_too_large")
	requireCodeError(t, "an html view without content", set(`"view_key":"empty.html"`), "invalid_arguments")
	requireCodeError(t, "a block_kit view without blocks", set(`"type":"block_kit","view_key":"x"`), "invalid_arguments")
	requireCodeError(t, "an unknown kind", set(`"type":"pdf","view_key":"x","content":"x"`), "invalid_arguments")
	requireCodeError(t, "a plain-http CSP origin", set(`"view_key":"x.html","content":"x","csp":{"resource_domains":["http://cdn.example.com"]}`), "invalid_arguments")
	requireCodeError(t, "a private CSP origin", set(`"view_key":"x.html","content":"x","csp":{"connect_domains":["https://10.0.0.1"]}`), "invalid_arguments")
	requireCodeError(t, "a canvas that does not exist", set(`"type":"canvas","view_key":"x","canvas_id":"F00000000"`), "canvas_not_found")
	requireCodeError(t, "invalid blocks", set(`"type":"block_kit","view_key":"x","blocks":[{"type":"nonsense"}]`), "invalid_blocks")

	listed := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.listViews", `{"channel_id":"`+channel+`"}`, false)
	views, _ := listed["views"].([]any)
	if len(views) != 5 {
		t.Fatalf("listViews=%v, want five views", listed)
	}
	first := views[0].(map[string]any)
	if first["view_key"] != "reports/coverage.html" || first["label"] != "coverage" || first["content_version"] != float64(2) || first["date_added"] == nil {
		t.Fatalf("the first view=%v", first)
	}
	byKey := map[string]string{}
	for _, view := range views {
		entry := view.(map[string]any)
		byKey[entry["view_key"].(string)] = entry["label"].(string)
	}
	if byKey["diff"] != "Diff" || byKey["status"] != "Status" || byKey["pr"] != "pr" {
		t.Fatalf("labels=%v", byKey)
	}

	remove := func(body string) map[string]any {
		return callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.removeView", `{"channel_id":"`+channel+`",`+body+`}`, false)
	}
	requireCodeOK(t, "remove by key", remove(`"view_key":"pr"`))
	requireCodeOK(t, "remove by tab", remove(`"view_id":"`+html["view_id"].(string)+`"`))
	requireCodeError(t, "remove by both", remove(`"view_key":"status","view_id":"`+diff["view_id"].(string)+`"`), "invalid_arguments")
	requireCodeError(t, "remove a view that is gone", remove(`"view_key":"pr"`), "not_found")
	left, err := repository.ListCodeChannelViews(ctx, "T1", domain.ConversationID(channel))
	if err != nil || len(left) != 3 {
		t.Fatalf("views left=%+v err=%v", left, err)
	}
	requireCodeError(t, "listViews on a channel that is not a code channel",
		callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.listViews", `{"channel_id":"C1"}`, false), "channel_not_found")
}

// TestCodeChannelCommandsAndCanvasesBelongToTheirAgents drives setCommands,
// getCanvas and setCanvasContent over HTTP: each agent replaces only its own
// commands under the channel's limit of ten, and an agent reads and rewrites
// a canvas its channel shows, keeping the sections it did not change.
func TestCodeChannelCommandsAndCanvasesBelongToTheirAgents(t *testing.T) {
	ctx := context.Background()
	mux, repository, _ := codeChannelAPI(t)
	created := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.create", `{"name":"Command work"}`, false)
	channel := created["channel_id"].(string)
	for _, member := range []domain.UserID{"UB2", "U1"} {
		if err := repository.SeedConversationMember(domain.ConversationID(channel), member); err != nil {
			t.Fatal(err)
		}
	}
	commands := func(token, body string) map[string]any {
		return callAgentSessionMethod(t, mux, token, "agents.conversations.setCommands", `{"channel_id":"`+channel+`","commands":`+body+`}`, false)
	}
	first := commands("xoxb-agent", `[{"name":"review","description":"Review the diff","argument_hint":"[path]","should_escape":true},{"name":"ship"}]`)
	if first["ok"] != true || first["channel_id"] != channel || first["command_count"] != float64(2) {
		t.Fatalf("setCommands=%v", first)
	}
	if other := commands("xoxb-other", `[{"name":"test"}]`); other["command_count"] != float64(3) {
		t.Fatalf("a second agent's set=%v, want three in the channel", other)
	}
	if replaced := commands("xoxb-agent", `[{"name":"review"}]`); replaced["command_count"] != float64(2) {
		t.Fatalf("a replaced set=%v, want the other agent's command kept", replaced)
	}
	requireCodeError(t, "another agent's name", commands("xoxb-other", `[{"name":"review"}]`), "invalid_arguments")
	requireCodeError(t, "a Slack command", commands("xoxb-agent", `[{"name":"remind"}]`), "invalid_arguments")
	requireCodeError(t, "more than ten in the channel", commands("xoxb-agent", `[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"},{"name":"f"},{"name":"g"},{"name":"h"},{"name":"i"},{"name":"j"}]`), "invalid_arguments")
	requireCodeError(t, "no commands argument", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setCommands", `{"channel_id":"`+channel+`"}`, false), "invalid_arguments")
	requireCodeError(t, "a channel that is not a code channel", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setCommands", `{"channel_id":"C1","commands":[]}`, false), "channel_not_found")
	form := url.Values{"channel_id": {channel}, "commands": {`[{"name":"deploy"}]`}}.Encode()
	if formed := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setCommands", form, true); formed["command_count"] != float64(2) {
		t.Fatalf("a form-encoded set=%v", formed)
	}

	messages := service.Messages{Store: repository}
	canvas, err := messages.CreateCanvas(ctx, "T1", "UB1", "Plan", `{"type":"markdown","markdown":"# Plan\n\nPort the cron.\n\nShip it."}`, "")
	if err != nil {
		t.Fatal(err)
	}
	getCanvas := func(token, body string) map[string]any {
		return callAgentSessionMethod(t, mux, token, "agents.conversations.getCanvas", `{"channel":"`+channel+`","canvas_id":"`+string(canvas.ID)+`"`+body+`}`, false)
	}
	requireCodeError(t, "a canvas the channel does not show", getCanvas("xoxb-agent", ``), "canvas_not_found")
	requireCodeOK(t, "a canvas view", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setView",
		`{"channel_id":"`+channel+`","type":"canvas","view_key":"plan","canvas_id":"`+string(canvas.ID)+`"}`, false))
	sections, err := domain.CanvasDocumentSections(canvas.DocumentContent)
	if err != nil || len(sections) != 3 {
		t.Fatalf("sections=%+v err=%v", sections, err)
	}
	if _, err := messages.CommentOnCanvas(ctx, "T1", "U1", canvas.ID, sections[1].ID, "Which cron?"); err != nil {
		t.Fatal(err)
	}
	read := getCanvas("xoxb-agent", ``)
	comments, _ := read["comments"].([]any)
	if read["ok"] != true || read["canvas_id"] != string(canvas.ID) || read["title"] != "Plan" || read["content"] != "# Plan\n\nPort the cron.\n\nShip it.\n" ||
		len(comments) != 1 || read["has_more_comments"] != false {
		t.Fatalf("getCanvas=%v", read)
	}
	comment := comments[0].(map[string]any)
	if comment["text"] != "Which cron?" || comment["user_id"] != "U1" || comment["quoted_text"] != "Port the cron." || comment["is_resolved"] != false || comment["ts"] == "" {
		t.Fatalf("comment=%v", comment)
	}
	if html := getCanvas("xoxb-agent", `,"content_format":"html"`); !strings.Contains(html["content"].(string), "<h1") {
		t.Fatalf("html content=%v", html)
	}
	requireCodeError(t, "an unknown format", getCanvas("xoxb-agent", `,"content_format":"pdf"`), "invalid_arguments")
	// The canvas view shared the canvas with the channel, so every agent in
	// it reads the canvas as its members do.
	requireCodeOK(t, "another agent of the channel", getCanvas("xoxb-other", ``))
	requireCodeError(t, "the canvas through a channel that does not show it", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.getCanvas",
		`{"channel":"C1","canvas_id":"`+string(canvas.ID)+`"}`, false), "channel_not_found")

	setContent := func(content string) map[string]any {
		return callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setCanvasContent",
			`{"channel":"`+channel+`","canvas_id":"`+string(canvas.ID)+`","content":`+strconv.Quote(content)+`}`, false)
	}
	set := setContent("# Plan\n\nPort the cron.\n\nShip it on Friday.\n\nTell billing.")
	if set["ok"] != true || set["canvas_id"] != string(canvas.ID) || set["sections_changed_count"] != float64(2) {
		t.Fatalf("setCanvasContent=%v", set)
	}
	if again := setContent("# Plan\n\nPort the cron.\n\nShip it on Friday.\n\nTell billing."); again["sections_changed_count"] != float64(0) {
		t.Fatalf("the same content again=%v", again)
	}
	stored, err := repository.GetCanvas(ctx, "T1", canvas.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := domain.CanvasDocumentSections(stored.DocumentContent)
	if err != nil || len(after) != 4 || after[0].ID != sections[0].ID || after[1].ID != sections[1].ID || after[2].Text != "Ship it on Friday." {
		t.Fatalf("sections after=%+v err=%v; the unchanged sections must keep their identifiers", after, err)
	}
	if kept := getCanvas("xoxb-agent", ``); kept["comments"].([]any)[0].(map[string]any)["quoted_text"] != "Port the cron." {
		t.Fatalf("the comment lost its section: %v", kept)
	}
	requireCodeError(t, "no content", callAgentSessionMethod(t, mux, "xoxb-agent", "agents.conversations.setCanvasContent",
		`{"channel":"`+channel+`","canvas_id":"`+string(canvas.ID)+`"}`, false), "invalid_arguments")
}
