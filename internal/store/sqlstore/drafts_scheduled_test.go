package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
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
	upload := domain.ExternalUpload{
		ID: "upload_draft", WorkspaceID: "T1", Uploader: "U1", Name: "notes.txt", Title: "notes.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/upload_draft", Size: 7, Status: domain.ExternalUploadUploaded,
		CreatedAt: now, ExpiresAt: now.Add(10 * 365 * 24 * time.Hour), UploadedAt: now,
	}
	require(s.CreateExternalUpload(ctx, upload))
	draft := domain.Draft{
		WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", Text: "unfinished", UpdatedAt: now,
		Attachments: []domain.DraftAttachment{{UploadID: upload.ID, Name: upload.Name, Title: "Release notes", MIMEType: upload.MIMEType, Size: upload.Size}},
	}
	if _, err := s.UpsertDraft(ctx, draft, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "draft.saved"}); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "sent", CreatedAt: now.Add(time.Minute)}
	require(s.CreateMessage(ctx, message, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "message.created"}, ""))
	scheduled := domain.ScheduledMessage{
		WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", CredentialHash: "first-party",
		Text: "before", PostAt: now.Add(2 * time.Hour), CreatedAt: now,
		FileAttachments: []domain.DraftAttachment{{
			UploadID: upload.ID, Name: upload.Name, Title: "Scheduled notes", MIMEType: upload.MIMEType, Size: upload.Size,
		}},
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
	if err != nil || failedClaim.ID != "Q1" || len(failedClaim.FileAttachments) != 1 {
		t.Fatalf("worker claim=%+v err=%v", failedClaim, err)
	}
	require(s.MarkScheduledMessageFailed(ctx, "worker", "Q1", "not_in_channel", now.Add(time.Minute), events.Event{ID: "E5", WorkspaceID: "T1", Topic: "message.schedule_failed"}))
	claimed, err := s.ClaimScheduledMessageForCredential(ctx, "T1", "first-party", "Q1", "send-now", time.Minute)
	if err != nil || claimed.ID != "Q1" || len(claimed.FileAttachments) != 1 {
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
	if err != nil || storedDraft.Text != "unfinished" || len(storedDraft.Attachments) != 1 ||
		storedDraft.Attachments[0].UploadID != upload.ID || storedDraft.Attachments[0].Title != "Release notes" {
		t.Fatalf("draft=%+v err=%v", storedDraft, err)
	}
	drafts, err := s.ListDrafts(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(drafts.Items) != 1 || len(drafts.Items[0].Attachments) != 1 {
		t.Fatalf("drafts=%+v err=%v", drafts, err)
	}
	var references []string
	if err := s.WalkBlobReferences(ctx, "T1", func(reference string) error {
		references = append(references, reference)
		return nil
	}); err != nil || len(references) != 1 || references[0] != upload.BlobKey {
		t.Fatalf("draft blob references=%v err=%v", references, err)
	}
	history, err := s.ListScheduledMessageHistory(ctx, "T1", "first-party", true, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Items) != 1 || history.Items[0].Text != "after" || len(history.Items[0].FileAttachments) != 1 ||
		history.Items[0].FileAttachments[0].Title != "Scheduled notes" || history.Items[0].DeliveredAt.IsZero() ||
		!history.Items[0].FailedAt.IsZero() || history.Items[0].FailureCode != "" {
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

func TestVersion117MigrationCreatesDraftAttachmentStorage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-117.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE draft_attachments`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 116 WHERE version = ?`, schemaVersion); err != nil {
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
	columns, err := s.tableColumns(ctx, s.db, "draft_attachments")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"workspace_id", "user_id", "conversation_id", "thread_ts", "ordinal", "upload_id", "title"} {
		if !columns[column] {
			t.Fatalf("draft_attachments.%s was not migrated", column)
		}
	}
}

func TestVersion118MigrationCreatesScheduledFileStorage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-118.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE scheduled_message_files`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 117 WHERE version = ?`, schemaVersion); err != nil {
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
	columns, err := s.tableColumns(ctx, s.db, "scheduled_message_files")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"scheduled_message_id", "ordinal", "upload_id", "title"} {
		if !columns[column] {
			t.Fatalf("scheduled_message_files.%s was not migrated", column)
		}
	}
}

func TestSQLiteDraftAttachmentOutlivesExternalUploadTicket(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:draft-ticket-window?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "expired-draft-upload", WorkspaceID: "T1", Uploader: "U1", Name: "old.txt", Title: "old.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/expired-draft-upload", Size: 3,
		Status: domain.ExternalUploadUploaded, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute),
	}
	require(s.CreateExternalUpload(ctx, upload))
	if _, err := s.UpsertDraft(ctx, domain.Draft{
		WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", Text: "unfinished",
		Attachments: []domain.DraftAttachment{{UploadID: upload.ID, Name: upload.Name, Title: upload.Title, MIMEType: upload.MIMEType, Size: upload.Size}},
		UpdatedAt:   now,
	}, events.Event{ID: "draft-save", WorkspaceID: "T1", Topic: "draft.saved", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	file := domain.File{
		ID: domain.FileID(upload.ID), WorkspaceID: "T1", Uploader: "U1", Name: upload.Name, Title: upload.Title,
		MIMEType: upload.MIMEType, BlobKey: upload.BlobKey, Size: upload.Size, CreatedAt: now,
	}
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "finished", CreatedAt: now}
	require(s.CompleteExternalUploads(
		ctx,
		[]storepkg.UploadedFile{{Completion: domain.ExternalUploadCompletion{ID: upload.ID}, File: file}},
		[]domain.ConversationID{"C1"},
		[]events.Event{{ID: "file-event", WorkspaceID: "T1", Topic: "file.created", CreatedAt: now}},
		[]storepkg.PostedMessage{{Message: message, Event: events.Event{ID: "message-event", WorkspaceID: "T1", Topic: "message.created", CreatedAt: now}}},
	))
	completed, err := s.GetExternalUpload(ctx, upload.ID)
	if err != nil || completed.Status != domain.ExternalUploadCompleted {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestSQLiteScheduledFileDeliveryRollsBackAndRetriesAsOneTransaction(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:scheduled-file-atomicity?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "scheduled-atomic-upload", WorkspaceID: "T1", Uploader: "U1", Name: "evidence.txt", Title: "evidence.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/scheduled-atomic-upload", Size: 8,
		Status: domain.ExternalUploadUploaded, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute), UploadedAt: now.Add(-time.Hour),
	}
	require(s.CreateExternalUpload(ctx, upload))
	scheduled := domain.ScheduledMessage{
		WorkspaceID: "T1", ID: "Q-atomic", Channel: "C1", Author: "U1", CredentialHash: "first-party",
		PostAt: now.Add(time.Hour), CreatedAt: now,
		FileAttachments: []domain.DraftAttachment{{
			UploadID: upload.ID, Name: upload.Name, Title: "Evidence", MIMEType: upload.MIMEType, Size: upload.Size,
		}},
	}
	require(s.CreateScheduledMessage(ctx, scheduled, events.Event{ID: "scheduled-event", WorkspaceID: "T1", Topic: "message.scheduled", CreatedAt: now}))
	file := domain.File{
		ID: domain.FileID(upload.ID), WorkspaceID: "T1", Uploader: "U1", Name: upload.Name, Title: "Evidence",
		MIMEType: upload.MIMEType, BlobKey: upload.BlobKey, Size: upload.Size, CreatedAt: now,
	}
	require(s.CreateMessage(ctx, domain.Message{
		ID: "M-taken", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1",
		Text: "existing", CreatedAt: now.Add(-time.Second),
	}, events.Event{ID: "existing-message-event", WorkspaceID: "T1", Topic: "message.created", CreatedAt: now.Add(-time.Second)}, ""))
	message := domain.Message{
		ID: "M-taken", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1",
		Text: "scheduled evidence", CreatedAt: now, Files: []domain.File{file},
	}
	completions := []domain.ExternalUploadCompletion{{ID: upload.ID, Title: file.Title}}
	fileEvents := []events.Event{{ID: "file-event", WorkspaceID: "T1", Topic: "file.created", CreatedAt: now}}
	messageEvents := []events.Event{{ID: "message-event", WorkspaceID: "T1", Topic: "message.created", CreatedAt: now}}
	err = s.CompleteScheduledExternalUploads(ctx, scheduled.ID,
		[]storepkg.UploadedFile{{Completion: completions[0], File: file}},
		[]domain.ConversationID{"C1"}, fileEvents,
		storepkg.PostedMessage{Message: message, Event: messageEvents[0]})
	if !errors.Is(err, storepkg.ErrAlreadyExists) {
		t.Fatalf("late message failure=%v, want already exists", err)
	}
	storedUpload, err := s.GetExternalUpload(ctx, upload.ID)
	if err != nil || storedUpload.Status != domain.ExternalUploadUploaded || storedUpload.FileID != "" {
		t.Fatalf("failed transaction changed upload=%+v err=%v", storedUpload, err)
	}
	if _, err := s.GetFile(ctx, file.ID); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("failed transaction exposed file: %v", err)
	}
	if _, err := s.GetIdempotentMessage(ctx, "T1", "U1", string(scheduled.ID)); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("failed transaction reserved idempotency key: %v", err)
	}

	message.ID = "M-atomic"
	require(s.CompleteScheduledExternalUploads(ctx, scheduled.ID,
		[]storepkg.UploadedFile{{Completion: completions[0], File: file}},
		[]domain.ConversationID{"C1"}, fileEvents,
		storepkg.PostedMessage{Message: message, Event: messageEvents[0]}))
	delivered, err := s.GetIdempotentMessage(ctx, "T1", "U1", string(scheduled.ID))
	if err != nil || delivered.ID != message.ID || len(delivered.Files) != 1 || delivered.Files[0].ID != file.ID {
		t.Fatalf("delivered=%+v err=%v", delivered, err)
	}
	if err := s.CompleteScheduledExternalUploads(ctx, scheduled.ID,
		[]storepkg.UploadedFile{{Completion: completions[0], File: file}},
		[]domain.ConversationID{"C1"}, fileEvents,
		storepkg.PostedMessage{Message: message, Event: messageEvents[0]}); !errors.Is(err, storepkg.ErrIdempotencyConflict) {
		t.Fatalf("store retry=%v, want idempotency conflict", err)
	}
	history, err := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 2 || history.Messages[1].ID != message.ID {
		t.Fatalf("history=%+v err=%v", history, err)
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
