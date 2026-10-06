package slack

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
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// agentSessionAPI serves the Web API, and returns the store behind it, over a
// workspace with:
//
//   - xoxb-agent: app A1's bot UB1, a member of C1, chat:write, subscribed to
//     agent_session_stopped;
//   - xoxb-custom: the same bot with chat:write.customize as well;
//   - xoxb-quiet: app A2's bot UB2, a member of C1, not subscribed;
//   - xoxb-outside: app A3's bot UB3, not a member of C1;
//   - xoxp-user: U1's user token, chat:write.
func agentSessionAPI(t *testing.T) (*http.ServeMux, domain.MessageTimestamp, *memory.Store) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	now := time.Now().UTC()
	for _, app := range []struct {
		id         domain.AppID
		bot        domain.UserID
		member     bool
		subscribed bool
	}{{"A1", "UB1", true, true}, {"A2", "UB2", true, false}, {"A3", "UB3", false, true}} {
		repository.SeedUser(domain.User{ID: app.bot, WorkspaceID: "T1", Name: "bot-" + string(app.id)})
		if app.member {
			repository.SeedConversationMember("C1", app.bot)
		}
		subscriptions := `["message.channels"]`
		if app.subscribed {
			subscriptions = `["agent_session_stopped"]`
		}
		client := "client-" + string(app.id)
		if err := repository.CreateApp(ctx,
			domain.App{ID: app.id, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: string(app.id), ClientID: client, SigningSecretHash: "hash", SigningSecretCiphertext: "sealed", VerificationTokenCiphertext: "sealed", ManifestVersion: 1, CreatedAt: now, UpdatedAt: now},
			domain.AppManifestRevision{AppID: app.id, Version: 1, CreatedBy: "U1", CreatedAt: now,
				Manifest: `{"display_information":{"name":"` + string(app.id) + `"},"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":` + subscriptions + `}}}`},
			domain.OAuthClient{ID: client, SecretHash: "secret", AppID: app.id},
		); err != nil {
			t.Fatal(err)
		}
		botID := domain.BotID("B" + string(app.id))
		if err := repository.CreateBot(ctx, domain.Bot{ID: botID, WorkspaceID: "T1", AppID: app.id, UserID: app.bot, Name: string(app.id), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.id, WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for token, record := range map[string]domain.TokenRecord{
		"xoxb-agent":   {WorkspaceID: "T1", UserID: "UB1", AppID: "A1", BotID: "BA1", TokenType: "bot", Scopes: []string{string(auth.ScopeChatWrite)}},
		"xoxb-custom":  {WorkspaceID: "T1", UserID: "UB1", AppID: "A1", BotID: "BA1", TokenType: "bot", Scopes: []string{string(auth.ScopeChatWrite), string(auth.ScopeChatWriteCustomize)}},
		"xoxb-quiet":   {WorkspaceID: "T1", UserID: "UB2", AppID: "A2", BotID: "BA2", TokenType: "bot", Scopes: []string{string(auth.ScopeChatWrite)}},
		"xoxb-outside": {WorkspaceID: "T1", UserID: "UB3", AppID: "A3", BotID: "BA3", TokenType: "bot", Scopes: []string{string(auth.ScopeChatWrite)}},
		"xoxp-user":    {WorkspaceID: "T1", UserID: "U1", TokenType: "user", Scopes: []string{string(auth.ScopeChatWrite)}},
	} {
		if err := repository.SeedToken(ctx, token, record); err != nil {
			t.Fatal(err)
		}
	}
	messages := service.Messages{Store: repository}
	root, err := messages.Post(ctx, "T1", "U1", "C1", "Plan the trip", "", "")
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
	return mux, domain.NewMessageTimestamp(root.CreatedAt), repository
}

// callAgentSessionMethod posts a JSON body, or a form body when form is set,
// and decodes the answer, which must be HTTP 200 whatever it says.
func callAgentSessionMethod(t *testing.T, mux *http.ServeMux, token, method, body string, form bool) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if form {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestAgentsSessionsSetStatusAnswersTheReferenceShapes drives
// agents.sessions.setStatus over HTTP: the documented success response
// ({ok, status, agent_status, title}), the session-level status when two
// agents differ, the missing_agent_session_stopped_event_subscription warning,
// the JSON null that leaves an identity override alone, and each listed error
// the reference names for what this deployment can produce.
func TestAgentsSessionsSetStatusAnswersTheReferenceShapes(t *testing.T) {
	mux, thread, _ := agentSessionAPI(t)
	key := `"channel_id":"C1","thread_ts":"` + string(thread) + `"`

	created := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus", `{`+key+`,"status":"processing","title":"Scuba diving research","initiator_user_id":"U1"}`, false)
	if created["ok"] != true || created["status"] != "processing" || created["agent_status"] != "processing" || created["title"] != "Scuba diving research" {
		t.Fatalf("created=%v", created)
	}
	if _, warned := created["warning"]; warned {
		t.Fatalf("a subscribed app is not warned: %v", created)
	}
	// Form encoding and GET reach the same method.
	quiet := callAgentSessionMethod(t, mux, "xoxb-quiet", "agents.sessions.setStatus", url.Values{"channel_id": {"C1"}, "thread_ts": {string(thread)}, "status": {"active"}}.Encode(), true)
	metadata, _ := quiet["response_metadata"].(map[string]any)
	warnings, _ := metadata["warnings"].([]any)
	if quiet["ok"] != true || quiet["status"] != "processing" || quiet["agent_status"] != "active" ||
		quiet["warning"] != "missing_agent_session_stopped_event_subscription" || len(warnings) != 1 || warnings[0] != "missing_agent_session_stopped_event_subscription" {
		t.Fatalf("the page's multi-agent example and the warning: %v", quiet)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/agents.sessions.setStatus?"+url.Values{"channel_id": {"C1"}, "thread_ts": {string(thread)}, "status": {"suspended"}}.Encode(), nil)
	get.Header.Set("Authorization", "Bearer xoxb-agent")
	got := httptest.NewRecorder()
	mux.ServeHTTP(got, get)
	if !strings.Contains(got.Body.String(), `"status":"suspended"`) {
		t.Fatalf("GET body=%s", got.Body)
	}
	// The identity override: chat:write.customize, and a JSON null for each
	// of the three is the same as leaving it out.
	customized := callAgentSessionMethod(t, mux, "xoxb-custom", "agents.sessions.setStatus", `{`+key+`,"status":"processing","icon_emoji":":robot_face:","username":"Custom Agent Name","icon_url":null}`, false)
	if customized["ok"] != true {
		t.Fatalf("customized=%v", customized)
	}
	nulls := callAgentSessionMethod(t, mux, "xoxb-custom", "agents.sessions.setStatus", `{`+key+`,"status":"processing","icon_emoji":null,"icon_url":null,"username":null}`, false)
	if nulls["ok"] != true {
		t.Fatalf("all three null=%v", nulls)
	}

	for name, test := range map[string]struct {
		token string
		body  string
		form  bool
		want  string
	}{
		"user token":                   {"xoxp-user", `{` + key + `,"status":"active"}`, false, "not_allowed_token_type"},
		"no channel":                   {"xoxb-agent", `{"thread_ts":"` + string(thread) + `","status":"active"}`, false, "invalid_arguments"},
		"no thread":                    {"xoxb-agent", `{"channel_id":"C1","status":"active"}`, false, "thread_ts_required"},
		"no status":                    {"xoxb-agent", `{` + key + `}`, false, "invalid_arguments"},
		"unknown status":               {"xoxb-agent", `{` + key + `,"status":"thinking"}`, false, "invalid_status"},
		"app outside the channel":      {"xoxb-outside", `{` + key + `,"status":"active"}`, false, "not_authorized"},
		"no such channel":              {"xoxb-agent", `{"channel_id":"CNOPE","thread_ts":"` + string(thread) + `","status":"active"}`, false, "channel_not_found"},
		"customizing without scope":    {"xoxb-agent", `{` + key + `,"status":"active","username":"Someone"}`, false, "missing_scope"},
		"a null elsewhere is no value": {"xoxb-agent", `{` + key + `,"status":null}`, false, "invalid_arg_name"},
	} {
		t.Run(name, func(t *testing.T) {
			result := callAgentSessionMethod(t, mux, test.token, "agents.sessions.setStatus", test.body, test.form)
			if result["ok"] != false || result["error"] != test.want {
				t.Fatalf("result=%v, want %s", result, test.want)
			}
		})
	}
	scope := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus", `{`+key+`,"status":"active","icon_emoji":":x:"}`, false)
	if scope["needed"] != "chat:write.customize" {
		t.Fatalf("missing_scope names the scope: %v", scope)
	}
	// A new session's arguments are validated where they create it.
	other := callAgentSessionMethod(t, mux, "xoxp-user", "chat.postMessage", `{"channel":"C1","text":"another"}`, false)
	otherTS, _ := other["ts"].(string)
	unknown := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus", `{"channel_id":"C1","thread_ts":"`+otherTS+`","status":"active","initiator_user_id":"UNOBODY"}`, false)
	if unknown["error"] != "user_not_found" {
		t.Fatalf("unknown initiator=%v", unknown)
	}
	long := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus", `{"channel_id":"C1","thread_ts":"`+otherTS+`","status":"active","title":"`+strings.Repeat("t", 201)+`"}`, false)
	if long["error"] != "invalid_arguments" {
		t.Fatalf("over-long title=%v", long)
	}
}

// TestAgentsSessionsRenameAnswersTheReferenceShapes drives
// agents.sessions.rename over HTTP: {ok, title} for an agent of the session,
// session_not_found, not_authorized for an app that is not an agent,
// no_permission for an app outside the channel, thread_ts_required and
// invalid_arguments for a title outside 1-200 characters.
func TestAgentsSessionsRenameAnswersTheReferenceShapes(t *testing.T) {
	mux, thread, _ := agentSessionAPI(t)
	key := `"channel_id":"C1","thread_ts":"` + string(thread) + `"`
	missing := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.rename", `{`+key+`,"title":"Bora Bora trip prep"}`, false)
	if missing["ok"] != false || missing["error"] != "session_not_found" {
		t.Fatalf("missing=%v", missing)
	}
	callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.setStatus", `{`+key+`,"status":"processing","title":"Scuba diving research"}`, false)
	renamed := callAgentSessionMethod(t, mux, "xoxb-agent", "agents.sessions.rename", url.Values{"channel_id": {"C1"}, "thread_ts": {string(thread)}, "title": {"Bora Bora trip prep"}}.Encode(), true)
	if renamed["ok"] != true || renamed["title"] != "Bora Bora trip prep" {
		t.Fatalf("renamed=%v", renamed)
	}
	for name, test := range map[string]struct {
		token string
		body  string
		want  string
	}{
		"not an agent":        {"xoxb-quiet", `{` + key + `,"title":"Mine now"}`, "not_authorized"},
		"outside the channel": {"xoxb-outside", `{` + key + `,"title":"Mine now"}`, "no_permission"},
		"no thread":           {"xoxb-agent", `{"channel_id":"C1","title":"x"}`, "thread_ts_required"},
		"empty title":         {"xoxb-agent", `{` + key + `,"title":"   "}`, "invalid_arguments"},
		"title over 200":      {"xoxb-agent", `{` + key + `,"title":"` + strings.Repeat("t", 201) + `"}`, "invalid_arguments"},
		"user token":          {"xoxp-user", `{` + key + `,"title":"x"}`, "not_allowed_token_type"},
	} {
		t.Run(name, func(t *testing.T) {
			result := callAgentSessionMethod(t, mux, test.token, "agents.sessions.rename", test.body, false)
			if result["ok"] != false || result["error"] != test.want {
				t.Fatalf("result=%v, want %s", result, test.want)
			}
		})
	}
}
