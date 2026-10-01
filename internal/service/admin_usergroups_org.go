package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The admin.usergroups.* organization methods.
//
// This deployment's organization is one workspace, the topology
// AdminAddUserGroupTeams and AdminSetConversationTeams already assert, so an
// organization group is a group of that workspace and an organization
// administrator is that workspace's administrator. Every method here requires
// one, as every other user-group mutation does: group membership is a key to
// channels restricted to the group.

// AdminCreateUserGroup creates an organization group. The handle is derived
// from the name when none is given, as usergroups.create derives it.
func (m Messages) AdminCreateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, name, handle, description string, visible bool) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	return m.createUserGroup(ctx, workspaceID, actor, domain.UserGroup{
		Name: name, Handle: handle, Description: description, OrgLevel: true, Hidden: !visible,
	})
}

// AdminFetchUserGroup reads one group for an administrator.
func (m Messages) AdminFetchUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	return m.Store.GetUserGroup(ctx, workspaceID, id)
}

// AdminUpdateUserGroup changes the properties patch names and leaves the rest.
// A visible group must keep a handle, since the handle is how a client
// mentions it.
func (m Messages) AdminUpdateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, patch domain.UserGroupPatch) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return domain.UserGroup{}, err
	}
	if patch.Visible != nil {
		value.Hidden = !*patch.Visible
	}
	if patch.Name != nil {
		if value.Name = strings.TrimSpace(*patch.Name); value.Name == "" {
			return domain.UserGroup{}, domain.ErrInvalidUserGroup
		}
	}
	if patch.Handle != nil {
		if strings.TrimSpace(*patch.Handle) == "" {
			// Every group here has a handle. Removing it is refused, and for a
			// visible group Slack's own code names why.
			if !value.Hidden {
				return domain.UserGroup{}, domain.ErrUserGroupNeedsHandle
			}
			return domain.UserGroup{}, domain.ErrInvalidUserGroup
		}
		valid := false
		if value.Handle, valid = userGroupHandle(*patch.Handle); !valid {
			return domain.UserGroup{}, domain.ErrInvalidUserGroup
		}
	}
	if patch.Description != nil {
		value.Description = strings.TrimSpace(*patch.Description)
	}
	return m.saveUserGroup(ctx, workspaceID, actor, value)
}

// AdminAddUserGroupUsers adds members to a group. A user who is not an active
// member of the organization fails the whole call with domain.ErrUserNotFound;
// a guest is listed as refused while the others are added, and a call in which
// every user is refused is domain.ErrInvalidUserGroupUsers.
func (m Messages) AdminAddUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, users []domain.UserID) (domain.UserGroupMembershipResult, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	named, err := normalizeAdminUserGroupUsers(users)
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	group, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	eligible := make([]domain.UserID, 0, len(named))
	refused := make([]domain.UserGroupMemberRefusal, 0)
	for _, userID := range named {
		membership, err := m.organizationMember(ctx, workspaceID, userID)
		if err != nil {
			return domain.UserGroupMembershipResult{}, err
		}
		if membership.Guest() {
			refused = append(refused, domain.UserGroupMemberRefusal{UserID: userID, Reason: domain.UserGroupRefusalGuest})
			continue
		}
		eligible = append(eligible, userID)
	}
	if len(eligible) == 0 {
		return domain.UserGroupMembershipResult{}, domain.ErrInvalidUserGroupUsers
	}
	return m.admitUserGroupUsers(ctx, workspaceID, actor, group, eligible, refused)
}

// AdminUploadUserGroupUsers adds the members a "member id, email" CSV names.
// Unlike AdminAddUserGroupUsers, a row naming nobody in the organization is
// listed as refused rather than failing the upload, because the method's
// contract reports every row it could not add and declares no user_not_found.
func (m Messages) AdminUploadUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, file string) (domain.UserGroupMembershipResult, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	rows, err := domain.ParseUserGroupCSV(file)
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	group, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	seen := make(map[domain.UserID]struct{}, len(rows))
	eligible := make([]domain.UserID, 0, len(rows))
	refused := make([]domain.UserGroupMemberRefusal, 0)
	for _, row := range rows {
		userID, membership, err := m.uploadedMember(ctx, workspaceID, row)
		if errors.Is(err, domain.ErrUserNotFound) {
			named := row.UserID
			if named == "" {
				named = domain.UserID(row.Email)
			}
			refused = append(refused, domain.UserGroupMemberRefusal{UserID: named, Reason: domain.UserGroupRefusalNotFound})
			continue
		}
		if err != nil {
			return domain.UserGroupMembershipResult{}, err
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		if membership.Guest() {
			refused = append(refused, domain.UserGroupMemberRefusal{UserID: userID, Reason: domain.UserGroupRefusalGuest})
			continue
		}
		eligible = append(eligible, userID)
	}
	if len(eligible) == 0 {
		return domain.UserGroupMembershipResult{}, domain.ErrNoValidUserGroupUsers
	}
	return m.admitUserGroupUsers(ctx, workspaceID, actor, group, eligible, refused)
}

// AdminRemoveUserGroupUsers removes members from a group. Every user named
// must be a user of the organization; one who is not in the group is already
// where the caller wants them, as a channel absent from a group is to
// RemoveUserGroupChannels.
func (m Messages) AdminRemoveUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, users []domain.UserID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	named, err := normalizeAdminUserGroupUsers(users)
	if err != nil {
		return err
	}
	group, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	for _, userID := range named {
		// A deactivated member may still be removed from a group; a user of no
		// account in this organization may not be named.
		user, err := m.Store.GetUser(ctx, userID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && user.WorkspaceID != workspaceID) {
			return domain.ErrUserNotFound
		}
		if err != nil {
			return err
		}
	}
	current := make(map[domain.UserID]struct{}, len(group.Users))
	for _, userID := range group.Users {
		current[userID] = struct{}{}
	}
	removed := make([]domain.UserID, 0, len(named))
	for _, userID := range named {
		if _, member := current[userID]; member {
			removed = append(removed, userID)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	return m.changeUserGroupUsers(ctx, workspaceID, actor, group, nil, removed)
}

// AdminRemoveUserGroupTeams releases a group from workspaces of the
// organization.
func (m Messages) AdminRemoveUserGroupTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, teams []domain.WorkspaceID) error {
	return m.changeUserGroupTeams(ctx, workspaceID, actor, id, teams, false)
}

func (m Messages) changeUserGroupTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, teams []domain.WorkspaceID, assign bool) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	group, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if len(teams) == 0 || len(teams) > domain.MaxAdminUserGroupTeams {
		return domain.ErrInvalidUserGroup
	}
	for _, team := range teams {
		// The organization's only workspace is the group's own; any other
		// identifier is a workspace outside it.
		if domain.WorkspaceID(strings.TrimSpace(string(team))) != workspaceID {
			return domain.ErrInvalidUserGroup
		}
	}
	assigned := len(group.Teams) == 1 && group.Teams[0] == workspaceID
	if assigned == assign {
		return nil
	}
	snapshot := group
	snapshot.UpdatedBy = actor
	snapshot.UpdatedAt = time.Now().UTC()
	var add, remove []domain.WorkspaceID
	if assign {
		add, snapshot.Teams = []domain.WorkspaceID{workspaceID}, []domain.WorkspaceID{workspaceID}
	} else {
		remove, snapshot.Teams = []domain.WorkspaceID{workspaceID}, nil
	}
	payload, err := userGroupEventPayload("usergroup.updated", snapshot, events.Strings("team_ids", workspaceIDStrings(snapshot.Teams)))
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actor, payload, snapshot.UpdatedAt)
	if err != nil {
		return err
	}
	return m.Store.ChangeUserGroupTeams(ctx, workspaceID, id, add, remove, actor, event)
}

// admitUserGroupUsers adds the eligible users who are not yet members and
// reports the group as it stands afterwards.
func (m Messages) admitUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, group domain.UserGroup, eligible []domain.UserID, refused []domain.UserGroupMemberRefusal) (domain.UserGroupMembershipResult, error) {
	current := make(map[domain.UserID]struct{}, len(group.Users))
	for _, userID := range group.Users {
		current[userID] = struct{}{}
	}
	added := make([]domain.UserID, 0, len(eligible))
	for _, userID := range eligible {
		if _, member := current[userID]; !member {
			added = append(added, userID)
		}
	}
	if len(added) > 0 {
		if err := m.changeUserGroupUsers(ctx, workspaceID, actor, group, added, nil); err != nil {
			return domain.UserGroupMembershipResult{}, err
		}
	}
	after, err := m.Store.GetUserGroup(ctx, workspaceID, group.ID)
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	return domain.UserGroupMembershipResult{Group: after, Succeeded: len(eligible), Invalid: refused}, nil
}

// changeUserGroupUsers stores a membership delta with the
// usergroup.users_changed event usergroups.users.update also emits.
func (m Messages) changeUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, group domain.UserGroup, add, remove []domain.UserID) error {
	members := make(map[domain.UserID]struct{}, len(group.Users)+len(add))
	for _, userID := range group.Users {
		members[userID] = struct{}{}
	}
	for _, userID := range add {
		members[userID] = struct{}{}
	}
	for _, userID := range remove {
		delete(members, userID)
	}
	snapshot := group
	snapshot.Users = make([]domain.UserID, 0, len(members))
	for userID := range members {
		snapshot.Users = append(snapshot.Users, userID)
	}
	sort.Slice(snapshot.Users, func(left, right int) bool { return snapshot.Users[left] < snapshot.Users[right] })
	snapshot.UpdatedBy = actor
	snapshot.UpdatedAt = time.Now().UTC()
	payload, err := userGroupEventPayload("usergroup.users_changed", snapshot,
		events.Strings("added_users", userIDStrings(add)),
		events.Strings("removed_users", userIDStrings(remove)),
	)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actor, payload, snapshot.UpdatedAt)
	if err != nil {
		return err
	}
	return m.Store.ChangeUserGroupUsers(ctx, workspaceID, group.ID, add, remove, actor, event)
}

// organizationMember is the membership of an active member of the
// organization, or domain.ErrUserNotFound for anybody else.
func (m Messages) organizationMember(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceMembership, error) {
	user, err := m.Store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (user.WorkspaceID != workspaceID || user.Deleted)) {
		return domain.WorkspaceMembership{}, domain.ErrUserNotFound
	}
	if err != nil {
		return domain.WorkspaceMembership{}, err
	}
	membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, userID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !membership.Active) {
		return domain.WorkspaceMembership{}, domain.ErrUserNotFound
	}
	return membership, err
}

// uploadedMember resolves one CSV row: by its member ID, or by its email
// address when the row gives no ID.
func (m Messages) uploadedMember(ctx context.Context, workspaceID domain.WorkspaceID, row domain.UserGroupCSVRow) (domain.UserID, domain.WorkspaceMembership, error) {
	userID := row.UserID
	if userID == "" {
		user, err := m.Store.FindUserByEmail(ctx, workspaceID, row.Email)
		if errors.Is(err, store.ErrNotFound) {
			return "", domain.WorkspaceMembership{}, domain.ErrUserNotFound
		}
		if err != nil {
			return "", domain.WorkspaceMembership{}, err
		}
		userID = user.ID
	}
	membership, err := m.organizationMember(ctx, workspaceID, userID)
	return userID, membership, err
}

// normalizeAdminUserGroupUsers trims and de-duplicates a users argument,
// refusing an empty list or one past the documented maximum.
func normalizeAdminUserGroupUsers(values []domain.UserID) ([]domain.UserID, error) {
	if len(values) == 0 || len(values) > domain.MaxAdminUserGroupUsers {
		return nil, domain.ErrInvalidUserGroup
	}
	seen := make(map[domain.UserID]struct{}, len(values))
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		value = domain.UserID(strings.TrimSpace(string(value)))
		if value == "" {
			return nil, domain.ErrInvalidUserGroup
		}
		if _, duplicate := seen[value]; !duplicate {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}
