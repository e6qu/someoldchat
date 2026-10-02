package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A workspace that had owners before version 198 has no primary owner on
// record. The upgrade names one, the first active owner by member ID, so
// is_primary_owner is true for exactly one member from the first read on, and
// a deactivated owner is passed over.
func TestVersion198MigrationNamesOnePrimaryOwner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-198.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2", "U3"} {
		if err := s.SeedUser(ctx, domain.User{ID: user, WorkspaceID: "T1", Name: string(user)}); err != nil {
			t.Fatal(err)
		}
	}
	// Rewind to what version 197 stored: three owners, the first of them
	// deactivated, and no primary owner column at all.
	for _, statement := range []string{
		`DROP INDEX workspace_members_primary_owner`,
		`ALTER TABLE workspace_members DROP COLUMN primary_owner`,
		`UPDATE workspace_members SET role = 'owner'`,
		`UPDATE workspace_members SET active = 0 WHERE user_id = 'U1'`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 197 WHERE version = ?`, schemaVersion); err != nil {
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
	for user, want := range map[domain.UserID]bool{"U1": false, "U2": true, "U3": false} {
		membership, err := s.GetWorkspaceMembership(ctx, "T1", user)
		if err != nil || membership.PrimaryOwner != want {
			t.Fatalf("%s primary owner=%v err=%v, want %v", user, membership.PrimaryOwner, err, want)
		}
	}
}
