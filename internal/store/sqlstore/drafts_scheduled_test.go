package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func TestSQLiteDraftsScheduledHistoryAndSentMessagesSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "drafts-and-sent.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}))
	require(s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}))
	require(s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}))
	require(s.SeedConversationMember(ctx, "C1", "U1"))

	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	draft := domain.Draft{WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", Text: "unfinished", UpdatedAt: now}
	if _, err := s.UpsertDraft(ctx, draft, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "draft.saved"}); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "sent", CreatedAt: now.Add(time.Minute)}
	require(s.CreateMessage(ctx, message, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "message.created"}, ""))
	scheduled := domain.ScheduledMessage{
		WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", CredentialHash: "first-party",
		Text: "before", PostAt: now.Add(2 * time.Hour), CreatedAt: now,
	}
	require(s.CreateScheduledMessageWithinLimit(ctx, scheduled, 5*time.Minute, 30, events.Event{ID: "E3", WorkspaceID: "T1", Topic: "message.scheduled"}))
	updated, err := s.UpdateScheduledMessageWithinLimit(ctx, domain.ScheduledMessageUpdate{
		WorkspaceID: "T1", ID: "Q1", Channel: "C1", CredentialHash: "first-party",
		Text: "after", PostAt: now.Add(3 * time.Hour),
	}, 5*time.Minute, 30, events.Event{ID: "E4", WorkspaceID: "T1", Topic: "message.schedule_updated"})
	if err != nil || updated.Text != "after" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	failedClaim, err := s.ClaimScheduledMessageForCredential(ctx, "T1", "first-party", "Q1", "worker", time.Minute)
	if err != nil || failedClaim.ID != "Q1" {
		t.Fatalf("worker claim=%+v err=%v", failedClaim, err)
	}
	require(s.MarkScheduledMessageFailed(ctx, "worker", "Q1", "not_in_channel", now.Add(time.Minute), events.Event{ID: "E5", WorkspaceID: "T1", Topic: "message.schedule_failed"}))
	claimed, err := s.ClaimScheduledMessageForCredential(ctx, "T1", "first-party", "Q1", "send-now", time.Minute)
	if err != nil || claimed.ID != "Q1" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	require(s.MarkScheduledMessageDelivered(ctx, "send-now", "Q1"))
	require(s.Close())

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	storedDraft, err := s.GetDraft(ctx, "T1", "U1", "C1", "")
	if err != nil || storedDraft.Text != "unfinished" {
		t.Fatalf("draft=%+v err=%v", storedDraft, err)
	}
	drafts, err := s.ListDrafts(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(drafts.Items) != 1 {
		t.Fatalf("drafts=%+v err=%v", drafts, err)
	}
	history, err := s.ListScheduledMessageHistory(ctx, "T1", "first-party", true, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Items) != 1 || history.Items[0].Text != "after" || history.Items[0].DeliveredAt.IsZero() || !history.Items[0].FailedAt.IsZero() || history.Items[0].FailureCode != "" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	pending, err := s.ListScheduledMessageHistory(ctx, "T1", "first-party", false, domain.PageRequest{Limit: 10})
	if err != nil || len(pending.Items) != 0 {
		t.Fatalf("pending history=%+v err=%v", pending, err)
	}
	sent, err := s.ListAuthoredMessages(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(sent.Messages) != 1 || sent.Messages[0].Text != "sent" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
}

func TestVersion109MigrationCreatesDraftStorage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-109.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE drafts`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 108 WHERE version = ?`, schemaVersion); err != nil {
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
	columns, err := s.tableColumns(ctx, s.db, "drafts")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"workspace_id", "user_id", "conversation_id", "thread_ts", "text", "updated_at"} {
		if !columns[column] {
			t.Fatalf("drafts.%s was not migrated", column)
		}
	}
}
