package grpc

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

// The admin.usergroups.* organization methods, carried on UserGroupsService.

func (r Remote) AdminRemoveUserGroupTeams(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, teams []domain.WorkspaceID) error {
	out, err := r.usergroups.AdminRemoveUserGroupTeams(ctx, &chatv1.AdminUserGroupTeamsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UsergroupId: string(id), TeamIds: workspaceStrings(teams)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "user group team removal")
}

func (r Remote) AdminCreateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, handle, description string, visible bool) (domain.UserGroup, error) {
	out, err := r.usergroups.AdminCreateUserGroup(ctx, &chatv1.AdminCreateUserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Handle: handle, Description: description, Visible: visible})
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) AdminFetchUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID) (domain.UserGroup, error) {
	out, err := r.usergroups.AdminFetchUserGroup(ctx, &chatv1.UserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id)})
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) AdminUpdateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, patch domain.UserGroupPatch) (domain.UserGroup, error) {
	out, err := r.usergroups.AdminUpdateUserGroup(ctx, &chatv1.AdminUpdateUserGroupRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id),
		Name: patch.Name, Handle: patch.Handle, Description: patch.Description, Visible: patch.Visible,
	})
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) AdminAddUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, users []domain.UserID) (domain.UserGroupMembershipResult, error) {
	out, err := r.usergroups.AdminAddUserGroupUsers(ctx, &chatv1.UserGroupUsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), Users: userStrings(users)})
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	return decodeProtoUserGroupMembership(out)
}

func (r Remote) AdminRemoveUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, users []domain.UserID) error {
	out, err := r.usergroups.AdminRemoveUserGroupUsers(ctx, &chatv1.UserGroupUsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), Users: userStrings(users)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "user group member removal")
}

func (r Remote) AdminUploadUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, file string) (domain.UserGroupMembershipResult, error) {
	out, err := r.usergroups.AdminUploadUserGroupUsers(ctx, &chatv1.AdminUploadUserGroupUsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), File: file})
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	return decodeProtoUserGroupMembership(out)
}

func (s *Server) AdminRemoveUserGroupTeams(ctx context.Context, input *chatv1.AdminUserGroupTeamsRequest) (*chatv1.MutationResponse, error) {
	teams := make([]domain.WorkspaceID, 0, len(input.GetTeamIds()))
	for _, value := range input.GetTeamIds() {
		teams = append(teams, domain.WorkspaceID(value))
	}
	if err := s.implementation.AdminRemoveUserGroupTeams(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUsergroupId()), teams); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminCreateUserGroup(ctx context.Context, input *chatv1.AdminCreateUserGroupRequest) (*chatv1.UserGroup, error) {
	value, err := s.implementation.AdminCreateUserGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetHandle(), input.GetDescription(), input.GetVisible())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(value), nil
}

func (s *Server) AdminFetchUserGroup(ctx context.Context, input *chatv1.UserGroupRequest) (*chatv1.UserGroup, error) {
	value, err := s.implementation.AdminFetchUserGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(value), nil
}

func (s *Server) AdminUpdateUserGroup(ctx context.Context, input *chatv1.AdminUpdateUserGroupRequest) (*chatv1.UserGroup, error) {
	value, err := s.implementation.AdminUpdateUserGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), domain.UserGroupPatch{
		Name: input.Name, Handle: input.Handle, Description: input.Description, Visible: input.Visible,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(value), nil
}

func (s *Server) AdminAddUserGroupUsers(ctx context.Context, input *chatv1.UserGroupUsersRequest) (*chatv1.UserGroupMembershipResult, error) {
	result, err := s.implementation.AdminAddUserGroupUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), userIDs(input.GetUsers()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroupMembership(result), nil
}

func (s *Server) AdminRemoveUserGroupUsers(ctx context.Context, input *chatv1.UserGroupUsersRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRemoveUserGroupUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), userIDs(input.GetUsers())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminUploadUserGroupUsers(ctx context.Context, input *chatv1.AdminUploadUserGroupUsersRequest) (*chatv1.UserGroupMembershipResult, error) {
	result, err := s.implementation.AdminUploadUserGroupUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), input.GetFile())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroupMembership(result), nil
}

func encodeProtoUserGroupMembership(value domain.UserGroupMembershipResult) *chatv1.UserGroupMembershipResult {
	result := &chatv1.UserGroupMembershipResult{Usergroup: encodeProtoUserGroup(value.Group), Succeeded: int64(value.Succeeded)}
	for _, refusal := range value.Invalid {
		result.Invalid = append(result.Invalid, &chatv1.UserGroupMemberRefusal{UserId: string(refusal.UserID), Reason: refusal.Reason})
	}
	return result
}

func decodeProtoUserGroupMembership(value *chatv1.UserGroupMembershipResult) (domain.UserGroupMembershipResult, error) {
	if value == nil || value.GetSucceeded() < 0 {
		return domain.UserGroupMembershipResult{}, errors.New("typed user group membership result is incomplete")
	}
	group, err := decodeProtoUserGroup(value.GetUsergroup())
	if err != nil {
		return domain.UserGroupMembershipResult{}, err
	}
	result := domain.UserGroupMembershipResult{Group: group, Succeeded: int(value.GetSucceeded()), Invalid: make([]domain.UserGroupMemberRefusal, 0, len(value.GetInvalid()))}
	for _, refusal := range value.GetInvalid() {
		if refusal.GetUserId() == "" || refusal.GetReason() == "" {
			return domain.UserGroupMembershipResult{}, errors.New("typed user group refusal is incomplete")
		}
		result.Invalid = append(result.Invalid, domain.UserGroupMemberRefusal{UserID: domain.UserID(refusal.GetUserId()), Reason: refusal.GetReason()})
	}
	return result, nil
}
