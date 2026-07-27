package service

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestCanvasLifecycleAndSectionLookup(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: store}

	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Planning", `{"type":"h1","markdown":"Project plan"}`, "C1")
	if err != nil || canvas.ID == "" {
		t.Fatalf("create canvas=%+v err=%v", canvas, err)
	}
	if canvas.Title != "Planning" {
		t.Fatalf("canvas title=%q, want Planning", canvas.Title)
	}
	sections, err := messages.LookupCanvasSections(ctx, "T1", "U1", canvas.ID, `{"section_types":["h1"],"contains_text":"Project"}`)
	if err != nil || len(sections) != 1 || sections[0].Type != "h1" {
		t.Fatalf("sections=%+v err=%v", sections, err)
	}
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"insert_at_end","document_content":{"type":"paragraph","markdown":"Details"}}]`); err != nil {
		t.Fatal(err)
	}
	sections, err = messages.LookupCanvasSections(ctx, "T1", "U1", canvas.ID, `{"contains_text":"Details"}`)
	if err != nil || len(sections) != 1 {
		t.Fatalf("edited sections=%+v err=%v", sections, err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "write", nil, []domain.UserID{"U1"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteCanvasAccess(ctx, "T1", "U1", canvas.ID, nil, []domain.UserID{"U1"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteCanvas(ctx, "T1", "U1", canvas.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCanvas(ctx, "T1", canvas.ID); err == nil {
		t.Fatal("deleted canvas remained readable")
	}
}

// Access grants for canvases and lists were write-only: the store had setters and
// no reader, so every operation authorized on workspace membership alone and any
// member of the workspace could read, edit, or delete any other member's canvas
// or list. These assert the refusal, because an authorization check nothing
// exercises is indistinguishable from no check at all.
func TestAnotherMemberWithoutAGrantCannotReachACanvas(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	// U2 is a fully active member of the same workspace. Membership is exactly
	// the authority the defective check accepted.
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	messages := Messages{Store: store}

	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Planning", `{"type":"h1","markdown":"Project plan"}`, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := messages.EditCanvas(ctx, "T1", "U2", canvas.ID, `[{"operation":"replace","document_content":{"type":"h1","markdown":"seized"}}]`); err == nil {
		t.Fatal("a member with no grant edited another member's canvas")
	}
	if err := messages.DeleteCanvas(ctx, "T1", "U2", canvas.ID); err == nil {
		t.Fatal("a member with no grant deleted another member's canvas")
	}

	// The owner is not locked out by the check.
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"replace","document_content":{"type":"h1","markdown":"revised"}}]`); err != nil {
		t.Fatalf("the canvas owner was refused: %v", err)
	}
	if err := messages.DeleteCanvas(ctx, "T1", "U1", canvas.ID); err != nil {
		t.Fatalf("the canvas owner could not delete their own canvas: %v", err)
	}
}

func TestAnotherMemberWithoutAGrantCannotReachAList(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	messages := Messages{Store: store}

	list, err := messages.CreateList(ctx, "T1", "U1", "Roadmap", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"first"}]`); err != nil {
		t.Fatal(err)
	}

	if _, err := messages.ListItems(ctx, "T1", "U2", list.ID, domain.PageRequest{Limit: 10}, false); err == nil {
		t.Fatal("a member with no grant read another member's list items")
	}
	if _, err := messages.CreateListItem(ctx, "T1", "U2", list.ID, "", `[{"column_id":"title","value":"injected"}]`); err == nil {
		t.Fatal("a member with no grant wrote to another member's list")
	}

	if _, err := messages.ListItems(ctx, "T1", "U1", list.ID, domain.PageRequest{Limit: 10}, false); err != nil {
		t.Fatalf("the list owner was refused: %v", err)
	}
}
