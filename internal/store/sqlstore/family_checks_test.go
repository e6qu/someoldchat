package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func openFamilyCheckStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		if err := s.SeedWorkspace(ctx, domain.Workspace{ID: workspace, Name: string(workspace)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []domain.Conversation{{ID: "C1", WorkspaceID: "T1", Name: "one"}, {ID: "C2", WorkspaceID: "T1", Name: "two"}, {ID: "CX", WorkspaceID: "T2", Name: "elsewhere"}} {
		if err := s.SeedConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSQLiteGuestStatusEventsCommitWithTheChange(t *testing.T) {
	storetest.CheckGuestStatusEventsCommitWithTheChange(t, openFamilyCheckStore(t))
}

func TestSQLiteShortTokenRotation(t *testing.T) {
	storetest.CheckShortTokenRotation(t, openFamilyCheckStore(t))
}

func TestSQLiteExistingConversationsExcludedFromAI(t *testing.T) {
	storetest.CheckExistingConversationsExcludedFromAI(t, openFamilyCheckStore(t))
}

// A database from before schema 196 gains the pending-rotation table, so a
// rotation can be begun on an upgraded deployment.
func TestSQLiteMigrationAddsShortTokenRotations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-192.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE short_token_rotations`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 192 WHERE version = ?`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BeginShortTokenRotation(ctx, domain.ShortTokenRotation{TokenHash: "old", NewTokenHash: "new", AppID: "A1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatalf("begin after the migration: %v", err)
	}
}

func TestSQLiteCanvasPresence(t *testing.T) {
	storetest.CheckCanvasPresence(t, openFamilyCheckStore(t))
}
