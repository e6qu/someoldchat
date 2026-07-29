package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestDeveloperAppConsoleCoversCreateEditInstallTokensAndDelete(t *testing.T) {
	repository, mux := browserWorkspace(t, auth.AllScopes())
	page := get(t, mux, "/app/developer/apps")
	if page.Code != http.StatusOK {
		t.Fatalf("apps status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "empty app console", page.Body.String(), "Developer apps", "Create an app", "Issue configuration token", "My app", "chat:write")

	invalid := postForm(t, mux, "/app/developer/apps/create", url.Values{"manifest": {`{"display_information":{}}`}}.Encode(), false)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body)
	}
	requireContains(t, "manifest validation", invalid.Body.String(), "Fix the manifest errors below", "/display_information/name")
	if apps, err := repository.ListDeveloperApps(context.Background(), "T1", "U1"); err != nil || len(apps) != 0 {
		t.Fatalf("invalid manifest created apps=%+v err=%v", apps, err)
	}

	manifest := `{"display_information":{"name":"Alerts","description":"Posts alerts"},"oauth_config":{"redirect_urls":["https://client.example/oauth"],"scopes":{"bot":["chat:write"],"user":["users:read"]}},"settings":{"token_rotation_enabled":true,"socket_mode_enabled":true}}`
	created := postForm(t, mux, "/app/developer/apps/create", url.Values{"manifest": {manifest}}.Encode(), false)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	requireContains(t, "created app", created.Body.String(), "Save these app credentials now", "Client secret", "Signing secret", "Alerts", "Open install flow", "scope=chat%3Awrite", "user_scope=users%3Aread")
	apps, err := repository.ListDeveloperApps(context.Background(), "T1", "U1")
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps=%+v err=%v", apps, err)
	}
	appID := apps[0].ID

	reloaded := get(t, mux, "/app/developer/apps?app="+url.QueryEscape(string(appID)))
	if reloaded.Code != http.StatusOK {
		t.Fatalf("reload status=%d body=%s", reloaded.Code, reloaded.Body)
	}
	requireMissing(t, "reloaded app", reloaded.Body.String(), "Save these app credentials now", "Client secret", "Signing secret")

	updatedManifest := strings.Replace(manifest, `"Alerts"`, `"Incident alerts"`, 1)
	updated := postForm(t, mux, "/app/developer/apps/update", url.Values{"app_id": {string(appID)}, "manifest": {updatedManifest}}.Encode(), false)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	requireContains(t, "updated app", updated.Body.String(), "Manifest saved", "Incident alerts", "Manifest v2")

	appTokenResponse := postForm(t, mux, "/app/developer/apps/app-token", url.Values{"app_id": {string(appID)}}.Encode(), false)
	if appTokenResponse.Code != http.StatusCreated {
		t.Fatalf("app token status=%d body=%s", appTokenResponse.Code, appTokenResponse.Body)
	}
	requireContains(t, "app token", appTokenResponse.Body.String(), "Save this app-level token now", "xapp-", "connections:write")
	appToken := regexp.MustCompile(`xapp-[A-Za-z0-9_-]+`).FindString(appTokenResponse.Body.String())
	if record, err := repository.LookupAppToken(context.Background(), appToken); err != nil || record.AppID != appID || strings.Join(record.Scopes, " ") != "connections:write" {
		t.Fatalf("app token record=%+v err=%v", record, err)
	}

	configuration := postForm(t, mux, "/app/developer/apps/configuration-token", "", false)
	if configuration.Code != http.StatusCreated {
		t.Fatalf("configuration status=%d body=%s", configuration.Code, configuration.Body)
	}
	requireContains(t, "configuration token", configuration.Body.String(), "Save this configuration token now", "xoxe.xoxp-", "xoxe-", "expires in 12 hours")

	deleted := postForm(t, mux, "/app/developer/apps/delete", url.Values{"app_id": {string(appID)}}.Encode(), false)
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/app/developer/apps" {
		t.Fatalf("delete status=%d location=%q body=%s", deleted.Code, deleted.Header().Get("Location"), deleted.Body)
	}
	if _, _, err := repository.GetApp(context.Background(), domain.AppID(appID)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted app error=%v", err)
	}
}
