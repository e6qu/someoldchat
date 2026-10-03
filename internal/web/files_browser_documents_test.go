package web

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// Slack's Files view holds canvases and lists beside uploaded files. Each
// opens its own page, has no download, and can be picked out with the type
// filter; the ownership tabs and the search apply to them as to files.
func TestFilesViewListsCanvasesAndLists(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	ctx := context.Background()
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Launch plan", `{"type":"markdown","markdown":"steps"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch checklist", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	file := domain.File{ID: "Fnotes", WorkspaceID: "T1", Uploader: "U1", Name: "notes.txt", Title: "Launch notes", MIMEType: "text/plain", BlobKey: "notes", Size: 12, CreatedAt: time.Now().UTC()}
	if err := s.CreateFile(ctx, file, events.Event{ID: "EFnotes", WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	canvasLink := `href="/app/canvases/` + string(canvas.ID) + `">Launch plan</a>`
	listLink := `href="/app/lists/` + string(list.ID) + `">Launch checklist</a>`
	all := get(t, mux, "/app/files").Body.String()
	requireContains(t, "all files", all, canvasLink, listLink, `>Launch notes</a>`, "<span>Canvas</span>", "<span>List</span>",
		`<option value="canvases">Canvases</option>`, `<option value="lists">Lists</option>`, "3 files.")
	requireMissing(t, "a canvas has no bytes", all, `aria-label="Download Launch plan"`, `aria-label="Download Launch checklist"`)
	requireContains(t, "a file keeps its download", all, `aria-label="Download Launch notes"`)

	canvases := get(t, mux, "/app/files?type=canvases").Body.String()
	requireContains(t, "canvases only", canvases, canvasLink, "1 file.")
	requireMissing(t, "canvases only", canvases, listLink, `>Launch notes</a>`)
	lists := get(t, mux, "/app/files?type=lists&q=checklist").Body.String()
	requireContains(t, "lists searched", lists, listLink)
	requireMissing(t, "lists searched", lists, canvasLink)
	documents := get(t, mux, "/app/files?type=documents").Body.String()
	requireMissing(t, "a file type", documents, canvasLink, listLink)
	shared := get(t, mux, "/app/files?owner=shared").Body.String()
	requireMissing(t, "shared with you", shared, canvasLink, listLink)
}
