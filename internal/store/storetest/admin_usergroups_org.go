package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// OrganizationUserGroupRepository is the part of store.Store the organization
// user-group check drives.
type OrganizationUserGroupRepository interface {
	CreateUserGroup(context.Context, domain.UserGroup, events.Event) error
	GetUserGroup(context.Context, domain.WorkspaceID, domain.UserGroupID) (domain.UserGroup, error)
	ListUserGroups(context.Context, domain.WorkspaceID, bool, domain.PageRequest) (domain.UserGroupPage, error)
	UpdateUserGroup(context.Context, domain.UserGroup, events.Event) error
	ChangeUserGroupUsers(context.Context, domain.WorkspaceID, domain.UserGroupID, []domain.UserID, []domain.UserID, domain.UserID, events.Event) error
	ChangeUserGroupTeams(context.Context, domain.WorkspaceID, domain.UserGroupID, []domain.WorkspaceID, []domain.WorkspaceID, domain.UserID, events.Event) error
}

// CheckOrganizationUserGroups requires a group's organization properties to be
// durable and its membership and workspace deltas to change only what they
// name. The repository must hold workspaces T1 and T2, members U1 and UB of
// T1, and UX of T2.
func CheckOrganizationUserGroups(t *testing.T, repository OrganizationUserGroupRepository) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	event := func(id string) events.Event {
		value, err := events.New(domain.EventID(id), "T1", "U1", events.NewPayload("usergroup.updated", events.String("usergroup_id", "Sorg")), now)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	group := domain.UserGroup{
		WorkspaceID: "T1", ID: "Sorg", Name: "Org", Handle: "org", Creator: "U1", UpdatedBy: "U1",
		CreatedAt: now, UpdatedAt: now, Enabled: true, OrgLevel: true, Hidden: true,
	}
	if err := repository.CreateUserGroup(ctx, group, event("E-org-create")); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetUserGroup(ctx, "T1", "Sorg")
	if err != nil || !stored.OrgLevel || !stored.Hidden || len(stored.Teams) != 0 {
		t.Fatalf("created group %+v err=%v", stored, err)
	}

	if err := repository.ChangeUserGroupUsers(ctx, "T1", "Sorg", []domain.UserID{"U1", "UB", "UX", "U-nobody"}, nil, "U1", event("E-org-add")); err != nil {
		t.Fatal(err)
	}
	if err := repository.ChangeUserGroupUsers(ctx, "T1", "Sorg", []domain.UserID{"U1"}, []domain.UserID{"UB", "U-nobody"}, "U1", event("E-org-change")); err != nil {
		t.Fatalf("a delta that repeats a member and removes a non-member is not an error: %v", err)
	}
	stored, _ = repository.GetUserGroup(ctx, "T1", "Sorg")
	if !slices.Equal(stored.Users, []domain.UserID{"U1"}) {
		t.Fatalf("members %v, want only the workspace's member U1 left", stored.Users)
	}

	if err := repository.ChangeUserGroupTeams(ctx, "T1", "Sorg", []domain.WorkspaceID{"T1"}, nil, "U1", event("E-org-team")); err != nil {
		t.Fatal(err)
	}
	if err := repository.ChangeUserGroupTeams(ctx, "T1", "Sorg", []domain.WorkspaceID{"T1"}, nil, "U1", event("E-org-team-again")); err != nil {
		t.Fatalf("repeating an assignment is not an error: %v", err)
	}
	if err := repository.ChangeUserGroupTeams(ctx, "T1", "Sorg", []domain.WorkspaceID{"T-nowhere"}, nil, "U1", event("E-org-team-missing")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("assigning a workspace that does not exist err=%v", err)
	}
	stored, _ = repository.GetUserGroup(ctx, "T1", "Sorg")
	if !slices.Equal(stored.Teams, []domain.WorkspaceID{"T1"}) {
		t.Fatalf("teams %v", stored.Teams)
	}
	page, err := repository.ListUserGroups(ctx, "T1", true, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Groups) != 1 || !page.Groups[0].OrgLevel || !page.Groups[0].Hidden || !slices.Equal(page.Groups[0].Teams, []domain.WorkspaceID{"T1"}) {
		t.Fatalf("listed %+v err=%v", page.Groups, err)
	}

	// An update writes visibility and leaves membership, assignment and the
	// organization flag to their own writers.
	changed := stored
	changed.Hidden, changed.OrgLevel, changed.Teams, changed.Users = false, false, nil, nil
	changed.Description = "visible now"
	if err := repository.UpdateUserGroup(ctx, changed, event("E-org-update")); err != nil {
		t.Fatal(err)
	}
	stored, _ = repository.GetUserGroup(ctx, "T1", "Sorg")
	if stored.Hidden || !stored.OrgLevel || stored.Description != "visible now" ||
		!slices.Equal(stored.Teams, []domain.WorkspaceID{"T1"}) || !slices.Equal(stored.Users, []domain.UserID{"U1"}) {
		t.Fatalf("updated group %+v", stored)
	}

	if err := repository.ChangeUserGroupTeams(ctx, "T1", "Sorg", nil, []domain.WorkspaceID{"T1"}, "U1", event("E-org-release")); err != nil {
		t.Fatal(err)
	}
	if stored, _ = repository.GetUserGroup(ctx, "T1", "Sorg"); len(stored.Teams) != 0 {
		t.Fatalf("released teams %v", stored.Teams)
	}
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		id := domain.UserGroupID("S-nobody")
		if workspace == "T2" {
			id = "Sorg"
		}
		if err := repository.ChangeUserGroupUsers(ctx, workspace, id, []domain.UserID{"U1"}, nil, "U1", event("E-org-missing-"+string(workspace))); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("users of %s/%s err=%v", workspace, id, err)
		}
		if err := repository.ChangeUserGroupTeams(ctx, workspace, id, []domain.WorkspaceID{"T1"}, nil, "U1", event("E-org-missing-team-"+string(workspace))); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("teams of %s/%s err=%v", workspace, id, err)
		}
	}
}
