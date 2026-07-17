package localchat

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

type failingCloser struct {
	err    error
	closed bool
}

func (c *failingCloser) Close() error {
	c.closed = true
	return c.err
}

func TestCloseAfterOpenPreservesInitializationAndCleanupErrors(t *testing.T) {
	initializationErr := errors.New("initialization failed")
	cleanupErr := errors.New("cleanup failed")
	closer := &failingCloser{err: cleanupErr}

	err := closeAfterOpen(closer, initializationErr)
	if !closer.closed {
		t.Fatal("startup resource was not closed")
	}
	if !errors.Is(err, initializationErr) {
		t.Fatalf("error=%v does not preserve initialization error", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v does not preserve cleanup error", err)
	}
}

func TestParseClusterNormalizesAndRejectsDuplicateAddresses(t *testing.T) {
	empty, err := ParseCluster("")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty cluster=%v err=%v, want explicit empty slice", empty, err)
	}
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

func TestOpenMemoryProvidesAuthSeeders(t *testing.T) {
	ctx := context.Background()
	runtime, err := Open(ctx, Config{Backend: BackendMemory})
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
}

func TestOpenBlobStoreRequiresOneExplicitProvider(t *testing.T) {
	if _, err := openBlobStore(context.Background(), Config{BlobDirectory: filepath.Join(t.TempDir(), "objects"), BlobS3Bucket: "bucket", BlobMaxBytes: 1024}); err == nil {
		t.Fatal("filesystem and Amazon Simple Storage Service blob stores were accepted together")
	}
	selected, err := openBlobStore(context.Background(), Config{BlobS3Bucket: "bucket", BlobS3Prefix: "objects", BlobMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := selected.(blob.S3); !ok {
		t.Fatalf("selected blob store=%T, want blob.S3", selected)
	}
}
