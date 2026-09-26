package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// A direct conversation opened before version 182 carries a NUL-joined key.
// After the upgrade the same member set must still find it, or reopening the
// DM would create a second conversation beside the first.
func TestVersion182MigrationRewritesNULJoinedDirectKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-182.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{{ID: "U1", WorkspaceID: "T1", Name: "alice"}, {ID: "U2", WorkspaceID: "T1", Name: "bob"}} {
		if err := s.SeedUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	members := []domain.UserID{"U2", "U1"}
	direct := domain.Conversation{ID: "D1", WorkspaceID: "T1", Name: "direct", Kind: domain.ConversationTypeIM}
	if err := s.CreateDirectConversation(ctx, direct, members, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "conversation.direct_created", Payload: "D1"}); err != nil {
		t.Fatal(err)
	}
	// Rewind to the key the store wrote before version 182.
	if _, err := s.db.ExecContext(ctx, `UPDATE conversations SET direct_key = ? WHERE id = ?`, "T1\x00U1\x00U2", "D1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 181 WHERE version = ?`, schemaVersion); err != nil {
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
	found, err := s.FindDirectConversation(ctx, "T1", members)
	if err != nil || found.ID != "D1" {
		t.Fatalf("direct conversation after upgrade = %+v, %v; want D1", found, err)
	}
}
