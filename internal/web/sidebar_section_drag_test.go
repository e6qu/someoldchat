package web

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A dragged section lands before the section it is dropped on, or last; a
// target the member does not have leaves the order alone.
func TestPlaceSidebarSectionBefore(t *testing.T) {
	order := []domain.SidebarSectionID{"A", "B", "C"}
	for _, test := range []struct {
		id, before domain.SidebarSectionID
		want       []domain.SidebarSectionID
	}{
		{"C", "A", []domain.SidebarSectionID{"C", "A", "B"}},
		{"A", "C", []domain.SidebarSectionID{"B", "A", "C"}},
		{"A", "end", []domain.SidebarSectionID{"B", "C", "A"}},
		{"B", "B", []domain.SidebarSectionID{"A", "B", "C"}},
		{"B", "Zgone", []domain.SidebarSectionID{"A", "B", "C"}},
	} {
		if got := placeSidebarSectionBefore(append([]domain.SidebarSectionID(nil), order...), test.id, test.before); !reflect.DeepEqual(got, test.want) {
			t.Errorf("move %s before %s = %v, want %v", test.id, test.before, got, test.want)
		}
	}
}

// Dropping a section on another posts the move with the section it lands
// before, and the sidebar then lists the sections in that order.
func TestDraggingASectionReordersTheSidebar(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	for position, name := range []string{"First", "Second", "Third"} {
		section := domain.SidebarSection{ID: domain.SidebarSectionID("S" + name), WorkspaceID: "T1", UserID: "U1", Name: name, Position: position}
		if err := s.CreateSidebarSection(ctx, section); err != nil {
			t.Fatal(err)
		}
	}
	moved := postForm(t, mux, "/app/sidebar/sections/move?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "section_id": {"SThird"}, "before": {"SFirst"}}.Encode(), false)
	if moved.Code != http.StatusSeeOther {
		t.Fatalf("move answered %d: %s", moved.Code, moved.Body)
	}
	sections, err := s.SidebarSections(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, section := range sections {
		names = append(names, section.Name)
	}
	if want := []string{"Third", "First", "Second"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("sections = %v, want %v", names, want)
	}
}
