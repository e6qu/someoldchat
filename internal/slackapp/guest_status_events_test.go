package slackapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestEventProcessorDeliversUserGuestStatusChanged ends a guest tier through
// the service, as admin.users.setRegular does, and requires the app that
// subscribed to user_guest_status_changed with users:read to receive the
// reference's event_callback: the inner event with the user object and
// cache_ts, inside the wrapper with the team, app and authorizations.
func TestEventProcessorDeliversUserGuestStatusChanged(t *testing.T) {
	ctx := context.Background()
	var mutex sync.Mutex
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
		received = append(received, string(body))
		mutex.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	repository := memory.New()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"}),
		repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "directory-bot"}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	if err := repository.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleOwner); err != nil {
		t.Fatal(err)
	}
	guest := domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.test", Name: "guest", UltraRestricted: true}
	joined, err := events.UserChangePayload("user.created", guest, false, false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	created, err := events.New("Ev-guest-created", "T1", "U1", joined, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateUser(ctx, domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.test", Name: "guest"},
		domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "UG", Role: domain.WorkspaceRoleMember, Active: true, UltraRestricted: true}, created); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	messages := service.Messages{Store: repository, AppCredentialKey: key, AppHTTPClient: receiver.Client()}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Directory"},"oauth_config":{"scopes":{"bot":["users:read"]}},"settings":{"event_subscriptions":{"request_url":"` + receiver.URL + `","bot_events":["user_guest_status_changed"]}}}`
	app, _, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.ID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: app.ID, UserID: "UB", Name: "directory-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-directory", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: app.ID, BotID: "B1", TokenType: domain.TokenBot, Scopes: []string{"users:read"}}); err != nil {
		t.Fatal(err)
	}

	if err := messages.SetUserRole(ctx, "T1", "U1", "UG", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	processor := EventProcessor{Store: repository, AppCredentialKey: key, Owner: "worker-1", Lease: time.Minute, Client: receiver.Client()}
	for attempt := 0; attempt < 20; attempt++ {
		if _, err := processor.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		mutex.Lock()
		done := len(received) > 0
		mutex.Unlock()
		if done {
			break
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(received) != 1 {
		t.Fatalf("callbacks=%v, want exactly the user_guest_status_changed callback", received)
	}
	var callback struct {
		Type     string `json:"type"`
		TeamID   string `json:"team_id"`
		APIAppID string `json:"api_app_id"`
		Event    struct {
			Type    string `json:"type"`
			CacheTS int64  `json:"cache_ts"`
			EventTS string `json:"event_ts"`
			User    struct {
				ID                string `json:"id"`
				IsRestricted      bool   `json:"is_restricted"`
				IsUltraRestricted bool   `json:"is_ultra_restricted"`
			} `json:"user"`
		} `json:"event"`
		Authorizations []struct {
			TeamID string `json:"team_id"`
			UserID string `json:"user_id"`
			IsBot  bool   `json:"is_bot"`
		} `json:"authorizations"`
	}
	if err := json.Unmarshal([]byte(received[0]), &callback); err != nil {
		t.Fatal(err)
	}
	if callback.Type != "event_callback" || callback.TeamID != "T1" || callback.APIAppID != string(app.ID) ||
		callback.Event.Type != "user_guest_status_changed" || callback.Event.CacheTS == 0 || callback.Event.EventTS == "" ||
		callback.Event.User.ID != "UG" || callback.Event.User.IsRestricted || callback.Event.User.IsUltraRestricted ||
		len(callback.Authorizations) != 1 || callback.Authorizations[0].UserID != "UB" || !callback.Authorizations[0].IsBot {
		t.Fatalf("callback=%s", received[0])
	}
}
