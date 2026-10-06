package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A thread reply that carries files and is also sent to the channel keeps
// its broadcast, whether it is sent now or scheduled, as a reply without
// files does. A broadcast outside a thread is refused.
func TestAReplyWithFilesKeepsItsBroadcast(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	ctx := context.Background()
	upload := func() domain.ExternalUploadCompletion {
		t.Helper()
		value, err := messages.CreateExternalUpload(ctx, "T1", "U1", domain.ExternalUploadRequest{Name: "notes.txt", MIMEType: "text/plain", Size: 7, TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if err := messages.UploadExternalFile(ctx, value.ID, 7, bytes.NewReader([]byte("content"))); err != nil {
			t.Fatal(err)
		}
		return domain.ExternalUploadCompletion{ID: value.ID, Title: "Notes"}
	}
	root, err := messages.Post(ctx, "T1", "U1", "C1", "the thread", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)

	if _, err := messages.CompleteExternalUploads(ctx, "T1", "U1", []domain.ExternalUploadCompletion{upload()}, []domain.ConversationID{"C1"}, "also here", "", "", true); !errors.Is(err, domain.ErrInvalidMessage) {
		t.Fatalf("a broadcast outside a thread: %v", err)
	}
	if _, err := messages.CompleteExternalUploads(ctx, "T1", "U1", []domain.ExternalUploadCompletion{upload()}, []domain.ConversationID{"C1"}, "see attached", "", thread, true); err != nil {
		t.Fatal(err)
	}
	draft, err := messages.SaveDraftWithAttachments(ctx, "T1", "U1", "C1", thread, "later, with a file", []domain.DraftAttachment{{UploadID: upload().ID, Title: "Later"}})
	if err != nil || len(draft.Attachments) != 1 {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	scheduled, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{
		Channel: "C1", Text: "later, with a file", ThreadTimestamp: thread, PostAt: time.Now().Add(time.Hour),
		StreamState:     `{"reply_broadcast":true}`,
		CredentialHash:  domain.InternalScheduledCredential("T1", "U1"),
		FileAttachments: draft.Attachments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.PostScheduledMessage(ctx, "T1", scheduled.ID); err != nil {
		t.Fatal(err)
	}
	replies, err := s.ListThreadMessages(ctx, "C1", thread, domain.ThreadRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	broadcasts := 0
	for _, reply := range replies.Messages {
		if len(reply.Files) == 1 && reply.ReplyBroadcast {
			broadcasts++
		}
	}
	if broadcasts != 2 {
		t.Fatalf("replies=%+v, want both the sent and the scheduled reply with files broadcast", replies.Messages)
	}
}
