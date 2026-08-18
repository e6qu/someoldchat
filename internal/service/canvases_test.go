package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// canvasWorld gives an owner and a second member with no grant, because most of
// what a canvas search has to get right is who may see a result.
func canvasWorld(t *testing.T) (context.Context, *memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	return ctx, repository, Messages{Store: repository}
}

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
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"replace","title_content":{"title":"Revised plan"}},{"operation":"replace","document_content":{"type":"markdown","markdown":"One atomic revision"}}]`); err != nil {
		t.Fatal(err)
	}
	revised, err := store.GetCanvas(ctx, "T1", canvas.ID)
	if err != nil || revised.Title != "Revised plan" || revised.Version != 3 {
		t.Fatalf("revised canvas=%+v err=%v", revised, err)
	}
	sections, err = messages.LookupCanvasSections(ctx, "T1", "U1", canvas.ID, `{"contains_text":"Details"}`)
	if err != nil || len(sections) != 0 {
		t.Fatalf("replaced content still contains old section=%+v err=%v", sections, err)
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

// TestCanvasSectionMovePreservesIdentityAndOrder covers the reorder operation
// the block editor composes: a section moves before or after another, its own
// id survives the move (so comment anchors stay attached), and a move naming a
// missing or identical section is refused rather than silently dropped.
func TestCanvasSectionMovePreservesIdentityAndOrder(t *testing.T) {
	ctx := context.Background()
	backing := memory.New()
	backing.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	backing.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	messages := Messages{Store: backing}

	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Ordered", `{"type":"markdown","markdown":"first"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"second", "third"} {
		if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"`+text+`"}}]`); err != nil {
			t.Fatal(err)
		}
	}
	order := func() []domain.CanvasSection {
		sections, err := messages.LookupCanvasSections(ctx, "T1", "U1", canvas.ID, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		return sections
	}
	sections := order()
	if len(sections) != 3 || sections[0].Text != "first" || sections[2].Text != "third" {
		t.Fatalf("initial order=%+v", sections)
	}
	first, third := sections[0].ID, sections[2].ID

	// Move the first section to the end (after the third). Its id must survive.
	move := `[{"operation":"move_after","section_id":"` + first + `","target_section_id":"` + third + `"}]`
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, move); err != nil {
		t.Fatalf("move_after: %v", err)
	}
	moved := order()
	if len(moved) != 3 || moved[0].Text != "second" || moved[2].Text != "first" || moved[2].ID != first {
		t.Fatalf("after move_after=%+v (first id %s)", moved, first)
	}

	// Move it back to the very front (before what is now the first section).
	back := `[{"operation":"move_before","section_id":"` + first + `","target_section_id":"` + moved[0].ID + `"}]`
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, back); err != nil {
		t.Fatalf("move_before: %v", err)
	}
	if restored := order(); restored[0].ID != first || restored[0].Text != "first" {
		t.Fatalf("after move_before=%+v", restored)
	}

	// A move that names a missing target is a not-found, and a section cannot be
	// moved relative to itself.
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"move_after","section_id":"`+first+`","target_section_id":"nope"}]`); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing target = %v, want ErrNotFound", err)
	}
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"move_before","section_id":"`+first+`","target_section_id":"`+first+`"}]`); !errors.Is(err, ErrInvalidCanvas) {
		t.Fatalf("self move = %v, want ErrInvalidCanvas", err)
	}
}

func TestConversationCanvasIsSingularAndInheritsChannelAccess(t *testing.T) {
	ctx := context.Background()
	backing := memory.New()
	backing.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	backing.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	backing.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	backing.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	backing.SeedConversationMember("C1", "U1")
	backing.SeedConversationMember("C1", "U2")
	messages := Messages{Store: backing}

	canvas, err := messages.CreateConversationCanvas(ctx, "T1", "U1", "C1", "Channel notes", `{"type":"markdown","markdown":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ConversationCanvas(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatalf("channel member could not read its channel canvas: %v", err)
	}
	if err := messages.EditCanvas(ctx, "T1", "U2", canvas.ID, `[{"operation":"replace","document_content":{"type":"markdown","markdown":"updated"}}]`); err != nil {
		t.Fatalf("channel member could not edit its channel canvas: %v", err)
	}
	if _, err := messages.CreateConversationCanvas(ctx, "T1", "U1", "C1", "Duplicate", ""); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate error=%v, want %v", err, store.ErrAlreadyExists)
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

// Search finds a canvas by its prose, and stops exactly where the directory
// stops. The second half is the part worth a test: a search that matched more
// than the listing would disclose the title of a canvas the reader cannot open,
// which is a smaller leak than the document and the same kind.
func TestCanvasSearchMatchesProseAndRespectsAccess(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Deployment runbook", `{"type":"markdown","markdown":"roll back with the previous revision"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	page := domain.PageRequest{Limit: 10}
	found, err := messages.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "runbook", Page: page})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Canvases) != 1 || found.Canvases[0].ID != canvas.ID {
		t.Fatalf("search by title = %+v, want the canvas", found.Canvases)
	}

	// The body is searchable, and the one fold the product uses applies.
	body, err := messages.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "ROLL BACK", Page: page})
	if err != nil || len(body.Canvases) != 1 {
		t.Fatalf("search by body = %+v err = %v, want the canvas", body.Canvases, err)
	}

	// The document is stored as JSON. If the index were the stored bytes, a
	// member searching for "sections" would match every canvas in the
	// workspace, and one searching for a heading would miss it whenever the
	// text carried an escape.
	syntax, err := messages.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "sections", Page: page})
	if err != nil || len(syntax.Canvases) != 0 {
		t.Fatalf("search for JSON syntax = %+v err = %v, want nothing", syntax.Canvases, err)
	}

	stranger, err := messages.SearchCanvases(ctx, "T1", "U2", domain.CanvasSearchRequest{Query: "runbook", Page: page})
	if err != nil {
		t.Fatal(err)
	}
	if len(stranger.Canvases) != 0 {
		t.Fatalf("a member with no grant found %+v", stranger.Canvases)
	}
}

// A canvas is not in a conversation, so a conversation modifier has no meaning
// here. Refusing it is the honest answer; dropping it would return results that
// look like an answer to the question that was asked.
func TestCanvasSearchRefusesAModifierItCannotHonour(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	page := domain.PageRequest{Limit: 10}
	if _, err := messages.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "runbook in:#general", Page: page}); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("scoped canvas search = %v, want ErrInvalidSearch", err)
	}
	if _, err := messages.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "   ", Page: page}); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("empty canvas search = %v, want ErrInvalidSearch", err)
	}
}

// Sharing a canvas is news, and Activity is where a member finds out. The two
// silences matter as much as the item: re-granting access someone already has
// tells them nothing, and sharing with yourself is not being told anything.
func TestSharingACanvasReachesTheOtherMembersActivity(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Runbook", `{"type":"markdown","markdown":"steps"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	page, err := messages.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].CanvasID != canvas.ID {
		t.Fatalf("activity = %+v, want one canvas share", page.Items)
	}
	if !page.Items[0].SourceAvailable || page.Items[0].CanvasTitle != "Runbook" {
		t.Fatalf("item = %+v, want it reachable and named", page.Items[0])
	}

	// Re-granting is not news.
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "write", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	again, err := messages.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(again.Items) != 1 {
		t.Fatalf("activity after re-granting = %+v err = %v, want still one", again.Items, err)
	}

	// The owner is told nothing about their own share.
	own, err := messages.Activity(ctx, "T1", "U1", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(own.Items) != 0 {
		t.Fatalf("the sharer's activity = %+v err = %v, want none", own.Items, err)
	}
}

// The row survives the grant being withdrawn, and says the source is gone
// rather than offering a link that would refuse the reader.
func TestAWithdrawnCanvasShareStaysInActivityAsUnavailable(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Runbook", `{"type":"markdown","markdown":"steps"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteCanvasAccess(ctx, "T1", "U1", canvas.ID, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	page, err := messages.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("activity = %+v, want the row to survive", page.Items)
	}
	if page.Items[0].SourceAvailable {
		t.Fatalf("item = %+v, want it marked unreachable", page.Items[0])
	}
}
