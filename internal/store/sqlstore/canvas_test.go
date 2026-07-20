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
	if err := store.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: "user", EntityID: string(user.ID), Access: "write"}, events.Event{ID: "E-canvas-access", WorkspaceID: workspace.ID, Topic: "canvas.access_set", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: "user", EntityID: string(user.ID)}, events.Event{ID: "E-canvas-access-delete", WorkspaceID: workspace.ID, Topic: "canvas.access_deleted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCanvas(ctx, workspace.ID, canvas.ID, events.Event{ID: "E-canvas-delete", WorkspaceID: workspace.ID, Topic: "canvas.deleted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}
