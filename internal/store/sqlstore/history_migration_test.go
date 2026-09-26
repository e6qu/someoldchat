package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Version 176 made the broadcast flag a column. A reply broadcast before the
// upgrade carried the flag only in stream_state, so the migration has to read
// it from there: without the backfill, every reply ever sent to the channel
// would vanish from conversations.history the moment the history read began
// filtering thread replies.
func TestVersion176MigrationBackfillsReplyBroadcastFromStreamState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-176.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	root := domain.Message{ID: "M-root", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "root", CreatedAt: time.Unix(100, 0).UTC()}
	rootTS := domain.NewMessageTimestamp(root.CreatedAt)
	quiet := domain.Message{ID: "M-quiet", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "quiet", ThreadTimestamp: rootTS, CreatedAt: time.Unix(101, 0).UTC()}
	loud := domain.Message{ID: "M-loud", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "loud", ThreadTimestamp: rootTS, CreatedAt: time.Unix(102, 0).UTC(),
		StreamState: `{"active":false,"reply_broadcast":true}`}
	for _, message := range []domain.Message{root, quiet, loud} {
		if err := s.CreateMessage(ctx, message, events.Event{ID: domain.EventID("E-" + string(message.ID)), WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: message.CreatedAt}, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Rewind to the schema before the column existed.
	for _, statement := range []string{
		`ALTER TABLE messages DROP COLUMN reply_broadcast`,
		`ALTER TABLE ephemeral_messages DROP COLUMN thread_ts`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 175 WHERE version = ?`, schemaVersion); err != nil {
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
	columns, err := s.tableColumns(ctx, s.db, "ephemeral_messages")
	if err != nil || !columns["thread_ts"] {
		t.Fatalf("ephemeral_messages.thread_ts was not migrated: %v %v", columns, err)
	}
	page, err := s.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}, RootsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != root.ID || page.Messages[1].ID != loud.ID || !page.Messages[1].ReplyBroadcast {
		t.Fatalf("history after the migration = %+v, want the root and the broadcast reply", page.Messages)
	}
}
