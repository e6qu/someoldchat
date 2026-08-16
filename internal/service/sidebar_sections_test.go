package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func sidebarWorld(t *testing.T) (context.Context, Messages, *memory.Store) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	return ctx, Messages{Store: s}, s
}

// A member builds their own sidebar sections, assigns channels, reorders both
// sections and their channels, and deletes a section — and none of it is
// visible to anyone else.
func TestSidebarSectionsAreTheMembersOwnOrderedGroups(t *testing.T) {
	ctx, messages, _ := sidebarWorld(t)
	alpha, err := messages.CreateSidebarSection(ctx, "T1", "U1", "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := messages.CreateSidebarSection(ctx, "T1", "U1", "Beta")
	if err != nil {
		t.Fatal(err)
	}
	// New sections append in order.
	sections, err := messages.SidebarSections(ctx, "T1", "U1")
	if err != nil || len(sections) != 2 || sections[0].ID != alpha.ID || sections[1].ID != beta.ID {
		t.Fatalf("sections = %+v err=%v, want Alpha then Beta", sections, err)
	}
	if err := messages.RenameSidebarSection(ctx, "T1", "U1", alpha.ID, "Priorities"); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetSidebarSectionCollapsed(ctx, "T1", "U1", alpha.ID, true); err != nil {
		t.Fatal(err)
	}
	// Two channels join alpha; the second is placed after the first.
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U1", "C1", alpha.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U1", "C2", alpha.ID, "C1"); err != nil {
		t.Fatal(err)
	}
	// Reorder the two channels: put C2 before C1 by placing C1 after C2.
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U1", "C1", alpha.ID, "C2"); err != nil {
		t.Fatal(err)
	}
	// Beta before Priorities.
	if err := messages.ReorderSidebarSections(ctx, "T1", "U1", []domain.SidebarSectionID{beta.ID, alpha.ID}); err != nil {
		t.Fatal(err)
	}
	sections, err = messages.SidebarSections(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[0].ID != beta.ID || sections[1].ID != alpha.ID {
		t.Fatalf("sections after reorder = %+v, want Beta then Priorities", sections)
	}
	priorities := sections[1]
	if priorities.Name != "Priorities" || !priorities.Collapsed {
		t.Fatalf("priorities = %+v, want renamed and collapsed", priorities)
	}
	if len(priorities.Conversations) != 2 || priorities.Conversations[0] != "C2" || priorities.Conversations[1] != "C1" {
		t.Fatalf("priorities channels = %v, want [C2 C1]", priorities.Conversations)
	}
	// Moving C1 to beta removes it from priorities.
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U1", "C1", beta.ID, ""); err != nil {
		t.Fatal(err)
	}
	sections, _ = messages.SidebarSections(ctx, "T1", "U1")
	if len(sections[0].Conversations) != 1 || sections[0].Conversations[0] != "C1" {
		t.Fatalf("beta channels = %v, want [C1]", sections[0].Conversations)
	}
	if len(sections[1].Conversations) != 1 || sections[1].Conversations[0] != "C2" {
		t.Fatalf("priorities channels after move = %v, want [C2]", sections[1].Conversations)
	}
	// Removing C2 from every section (empty section id) drops it to the default.
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U1", "C2", "", ""); err != nil {
		t.Fatal(err)
	}
	sections, _ = messages.SidebarSections(ctx, "T1", "U1")
	if len(sections[1].Conversations) != 0 {
		t.Fatalf("priorities after removal = %v, want none", sections[1].Conversations)
	}
	// Another member sees none of this.
	if other, err := messages.SidebarSections(ctx, "T1", "U2"); err != nil || len(other) != 0 {
		t.Fatalf("another member saw %+v err=%v, want none", other, err)
	}
	// Deleting beta drops its channel to the default; priorities remains.
	if err := messages.DeleteSidebarSection(ctx, "T1", "U1", beta.ID); err != nil {
		t.Fatal(err)
	}
	if sections, _ := messages.SidebarSections(ctx, "T1", "U1"); len(sections) != 1 || sections[0].ID != alpha.ID {
		t.Fatalf("sections after delete = %+v, want only Priorities", sections)
	}
}

func TestSidebarSectionValidationAndOwnership(t *testing.T) {
	ctx, messages, _ := sidebarWorld(t)
	if _, err := messages.CreateSidebarSection(ctx, "T1", "U1", "   "); !errors.Is(err, ErrInvalidSidebarSection) {
		t.Fatalf("empty name = %v, want ErrInvalidSidebarSection", err)
	}
	if _, err := messages.CreateSidebarSection(ctx, "T1", "U1", strings.Repeat("x", domain.SidebarSectionNameLimit+1)); !errors.Is(err, ErrInvalidSidebarSection) {
		t.Fatalf("over-long name = %v, want ErrInvalidSidebarSection", err)
	}
	section, err := messages.CreateSidebarSection(ctx, "T1", "U1", "Mine")
	if err != nil {
		t.Fatal(err)
	}
	// Another member cannot touch it; each answers as a missing one.
	if err := messages.RenameSidebarSection(ctx, "T1", "U2", section.ID, "Yours"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-member rename = %v, want not found", err)
	}
	if err := messages.DeleteSidebarSection(ctx, "T1", "U2", section.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-member delete = %v, want not found", err)
	}
	if err := messages.SetSidebarSectionCollapsed(ctx, "T1", "U2", section.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-member collapse = %v, want not found", err)
	}
	// The per-member section limit is enforced.
	for messages.sectionCount(ctx, t, "U1") < domain.SidebarSectionLimit {
		if _, err := messages.CreateSidebarSection(ctx, "T1", "U1", "Section"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := messages.CreateSidebarSection(ctx, "T1", "U1", "one too many"); !errors.Is(err, ErrInvalidSidebarSection) {
		t.Fatalf("past the limit = %v, want ErrInvalidSidebarSection", err)
	}
}

func (m Messages) sectionCount(ctx context.Context, t *testing.T, user domain.UserID) int {
	t.Helper()
	sections, err := m.SidebarSections(ctx, "T1", user)
	if err != nil {
		t.Fatal(err)
	}
	return len(sections)
}

// TestDeactivatedMembersLoseTheirSidebarGuards makes the workspace-membership
// guard load-bearing: the store scopes sections to their owner but records
// nothing about standing.
func TestDeactivatedMembersLoseTheirSidebarGuards(t *testing.T) {
	ctx, messages, s := sidebarWorld(t)
	section, err := messages.CreateSidebarSection(ctx, "T1", "U1", "Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SidebarSections(ctx, "T1", "U1"); err == nil {
		t.Fatal("a deactivated member read their sidebar sections")
	}
	if _, err := messages.CreateSidebarSection(ctx, "T1", "U1", "New"); err == nil {
		t.Fatal("a deactivated member created a section")
	}
	if err := messages.DeleteSidebarSection(ctx, "T1", "U1", section.ID); err == nil {
		t.Fatal("a deactivated member deleted a section")
	}
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U1", "C1", section.ID, ""); err == nil {
		t.Fatal("a deactivated member assigned a channel")
	}
	if err := messages.RenameSidebarSection(ctx, "T1", "U1", section.ID, "Renamed"); err == nil {
		t.Fatal("a deactivated member renamed a section")
	}
	if err := messages.SetSidebarSectionCollapsed(ctx, "T1", "U1", section.ID, true); err == nil {
		t.Fatal("a deactivated member collapsed a section")
	}
	if err := messages.SetSidebarSectionNotificationLevel(ctx, "T1", "U1", section.ID, domain.NotificationMute); err == nil {
		t.Fatal("a deactivated member set a section notification level")
	}
	if err := messages.ReorderSidebarSections(ctx, "T1", "U1", []domain.SidebarSectionID{section.ID}); err == nil {
		t.Fatal("a deactivated member reordered sections")
	}
}
