package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func describedWorld(t *testing.T) (context.Context, Messages, domain.FileID) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "uploader"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "reader"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	repository.SeedConversationMember("C1", "U2")
	file := domain.File{
		ID: "Fimage", WorkspaceID: "T1", Uploader: "U1", Name: "diagram.png", Title: "Architecture",
		MIMEType: "image/png", BlobKey: "blob", Size: 12, CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		SharedChannels: []domain.ConversationID{"C1"},
	}
	if err := repository.CreateFile(ctx, file, events.Event{ID: "EF", WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	return ctx, Messages{Store: repository}, file.ID
}

// A description is the uploader's account of their own file. Anyone being able
// to write it would make it a caption instead, and the refusal must be the same
// answer a missing file gives so it cannot be used to probe for files.
func TestOnlyTheUploaderDescribesAFile(t *testing.T) {
	ctx, messages, fileID := describedWorld(t)
	if err := messages.SetFileDescription(ctx, "T1", "U1", fileID, "A box diagram of the seam"); err != nil {
		t.Fatal(err)
	}
	described, err := messages.FileInfo(ctx, "T1", "U2", fileID)
	if err != nil {
		t.Fatal(err)
	}
	if described.Description != "A box diagram of the seam" {
		t.Fatalf("description = %q, want the uploader's", described.Description)
	}
	if err := messages.SetFileDescription(ctx, "T1", "U2", fileID, "not mine to write"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member's write = %v, want ErrNotFound", err)
	}
	unchanged, err := messages.FileInfo(ctx, "T1", "U1", fileID)
	if err != nil || unchanged.Description != "A box diagram of the seam" {
		t.Fatalf("description = %q err = %v, want it unchanged", unchanged.Description, err)
	}
}

// Clearing is the only way to correct a description that was wrong, so the
// empty string is accepted where an over-long one is not.
func TestADescriptionCanBeClearedAndIsBounded(t *testing.T) {
	ctx, messages, fileID := describedWorld(t)
	if err := messages.SetFileDescription(ctx, "T1", "U1", fileID, "first attempt"); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetFileDescription(ctx, "T1", "U1", fileID, "   "); err != nil {
		t.Fatalf("clearing a description = %v, want it accepted", err)
	}
	cleared, err := messages.FileInfo(ctx, "T1", "U1", fileID)
	if err != nil || cleared.Description != "" {
		t.Fatalf("description = %q err = %v, want it cleared", cleared.Description, err)
	}
	tooLong := strings.Repeat("x", FileDescriptionLimit+1)
	if err := messages.SetFileDescription(ctx, "T1", "U1", fileID, tooLong); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("over-long description = %v, want ErrInvalidFile", err)
	}
}

// What a screen reader announces. The file name is deliberately never used: an
// alt text of "diagram.png" tells a reader nothing and stops them skipping the
// image, which is worse than an empty one.
func TestAccessibleNamePrefersTheDescriptionAndNeverTheFileName(t *testing.T) {
	file := domain.File{Name: "IMG_4032.png", Title: "Architecture", MIMEType: "image/png"}
	if got := file.AccessibleName(); got != "Architecture" {
		t.Fatalf("accessible name = %q, want the title", got)
	}
	file.Description = "A box diagram of the seam"
	if got := file.AccessibleName(); got != file.Description {
		t.Fatalf("accessible name = %q, want the description", got)
	}
	bare := domain.File{Name: "IMG_4032.png", MIMEType: "image/png"}
	if got := bare.AccessibleName(); got != "" {
		t.Fatalf("accessible name = %q, want empty rather than the file name", got)
	}
	if !bare.IsImage() {
		t.Fatal("a PNG is not reported as an image")
	}
	if (domain.File{Name: "notes.txt", MIMEType: "text/plain"}).IsImage() {
		t.Fatal("a text file is reported as an image")
	}
}
