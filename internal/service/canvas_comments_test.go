package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// TestADeactivatedAuthorCannotDeleteTheirCanvasComment makes the
// workspace-membership guard on DeleteCanvasComment load-bearing. The store still
// recognises the deactivated user as the comment's author, so its author-only
// WHERE clause would still delete for them; only the service guard refuses a
// removed member. Without this the guard was shadowed by that clause and survived
// mutation — the same shape the list-item delete guard is proven against.
func TestADeactivatedAuthorCannotDeleteTheirCanvasComment(t *testing.T) {
	ctx, repository, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Under review", `{"type":"markdown","markdown":"the proposal"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	comment, err := messages.CommentOnCanvas(ctx, "T1", "U2", canvas.ID, "", "a remark")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserDeleted(ctx, "T1", "U2", true, events.Event{ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U2", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteCanvasComment(ctx, "T1", "U2", comment.ID); err == nil {
		t.Fatal("a deactivated author deleted their canvas comment")
	}
}

// Commenting needs read access, not write. A canvas shared for review that only
// its editors could discuss would make review impossible, which is the opposite
// of why it was shared.
func TestAReaderMayCommentWithoutBeingAbleToEdit(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Under review", `{"type":"markdown","markdown":"the proposal"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	// The reader cannot edit...
	if err := messages.EditCanvas(ctx, "T1", "U2", canvas.ID, `[{"operation":"replace","document_content":{"type":"markdown","markdown":"mine now"}}]`); err == nil {
		t.Fatal("a read-only member edited the canvas")
	}
	// ...and can still say what they think of it.
	comment, err := messages.CommentOnCanvas(ctx, "T1", "U2", canvas.ID, "s1", "this paragraph is wrong")
	if err != nil {
		t.Fatal(err)
	}
	page, err := messages.CanvasComments(ctx, "T1", "U1", canvas.ID, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Comments) != 1 || page.Comments[0].ID != comment.ID || page.Comments[0].SectionID != "s1" {
		t.Fatalf("comments = %+v, want the reader's anchored comment", page.Comments)
	}
}

// A comment belongs to whoever wrote it. An editor who could delete what others
// said about their own document would make the comments worth less than
// silence.
func TestOnlyTheAuthorDeletesTheirComment(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Under review", `{"type":"markdown","markdown":"the proposal"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	comment, err := messages.CommentOnCanvas(ctx, "T1", "U2", canvas.ID, "", "a remark")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteCanvasComment(ctx, "T1", "U1", comment.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the canvas owner deleted someone else's comment: %v", err)
	}
	if err := messages.DeleteCanvasComment(ctx, "T1", "U2", comment.ID); err != nil {
		t.Fatal(err)
	}
	page, err := messages.CanvasComments(ctx, "T1", "U1", canvas.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Comments) != 0 {
		t.Fatalf("comments after deletion = %+v err = %v, want none", page.Comments, err)
	}
}

// A member with no grant can neither read the comments nor add one, because a
// comment is part of the document's conversation.
func TestCommentsAreInvisibleWithoutAccessToTheCanvas(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Private", `{"type":"markdown","markdown":"secret"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CanvasComments(ctx, "T1", "U2", canvas.ID, domain.PageRequest{Limit: 10}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger read the comments: %v", err)
	}
	if _, err := messages.CommentOnCanvas(ctx, "T1", "U2", canvas.ID, "", "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger commented: %v", err)
	}
}

func TestACommentMustSaySomethingAndNotTooMuch(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Under review", `{"type":"markdown","markdown":"the proposal"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CommentOnCanvas(ctx, "T1", "U1", canvas.ID, "", "   "); !errors.Is(err, ErrInvalidCanvas) {
		t.Fatalf("an empty comment = %v, want ErrInvalidCanvas", err)
	}
	tooLong := strings.Repeat("x", domain.CanvasCommentLimit+1)
	if _, err := messages.CommentOnCanvas(ctx, "T1", "U1", canvas.ID, "", tooLong); !errors.Is(err, ErrInvalidCanvas) {
		t.Fatalf("an over-long comment = %v, want ErrInvalidCanvas", err)
	}
}
