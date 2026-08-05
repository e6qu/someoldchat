package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// assignmentWorld gives a list its owner writes, a member who can read it, and
// a member who cannot — which is the whole of what assignment has to get right.
func assignmentWorld(t *testing.T) (context.Context, Messages, domain.ListID, domain.ListItemID) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	for _, id := range []domain.UserID{"U1", "U2", "U3"} {
		repository.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)})
	}
	messages := Messages{Store: repository}
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch tasks", "", "[]", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetListAccess(ctx, "T1", "U1", list.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	item, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"ship it"}]`)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, messages, list.ID, item.ID
}

// Assigning work tells the person it belongs to. An assignment nobody is told
// about is the same defect as a canvas shared in silence.
func TestAssigningAnItemTellsTheAssignee(t *testing.T) {
	ctx, messages, listID, itemID := assignmentWorld(t)
	due := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	assigned, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "U2", due)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.AssigneeID != "U2" || !assigned.DueAt.Equal(due) {
		t.Fatalf("assigned = %+v, want U2 and the due date", assigned)
	}
	told, err := messages.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(told.Items) != 1 || told.Items[0].ListItemID != itemID {
		t.Fatalf("activity = %+v, want one assignment", told.Items)
	}
	if !told.Items[0].SourceAvailable || told.Items[0].ListName != "Launch tasks" {
		t.Fatalf("item = %+v, want it reachable and named", told.Items[0])
	}
	if !told.Items[0].ListItem.DueAt.Equal(due) {
		t.Fatalf("row lost the due date: %+v", told.Items[0].ListItem)
	}
}

// Work cannot be given to someone who cannot open where it lives: they would be
// told about an item they can never reach, which is worse than not being
// assigned it.
func TestAssigningToSomeoneWhoCannotSeeTheListIsRefused(t *testing.T) {
	ctx, messages, listID, itemID := assignmentWorld(t)
	if _, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "U3", time.Time{}); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("assigning to a stranger = %v, want ErrInvalidList", err)
	}
	told, err := messages.Activity(ctx, "T1", "U3", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(told.Items) != 0 {
		t.Fatalf("the stranger was told: %+v err = %v", told.Items, err)
	}
}

// Only a change of hands is news. Re-saving a due date on work someone already
// holds is not an assignment, and assigning to yourself is not being told.
func TestOnlyAChangeOfHandsIsNews(t *testing.T) {
	ctx, messages, listID, itemID := assignmentWorld(t)
	if _, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "U2", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "U2", time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	told, err := messages.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(told.Items) != 1 {
		t.Fatalf("activity = %+v err = %v, want one item after a due-date change", told.Items, err)
	}

	// The owner assigning to themselves is told nothing.
	if _, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "U1", time.Time{}); err != nil {
		t.Fatal(err)
	}
	own, err := messages.Activity(ctx, "T1", "U1", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(own.Items) != 0 {
		t.Fatalf("the assigner was told about their own assignment: %+v err = %v", own.Items, err)
	}
}

// Clearing is how a mistaken assignment is undone, and an archived item is
// never overdue: telling someone finished work is late is noise dressed as
// urgency.
func TestAnAssignmentCanBeClearedAndArchivedWorkIsNotLate(t *testing.T) {
	ctx, messages, listID, itemID := assignmentWorld(t)
	if _, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "U2", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	cleared, err := messages.AssignListItem(ctx, "T1", "U1", listID, itemID, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.AssigneeID != "" || !cleared.DueAt.IsZero() {
		t.Fatalf("cleared = %+v, want no assignee and no due date", cleared)
	}
	late := domain.ListItem{DueAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !late.Overdue(time.Now()) {
		t.Fatal("a past due date is not reported as overdue")
	}
	late.Archived = true
	if late.Overdue(time.Now()) {
		t.Fatal("archived work is reported as overdue")
	}
}
