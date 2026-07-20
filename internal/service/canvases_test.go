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
