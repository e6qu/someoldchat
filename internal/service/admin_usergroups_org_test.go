package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// orgUserGroupFixture is a workspace with an administrator UA, members U1 and
// U2, a multi-channel guest UG and a deactivated member UD, and one
// organization group made by UA.
func orgUserGroupFixture(t *testing.T) (Messages, *memory.Store, domain.UserGroup) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "org"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "UA", WorkspaceID: "T1", Name: "admin", Email: "admin@example.com"},
		{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com"},
		{ID: "U2", WorkspaceID: "T1", Name: "bob", Email: "bob@example.com"},
		{ID: "UD", WorkspaceID: "T1", Name: "gone", Email: "gone@example.com", Deleted: true},
		{ID: "UX", WorkspaceID: "T2", Name: "elsewhere", Email: "elsewhere@example.com"},
	} {
		if err := s.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	seedWorkspaceAdmin(t, s, "T1", "UA")
	guest := domain.User{ID: "UG", WorkspaceID: "T1", Name: "guest", Email: "guest@example.com"}
	if err := s.CreateUser(ctx, guest, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "UG", Role: domain.WorkspaceRoleMember, Active: true, Restricted: true},
		events.Event{ID: "E-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	group, err := messages.AdminCreateUserGroup(ctx, "T1", "UA", "Marketing Team", "", "Marketing gurus", false)
	if err != nil {
		t.Fatal(err)
	}
	return messages, s, group
}

func TestAdminCreateUserGroupMakesAnOrganizationGroup(t *testing.T) {
	ctx := context.Background()
	messages, _, group := orgUserGroupFixture(t)
	if !group.OrgLevel || !group.Hidden || group.Handle != "marketing-team" || group.Description != "Marketing gurus" || group.Creator != "UA" || !group.Enabled {
		t.Fatalf("created %+v", group)
	}
	fetched, err := messages.AdminFetchUserGroup(ctx, "T1", "UA", group.ID)
	if err != nil || !fetched.OrgLevel || !fetched.Hidden || fetched.Name != "Marketing Team" {
		t.Fatalf("fetched %+v err=%v", fetched, err)
	}
	if _, err := messages.AdminCreateUserGroup(ctx, "T1", "UA", "marketing team", "", "", true); !errors.Is(err, domain.ErrUserGroupNameTaken) {
		t.Fatalf("duplicate name err=%v", err)
	}
	if _, err := messages.AdminCreateUserGroup(ctx, "T1", "UA", "Other", "marketing-team", "", true); !errors.Is(err, domain.ErrUserGroupHandleTaken) {
		t.Fatalf("duplicate handle err=%v", err)
	}
	visible, err := messages.AdminCreateUserGroup(ctx, "T1", "UA", "Sales", "sales", "", true)
	if err != nil || visible.Hidden {
		t.Fatalf("visible create %+v err=%v", visible, err)
	}
	if _, err := messages.AdminCreateUserGroup(ctx, "T1", "U1", "Members", "", "", true); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member create err=%v", err)
	}
	if _, err := messages.AdminFetchUserGroup(ctx, "T1", "U1", group.ID); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member fetch err=%v", err)
	}
	if _, err := messages.AdminFetchUserGroup(ctx, "T1", "UA", "S-nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing fetch err=%v", err)
	}
	// A group usergroups.create makes is a workspace group.
	workspaceGroup, err := messages.CreateUserGroup(ctx, "T1", "UA", "Workspace", "", "", nil)
	if err != nil || workspaceGroup.OrgLevel || workspaceGroup.Hidden {
		t.Fatalf("workspace group %+v err=%v", workspaceGroup, err)
	}
}

func TestAdminUpdateUserGroupChangesOnlyWhatItNames(t *testing.T) {
	ctx := context.Background()
	messages, _, group := orgUserGroupFixture(t)
	name := "Marketing"
	updated, err := messages.AdminUpdateUserGroup(ctx, "T1", "UA", group.ID, domain.UserGroupPatch{Name: &name})
	if err != nil || updated.Name != "Marketing" || updated.Handle != "marketing-team" || updated.Description != "Marketing gurus" || !updated.Hidden {
		t.Fatalf("name only: %+v err=%v", updated, err)
	}
	visible, handle, empty := true, "@growth", ""
	updated, err = messages.AdminUpdateUserGroup(ctx, "T1", "UA", group.ID, domain.UserGroupPatch{Visible: &visible, Handle: &handle, Description: &empty})
	if err != nil || updated.Hidden || updated.Handle != "growth" || updated.Description != "" || !updated.OrgLevel {
		t.Fatalf("visible, handle, cleared description: %+v err=%v", updated, err)
	}
	if _, err := messages.AdminUpdateUserGroup(ctx, "T1", "UA", group.ID, domain.UserGroupPatch{Handle: &empty}); !errors.Is(err, domain.ErrUserGroupNeedsHandle) {
		t.Fatalf("removing a visible group's handle err=%v", err)
	}
	hidden := false
	if _, err := messages.AdminUpdateUserGroup(ctx, "T1", "UA", group.ID, domain.UserGroupPatch{Visible: &hidden, Handle: &empty}); !errors.Is(err, domain.ErrInvalidUserGroup) {
		t.Fatalf("removing a hidden group's handle err=%v", err)
	}
	if _, err := messages.AdminUpdateUserGroup(ctx, "T1", "UA", group.ID, domain.UserGroupPatch{Name: &empty}); !errors.Is(err, domain.ErrInvalidUserGroup) {
		t.Fatalf("empty name err=%v", err)
	}
	other, err := messages.AdminCreateUserGroup(ctx, "T1", "UA", "Sales", "sales", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AdminUpdateUserGroup(ctx, "T1", "UA", other.ID, domain.UserGroupPatch{Name: &name}); !errors.Is(err, domain.ErrUserGroupNameTaken) {
		t.Fatalf("taken name err=%v", err)
	}
	if _, err := messages.AdminUpdateUserGroup(ctx, "T1", "UA", "S-nobody", domain.UserGroupPatch{Name: &name}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing group err=%v", err)
	}
	if _, err := messages.AdminUpdateUserGroup(ctx, "T1", "U1", group.ID, domain.UserGroupPatch{Name: &name}); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member update err=%v", err)
	}
	// usergroups.update keeps the organization properties it does not name.
	kept, err := messages.UpdateUserGroup(ctx, "T1", "UA", group.ID, "Renamed", "", "", nil)
	if err != nil || !kept.OrgLevel || kept.Hidden || kept.Handle != "growth" {
		t.Fatalf("usergroups.update kept %+v err=%v", kept, err)
	}
}

func TestAdminAddUserGroupUsersAddsMembersAndListsTheRefused(t *testing.T) {
	ctx := context.Background()
	messages, s, group := orgUserGroupFixture(t)
	before := len(s.Outbox())
	result, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1", "UG", "U1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Group.Users, []domain.UserID{"U1"}) || result.Succeeded != 1 ||
		len(result.Invalid) != 1 || result.Invalid[0] != (domain.UserGroupMemberRefusal{UserID: "UG", Reason: "guest_user"}) {
		t.Fatalf("result %+v", result)
	}
	emitted := s.Outbox()[before:]
	if len(emitted) != 1 || emitted[0].Topic != "usergroup.users_changed" {
		t.Fatalf("events %+v", emitted)
	}
	// Adding a member again changes nothing and emits nothing.
	before = len(s.Outbox())
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1"}); err != nil || len(s.Outbox()) != before {
		t.Fatalf("repeat add err=%v events=%d", err, len(s.Outbox())-before)
	}
	for _, unknown := range []domain.UserID{"U-nobody", "UD", "UX"} {
		_, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U2", unknown})
		if !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("%s: err=%v, want user not found", unknown, err)
		}
	}
	if users, _ := messages.UserGroupUsers(ctx, "T1", "UA", group.ID, true); !slices.Equal(users, []domain.UserID{"U1"}) {
		t.Fatalf("a refused call changed the members: %v", users)
	}
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"UG"}); !errors.Is(err, domain.ErrInvalidUserGroupUsers) {
		t.Fatalf("only guests err=%v", err)
	}
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, nil); !errors.Is(err, domain.ErrInvalidUserGroup) {
		t.Fatalf("no users err=%v", err)
	}
	tooMany := make([]domain.UserID, domain.MaxAdminUserGroupUsers+1)
	for index := range tooMany {
		tooMany[index] = "U1"
	}
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, tooMany); !errors.Is(err, domain.ErrInvalidUserGroup) {
		t.Fatalf("over the maximum err=%v", err)
	}
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", "S-nobody", []domain.UserID{"U1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing group err=%v", err)
	}
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "U1", group.ID, []domain.UserID{"U2"}); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member add err=%v", err)
	}
}

func TestAdminUploadUserGroupUsersReadsTheCSV(t *testing.T) {
	ctx := context.Background()
	messages, _, group := orgUserGroupFixture(t)
	result, err := messages.AdminUploadUserGroupUsers(ctx, "T1", "UA", group.ID, "member id, email\nU1,alice@example.com\n,bob@example.com\nUG,\nU-nobody,\n,nobody@example.com\nU1,\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.UserGroupMemberRefusal{
		{UserID: "UG", Reason: "guest_user"},
		{UserID: "U-nobody", Reason: "user_not_found"},
		{UserID: "nobody@example.com", Reason: "user_not_found"},
	}
	if !slices.Equal(result.Group.Users, []domain.UserID{"U1", "U2"}) || result.Succeeded != 2 || !slices.Equal(result.Invalid, want) {
		t.Fatalf("result %+v", result)
	}
	if _, err := messages.AdminUploadUserGroupUsers(ctx, "T1", "UA", group.ID, "U1,a,b\n"); !errors.Is(err, domain.ErrUnparseableUserGroupFile) {
		t.Fatalf("three columns err=%v", err)
	}
	if _, err := messages.AdminUploadUserGroupUsers(ctx, "T1", "UA", group.ID, "\"U1,\n"); !errors.Is(err, domain.ErrUnparseableUserGroupFile) {
		t.Fatalf("broken quoting err=%v", err)
	}
	for _, file := range []string{"", "member id,email\n", "UG\nU-nobody\n"} {
		if _, err := messages.AdminUploadUserGroupUsers(ctx, "T1", "UA", group.ID, file); !errors.Is(err, domain.ErrNoValidUserGroupUsers) {
			t.Fatalf("%q err=%v", file, err)
		}
	}
	if _, err := messages.AdminUploadUserGroupUsers(ctx, "T1", "U1", group.ID, "U2\n"); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member upload err=%v", err)
	}
}

func TestAdminRemoveUserGroupUsersRemovesOnlyThoseNamed(t *testing.T) {
	ctx := context.Background()
	messages, s, group := orgUserGroupFixture(t)
	if _, err := messages.AdminAddUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1", "U2"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminRemoveUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1", "UA"}); err != nil {
		t.Fatal(err)
	}
	if users, _ := messages.UserGroupUsers(ctx, "T1", "UA", group.ID, true); !slices.Equal(users, []domain.UserID{"U2"}) {
		t.Fatalf("members %v", users)
	}
	// Removing somebody who is not a member is already done.
	before := len(s.Outbox())
	if err := messages.AdminRemoveUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1"}); err != nil || len(s.Outbox()) != before {
		t.Fatalf("repeat removal err=%v events=%d", err, len(s.Outbox())-before)
	}
	if err := messages.AdminRemoveUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U2", "U-nobody"}); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("unknown user err=%v", err)
	}
	if err := messages.AdminRemoveUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"UX"}); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("another organization's user err=%v", err)
	}
	if users, _ := messages.UserGroupUsers(ctx, "T1", "UA", group.ID, true); !slices.Equal(users, []domain.UserID{"U2"}) {
		t.Fatalf("a refused removal changed the members: %v", users)
	}
	if err := messages.AdminRemoveUserGroupUsers(ctx, "T1", "U1", group.ID, []domain.UserID{"U2"}); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member removal err=%v", err)
	}
	if err := messages.AdminRemoveUserGroupUsers(ctx, "T1", "UA", "S-nobody", []domain.UserID{"U2"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing group err=%v", err)
	}
}

func TestAdminUserGroupTeamsAreRecorded(t *testing.T) {
	ctx := context.Background()
	messages, s, group := orgUserGroupFixture(t)
	if err := messages.AdminAddUserGroupTeams(ctx, "T1", "UA", group.ID, []domain.WorkspaceID{"T1"}); err != nil {
		t.Fatal(err)
	}
	fetched, _ := messages.AdminFetchUserGroup(ctx, "T1", "UA", group.ID)
	if !slices.Equal(fetched.Teams, []domain.WorkspaceID{"T1"}) {
		t.Fatalf("assigned teams %v", fetched.Teams)
	}
	before := len(s.Outbox())
	if err := messages.AdminAddUserGroupTeams(ctx, "T1", "UA", group.ID, []domain.WorkspaceID{"T1"}); err != nil || len(s.Outbox()) != before {
		t.Fatalf("repeat assignment err=%v events=%d", err, len(s.Outbox())-before)
	}
	if err := messages.AdminRemoveUserGroupTeams(ctx, "T1", "UA", group.ID, []domain.WorkspaceID{"T1"}); err != nil {
		t.Fatal(err)
	}
	fetched, _ = messages.AdminFetchUserGroup(ctx, "T1", "UA", group.ID)
	if len(fetched.Teams) != 0 {
		t.Fatalf("released teams %v", fetched.Teams)
	}
	for _, teams := range [][]domain.WorkspaceID{nil, {"T2"}, {"T1", "T2"}} {
		if err := messages.AdminRemoveUserGroupTeams(ctx, "T1", "UA", group.ID, teams); !errors.Is(err, domain.ErrInvalidUserGroup) {
			t.Fatalf("%v err=%v", teams, err)
		}
	}
	if err := messages.AdminRemoveUserGroupTeams(ctx, "T1", "U1", group.ID, []domain.WorkspaceID{"T1"}); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member removal err=%v", err)
	}
	if err := messages.AdminRemoveUserGroupTeams(ctx, "T1", "UA", "S-nobody", []domain.WorkspaceID{"T1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing group err=%v", err)
	}
}
