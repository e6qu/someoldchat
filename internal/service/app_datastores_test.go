package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestHostedAppDatastoreCRUDValidatesManifestSchema(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Hosted"},"oauth_config":{"scopes":{"bot":["datastore:read","datastore:write"]}},"settings":{"is_hosted":true,"function_runtime":"slack"},"datastores":{"incidents":{"primary_key":"id","time_to_live_attribute":"expires_at","attributes":{"id":{"type":"string"},"title":{"type":"string"},"priority":{"type":"integer"},"expires_at":{"type":"slack#/types/timestamp"}}}}}`
	app := domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Hosted", ClientID: "client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateApp(ctx, app, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository}

	put, err := messages.PutAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{
		`{"title":"Investigate","id":"INC-1","priority":1}`,
		`{"id":"INC-2","title":"Mitigate"}`,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if put[0] != `{"id":"INC-1","priority":1,"title":"Investigate"}` {
		t.Fatalf("put item is not canonical: %s", put[0])
	}
	updated, err := messages.PutAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{
		`{"id":"INC-1","priority":2}`,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	if err := json.Unmarshal([]byte(updated[0]), &merged); err != nil || merged["title"] != "Investigate" || merged["priority"] != float64(2) {
		t.Fatalf("merged=%v err=%v", merged, err)
	}
	got, err := messages.GetAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"missing", "INC-2", "INC-1"})
	if err != nil || len(got) != 2 || got[0] != put[1] || got[1] != updated[0] {
		t.Fatalf("get=%v err=%v", got, err)
	}
	if err := messages.DeleteAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"INC-2"}); err != nil {
		t.Fatal(err)
	}
	got, err = messages.GetAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"INC-2"})
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted item=%v err=%v", got, err)
	}

	for name, raw := range map[string]string{
		"unknown attribute": `{"id":"INC-3","other":"no"}`,
		"wrong type":        `{"id":"INC-3","priority":"high"}`,
		"missing key":       `{"title":"No id"}`,
		"trailing JSON":     `{"id":"INC-3"} true`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := messages.PutAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{raw}, false); !errors.Is(err, ErrInvalidDatastoreItem) {
				t.Fatalf("error=%v, want %v", err, ErrInvalidDatastoreItem)
			}
		})
	}
	if _, err := messages.GetAppDatastoreItems(ctx, "T1", "U1", "A1", "missing", []string{"INC-1"}); !errors.Is(err, ErrAppDatastoreNotFound) {
		t.Fatalf("missing datastore error=%v, want %v", err, ErrAppDatastoreNotFound)
	}
	if _, err := messages.GetAppDatastoreItems(ctx, "T1", "U1", "missing-app", "incidents", []string{"INC-1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing app error=%v, want %v", err, store.ErrNotFound)
	}
}

func TestAppDatastoreRejectsNonHostedApp(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Remote", ClientID: "client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"Remote"}}`, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository}
	if _, err := messages.GetAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"INC-1"}); !errors.Is(err, ErrAppNotHosted) {
		t.Fatalf("error=%v, want %v", err, ErrAppNotHosted)
	}
}
