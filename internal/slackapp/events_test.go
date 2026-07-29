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
	if count, err := processor.RunOnce(ctx); err == nil || count != 0 {
		t.Fatalf("first delivery count=%d err=%v, want a released retry", count, err)
	}
	if count, err := processor.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("retried delivery count=%d err=%v", count, err)
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
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", IsPrivate: true})
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
