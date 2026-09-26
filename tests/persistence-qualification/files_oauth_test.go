package qualification

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// visibleFilesAreNewestFirst pins the order files.list pages in: created_at
// DESC, then id DESC for files created in the same instant, with a cursor that
// resumes exactly where the previous page stopped on every profile.
func visibleFilesAreNewestFirst(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1_700_000_000, 0).UTC()
	create := func(name string, createdAt time.Time) domain.FileID {
		t.Helper()
		file := domain.File{
			ID: domain.FileID("F-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, Uploader: f.userID,
			Name: name + ".txt", Title: name, MIMEType: "text/plain", BlobKey: "blob-" + name + "-" + f.suffix, Size: 1, CreatedAt: createdAt,
		}
		if err := f.repository.CreateFile(ctx, file, f.event("file-"+name, "file.created", string(file.ID))); err != nil {
			t.Fatal(err)
		}
		return file.ID
	}
	oldest := create("a", base)
	tiedLow := create("b", base.Add(time.Second))
	tiedHigh := create("c", base.Add(time.Second))
	newest := create("d", base.Add(2*time.Second))
	want := []domain.FileID{newest, tiedHigh, tiedLow, oldest}

	var got []domain.FileID
	request := domain.PageRequest{Limit: 2}
	for pages := 0; pages < 4; pages++ {
		page, err := f.repository.ListVisibleFiles(ctx, f.workspaceID, f.userID, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range page.Files {
			got = append(got, file.ID)
		}
		if !page.HasMore {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("files=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("files=%v, want %v", got, want)
		}
	}
}
