package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// A member already talking in a huddle when the store is upgraded to version
// 216 must read back in that huddle, and a member whose huddle has ended — or
// who was never in one — must not: the column is new, so the upgrade derives
// it from the running huddles rather than reporting everybody out.
func TestVersion216MigrationCarriesRunningHuddlesIntoTheProfile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-216.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{{ID: "U1", WorkspaceID: "T1", Name: "alice"}, {ID: "U2", WorkspaceID: "T1", Name: "bob"}, {ID: "U3", WorkspaceID: "T1", Name: "carol"}} {
		if err := s.SeedUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, conversation := range []domain.ConversationID{"C1", "C2"} {
		if err := s.SeedConversation(ctx, domain.Conversation{ID: conversation, WorkspaceID: "T1", Name: string(conversation)}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	event := func(id string) events.Event {
		return events.Event{ID: domain.EventID(id), WorkspaceID: "T1", Topic: "huddle.joined", Payload: id, CreatedAt: at}
	}
	start := func(call domain.CallID, conversation domain.ConversationID, actor domain.UserID) {
		t.Helper()
		if _, _, err := s.StartHuddle(ctx, domain.Call{ID: call, WorkspaceID: "T1", Kind: domain.CallKindHuddle, ConversationID: conversation, CreatedBy: actor, StartedAt: at},
			event(string(call)+"-started-"+string(actor)), event(string(call)+"-joined-"+string(actor)),
			domain.Message{ID: domain.MessageID("M" + string(call) + string(actor)), WorkspaceID: "T1", Conversation: conversation, AuthorID: actor, Subtype: domain.MessageSubtypeHuddleThread, CreatedAt: at, Attachments: "[]"}); err != nil {
			t.Fatal(err)
		}
	}
	start("R1", "C1", "U1")
	start("R2", "C2", "U2")
	if err := s.EndCall(ctx, "T1", "R2", 5, event("R2-ended")); err != nil {
		t.Fatal(err)
	}
	// Rewind to the schema before version 216.
	for _, statement := range []string{
		`DROP INDEX call_participants_user`,
		`ALTER TABLE users DROP COLUMN huddle_call_id`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 215 WHERE version = ?`, schemaVersion); err != nil {
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
	for user, want := range map[domain.UserID]domain.CallID{"U1": "R1", "U2": "", "U3": ""} {
		value, err := s.GetUser(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if value.HuddleCallID != want {
			t.Fatalf("%s after upgrade is in huddle %q, want %q", user, value.HuddleCallID, want)
		}
	}
}
