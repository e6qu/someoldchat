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
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedSocketModeModalApp installs a Socket Mode app with interactivity so a
// test can read the interaction payloads the web client produces.
func seedSocketModeModalApp(t *testing.T, s *memory.Store, features string) {
	t.Helper()
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
	if features != "" {
		features = `"features":` + features + `,`
	}
	manifest := `{"display_information":{"name":"Modal app"},` + features + `"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := s.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Modal app", ClientID: "modal-client",
		SigningSecretHash: domain.HashToken("signing-secret"), SigningSecretCiphertext: signing,
		VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "modal-client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func seedOpenModal(t *testing.T, s *memory.Store, id domain.ViewID, payload string) {
	t.Helper()
	now := time.Now().UTC()
	view := domain.View{ID: id, AppID: "A1", WorkspaceID: "T1", UserID: "U1", Type: "modal", Payload: payload, Hash: "h-" + string(id), RootViewID: id, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateView(context.Background(), view, events.Event{ID: domain.EventID("E-" + string(id)), WorkspaceID: "T1", Topic: "view.opened", Payload: string(id), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func claimInteraction(t *testing.T, s *memory.Store) map[string]any {
	t.Helper()
	interaction, found, err := s.ClaimSocketModeInteraction(context.Background(), "A1", "modal-client", time.Minute)
	if err != nil || !found {
		t.Fatalf("no interaction: found=%v err=%v", found, err)
	}
	if err := s.AckSocketModeInteraction(context.Background(), "A1", interaction.EnvelopeID, "modal-client"); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func viewState(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	view, _ := payload["view"].(map[string]any)
	state, ok := view["state"].(map[string]any)
	if !ok {
		t.Fatalf("interaction view has no state: %v", payload)
	}
	values, ok := state["values"].(map[string]any)
	if !ok {
		t.Fatalf("interaction view.state has no values: %v", state)
	}
	return values
}

// Every view an interaction payload carries includes view.state, as Slack's
// does; Bolt reads view.state.values without checking for it.
func TestViewBlockActionCarriesViewState(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vb", `{"type":"modal","title":{"type":"plain_text","text":"P"},"submit":{"type":"plain_text","text":"Go"},"blocks":[{"type":"input","block_id":"b","label":{"type":"plain_text","text":"L"},"element":{"type":"plain_text_input","action_id":"a"}},{"type":"actions","block_id":"act","elements":[{"type":"button","action_id":"btn","text":{"type":"plain_text","text":"Btn"},"value":"v"}]}]}`)
	response := postForm(t, mux, "/app/view/action?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vb"}, "modal_action": {"0"}, "input_0": {"typed"}}.Encode(), false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	values := viewState(t, claimInteraction(t, s))
	if value := values["b"].(map[string]any)["a"].(map[string]any)["value"]; value != "typed" {
		t.Fatalf("view.state.values = %v", values)
	}
}

// Closing is the user's action: it works after the app is uninstalled, and
// no view_closed is attempted for an app that can no longer receive one.
func TestModalClosesAfterItsAppIsUninstalled(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vc", `{"type":"modal","title":{"type":"plain_text","text":"Info"},"notify_on_close":true,"blocks":[]}`)
	if err := s.UninstallApp(context.Background(), "T1", "A1"); err != nil {
		t.Fatal(err)
	}
	response := postForm(t, mux, "/app/view/close?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vc"}, "clear": {"false"}}.Encode(), false)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("close status=%d body=%s", response.Code, response.Body)
	}
	requireMissing(t, "closed modal", get(t, mux, "/app?channel=Cdev").Body.String(), `role="dialog"`)
}
