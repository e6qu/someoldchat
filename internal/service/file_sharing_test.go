package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestShareUploadedFileSharesIntoChannels covers files.upload's sharing path: a
// hosted file is shared into several channels with an initial comment, every
// destination is validated before any share, and the refusals match Slack's.
func TestShareUploadedFileSharesIntoChannels(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "random"})
	s.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "archived", Archived: true})
	s.SeedConversation(domain.Conversation{ID: "C4", WorkspaceID: "T1", Name: "private"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	s.SeedConversationMember("C2", "U1")
	s.SeedConversationMember("C3", "U1")
	s.SeedConversationMember("C4", "U2") // U1 is not a member of C4
	if err := s.CreateFile(ctx, domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", Name: "report.txt", Title: "Report", MIMEType: "text/plain", BlobKey: "b1", Size: 6, CreatedAt: time.Now().UTC()}, events.Event{ID: "EF1", WorkspaceID: "T1", Topic: "file.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}

	shared, err := messages.ShareUploadedFile(ctx, "T1", "U1", "F1", []domain.ConversationID{"C1", "C2"}, "here is the report", "")
	if err != nil || len(shared) != 2 || shared[0] != "C1" || shared[1] != "C2" {
		t.Fatalf("shared=%+v err=%v", shared, err)
	}
	// Each channel got a message carrying the initial comment and the file.
	for _, channel := range []domain.ConversationID{"C1", "C2"} {
		page, listErr := s.ListMessages(ctx, channel, domain.PageRequest{Limit: 10})
		if listErr != nil {
			t.Fatal(listErr)
		}
		found := false
		for _, message := range page.Messages {
			if message.Text == "here is the report" && len(message.Files) == 1 && message.Files[0].ID == "F1" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no share message in %s: %+v", channel, page.Messages)
		}
	}

	// Refusals.
	if _, err := messages.ShareUploadedFile(ctx, "T1", "U9", "F1", []domain.ConversationID{"C1"}, "", ""); err == nil {
		t.Fatal("a stranger was allowed to share")
	}
	if _, err := messages.ShareUploadedFile(ctx, "T1", "U2", "F1", []domain.ConversationID{"C1"}, "", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner share = %v, want ErrNotFound", err)
	}
	if _, err := messages.ShareUploadedFile(ctx, "T1", "U1", "F1", []domain.ConversationID{"C4"}, "", ""); err == nil {
		t.Fatal("sharing into a channel the sharer is not in was allowed")
	}
	if _, err := messages.ShareUploadedFile(ctx, "T1", "U1", "F1", []domain.ConversationID{"C3"}, "", ""); !errors.Is(err, ErrConversationAlreadyArchived) {
		t.Fatalf("archived share = %v, want ErrConversationAlreadyArchived", err)
	}
	if _, err := messages.ShareUploadedFile(ctx, "T1", "U1", "F1", []domain.ConversationID{"C1", "C2"}, "", "123.456"); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("threaded multi-channel share = %v, want ErrInvalidFile", err)
	}

	// Validate-all-first: a request naming a good channel and an archived one
	// shares into neither, so nothing is left half-shared.
	before, _ := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 50})
	if _, err := messages.ShareUploadedFile(ctx, "T1", "U1", "F1", []domain.ConversationID{"C1", "C3"}, "", ""); !errors.Is(err, ErrConversationAlreadyArchived) {
		t.Fatalf("mixed archived share = %v, want ErrConversationAlreadyArchived", err)
	}
	after, _ := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 50})
	if len(after.Messages) != len(before.Messages) {
		t.Fatalf("validate-all-first violated: C1 gained a share though C3 was archived (%d -> %d)", len(before.Messages), len(after.Messages))
	}
}
