package localchat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestParseClusterNormalizesAndRejectsDuplicateAddresses(t *testing.T) {
	cluster, err := ParseCluster(" node-a:1, node-b:2 ")
	if err != nil || len(cluster) != 2 || cluster[0] != "node-a:1" || cluster[1] != "node-b:2" {
		t.Fatalf("cluster=%v err=%v", cluster, err)
	}
	if _, err := ParseCluster("node-a:1,node-a:1"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate cluster error=%v", err)
	}
}

func TestOpenSQLiteProvidesDurableAuthStores(t *testing.T) {
	ctx := context.Background()
	runtime, err := Open(ctx, Config{Backend: BackendSQLite, DSN: filepath.Join(t.TempDir(), "chat.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Closer.Close()

	if err := runtime.TokenSeeder.SeedToken(ctx, "api-token", domain.TokenRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SessionSeeder.SeedSession(ctx, "session-token", domain.SessionRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.TokenStore.LookupToken(ctx, "api-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SessionStore.LookupSession(ctx, "session-token"); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.BlobStore.(blob.Disabled); !ok {
		t.Fatalf("default blob store has type %T, want blob.Disabled", runtime.BlobStore)
	}
}
