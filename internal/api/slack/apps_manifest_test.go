package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestAppsManifestLifecycleMatchesCurrentSlackResponseShapes(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("unused-user-token", auth.Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)

	invalid := callManifestAPI(t, mux, "/api/apps.manifest.validate", configuration.Token, map[string]any{
		"manifest": `{"display_information":{}}`,
	})
	if invalid["ok"] != false || invalid["error"] != "invalid_manifest" {
		t.Fatalf("invalid validation response=%+v", invalid)
	}
	if problems, ok := invalid["errors"].([]any); !ok || len(problems) == 0 {
		t.Fatalf("invalid validation errors=%T %+v", invalid["errors"], invalid["errors"])
	}

	manifest := `{"display_information":{"name":"Example"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true}}`
	created := callManifestAPI(t, mux, "/api/apps.manifest.create", configuration.Token, map[string]any{"manifest": manifest})
	appID, _ := created["app_id"].(string)
	credentials, _ := created["credentials"].(map[string]any)
	if created["ok"] != true || appID == "" || credentials["client_secret"] == "" || credentials["signing_secret"] == "" {
		t.Fatalf("create response=%+v", created)
	}
	if authorizeURL, _ := created["oauth_authorize_url"].(string); !strings.Contains(authorizeURL, "/oauth/v2/authorize?") || !strings.Contains(authorizeURL, "scope=chat%3Awrite") {
		t.Fatalf("oauth_authorize_url=%q", authorizeURL)
	}

	exported := callManifestAPI(t, mux, "/api/apps.manifest.export", configuration.Token, map[string]any{"app_id": appID})
	document, _ := exported["manifest"].(map[string]any)
	display, _ := document["display_information"].(map[string]any)
	if exported["ok"] != true || display["name"] != "Example" {
		t.Fatalf("export response=%+v", exported)
	}

	updatedManifest := `{"display_information":{"name":"Updated"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write","commands"]}},"settings":{"socket_mode_enabled":true}}`
	updated := callManifestAPI(t, mux, "/api/apps.manifest.update", configuration.Token, map[string]any{"app_id": appID, "manifest": updatedManifest})
	if updated["ok"] != true || updated["app_id"] != appID || updated["permissions_updated"] != true {
		t.Fatalf("update response=%+v", updated)
	}

	rotated := callManifestAPI(t, mux, "/api/tooling.tokens.rotate", "", map[string]any{"refresh_token": configuration.RefreshToken})
	nextToken, _ := rotated["token"].(string)
	if rotated["ok"] != true || nextToken == "" || rotated["team_id"] != "T1" || rotated["user_id"] != "U1" || rotated["iat"] == nil || rotated["exp"] == nil {
		t.Fatalf("rotate response=%+v", rotated)
	}
	replayed := callManifestAPI(t, mux, "/api/tooling.tokens.rotate", "", map[string]any{"refresh_token": configuration.RefreshToken})
	if replayed["ok"] != false || replayed["error"] != "invalid_refresh_token" {
		t.Fatalf("refresh replay response=%+v", replayed)
	}
	oldAccess := callManifestAPI(t, mux, "/api/apps.manifest.export", configuration.Token, map[string]any{"app_id": appID})
	if oldAccess["ok"] != false || oldAccess["error"] != "invalid_auth" {
		t.Fatalf("old access response=%+v", oldAccess)
	}
	deleted := callManifestAPI(t, mux, "/api/apps.manifest.delete", nextToken, map[string]any{"app_id": appID})
	if deleted["ok"] != true {
		t.Fatalf("delete response=%+v", deleted)
	}
}

func callManifestAPI(t *testing.T, handler http.Handler, path, token string, payload map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://chat.example.test"+path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("%s decode: %v body=%s", path, err, response.Body.String())
	}
	return body
}
