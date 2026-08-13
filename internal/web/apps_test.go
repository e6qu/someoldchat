package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
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
	retainedMethod := postForm(t, mux, "/app/developer/apps?app="+url.QueryEscape(string(appID)), "", false)
	if retainedMethod.Code != http.StatusSeeOther || retainedMethod.Header().Get("Location") != "/app/developer/apps?app="+url.QueryEscape(string(appID)) {
		t.Fatalf("retained-method reload status=%d location=%q body=%s", retainedMethod.Code, retainedMethod.Header().Get("Location"), retainedMethod.Body)
	}

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

func TestDeveloperDatastoreConsoleUsesHostedAppPersistenceAndSlackQuerySemantics(t *testing.T) {
	repository, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Hosted incidents"},"oauth_config":{"scopes":{"bot":["datastore:read","datastore:write"]}},"settings":{"is_hosted":true,"function_runtime":"slack"},"datastores":{"incidents":{"primary_key":"id","time_to_live_attribute":"expires_at","attributes":{"id":{"type":"string"},"title":{"type":"string"},"priority":{"type":"integer"},"expires_at":{"type":"slack#/types/timestamp"}}}}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "Ahosted", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Hosted incidents", ClientID: "hosted-client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "Ahosted", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "hosted-client", SecretHash: "client-hash", AppID: "Ahosted"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "Ahosted", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	appPage := get(t, mux, "/app/developer/apps?app=Ahosted")
	if appPage.Code != http.StatusOK {
		t.Fatalf("app status=%d body=%s", appPage.Code, appPage.Body)
	}
	requireContains(t, "hosted app link", appPage.Body.String(), "Manage hosted datastores", "datastore=incidents")

	page := get(t, mux, "/app/developer/apps/datastore?app=Ahosted&datastore=incidents")
	if page.Code != http.StatusOK {
		t.Fatalf("datastore status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "empty datastore", page.Body.String(),
		"Hosted datastore", "Manifest schema", "primary key", "time to live", "No items matched this page", "0 matching items",
	)

	put := postForm(t, mux, "/app/developer/apps/datastore/put", url.Values{
		"app_id": {"Ahosted"}, "datastore": {"incidents"}, "mode": {"replace"},
		"item": {`{"title":"Investigate latency","id":"INC-1","priority":1}`},
	}.Encode(), false)
	if put.Code != http.StatusSeeOther {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body)
	}
	persisted := get(t, mux, put.Header().Get("Location"))
	if persisted.Code != http.StatusOK {
		t.Fatalf("persisted status=%d body=%s", persisted.Code, persisted.Body)
	}
	requireContains(t, "persisted item", persisted.Body.String(), "Item persisted", "INC-1", "Investigate latency", "1 matching item")

	filtered := get(t, mux, "/app/developer/apps/datastore?"+url.Values{
		"app": {"Ahosted"}, "datastore": {"incidents"},
		"expression": {"contains (#title, :term)"},
		"attributes": {`{"#title":"title"}`}, "values": {`{":term":"latency"}`}, "limit": {"10"},
	}.Encode())
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered status=%d body=%s", filtered.Code, filtered.Body)
	}
	requireContains(t, "filtered item", filtered.Body.String(), "Investigate latency", "1 matching item", "contains (#title, :term)")

	merged := postForm(t, mux, "/app/developer/apps/datastore/put", url.Values{
		"app_id": {"Ahosted"}, "datastore": {"incidents"}, "mode": {"merge"},
		"item": {`{"id":"INC-1","priority":2}`},
	}.Encode(), false)
	if merged.Code != http.StatusSeeOther {
		t.Fatalf("merge status=%d body=%s", merged.Code, merged.Body)
	}
	afterMerge := get(t, mux, merged.Header().Get("Location"))
	requireContains(t, "merged item", afterMerge.Body.String(), "Investigate latency", `&#34;priority&#34;:2`)

	invalid := postForm(t, mux, "/app/developer/apps/datastore/put", url.Values{
		"app_id": {"Ahosted"}, "datastore": {"incidents"}, "mode": {"replace"},
		"item": {`{"id":"INC-2","priority":"urgent"}`},
	}.Encode(), false)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body)
	}
	requireContains(t, "invalid schema", invalid.Body.String(), "does not match the datastore schema", `priority&#34;:&#34;urgent`)

	deleted := postForm(t, mux, "/app/developer/apps/datastore/delete", url.Values{
		"app_id": {"Ahosted"}, "datastore": {"incidents"}, "id": {"INC-1"},
	}.Encode(), false)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body)
	}
	empty := get(t, mux, deleted.Header().Get("Location"))
	requireContains(t, "deleted item", empty.Body.String(), "Item deleted", "No items matched this page", "0 matching items")
}

func TestDeveloperAppDeliveryHealthShowsDurableRetryWithoutExposingPayload(t *testing.T) {
	repository, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Event receiver"},"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":["message.channels"]}}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "Aevents", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Event receiver", ClientID: "events-client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "Aevents", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "events-client", SecretHash: "client-hash", AppID: "Aevents"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "Aevents", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	const privatePayload = `{"secret":"must-not-render"}`
	if err := repository.AppendEvent(ctx, events.Event{
		ID: "Edelivery", WorkspaceID: "T1", ActorID: "U1", Topic: "message.created", Payload: privatePayload, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, _, _, found, err := repository.ClaimAppEvent(ctx, "Aevents", "socket", "test-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	if err := repository.ReleaseAppEvent(ctx, "Aevents", "socket", "test-worker", claimed.Sequence, "connection_closed", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	appPage := get(t, mux, "/app/developer/apps?app=Aevents")
	requireContains(t, "delivery link", appPage.Body.String(), "View event delivery health", "/app/developer/apps/delivery?app=Aevents")
	page := get(t, mux, "/app/developer/apps/delivery?app=Aevents")
	if page.Code != http.StatusOK {
		t.Fatalf("delivery status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "durable retry", page.Body.String(),
		"Event delivery health", "Retry scheduled", "Socket Mode", "connection_closed",
		"Next journal record awaiting evaluation", "message.created",
	)
	// The failed release is now shown as a retained delivery attempt with metrics.
	requireContains(t, "delivery attempt history", page.Body.String(),
		"Recent delivery attempts", "Success rate", "0%", "attempt 1", "failed",
	)
	requireMissing(t, "delivery payload redaction", page.Body.String(), privatePayload, "must-not-render", "test-worker")
}
