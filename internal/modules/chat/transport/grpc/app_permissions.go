package grpc

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

// The app access control adapters: admin.apps.permissions.*,
// admin.apps.mcp.servers.* and apps.managed.permissions.set.

func (r Remote) AdminAppPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.AppPermission, error) {
	out, err := r.apps.AdminAppPermission(ctx, &chatv1.AppAccessControlRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return domain.AppPermission{}, err
	}
	return decodeProtoAppPermission(out), nil
}

func (r Remote) AdminSetAppPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.AppPermission) (domain.AppPermission, error) {
	out, err := r.apps.AdminSetAppPermission(ctx, &chatv1.AppPermissionMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Permission: encodeProtoAppPermission(value),
	})
	if err != nil {
		return domain.AppPermission{}, err
	}
	return decodeProtoAppPermission(out), nil
}

func (r Remote) AdminAddAppPermissionEntities(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, change domain.AppPermissionChange) (domain.AppPermission, error) {
	out, err := r.apps.AdminAddAppPermissionEntities(ctx, encodeProtoAppPermissionChange(workspaceID, userID, change))
	if err != nil {
		return domain.AppPermission{}, err
	}
	return decodeProtoAppPermission(out), nil
}

func (r Remote) AdminRemoveAppPermissionEntities(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, change domain.AppPermissionChange) (domain.AppPermission, error) {
	out, err := r.apps.AdminRemoveAppPermissionEntities(ctx, encodeProtoAppPermissionChange(workspaceID, userID, change))
	if err != nil {
		return domain.AppPermission{}, err
	}
	return decodeProtoAppPermission(out), nil
}

func (r Remote) AdminMCPServers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.MCPServerPage, error) {
	out, err := r.apps.AdminMCPServers(ctx, &chatv1.MCPServersRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor),
		Descending: request.Descending,
	})
	if err != nil {
		return domain.MCPServerPage{}, err
	}
	page := domain.MCPServerPage{Servers: make([]domain.MCPServer, 0, len(out.GetServers())), NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}
	for _, server := range out.GetServers() {
		page.Servers = append(page.Servers, decodeProtoMCPServer(server))
	}
	return page, nil
}

func (r Remote) AdminAppMCPServerPermissions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) ([]domain.MCPServerAccess, error) {
	out, err := r.apps.AdminAppMCPServerPermissions(ctx, &chatv1.AppAccessControlRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return nil, err
	}
	access := make([]domain.MCPServerAccess, 0, len(out.GetServers()))
	for _, value := range out.GetServers() {
		access = append(access, domain.MCPServerAccess{
			Server: decodeProtoMCPServer(value.GetServer()), Permission: decodeProtoMCPServerPermission(value.GetPermission()),
		})
	}
	return access, nil
}

func (r Remote) AdminSetMCPServerPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.MCPServerPermission) (domain.MCPServerPermission, error) {
	out, err := r.apps.AdminSetMCPServerPermission(ctx, &chatv1.MCPServerPermissionMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Permission: encodeProtoMCPServerPermission(value),
	})
	if err != nil {
		return domain.MCPServerPermission{}, err
	}
	return decodeProtoMCPServerPermission(out), nil
}

func (r Remote) SetManagedAppPermissions(ctx context.Context, configurationToken string, appID domain.AppID, permission domain.ManagedAppPermission) error {
	out, err := r.apps.SetManagedAppPermissions(ctx, &chatv1.ManagedAppPermissionRequest{
		ConfigurationToken: configurationToken, AppId: string(appID), Permissions: string(permission),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed managed app permission change was not acknowledged")
	}
	return nil
}

func (s *Server) AdminAppPermission(ctx context.Context, input *chatv1.AppAccessControlRequest) (*chatv1.AppPermission, error) {
	value, err := s.implementation.AdminAppPermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppPermission(value), nil
}

func (s *Server) AdminSetAppPermission(ctx context.Context, input *chatv1.AppPermissionMutationRequest) (*chatv1.AppPermission, error) {
	value, err := s.implementation.AdminSetAppPermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeProtoAppPermission(input.GetPermission()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppPermission(value), nil
}

func (s *Server) AdminAddAppPermissionEntities(ctx context.Context, input *chatv1.AppPermissionChangeRequest) (*chatv1.AppPermission, error) {
	value, err := s.implementation.AdminAddAppPermissionEntities(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeProtoAppPermissionChange(input))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppPermission(value), nil
}

func (s *Server) AdminRemoveAppPermissionEntities(ctx context.Context, input *chatv1.AppPermissionChangeRequest) (*chatv1.AppPermission, error) {
	value, err := s.implementation.AdminRemoveAppPermissionEntities(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeProtoAppPermissionChange(input))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppPermission(value), nil
}

func (s *Server) AdminMCPServers(ctx context.Context, input *chatv1.MCPServersRequest) (*chatv1.MCPServerPage, error) {
	page, err := s.implementation.AdminMCPServers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.PageRequest{
		Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor()), Descending: input.GetDescending(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	servers := make([]*chatv1.MCPServer, 0, len(page.Servers))
	for _, server := range page.Servers {
		servers = append(servers, encodeProtoMCPServer(server))
	}
	return &chatv1.MCPServerPage{Servers: servers, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) AdminAppMCPServerPermissions(ctx context.Context, input *chatv1.AppAccessControlRequest) (*chatv1.MCPServerAccessResponse, error) {
	access, err := s.implementation.AdminAppMCPServerPermissions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	servers := make([]*chatv1.MCPServerAccess, 0, len(access))
	for _, value := range access {
		servers = append(servers, &chatv1.MCPServerAccess{
			Server: encodeProtoMCPServer(value.Server), Permission: encodeProtoMCPServerPermission(value.Permission),
		})
	}
	return &chatv1.MCPServerAccessResponse{Servers: servers}, nil
}

func (s *Server) AdminSetMCPServerPermission(ctx context.Context, input *chatv1.MCPServerPermissionMutationRequest) (*chatv1.MCPServerPermission, error) {
	value, err := s.implementation.AdminSetMCPServerPermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeProtoMCPServerPermission(input.GetPermission()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMCPServerPermission(value), nil
}

func (s *Server) SetManagedAppPermissions(ctx context.Context, input *chatv1.ManagedAppPermissionRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.SetManagedAppPermissions(ctx, input.GetConfigurationToken(), domain.AppID(input.GetAppId()), domain.ManagedAppPermission(input.GetPermissions())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func encodeProtoAppPermission(value domain.AppPermission) *chatv1.AppPermission {
	return &chatv1.AppPermission{
		AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), PermissionType: string(value.PermissionType),
		UserIds: idStrings(value.UserIDs), UsergroupIds: idStrings(value.UserGroupIDs),
		ChannelRestrictionMode: string(value.ChannelRestrictionMode), ChannelIds: idStrings(value.ChannelIDs),
		UpdatedAt: optionalUnixNano(value.UpdatedAt),
	}
}

func decodeProtoAppPermission(value *chatv1.AppPermission) domain.AppPermission {
	return domain.AppPermission{
		AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		PermissionType: domain.AppPermissionType(value.GetPermissionType()),
		UserIDs:        typedIDs[domain.UserID](value.GetUserIds()), UserGroupIDs: typedIDs[domain.UserGroupID](value.GetUsergroupIds()),
		ChannelRestrictionMode: domain.ChannelRestrictionMode(value.GetChannelRestrictionMode()),
		ChannelIDs:             typedIDs[domain.ConversationID](value.GetChannelIds()),
		UpdatedAt:              optionalTimeFromUnixNano(value.GetUpdatedAt()),
	}
}

func encodeProtoAppPermissionChange(workspaceID domain.WorkspaceID, userID domain.UserID, change domain.AppPermissionChange) *chatv1.AppPermissionChangeRequest {
	return &chatv1.AppPermissionChangeRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(change.AppID),
		UserIds: idStrings(change.UserIDs), UsergroupIds: idStrings(change.UserGroupIDs), ChannelIds: idStrings(change.ChannelIDs),
	}
}

func decodeProtoAppPermissionChange(input *chatv1.AppPermissionChangeRequest) domain.AppPermissionChange {
	return domain.AppPermissionChange{
		AppID: domain.AppID(input.GetAppId()), UserIDs: typedIDs[domain.UserID](input.GetUserIds()),
		UserGroupIDs: typedIDs[domain.UserGroupID](input.GetUsergroupIds()), ChannelIDs: typedIDs[domain.ConversationID](input.GetChannelIds()),
	}
}

func encodeProtoMCPServer(value domain.MCPServer) *chatv1.MCPServer {
	return &chatv1.MCPServer{Id: string(value.ID), AppId: string(value.AppID), Name: value.Name, Url: value.URL}
}

func decodeProtoMCPServer(value *chatv1.MCPServer) domain.MCPServer {
	return domain.MCPServer{ID: domain.MCPServerID(value.GetId()), AppID: domain.AppID(value.GetAppId()), Name: value.GetName(), URL: value.GetUrl()}
}

func encodeProtoMCPServerPermission(value domain.MCPServerPermission) *chatv1.MCPServerPermission {
	return &chatv1.MCPServerPermission{
		WorkspaceId: string(value.WorkspaceID), AppId: string(value.AppID), ServerId: string(value.ServerID),
		PermissionType: string(value.PermissionType), UserIds: idStrings(value.UserIDs), UsergroupIds: idStrings(value.UserGroupIDs),
		UpdatedAt: optionalUnixNano(value.UpdatedAt),
	}
}

func decodeProtoMCPServerPermission(value *chatv1.MCPServerPermission) domain.MCPServerPermission {
	return domain.MCPServerPermission{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()),
		ServerID: domain.MCPServerID(value.GetServerId()), PermissionType: domain.MCPServerPermissionType(value.GetPermissionType()),
		UserIDs: typedIDs[domain.UserID](value.GetUserIds()), UserGroupIDs: typedIDs[domain.UserGroupID](value.GetUsergroupIds()),
		UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAt()),
	}
}

// idStrings and typedIDs carry an identifier list across the wire. A decoded
// list is never nil, so an empty list reads the same in both compositions.
func idStrings[T ~string](values []T) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func typedIDs[T ~string](values []string) []T {
	result := make([]T, 0, len(values))
	for _, value := range values {
		result = append(result, T(value))
	}
	return result
}
