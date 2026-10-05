package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func TestCanvasPersistence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := domain.Workspace{ID: "T-canvas", Name: "Canvas"}
	user := domain.User{ID: "U-canvas", WorkspaceID: workspace.ID, Email: "canvas@example.com", Name: "canvas"}
	if err := store.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	canvas := domain.Canvas{ID: "F-canvas", WorkspaceID: workspace.ID, OwnerID: user.ID, Title: "Canvas", DocumentContent: `{"sections":[]}`, CreatedAt: now, UpdatedAt: now}
	event := events.Event{ID: "E-canvas", WorkspaceID: workspace.ID, Topic: "canvas.created", Payload: string(canvas.ID), CreatedAt: now}
	if err := store.CreateCanvas(ctx, canvas, event); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCanvas(ctx, workspace.ID, canvas.ID)
	if err != nil || loaded.DocumentContent != canvas.DocumentContent {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if err := store.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: domain.GrantUser, EntityID: string(user.ID), Access: domain.AccessWrite}, events.Event{ID: "E-canvas-access", WorkspaceID: workspace.ID, Topic: "canvas.access_set", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: domain.GrantUser, EntityID: string(user.ID)}, events.Event{ID: "E-canvas-access-delete", WorkspaceID: workspace.ID, Topic: "canvas.access_deleted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCanvas(ctx, workspace.ID, canvas.ID, events.Event{ID: "E-canvas-delete", WorkspaceID: workspace.ID, Topic: "canvas.deleted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

// The collaborative text is stored beside the document: written by every
// update, read back by GetCanvas, and never copied into a revision, which
// records what the document said rather than how editors named its characters.
func TestCanvasTextStatePersists(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := domain.Workspace{ID: "T-text", Name: "Text"}
	user := domain.User{ID: "U-text", WorkspaceID: workspace.ID, Email: "text@example.com", Name: "text"}
	if err := store.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	canvas := domain.Canvas{ID: "F-text", WorkspaceID: workspace.ID, OwnerID: user.ID, Title: "Text", DocumentContent: `{"sections":[]}`, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateCanvas(ctx, canvas, events.Event{ID: "E-text", WorkspaceID: workspace.ID, Topic: "canvas.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.GetCanvas(ctx, workspace.ID, canvas.ID); err != nil || loaded.TextState != "" {
		t.Fatalf("new canvas text = %q, %v", loaded.TextState, err)
	}
	canvas.TextState = `[{"r":"U-text.tab","c":1,"t":"Hello"}]`
	canvas.DocumentContent = `{"sections":[{"id":"s1","type":"markdown","text":"Hello"}]}`
	canvas.Version = 2
	canvas.UpdatedAt = now.Add(time.Hour)
	if err := store.UpdateCanvas(ctx, canvas, events.Event{ID: "E-text-2", WorkspaceID: workspace.ID, ActorID: user.ID, Topic: "canvas.updated", CreatedAt: canvas.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCanvas(ctx, workspace.ID, canvas.ID)
	if err != nil || loaded.TextState != canvas.TextState || loaded.DocumentContent != canvas.DocumentContent {
		t.Fatalf("updated canvas = %+v, %v", loaded, err)
	}
	page, err := store.ListCanvasRevisions(ctx, workspace.ID, user.ID, canvas.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Revisions) != 1 || page.Revisions[0].DocumentContent != `{"sections":[]}` {
		t.Fatalf("revisions = %+v, %v", page.Revisions, err)
	}
}
