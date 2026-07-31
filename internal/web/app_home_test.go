package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

func TestInstalledAppDirectoryRendersPublishedHomeAndDispatchesActions(t *testing.T) {
	repository, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Now().UTC()
	key := []byte(strings.Repeat("k", 32))
	signing, err := secretbox.Seal(key, "app:A1:signing-secret", "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	verification, err := secretbox.Seal(key, "app:A1:verification-token", "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Release assistant","description":"Coordinates production releases"},"features":{"app_home":{"home_tab_enabled":true,"messages_tab_enabled":true},"bot_user":{"display_name":"Release bot"}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true},"event_subscriptions":{"bot_events":["app_home_opened"]}}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Release assistant", ClientID: "home-client",
		SigningSecretHash: domain.HashToken("signing-secret"), SigningSecretCiphertext: signing,
		VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "home-client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "release-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", AppID: "A1", WorkspaceID: "T1", UserID: "UBOT", Name: "Release bot", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-home", domain.TokenRecord{WorkspaceID: "T1", UserID: "UBOT", AppID: "A1", BotID: "B1", TokenType: "bot"}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository, AppCredentialKey: key}
	published, err := messages.PublishView(ctx, "T1", "U1", "A1", "U1", `{"type":"home","blocks":[{"type":"header","block_id":"heading","text":{"type":"plain_text","text":"Production releases"}},{"type":"section","block_id":"summary","text":{"type":"mrkdwn","text":"Choose the environment to inspect."}},{"type":"card","block_id":"health","title":{"type":"plain_text","text":"Release health"},"body":{"type":"mrkdwn","text":"*Healthy*"}},{"type":"data_table","block_id":"deployments","caption":"Recent deployments","rows":[[{"type":"raw_text","text":"Service"},{"type":"raw_text","text":"Version"}],[{"type":"raw_text","text":"API"},{"type":"raw_text","text":"42"}]]},{"type":"actions","block_id":"environment","elements":[{"type":"static_select","action_id":"select_environment","placeholder":{"type":"plain_text","text":"Environment"},"options":[{"text":{"type":"plain_text","text":"Production"},"value":"production"},{"text":{"type":"plain_text","text":"Staging"},"value":"staging"}]}]}]}`, "")
	if err != nil {
		t.Fatal(err)
	}

	directory := get(t, mux, "/app/apps?channel=Cdev")
	if directory.Code != http.StatusOK {
		t.Fatalf("directory status=%d body=%s", directory.Code, directory.Body)
	}
	requireContains(t, "installed app directory", directory.Body.String(), "Release assistant", "Coordinates production releases", "/app/developer/apps")

	home := get(t, mux, "/app/apps/A1?channel=Cdev")
	if home.Code != http.StatusOK {
		t.Fatalf("home status=%d body=%s", home.Code, home.Body)
	}
	requireContains(t, "app home", home.Body.String(),
		"Production releases", "Choose the environment to inspect.", "Release health", "<strong>Healthy</strong>",
		"<caption>Recent deployments</caption>", `<th scope="col">Service</th>`, "Production", "Staging",
		`name="home_action" value="0"`, `name="view_id" value="`+string(published.ID)+`"`,
		`action="/app/apps/A1/action?channel=Cdev"`, "Messages", "About",
	)
	record, _, _, found, err := messages.ClaimAppEvent(ctx, "A1", "socket", "home-events", time.Minute)
	if err != nil || !found || record.Event.Topic != "app.home_opened" {
		t.Fatalf("app_home_opened record=%+v found=%v err=%v", record, found, err)
	}
	envelopes, err := events.SocketModeEnvelopes(record, "A1")
	if err != nil || len(envelopes) != 1 {
		t.Fatalf("app_home_opened envelopes=%d err=%v", len(envelopes), err)
	}
	encodedEnvelope, _ := json.Marshal(envelopes[0].Frame)
	var appHomeEnvelope struct {
		Payload struct {
			Event struct {
				Type    string `json:"type"`
				User    string `json:"user"`
				Channel string `json:"channel"`
				Tab     string `json:"tab"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(encodedEnvelope, &appHomeEnvelope) != nil ||
		appHomeEnvelope.Payload.Event.Type != "app_home_opened" ||
		appHomeEnvelope.Payload.Event.User != "U1" ||
		appHomeEnvelope.Payload.Event.Tab != "home" ||
		appHomeEnvelope.Payload.Event.Channel == "" {
		t.Fatalf("app_home_opened envelope=%s", encodedEnvelope)
	}
	if err := messages.AckAppEvent(ctx, "A1", "socket", "home-events", record.Sequence); err != nil {
		t.Fatal(err)
	}

	action := postForm(t, mux, "/app/apps/A1/action?channel=Cdev", url.Values{
		"_csrf":       {auth.CSRFToken("session")},
		"view_id":     {string(published.ID)},
		"home_action": {"0"},
		"action_0":    {"production"},
	}.Encode(), false)
	if action.Code != http.StatusSeeOther {
		t.Fatalf("action status=%d body=%s", action.Code, action.Body)
	}
	interaction, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket", time.Minute)
	if err != nil || !found {
		t.Fatalf("interaction found=%v err=%v", found, err)
	}
	requireContains(t, "home action payload", interaction.Payload,
		`"type":"block_actions"`, `"container":{"type":"view","view_id":"`+string(published.ID)+`"}`,
		`"selected_option":{"text":{"emoji":true,"text":"Production","type":"plain_text"},"value":"production"}`,
		`"state":{"values":{"environment":{"select_environment":`,
	)
}
