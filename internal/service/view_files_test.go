package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A file_input value names files by id, and only the submitting member's own
// live uploads of an accepted type, no more than max_files of them, are
// handed to the app — which is granted read access to exactly those.
func TestViewFileInputAcceptsOnlyTheMembersOwnAcceptedFiles(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}),
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"}),
		repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1"}),
		repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "bot", UpdatedAt: now}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	for _, file := range []domain.File{
		{ID: "Fmine", WorkspaceID: "T1", Uploader: "U1", Name: "mine.pdf", BlobKey: "a", CreatedAt: now},
		{ID: "Fsecond", WorkspaceID: "T1", Uploader: "U1", Name: "second.pdf", BlobKey: "b", CreatedAt: now},
		{ID: "Fimage", WorkspaceID: "T1", Uploader: "U1", Name: "photo.png", BlobKey: "c", CreatedAt: now},
		{ID: "Ftheirs", WorkspaceID: "T1", Uploader: "U2", Name: "theirs.pdf", BlobKey: "d", CreatedAt: now},
	} {
		if err := repository.CreateFile(ctx, file, events.Event{ID: domain.EventID("E" + string(file.ID)), WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	view := domain.View{ID: "V1", AppID: "A1", WorkspaceID: "T1", UserID: "U1", Type: "modal",
		Payload: `{"type":"modal","blocks":[{"type":"input","block_id":"b","element":{"type":"file_input","action_id":"f","filetypes":["pdf"],"max_files":1}}]}`}
	messages := Messages{Store: repository, PublicURL: "https://chat.example.test"}
	state := func(ids ...string) string {
		files := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			files = append(files, map[string]string{"id": id})
		}
		encoded, _ := json.Marshal(map[string]any{"values": map[string]any{"b": map[string]any{"f": map[string]any{"type": "file_input", "files": files}}}})
		return string(encoded)
	}
	for name, ids := range map[string][]string{
		"another member's file":    {"Ftheirs"},
		"a type outside filetypes": {"Fimage"},
		"more than max_files":      {"Fmine", "Fsecond"},
		"an unknown file":          {"Fmissing"},
	} {
		if _, err := messages.attachViewFiles(ctx, view, "U1", state(ids...)); !errors.Is(err, ErrViewFilesInvalid) {
			t.Fatalf("%s: err=%v, want ErrViewFilesInvalid", name, err)
		}
	}
	if readable, _ := repository.FileReadableViaGrant(ctx, "T1", "UB", "Ftheirs"); readable {
		t.Fatal("a refused file was granted to the app")
	}
	accepted, err := messages.attachViewFiles(ctx, view, "U1", state("Fmine"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Values map[string]map[string]struct {
			Files []map[string]any `json:"files"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(accepted), &decoded); err != nil {
		t.Fatal(err)
	}
	files := decoded.Values["b"]["f"].Files
	if len(files) != 1 || files[0]["id"] != "Fmine" || files[0]["name"] != "mine.pdf" ||
		files[0]["url_private"] != "https://chat.example.test/api/files/Fmine" {
		t.Fatalf("accepted files = %v", files)
	}
	for user, want := range map[domain.UserID]bool{"UB": true, "U2": false} {
		if readable, err := repository.FileReadableViaGrant(ctx, "T1", user, "Fmine"); err != nil || readable != want {
			t.Fatalf("%s readable=%v err=%v, want %v", user, readable, err, want)
		}
	}
	if _, err := messages.FileInfo(ctx, "T1", "U2", "Fmine"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bystander file info err=%v, want ErrNotFound", err)
	}
}
