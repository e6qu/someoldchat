package memory

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// TestDeleteCanvasClearsCommentsAndRevisions guards a cross-profile invariant the
// public API cannot observe: reads of a canvas's comments and revisions are gated
// on the canvas still existing, so a leaked child row is unreachable rather than
// wrong. The SQL profile deletes canvas_comments and canvas_revisions with the
// canvas; the in-memory profile once deleted only the canvas and its access rows
// and left comments and revisions behind. That divergence is benign until a
// count or a future ungated read exposes it, so the two profiles are kept in step
// here, checked directly against the store's own maps.
func TestDeleteCanvasClearsCommentsAndRevisions(t *testing.T) {
	s := New()
	const workspace = domain.WorkspaceID("T1")
	const canvas = domain.CanvasID("CV1")
	now := time.Unix(1700000000, 0).UTC()

	s.canvases[canvas] = domain.Canvas{ID: canvas, WorkspaceID: workspace, Title: "Notes"}
	s.canvasAccess["CV1\x00user\x00U1"] = domain.CanvasAccess{CanvasID: canvas}
	s.canvasComments["CC1"] = domain.CanvasComment{ID: "CC1", CanvasID: canvas, WorkspaceID: workspace}
	s.canvasComments["CC-other"] = domain.CanvasComment{ID: "CC-other", CanvasID: "CV-other", WorkspaceID: workspace}
	s.canvasRevisions[canvas] = []domain.CanvasRevision{{CanvasID: canvas, CreatedAt: now}}
	s.canvasRevisions["CV-other"] = []domain.CanvasRevision{{CanvasID: "CV-other", CreatedAt: now}}

	if err := s.DeleteCanvas(context.Background(), workspace, canvas, events.Event{ID: "E1", WorkspaceID: workspace, Topic: "canvas.deleted", CreatedAt: now}); err != nil {
		t.Fatalf("delete canvas: %v", err)
	}

	if _, ok := s.canvasComments["CC1"]; ok {
		t.Error("the deleted canvas's comment survived")
	}
	if _, ok := s.canvasComments["CC-other"]; !ok {
		t.Error("another canvas's comment was deleted")
	}
	if _, ok := s.canvasRevisions[canvas]; ok {
		t.Error("the deleted canvas's revisions survived")
	}
	if _, ok := s.canvasRevisions["CV-other"]; !ok {
		t.Error("another canvas's revisions were deleted")
	}
	if _, ok := s.canvasAccess["CV1\x00user\x00U1"]; ok {
		t.Error("the deleted canvas's access grant survived")
	}
}
