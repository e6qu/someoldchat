package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func templateWorld(t *testing.T) (context.Context, *memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.UserID{"U1", "U2", "U3"} {
		if err := s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	seedWorkspaceAdmin(t, s, "T1", "U3")
	return ctx, s, Messages{Store: s}
}

func TestListTemplateLifecycle(t *testing.T) {
	ctx, _, messages := templateWorld(t)
	// U1 builds a small list with a row.
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch", "", `[{"key":"title","name":"Title","type":"text","is_primary_column":true}]`, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"ship it"}]`); err != nil {
		t.Fatal(err)
	}

	// U1 saves it as a template carrying its rows.
	template, err := messages.SaveListAsTemplate(ctx, "T1", "U1", list.ID, "Launch template", "", true)
	if err != nil || template.ID == "" || template.SeedItems == "[]" {
		t.Fatalf("save template=%+v err=%v", template, err)
	}

	// Any member sees it.
	templates, err := messages.WorkspaceListTemplates(ctx, "T1", "U2")
	if err != nil || len(templates) != 1 || templates[0].ID != template.ID {
		t.Fatalf("templates=%+v err=%v", templates, err)
	}

	// U2 instantiates a list from it — owned by U2, carrying the seeded row.
	made, err := messages.CreateListFromTemplate(ctx, "T1", "U2", template.ID, "My launch")
	if err != nil || made.OwnerID != "U2" || made.Name != "My launch" {
		t.Fatalf("made=%+v err=%v", made, err)
	}
	items, err := messages.ListItems(ctx, "T1", "U2", made.ID, domain.PageRequest{Limit: 10}, false)
	if err != nil || len(items.Items) != 1 || !strings.Contains(items.Items[0].Fields, "ship it") {
		t.Fatalf("seeded items=%+v err=%v", items.Items, err)
	}

	// A non-creator, non-admin cannot delete the template.
	if err := messages.DeleteListTemplate(ctx, "T1", "U2", template.ID); !errors.Is(err, ErrNotWorkspaceAdmin) {
		t.Fatalf("non-creator delete = %v, want ErrNotWorkspaceAdmin", err)
	}
	// A workspace administrator can, even though they did not create it.
	if err := messages.DeleteListTemplate(ctx, "T1", "U3", template.ID); err != nil {
		t.Fatalf("admin delete: %v", err)
	}
	if after, err := messages.WorkspaceListTemplates(ctx, "T1", "U2"); err != nil || len(after) != 0 {
		t.Fatalf("templates after delete=%+v err=%v", after, err)
	}
	// Deleting the template did not touch the list made from it.
	if _, err := messages.List(ctx, "T1", "U2", made.ID); err != nil {
		t.Fatalf("list made from a deleted template is gone: %v", err)
	}
}

func TestSaveListAsTemplateRefusesWithoutListAccess(t *testing.T) {
	ctx, _, messages := templateWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Private", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	// U2 has no access to U1's list, so cannot capture it as a template.
	if _, err := messages.SaveListAsTemplate(ctx, "T1", "U2", list.ID, "Sneaky", "", true); err == nil {
		t.Fatal("a member without list access saved someone else's list as a template")
	}
}

func TestListTemplateOperationsRefuseAStranger(t *testing.T) {
	ctx, _, messages := templateWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	template, err := messages.SaveListAsTemplate(ctx, "T1", "U1", list.ID, "Launch template", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// A stranger (not a workspace member) is refused every template operation.
	if _, err := messages.WorkspaceListTemplates(ctx, "T1", "U9"); err == nil {
		t.Fatal("stranger listed templates")
	}
	if _, err := messages.CreateListFromTemplate(ctx, "T1", "U9", template.ID, "x"); err == nil {
		t.Fatal("stranger created a list from a template")
	}
	if err := messages.DeleteListTemplate(ctx, "T1", "U9", template.ID); err == nil {
		t.Fatal("stranger deleted a template")
	}
}
