package slackapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestEventProcessorUsesInstalledManifestSubscriptionsSigningAndSlackRetryHeaders(t *testing.T) {
	ctx := context.Background()
	var mutex sync.Mutex
	var signingSecret string
	var callbacks int
	var retryNumber, retryReason string
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		var payload struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			http.Error(w, "json", http.StatusBadRequest)
			return
		}
		if payload.Type == "url_verification" {
			_, _ = io.WriteString(w, payload.Challenge)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		callbacks++
		timestamp, err := strconv.ParseInt(r.Header.Get("X-Slack-Request-Timestamp"), 10, 64)
		if err != nil {
			t.Errorf("timestamp: %v", err)
		}
		expected, err := events.SlackSignature(signingSecret, time.Unix(timestamp, 0).UTC(), body)
		if err != nil || expected != r.Header.Get("X-Slack-Signature") {
			t.Errorf("signature=%q want=%q err=%v", r.Header.Get("X-Slack-Signature"), expected, err)
		}
		retryNumber = r.Header.Get("X-Slack-Retry-Num")
		retryReason = r.Header.Get("X-Slack-Retry-Reason")
		if callbacks == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	messages := service.Messages{Store: repository, AppCredentialKey: key, AppHTTPClient: receiver.Client()}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Events"},"settings":{"event_subscriptions":{"request_url":"` + receiver.URL + `","bot_events":["reaction_added"]}}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	signingSecret = credentials.SigningSecret
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.ID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "events-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember("C1", "UBOT"); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: app.ID, UserID: "UBOT", Name: "events-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-events", domain.TokenRecord{WorkspaceID: "T1", UserID: "UBOT", AppID: app.ID, BotID: "B1", TokenType: "bot", Scopes: []string{"reactions:read"}}); err != nil {
		t.Fatal(err)
	}
	event, err := events.New("evt_reaction", "T1", "U1", events.NewPayload("reaction.added",
		events.String("channel_id", "C1"),
		events.String("user_id", "U1"),
		events.String("reaction", "wave"),
		events.String("ts", "1700000000.000001"),
	), time.Unix(1700000001, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000100, 0).UTC()
	processor := EventProcessor{Store: repository, AppCredentialKey: key, Owner: "worker-1", Lease: time.Minute, Client: receiver.Client(), Now: func() time.Time { return now }}
	// Slack's first retry is immediate, so the failed delivery and its retry
	// both happen in one cycle; the failure is still reported.
	if count, err := processor.RunOnce(ctx); err == nil || count != 1 {
		t.Fatalf("delivery count=%d err=%v, want one record delivered on its retry and the failure reported", count, err)
	}
	if count, err := processor.RunOnce(ctx); err != nil || count != 0 {
		t.Fatalf("a settled record was delivered again: count=%d err=%v", count, err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if callbacks != 2 || retryNumber != "1" || retryReason != "http_error" {
		t.Fatalf("callbacks=%d retry-num=%q retry-reason=%q", callbacks, retryNumber, retryReason)
	}
}

func TestEventProcessorHydratesARealMessageOnlyForTheInstalledBot(t *testing.T) {
	ctx := context.Background()
	var received string
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var envelope struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Error(err)
			return
		}
		if envelope.Type == "url_verification" {
			_, _ = io.WriteString(w, envelope.Challenge)
			return
		}
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	repository.SeedConversationMember("C1", "U1")
	key := []byte(strings.Repeat("k", 32))
	messages := service.Messages{Store: repository, AppCredentialKey: key, AppHTTPClient: receiver.Client()}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Messages"},"oauth_config":{"scopes":{"bot":["chat:write","groups:history"]}},"settings":{"event_subscriptions":{"request_url":"` + receiver.URL + `","bot_events":["message.groups"]}}}`
	app, _, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.ID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "messages-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: app.ID, UserID: "UB", Name: "messages-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-messages", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: app.ID, BotID: "B1", TokenType: "bot", Scopes: []string{"groups:history"}}); err != nil {
		t.Fatal(err)
	}
	bot, err := repository.GetBotByApp(ctx, "T1", app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember("C1", bot.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "real private message", "", ""); err != nil {
		t.Fatal(err)
	}

	processor := EventProcessor{Store: repository, AppCredentialKey: key, Owner: "worker-1", Lease: time.Minute, Client: receiver.Client()}
	for attempt := 0; attempt < 20 && received == ""; attempt++ {
		if _, err := processor.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(received, `"type":"message"`) || !strings.Contains(received, `"text":"real private message"`) || !strings.Contains(received, `"channel":"C1"`) {
		t.Fatalf("callback=%s", received)
	}
}

// A posted link on an app's unfurl domain reaches the app's request URL as a
// signed link_shared callback, even from a public channel its bot has not
// joined, and a link on no domain of the app's does not.
func TestEventProcessorDeliversLinkSharedForTheAppsUnfurlDomains(t *testing.T) {
	ctx := context.Background()
	var mutex sync.Mutex
	var signingSecret string
	var received []string
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var envelope struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Error(err)
			return
		}
		if envelope.Type == "url_verification" {
			_, _ = io.WriteString(w, envelope.Challenge)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		timestamp, err := strconv.ParseInt(r.Header.Get("X-Slack-Request-Timestamp"), 10, 64)
		if err != nil {
			t.Errorf("timestamp: %v", err)
		}
		if expected, err := events.SlackSignature(signingSecret, time.Unix(timestamp, 0).UTC(), body); err != nil || expected != r.Header.Get("X-Slack-Signature") {
			t.Errorf("signature=%q want=%q err=%v", r.Header.Get("X-Slack-Signature"), expected, err)
		}
		received = append(received, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "links-bot"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	key := []byte(strings.Repeat("k", 32))
	messages := service.Messages{Store: repository, AppCredentialKey: key, AppHTTPClient: receiver.Client()}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Links"},"features":{"unfurl_domains":["example.com"]},"oauth_config":{"scopes":{"bot":["links:read","links:write"]}},"settings":{"event_subscriptions":{"request_url":"` + receiver.URL + `","bot_events":["link_shared"]}}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	signingSecret = credentials.SigningSecret
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.ID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: app.ID, UserID: "UB", Name: "links-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-links", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: app.ID, BotID: "B1", TokenType: "bot", Scopes: []string{"links:read", "links:write"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "nothing of ours: https://elsewhere.test/a", "", ""); err != nil {
		t.Fatal(err)
	}
	posted, err := messages.Post(ctx, "T1", "U1", "C1", "look at <https://app.example.com/items/7|item 7>", "", "")
	if err != nil {
		t.Fatal(err)
	}
	processor := EventProcessor{Store: repository, AppCredentialKey: key, Owner: "worker-1", Lease: time.Minute, Client: receiver.Client()}
	for attempt := 0; attempt < 20; attempt++ {
		count, err := processor.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(received) != 1 {
		t.Fatalf("callbacks=%q, want the one link_shared", received)
	}
	var envelope struct {
		Type     string `json:"type"`
		APIAppID string `json:"api_app_id"`
		Event    struct {
			Type            string              `json:"type"`
			Channel         string              `json:"channel"`
			IsBotUserMember *bool               `json:"is_bot_user_member"`
			User            string              `json:"user"`
			MessageTS       string              `json:"message_ts"`
			UnfurlID        string              `json:"unfurl_id"`
			Source          string              `json:"source"`
			Links           []domain.SharedLink `json:"links"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(received[0]), &envelope); err != nil {
		t.Fatal(err)
	}
	ts := domain.NewMessageTimestamp(posted.CreatedAt)
	if envelope.Type != "event_callback" || envelope.APIAppID != string(app.ID) || envelope.Event.Type != "link_shared" ||
		envelope.Event.Channel != "C1" || envelope.Event.IsBotUserMember == nil || *envelope.Event.IsBotUserMember ||
		envelope.Event.User != "U1" || envelope.Event.MessageTS != string(ts) || envelope.Event.Source != "conversations_history" ||
		envelope.Event.UnfurlID != domain.NewUnfurlID("C1", ts) || len(envelope.Event.Links) != 1 ||
		envelope.Event.Links[0] != (domain.SharedLink{Domain: "example.com", URL: "https://app.example.com/items/7"}) {
		t.Fatalf("callback=%s", received[0])
	}
}

func TestEventProcessorDeliversOwnedFunctionWithoutManifestSubscription(t *testing.T) {
	ctx := context.Background()
	var received string
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var envelope struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
			Event     struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Error(err)
			return
		}
		if envelope.Type == "url_verification" {
			_, _ = io.WriteString(w, envelope.Challenge)
			return
		}
		if envelope.Event.Type == "function_executed" {
			received = string(body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "workflow-bot"}); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	messages := service.Messages{Store: repository, AppCredentialKey: key, AppHTTPClient: receiver.Client()}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Functions"},"settings":{"function_runtime":"remote","event_subscriptions":{"request_url":"` + receiver.URL + `"}},"functions":{"triage":{"title":"Triage","input_parameters":{"properties":{}},"output_parameters":{"properties":{}}}}}`
	app, _, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.ID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: app.ID, UserID: "UB", Name: "workflow-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-workflow", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: app.ID, BotID: "B1", TokenType: "bot"}); err != nil {
		t.Fatal(err)
	}
	functionEvent, err := events.New("evt_function", "T1", "U1", events.NewPayload("function_executed",
		events.String("target_app_id", string(app.ID)),
		events.String("function_execution_id", "Fx1"),
		events.String("function_id", "Fn1"),
		events.String("workflow_run_id", "Wx1"),
	), time.Unix(1700000001, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	functionEvent.PrivatePayload = `{"app_id":"` + string(app.ID) + `","function_execution_id":"Fx1","function":{"id":"Fn1","callback_id":"triage","title":"Triage","type":"app","input_parameters":[],"output_parameters":[],"app_id":"` + string(app.ID) + `","date_created":1700000000,"date_updated":1700000000,"date_deleted":0},"workflow_execution_id":"Wx1","inputs":{"incident":"INC-1"}}`
	if err := repository.AppendEvent(ctx, functionEvent); err != nil {
		t.Fatal(err)
	}

	processor := EventProcessor{Store: repository, AppCredentialKey: key, Owner: "worker-1", Lease: time.Minute, Client: receiver.Client()}
	for attempt := 0; attempt < 20 && received == ""; attempt++ {
		if _, err := processor.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(received, `"type":"function_executed"`) ||
		!strings.Contains(received, `"function_execution_id":"Fx1"`) ||
		!strings.Contains(received, `"workflow_execution_id":"Wx1"`) ||
		!strings.Contains(received, `"incident":"INC-1"`) {
		t.Fatalf("callback=%s", received)
	}
}
