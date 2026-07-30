package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestAppsDatastoreMethodsMatchCurrentSlackRequestAndResponseShapes(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "hosted-bot"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Hosted"},"oauth_config":{"scopes":{"bot":["datastore:read","datastore:write"]}},"settings":{"is_hosted":true,"function_runtime":"slack"},"datastores":{"incidents":{"primary_key":"id","attributes":{"id":{"type":"string"},"title":{"type":"string"},"priority":{"type":"integer"}}}}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "UBOT", Name: "Hosted", ClientID: "client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "UBOT", CreatedAt: now,
	}, domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("xoxb-hosted", auth.Principal{
		WorkspaceID: "T1", UserID: "UBOT", AppID: "A1", TokenType: "bot",
		Scopes: map[auth.Scope]struct{}{auth.ScopeDatastoreRead: {}, auth.ScopeDatastoreWrite: {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)

	put := callDatastoreAPI(t, mux, "/api/apps.datastore.put", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "item": map[string]any{"id": "INC-1", "title": "Investigate", "priority": 1},
	})
	item, _ := put["item"].(map[string]any)
	if put["ok"] != true || put["datastore"] != "incidents" || item["id"] != "INC-1" {
		t.Fatalf("put=%v", put)
	}
	bulkPut := callDatastoreAPI(t, mux, "/api/apps.datastore.bulkPut", "xoxb-hosted", map[string]any{
		"datastore": "incidents",
		"items": []map[string]any{
			{"id": "INC-2", "title": "Mitigate"},
			{"id": "INC-3", "title": "Recover", "priority": 3},
		},
	})
	if failed, ok := bulkPut["failed_items"].([]any); bulkPut["ok"] != true || !ok || len(failed) != 0 {
		t.Fatalf("bulk put=%v", bulkPut)
	}
	update := callDatastoreAPI(t, mux, "/api/apps.datastore.update", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "item": map[string]any{"id": "INC-1", "priority": 2},
	})
	updatedItem, _ := update["item"].(map[string]any)
	if updatedItem["title"] != "Investigate" || updatedItem["priority"] != float64(2) {
		t.Fatalf("update=%v", update)
	}
	bulkGet := callDatastoreAPI(t, mux, "/api/apps.datastore.bulkGet", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "ids": []string{"INC-3", "missing", "INC-1"},
	})
	items, _ := bulkGet["items"].([]any)
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if bulkGet["ok"] != true || len(items) != 2 || first["id"] != "INC-3" || second["id"] != "INC-1" {
		t.Fatalf("bulk get=%v", bulkGet)
	}
	query := callDatastoreAPI(t, mux, "/api/apps.datastore.query", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "limit": 1,
		"expression":            "#priority >= :minimum",
		"expression_attributes": map[string]any{"#priority": "priority"},
		"expression_values":     map[string]any{":minimum": 3},
	})
	queryItems, _ := query["items"].([]any)
	queryMetadata, _ := query["response_metadata"].(map[string]any)
	if query["ok"] != true || len(queryItems) != 0 || queryMetadata["next_cursor"] == "" {
		t.Fatalf("first query page=%v", query)
	}
	query = callDatastoreAPI(t, mux, "/api/apps.datastore.query", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "limit": 1, "cursor": queryMetadata["next_cursor"],
		"expression":            "#priority >= :minimum",
		"expression_attributes": map[string]any{"#priority": "priority"},
		"expression_values":     map[string]any{":minimum": 3},
	})
	queryMetadata, _ = query["response_metadata"].(map[string]any)
	if queryItems, _ = query["items"].([]any); len(queryItems) != 0 || queryMetadata["next_cursor"] == "" {
		t.Fatalf("second query page=%v", query)
	}
	query = callDatastoreAPI(t, mux, "/api/apps.datastore.query", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "limit": 1, "cursor": queryMetadata["next_cursor"],
		"expression":            "#priority >= :minimum",
		"expression_attributes": map[string]any{"#priority": "priority"},
		"expression_values":     map[string]any{":minimum": 3},
	})
	queryItems, _ = query["items"].([]any)
	if len(queryItems) != 1 {
		t.Fatalf("third query page=%v", query)
	}
	matched, _ := queryItems[0].(map[string]any)
	if matched["id"] != "INC-3" {
		t.Fatalf("third query page=%v", query)
	}
	count := callDatastoreAPI(t, mux, "/api/apps.datastore.count", "xoxb-hosted", map[string]any{
		"datastore":             "incidents",
		"expression":            "contains (#title, :term)",
		"expression_attributes": map[string]any{"#title": "title"},
		"expression_values":     map[string]any{":term": "i"},
	})
	if count["ok"] != true || count["count"] != float64(2) {
		t.Fatalf("count=%v", count)
	}
	invalidQuery := callDatastoreAPI(t, mux, "/api/apps.datastore.query", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "expression": "#id = :id",
		"expression_attributes": map[string]any{"#id": "id"},
		"expression_values":     map[string]any{":id": "INC-1"},
	})
	if invalidQuery["error"] != "invalid_arguments" {
		t.Fatalf("primary-key query=%v", invalidQuery)
	}
	missing := callDatastoreAPI(t, mux, "/api/apps.datastore.get", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "id": "missing",
	})
	missingItem, _ := missing["item"].(map[string]any)
	if missing["ok"] != true || len(missingItem) != 0 {
		t.Fatalf("missing get=%v", missing)
	}
	bulkDelete := callDatastoreAPI(t, mux, "/api/apps.datastore.bulkDelete", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "ids": []string{"INC-2", "INC-3"},
	})
	if failed, ok := bulkDelete["failed_items"].([]any); bulkDelete["ok"] != true || !ok || len(failed) != 0 {
		t.Fatalf("bulk delete=%v", bulkDelete)
	}
	deleted := callDatastoreAPI(t, mux, "/api/apps.datastore.delete", "xoxb-hosted", map[string]any{
		"datastore": "incidents", "id": "INC-1",
	})
	if deleted["ok"] != true {
		t.Fatalf("delete=%v", deleted)
	}
	unknown := callDatastoreAPI(t, mux, "/api/apps.datastore.get", "xoxb-hosted", map[string]any{
		"datastore": "missing", "id": "INC-1",
	})
	problems, _ := unknown["errors"].([]any)
	problem, _ := problems[0].(map[string]any)
	if unknown["error"] != "datastore_error" || problem["code"] != "datastore_config_not_found" || problem["pointer"] != "/datastores" {
		t.Fatalf("unknown datastore=%v", unknown)
	}
	mismatched := callDatastoreAPI(t, mux, "/api/apps.datastore.get", "xoxb-hosted", map[string]any{
		"app_id": "A2", "datastore": "incidents", "id": "INC-1",
	})
	if mismatched["error"] != "invalid_app_id" {
		t.Fatalf("mismatched app=%v", mismatched)
	}
}

func TestAppsDatastoreUserTokensRequireAnInstalledAppID(t *testing.T) {
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("xoxp-user", auth.Principal{
		WorkspaceID: "T1", UserID: "U1", TokenType: "user",
		Scopes: map[auth.Scope]struct{}{auth.ScopeDatastoreRead: {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	response := callDatastoreAPI(t, mux, "/api/apps.datastore.get", "xoxp-user", map[string]any{
		"app_id": "A1", "datastore": "incidents", "id": "INC-1",
	})
	if response["error"] != "invalid_app_id" {
		t.Fatalf("response=%v", response)
	}
	missingAppID := callDatastoreAPI(t, mux, "/api/apps.datastore.get", "xoxp-user", map[string]any{
		"datastore": "incidents", "id": "INC-1",
	})
	if missingAppID["error"] != "invalid_arguments" {
		t.Fatalf("missing app id response=%v", missingAppID)
	}
}

func callDatastoreAPI(t *testing.T, handler http.Handler, path, token string, payload map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s decode: %v body=%s", path, err, response.Body.String())
	}
	return body
}
