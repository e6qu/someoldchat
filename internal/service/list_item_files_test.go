package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func listFileWorld(t *testing.T) (context.Context, Messages, *memory.Store, domain.ListID, domain.ListItemID) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "files"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, Blob: objects}
	list, err := messages.CreateList(ctx, "T1", "U1", "Assets", "", "[]", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Title", domain.ListColumnText, nil); err != nil {
		t.Fatal(err)
	}
	item, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"the logo"}]`)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, messages, repository, list.ID, item.ID
}

func uploadTestFile(t *testing.T, ctx context.Context, messages Messages, user domain.UserID, name string) domain.File {
	t.Helper()
	file, err := messages.UploadFile(ctx, "T1", user, name, name, "image/png", 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	return file
}

// The whole point of the join: a file attached to a list item is shared into no
// conversation, yet every reader of the list can open it. Without the
// authorizeFileAccess branch this test's reader would get not-found.
func TestAListReaderCanOpenAFileAttachedToAnItem(t *testing.T) {
	ctx, messages, repository, listID, itemID := listFileWorld(t)
	if err := messages.SetListAccess(ctx, "T1", "U1", listID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	file := uploadTestFile(t, ctx, messages, "U1", "logo.png")
	if len(file.SharedChannels) != 0 {
		t.Fatalf("a freshly uploaded file was shared into %v, want nowhere", file.SharedChannels)
	}
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U1", listID, itemID, file.ID); err != nil {
		t.Fatal(err)
	}
	files, err := messages.ListItemFiles(ctx, "T1", "U2", listID, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != file.ID {
		t.Fatalf("reader saw %+v, want the one attached file", files)
	}
	// The reader can both read the metadata and pull the bytes although the file
	// was never shared into a channel they belong to.
	if _, err := messages.FileInfo(ctx, "T1", "U2", file.ID); err != nil {
		t.Fatalf("a list reader could not read the attached file's info: %v", err)
	}
	_, reader, err := messages.OpenFile(ctx, "T1", "U2", file.ID)
	if err != nil {
		t.Fatalf("a list reader could not open the attached file: %v", err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	_ = repository
}

// A member with no grant on the list sees neither the attachment nor the file
// behind it — the file's own access answers exactly as the list's does.
func TestAStrangerCannotOpenAnItemFile(t *testing.T) {
	ctx, messages, _, listID, itemID := listFileWorld(t)
	file := uploadTestFile(t, ctx, messages, "U1", "logo.png")
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U1", listID, itemID, file.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ListItemFiles(ctx, "T1", "U3", listID, itemID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger listed the item's files: %v", err)
	}
	if _, err := messages.FileInfo(ctx, "T1", "U3", file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger read the attached file's info: %v", err)
	}
}

// Attaching and detaching are edits: a reader who may open the file may not
// change what the item carries.
func TestOnlyAListEditorAttachesAndDetaches(t *testing.T) {
	ctx, messages, _, listID, itemID := listFileWorld(t)
	if err := messages.SetListAccess(ctx, "T1", "U1", listID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	file := uploadTestFile(t, ctx, messages, "U1", "logo.png")
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U2", listID, itemID, file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a reader attached a file: %v", err)
	}
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U1", listID, itemID, file.ID); err != nil {
		t.Fatal(err)
	}
	if err := messages.DetachFileFromListItem(ctx, "T1", "U2", listID, itemID, file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a reader detached a file: %v", err)
	}
	if err := messages.DetachFileFromListItem(ctx, "T1", "U1", listID, itemID, file.ID); err != nil {
		t.Fatal(err)
	}
}

// Detaching a file revokes the access it lent the list's readers; the uploader,
// who could always reach their own file, keeps it.
func TestDetachingRevokesReaderFileAccess(t *testing.T) {
	ctx, messages, _, listID, itemID := listFileWorld(t)
	if err := messages.SetListAccess(ctx, "T1", "U1", listID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	file := uploadTestFile(t, ctx, messages, "U1", "logo.png")
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U1", listID, itemID, file.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.FileInfo(ctx, "T1", "U2", file.ID); err != nil {
		t.Fatalf("the reader could not read the attached file before detach: %v", err)
	}
	if err := messages.DetachFileFromListItem(ctx, "T1", "U1", listID, itemID, file.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.FileInfo(ctx, "T1", "U2", file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the reader still reached the file after detach: %v", err)
	}
	// The uploader keeps access — a detach removes the association, not the file.
	if _, err := messages.FileInfo(ctx, "T1", "U1", file.ID); err != nil {
		t.Fatalf("the uploader lost their own file after detach: %v", err)
	}
}

// TestDeactivatedListEditorsLoseTheirFileGuards makes the active-membership guard
// on attaching, listing and detaching load-bearing. The store records nothing
// about who may act — it enforces only that the file exists — so U1's ownership
// carries no weight there; only the service's requireListAccess refuses a removed
// member, and stripping it must be caught here.
func TestDeactivatedListEditorsLoseTheirFileGuards(t *testing.T) {
	ctx, messages, repository, listID, itemID := listFileWorld(t)
	attached := uploadTestFile(t, ctx, messages, "U1", "logo.png")
	spare := uploadTestFile(t, ctx, messages, "U1", "spare.png")
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U1", listID, itemID, attached.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: domain.EventID("gone-U1"), WorkspaceID: "T1", Topic: "user.removed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AttachFileToListItem(ctx, "T1", "U1", listID, itemID, spare.ID); err == nil {
		t.Fatal("a deactivated owner attached a file")
	}
	if _, err := messages.ListItemFiles(ctx, "T1", "U1", listID, itemID); err == nil {
		t.Fatal("a deactivated owner listed the item's files")
	}
	if err := messages.DetachFileFromListItem(ctx, "T1", "U1", listID, itemID, attached.ID); err == nil {
		t.Fatal("a deactivated owner detached a file")
	}
}
