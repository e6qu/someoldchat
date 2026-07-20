package grpc

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Remote struct {
	auth          chatv1.AuthServiceClient
	chat          chatv1.ChatServiceClient
	conversations chatv1.ConversationsServiceClient
	directory     chatv1.DirectoryServiceClient
	events        chatv1.EventsServiceClient
	files         chatv1.FilesServiceClient
	lists         chatv1.ListsServiceClient
	entity        chatv1.EntityServiceClient
	interactions  chatv1.InteractionsServiceClient
	messages      chatv1.MessagesServiceClient
	mutations     chatv1.ConversationMutationsServiceClient
	presence      chatv1.PresenceServiceClient
	reactions     chatv1.ReactionsServiceClient
	reminders     chatv1.RemindersServiceClient
	scheduled     chatv1.ScheduledMessagesServiceClient
	usergroups    chatv1.UserGroupsServiceClient
	calls         chatv1.CallsServiceClient
	audit         chatv1.AccessLogsServiceClient
	views         chatv1.ViewsServiceClient
	workflows     chatv1.WorkflowsServiceClient
	dialogs       chatv1.DialogsServiceClient
	bots          chatv1.BotsServiceClient
	migration     chatv1.MigrationServiceClient
	enterprise    chatv1.EnterpriseConversationsServiceClient
	bookmarks     chatv1.BookmarksServiceClient
	oauth         chatv1.OAuthServiceClient
	rtm           chatv1.RTMServiceClient
	canvases      chatv1.CanvasesServiceClient
}

// mappedClientConn preserves the domain error contract when an implementation
// is moved behind gRPC. The server maps store invariants to canonical status
// codes; the client maps those codes back to errors that callers can inspect
// with errors.Is while retaining the original gRPC status.
type mappedClientConn struct {
	grpc.ClientConnInterface
}

func (c mappedClientConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	return mapRemoteError(c.ClientConnInterface.Invoke(ctx, method, args, reply, opts...))
}

func (c mappedClientConn) NewStream(ctx context.Context, descriptor *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	stream, err := c.ClientConnInterface.NewStream(ctx, descriptor, method, opts...)
	if err != nil {
		return nil, mapRemoteError(err)
	}
	return mappedClientStream{ClientStream: stream}, nil
}

type mappedClientStream struct {
	grpc.ClientStream
}

func (s mappedClientStream) SendMsg(message any) error {
	return mapRemoteError(s.ClientStream.SendMsg(message))
}

func (s mappedClientStream) RecvMsg(message any) error {
	return mapRemoteError(s.ClientStream.RecvMsg(message))
}

type remoteDomainError struct {
	status *status.Status
	cause  error
}

func (e remoteDomainError) Error() string              { return e.status.Err().Error() }
func (e remoteDomainError) Unwrap() error              { return e.cause }
func (e remoteDomainError) GRPCStatus() *status.Status { return e.status }

func mapRemoteError(err error) error {
	if err == nil {
		return nil
	}
	remoteStatus, ok := status.FromError(err)
	if !ok {
		return err
	}
	var cause error
	switch remoteStatus.Code() {
	case codes.Canceled:
		cause = context.Canceled
	case codes.DeadlineExceeded:
		cause = context.DeadlineExceeded
	case codes.NotFound:
		cause = store.ErrNotFound
	case codes.AlreadyExists:
		cause = store.ErrAlreadyExists
	case codes.Aborted:
		cause = store.ErrConflict
	default:
		return err
	}
	return remoteDomainError{status: remoteStatus, cause: cause}
}

var _ chatapi.Service = Remote{}
var _ auth.TokenStore = Remote{}
var _ auth.AppTokenStore = Remote{}
var _ auth.TokenRevoker = Remote{}
var _ auth.SessionStore = Remote{}
var _ auth.SessionRevoker = Remote{}

func NewRemote(conn grpc.ClientConnInterface) (Remote, error) {
	if conn == nil {
		return Remote{}, errors.New("chat gRPC client requires a connection")
	}
	conn = mappedClientConn{ClientConnInterface: conn}
	return Remote{
		auth:          chatv1.NewAuthServiceClient(conn),
		chat:          chatv1.NewChatServiceClient(conn),
		conversations: chatv1.NewConversationsServiceClient(conn),
		directory:     chatv1.NewDirectoryServiceClient(conn),
		events:        chatv1.NewEventsServiceClient(conn),
		files:         chatv1.NewFilesServiceClient(conn),
		lists:         chatv1.NewListsServiceClient(conn),
		entity:        chatv1.NewEntityServiceClient(conn),
		interactions:  chatv1.NewInteractionsServiceClient(conn),
		messages:      chatv1.NewMessagesServiceClient(conn),
		mutations:     chatv1.NewConversationMutationsServiceClient(conn),
		presence:      chatv1.NewPresenceServiceClient(conn),
		reactions:     chatv1.NewReactionsServiceClient(conn),
		reminders:     chatv1.NewRemindersServiceClient(conn),
		scheduled:     chatv1.NewScheduledMessagesServiceClient(conn),
		usergroups:    chatv1.NewUserGroupsServiceClient(conn),
		calls:         chatv1.NewCallsServiceClient(conn),
		audit:         chatv1.NewAccessLogsServiceClient(conn),
		views:         chatv1.NewViewsServiceClient(conn),
		workflows:     chatv1.NewWorkflowsServiceClient(conn),
		dialogs:       chatv1.NewDialogsServiceClient(conn),
		bots:          chatv1.NewBotsServiceClient(conn),
		migration:     chatv1.NewMigrationServiceClient(conn),
		enterprise:    chatv1.NewEnterpriseConversationsServiceClient(conn),
		bookmarks:     chatv1.NewBookmarksServiceClient(conn),
		oauth:         chatv1.NewOAuthServiceClient(conn),
		rtm:           chatv1.NewRTMServiceClient(conn),
		canvases:      chatv1.NewCanvasesServiceClient(conn),
	}, nil
}

func (r Remote) CreateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, handle, description string) (domain.UserGroup, error) {
	out, err := r.usergroups.CreateUserGroup(ctx, &chatv1.CreateUserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Handle: handle, Description: description})
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) UpdateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, name, handle, description string) (domain.UserGroup, error) {
	out, err := r.usergroups.UpdateUserGroup(ctx, &chatv1.UpdateUserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), Name: name, Handle: handle, Description: description})
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) SetUserGroupEnabled(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, enabled bool) (domain.UserGroup, error) {
	in := &chatv1.UserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id)}
	var out *chatv1.UserGroup
	var err error
	if enabled {
		out, err = r.usergroups.EnableUserGroup(ctx, in)
	} else {
		out, err = r.usergroups.DisableUserGroup(ctx, in)
	}
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) ListUserGroups(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, includeDisabled bool, request domain.PageRequest) (domain.UserGroupPage, error) {
	out, err := r.usergroups.UserGroups(ctx, &chatv1.UserGroupsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), IncludeDisabled: includeDisabled, Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.UserGroupPage{}, err
	}
	result := make([]domain.UserGroup, 0, len(out.GetUsergroups()))
	for _, item := range out.GetUsergroups() {
		value, err := decodeProtoUserGroup(item)
		if err != nil {
			return domain.UserGroupPage{}, err
		}
		result = append(result, value)
	}
	return domain.UserGroupPage{Groups: result, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) UserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID) ([]domain.UserID, error) {
	out, err := r.usergroups.UserGroupUsers(ctx, &chatv1.UserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.UserID, 0, len(out.GetUsers()))
	for _, item := range out.GetUsers() {
		result = append(result, domain.UserID(item))
	}
	return result, nil
}

func (r Remote) SetUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, users []domain.UserID) (domain.UserGroup, error) {
	values := make([]string, 0, len(users))
	for _, item := range users {
		values = append(values, string(item))
	}
	out, err := r.usergroups.SetUserGroupUsers(ctx, &chatv1.UserGroupUsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), Users: values})
	if err != nil {
		return domain.UserGroup{}, err
	}
	return decodeProtoUserGroup(out)
}

func (r Remote) UserGroupChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID) ([]domain.ConversationID, error) {
	out, err := r.usergroups.UserGroupChannels(ctx, &chatv1.UserGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.ConversationID, 0, len(out.GetChannels()))
	for _, value := range out.GetChannels() {
		result = append(result, domain.ConversationID(value))
	}
	return result, nil
}
func (r Remote) AddUserGroupChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, channels []domain.ConversationID) error {
	values := make([]string, 0, len(channels))
	for _, value := range channels {
		values = append(values, string(value))
	}
	out, err := r.usergroups.AddUserGroupChannels(ctx, &chatv1.UserGroupChannelsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), Channels: values})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "user group channel add")
}
func (r Remote) RemoveUserGroupChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, channels []domain.ConversationID) error {
	values := make([]string, 0, len(channels))
	for _, value := range channels {
		values = append(values, string(value))
	}
	out, err := r.usergroups.RemoveUserGroupChannels(ctx, &chatv1.UserGroupChannelsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserGroupId: string(id), Channels: values})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "user group channel remove")
}

func (r Remote) AdminAddUserGroupTeams(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.UserGroupID, teams []domain.WorkspaceID) error {
	values := make([]string, 0, len(teams))
	for _, value := range teams {
		values = append(values, string(value))
	}
	out, err := r.usergroups.AdminAddUserGroupTeams(ctx, &chatv1.AdminUserGroupTeamsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UsergroupId: string(id), TeamIds: values})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "user group team add")
}

func (r Remote) AddCall(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, externalUniqueID, externalDisplayID, joinURL, desktopAppJoinURL, title string, startedAt time.Time, participants []domain.UserID) (domain.Call, error) {
	users := make([]string, 0, len(participants))
	for _, value := range participants {
		users = append(users, string(value))
	}
	out, err := r.calls.AddCall(ctx, &chatv1.AddCallRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ExternalUniqueId: externalUniqueID, ExternalDisplayId: externalDisplayID, JoinUrl: joinURL, DesktopAppJoinUrl: desktopAppJoinURL, Title: title, StartedAt: startedAt.Unix(), Participants: users})
	if err != nil {
		return domain.Call{}, err
	}
	return decodeProtoCall(out)
}
func (r Remote) GetCall(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CallID) (domain.Call, error) {
	out, err := r.calls.CallInfo(ctx, &chatv1.CallRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CallId: string(id)})
	if err != nil {
		return domain.Call{}, err
	}
	return decodeProtoCall(out)
}
func (r Remote) UpdateCall(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CallID, title, joinURL, desktopAppJoinURL string) (domain.Call, error) {
	out, err := r.calls.UpdateCall(ctx, &chatv1.UpdateCallRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CallId: string(id), Title: title, JoinUrl: joinURL, DesktopAppJoinUrl: desktopAppJoinURL})
	if err != nil {
		return domain.Call{}, err
	}
	return decodeProtoCall(out)
}
func (r Remote) EndCall(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CallID, duration int64) error {
	out, err := r.calls.EndCall(ctx, &chatv1.EndCallRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CallId: string(id), DurationSeconds: duration})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed call end was not acknowledged")
	}
	return nil
}
func (r Remote) AddCallParticipants(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CallID, participants []domain.UserID) error {
	return r.callParticipants(ctx, true, workspaceID, userID, id, participants)
}
func (r Remote) RemoveCallParticipants(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CallID, participants []domain.UserID) error {
	return r.callParticipants(ctx, false, workspaceID, userID, id, participants)
}
func (r Remote) callParticipants(ctx context.Context, add bool, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CallID, participants []domain.UserID) error {
	users := make([]string, 0, len(participants))
	for _, value := range participants {
		users = append(users, string(value))
	}
	in := &chatv1.CallParticipantsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CallId: string(id), Participants: users}
	var out *chatv1.MutationResponse
	var err error
	if add {
		out, err = r.calls.AddCallParticipants(ctx, in)
	} else {
		out, err = r.calls.RemoveCallParticipants(ctx, in)
	}
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed call participant mutation was not acknowledged")
	}
	return nil
}

func (r Remote) Post(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, text string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	in := &chatv1.PostRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Text: text, ThreadTimestamp: string(threadTimestamp), IdempotencyKey: idempotencyKey}
	out, err := r.messages.Post(ctx, in)
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) Unfurl(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, unfurls map[string]string) (domain.Message, error) {
	out, err := r.messages.Unfurl(ctx, &chatv1.UnfurlRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Unfurls: unfurls})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) PostEphemeral(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, recipientID domain.UserID, text string) (domain.EphemeralMessage, error) {
	return r.PostEphemeralWithBlocks(ctx, workspaceID, userID, conversationID, recipientID, text, "")
}

func (r Remote) RecordAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ip, userAgent string) error {
	out, err := r.audit.RecordAccess(ctx, &chatv1.RecordAccessRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Ip: ip, UserAgent: userAgent})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed access recording was not acknowledged")
	}
	return nil
}
func (r Remote) ListAccessLogs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, before time.Time, limit, page int) ([]domain.AccessLog, bool, error) {
	input := &chatv1.AccessLogsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(limit), Page: int32(page)}
	if !before.IsZero() {
		input.Before = before.Unix()
	}
	out, err := r.audit.AccessLogs(ctx, input)
	if err != nil {
		return nil, false, err
	}
	result := make([]domain.AccessLog, 0, len(out.GetLogs()))
	for _, item := range out.GetLogs() {
		value, err := decodeProtoAccessLog(item)
		if err != nil {
			return nil, false, err
		}
		result = append(result, value)
	}
	return result, out.GetHasMore(), nil
}

func (r Remote) IntegrationLogs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID, changeType, serviceID, userFilter string, count, page int) (domain.IntegrationLogPage, error) {
	out, err := r.audit.IntegrationLogs(ctx, &chatv1.IntegrationLogsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: appID, ChangeType: changeType, ServiceId: serviceID, UserFilter: userFilter, Count: int32(count), Page: int32(page)})
	if err != nil {
		return domain.IntegrationLogPage{}, err
	}
	result := domain.IntegrationLogPage{Page: int(out.GetPage()), Pages: int(out.GetPages()), Total: int(out.GetTotal()), Logs: make([]domain.IntegrationLog, 0, len(out.GetLogs()))}
	for _, item := range out.GetLogs() {
		result.Logs = append(result.Logs, domain.IntegrationLog{AppID: domain.AppID(item.GetAppId()), AppType: item.GetAppType(), ChangeType: item.GetChangeType(), ChannelID: domain.ConversationID(item.GetChannelId()), Date: time.Unix(item.GetDateUnix(), 0).UTC(), Scope: item.GetScope(), ServiceID: item.GetServiceId(), ServiceType: item.GetServiceType(), UserID: domain.UserID(item.GetUserId()), UserName: item.GetUserName()})
	}
	return result, nil
}

func (r Remote) CreateRTMConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.RTMConnection, error) {
	out, err := r.rtm.CreateConnection(ctx, &chatv1.RTMConnectionRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.RTMConnection{}, err
	}
	return decodeProtoRTMConnection(out), nil
}

func (r Remote) ConsumeRTMConnection(ctx context.Context, id string) (domain.RTMConnection, error) {
	out, err := r.rtm.ConsumeConnection(ctx, &chatv1.RTMConnectionIDRequest{Id: id})
	if err != nil {
		return domain.RTMConnection{}, err
	}
	return decodeProtoRTMConnection(out), nil
}

func (r Remote) CreateSocketModeConnection(ctx context.Context, value domain.SocketModeConnection) error {
	_, err := r.rtm.CreateSocketModeConnection(ctx, &chatv1.SocketModeConnectionRequest{AppId: string(value.AppID), Id: value.ID, ExpiresAtUnixNano: value.ExpiresAt.UnixNano()})
	return err
}

func (r Remote) ConsumeSocketModeConnection(ctx context.Context, id string) (domain.SocketModeConnection, error) {
	out, err := r.rtm.ConsumeSocketModeConnection(ctx, &chatv1.RTMConnectionIDRequest{Id: id})
	if err != nil {
		return domain.SocketModeConnection{}, err
	}
	return domain.SocketModeConnection{ID: out.GetId(), AppID: domain.AppID(out.GetAppId()), ExpiresAt: time.Unix(0, out.GetExpiresAtUnixNano()).UTC()}, nil
}

func (r Remote) RenewSocketModeConnection(ctx context.Context, id string, expiresAt time.Time) error {
	_, err := r.rtm.RenewSocketModeConnection(ctx, &chatv1.SocketModeConnectionRenewalRequest{Id: id, ExpiresAtUnixNano: expiresAt.UTC().UnixNano()})
	return err
}

func (r Remote) ReleaseSocketModeConnection(ctx context.Context, id string) error {
	_, err := r.rtm.ReleaseSocketModeConnection(ctx, &chatv1.RTMConnectionIDRequest{Id: id})
	return err
}

func (r Remote) CountSocketModeConnections(ctx context.Context, appID domain.AppID) (int, error) {
	out, err := r.rtm.CountSocketModeConnections(ctx, &chatv1.SocketModeCursorRequest{AppId: string(appID)})
	if err != nil {
		return 0, err
	}
	return int(out.GetCount()), nil
}

func (r Remote) GetSocketModeCursor(ctx context.Context, appID domain.AppID) (uint64, error) {
	out, err := r.rtm.GetSocketModeCursor(ctx, &chatv1.SocketModeCursorRequest{AppId: string(appID)})
	if err != nil {
		return 0, err
	}
	return out.GetSequence(), nil
}

func (r Remote) SetSocketModeCursor(ctx context.Context, appID domain.AppID, cursor uint64) error {
	_, err := r.rtm.SetSocketModeCursor(ctx, &chatv1.SocketModeCursorRequest{AppId: string(appID), Sequence: cursor})
	return err
}

func (r Remote) RecordSocketModeResponse(ctx context.Context, value domain.SocketModeResponse) error {
	_, err := r.rtm.RecordSocketModeResponse(ctx, &chatv1.SocketModeResponseRequest{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID, Payload: value.Payload, ReceivedAtUnixNano: value.ReceivedAt.UTC().UnixNano()})
	return err
}

func (r Remote) ClaimSocketModeResponses(ctx context.Context, appID domain.AppID, owner string, limit int, lease time.Duration) ([]domain.SocketModeResponse, error) {
	out, err := r.rtm.ClaimSocketModeResponses(ctx, &chatv1.SocketModeResponseLeaseRequest{AppId: string(appID), Owner: owner, Limit: int32(limit), LeaseNanos: lease.Nanoseconds()})
	if err != nil {
		return nil, err
	}
	return decodeSocketModeResponses(out.GetResponses()), nil
}

func (r Remote) RenewSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, lease time.Duration) error {
	keys := make([]*chatv1.SocketModeResponseKey, 0, len(values))
	for _, value := range values {
		keys = append(keys, &chatv1.SocketModeResponseKey{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID})
	}
	_, err := r.rtm.RenewSocketModeResponses(ctx, &chatv1.SocketModeResponseRenewRequest{Owner: owner, Responses: keys, LeaseNanos: lease.Nanoseconds()})
	return err
}

func (r Remote) AckSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse) error {
	keys := make([]*chatv1.SocketModeResponseKey, 0, len(values))
	for _, value := range values {
		keys = append(keys, &chatv1.SocketModeResponseKey{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID})
	}
	_, err := r.rtm.AckSocketModeResponses(ctx, &chatv1.SocketModeResponseAckRequest{Owner: owner, Responses: keys})
	return err
}

func (r Remote) ReleaseSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, retryAt time.Time) error {
	keys := make([]*chatv1.SocketModeResponseKey, 0, len(values))
	for _, value := range values {
		keys = append(keys, &chatv1.SocketModeResponseKey{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID})
	}
	_, err := r.rtm.ReleaseSocketModeResponses(ctx, &chatv1.SocketModeResponseReleaseRequest{Owner: owner, Responses: keys, RetryAtUnixNano: retryAt.UTC().UnixNano()})
	return err
}

func decodeSocketModeResponses(values []*chatv1.SocketModeResponse) []domain.SocketModeResponse {
	result := make([]domain.SocketModeResponse, 0, len(values))
	for _, value := range values {
		result = append(result, domain.SocketModeResponse{AppID: domain.AppID(value.GetAppId()), EnvelopeID: value.GetEnvelopeId(), Payload: value.GetPayload(), ReceivedAt: time.Unix(0, value.GetReceivedAtUnixNano()).UTC()})
	}
	return result
}

func (r Remote) Update(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, text string) (domain.Message, error) {
	in := &chatv1.UpdateRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Text: text}
	out, err := r.messages.Update(ctx, in)
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) Delete(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.Message, error) {
	in := &chatv1.DeleteRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)}
	out, err := r.messages.Delete(ctx, in)
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) Permalink(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (string, error) {
	in := &chatv1.PermalinkRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)}
	out, err := r.messages.Permalink(ctx, in)
	if err != nil {
		return "", err
	}
	if out.GetPermalink() == "" {
		return "", errors.New("typed permalink response is incomplete")
	}
	return out.GetPermalink(), nil
}

func (r Remote) History(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	in := &chatv1.HistoryRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.messages.History(ctx, in)
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
}

func (r Remote) Search(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	in := &chatv1.SearchRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Query: query, Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.messages.Search(ctx, in)
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
}

func (r Remote) UploadFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, title, mimeType string, size int64, source io.Reader) (domain.File, error) {
	if source == nil {
		return domain.File{}, errors.New("file upload requires a source")
	}
	header := &chatv1.UploadFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Title: title, MimeType: mimeType, Size: size}
	stream, err := r.chat.UploadFile(ctx)
	if err != nil {
		return domain.File{}, err
	}
	if err := stream.Send(&chatv1.UploadFilePart{Part: &chatv1.UploadFilePart_Metadata{Metadata: header}}); err != nil {
		return domain.File{}, err
	}
	buffer := make([]byte, 64<<10)
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			chunk := &chatv1.UploadFilePart{Part: &chatv1.UploadFilePart_Chunk{Chunk: append([]byte(nil), buffer[:read]...)}}
			if err := stream.Send(chunk); err != nil {
				return domain.File{}, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return domain.File{}, readErr
		}
		if read == 0 {
			return domain.File{}, errors.New("file source returned no data or error")
		}
	}
	if err := stream.CloseSend(); err != nil {
		return domain.File{}, err
	}
	result, err := stream.CloseAndRecv()
	if err != nil {
		return domain.File{}, err
	}
	return decodeProtoFile(result)
}

func (r Remote) SetUserPhoto(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, mimeType string, size int64, source io.Reader) (domain.User, error) {
	if source == nil {
		return domain.User{}, errors.New("user photo upload requires a source")
	}
	stream, err := r.chat.UploadUserPhoto(ctx)
	if err != nil {
		return domain.User{}, err
	}
	if err := stream.Send(&chatv1.UserPhotoUploadPart{Part: &chatv1.UserPhotoUploadPart_Metadata{Metadata: &chatv1.UserPhotoUploadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), MimeType: mimeType, Size: size}}}); err != nil {
		return domain.User{}, err
	}
	buffer := make([]byte, 64<<10)
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			if err := stream.Send(&chatv1.UserPhotoUploadPart{Part: &chatv1.UserPhotoUploadPart_Chunk{Chunk: append([]byte(nil), buffer[:read]...)}}); err != nil {
				return domain.User{}, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return domain.User{}, readErr
		}
		if read == 0 {
			return domain.User{}, errors.New("user photo source returned no data or error")
		}
	}
	if err := stream.CloseSend(); err != nil {
		return domain.User{}, err
	}
	out, err := stream.CloseAndRecv()
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) DeleteUserPhoto(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	out, err := r.chat.DeleteUserPhoto(ctx, &chatv1.UserPhotoDeleteRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed user photo deletion was not acknowledged")
	}
	return nil
}

func (r Remote) OpenFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, io.ReadCloser, error) {
	in := &chatv1.DownloadFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID)}
	streamContext, cancel := context.WithCancel(ctx)
	stream, err := r.chat.DownloadFile(streamContext, in)
	if err != nil {
		cancel()
		return domain.File{}, nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		cancel()
		return domain.File{}, nil, err
	}
	header := first.GetMetadata()
	if header == nil {
		cancel()
		return domain.File{}, nil, errors.New("download stream did not begin with file metadata")
	}
	file, err := decodeProtoFile(header)
	if err != nil {
		cancel()
		return domain.File{}, nil, err
	}
	return file, &remoteFileReader{stream: stream, cancel: cancel}, nil
}

func (r Remote) LookupToken(ctx context.Context, token string) (domain.TokenRecord, error) {
	in := &chatv1.TokenRequest{Token: token}
	out, err := r.auth.LookupToken(ctx, in)
	if err != nil {
		return domain.TokenRecord{}, err
	}
	return decodeProtoToken(out)
}

func (r Remote) LookupAppToken(ctx context.Context, token string) (domain.AppTokenRecord, error) {
	out, err := r.auth.LookupAppToken(ctx, &chatv1.TokenRequest{Token: token})
	if err != nil {
		return domain.AppTokenRecord{}, err
	}
	return domain.AppTokenRecord{AppID: domain.AppID(out.GetAppId()), Scopes: append([]string(nil), out.GetScopes()...), Revoked: out.GetRevoked()}, nil
}

func (r Remote) CreateAppInstallation(ctx context.Context, value domain.AppInstallation) error {
	_, err := r.auth.CreateAppInstallation(ctx, &chatv1.AppInstallationRequest{Installation: &chatv1.AppInstallation{AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), Enabled: value.Enabled, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}})
	return err
}

func (r Remote) ListAppInstallations(ctx context.Context, appID domain.AppID) ([]domain.AppInstallation, error) {
	out, err := r.auth.ListAppInstallations(ctx, &chatv1.AppInstallationRequest{AppId: string(appID)})
	if err != nil {
		return nil, err
	}
	values := make([]domain.AppInstallation, 0, len(out.GetInstallations()))
	for _, item := range out.GetInstallations() {
		created, parseErr := time.Parse(time.RFC3339Nano, item.GetCreatedAt())
		if parseErr != nil {
			return nil, parseErr
		}
		values = append(values, domain.AppInstallation{AppID: domain.AppID(item.GetAppId()), WorkspaceID: domain.WorkspaceID(item.GetWorkspaceId()), Enabled: item.GetEnabled(), CreatedAt: created})
	}
	return values, nil
}

func (r Remote) LookupSession(ctx context.Context, token string) (domain.SessionRecord, error) {
	in := &chatv1.TokenRequest{Token: token}
	out, err := r.auth.LookupSession(ctx, in)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	return decodeProtoSession(out)
}

func (r Remote) CreateSession(ctx context.Context, token string, record domain.SessionRecord) error {
	out, err := r.auth.CreateSession(ctx, &chatv1.CreateSessionRequest{Token: token, Session: encodeProtoSession(record)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed session creation was not acknowledged")
	}
	return nil
}

func (r Remote) GetAuthMethod(ctx context.Context, workspaceID domain.WorkspaceID, provider string) (domain.AuthMethod, error) {
	out, err := r.auth.GetAuthMethod(ctx, &chatv1.AuthMethodRequest{WorkspaceId: string(workspaceID), Provider: provider})
	if err != nil {
		return domain.AuthMethod{}, err
	}
	return domain.AuthMethod{WorkspaceID: domain.WorkspaceID(out.GetWorkspaceId()), Provider: out.GetProvider(), Enabled: out.GetEnabled()}, nil
}

func (r Remote) SetAuthMethod(ctx context.Context, value domain.AuthMethod) error {
	out, err := r.auth.SetAuthMethod(ctx, &chatv1.AuthMethodRequest{WorkspaceId: string(value.WorkspaceID), Provider: value.Provider, Enabled: value.Enabled})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed auth method update was not acknowledged")
	}
	return nil
}

func (r Remote) GetExternalIdentity(ctx context.Context, workspaceID domain.WorkspaceID, provider, subject string) (domain.ExternalIdentity, error) {
	out, err := r.auth.GetExternalIdentity(ctx, &chatv1.ExternalIdentityRequest{WorkspaceId: string(workspaceID), Provider: provider, Subject: subject})
	if err != nil {
		return domain.ExternalIdentity{}, err
	}
	return domain.ExternalIdentity{WorkspaceID: domain.WorkspaceID(out.GetWorkspaceId()), Provider: out.GetProvider(), Subject: out.GetSubject(), UserID: domain.UserID(out.GetUserId())}, nil
}

func (r Remote) CreateExternalIdentity(ctx context.Context, value domain.ExternalIdentity) error {
	out, err := r.auth.CreateExternalIdentity(ctx, &chatv1.ExternalIdentityRequest{WorkspaceId: string(value.WorkspaceID), Provider: value.Provider, Subject: value.Subject, UserId: string(value.UserID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed external identity creation was not acknowledged")
	}
	return nil
}

func (r Remote) RevokeSession(ctx context.Context, token string) error {
	in := &chatv1.TokenRequest{Token: token}
	out, err := r.auth.RevokeSession(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed session revocation was not acknowledged")
	}
	return nil
}

func (r Remote) RevokeToken(ctx context.Context, token string) error {
	in := &chatv1.TokenRequest{Token: token}
	out, err := r.auth.RevokeToken(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed token revocation was not acknowledged")
	}
	return nil
}

type remoteFileReader struct {
	stream interface {
		Recv() (*chatv1.DownloadFilePart, error)
	}
	cancel context.CancelFunc
	buffer []byte
	closed bool
}

func (r *remoteFileReader) Read(destination []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	for len(r.buffer) == 0 {
		chunk, err := r.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		if len(chunk.GetChunk()) != 0 {
			r.buffer = chunk.GetChunk()
		}
	}
	read := copy(destination, r.buffer)
	r.buffer = r.buffer[read:]
	return read, nil
}

func (r *remoteFileReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	return nil
}

func (r Remote) FileInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, error) {
	in := &chatv1.FileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID)}
	out, err := r.files.FileInfo(ctx, in)
	if err != nil {
		return domain.File{}, err
	}
	return decodeProtoFile(out)
}

func (r Remote) ShareFilePublic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, error) {
	out, err := r.files.SharePublicURL(ctx, &chatv1.PublicFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID)})
	if err != nil {
		return domain.File{}, err
	}
	return decodeProtoFile(out)
}

func (r Remote) RevokeFilePublic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, error) {
	out, err := r.files.RevokePublicURL(ctx, &chatv1.PublicFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID)})
	if err != nil {
		return domain.File{}, err
	}
	return decodeProtoFile(out)
}

func (r Remote) OpenPublicFile(ctx context.Context, token string) (domain.File, io.ReadCloser, error) {
	streamContext, cancel := context.WithCancel(ctx)
	stream, err := r.chat.DownloadPublicFile(streamContext, &chatv1.PublicFileTokenRequest{Token: token})
	if err != nil {
		cancel()
		return domain.File{}, nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		cancel()
		return domain.File{}, nil, err
	}
	if first.GetMetadata() == nil {
		cancel()
		return domain.File{}, nil, errors.New("public download stream did not begin with file metadata")
	}
	file, err := decodeProtoFile(first.GetMetadata())
	if err != nil {
		cancel()
		return domain.File{}, nil, err
	}
	return file, &remoteFileReader{stream: stream, cancel: cancel}, nil
}

type remoteUserPhotoReader struct {
	stream chatv1.ChatService_DownloadUserPhotoClient
	cancel context.CancelFunc
	buffer []byte
	closed bool
}

func (r Remote) openUserPhotoStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, token string) (domain.User, io.ReadCloser, error) {
	streamContext, cancel := context.WithCancel(ctx)
	stream, err := r.chat.DownloadUserPhoto(streamContext, &chatv1.UserPhotoDownloadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Token: token})
	if err != nil {
		cancel()
		return domain.User{}, nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		cancel()
		return domain.User{}, nil, err
	}
	metadata := first.GetMetadata()
	if metadata == nil {
		cancel()
		return domain.User{}, nil, errors.New("user photo stream did not begin with metadata")
	}
	return domain.User{ID: userID, WorkspaceID: workspaceID}, &remoteUserPhotoReader{stream: stream, cancel: cancel, buffer: first.GetChunk()}, nil
}
func (r Remote) OpenUserPhoto(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, token string) (domain.User, io.ReadCloser, error) {
	return r.openUserPhotoStream(ctx, workspaceID, userID, token)
}
func (r *remoteUserPhotoReader) Read(destination []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	for len(r.buffer) == 0 {
		part, err := r.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		if len(part.GetChunk()) > 0 {
			r.buffer = part.GetChunk()
		}
	}
	read := copy(destination, r.buffer)
	r.buffer = r.buffer[read:]
	return read, nil
}
func (r *remoteUserPhotoReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	return nil
}

func (r Remote) CreateList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, descriptionBlocks, schema string, copyFrom domain.ListID, includeCopiedRecords, todoMode bool) (domain.List, error) {
	out, err := r.lists.CreateList(ctx, &chatv1.CreateListRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, DescriptionBlocks: descriptionBlocks, Schema: schema, CopyFromListId: string(copyFrom), IncludeCopiedRecords: includeCopiedRecords, TodoMode: todoMode})
	if err != nil {
		return domain.List{}, err
	}
	return decodeProtoList(out.GetList())
}

func (r Remote) UpdateList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, name, descriptionBlocks string, todoMode, todoModeSet bool) (domain.List, error) {
	out, err := r.lists.UpdateList(ctx, &chatv1.UpdateListRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(id), Name: name, DescriptionBlocks: descriptionBlocks, TodoMode: todoMode, TodoModeSet: todoModeSet})
	if err != nil {
		return domain.List{}, err
	}
	return decodeProtoList(out.GetList())
}

func (r Remote) CreateListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, parentItemID domain.ListItemID, fields string) (domain.ListItem, error) {
	out, err := r.lists.CreateListItem(ctx, &chatv1.CreateListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ParentItemId: string(parentItemID), Fields: fields})
	if err != nil {
		return domain.ListItem{}, err
	}
	return decodeProtoListItem(out.GetItem())
}

func (r Remote) GetListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID) (domain.ListItem, error) {
	out, err := r.lists.GetListItem(ctx, &chatv1.ListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID)})
	if err != nil {
		return domain.ListItem{}, err
	}
	return decodeProtoListItem(out.GetItem())
}

func (r Remote) ListItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, request domain.PageRequest, archived bool) (domain.ListItemPage, error) {
	out, err := r.lists.ListItems(ctx, &chatv1.ListItemsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Archived: archived})
	if err != nil {
		return domain.ListItemPage{}, err
	}
	return decodeProtoListItemPage(out.GetPage())
}

func (r Remote) UpdateListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fields string, archived bool) (domain.ListItem, error) {
	out, err := r.lists.UpdateListItem(ctx, &chatv1.UpdateListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID), Fields: fields, Archived: archived})
	if err != nil {
		return domain.ListItem{}, err
	}
	return decodeProtoListItem(out.GetItem())
}

func (r Remote) UpdateListCells(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, cells string) ([]domain.ListItem, error) {
	out, err := r.lists.UpdateListCells(ctx, &chatv1.UpdateListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), Fields: cells})
	if err != nil {
		return nil, err
	}
	page, err := decodeProtoListItemPage(out.GetPage())
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (r Remote) DeleteListItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemIDs []domain.ListItemID) error {
	ids := make([]string, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		ids = append(ids, string(itemID))
	}
	out, err := r.lists.DeleteListItems(ctx, &chatv1.DeleteListItemsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemIds: ids})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "list item deletion")
}

func (r Remote) SetListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, access string, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	out, err := r.lists.SetListAccess(ctx, &chatv1.ListAccessRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), Access: access, ChannelIds: conversationStrings(channelIDs), UserIds: userStrings(userIDs)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "list access set")
}

func (r Remote) DeleteListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	out, err := r.lists.DeleteListAccess(ctx, &chatv1.ListAccessRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ChannelIds: conversationStrings(channelIDs), UserIds: userStrings(userIDs)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "list access deletion")
}

func (r Remote) StartListDownload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, includeArchived bool) (domain.ListDownload, error) {
	out, err := r.lists.StartListDownload(ctx, &chatv1.ListDownloadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), IncludeArchived: includeArchived})
	if err != nil {
		return domain.ListDownload{}, err
	}
	return decodeProtoListDownload(out.GetDownload())
}

func (r Remote) GetListDownload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, jobID domain.ListDownloadID) (domain.ListDownload, error) {
	out, err := r.lists.GetListDownload(ctx, &chatv1.ListDownloadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), JobId: string(jobID)})
	if err != nil {
		return domain.ListDownload{}, err
	}
	return decodeProtoListDownload(out.GetDownload())
}

func (r Remote) DeleteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) error {
	in := &chatv1.FileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID)}
	out, err := r.files.DeleteFile(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed delete file response was not acknowledged")
	}
	return nil
}

func (r Remote) DeleteFileComment(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID, commentID domain.FileCommentID) error {
	out, err := r.files.DeleteFileComment(ctx, &chatv1.FileCommentDeleteRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID), CommentId: string(commentID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed file comment deletion was not acknowledged")
	}
	return nil
}

func (r Remote) Files(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.FilePage, error) {
	in := &chatv1.FilesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.files.Files(ctx, in)
	if err != nil {
		return domain.FilePage{}, err
	}
	return decodeProtoFilePage(out)
}

func (r Remote) AddRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.RemoteFile) (domain.RemoteFile, error) {
	out, err := r.files.AddRemoteFile(ctx, &chatv1.AddRemoteFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ExternalId: value.ExternalID, Title: value.Title, FileType: value.FileType, ExternalUrl: value.ExternalURL, PreviewImage: value.PreviewImage, IndexableContents: value.IndexableContents})
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return decodeProtoRemoteFile(out)
}

func (r Remote) RemoteFileInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.RemoteFileLookup) (domain.RemoteFile, error) {
	out, err := r.files.RemoteFileInfo(ctx, &chatv1.RemoteFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(lookup.ID), ExternalId: lookup.ExternalID})
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return decodeProtoRemoteFile(out)
}

func (r Remote) RemoteFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.RemoteFilePage, error) {
	out, err := r.files.RemoteFiles(ctx, &chatv1.RemoteFilesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.RemoteFilePage{}, err
	}
	return decodeProtoRemoteFilePage(out)
}

func (r Remote) RemoveRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.RemoteFileLookup) error {
	out, err := r.files.RemoveRemoteFile(ctx, &chatv1.RemoteFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(lookup.ID), ExternalId: lookup.ExternalID})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed remove remote file response was not acknowledged")
	}
	return nil
}

func (r Remote) ShareRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.RemoteFileLookup, channels []domain.ConversationID) (domain.RemoteFile, error) {
	values := make([]string, 0, len(channels))
	for _, channel := range channels {
		values = append(values, string(channel))
	}
	out, err := r.files.ShareRemoteFile(ctx, &chatv1.ShareRemoteFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(lookup.ID), ExternalId: lookup.ExternalID, Channels: values})
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return decodeProtoRemoteFile(out)
}

func (r Remote) CreateCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, title, documentContent string, channelID domain.ConversationID) (domain.Canvas, error) {
	out, err := r.canvases.CreateCanvas(ctx, &chatv1.CreateCanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Title: title, DocumentContent: documentContent, ChannelId: string(channelID)})
	if err != nil {
		return domain.Canvas{}, err
	}
	return decodeProtoCanvas(out)
}

func (r Remote) EditCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, changes string) error {
	out, err := r.canvases.EditCanvas(ctx, &chatv1.EditCanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), Changes: changes})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "canvas edit")
}

func (r Remote) DeleteCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) error {
	out, err := r.canvases.DeleteCanvas(ctx, &chatv1.CanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "canvas delete")
}

func (r Remote) SetCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, access string, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	out, err := r.canvases.SetCanvasAccess(ctx, &chatv1.CanvasAccessRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), AccessLevel: access, ChannelIds: conversationStrings(channelIDs), UserIds: userStrings(userIDs)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "canvas access set")
}

func (r Remote) DeleteCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	out, err := r.canvases.DeleteCanvasAccess(ctx, &chatv1.CanvasAccessDeleteRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), ChannelIds: conversationStrings(channelIDs), UserIds: userStrings(userIDs)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "canvas access delete")
}

func (r Remote) LookupCanvasSections(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, criteria string) ([]domain.CanvasSection, error) {
	out, err := r.canvases.LookupCanvasSections(ctx, &chatv1.CanvasSectionsLookupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), Criteria: criteria})
	if err != nil {
		return nil, err
	}
	result := make([]domain.CanvasSection, 0, len(out.GetSections()))
	for _, section := range out.GetSections() {
		result = append(result, domain.CanvasSection{ID: section.GetId(), Type: section.GetType(), Text: section.GetText()})
	}
	return result, nil
}

func conversationStrings(values []domain.ConversationID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func userStrings(values []domain.UserID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func (r Remote) UpdateRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, update domain.RemoteFileUpdate) (domain.RemoteFile, error) {
	fields := make([]string, 0, 5)
	if update.SetTitle {
		fields = append(fields, "title")
	}
	if update.SetFileType {
		fields = append(fields, "file_type")
	}
	if update.SetExternalURL {
		fields = append(fields, "external_url")
	}
	if update.SetPreviewImage {
		fields = append(fields, "preview_image")
	}
	if update.SetIndexableData {
		fields = append(fields, "indexable_contents")
	}
	out, err := r.files.UpdateRemoteFile(ctx, &chatv1.UpdateRemoteFileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(update.Lookup.ID), ExternalId: update.Lookup.ExternalID, Title: update.Title, FileType: update.FileType, ExternalUrl: update.ExternalURL, PreviewImage: update.PreviewImage, IndexableContents: update.IndexableContents, UpdateFields: fields})
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return decodeProtoRemoteFile(out)
}

func (r Remote) Replies(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) (domain.MessagePage, error) {
	in := &chatv1.RepliesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.messages.Replies(ctx, in)
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
}

func (r Remote) ConversationInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	in := &chatv1.ConversationInfoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)}
	out, err := r.conversations.ConversationInfo(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) UserInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, requestedID domain.UserID) (domain.User, error) {
	in := &chatv1.UserRequest{WorkspaceId: string(workspaceID), UserId: string(userID), RequestedUserId: string(requestedID)}
	out, err := r.presence.UserInfo(ctx, in)
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) RemoveUser(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID) error {
	out, err := r.directory.RemoveUser(ctx, &chatv1.RemoveUserRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed user removal was not acknowledged")
	}
	return nil
}

func (r Remote) SetUserRole(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID, role domain.WorkspaceRole) error {
	out, err := r.directory.SetUserRole(ctx, &chatv1.SetUserRoleRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID), Role: string(role)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed user role mutation was not acknowledged")
	}
	return nil
}

func (r Remote) SetUserExpiration(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID, expiration time.Time) error {
	seconds := int64(0)
	if !expiration.IsZero() {
		seconds = expiration.Unix()
	}
	out, err := r.directory.SetUserExpiration(ctx, &chatv1.SetUserExpirationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID), ExpirationTs: seconds})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed user expiration mutation was not acknowledged")
	}
	return nil
}

func (r Remote) ResetUserSessions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID) error {
	out, err := r.directory.ResetUserSessions(ctx, &chatv1.ResetUserSessionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed session reset was not acknowledged")
	}
	return nil
}

func (r Remote) AdminRenameConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	out, err := r.mutations.AdminRenameConversation(ctx, &chatv1.RenameConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Name: name})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) AdminSetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	out, err := r.mutations.AdminSetConversationArchived(ctx, &chatv1.SetConversationArchivedRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Archived: archived})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) AdminDeleteConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	out, err := r.mutations.AdminDeleteConversation(ctx, &chatv1.DeleteConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed admin conversation deletion was not acknowledged")
	}
	return nil
}

func (r Remote) AdminAddConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID) error {
	out, err := r.mutations.AdminAddConversationAccessGroup(ctx, &chatv1.ConversationAccessGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), GroupId: string(groupID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed conversation access group add was not acknowledged")
	}
	return nil
}

func (r Remote) AdminRemoveConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID) error {
	out, err := r.mutations.AdminRemoveConversationAccessGroup(ctx, &chatv1.ConversationAccessGroupRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), GroupId: string(groupID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed conversation access group removal was not acknowledged")
	}
	return nil
}

func (r Remote) AdminListConversationAccessGroups(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) ([]domain.UserGroupID, error) {
	out, err := r.mutations.AdminListConversationAccessGroups(ctx, &chatv1.ConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return nil, err
	}
	groups := make([]domain.UserGroupID, 0, len(out.GetGroupIds()))
	for _, groupID := range out.GetGroupIds() {
		groups = append(groups, domain.UserGroupID(groupID))
	}
	return groups, nil
}

func (r Remote) AdminInviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	values := make([]string, 0, len(users))
	for _, value := range users {
		values = append(values, string(value))
	}
	out, err := r.mutations.AdminInviteConversationMembers(ctx, &chatv1.InviteConversationMembersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Users: values})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) AdminConvertConversationToPrivate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	out, err := r.mutations.AdminConvertConversationToPrivate(ctx, &chatv1.ConvertConversationToPrivateRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) AdminConversationTeams(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) ([]domain.WorkspaceID, bool, domain.Cursor, error) {
	out, err := r.mutations.AdminConversationTeams(ctx, &chatv1.AdminConversationTeamsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return nil, false, "", err
	}
	teams := make([]domain.WorkspaceID, 0, len(out.GetTeamIds()))
	for _, team := range out.GetTeamIds() {
		teams = append(teams, domain.WorkspaceID(team))
	}
	return teams, out.GetHasMore(), domain.Cursor(out.GetNextCursor()), nil
}

func (r Remote) AdminSetConversationTeams(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, teams []domain.WorkspaceID, orgChannel bool) error {
	teamIDs := make([]string, 0, len(teams))
	for _, team := range teams {
		teamIDs = append(teamIDs, string(team))
	}
	out, err := r.mutations.AdminSetConversationTeams(ctx, &chatv1.AdminConversationTeamsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), TeamIds: teamIDs, OrgChannel: orgChannel})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed conversation team mutation was not acknowledged")
	}
	return nil
}

func (r Remote) AdminSearchConversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.ConversationPage, error) {
	out, err := r.directory.SearchConversations(ctx, &chatv1.SearchConversationsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Query: query, Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.ConversationPage{}, err
	}
	return decodeProtoConversationPage(out)
}

func (r Remote) AdminSetWorkspaceName(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string) (domain.Workspace, error) {
	out, err := r.directory.SetWorkspaceName(ctx, &chatv1.SetWorkspaceNameRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name})
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AdminSetWorkspaceDescription(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, description string) (domain.Workspace, error) {
	out, err := r.directory.SetWorkspaceDescription(ctx, &chatv1.SetWorkspaceDescriptionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Description: description})
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AdminSetWorkspaceDiscoverability(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, discoverability domain.WorkspaceDiscoverability) (domain.Workspace, error) {
	out, err := r.directory.SetWorkspaceDiscoverability(ctx, &chatv1.SetWorkspaceDiscoverabilityRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Discoverability: string(discoverability)})
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AdminSetWorkspaceIcon(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, iconURL string) (domain.Workspace, error) {
	out, err := r.directory.SetWorkspaceIcon(ctx, &chatv1.SetWorkspaceIconRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ImageUrl: iconURL})
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AdminSetWorkspaceDefaultChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channels []domain.ConversationID) (domain.Workspace, error) {
	values := make([]string, 0, len(channels))
	for _, channel := range channels {
		values = append(values, string(channel))
	}
	out, err := r.directory.SetWorkspaceDefaultChannels(ctx, &chatv1.SetWorkspaceDefaultChannelsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelIds: values})
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AdminGetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationPrefs, error) {
	out, err := r.directory.GetConversationPrefs(ctx, &chatv1.ConversationPrefsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	return decodeProtoConversationPrefs(out)
}

func (r Remote) AdminSetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, value domain.ConversationPrefs) (domain.ConversationPrefs, error) {
	out, err := r.directory.SetConversationPrefs(ctx, &chatv1.SetConversationPrefsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Prefs: encodeProtoConversationPrefs(value)})
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	return decodeProtoConversationPrefs(out)
}

func (r Remote) AdminTeamUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole, request domain.PageRequest) (domain.UserPage, error) {
	out, err := r.directory.AdminTeamUsers(ctx, &chatv1.AdminTeamUsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Role: string(role), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.UserPage{}, err
	}
	return decodeProtoUserPage(out)
}

func (r Remote) AdminInviteUser(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, email string, channels []domain.ConversationID, customMessage, realName string, resend, restricted, ultraRestricted bool, guestExpirationAt time.Time) error {
	channelIDs := make([]string, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, string(channel))
	}
	out, err := r.directory.AdminInviteUser(ctx, &chatv1.AdminInviteUserRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Email: email, ChannelIds: channelIDs, CustomMessage: customMessage, RealName: realName, Resend: resend, Restricted: restricted, UltraRestricted: ultraRestricted, GuestExpirationAt: guestExpirationAt.Unix()})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed user invitation was not acknowledged")
	}
	return nil
}

func (r Remote) AdminCreateUser(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, email, realName string, role domain.WorkspaceRole) (domain.User, error) {
	out, err := r.directory.AdminCreateUser(ctx, &chatv1.AdminCreateUserRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Email: email, RealName: realName, Role: string(role)})
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) AdminListUsers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.AdminUserPage, error) {
	out, err := r.directory.AdminListUsers(ctx, &chatv1.AdminUsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.AdminUserPage{}, err
	}
	return decodeProtoAdminUserPage(out)
}

func (r Remote) AdminAssignUser(ctx context.Context, workspaceID domain.WorkspaceID, userID, targetID domain.UserID, channels []domain.ConversationID) error {
	channelIDs := make([]string, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, string(channel))
	}
	out, err := r.directory.AdminAssignUser(ctx, &chatv1.AdminAssignUserRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID), ChannelIds: channelIDs})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed user assignment was not acknowledged")
	}
	return nil
}

func (r Remote) AdminApproveInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.InviteRequestID) error {
	out, err := r.directory.AdminApproveInviteRequest(ctx, &chatv1.InviteRequestMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), InviteRequestId: string(id)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed invite approval was not acknowledged")
	}
	return nil
}

func (r Remote) AdminDenyInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.InviteRequestID) error {
	out, err := r.directory.AdminDenyInviteRequest(ctx, &chatv1.InviteRequestMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), InviteRequestId: string(id)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed invite denial was not acknowledged")
	}
	return nil
}

func (r Remote) AdminListInviteRequests(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, status domain.InviteRequestStatus, request domain.PageRequest) (domain.InviteRequestPage, error) {
	out, err := r.directory.AdminListInviteRequests(ctx, &chatv1.InviteRequestsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Status: string(status), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.InviteRequestPage{}, err
	}
	values := make([]domain.InviteRequest, 0, len(out.GetRequests()))
	for _, item := range out.GetRequests() {
		values = append(values, decodeProtoInviteRequest(item))
	}
	return domain.InviteRequestPage{Requests: values, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) AdminApproveApp(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	out, err := r.directory.AdminApproveApp(ctx, &chatv1.AppApprovalMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), RequestId: string(requestID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app approval was not acknowledged")
	}
	return nil
}

func (r Remote) AdminRestrictApp(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	out, err := r.directory.AdminRestrictApp(ctx, &chatv1.AppApprovalMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), RequestId: string(requestID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app restriction was not acknowledged")
	}
	return nil
}

func (r Remote) AdminListApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, approvalStatus domain.AppApprovalStatus, request domain.PageRequest) (domain.AppApprovalPage, error) {
	out, err := r.directory.AdminListApps(ctx, &chatv1.AppApprovalsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Status: string(approvalStatus), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.AppApprovalPage{}, err
	}
	values := make([]domain.AppApproval, 0, len(out.GetApps()))
	for _, item := range out.GetApps() {
		values = append(values, decodeProtoAppApproval(item))
	}
	return domain.AppApprovalPage{Apps: values, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) Emojis(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.CustomEmoji, error) {
	out, err := r.directory.Emojis(ctx, &chatv1.EmojiListRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.CustomEmoji, 0, len(out.GetEmojis()))
	for _, value := range out.GetEmojis() {
		result = append(result, domain.CustomEmoji{WorkspaceID: workspaceID, Name: value.GetName(), URL: value.GetUrl(), AliasFor: value.GetAliasFor()})
	}
	return result, nil
}

func (r Remote) AdminAddEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, url string) error {
	out, err := r.directory.AddEmoji(ctx, &chatv1.EmojiMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Value: url})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "emoji add")
}
func (r Remote) AdminAddEmojiAlias(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, target string) error {
	out, err := r.directory.AddEmojiAlias(ctx, &chatv1.EmojiMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Value: target})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "emoji alias add")
}
func (r Remote) AdminRemoveEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string) error {
	out, err := r.directory.RemoveEmoji(ctx, &chatv1.EmojiMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "emoji remove")
}
func (r Remote) AdminRenameEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, oldName, newName string) error {
	out, err := r.directory.RenameEmoji(ctx, &chatv1.EmojiMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: oldName, Value: newName})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "emoji rename")
}

func requireAcknowledgement(ok bool, operation string) error {
	if !ok {
		return errors.New("typed " + operation + " was not acknowledged")
	}
	return nil
}

func (r Remote) UserByEmail(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, email string) (domain.User, error) {
	in := &chatv1.UserByEmailRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Email: email}
	out, err := r.presence.UserByEmail(ctx, in)
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) SetUserProfile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, profile domain.UserProfile) (domain.User, error) {
	in := &chatv1.SetUserProfileRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Profile: encodeProtoProfile(profile)}
	out, err := r.presence.SetUserProfile(ctx, in)
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) SetUserPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, presence domain.Presence) (domain.User, error) {
	in := &chatv1.SetUserPresenceRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Presence: string(presence)}
	out, err := r.presence.SetUserPresence(ctx, in)
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) DoNotDisturbInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, requestedID domain.UserID) (domain.DoNotDisturb, error) {
	in := &chatv1.DoNotDisturbRequest{WorkspaceId: string(workspaceID), UserId: string(userID), RequestedUserId: string(requestedID)}
	out, err := r.presence.DoNotDisturbInfo(ctx, in)
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	return decodeProtoDoNotDisturb(out)
}

func (r Remote) SetSnooze(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, minutes int64) (domain.DoNotDisturb, error) {
	out, err := r.presence.SetSnooze(ctx, &chatv1.SetSnoozeRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Minutes: minutes})
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	return decodeProtoDoNotDisturb(out)
}

func (r Remote) EndSnooze(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.DoNotDisturb, error) {
	out, err := r.presence.EndSnooze(ctx, &chatv1.DoNotDisturbRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	return decodeProtoDoNotDisturb(out)
}

func (r Remote) EndDND(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	out, err := r.presence.EndDND(ctx, &chatv1.DoNotDisturbRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed dnd response is not ok")
	}
	return nil
}

func (r Remote) Users(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.UserPage, error) {
	in := &chatv1.UsersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.directory.Users(ctx, in)
	if err != nil {
		return domain.UserPage{}, err
	}
	return decodeProtoUserPage(out)
}

func (r Remote) ConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) (domain.UserPage, error) {
	in := &chatv1.ConversationMembersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.directory.ConversationMembers(ctx, in)
	if err != nil {
		return domain.UserPage{}, err
	}
	return decodeProtoUserPage(out)
}

func (r Remote) WorkspaceInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.Workspace, error) {
	in := &chatv1.WorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID)}
	out, err := r.directory.WorkspaceInfo(ctx, in)
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AdminCreateWorkspace(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, domainName, name, description string, discoverability domain.WorkspaceDiscoverability) (domain.Workspace, error) {
	out, err := r.directory.AdminCreateWorkspace(ctx, &chatv1.AdminCreateWorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TeamDomain: domainName, TeamName: name, TeamDescription: description, TeamDiscoverability: string(discoverability)})
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) RequestAppPermissions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, target domain.UserID, scopes []string, triggerID string) error {
	out, err := r.directory.RequestAppPermissions(ctx, &chatv1.AppPermissionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(target), Scopes: append([]string(nil), scopes...), TriggerId: triggerID})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "app permission request")
}

func decodeProtoView(value *chatv1.View) (domain.View, error) {
	if value == nil {
		return domain.View{}, errors.New("view response is nil")
	}
	return domain.View{ID: domain.ViewID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Type: value.GetType(), ExternalID: value.GetExternalId(), Payload: value.GetPayload(), Hash: value.GetHash(), RootViewID: domain.ViewID(value.GetRootViewId()), PreviousViewID: domain.ViewID(value.GetPreviousViewId()), CreatedAt: time.Unix(0, value.GetCreatedAtUnixNano()).UTC(), UpdatedAt: time.Unix(0, value.GetUpdatedAtUnixNano()).UTC()}, nil
}

func (r Remote) OpenView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, payload string) (domain.View, error) {
	out, err := r.views.OpenView(ctx, &chatv1.OpenViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: triggerID, Payload: payload})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) PublishView(ctx context.Context, workspaceID domain.WorkspaceID, userID, target domain.UserID, payload, hash string) (domain.View, error) {
	out, err := r.views.PublishView(ctx, &chatv1.PublishViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(target), Payload: payload, Hash: hash})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) PushView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, payload string) (domain.View, error) {
	out, err := r.views.PushView(ctx, &chatv1.PushViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: triggerID, Payload: payload})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) UpdateView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, viewID, externalID, payload, hash string) (domain.View, error) {
	out, err := r.views.UpdateView(ctx, &chatv1.UpdateViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ViewId: viewID, ExternalId: externalID, Payload: payload, Hash: hash})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) WorkflowStepCompleted(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, executeID, outputs string) error {
	out, err := r.workflows.StepCompleted(ctx, &chatv1.WorkflowStepRequest{WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowStepExecuteId: executeID, Outputs: outputs})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("workflow completion was not acknowledged")
	}
	return nil
}

func (r Remote) WorkflowStepFailed(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, executeID, failure string) error {
	out, err := r.workflows.StepFailed(ctx, &chatv1.WorkflowStepRequest{WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowStepExecuteId: executeID, Error: failure})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("workflow failure was not acknowledged")
	}
	return nil
}

func (r Remote) WorkflowUpdateStep(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, editID, inputs, outputs, stepName, imageURL string) error {
	out, err := r.workflows.UpdateStep(ctx, &chatv1.WorkflowStepUpdateRequest{WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowStepEditId: editID, Inputs: inputs, Outputs: outputs, StepName: stepName, StepImageUrl: imageURL})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("workflow update was not acknowledged")
	}
	return nil
}

func (r Remote) OpenDialog(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, payload string) error {
	out, err := r.dialogs.OpenDialog(ctx, &chatv1.OpenDialogRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: triggerID, Payload: payload})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("dialog open was not acknowledged")
	}
	return nil
}

func (r Remote) BotInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, botID domain.BotID) (domain.Bot, error) {
	out, err := r.bots.BotInfo(ctx, &chatv1.BotInfoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), BotId: string(botID)})
	if err != nil {
		return domain.Bot{}, err
	}
	return decodeProtoBot(out)
}

func (r Remote) MigrationExchange(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.UserID, toOld bool) (domain.MigrationExchange, error) {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, string(id))
	}
	out, err := r.migration.Exchange(ctx, &chatv1.MigrationExchangeRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UserIds: values, ToOld: toOld})
	if err != nil {
		return domain.MigrationExchange{}, err
	}
	result := domain.MigrationExchange{WorkspaceID: domain.WorkspaceID(out.GetWorkspaceId()), UserIDMap: make(map[domain.UserID]domain.UserID, len(out.GetUserIdMap())), InvalidUserIDs: make([]domain.UserID, 0, len(out.GetInvalidUserIds()))}
	for key, value := range out.GetUserIdMap() {
		result.UserIDMap[domain.UserID(key)] = domain.UserID(value)
	}
	for _, value := range out.GetInvalidUserIds() {
		result.InvalidUserIDs = append(result.InvalidUserIDs, domain.UserID(value))
	}
	return result, nil
}

func (r Remote) AdminDisconnectSharedConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, leaving []domain.WorkspaceID) error {
	teams := make([]string, 0, len(leaving))
	for _, team := range leaving {
		teams = append(teams, string(team))
	}
	out, err := r.enterprise.DisconnectShared(ctx, &chatv1.DisconnectSharedConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), LeavingTeamIds: teams})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("shared conversation disconnect was not acknowledged")
	}
	return nil
}

func (r Remote) AdminConnectedChannelInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channels []domain.ConversationID, teams []domain.WorkspaceID, request domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error) {
	channelIDs := make([]string, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, string(channel))
	}
	teamIDs := make([]string, 0, len(teams))
	for _, team := range teams {
		teamIDs = append(teamIDs, string(team))
	}
	out, err := r.enterprise.ListOriginalConnectedChannelInfo(ctx, &chatv1.ConnectedChannelInfoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelIds: channelIDs, TeamIds: teamIDs, Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return nil, false, "", err
	}
	values := make([]domain.ConnectedChannelInfo, 0, len(out.GetChannels()))
	for _, item := range out.GetChannels() {
		teams := make([]domain.WorkspaceID, 0, len(item.GetInternalTeamIds()))
		for _, team := range item.GetInternalTeamIds() {
			teams = append(teams, domain.WorkspaceID(team))
		}
		values = append(values, domain.ConnectedChannelInfo{ChannelID: domain.ConversationID(item.GetChannelId()), InternalTeamIDs: teams, OriginalConnectedChannelID: domain.ConversationID(item.GetOriginalConnectedChannelId()), OriginalConnectedHostID: domain.WorkspaceID(item.GetOriginalConnectedHostId())})
	}
	return values, out.GetHasMore(), domain.Cursor(out.GetNextCursor()), nil
}

func (r Remote) OAuthExchange(ctx context.Context, clientID, clientSecret, code, redirectURI string) (domain.OAuthToken, error) {
	out, err := r.oauth.ExchangeOAuth(ctx, &chatv1.OAuthExchangeRequest{ClientId: clientID, ClientSecret: clientSecret, Code: code, RedirectUri: redirectURI})
	if err != nil {
		return domain.OAuthToken{}, err
	}
	return domain.OAuthToken{AccessToken: out.GetAccessToken(), ClientID: out.GetClientId(), AppID: domain.AppID(out.GetAppId()), WorkspaceID: domain.WorkspaceID(out.GetWorkspaceId()), UserID: domain.UserID(out.GetUserId()), Scopes: append([]string(nil), out.GetScopes()...), TokenType: out.GetTokenType()}, nil
}

func (r Remote) TeamBillableInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID) (domain.BillableInfo, error) {
	out, err := r.directory.TeamBillableInfo(ctx, &chatv1.BillableInfoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID)})
	if err != nil {
		return domain.BillableInfo{}, err
	}
	values := make([]domain.BillableUser, 0, len(out.GetUsers()))
	for _, item := range out.GetUsers() {
		values = append(values, domain.BillableUser{UserID: domain.UserID(item.GetUserId()), BillingActive: item.GetBillingActive()})
	}
	return domain.BillableInfo{Users: values}, nil
}

func (r Remote) Conversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ConversationListRequest) (domain.ConversationPage, error) {
	in := &chatv1.ConversationsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Types: conversationTypeStrings(request.Types), ExcludeArchived: request.ExcludeArchived, MemberUserId: string(request.MemberUserID)}
	out, err := r.conversations.Conversations(ctx, in)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	return decodeProtoConversationPage(out)
}

func (r Remote) OpenConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, users []domain.UserID) (domain.Conversation, error) {
	in := &chatv1.OpenConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Users: stringIDs(users)}
	out, err := r.mutations.OpenConversation(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) CreateConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string, private bool) (domain.Conversation, error) {
	in := &chatv1.CreateConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Private: private}
	out, err := r.mutations.CreateConversation(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) JoinConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	in := &chatv1.ConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)}
	out, err := r.mutations.JoinConversation(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) InviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	in := &chatv1.InviteConversationMembersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Users: stringIDs(users)}
	out, err := r.mutations.InviteConversationMembers(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) LeaveConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	in := &chatv1.ConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)}
	out, err := r.mutations.LeaveConversation(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed leave response was not acknowledged")
	}
	return nil
}

func (r Remote) KickConversationMember(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, targetID domain.UserID) error {
	in := &chatv1.KickConversationMemberRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), TargetId: string(targetID)}
	out, err := r.mutations.KickConversationMember(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed kick response was not acknowledged")
	}
	return nil
}

func (r Remote) RenameConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	in := &chatv1.RenameConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Name: name}
	out, err := r.mutations.RenameConversation(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) SetConversationTopic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, topic string) (domain.Conversation, error) {
	in := &chatv1.SetConversationTopicRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Topic: topic}
	out, err := r.mutations.SetConversationTopic(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) SetConversationPurpose(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, purpose string) (domain.Conversation, error) {
	in := &chatv1.SetConversationPurposeRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Purpose: purpose}
	out, err := r.mutations.SetConversationPurpose(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) SetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	in := &chatv1.SetConversationArchivedRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Archived: archived}
	out, err := r.mutations.SetConversationArchived(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) MarkRead(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.ReadCursor, error) {
	in := &chatv1.MarkReadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)}
	out, err := r.interactions.MarkRead(ctx, in)
	if err != nil {
		return domain.ReadCursor{}, err
	}
	return decodeProtoReadCursor(out)
}

func (r Remote) AddReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) error {
	in := &chatv1.ReactionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Name: name}
	out, err := r.reactions.AddReaction(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed reaction addition was not acknowledged")
	}
	return nil
}

func (r Remote) RemoveReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) error {
	in := &chatv1.ReactionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Name: name}
	out, err := r.reactions.RemoveReaction(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed reaction removal was not acknowledged")
	}
	return nil
}

func (r Remote) Reactions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error) {
	in := &chatv1.ReactionPageRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.reactions.Reactions(ctx, in)
	if err != nil {
		return nil, "", false, err
	}
	page, err := decodeProtoReactionPage(out)
	if err != nil {
		return nil, "", false, err
	}
	return page.Reactions, page.NextCursor, page.HasMore, nil
}

func (r Remote) UserReactions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.UserReactionPage, error) {
	in := &chatv1.UserReactionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.reactions.UserReactions(ctx, in)
	if err != nil {
		return domain.UserReactionPage{}, err
	}
	return decodeProtoUserReactionPage(out)
}

func (r Remote) AddPin(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	in := &chatv1.PinRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)}
	out, err := r.reactions.AddPin(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed pin addition was not acknowledged")
	}
	return nil
}

func (r Remote) RemovePin(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	in := &chatv1.PinRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)}
	out, err := r.reactions.RemovePin(ctx, in)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed pin removal was not acknowledged")
	}
	return nil
}

func (r Remote) Pins(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error) {
	in := &chatv1.PinsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.reactions.Pins(ctx, in)
	if err != nil {
		return nil, "", false, err
	}
	page, err := decodeProtoPinPage(out)
	if err != nil {
		return nil, "", false, err
	}
	return page.Pins, page.NextCursor, page.HasMore, nil
}

func (r Remote) AddStar(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	out, err := r.reactions.AddStar(ctx, &chatv1.PinRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed star addition was not acknowledged")
	}
	return nil
}

func (r Remote) RemoveStar(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	out, err := r.reactions.RemoveStar(ctx, &chatv1.PinRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed star removal was not acknowledged")
	}
	return nil
}

func (r Remote) Stars(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error) {
	out, err := r.reactions.Stars(ctx, &chatv1.StarsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return nil, "", false, err
	}
	page, err := decodeProtoStarPage(out)
	if err != nil {
		return nil, "", false, err
	}
	return page.Stars, page.NextCursor, page.HasMore, nil
}

func (r Remote) AddBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, title, bookmarkType, link, emoji, entityID, accessLevel, parentID string) (domain.Bookmark, error) {
	out, err := r.bookmarks.AddBookmark(ctx, &chatv1.AddBookmarkRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Title: title, Type: bookmarkType, Link: link, Emoji: emoji, EntityId: entityID, AccessLevel: accessLevel, ParentId: parentID})
	if err != nil {
		return domain.Bookmark{}, err
	}
	return decodeProtoBookmark(out)
}

func (r Remote) EditBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, id domain.BookmarkID, update domain.BookmarkUpdate) (domain.Bookmark, error) {
	input := &chatv1.EditBookmarkRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), BookmarkId: string(id)}
	if update.SetTitle {
		input.Title = &update.Title
	}
	if update.SetLink {
		input.Link = &update.Link
	}
	if update.SetEmoji {
		input.Emoji = &update.Emoji
	}
	out, err := r.bookmarks.EditBookmark(ctx, input)
	if err != nil {
		return domain.Bookmark{}, err
	}
	return decodeProtoBookmark(out)
}

func (r Remote) Bookmarks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) ([]domain.Bookmark, error) {
	out, err := r.bookmarks.ListBookmarks(ctx, &chatv1.BookmarksRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return nil, err
	}
	items := make([]domain.Bookmark, 0, len(out.GetBookmarks()))
	for _, item := range out.GetBookmarks() {
		bookmark, err := decodeProtoBookmark(item)
		if err != nil {
			return nil, err
		}
		items = append(items, bookmark)
	}
	return items, nil
}

func (r Remote) RemoveBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, id domain.BookmarkID) error {
	out, err := r.bookmarks.RemoveBookmark(ctx, &chatv1.BookmarkRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), BookmarkId: string(id)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "bookmark removal")
}

func (r Remote) AddReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID, text string, due time.Time) (domain.Reminder, error) {
	out, err := r.reminders.AddReminder(ctx, &chatv1.AddReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID), Text: text, Time: due.Unix()})
	if err != nil {
		return domain.Reminder{}, err
	}
	return decodeProtoReminder(out)
}

func (r Remote) CompleteReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) error {
	out, err := r.reminders.CompleteReminder(ctx, &chatv1.ReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed reminder completion was not acknowledged")
	}
	return nil
}

func (r Remote) DeleteReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) error {
	out, err := r.reminders.DeleteReminder(ctx, &chatv1.ReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed reminder deletion was not acknowledged")
	}
	return nil
}

func (r Remote) ReminderInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) (domain.Reminder, error) {
	out, err := r.reminders.ReminderInfo(ctx, &chatv1.ReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID)})
	if err != nil {
		return domain.Reminder{}, err
	}
	return decodeProtoReminder(out)
}

func (r Remote) Reminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ReminderPage, error) {
	out, err := r.reminders.Reminders(ctx, &chatv1.RemindersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.ReminderPage{}, err
	}
	result := make([]domain.Reminder, 0, len(out.GetReminders()))
	for _, value := range out.GetReminders() {
		reminder, err := decodeProtoReminder(value)
		if err != nil {
			return domain.ReminderPage{}, err
		}
		result = append(result, reminder)
	}
	return domain.ReminderPage{Reminders: result, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) ScheduleMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text string, postAt time.Time) (domain.ScheduledMessage, error) {
	return r.ScheduleMessageWithBlocks(ctx, workspaceID, userID, channel, text, "", postAt)
}

func (r Remote) ScheduledMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	out, err := r.scheduled.ScheduledMessages(ctx, &chatv1.ScheduledMessagesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(channel), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	items := make([]domain.ScheduledMessage, 0, len(out.GetScheduledMessages()))
	for _, value := range out.GetScheduledMessages() {
		item, err := decodeProtoScheduledMessage(value)
		if err != nil {
			return domain.ScheduledMessagePage{}, err
		}
		items = append(items, item)
	}
	return domain.ScheduledMessagePage{Items: items, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) DeleteScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID) error {
	out, err := r.scheduled.DeleteScheduledMessage(ctx, &chatv1.DeleteScheduledMessageRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(channel), ScheduledMessageId: string(id)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed scheduled message deletion was not acknowledged")
	}
	return nil
}

func (r Remote) ListEventsAfter(ctx context.Context, workspace domain.WorkspaceID, after uint64, limit int) ([]events.Record, error) {
	in := &chatv1.EventsRequest{WorkspaceId: string(workspace), After: after, Limit: int32(limit)}
	out, err := r.events.ListEventsAfter(ctx, in)
	if err != nil {
		return nil, err
	}
	return decodeProtoEvents(out)
}

func (r Remote) ListAppEventsAfter(ctx context.Context, appID domain.AppID, after uint64, limit int) ([]events.Record, error) {
	in := &chatv1.EventsRequest{AppId: string(appID), After: after, Limit: int32(limit)}
	out, err := r.events.ListEventsAfter(ctx, in)
	if err != nil {
		return nil, err
	}
	return decodeProtoEvents(out)
}

type Server struct {
	implementation chatapi.Service
	tokens         auth.TokenStore
	tokenRevoker   auth.TokenRevoker
	sessions       auth.SessionStore
	revoker        auth.SessionRevoker
}

var (
	_ chatv1.ChatServiceServer                    = (*Server)(nil)
	_ chatv1.AuthServiceServer                    = (*Server)(nil)
	_ chatv1.ConversationMutationsServiceServer   = (*Server)(nil)
	_ chatv1.ConversationsServiceServer           = (*Server)(nil)
	_ chatv1.DirectoryServiceServer               = (*Server)(nil)
	_ chatv1.EventsServiceServer                  = (*Server)(nil)
	_ chatv1.FilesServiceServer                   = (*Server)(nil)
	_ chatv1.ListsServiceServer                   = (*Server)(nil)
	_ chatv1.InteractionsServiceServer            = (*Server)(nil)
	_ chatv1.MessagesServiceServer                = (*Server)(nil)
	_ chatv1.PresenceServiceServer                = (*Server)(nil)
	_ chatv1.ReactionsServiceServer               = (*Server)(nil)
	_ chatv1.BookmarksServiceServer               = (*Server)(nil)
	_ chatv1.UserGroupsServiceServer              = (*Server)(nil)
	_ chatv1.CallsServiceServer                   = (*Server)(nil)
	_ chatv1.AccessLogsServiceServer              = (*Server)(nil)
	_ chatv1.ViewsServiceServer                   = (*Server)(nil)
	_ chatv1.WorkflowsServiceServer               = (*Server)(nil)
	_ chatv1.DialogsServiceServer                 = (*Server)(nil)
	_ chatv1.BotsServiceServer                    = (*Server)(nil)
	_ chatv1.MigrationServiceServer               = (*Server)(nil)
	_ chatv1.EnterpriseConversationsServiceServer = (*Server)(nil)
	_ chatv1.OAuthServiceServer                   = (*Server)(nil)
	_ chatv1.RTMServiceServer                     = (*Server)(nil)
	_ chatv1.CanvasesServiceServer                = (*Server)(nil)
	_ chatv1.EntityServiceServer                  = (*Server)(nil)
)

func NewServer(implementation chatapi.Service, tokens auth.TokenStore, sessions auth.SessionStore, revoker auth.SessionRevoker) (*Server, error) {
	if implementation == nil {
		return nil, errors.New("chat gRPC server requires an implementation")
	}
	if tokens == nil {
		return nil, errors.New("chat gRPC server requires a token store")
	}
	tokenRevoker, ok := tokens.(auth.TokenRevoker)
	if !ok {
		return nil, errors.New("chat gRPC server requires a token revoker")
	}
	if sessions == nil {
		return nil, errors.New("chat gRPC server requires a session store")
	}
	if revoker == nil {
		return nil, errors.New("chat gRPC server requires a session revoker")
	}
	return &Server{implementation: implementation, tokens: tokens, tokenRevoker: tokenRevoker, sessions: sessions, revoker: revoker}, nil
}

func RegisterServer(registrar grpc.ServiceRegistrar, implementation chatapi.Service, tokens auth.TokenStore, sessions auth.SessionStore, revoker auth.SessionRevoker) error {
	if registrar == nil {
		return errors.New("chat gRPC server requires a registrar")
	}
	server, err := NewServer(implementation, tokens, sessions, revoker)
	if err != nil {
		return err
	}
	chatv1.RegisterChatServiceServer(registrar, server)
	chatv1.RegisterPresenceServiceServer(registrar, server)
	chatv1.RegisterDirectoryServiceServer(registrar, server)
	chatv1.RegisterConversationsServiceServer(registrar, server)
	chatv1.RegisterFilesServiceServer(registrar, server)
	chatv1.RegisterListsServiceServer(registrar, server)
	chatv1.RegisterConversationMutationsServiceServer(registrar, server)
	chatv1.RegisterInteractionsServiceServer(registrar, server)
	chatv1.RegisterAuthServiceServer(registrar, server)
	chatv1.RegisterEventsServiceServer(registrar, server)
	chatv1.RegisterReactionsServiceServer(registrar, server)
	chatv1.RegisterBookmarksServiceServer(registrar, server)
	chatv1.RegisterMessagesServiceServer(registrar, server)
	chatv1.RegisterRemindersServiceServer(registrar, server)
	chatv1.RegisterScheduledMessagesServiceServer(registrar, server)
	chatv1.RegisterUserGroupsServiceServer(registrar, server)
	chatv1.RegisterCallsServiceServer(registrar, server)
	chatv1.RegisterAccessLogsServiceServer(registrar, server)
	chatv1.RegisterViewsServiceServer(registrar, server)
	chatv1.RegisterWorkflowsServiceServer(registrar, server)
	chatv1.RegisterDialogsServiceServer(registrar, server)
	chatv1.RegisterBotsServiceServer(registrar, server)
	chatv1.RegisterMigrationServiceServer(registrar, server)
	chatv1.RegisterEnterpriseConversationsServiceServer(registrar, server)
	chatv1.RegisterOAuthServiceServer(registrar, server)
	chatv1.RegisterRTMServiceServer(registrar, server)
	chatv1.RegisterCanvasesServiceServer(registrar, server)
	chatv1.RegisterEntityServiceServer(registrar, server)
	return nil
}

func (s *Server) CreateCanvas(ctx context.Context, input *chatv1.CreateCanvasRequest) (*chatv1.Canvas, error) {
	return s.createCanvasProto(ctx, input)
}

func (s *Server) EditCanvas(ctx context.Context, input *chatv1.EditCanvasRequest) (*chatv1.MutationResponse, error) {
	return s.editCanvasProto(ctx, input)
}

func (s *Server) DeleteCanvas(ctx context.Context, input *chatv1.CanvasRequest) (*chatv1.MutationResponse, error) {
	return s.deleteCanvasProto(ctx, input)
}

func (s *Server) SetCanvasAccess(ctx context.Context, input *chatv1.CanvasAccessRequest) (*chatv1.MutationResponse, error) {
	return s.setCanvasAccessProto(ctx, input)
}

func (s *Server) DeleteCanvasAccess(ctx context.Context, input *chatv1.CanvasAccessDeleteRequest) (*chatv1.MutationResponse, error) {
	return s.deleteCanvasAccessProto(ctx, input)
}

func (s *Server) LookupCanvasSections(ctx context.Context, input *chatv1.CanvasSectionsLookupRequest) (*chatv1.CanvasSectionsResponse, error) {
	return s.lookupCanvasSectionsProto(ctx, input)
}

func (s *Server) createCanvasProto(ctx context.Context, input *chatv1.CreateCanvasRequest) (*chatv1.Canvas, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	canvas, err := s.implementation.CreateCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTitle(), input.GetDocumentContent(), domain.ConversationID(input.GetChannelId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvas(canvas), nil
}

func (s *Server) editCanvasProto(ctx context.Context, input *chatv1.EditCanvasRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetCanvasId() == "" || input.GetChanges() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, canvas_id, and changes are required")
	}
	if err := s.implementation.EditCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetChanges()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteCanvasProto(ctx context.Context, input *chatv1.CanvasRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetCanvasId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and canvas_id are required")
	}
	if err := s.implementation.DeleteCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) setCanvasAccessProto(ctx context.Context, input *chatv1.CanvasAccessRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetCanvasId() == "" || input.GetAccessLevel() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, canvas_id, and access_level are required")
	}
	if err := s.implementation.SetCanvasAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetAccessLevel(), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteCanvasAccessProto(ctx context.Context, input *chatv1.CanvasAccessDeleteRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetCanvasId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and canvas_id are required")
	}
	if err := s.implementation.DeleteCanvasAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) lookupCanvasSectionsProto(ctx context.Context, input *chatv1.CanvasSectionsLookupRequest) (*chatv1.CanvasSectionsResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetCanvasId() == "" || input.GetCriteria() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, canvas_id, and criteria are required")
	}
	sections, err := s.implementation.LookupCanvasSections(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetCriteria())
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.CanvasSection, 0, len(sections))
	for _, section := range sections {
		result = append(result, &chatv1.CanvasSection{Id: section.ID, Type: section.Type, Text: section.Text})
	}
	return &chatv1.CanvasSectionsResponse{Sections: result}, nil
}

func conversationIDs(values []string) []domain.ConversationID {
	result := make([]domain.ConversationID, 0, len(values))
	for _, value := range values {
		result = append(result, domain.ConversationID(value))
	}
	return result
}

func userIDs(values []string) []domain.UserID {
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		result = append(result, domain.UserID(value))
	}
	return result
}

func (s *Server) postProto(ctx context.Context, input *chatv1.PostRequest) (*chatv1.Message, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetText() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and text are required")
	}
	message, err := s.implementation.Post(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetText(), domain.MessageTimestamp(input.GetThreadTimestamp()), input.GetIdempotencyKey())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(message), nil
}

func (s *Server) CreateUserGroup(ctx context.Context, input *chatv1.CreateUserGroupRequest) (*chatv1.UserGroup, error) {
	value, err := s.implementation.CreateUserGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetHandle(), input.GetDescription())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(value), nil
}
func (s *Server) UpdateUserGroup(ctx context.Context, input *chatv1.UpdateUserGroupRequest) (*chatv1.UserGroup, error) {
	value, err := s.implementation.UpdateUserGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), input.GetName(), input.GetHandle(), input.GetDescription())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(value), nil
}
func (s *Server) EnableUserGroup(ctx context.Context, input *chatv1.UserGroupRequest) (*chatv1.UserGroup, error) {
	return s.setUserGroupEnabled(ctx, input, true)
}
func (s *Server) DisableUserGroup(ctx context.Context, input *chatv1.UserGroupRequest) (*chatv1.UserGroup, error) {
	return s.setUserGroupEnabled(ctx, input, false)
}
func (s *Server) setUserGroupEnabled(ctx context.Context, input *chatv1.UserGroupRequest, enabled bool) (*chatv1.UserGroup, error) {
	value, err := s.implementation.SetUserGroupEnabled(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), enabled)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(value), nil
}
func (s *Server) UserGroups(ctx context.Context, input *chatv1.UserGroupsRequest) (*chatv1.UserGroupPage, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.ListUserGroups(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetIncludeDisabled(), request)
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.UserGroupPage{Usergroups: make([]*chatv1.UserGroup, 0, len(page.Groups)), NextCursor: string(page.NextCursor), HasMore: page.HasMore}
	for _, value := range page.Groups {
		result.Usergroups = append(result.Usergroups, encodeProtoUserGroup(value))
	}
	return result, nil
}
func (s *Server) UserGroupUsers(ctx context.Context, input *chatv1.UserGroupRequest) (*chatv1.UserGroupUsersResponse, error) {
	values, err := s.implementation.UserGroupUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.UserGroupUsersResponse{Users: make([]string, 0, len(values))}
	for _, value := range values {
		result.Users = append(result.Users, string(value))
	}
	return result, nil
}
func (s *Server) SetUserGroupUsers(ctx context.Context, input *chatv1.UserGroupUsersRequest) (*chatv1.UserGroup, error) {
	values := make([]domain.UserID, 0, len(input.GetUsers()))
	for _, value := range input.GetUsers() {
		values = append(values, domain.UserID(value))
	}
	result, err := s.implementation.SetUserGroupUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), values)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserGroup(result), nil
}

func (s *Server) UserGroupChannels(ctx context.Context, input *chatv1.UserGroupRequest) (*chatv1.UserGroupChannelsResponse, error) {
	values, err := s.implementation.UserGroupChannels(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return &chatv1.UserGroupChannelsResponse{Channels: result}, nil
}
func (s *Server) AddUserGroupChannels(ctx context.Context, input *chatv1.UserGroupChannelsRequest) (*chatv1.MutationResponse, error) {
	values := make([]domain.ConversationID, 0, len(input.GetChannels()))
	for _, value := range input.GetChannels() {
		values = append(values, domain.ConversationID(value))
	}
	if err := s.implementation.AddUserGroupChannels(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), values); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}
func (s *Server) RemoveUserGroupChannels(ctx context.Context, input *chatv1.UserGroupChannelsRequest) (*chatv1.MutationResponse, error) {
	values := make([]domain.ConversationID, 0, len(input.GetChannels()))
	for _, value := range input.GetChannels() {
		values = append(values, domain.ConversationID(value))
	}
	if err := s.implementation.RemoveUserGroupChannels(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUserGroupId()), values); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminAddUserGroupTeams(ctx context.Context, input *chatv1.AdminUserGroupTeamsRequest) (*chatv1.MutationResponse, error) {
	teams := make([]domain.WorkspaceID, 0, len(input.GetTeamIds()))
	for _, value := range input.GetTeamIds() {
		teams = append(teams, domain.WorkspaceID(value))
	}
	if err := s.implementation.AdminAddUserGroupTeams(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserGroupID(input.GetUsergroupId()), teams); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AddCall(ctx context.Context, input *chatv1.AddCallRequest) (*chatv1.Call, error) {
	participants := make([]domain.UserID, 0, len(input.GetParticipants()))
	for _, value := range input.GetParticipants() {
		participants = append(participants, domain.UserID(value))
	}
	startedAt := time.Time{}
	if input.GetStartedAt() != 0 {
		startedAt = time.Unix(input.GetStartedAt(), 0).UTC()
	}
	value, err := s.implementation.AddCall(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetExternalUniqueId(), input.GetExternalDisplayId(), input.GetJoinUrl(), input.GetDesktopAppJoinUrl(), input.GetTitle(), startedAt, participants)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCall(value), nil
}
func (s *Server) EndCall(ctx context.Context, input *chatv1.EndCallRequest) (*chatv1.MutationResponse, error) {
	err := s.implementation.EndCall(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CallID(input.GetCallId()), input.GetDurationSeconds())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}
func (s *Server) CallInfo(ctx context.Context, input *chatv1.CallRequest) (*chatv1.Call, error) {
	value, err := s.implementation.GetCall(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CallID(input.GetCallId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCall(value), nil
}
func (s *Server) UpdateCall(ctx context.Context, input *chatv1.UpdateCallRequest) (*chatv1.Call, error) {
	value, err := s.implementation.UpdateCall(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CallID(input.GetCallId()), input.GetTitle(), input.GetJoinUrl(), input.GetDesktopAppJoinUrl())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCall(value), nil
}
func (s *Server) AddCallParticipants(ctx context.Context, input *chatv1.CallParticipantsRequest) (*chatv1.MutationResponse, error) {
	return s.callParticipants(ctx, input, true)
}
func (s *Server) RemoveCallParticipants(ctx context.Context, input *chatv1.CallParticipantsRequest) (*chatv1.MutationResponse, error) {
	return s.callParticipants(ctx, input, false)
}
func (s *Server) callParticipants(ctx context.Context, input *chatv1.CallParticipantsRequest, add bool) (*chatv1.MutationResponse, error) {
	users := make([]domain.UserID, 0, len(input.GetParticipants()))
	for _, value := range input.GetParticipants() {
		users = append(users, domain.UserID(value))
	}
	var err error
	if add {
		err = s.implementation.AddCallParticipants(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CallID(input.GetCallId()), users)
	} else {
		err = s.implementation.RemoveCallParticipants(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CallID(input.GetCallId()), users)
	}
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) UserInfo(ctx context.Context, input *chatv1.UserRequest) (*chatv1.User, error) {
	return s.userInfoProto(ctx, input)
}

func (s *Server) RemoveUser(ctx context.Context, input *chatv1.RemoveUserRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RemoveUser(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SetUserRole(ctx context.Context, input *chatv1.SetUserRoleRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetUserRole(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), domain.WorkspaceRole(input.GetRole())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SetUserExpiration(ctx context.Context, input *chatv1.SetUserExpirationRequest) (*chatv1.MutationResponse, error) {
	expiration := time.Time{}
	if input.GetExpirationTs() != 0 {
		expiration = time.Unix(input.GetExpirationTs(), 0).UTC()
	}
	if err := s.implementation.SetUserExpiration(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), expiration); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) ResetUserSessions(ctx context.Context, input *chatv1.ResetUserSessionsRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.ResetUserSessions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminRenameConversation(ctx context.Context, input *chatv1.RenameConversationRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.AdminRenameConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) AdminSetConversationArchived(ctx context.Context, input *chatv1.SetConversationArchivedRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.AdminSetConversationArchived(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetArchived())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) AdminDeleteConversation(ctx context.Context, input *chatv1.DeleteConversationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminDeleteConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminAddConversationAccessGroup(ctx context.Context, input *chatv1.ConversationAccessGroupRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminAddConversationAccessGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.UserGroupID(input.GetGroupId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminRemoveConversationAccessGroup(ctx context.Context, input *chatv1.ConversationAccessGroupRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRemoveConversationAccessGroup(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.UserGroupID(input.GetGroupId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminListConversationAccessGroups(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.ConversationAccessGroupsResponse, error) {
	groups, err := s.implementation.AdminListConversationAccessGroups(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	values := make([]string, 0, len(groups))
	for _, groupID := range groups {
		values = append(values, string(groupID))
	}
	return &chatv1.ConversationAccessGroupsResponse{GroupIds: values}, nil
}

func (s *Server) AdminInviteConversationMembers(ctx context.Context, input *chatv1.InviteConversationMembersRequest) (*chatv1.Conversation, error) {
	users := make([]domain.UserID, 0, len(input.GetUsers()))
	for _, value := range input.GetUsers() {
		users = append(users, domain.UserID(value))
	}
	result, err := s.implementation.AdminInviteConversationMembers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), users)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) AdminConvertConversationToPrivate(ctx context.Context, input *chatv1.ConvertConversationToPrivateRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.AdminConvertConversationToPrivate(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) AdminConversationTeams(ctx context.Context, input *chatv1.AdminConversationTeamsRequest) (*chatv1.AdminConversationTeamsResponse, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	teams, hasMore, nextCursor, err := s.implementation.AdminConversationTeams(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	values := make([]string, 0, len(teams))
	for _, team := range teams {
		values = append(values, string(team))
	}
	return &chatv1.AdminConversationTeamsResponse{TeamIds: values, HasMore: hasMore, NextCursor: string(nextCursor)}, nil
}

func (s *Server) AdminSetConversationTeams(ctx context.Context, input *chatv1.AdminConversationTeamsRequest) (*chatv1.MutationResponse, error) {
	teams := make([]domain.WorkspaceID, 0, len(input.GetTeamIds()))
	for _, team := range input.GetTeamIds() {
		teams = append(teams, domain.WorkspaceID(team))
	}
	if err := s.implementation.AdminSetConversationTeams(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), teams, input.GetOrgChannel()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) Emojis(ctx context.Context, input *chatv1.EmojiListRequest) (*chatv1.EmojiListResponse, error) {
	values, err := s.implementation.Emojis(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.Emoji, 0, len(values))
	for _, value := range values {
		result = append(result, &chatv1.Emoji{Name: value.Name, Url: value.URL, AliasFor: value.AliasFor})
	}
	return &chatv1.EmojiListResponse{Emojis: result}, nil
}
func (s *Server) AddEmoji(ctx context.Context, input *chatv1.EmojiMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminAddEmoji(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetValue()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}
func (s *Server) AddEmojiAlias(ctx context.Context, input *chatv1.EmojiMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminAddEmojiAlias(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetValue()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}
func (s *Server) RemoveEmoji(ctx context.Context, input *chatv1.EmojiMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRemoveEmoji(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}
func (s *Server) RenameEmoji(ctx context.Context, input *chatv1.EmojiMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRenameEmoji(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetValue()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SearchConversations(ctx context.Context, input *chatv1.SearchConversationsRequest) (*chatv1.ConversationPage, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	value, err := s.implementation.AdminSearchConversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery(), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPage(value), nil
}

func (s *Server) SetWorkspaceName(ctx context.Context, input *chatv1.SetWorkspaceNameRequest) (*chatv1.Workspace, error) {
	value, err := s.implementation.AdminSetWorkspaceName(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(value), nil
}

func (s *Server) SetWorkspaceDescription(ctx context.Context, input *chatv1.SetWorkspaceDescriptionRequest) (*chatv1.Workspace, error) {
	value, err := s.implementation.AdminSetWorkspaceDescription(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetDescription())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(value), nil
}

func (s *Server) SetWorkspaceDiscoverability(ctx context.Context, input *chatv1.SetWorkspaceDiscoverabilityRequest) (*chatv1.Workspace, error) {
	value, err := s.implementation.AdminSetWorkspaceDiscoverability(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkspaceDiscoverability(input.GetDiscoverability()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(value), nil
}

func (s *Server) SetWorkspaceIcon(ctx context.Context, input *chatv1.SetWorkspaceIconRequest) (*chatv1.Workspace, error) {
	value, err := s.implementation.AdminSetWorkspaceIcon(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetImageUrl())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(value), nil
}

func (s *Server) SetWorkspaceDefaultChannels(ctx context.Context, input *chatv1.SetWorkspaceDefaultChannelsRequest) (*chatv1.Workspace, error) {
	channels := make([]domain.ConversationID, 0, len(input.GetChannelIds()))
	for _, channel := range input.GetChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	value, err := s.implementation.AdminSetWorkspaceDefaultChannels(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), channels)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(value), nil
}

func (s *Server) GetConversationPrefs(ctx context.Context, input *chatv1.ConversationPrefsRequest) (*chatv1.ConversationPrefs, error) {
	value, err := s.implementation.AdminGetConversationPrefs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPrefs(value), nil
}

func (s *Server) SetConversationPrefs(ctx context.Context, input *chatv1.SetConversationPrefsRequest) (*chatv1.ConversationPrefs, error) {
	value, err := s.implementation.AdminSetConversationPrefs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetPrefs().GetConversationId()), decodeProtoConversationPrefsValue(input.GetPrefs()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPrefs(value), nil
}

func (s *Server) AdminTeamUsers(ctx context.Context, input *chatv1.AdminTeamUsersRequest) (*chatv1.UserPage, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	value, err := s.implementation.AdminTeamUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkspaceRole(input.GetRole()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserPage(value), nil
}

func (s *Server) AdminApproveInviteRequest(ctx context.Context, input *chatv1.InviteRequestMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminApproveInviteRequest(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.InviteRequestID(input.GetInviteRequestId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminInviteUser(ctx context.Context, input *chatv1.AdminInviteUserRequest) (*chatv1.MutationResponse, error) {
	channels := make([]domain.ConversationID, 0, len(input.GetChannelIds()))
	for _, channel := range input.GetChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	var expiration time.Time
	if input.GetGuestExpirationAt() != 0 {
		expiration = time.Unix(input.GetGuestExpirationAt(), 0).UTC()
	}
	if err := s.implementation.AdminInviteUser(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetEmail(), channels, input.GetCustomMessage(), input.GetRealName(), input.GetResend(), input.GetRestricted(), input.GetUltraRestricted(), expiration); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminCreateUser(ctx context.Context, input *chatv1.AdminCreateUserRequest) (*chatv1.User, error) {
	value, err := s.implementation.AdminCreateUser(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetEmail(), input.GetRealName(), domain.WorkspaceRole(input.GetRole()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(value), nil
}

func (s *Server) AdminListUsers(ctx context.Context, input *chatv1.AdminUsersRequest) (*chatv1.AdminUserPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	value, err := s.implementation.AdminListUsers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAdminUserPage(value), nil
}

func (s *Server) AdminAssignUser(ctx context.Context, input *chatv1.AdminAssignUserRequest) (*chatv1.MutationResponse, error) {
	channels := make([]domain.ConversationID, 0, len(input.GetChannelIds()))
	for _, channel := range input.GetChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	if err := s.implementation.AdminAssignUser(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), channels); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminDenyInviteRequest(ctx context.Context, input *chatv1.InviteRequestMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminDenyInviteRequest(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.InviteRequestID(input.GetInviteRequestId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminListInviteRequests(ctx context.Context, input *chatv1.InviteRequestsRequest) (*chatv1.InviteRequestPage, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.AdminListInviteRequests(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.InviteRequestStatus(input.GetStatus()), request)
	if err != nil {
		return nil, mapError(err)
	}
	values := make([]*chatv1.InviteRequest, 0, len(page.Requests))
	for _, value := range page.Requests {
		values = append(values, encodeProtoInviteRequest(value))
	}
	return &chatv1.InviteRequestPage{Requests: values, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) AdminApproveApp(ctx context.Context, input *chatv1.AppApprovalMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminApproveApp(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.AppRequestID(input.GetRequestId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminRestrictApp(ctx context.Context, input *chatv1.AppApprovalMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRestrictApp(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.AppRequestID(input.GetRequestId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminListApps(ctx context.Context, input *chatv1.AppApprovalsRequest) (*chatv1.AppApprovalPage, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.AdminListApps(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppApprovalStatus(input.GetStatus()), request)
	if err != nil {
		return nil, mapError(err)
	}
	values := make([]*chatv1.AppApproval, 0, len(page.Apps))
	for _, value := range page.Apps {
		values = append(values, encodeProtoAppApproval(value))
	}
	return &chatv1.AppApprovalPage{Apps: values, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) LookupToken(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.TokenRecord, error) {
	return s.lookupTokenProto(ctx, input)
}

func (s *Server) LookupAppToken(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AppTokenRecord, error) {
	if input == nil || input.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	value, err := s.implementation.LookupAppToken(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppTokenRecord{AppId: string(value.AppID), Scopes: value.Scopes, Revoked: value.Revoked}, nil
}

func (s *Server) CreateAppInstallation(ctx context.Context, input *chatv1.AppInstallationRequest) (*chatv1.AuthRevokeResponse, error) {
	if input == nil || input.GetInstallation() == nil {
		return nil, status.Error(codes.InvalidArgument, "installation is required")
	}
	value := input.GetInstallation()
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "installation created_at is invalid")
	}
	if err := s.implementation.CreateAppInstallation(ctx, domain.AppInstallation{AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Enabled: value.GetEnabled(), CreatedAt: created}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) ListAppInstallations(ctx context.Context, input *chatv1.AppInstallationRequest) (*chatv1.AppInstallationsResponse, error) {
	if input == nil || input.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	values, err := s.implementation.ListAppInstallations(ctx, domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.AppInstallationsResponse{Installations: make([]*chatv1.AppInstallation, 0, len(values))}
	for _, value := range values {
		result.Installations = append(result.Installations, &chatv1.AppInstallation{AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), Enabled: value.Enabled, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return result, nil
}

func (s *Server) LookupSession(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.SessionRecord, error) {
	return s.lookupSessionProto(ctx, input)
}

func (s *Server) CreateSession(ctx context.Context, input *chatv1.CreateSessionRequest) (*chatv1.AuthRevokeResponse, error) {
	if input == nil || input.GetSession() == nil {
		return nil, status.Error(codes.InvalidArgument, "session is required")
	}
	record, err := decodeProtoSession(input.GetSession())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.implementation.CreateSession(ctx, input.GetToken(), record); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) GetAuthMethod(ctx context.Context, input *chatv1.AuthMethodRequest) (*chatv1.AuthMethod, error) {
	if input == nil || input.GetWorkspaceId() == "" || input.GetProvider() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace and provider are required")
	}
	value, err := s.implementation.GetAuthMethod(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetProvider())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthMethod{WorkspaceId: string(value.WorkspaceID), Provider: value.Provider, Enabled: value.Enabled}, nil
}

func (s *Server) SetAuthMethod(ctx context.Context, input *chatv1.AuthMethodRequest) (*chatv1.AuthRevokeResponse, error) {
	if input == nil || input.GetWorkspaceId() == "" || input.GetProvider() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace and provider are required")
	}
	if err := s.implementation.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: domain.WorkspaceID(input.GetWorkspaceId()), Provider: input.GetProvider(), Enabled: input.GetEnabled()}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) GetExternalIdentity(ctx context.Context, input *chatv1.ExternalIdentityRequest) (*chatv1.ExternalIdentity, error) {
	if input == nil || input.GetWorkspaceId() == "" || input.GetProvider() == "" || input.GetSubject() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace, provider, and subject are required")
	}
	value, err := s.implementation.GetExternalIdentity(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetProvider(), input.GetSubject())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ExternalIdentity{WorkspaceId: string(value.WorkspaceID), Provider: value.Provider, Subject: value.Subject, UserId: string(value.UserID)}, nil
}

func (s *Server) CreateExternalIdentity(ctx context.Context, input *chatv1.ExternalIdentityRequest) (*chatv1.AuthRevokeResponse, error) {
	if input == nil || input.GetWorkspaceId() == "" || input.GetProvider() == "" || input.GetSubject() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "external identity is incomplete")
	}
	if err := s.implementation.CreateExternalIdentity(ctx, domain.ExternalIdentity{WorkspaceID: domain.WorkspaceID(input.GetWorkspaceId()), Provider: input.GetProvider(), Subject: input.GetSubject(), UserID: domain.UserID(input.GetUserId())}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) RevokeSession(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AuthRevokeResponse, error) {
	return s.revokeSessionProto(ctx, input)
}

func (s *Server) RevokeToken(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AuthRevokeResponse, error) {
	return s.revokeTokenProto(ctx, input)
}

func (s *Server) OpenConversation(ctx context.Context, input *chatv1.OpenConversationRequest) (*chatv1.Conversation, error) {
	return s.openConversationProto(ctx, input)
}

func (s *Server) CreateConversation(ctx context.Context, input *chatv1.CreateConversationRequest) (*chatv1.Conversation, error) {
	return s.createConversationProto(ctx, input)
}

func (s *Server) JoinConversation(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.Conversation, error) {
	return s.joinConversationProto(ctx, input)
}

func (s *Server) InviteConversationMembers(ctx context.Context, input *chatv1.InviteConversationMembersRequest) (*chatv1.Conversation, error) {
	return s.inviteConversationMembersProto(ctx, input)
}

func (s *Server) LeaveConversation(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.MutationResponse, error) {
	return s.leaveConversationProto(ctx, input)
}

func (s *Server) KickConversationMember(ctx context.Context, input *chatv1.KickConversationMemberRequest) (*chatv1.MutationResponse, error) {
	return s.kickConversationMemberProto(ctx, input)
}

func (s *Server) RenameConversation(ctx context.Context, input *chatv1.RenameConversationRequest) (*chatv1.Conversation, error) {
	return s.renameConversationProto(ctx, input)
}

func (s *Server) SetConversationTopic(ctx context.Context, input *chatv1.SetConversationTopicRequest) (*chatv1.Conversation, error) {
	return s.setConversationTopicProto(ctx, input)
}

func (s *Server) SetConversationPurpose(ctx context.Context, input *chatv1.SetConversationPurposeRequest) (*chatv1.Conversation, error) {
	return s.setConversationPurposeProto(ctx, input)
}

func (s *Server) SetConversationArchived(ctx context.Context, input *chatv1.SetConversationArchivedRequest) (*chatv1.Conversation, error) {
	return s.setConversationArchivedProto(ctx, input)
}

func (s *Server) ConversationInfo(ctx context.Context, input *chatv1.ConversationInfoRequest) (*chatv1.Conversation, error) {
	return s.conversationInfoProto(ctx, input)
}

func (s *Server) Conversations(ctx context.Context, input *chatv1.ConversationsRequest) (*chatv1.ConversationPage, error) {
	return s.conversationsProto(ctx, input)
}

func (s *Server) Users(ctx context.Context, input *chatv1.UsersRequest) (*chatv1.UserPage, error) {
	return s.usersProto(ctx, input)
}

func (s *Server) ConversationMembers(ctx context.Context, input *chatv1.ConversationMembersRequest) (*chatv1.UserPage, error) {
	return s.conversationMembersProto(ctx, input)
}

func (s *Server) WorkspaceInfo(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.Workspace, error) {
	return s.workspaceInfoProto(ctx, input)
}

func (s *Server) AdminCreateWorkspace(ctx context.Context, input *chatv1.AdminCreateWorkspaceRequest) (*chatv1.Workspace, error) {
	value, err := s.implementation.AdminCreateWorkspace(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTeamDomain(), input.GetTeamName(), input.GetTeamDescription(), domain.WorkspaceDiscoverability(input.GetTeamDiscoverability()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(value), nil
}

func (s *Server) RequestAppPermissions(ctx context.Context, input *chatv1.AppPermissionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RequestAppPermissions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), input.GetScopes(), input.GetTriggerId()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func encodeProtoView(value domain.View) *chatv1.View {
	return &chatv1.View{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Type: value.Type, ExternalId: value.ExternalID, Payload: value.Payload, Hash: value.Hash, RootViewId: string(value.RootViewID), PreviousViewId: string(value.PreviousViewID), CreatedAtUnixNano: value.CreatedAt.UnixNano(), UpdatedAtUnixNano: value.UpdatedAt.UnixNano()}
}

func (s *Server) OpenView(ctx context.Context, input *chatv1.OpenViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.OpenView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggerId(), input.GetPayload())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) PublishView(ctx context.Context, input *chatv1.PublishViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.PublishView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), input.GetPayload(), input.GetHash())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) PushView(ctx context.Context, input *chatv1.PushViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.PushView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggerId(), input.GetPayload())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) UpdateView(ctx context.Context, input *chatv1.UpdateViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.UpdateView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetViewId(), input.GetExternalId(), input.GetPayload(), input.GetHash())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) StepCompleted(ctx context.Context, input *chatv1.WorkflowStepRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.WorkflowStepCompleted(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetWorkflowStepExecuteId(), input.GetOutputs()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) StepFailed(ctx context.Context, input *chatv1.WorkflowStepRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.WorkflowStepFailed(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetWorkflowStepExecuteId(), input.GetError()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) UpdateStep(ctx context.Context, input *chatv1.WorkflowStepUpdateRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.WorkflowUpdateStep(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetWorkflowStepEditId(), input.GetInputs(), input.GetOutputs(), input.GetStepName(), input.GetStepImageUrl()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) OpenDialog(ctx context.Context, input *chatv1.OpenDialogRequest) (*chatv1.DialogMutationResponse, error) {
	if err := s.implementation.OpenDialog(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggerId(), input.GetPayload()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DialogMutationResponse{Ok: true}, nil
}

func encodeProtoBot(value domain.Bot) *chatv1.Bot {
	return &chatv1.Bot{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), AppId: string(value.AppID), UserId: string(value.UserID), Name: value.Name, Image_36: value.Image36, Image_48: value.Image48, Image_72: value.Image72, Deleted: value.Deleted, UpdatedAt: value.UpdatedAt.Unix()}
}

func decodeProtoBot(value *chatv1.Bot) (domain.Bot, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetName() == "" {
		return domain.Bot{}, errors.New("typed bot response is incomplete")
	}
	return domain.Bot{ID: domain.BotID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()), UserID: domain.UserID(value.GetUserId()), Name: value.GetName(), Image36: value.GetImage_36(), Image48: value.GetImage_48(), Image72: value.GetImage_72(), Deleted: value.GetDeleted(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC()}, nil
}

func (s *Server) BotInfo(ctx context.Context, input *chatv1.BotInfoRequest) (*chatv1.Bot, error) {
	value, err := s.implementation.BotInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.BotID(input.GetBotId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBot(value), nil
}

func (s *Server) Exchange(ctx context.Context, input *chatv1.MigrationExchangeRequest) (*chatv1.MigrationExchangeResponse, error) {
	ids := make([]domain.UserID, 0, len(input.GetUserIds()))
	for _, id := range input.GetUserIds() {
		ids = append(ids, domain.UserID(id))
	}
	value, err := s.implementation.MigrationExchange(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids, input.GetToOld())
	if err != nil {
		return nil, mapError(err)
	}
	mapping := make(map[string]string, len(value.UserIDMap))
	for key, item := range value.UserIDMap {
		mapping[string(key)] = string(item)
	}
	invalid := make([]string, 0, len(value.InvalidUserIDs))
	for _, item := range value.InvalidUserIDs {
		invalid = append(invalid, string(item))
	}
	return &chatv1.MigrationExchangeResponse{WorkspaceId: string(value.WorkspaceID), UserIdMap: mapping, InvalidUserIds: invalid}, nil
}

func (s *Server) DisconnectShared(ctx context.Context, input *chatv1.DisconnectSharedConversationRequest) (*chatv1.MutationResponse, error) {
	teams := make([]domain.WorkspaceID, 0, len(input.GetLeavingTeamIds()))
	for _, team := range input.GetLeavingTeamIds() {
		teams = append(teams, domain.WorkspaceID(team))
	}
	if err := s.implementation.AdminDisconnectSharedConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), teams); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) ListOriginalConnectedChannelInfo(ctx context.Context, input *chatv1.ConnectedChannelInfoRequest) (*chatv1.ConnectedChannelInfoResponse, error) {
	channels := make([]domain.ConversationID, 0, len(input.GetChannelIds()))
	for _, channel := range input.GetChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	teams := make([]domain.WorkspaceID, 0, len(input.GetTeamIds()))
	for _, team := range input.GetTeamIds() {
		teams = append(teams, domain.WorkspaceID(team))
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	values, more, next, err := s.implementation.AdminConnectedChannelInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), channels, teams, request)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.ConnectedChannelInfo, 0, len(values))
	for _, value := range values {
		teamIDs := make([]string, 0, len(value.InternalTeamIDs))
		for _, team := range value.InternalTeamIDs {
			teamIDs = append(teamIDs, string(team))
		}
		items = append(items, &chatv1.ConnectedChannelInfo{ChannelId: string(value.ChannelID), InternalTeamIds: teamIDs, OriginalConnectedChannelId: string(value.OriginalConnectedChannelID), OriginalConnectedHostId: string(value.OriginalConnectedHostID)})
	}
	return &chatv1.ConnectedChannelInfoResponse{Channels: items, HasMore: more, NextCursor: string(next)}, nil
}

func (s *Server) ExchangeOAuth(ctx context.Context, input *chatv1.OAuthExchangeRequest) (*chatv1.OAuthToken, error) {
	value, err := s.implementation.OAuthExchange(ctx, input.GetClientId(), input.GetClientSecret(), input.GetCode(), input.GetRedirectUri())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.OAuthToken{AccessToken: value.AccessToken, ClientId: value.ClientID, AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Scopes: value.Scopes, TokenType: value.TokenType}, nil
}

func (s *Server) TeamBillableInfo(ctx context.Context, input *chatv1.BillableInfoRequest) (*chatv1.BillableInfo, error) {
	value, err := s.implementation.TeamBillableInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	users := make([]*chatv1.BillableUser, 0, len(value.Users))
	for _, item := range value.Users {
		users = append(users, &chatv1.BillableUser{UserId: string(item.UserID), BillingActive: item.BillingActive})
	}
	return &chatv1.BillableInfo{Users: users}, nil
}

func (s *Server) Post(ctx context.Context, input *chatv1.PostRequest) (*chatv1.Message, error) {
	return s.postProto(ctx, input)
}

func (s *Server) Unfurl(ctx context.Context, input *chatv1.UnfurlRequest) (*chatv1.Message, error) {
	value, err := s.implementation.Unfurl(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetUnfurls())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) PostEphemeral(ctx context.Context, input *chatv1.PostEphemeralRequest) (*chatv1.EphemeralMessage, error) {
	value, err := s.implementation.PostEphemeralWithBlocksAndAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.UserID(input.GetRecipientId()), input.GetText(), input.GetBlocks(), input.GetAttachments())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoEphemeralMessage(value), nil
}

func (s *Server) RecordAccess(ctx context.Context, input *chatv1.RecordAccessRequest) (*chatv1.AccessMutationResponse, error) {
	if err := s.implementation.RecordAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetIp(), input.GetUserAgent()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AccessMutationResponse{Ok: true}, nil
}
func (s *Server) AccessLogs(ctx context.Context, input *chatv1.AccessLogsRequest) (*chatv1.AccessLogsResponse, error) {
	var before time.Time
	if input.GetBefore() != 0 {
		before = time.Unix(input.GetBefore(), 0).UTC()
	}
	values, hasMore, err := s.implementation.ListAccessLogs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), before, int(input.GetLimit()), int(input.GetPage()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.AccessLogsResponse{Logs: make([]*chatv1.AccessLog, 0, len(values)), HasMore: hasMore}
	for _, value := range values {
		result.Logs = append(result.Logs, encodeProtoAccessLog(value))
	}
	return result, nil
}

func (s *Server) IntegrationLogs(ctx context.Context, input *chatv1.IntegrationLogsRequest) (*chatv1.IntegrationLogsResponse, error) {
	value, err := s.implementation.IntegrationLogs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetAppId(), input.GetChangeType(), input.GetServiceId(), input.GetUserFilter(), int(input.GetCount()), int(input.GetPage()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.IntegrationLogsResponse{Page: int32(value.Page), Pages: int32(value.Pages), Total: int32(value.Total), Logs: make([]*chatv1.IntegrationLog, 0, len(value.Logs))}
	for _, item := range value.Logs {
		result.Logs = append(result.Logs, &chatv1.IntegrationLog{AppId: string(item.AppID), AppType: item.AppType, ChangeType: item.ChangeType, ChannelId: string(item.ChannelID), DateUnix: item.Date.Unix(), Scope: item.Scope, ServiceId: item.ServiceID, ServiceType: item.ServiceType, UserId: string(item.UserID), UserName: item.UserName})
	}
	return result, nil
}

func (s *Server) CreateConnection(ctx context.Context, input *chatv1.RTMConnectionRequest) (*chatv1.RTMConnection, error) {
	value, err := s.implementation.CreateRTMConnection(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRTMConnection(value), nil
}

func (s *Server) ConsumeConnection(ctx context.Context, input *chatv1.RTMConnectionIDRequest) (*chatv1.RTMConnection, error) {
	value, err := s.implementation.ConsumeRTMConnection(ctx, input.GetId())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRTMConnection(value), nil
}

func (s *Server) CreateSocketModeConnection(ctx context.Context, input *chatv1.SocketModeConnectionRequest) (*chatv1.SocketModeConnection, error) {
	if input == nil || input.GetId() == "" || input.GetAppId() == "" || input.GetExpiresAtUnixNano() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "Socket Mode connection fields are required")
	}
	value := domain.SocketModeConnection{ID: input.GetId(), AppID: domain.AppID(input.GetAppId()), ExpiresAt: time.Unix(0, input.GetExpiresAtUnixNano()).UTC()}
	if err := s.implementation.CreateSocketModeConnection(ctx, value); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: value.ID, AppId: string(value.AppID), ExpiresAtUnixNano: value.ExpiresAt.UnixNano()}, nil
}

func (s *Server) ConsumeSocketModeConnection(ctx context.Context, input *chatv1.RTMConnectionIDRequest) (*chatv1.SocketModeConnection, error) {
	if input == nil || input.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "connection ID is required")
	}
	value, err := s.implementation.ConsumeSocketModeConnection(ctx, input.GetId())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: value.ID, AppId: string(value.AppID), ExpiresAtUnixNano: value.ExpiresAt.UnixNano()}, nil
}

func (s *Server) RenewSocketModeConnection(ctx context.Context, input *chatv1.SocketModeConnectionRenewalRequest) (*chatv1.SocketModeConnection, error) {
	if input == nil || input.GetId() == "" || input.GetExpiresAtUnixNano() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "connection renewal fields are required")
	}
	if err := s.implementation.RenewSocketModeConnection(ctx, input.GetId(), time.Unix(0, input.GetExpiresAtUnixNano()).UTC()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: input.GetId(), ExpiresAtUnixNano: input.GetExpiresAtUnixNano()}, nil
}

func (s *Server) ReleaseSocketModeConnection(ctx context.Context, input *chatv1.RTMConnectionIDRequest) (*chatv1.SocketModeConnection, error) {
	if input == nil || input.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "connection ID is required")
	}
	if err := s.implementation.ReleaseSocketModeConnection(ctx, input.GetId()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: input.GetId()}, nil
}

func (s *Server) CountSocketModeConnections(ctx context.Context, input *chatv1.SocketModeCursorRequest) (*chatv1.SocketModeConnectionCount, error) {
	if input == nil || input.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app ID is required")
	}
	count, err := s.implementation.CountSocketModeConnections(ctx, domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnectionCount{AppId: input.GetAppId(), Count: int32(count)}, nil
}

func (s *Server) GetSocketModeCursor(ctx context.Context, input *chatv1.SocketModeCursorRequest) (*chatv1.SocketModeCursor, error) {
	if input == nil || input.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app ID is required")
	}
	cursor, err := s.implementation.GetSocketModeCursor(ctx, domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeCursor{AppId: input.GetAppId(), Sequence: cursor}, nil
}

func (s *Server) SetSocketModeCursor(ctx context.Context, input *chatv1.SocketModeCursorRequest) (*chatv1.SocketModeCursor, error) {
	if input == nil || input.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app ID is required")
	}
	if err := s.implementation.SetSocketModeCursor(ctx, domain.AppID(input.GetAppId()), input.GetSequence()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeCursor{AppId: input.GetAppId(), Sequence: input.GetSequence()}, nil
}

func (s *Server) RecordSocketModeResponse(ctx context.Context, input *chatv1.SocketModeResponseRequest) (*chatv1.SocketModeResponse, error) {
	if input == nil || input.GetAppId() == "" || input.GetEnvelopeId() == "" || input.GetPayload() == "" || input.GetReceivedAtUnixNano() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "Socket Mode response fields are required")
	}
	value := domain.SocketModeResponse{AppID: domain.AppID(input.GetAppId()), EnvelopeID: input.GetEnvelopeId(), Payload: input.GetPayload(), ReceivedAt: time.Unix(0, input.GetReceivedAtUnixNano()).UTC()}
	if err := s.implementation.RecordSocketModeResponse(ctx, value); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponse{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID, Payload: value.Payload, ReceivedAtUnixNano: value.ReceivedAt.UnixNano()}, nil
}

func (s *Server) ClaimSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseLeaseRequest) (*chatv1.SocketModeResponseBatch, error) {
	if input == nil || input.GetAppId() == "" || input.GetOwner() == "" || input.GetLimit() < 1 || input.GetLeaseNanos() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "Socket Mode response lease fields are required")
	}
	values, err := s.implementation.ClaimSocketModeResponses(ctx, domain.AppID(input.GetAppId()), input.GetOwner(), int(input.GetLimit()), time.Duration(input.GetLeaseNanos()))
	if err != nil {
		return nil, mapError(err)
	}
	responses := make([]*chatv1.SocketModeResponse, 0, len(values))
	for _, value := range values {
		responses = append(responses, &chatv1.SocketModeResponse{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID, Payload: value.Payload, ReceivedAtUnixNano: value.ReceivedAt.UnixNano()})
	}
	return &chatv1.SocketModeResponseBatch{Responses: responses}, nil
}

func (s *Server) RenewSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseRenewRequest) (*chatv1.SocketModeResponseBatch, error) {
	if input == nil || input.GetOwner() == "" || len(input.GetResponses()) == 0 || input.GetLeaseNanos() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "Socket Mode response renewal fields are required")
	}
	values, err := socketModeResponseKeys(input.GetResponses())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.implementation.RenewSocketModeResponses(ctx, input.GetOwner(), values, time.Duration(input.GetLeaseNanos())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponseBatch{}, nil
}

func (s *Server) AckSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseAckRequest) (*chatv1.SocketModeResponseBatch, error) {
	if input == nil || input.GetOwner() == "" || len(input.GetResponses()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Socket Mode response acknowledgement fields are required")
	}
	values, err := socketModeResponseKeys(input.GetResponses())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.implementation.AckSocketModeResponses(ctx, input.GetOwner(), values); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponseBatch{}, nil
}

func (s *Server) ReleaseSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseReleaseRequest) (*chatv1.SocketModeResponseBatch, error) {
	if input == nil || input.GetOwner() == "" || len(input.GetResponses()) == 0 || input.GetRetryAtUnixNano() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "Socket Mode response release fields are required")
	}
	values, err := socketModeResponseKeys(input.GetResponses())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.implementation.ReleaseSocketModeResponses(ctx, input.GetOwner(), values, time.Unix(0, input.GetRetryAtUnixNano()).UTC()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponseBatch{}, nil
}

func socketModeResponseKeys(keys []*chatv1.SocketModeResponseKey) ([]domain.SocketModeResponse, error) {
	values := make([]domain.SocketModeResponse, 0, len(keys))
	for _, key := range keys {
		if key == nil || key.GetAppId() == "" || key.GetEnvelopeId() == "" {
			return nil, errors.New("Socket Mode response keys are required")
		}
		values = append(values, domain.SocketModeResponse{AppID: domain.AppID(key.GetAppId()), EnvelopeID: key.GetEnvelopeId()})
	}
	return values, nil
}

func (s *Server) Update(ctx context.Context, input *chatv1.UpdateRequest) (*chatv1.Message, error) {
	return s.updateProto(ctx, input)
}

func (s *Server) Delete(ctx context.Context, input *chatv1.DeleteRequest) (*chatv1.Message, error) {
	return s.deleteProto(ctx, input)
}

func (s *Server) Permalink(ctx context.Context, input *chatv1.PermalinkRequest) (*chatv1.PermalinkResponse, error) {
	return s.permalinkProto(ctx, input)
}

func (s *Server) History(ctx context.Context, input *chatv1.HistoryRequest) (*chatv1.MessagePage, error) {
	return s.historyProto(ctx, input)
}

func (s *Server) Replies(ctx context.Context, input *chatv1.RepliesRequest) (*chatv1.MessagePage, error) {
	return s.repliesProto(ctx, input)
}

func (s *Server) Search(ctx context.Context, input *chatv1.SearchRequest) (*chatv1.MessagePage, error) {
	return s.searchProto(ctx, input)
}

func (s *Server) CreateList(ctx context.Context, input *chatv1.CreateListRequest) (*chatv1.ListResponse, error) {
	value, err := s.implementation.CreateList(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetDescriptionBlocks(), input.GetSchema(), domain.ListID(input.GetCopyFromListId()), input.GetIncludeCopiedRecords(), input.GetTodoMode())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListResponse{Ok: true, List: encodeProtoList(value)}, nil
}

func (s *Server) UpdateList(ctx context.Context, input *chatv1.UpdateListRequest) (*chatv1.ListResponse, error) {
	value, err := s.implementation.UpdateList(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), input.GetName(), input.GetDescriptionBlocks(), input.GetTodoMode(), input.GetTodoModeSet())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListResponse{Ok: true, List: encodeProtoList(value)}, nil
}

func (s *Server) CreateListItem(ctx context.Context, input *chatv1.CreateListItemRequest) (*chatv1.ListItemResponse, error) {
	value, err := s.implementation.CreateListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetParentItemId()), input.GetFields())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListItemResponse{Ok: true, Item: encodeProtoListItem(value)}, nil
}

func (s *Server) GetListItem(ctx context.Context, input *chatv1.ListItemRequest) (*chatv1.ListItemResponse, error) {
	value, err := s.implementation.GetListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListItemResponse{Ok: true, Item: encodeProtoListItem(value)}, nil
}

func (s *Server) ListItems(ctx context.Context, input *chatv1.ListItemsRequest) (*chatv1.ListItemsResponse, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	value, err := s.implementation.ListItems(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), request, input.GetArchived())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListItemsResponse{Ok: true, Page: encodeProtoListItemPage(value)}, nil
}

func (s *Server) UpdateListItem(ctx context.Context, input *chatv1.UpdateListItemRequest) (*chatv1.ListItemResponse, error) {
	value, err := s.implementation.UpdateListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()), input.GetFields(), input.GetArchived())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListItemResponse{Ok: true, Item: encodeProtoListItem(value)}, nil
}

func (s *Server) UpdateListCells(ctx context.Context, input *chatv1.UpdateListItemRequest) (*chatv1.ListItemsResponse, error) {
	values, err := s.implementation.UpdateListCells(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), input.GetFields())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListItemsResponse{Ok: true, Page: encodeProtoListItemPage(domain.ListItemPage{Items: values})}, nil
}

func (s *Server) DeleteListItems(ctx context.Context, input *chatv1.DeleteListItemsRequest) (*chatv1.ListOKResponse, error) {
	ids := make([]domain.ListItemID, 0, len(input.GetItemIds()))
	for _, id := range input.GetItemIds() {
		ids = append(ids, domain.ListItemID(id))
	}
	if err := s.implementation.DeleteListItems(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), ids); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListOKResponse{Ok: true}, nil
}

func (s *Server) SetListAccess(ctx context.Context, input *chatv1.ListAccessRequest) (*chatv1.ListOKResponse, error) {
	if err := s.implementation.SetListAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), input.GetAccess(), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListOKResponse{Ok: true}, nil
}

func (s *Server) DeleteListAccess(ctx context.Context, input *chatv1.ListAccessRequest) (*chatv1.ListOKResponse, error) {
	if err := s.implementation.DeleteListAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListOKResponse{Ok: true}, nil
}

func (s *Server) StartListDownload(ctx context.Context, input *chatv1.ListDownloadRequest) (*chatv1.ListDownloadResponse, error) {
	value, err := s.implementation.StartListDownload(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), input.GetIncludeArchived())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListDownloadResponse{Ok: true, Download: encodeProtoListDownload(value)}, nil
}

func (s *Server) GetListDownload(ctx context.Context, input *chatv1.ListDownloadRequest) (*chatv1.ListDownloadResponse, error) {
	value, err := s.implementation.GetListDownload(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListDownloadID(input.GetJobId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListDownloadResponse{Ok: true, Download: encodeProtoListDownload(value)}, nil
}

func (s *Server) FileInfo(ctx context.Context, input *chatv1.FileRequest) (*chatv1.File, error) {
	return s.fileInfoProto(ctx, input)
}

func (s *Server) SharePublicURL(ctx context.Context, input *chatv1.PublicFileRequest) (*chatv1.File, error) {
	value, err := s.implementation.ShareFilePublic(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFile(value), nil
}

func (s *Server) RevokePublicURL(ctx context.Context, input *chatv1.PublicFileRequest) (*chatv1.File, error) {
	value, err := s.implementation.RevokeFilePublic(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFile(value), nil
}

func (s *Server) DeleteFile(ctx context.Context, input *chatv1.FileRequest) (*chatv1.DeleteFileResponse, error) {
	return s.deleteFileProto(ctx, input)
}

func (s *Server) DeleteFileComment(ctx context.Context, input *chatv1.FileCommentDeleteRequest) (*chatv1.DeleteFileResponse, error) {
	if err := s.implementation.DeleteFileComment(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()), domain.FileCommentID(input.GetCommentId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DeleteFileResponse{Ok: true}, nil
}

func (s *Server) Files(ctx context.Context, input *chatv1.FilesRequest) (*chatv1.FilePage, error) {
	return s.filesProto(ctx, input)
}

func (s *Server) AddRemoteFile(ctx context.Context, input *chatv1.AddRemoteFileRequest) (*chatv1.RemoteFile, error) {
	value, err := s.implementation.AddRemoteFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.RemoteFile{ExternalID: input.GetExternalId(), Title: input.GetTitle(), FileType: input.GetFileType(), ExternalURL: input.GetExternalUrl(), PreviewImage: input.GetPreviewImage(), IndexableContents: input.GetIndexableContents()})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRemoteFile(value), nil
}

func (s *Server) RemoteFileInfo(ctx context.Context, input *chatv1.RemoteFileRequest) (*chatv1.RemoteFile, error) {
	value, err := s.implementation.RemoteFileInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.RemoteFileLookup{ID: domain.FileID(input.GetFileId()), ExternalID: input.GetExternalId()})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRemoteFile(value), nil
}

func (s *Server) RemoteFiles(ctx context.Context, input *chatv1.RemoteFilesRequest) (*chatv1.RemoteFilePage, error) {
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	value, err := s.implementation.RemoteFiles(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRemoteFilePage(value), nil
}

func (s *Server) RemoveRemoteFile(ctx context.Context, input *chatv1.RemoteFileRequest) (*chatv1.DeleteFileResponse, error) {
	err := s.implementation.RemoveRemoteFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.RemoteFileLookup{ID: domain.FileID(input.GetFileId()), ExternalID: input.GetExternalId()})
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DeleteFileResponse{Ok: true}, nil
}

func (s *Server) ShareRemoteFile(ctx context.Context, input *chatv1.ShareRemoteFileRequest) (*chatv1.RemoteFile, error) {
	channels := make([]domain.ConversationID, 0, len(input.GetChannels()))
	for _, channel := range input.GetChannels() {
		channels = append(channels, domain.ConversationID(channel))
	}
	value, err := s.implementation.ShareRemoteFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.RemoteFileLookup{ID: domain.FileID(input.GetFileId()), ExternalID: input.GetExternalId()}, channels)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRemoteFile(value), nil
}

func (s *Server) UpdateRemoteFile(ctx context.Context, input *chatv1.UpdateRemoteFileRequest) (*chatv1.RemoteFile, error) {
	update := domain.RemoteFileUpdate{Lookup: domain.RemoteFileLookup{ID: domain.FileID(input.GetFileId()), ExternalID: input.GetExternalId()}, Title: input.GetTitle(), FileType: input.GetFileType(), ExternalURL: input.GetExternalUrl(), PreviewImage: input.GetPreviewImage(), IndexableContents: input.GetIndexableContents()}
	for _, field := range input.GetUpdateFields() {
		switch field {
		case "title":
			update.SetTitle = true
		case "file_type":
			update.SetFileType = true
		case "external_url":
			update.SetExternalURL = true
		case "preview_image":
			update.SetPreviewImage = true
		case "indexable_contents":
			update.SetIndexableData = true
		default:
			return nil, status.Error(codes.InvalidArgument, "unknown remote file update field")
		}
	}
	value, err := s.implementation.UpdateRemoteFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), update)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRemoteFile(value), nil
}

func (s *Server) MarkRead(ctx context.Context, input *chatv1.MarkReadRequest) (*chatv1.ReadCursor, error) {
	return s.markReadProto(ctx, input)
}

func (s *Server) AddReaction(ctx context.Context, input *chatv1.ReactionRequest) (*chatv1.MutationResponse, error) {
	return s.addReactionProto(ctx, input)
}

func (s *Server) RemoveReaction(ctx context.Context, input *chatv1.ReactionRequest) (*chatv1.MutationResponse, error) {
	return s.removeReactionProto(ctx, input)
}

func (s *Server) Reactions(ctx context.Context, input *chatv1.ReactionPageRequest) (*chatv1.ReactionPage, error) {
	return s.reactionsProto(ctx, input)
}

func (s *Server) UserReactions(ctx context.Context, input *chatv1.UserReactionsRequest) (*chatv1.UserReactionPage, error) {
	return s.userReactionsProto(ctx, input)
}

func (s *Server) AddPin(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	return s.addPinProto(ctx, input)
}

func (s *Server) RemovePin(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	return s.removePinProto(ctx, input)
}

func (s *Server) Pins(ctx context.Context, input *chatv1.PinsRequest) (*chatv1.PinPage, error) {
	return s.pinsProto(ctx, input)
}

func (s *Server) AddStar(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	return s.addStarProto(ctx, input)
}

func (s *Server) RemoveStar(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	return s.removeStarProto(ctx, input)
}

func (s *Server) Stars(ctx context.Context, input *chatv1.StarsRequest) (*chatv1.StarPage, error) {
	return s.starsProto(ctx, input)
}

func (s *Server) AddBookmark(ctx context.Context, input *chatv1.AddBookmarkRequest) (*chatv1.Bookmark, error) {
	return s.addBookmarkProto(ctx, input)
}

func (s *Server) EditBookmark(ctx context.Context, input *chatv1.EditBookmarkRequest) (*chatv1.Bookmark, error) {
	return s.editBookmarkProto(ctx, input)
}

func (s *Server) ListBookmarks(ctx context.Context, input *chatv1.BookmarksRequest) (*chatv1.BookmarksResponse, error) {
	return s.bookmarksProto(ctx, input)
}

func (s *Server) RemoveBookmark(ctx context.Context, input *chatv1.BookmarkRequest) (*chatv1.MutationResponse, error) {
	return s.removeBookmarkProto(ctx, input)
}

func (s *Server) AddReminder(ctx context.Context, input *chatv1.AddReminderRequest) (*chatv1.Reminder, error) {
	return s.addReminderProto(ctx, input)
}

func (s *Server) ReminderInfo(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.Reminder, error) {
	return s.reminderInfoProto(ctx, input)
}

func (s *Server) Reminders(ctx context.Context, input *chatv1.RemindersRequest) (*chatv1.ReminderPage, error) {
	return s.remindersProto(ctx, input)
}

func (s *Server) CompleteReminder(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.MutationResponse, error) {
	return s.completeReminderProto(ctx, input)
}

func (s *Server) DeleteReminder(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.MutationResponse, error) {
	return s.deleteReminderProto(ctx, input)
}

func (s *Server) ScheduleMessage(ctx context.Context, input *chatv1.ScheduleMessageRequest) (*chatv1.ScheduledMessage, error) {
	return s.scheduleMessageProto(ctx, input)
}

func (s *Server) ScheduledMessages(ctx context.Context, input *chatv1.ScheduledMessagesRequest) (*chatv1.ScheduledMessagePage, error) {
	return s.scheduledMessagesProto(ctx, input)
}

func (s *Server) DeleteScheduledMessage(ctx context.Context, input *chatv1.DeleteScheduledMessageRequest) (*chatv1.MutationResponse, error) {
	return s.deleteScheduledMessageProto(ctx, input)
}

func (s *Server) ListEventsAfter(ctx context.Context, input *chatv1.EventsRequest) (*chatv1.EventsResponse, error) {
	return s.listEventsAfterProto(ctx, input)
}

func (s *Server) UserByEmail(ctx context.Context, input *chatv1.UserByEmailRequest) (*chatv1.User, error) {
	return s.userByEmailProto(ctx, input)
}

func (s *Server) SetUserProfile(ctx context.Context, input *chatv1.SetUserProfileRequest) (*chatv1.User, error) {
	return s.setUserProfileProto(ctx, input)
}

func (s *Server) SetUserPresence(ctx context.Context, input *chatv1.SetUserPresenceRequest) (*chatv1.User, error) {
	return s.setUserPresenceProto(ctx, input)
}

func (s *Server) DoNotDisturbInfo(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.DoNotDisturb, error) {
	return s.doNotDisturbInfoProto(ctx, input)
}

func (s *Server) SetSnooze(ctx context.Context, input *chatv1.SetSnoozeRequest) (*chatv1.DoNotDisturb, error) {
	return s.setSnoozeProto(ctx, input)
}

func (s *Server) EndSnooze(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.DoNotDisturb, error) {
	return s.endSnoozeProto(ctx, input)
}

func (s *Server) EndDND(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.MutationResponse, error) {
	return s.endDNDProto(ctx, input)
}

func (s *Server) updateProto(ctx context.Context, input *chatv1.UpdateRequest) (*chatv1.Message, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" || input.GetText() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, timestamp, and text are required")
	}
	message, err := s.implementation.Update(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetText())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(message), nil
}

func (s *Server) deleteProto(ctx context.Context, input *chatv1.DeleteRequest) (*chatv1.Message, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	message, err := s.implementation.Delete(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(message), nil
}

func (s *Server) permalinkProto(ctx context.Context, input *chatv1.PermalinkRequest) (*chatv1.PermalinkResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	value, err := s.implementation.Permalink(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.PermalinkResponse{Permalink: value}, nil
}

func (s *Server) historyProto(ctx context.Context, input *chatv1.HistoryRequest) (*chatv1.MessagePage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.History(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) searchProto(ctx context.Context, input *chatv1.SearchRequest) (*chatv1.MessagePage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetQuery() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and query are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.Search(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery(), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) fileInfoProto(ctx context.Context, input *chatv1.FileRequest) (*chatv1.File, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetFileId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and file_id are required")
	}
	file, err := s.implementation.FileInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFile(file), nil
}

func (s *Server) deleteFileProto(ctx context.Context, input *chatv1.FileRequest) (*chatv1.DeleteFileResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetFileId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and file_id are required")
	}
	if err := s.implementation.DeleteFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DeleteFileResponse{Ok: true}, nil
}

func (s *Server) filesProto(ctx context.Context, input *chatv1.FilesRequest) (*chatv1.FilePage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.Files(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFilePage(page), nil
}

func (s *Server) UploadFile(stream chatv1.ChatService_UploadFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetMetadata()
	if header == nil {
		return status.Error(codes.InvalidArgument, "upload stream must begin with metadata")
	}
	if header.GetWorkspaceId() == "" || header.GetUserId() == "" || header.GetName() == "" || header.GetTitle() == "" || header.GetMimeType() == "" {
		return status.Error(codes.InvalidArgument, "workspace_id, user_id, name, title, and mime_type are required")
	}
	if header.GetSize() < 0 {
		return status.Error(codes.InvalidArgument, "size must be a non-negative integer")
	}
	reader, writer := io.Pipe()
	type uploadResult struct {
		file domain.File
		err  error
	}
	result := make(chan uploadResult, 1)
	go func() {
		file, uploadErr := s.implementation.UploadFile(stream.Context(), domain.WorkspaceID(header.GetWorkspaceId()), domain.UserID(header.GetUserId()), header.GetName(), header.GetTitle(), header.GetMimeType(), header.GetSize(), reader)
		result <- uploadResult{file: file, err: uploadErr}
		if uploadErr != nil {
			_ = writer.CloseWithError(uploadErr)
		}
	}()
	for {
		part, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = writer.Close()
			completed := <-result
			if completed.err != nil {
				return mapError(completed.err)
			}
			return stream.SendAndClose(encodeProtoFile(completed.file))
		}
		if err != nil {
			_ = writer.CloseWithError(err)
			<-result
			return err
		}
		chunk := part.GetChunk()
		if len(chunk) == 0 {
			_ = writer.CloseWithError(errors.New("upload stream contained an empty file chunk"))
			<-result
			return status.Error(codes.InvalidArgument, "upload stream contained an empty file chunk")
		}
		if _, err := writer.Write(chunk); err != nil {
			_ = writer.CloseWithError(err)
			<-result
			return mapError(err)
		}
	}
}

func (s *Server) UploadUserPhoto(stream chatv1.ChatService_UploadUserPhotoServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetMetadata()
	if header == nil {
		return status.Error(codes.InvalidArgument, "user photo stream must begin with metadata")
	}
	reader, writer := io.Pipe()
	type result struct {
		user domain.User
		err  error
	}
	done := make(chan result, 1)
	go func() {
		user, uploadErr := s.implementation.SetUserPhoto(stream.Context(), domain.WorkspaceID(header.GetWorkspaceId()), domain.UserID(header.GetUserId()), header.GetMimeType(), header.GetSize(), reader)
		done <- result{user: user, err: uploadErr}
		if uploadErr != nil {
			_ = writer.CloseWithError(uploadErr)
		}
	}()
	for {
		part, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			_ = writer.Close()
			completed := <-done
			if completed.err != nil {
				return mapError(completed.err)
			}
			return stream.SendAndClose(encodeProtoUser(completed.user))
		}
		if recvErr != nil {
			_ = writer.CloseWithError(recvErr)
			<-done
			return recvErr
		}
		chunk := part.GetChunk()
		if len(chunk) == 0 {
			_ = writer.CloseWithError(errors.New("user photo stream contained an empty chunk"))
			<-done
			return status.Error(codes.InvalidArgument, "user photo stream contained an empty chunk")
		}
		if _, err := writer.Write(chunk); err != nil {
			_ = writer.CloseWithError(err)
			<-done
			return mapError(err)
		}
	}
}

func (s *Server) DeleteUserPhoto(ctx context.Context, input *chatv1.UserPhotoDeleteRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	if err := s.implementation.DeleteUserPhoto(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) DownloadFile(input *chatv1.DownloadFileRequest, stream chatv1.ChatService_DownloadFileServer) error {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetFileId() == "" {
		return status.Error(codes.InvalidArgument, "workspace_id, user_id, and file_id are required")
	}
	file, reader, err := s.implementation.OpenFile(stream.Context(), domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return mapError(err)
	}
	defer reader.Close()
	if err := stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Metadata{Metadata: encodeProtoFile(file)}}); err != nil {
		return err
	}
	buffer := make([]byte, 64<<10)
	for {
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if err := stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Chunk{Chunk: append([]byte(nil), buffer[:read]...)}}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return mapError(readErr)
		}
		if read == 0 {
			return errors.New("file reader returned no data or error")
		}
	}
}

func (s *Server) DownloadPublicFile(input *chatv1.PublicFileTokenRequest, stream chatv1.ChatService_DownloadPublicFileServer) error {
	if input.GetToken() == "" {
		return status.Error(codes.InvalidArgument, "token is required")
	}
	file, reader, err := s.implementation.OpenPublicFile(stream.Context(), input.GetToken())
	if err != nil {
		return mapError(err)
	}
	defer reader.Close()
	if err := stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Metadata{Metadata: encodeProtoFile(file)}}); err != nil {
		return err
	}
	buffer := make([]byte, 64<<10)
	for {
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if err := stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Chunk{Chunk: append([]byte(nil), buffer[:read]...)}}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return mapError(readErr)
		}
		if read == 0 {
			return errors.New("public file reader returned no data or error")
		}
	}
}

func (s *Server) DownloadUserPhoto(input *chatv1.UserPhotoDownloadRequest, stream chatv1.ChatService_DownloadUserPhotoServer) error {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetToken() == "" {
		return status.Error(codes.InvalidArgument, "workspace_id, user_id, and token are required")
	}
	_, reader, err := s.implementation.OpenUserPhoto(stream.Context(), domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetToken())
	if err != nil {
		return mapError(err)
	}
	defer reader.Close()
	if err := stream.Send(&chatv1.UserPhotoDownloadPart{Part: &chatv1.UserPhotoDownloadPart_Metadata{Metadata: &chatv1.UserPhotoMetadata{MimeType: "application/octet-stream", Token: input.GetToken()}}}); err != nil {
		return err
	}
	buffer := make([]byte, 64<<10)
	for {
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if err := stream.Send(&chatv1.UserPhotoDownloadPart{Part: &chatv1.UserPhotoDownloadPart_Chunk{Chunk: append([]byte(nil), buffer[:read]...)}}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return mapError(readErr)
		}
		if read == 0 {
			return errors.New("user photo reader returned no data or error")
		}
	}
}

func (s *Server) lookupTokenProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.TokenRecord, error) {
	if input.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	record, err := s.tokens.LookupToken(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoToken(record), nil
}

func (s *Server) lookupSessionProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.SessionRecord, error) {
	if input.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	record, err := s.sessions.LookupSession(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSession(record), nil
}

func (s *Server) revokeSessionProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AuthRevokeResponse, error) {
	if input.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	if err := s.revoker.RevokeSession(ctx, input.GetToken()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) revokeTokenProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AuthRevokeResponse, error) {
	if input.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	if err := s.tokenRevoker.RevokeToken(ctx, input.GetToken()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) repliesProto(ctx context.Context, input *chatv1.RepliesRequest) (*chatv1.MessagePage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.Replies(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) conversationInfoProto(ctx context.Context, input *chatv1.ConversationInfoRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	result, err := s.implementation.ConversationInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) userInfoProto(ctx context.Context, input *chatv1.UserRequest) (*chatv1.User, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetRequestedUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and requested_user_id are required")
	}
	result, err := s.implementation.UserInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetRequestedUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) userByEmailProto(ctx context.Context, input *chatv1.UserByEmailRequest) (*chatv1.User, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and email are required")
	}
	result, err := s.implementation.UserByEmail(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetEmail())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) setUserProfileProto(ctx context.Context, input *chatv1.SetUserProfileRequest) (*chatv1.User, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetProfile() == nil {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and profile are required")
	}
	p := input.GetProfile()
	profile := domain.UserProfile{
		DisplayName: p.GetDisplayName(), StatusText: p.GetStatusText(), StatusEmoji: p.GetStatusEmoji(),
		Image24: p.GetImage_24(), Image32: p.GetImage_32(), Image48: p.GetImage_48(), Image72: p.GetImage_72(),
		Image192: p.GetImage_192(), Image512: p.GetImage_512(), Image1024: p.GetImage_1024(),
	}
	result, err := s.implementation.SetUserProfile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), profile)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) setUserPresenceProto(ctx context.Context, input *chatv1.SetUserPresenceRequest) (*chatv1.User, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetPresence() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and presence are required")
	}
	result, err := s.implementation.SetUserPresence(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.Presence(input.GetPresence()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) doNotDisturbInfoProto(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.DoNotDisturb, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	result, err := s.implementation.DoNotDisturbInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetRequestedUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDoNotDisturb(result), nil
}

func (s *Server) setSnoozeProto(ctx context.Context, input *chatv1.SetSnoozeRequest) (*chatv1.DoNotDisturb, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetMinutes() == 0 {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and positive minutes are required")
	}
	result, err := s.implementation.SetSnooze(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetMinutes())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDoNotDisturb(result), nil
}

func (s *Server) endSnoozeProto(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.DoNotDisturb, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	result, err := s.implementation.EndSnooze(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDoNotDisturb(result), nil
}

func (s *Server) endDNDProto(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	if err := s.implementation.EndDND(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) usersProto(ctx context.Context, input *chatv1.UsersRequest) (*chatv1.UserPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.Users(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserPage(page), nil
}

func (s *Server) conversationMembersProto(ctx context.Context, input *chatv1.ConversationMembersRequest) (*chatv1.UserPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.ConversationMembers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserPage(page), nil
}

func (s *Server) workspaceInfoProto(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.Workspace, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	result, err := s.implementation.WorkspaceInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(result), nil
}

func (s *Server) conversationsProto(ctx context.Context, input *chatv1.ConversationsRequest) (*chatv1.ConversationPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoConversationListRequest(input)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.Conversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPage(page), nil
}

func conversationTypeStrings(values []domain.ConversationType) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func protoConversationListRequest(input *chatv1.ConversationsRequest) (domain.ConversationListRequest, error) {
	page, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	types, err := domain.NormalizeConversationTypes(input.GetTypes())
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	return domain.ConversationListRequest{Limit: page.Limit, Cursor: page.Cursor, ExcludeArchived: input.GetExcludeArchived(), Types: types, MemberUserID: domain.UserID(strings.TrimSpace(input.GetMemberUserId()))}, nil
}

func (s *Server) openConversationProto(ctx context.Context, input *chatv1.OpenConversationRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	users, err := protoUserIDs(input.GetUsers())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	conversation, err := s.implementation.OpenConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), users)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) createConversationProto(ctx context.Context, input *chatv1.CreateConversationRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and name are required")
	}
	conversation, err := s.implementation.CreateConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetPrivate())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) joinConversationProto(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	conversation, err := s.implementation.JoinConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) inviteConversationMembersProto(ctx context.Context, input *chatv1.InviteConversationMembersRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	users, err := protoUserIDs(input.GetUsers())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	conversation, err := s.implementation.InviteConversationMembers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), users)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) leaveConversationProto(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	if err := s.implementation.LeaveConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) kickConversationMemberProto(ctx context.Context, input *chatv1.KickConversationMemberRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTargetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and target_id are required")
	}
	if err := s.implementation.KickConversationMember(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.UserID(input.GetTargetId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) renameConversationProto(ctx context.Context, input *chatv1.RenameConversationRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and name are required")
	}
	result, err := s.implementation.RenameConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) setConversationTopicProto(ctx context.Context, input *chatv1.SetConversationTopicRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	result, err := s.implementation.SetConversationTopic(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetTopic())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) setConversationPurposeProto(ctx context.Context, input *chatv1.SetConversationPurposeRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	result, err := s.implementation.SetConversationPurpose(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetPurpose())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) setConversationArchivedProto(ctx context.Context, input *chatv1.SetConversationArchivedRequest) (*chatv1.Conversation, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	result, err := s.implementation.SetConversationArchived(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetArchived())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) markReadProto(ctx context.Context, input *chatv1.MarkReadRequest) (*chatv1.ReadCursor, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	cursor, err := s.implementation.MarkRead(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReadCursor(cursor), nil
}

func (s *Server) addReactionProto(ctx context.Context, input *chatv1.ReactionRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" || input.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, timestamp, and name are required")
	}
	if err := s.implementation.AddReaction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) removeReactionProto(ctx context.Context, input *chatv1.ReactionRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" || input.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, timestamp, and name are required")
	}
	if err := s.implementation.RemoveReaction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) reactionsProto(ctx context.Context, input *chatv1.ReactionPageRequest) (*chatv1.ReactionPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	items, next, more, err := s.implementation.Reactions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReactionPage(items, next, more), nil
}

func (s *Server) userReactionsProto(ctx context.Context, input *chatv1.UserReactionsRequest) (*chatv1.UserReactionPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.UserReactions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserReactionPage(page), nil
}

func (s *Server) addPinProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	if err := s.implementation.AddPin(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) removePinProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	if err := s.implementation.RemovePin(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) pinsProto(ctx context.Context, input *chatv1.PinsRequest) (*chatv1.PinPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	items, next, more, err := s.implementation.Pins(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoPinPage(items, next, more), nil
}

func (s *Server) addStarProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	if err := s.implementation.AddStar(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) removeStarProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTimestamp() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and timestamp are required")
	}
	if err := s.implementation.RemoveStar(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) starsProto(ctx context.Context, input *chatv1.StarsRequest) (*chatv1.StarPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	items, next, more, err := s.implementation.Stars(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoStarPage(items, next, more), nil
}

func (s *Server) addBookmarkProto(ctx context.Context, input *chatv1.AddBookmarkRequest) (*chatv1.Bookmark, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetTitle() == "" || input.GetType() == "" || input.GetLink() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, title, type, and link are required")
	}
	bookmark, err := s.implementation.AddBookmark(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetTitle(), input.GetType(), input.GetLink(), input.GetEmoji(), input.GetEntityId(), input.GetAccessLevel(), input.GetParentId())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBookmark(bookmark), nil
}

func (s *Server) editBookmarkProto(ctx context.Context, input *chatv1.EditBookmarkRequest) (*chatv1.Bookmark, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetBookmarkId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and bookmark_id are required")
	}
	bookmark, err := s.implementation.EditBookmark(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.BookmarkID(input.GetBookmarkId()), domain.BookmarkUpdate{Title: input.GetTitle(), Link: input.GetLink(), Emoji: input.GetEmoji(), SetTitle: input.Title != nil, SetLink: input.Link != nil, SetEmoji: input.Emoji != nil})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBookmark(bookmark), nil
}

func (s *Server) bookmarksProto(ctx context.Context, input *chatv1.BookmarksRequest) (*chatv1.BookmarksResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and conversation_id are required")
	}
	items, err := s.implementation.Bookmarks(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.Bookmark, 0, len(items))
	for _, item := range items {
		result = append(result, encodeProtoBookmark(item))
	}
	return &chatv1.BookmarksResponse{Bookmarks: result}, nil
}

func (s *Server) removeBookmarkProto(ctx context.Context, input *chatv1.BookmarkRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetConversationId() == "" || input.GetBookmarkId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, conversation_id, and bookmark_id are required")
	}
	if err := s.implementation.RemoveBookmark(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.BookmarkID(input.GetBookmarkId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) addReminderProto(ctx context.Context, input *chatv1.AddReminderRequest) (*chatv1.Reminder, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetText() == "" || input.GetTime() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, text, and positive time are required")
	}
	reminder, err := s.implementation.AddReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), input.GetText(), time.Unix(input.GetTime(), 0).UTC())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReminder(reminder), nil
}

func (s *Server) reminderInfoProto(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.Reminder, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetReminderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and reminder_id are required")
	}
	reminder, err := s.implementation.ReminderInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ReminderID(input.GetReminderId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReminder(reminder), nil
}

func (s *Server) remindersProto(ctx context.Context, input *chatv1.RemindersRequest) (*chatv1.ReminderPage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.Reminders(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.Reminder, 0, len(page.Reminders))
	for _, reminder := range page.Reminders {
		result = append(result, encodeProtoReminder(reminder))
	}
	return &chatv1.ReminderPage{Reminders: result, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) completeReminderProto(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetReminderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and reminder_id are required")
	}
	if err := s.implementation.CompleteReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteReminderProto(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetReminderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and reminder_id are required")
	}
	if err := s.implementation.DeleteReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) scheduleMessageProto(ctx context.Context, input *chatv1.ScheduleMessageRequest) (*chatv1.ScheduledMessage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetChannelId() == "" || input.GetPostAt() <= 0 || (input.GetText() == "" && input.GetBlocks() == "") {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, channel_id, text or blocks, and positive post_at are required")
	}
	value, err := s.implementation.ScheduleMessageWithBlocksAndAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetChannelId()), input.GetText(), input.GetBlocks(), input.GetAttachments(), time.Unix(input.GetPostAt(), 0).UTC())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoScheduledMessage(value), nil
}

func (s *Server) scheduledMessagesProto(ctx context.Context, input *chatv1.ScheduledMessagesRequest) (*chatv1.ScheduledMessagePage, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id and user_id are required")
	}
	request, err := protoPageRequest(input.GetLimit(), input.GetCursor())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	page, err := s.implementation.ScheduledMessages(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetChannelId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.ScheduledMessage, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, encodeProtoScheduledMessage(value))
	}
	return &chatv1.ScheduledMessagePage{ScheduledMessages: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) deleteScheduledMessageProto(ctx context.Context, input *chatv1.DeleteScheduledMessageRequest) (*chatv1.MutationResponse, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetChannelId() == "" || input.GetScheduledMessageId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, channel_id, and scheduled_message_id are required")
	}
	if err := s.implementation.DeleteScheduledMessage(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetChannelId()), domain.ScheduledMessageID(input.GetScheduledMessageId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) listEventsAfterProto(ctx context.Context, input *chatv1.EventsRequest) (*chatv1.EventsResponse, error) {
	if input.GetWorkspaceId() == "" && input.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id or app_id is required")
	}
	if input.GetLimit() < 1 || input.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "limit must be between 1 and 100")
	}
	var records []events.Record
	var err error
	if input.GetAppId() != "" {
		records, err = s.implementation.ListAppEventsAfter(ctx, domain.AppID(input.GetAppId()), input.GetAfter(), int(input.GetLimit()))
	} else {
		records, err = s.implementation.ListEventsAfter(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetAfter(), int(input.GetLimit()))
	}
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoEvents(records), nil
}

func mapError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	if matchesAny(err,
		service.ErrInvalidMessage, service.ErrInvalidTimestamp,
		service.ErrInvalidConversation, service.ErrInvalidWorkspace,
		service.ErrInvalidConversationPrefs, service.ErrInvalidReaction,
		service.ErrInvalidFile, service.ErrInvalidSearch,
		service.ErrInvalidProfile, service.ErrInvalidPresence,
		service.ErrInvalidSnooze, service.ErrInvalidReminder,
		service.ErrInvalidCall, service.ErrInvalidUserGroup,
		service.ErrInvalidEphemeral, service.ErrInvalidAccessLog,
		service.ErrInvalidEmoji, service.ErrInvalidRemoteFile,
		service.ErrInvalidInviteRequest, service.ErrInvalidAppApproval,
		service.ErrInvalidView, service.ErrInvalidWorkflowStep,
		service.ErrInvalidDialog, service.ErrInvalidBot,
		service.ErrInvalidMigration, service.ErrInvalidOAuth,
		service.ErrInvalidOAuthClient, service.ErrInvalidIntegrationLogs,
		store.ErrInvalidConversationType, store.ErrInvalidInviteRequest,
		store.ErrInvalidAppApproval, domain.ErrInvalidCursor,
	) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if errors.Is(err, service.ErrEmojiAlreadyExists) {
		return status.Error(codes.AlreadyExists, err.Error())
	}
	if errors.Is(err, service.ErrBlobUnavailable) {
		return status.Error(codes.Unavailable, err.Error())
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		return status.Error(codes.AlreadyExists, err.Error())
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrLeaseConflict) || errors.Is(err, store.ErrIdempotencyConflict) || errors.Is(err, service.ErrInvalidList) || errors.Is(err, service.ErrInvalidEntity) {
		return status.Error(codes.Aborted, err.Error())
	}
	if errors.Is(err, store.ErrSocketModeConnectionLimit) {
		return status.Error(codes.ResourceExhausted, err.Error())
	}
	if errors.Is(err, service.ErrMessageNotOwned) {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if errors.Is(err, service.ErrMessageAlreadyDeleted) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, store.ErrNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Unavailable, err.Error())
}

func matchesAny(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func encodeProtoUser(value domain.User) *chatv1.User {
	return &chatv1.User{
		Id:          string(value.ID),
		WorkspaceId: string(value.WorkspaceID),
		Email:       value.Email,
		Name:        value.Name,
		RealName:    value.RealName,
		Profile:     encodeProtoProfile(value.Profile),
		Presence:    string(value.Presence),
		Deleted:     value.Deleted,
	}
}

func encodeProtoInviteRequest(value domain.InviteRequest) *chatv1.InviteRequest {
	channels := make([]string, 0, len(value.ChannelIDs))
	for _, channel := range value.ChannelIDs {
		channels = append(channels, string(channel))
	}
	result := &chatv1.InviteRequest{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Email: value.Email, RequestedBy: string(value.RequestedBy), ChannelIds: channels, CustomMessage: value.CustomMessage, RealName: value.RealName, Resend: value.Resend, Restricted: value.Restricted, UltraRestricted: value.UltraRestricted, GuestExpirationAt: value.GuestExpirationAt.Unix(), Status: string(value.Status), CreatedAt: value.CreatedAt.Unix()}
	if !value.ReviewedAt.IsZero() {
		result.ReviewedAt = value.ReviewedAt.Unix()
	}
	return result
}

func encodeProtoAppApproval(value domain.AppApproval) *chatv1.AppApproval {
	return &chatv1.AppApproval{AppId: string(value.ID), RequestId: string(value.RequestID), WorkspaceId: string(value.WorkspaceID), Status: string(value.Status), CreatedAt: value.CreatedAt.Unix(), UpdatedAt: value.UpdatedAt.Unix()}
}

func decodeProtoAppApproval(value *chatv1.AppApproval) domain.AppApproval {
	return domain.AppApproval{ID: domain.AppID(value.GetAppId()), RequestID: domain.AppRequestID(value.GetRequestId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Status: domain.AppApprovalStatus(value.GetStatus()), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC()}
}

func decodeProtoInviteRequest(value *chatv1.InviteRequest) domain.InviteRequest {
	channels := make([]domain.ConversationID, 0, len(value.GetChannelIds()))
	for _, channel := range value.GetChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	result := domain.InviteRequest{ID: domain.InviteRequestID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Email: value.GetEmail(), RequestedBy: domain.UserID(value.GetRequestedBy()), ChannelIDs: channels, CustomMessage: value.GetCustomMessage(), RealName: value.GetRealName(), Resend: value.GetResend(), Restricted: value.GetRestricted(), UltraRestricted: value.GetUltraRestricted(), Status: domain.InviteRequestStatus(value.GetStatus()), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC()}
	if value.GetGuestExpirationAt() != 0 {
		result.GuestExpirationAt = time.Unix(value.GetGuestExpirationAt(), 0).UTC()
	}
	if value.GetReviewedAt() != 0 {
		result.ReviewedAt = time.Unix(value.GetReviewedAt(), 0).UTC()
	}
	return result
}

func encodeProtoProfile(value domain.UserProfile) *chatv1.UserProfile {
	return &chatv1.UserProfile{
		DisplayName: value.DisplayName,
		StatusText:  value.StatusText,
		StatusEmoji: value.StatusEmoji,
		Image_24:    value.Image24,
		Image_32:    value.Image32,
		Image_48:    value.Image48,
		Image_72:    value.Image72,
		Image_192:   value.Image192,
		Image_512:   value.Image512,
		Image_1024:  value.Image1024,
	}
}

func protoPageRequest(limit int32, cursor string) (domain.PageRequest, error) {
	if limit < 1 || limit > 200 {
		return domain.PageRequest{}, errors.New("limit must be between 1 and 200")
	}
	return domain.PageRequest{Limit: int(limit), Cursor: domain.Cursor(cursor)}, nil
}

func stringIDs(values []domain.UserID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func protoUserIDs(values []string) ([]domain.UserID, error) {
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("users must contain non-empty strings")
		}
		result = append(result, domain.UserID(value))
	}
	return result, nil
}

func encodeProtoUserPage(page domain.UserPage) *chatv1.UserPage {
	users := make([]*chatv1.User, 0, len(page.Users))
	for _, user := range page.Users {
		users = append(users, encodeProtoUser(user))
	}
	return &chatv1.UserPage{Users: users, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func encodeProtoAdminUserPage(page domain.AdminUserPage) *chatv1.AdminUserPage {
	users := make([]*chatv1.AdminUser, 0, len(page.Users))
	for _, value := range page.Users {
		users = append(users, &chatv1.AdminUser{User: encodeProtoUser(value.User), Role: string(value.Membership.Role), Active: value.Membership.Active})
	}
	return &chatv1.AdminUserPage{Users: users, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoAdminUserPage(value *chatv1.AdminUserPage) (domain.AdminUserPage, error) {
	if value == nil {
		return domain.AdminUserPage{}, errors.New("missing administrator user page")
	}
	users := make([]domain.AdminUser, 0, len(value.GetUsers()))
	for _, item := range value.GetUsers() {
		if item == nil || item.GetUser() == nil {
			return domain.AdminUserPage{}, errors.New("administrator user page contains an empty user")
		}
		user, err := decodeProtoUser(item.GetUser())
		if err != nil {
			return domain.AdminUserPage{}, err
		}
		users = append(users, domain.AdminUser{User: user, Membership: domain.WorkspaceMembership{WorkspaceID: user.WorkspaceID, UserID: user.ID, Role: domain.WorkspaceRole(item.GetRole()), Active: item.GetActive()}})
	}
	return domain.AdminUserPage{Users: users, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func decodeProtoUserPage(value *chatv1.UserPage) (domain.UserPage, error) {
	if value == nil {
		return domain.UserPage{}, errors.New("typed user page is required")
	}
	users := make([]domain.User, 0, len(value.GetUsers()))
	for _, item := range value.GetUsers() {
		user, err := decodeProtoUser(item)
		if err != nil {
			return domain.UserPage{}, err
		}
		users = append(users, user)
	}
	return domain.UserPage{Users: users, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoWorkspace(value domain.Workspace) *chatv1.Workspace {
	channels := make([]string, 0, len(value.DefaultChannelIDs))
	for _, channel := range value.DefaultChannelIDs {
		channels = append(channels, string(channel))
	}
	return &chatv1.Workspace{Id: string(value.ID), Domain: value.Domain, Name: value.Name, Description: value.Description, Discoverability: string(value.Discoverability), IconUrl: value.IconURL, DefaultChannelIds: channels}
}

func decodeProtoWorkspace(value *chatv1.Workspace) (domain.Workspace, error) {
	if value == nil || value.GetId() == "" || value.GetName() == "" {
		return domain.Workspace{}, errors.New("typed workspace response is incomplete")
	}
	channels := make([]domain.ConversationID, 0, len(value.GetDefaultChannelIds()))
	for _, channel := range value.GetDefaultChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	return domain.Workspace{ID: domain.WorkspaceID(value.GetId()), Domain: value.GetDomain(), Name: value.GetName(), Description: value.GetDescription(), Discoverability: domain.WorkspaceDiscoverability(value.GetDiscoverability()), IconURL: value.GetIconUrl(), DefaultChannelIDs: channels}, nil
}

func encodeProtoConversationPrefs(value domain.ConversationPrefs) *chatv1.ConversationPrefs {
	canTypes := make([]string, 0, len(value.CanThread.Types))
	for _, item := range value.CanThread.Types {
		canTypes = append(canTypes, string(item))
	}
	canUsers := make([]string, 0, len(value.CanThread.Users))
	for _, item := range value.CanThread.Users {
		canUsers = append(canUsers, string(item))
	}
	postTypes := make([]string, 0, len(value.WhoCanPost.Types))
	for _, item := range value.WhoCanPost.Types {
		postTypes = append(postTypes, string(item))
	}
	postUsers := make([]string, 0, len(value.WhoCanPost.Users))
	for _, item := range value.WhoCanPost.Users {
		postUsers = append(postUsers, string(item))
	}
	return &chatv1.ConversationPrefs{ConversationId: string(value.ConversationID), CanThread: &chatv1.ConversationPreferenceList{Types: canTypes, Users: canUsers}, WhoCanPost: &chatv1.ConversationPreferenceList{Types: postTypes, Users: postUsers}}
}

func decodeProtoConversationPrefs(value *chatv1.ConversationPrefs) (domain.ConversationPrefs, error) {
	if value == nil || value.GetConversationId() == "" || value.GetCanThread() == nil || value.GetWhoCanPost() == nil {
		return domain.ConversationPrefs{}, errors.New("typed conversation preferences are incomplete")
	}
	return decodeProtoConversationPrefsValue(value), nil
}

func decodeProtoConversationPrefsValue(value *chatv1.ConversationPrefs) domain.ConversationPrefs {
	if value == nil {
		return domain.ConversationPrefs{}
	}
	canThread := value.GetCanThread()
	whoCanPost := value.GetWhoCanPost()
	result := domain.ConversationPrefs{ConversationID: domain.ConversationID(value.GetConversationId())}
	if canThread != nil {
		for _, item := range canThread.GetTypes() {
			result.CanThread.Types = append(result.CanThread.Types, domain.ConversationPreferenceType(item))
		}
		for _, item := range canThread.GetUsers() {
			result.CanThread.Users = append(result.CanThread.Users, domain.UserID(item))
		}
	}
	if whoCanPost != nil {
		for _, item := range whoCanPost.GetTypes() {
			result.WhoCanPost.Types = append(result.WhoCanPost.Types, domain.ConversationPreferenceType(item))
		}
		for _, item := range whoCanPost.GetUsers() {
			result.WhoCanPost.Users = append(result.WhoCanPost.Users, domain.UserID(item))
		}
	}
	return result
}

func encodeProtoList(value domain.List) *chatv1.List {
	return &chatv1.List{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), OwnerId: string(value.OwnerID), Name: value.Name, DescriptionBlocks: value.DescriptionBlocks, Schema: value.Schema, TodoMode: value.TodoMode, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func decodeProtoList(value *chatv1.List) (domain.List, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetOwnerId() == "" || value.GetName() == "" {
		return domain.List{}, errors.New("typed list response is incomplete")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.List{}, err
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, value.GetUpdatedAt())
	if err != nil {
		return domain.List{}, err
	}
	return domain.List{ID: domain.ListID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), OwnerID: domain.UserID(value.GetOwnerId()), Name: value.GetName(), DescriptionBlocks: value.GetDescriptionBlocks(), Schema: value.GetSchema(), TodoMode: value.GetTodoMode(), CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func encodeProtoListItem(value domain.ListItem) *chatv1.ListItem {
	return &chatv1.ListItem{Id: string(value.ID), ListId: string(value.ListID), ParentItemId: string(value.ParentItemID), WorkspaceId: string(value.WorkspaceID), Fields: value.Fields, CreatedBy: string(value.CreatedBy), UpdatedBy: string(value.UpdatedBy), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano), Archived: value.Archived}
}

func decodeProtoListItem(value *chatv1.ListItem) (domain.ListItem, error) {
	if value == nil || value.GetId() == "" || value.GetListId() == "" || value.GetWorkspaceId() == "" || value.GetCreatedBy() == "" || value.GetUpdatedBy() == "" {
		return domain.ListItem{}, errors.New("typed list item response is incomplete")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.ListItem{}, err
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, value.GetUpdatedAt())
	if err != nil {
		return domain.ListItem{}, err
	}
	return domain.ListItem{ID: domain.ListItemID(value.GetId()), ListID: domain.ListID(value.GetListId()), ParentItemID: domain.ListItemID(value.GetParentItemId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Fields: value.GetFields(), CreatedBy: domain.UserID(value.GetCreatedBy()), UpdatedBy: domain.UserID(value.GetUpdatedBy()), CreatedAt: createdAt, UpdatedAt: updatedAt, Archived: value.GetArchived()}, nil
}

func encodeProtoListItemPage(value domain.ListItemPage) *chatv1.ListItemPage {
	items := make([]*chatv1.ListItem, 0, len(value.Items))
	for _, item := range value.Items {
		items = append(items, encodeProtoListItem(item))
	}
	return &chatv1.ListItemPage{Items: items, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoListItemPage(value *chatv1.ListItemPage) (domain.ListItemPage, error) {
	if value == nil {
		return domain.ListItemPage{}, errors.New("typed list item page is required")
	}
	items := make([]domain.ListItem, 0, len(value.GetItems()))
	for _, item := range value.GetItems() {
		decoded, err := decodeProtoListItem(item)
		if err != nil {
			return domain.ListItemPage{}, err
		}
		items = append(items, decoded)
	}
	return domain.ListItemPage{Items: items, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoListDownload(value domain.ListDownload) *chatv1.ListDownload {
	return &chatv1.ListDownload{Id: string(value.ID), ListId: string(value.ListID), WorkspaceId: string(value.WorkspaceID), Status: value.Status, Url: value.URL, IncludeArchived: value.IncludeArchived, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func decodeProtoListDownload(value *chatv1.ListDownload) (domain.ListDownload, error) {
	if value == nil || value.GetId() == "" || value.GetListId() == "" || value.GetWorkspaceId() == "" || value.GetStatus() == "" {
		return domain.ListDownload{}, errors.New("typed list download response is incomplete")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.ListDownload{}, err
	}
	return domain.ListDownload{ID: domain.ListDownloadID(value.GetId()), ListID: domain.ListID(value.GetListId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Status: value.GetStatus(), URL: value.GetUrl(), IncludeArchived: value.GetIncludeArchived(), CreatedAt: createdAt}, nil
}

func encodeProtoConversation(value domain.Conversation) *chatv1.Conversation {
	return &chatv1.Conversation{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Name: value.Name,
		Topic: value.Topic, Purpose: value.Purpose, Archived: value.Archived,
		IsPrivate: value.IsPrivate, IsDirect: value.IsDirect, IsGroupDirect: value.IsGroupDirect,
		UnreadCount: int64(value.UnreadCount),
	}
}

func decodeProtoConversation(value *chatv1.Conversation) (domain.Conversation, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetName() == "" {
		return domain.Conversation{}, errors.New("typed conversation response is incomplete")
	}
	if value.GetUnreadCount() < 0 || value.GetUnreadCount() > int64(^uint(0)>>1) {
		return domain.Conversation{}, errors.New("typed unread_count is outside platform integer range")
	}
	return domain.Conversation{
		ID: domain.ConversationID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Name: value.GetName(),
		Topic: value.GetTopic(), Purpose: value.GetPurpose(), Archived: value.GetArchived(),
		IsPrivate: value.GetIsPrivate(), IsDirect: value.GetIsDirect(), IsGroupDirect: value.GetIsGroupDirect(),
		UnreadCount: int(value.GetUnreadCount()),
	}, nil
}

func encodeProtoConversationPage(page domain.ConversationPage) *chatv1.ConversationPage {
	conversations := make([]*chatv1.Conversation, 0, len(page.Conversations))
	for _, conversation := range page.Conversations {
		conversations = append(conversations, encodeProtoConversation(conversation))
	}
	return &chatv1.ConversationPage{Conversations: conversations, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoConversationPage(value *chatv1.ConversationPage) (domain.ConversationPage, error) {
	if value == nil {
		return domain.ConversationPage{}, errors.New("typed conversation page is required")
	}
	conversations := make([]domain.Conversation, 0, len(value.GetConversations()))
	for _, item := range value.GetConversations() {
		conversation, err := decodeProtoConversation(item)
		if err != nil {
			return domain.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	return domain.ConversationPage{Conversations: conversations, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoMessage(value domain.Message) *chatv1.Message {
	return &chatv1.Message{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.Conversation),
		AuthorId: string(value.AuthorID), Text: value.Text, ThreadTimestamp: string(value.ThreadTimestamp),
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Deleted: value.Deleted, Unfurls: value.Unfurls, Blocks: value.Blocks, Attachments: value.Attachments,
	}
}

func decodeProtoMessage(value *chatv1.Message) (domain.Message, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetConversationId() == "" || value.GetAuthorId() == "" {
		return domain.Message{}, errors.New("typed message response is incomplete")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.Message{}, errors.New("typed message created_at is invalid")
	}
	return domain.Message{
		ID: domain.MessageID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Conversation: domain.ConversationID(value.GetConversationId()), AuthorID: domain.UserID(value.GetAuthorId()),
		Text: value.GetText(), Blocks: value.GetBlocks(), Attachments: value.GetAttachments(), ThreadTimestamp: domain.MessageTimestamp(value.GetThreadTimestamp()), CreatedAt: created.UTC(), Deleted: value.GetDeleted(), Unfurls: value.GetUnfurls(),
	}, nil
}

func encodeProtoRTMConnection(value domain.RTMConnection) *chatv1.RTMConnection {
	return &chatv1.RTMConnection{Id: value.ID, WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), ExpiresAtUnixNano: value.ExpiresAt.UnixNano()}
}

func decodeProtoRTMConnection(value *chatv1.RTMConnection) domain.RTMConnection {
	return domain.RTMConnection{ID: value.GetId(), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), ExpiresAt: time.Unix(0, value.GetExpiresAtUnixNano()).UTC()}
}

func encodeProtoEphemeralMessage(value domain.EphemeralMessage) *chatv1.EphemeralMessage {
	return &chatv1.EphemeralMessage{WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.Conversation), AuthorId: string(value.AuthorID), RecipientId: string(value.RecipientID), Text: value.Text, Blocks: value.Blocks, Attachments: value.Attachments, Timestamp: string(value.Timestamp)}
}
func decodeProtoEphemeralMessage(value *chatv1.EphemeralMessage) (domain.EphemeralMessage, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetConversationId() == "" || value.GetAuthorId() == "" || value.GetRecipientId() == "" || (value.GetText() == "" && value.GetBlocks() == "") || value.GetTimestamp() == "" {
		return domain.EphemeralMessage{}, errors.New("typed ephemeral message is incomplete")
	}
	if _, err := domain.ParseMessageTimestamp(domain.MessageTimestamp(value.GetTimestamp())); err != nil {
		return domain.EphemeralMessage{}, err
	}
	return domain.EphemeralMessage{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversationId()), AuthorID: domain.UserID(value.GetAuthorId()), RecipientID: domain.UserID(value.GetRecipientId()), Text: value.GetText(), Blocks: value.GetBlocks(), Attachments: value.GetAttachments(), Timestamp: domain.MessageTimestamp(value.GetTimestamp())}, nil
}

func encodeProtoAccessLog(value domain.AccessLog) *chatv1.AccessLog {
	return &chatv1.AccessLog{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Username: value.Username, CreatedAt: value.CreatedAt.Unix(), Ip: value.IP, UserAgent: value.UserAgent}
}
func decodeProtoAccessLog(value *chatv1.AccessLog) (domain.AccessLog, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetUsername() == "" || value.GetCreatedAt() <= 0 {
		return domain.AccessLog{}, errors.New("typed access log is incomplete")
	}
	return domain.AccessLog{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Username: value.GetUsername(), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), IP: value.GetIp(), UserAgent: value.GetUserAgent()}, nil
}

func encodeProtoMessagePage(page domain.MessagePage) *chatv1.MessagePage {
	messages := make([]*chatv1.Message, 0, len(page.Messages))
	for _, message := range page.Messages {
		messages = append(messages, encodeProtoMessage(message))
	}
	return &chatv1.MessagePage{Messages: messages, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoMessagePage(value *chatv1.MessagePage) (domain.MessagePage, error) {
	if value == nil {
		return domain.MessagePage{}, errors.New("typed message page is required")
	}
	messages := make([]domain.Message, 0, len(value.GetMessages()))
	for _, item := range value.GetMessages() {
		message, err := decodeProtoMessage(item)
		if err != nil {
			return domain.MessagePage{}, err
		}
		messages = append(messages, message)
	}
	return domain.MessagePage{Messages: messages, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoCanvas(value domain.Canvas) *chatv1.Canvas {
	return &chatv1.Canvas{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), OwnerId: string(value.OwnerID), Title: value.Title, DocumentContent: value.DocumentContent, CreatedAt: value.CreatedAt.UTC().Unix(), UpdatedAt: value.UpdatedAt.UTC().Unix()}
}

func decodeProtoCanvas(value *chatv1.Canvas) (domain.Canvas, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetOwnerId() == "" {
		return domain.Canvas{}, errors.New("invalid canvas response")
	}
	return domain.Canvas{ID: domain.CanvasID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), OwnerID: domain.UserID(value.GetOwnerId()), Title: value.GetTitle(), DocumentContent: value.GetDocumentContent(), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC()}, nil
}

func encodeProtoFile(value domain.File) *chatv1.File {
	return &chatv1.File{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Uploader: string(value.Uploader),
		Name: value.Name, Title: value.Title, MimeType: value.MIMEType, Size: value.Size,
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Deleted: value.Deleted, PublicToken: value.PublicToken,
		SharedChannels: conversationStrings(value.SharedChannels),
	}
}

func decodeProtoFile(value *chatv1.File) (domain.File, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUploader() == "" || value.GetName() == "" || value.GetTitle() == "" || value.GetMimeType() == "" {
		return domain.File{}, errors.New("typed file response is incomplete")
	}
	if value.GetSize() < 0 {
		return domain.File{}, errors.New("typed file size is invalid")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.File{}, errors.New("typed file created_at is invalid")
	}
	return domain.File{ID: domain.FileID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Uploader: domain.UserID(value.GetUploader()), Name: value.GetName(), Title: value.GetTitle(), MIMEType: value.GetMimeType(), Size: value.GetSize(), CreatedAt: created.UTC(), Deleted: value.GetDeleted(), PublicToken: value.GetPublicToken(), SharedChannels: conversationIDs(value.GetSharedChannels())}, nil
}

func encodeProtoFilePage(page domain.FilePage) *chatv1.FilePage {
	files := make([]*chatv1.File, 0, len(page.Files))
	for _, file := range page.Files {
		files = append(files, encodeProtoFile(file))
	}
	return &chatv1.FilePage{Files: files, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoFilePage(value *chatv1.FilePage) (domain.FilePage, error) {
	if value == nil {
		return domain.FilePage{}, errors.New("typed file page is required")
	}
	files := make([]domain.File, 0, len(value.GetFiles()))
	for _, item := range value.GetFiles() {
		file, err := decodeProtoFile(item)
		if err != nil {
			return domain.FilePage{}, err
		}
		files = append(files, file)
	}
	return domain.FilePage{Files: files, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoRemoteFile(value domain.RemoteFile) *chatv1.RemoteFile {
	channels := make([]string, 0, len(value.SharedChannels))
	for _, channel := range value.SharedChannels {
		channels = append(channels, string(channel))
	}
	return &chatv1.RemoteFile{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ExternalId: value.ExternalID, Title: value.Title, FileType: value.FileType, ExternalUrl: value.ExternalURL, PreviewImage: value.PreviewImage, IndexableContents: value.IndexableContents, CreatedAt: value.CreatedAt.Unix(), Deleted: value.Deleted, SharedChannels: channels}
}

func decodeProtoRemoteFile(value *chatv1.RemoteFile) (domain.RemoteFile, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetExternalId() == "" {
		return domain.RemoteFile{}, errors.New("typed remote file response is incomplete")
	}
	channels := make([]domain.ConversationID, 0, len(value.GetSharedChannels()))
	for _, channel := range value.GetSharedChannels() {
		channels = append(channels, domain.ConversationID(channel))
	}
	return domain.RemoteFile{ID: domain.FileID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ExternalID: value.GetExternalId(), Title: value.GetTitle(), FileType: value.GetFileType(), ExternalURL: value.GetExternalUrl(), PreviewImage: value.GetPreviewImage(), IndexableContents: value.GetIndexableContents(), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), Deleted: value.GetDeleted(), SharedChannels: channels}, nil
}

func encodeProtoRemoteFilePage(page domain.RemoteFilePage) *chatv1.RemoteFilePage {
	files := make([]*chatv1.RemoteFile, 0, len(page.Files))
	for _, value := range page.Files {
		files = append(files, encodeProtoRemoteFile(value))
	}
	return &chatv1.RemoteFilePage{Files: files, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoRemoteFilePage(value *chatv1.RemoteFilePage) (domain.RemoteFilePage, error) {
	if value == nil {
		return domain.RemoteFilePage{}, errors.New("typed remote file page is required")
	}
	files := make([]domain.RemoteFile, 0, len(value.GetFiles()))
	for _, item := range value.GetFiles() {
		file, err := decodeProtoRemoteFile(item)
		if err != nil {
			return domain.RemoteFilePage{}, err
		}
		files = append(files, file)
	}
	return domain.RemoteFilePage{Files: files, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoReadCursor(value domain.ReadCursor) *chatv1.ReadCursor {
	return &chatv1.ReadCursor{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), ConversationId: string(value.Conversation), LastRead: string(value.LastRead), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func decodeProtoReadCursor(value *chatv1.ReadCursor) (domain.ReadCursor, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetConversationId() == "" || value.GetLastRead() == "" {
		return domain.ReadCursor{}, errors.New("typed read cursor response is incomplete")
	}
	updated, err := time.Parse(time.RFC3339Nano, value.GetUpdatedAt())
	if err != nil {
		return domain.ReadCursor{}, errors.New("typed read cursor updated_at is invalid")
	}
	return domain.ReadCursor{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Conversation: domain.ConversationID(value.GetConversationId()), LastRead: domain.MessageTimestamp(value.GetLastRead()), UpdatedAt: updated.UTC()}, nil
}

func encodeProtoToken(value domain.TokenRecord) *chatv1.TokenRecord {
	return &chatv1.TokenRecord{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Scopes: domain.NormalizeScopes(value.Scopes), Revoked: value.Revoked}
}

func decodeProtoToken(value *chatv1.TokenRecord) (domain.TokenRecord, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.TokenRecord{}, errors.New("typed token record is incomplete")
	}
	if len(value.GetScopes()) == 0 {
		return domain.TokenRecord{}, errors.New("typed token scopes are required")
	}
	return domain.TokenRecord{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Scopes: domain.NormalizeScopes(value.GetScopes()), Revoked: value.GetRevoked()}, nil
}

func encodeProtoSession(value domain.SessionRecord) *chatv1.SessionRecord {
	return &chatv1.SessionRecord{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Scopes: domain.NormalizeScopes(value.Scopes), ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano), Revoked: value.Revoked, OidcProvider: value.OIDCProvider, OidcIdToken: value.OIDCIDToken, OidcSubject: value.OIDCSubject, OidcSid: value.OIDCSID}
}

func decodeProtoSession(value *chatv1.SessionRecord) (domain.SessionRecord, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.SessionRecord{}, errors.New("typed session record is incomplete")
	}
	if len(value.GetScopes()) == 0 {
		return domain.SessionRecord{}, errors.New("typed session scopes are required")
	}
	expires, err := time.Parse(time.RFC3339Nano, value.GetExpiresAt())
	if err != nil {
		return domain.SessionRecord{}, errors.New("typed session expires_at is invalid")
	}
	return domain.SessionRecord{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Scopes: domain.NormalizeScopes(value.GetScopes()), ExpiresAt: expires.UTC(), Revoked: value.GetRevoked(), OIDCProvider: value.GetOidcProvider(), OIDCIDToken: value.GetOidcIdToken(), OIDCSubject: value.GetOidcSubject(), OIDCSID: value.GetOidcSid()}, nil
}

func encodeProtoEvents(records []events.Record) *chatv1.EventsResponse {
	result := make([]*chatv1.EventRecord, 0, len(records))
	for _, record := range records {
		result = append(result, &chatv1.EventRecord{Sequence: record.Sequence, Id: string(record.Event.ID), WorkspaceId: string(record.Event.WorkspaceID), Topic: record.Event.Topic, Payload: record.Event.Payload, CreatedAtUnixNano: record.Event.CreatedAt.UnixNano()})
	}
	return &chatv1.EventsResponse{Records: result}
}

func decodeProtoEvents(value *chatv1.EventsResponse) ([]events.Record, error) {
	if value == nil {
		return nil, errors.New("typed events response is required")
	}
	result := make([]events.Record, 0, len(value.GetRecords()))
	for _, item := range value.GetRecords() {
		if item == nil || item.GetSequence() == 0 || item.GetId() == "" || item.GetWorkspaceId() == "" || item.GetTopic() == "" || item.GetCreatedAtUnixNano() == 0 {
			return nil, errors.New("typed event record is incomplete")
		}
		result = append(result, events.Record{Sequence: item.GetSequence(), Event: events.Event{ID: domain.EventID(item.GetId()), WorkspaceID: domain.WorkspaceID(item.GetWorkspaceId()), Topic: item.GetTopic(), Payload: item.GetPayload(), CreatedAt: time.Unix(0, item.GetCreatedAtUnixNano()).UTC()}})
	}
	return result, nil
}

func encodeProtoReaction(value domain.Reaction) *chatv1.Reaction {
	return &chatv1.Reaction{MessageId: string(value.Message), Name: value.Name, UserId: string(value.UserID), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func decodeProtoReaction(value *chatv1.Reaction) (domain.Reaction, error) {
	if value == nil || value.GetMessageId() == "" || value.GetName() == "" || value.GetUserId() == "" {
		return domain.Reaction{}, errors.New("typed reaction is incomplete")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.Reaction{}, errors.New("typed reaction created_at is invalid")
	}
	return domain.Reaction{Message: domain.MessageID(value.GetMessageId()), Name: value.GetName(), UserID: domain.UserID(value.GetUserId()), CreatedAt: created.UTC()}, nil
}

func encodeProtoReactionPage(items []domain.Reaction, next domain.Cursor, more bool) *chatv1.ReactionPage {
	result := make([]*chatv1.Reaction, 0, len(items))
	for _, item := range items {
		result = append(result, encodeProtoReaction(item))
	}
	return &chatv1.ReactionPage{Reactions: result, NextCursor: string(next), HasMore: more}
}

func decodeProtoReactionPage(value *chatv1.ReactionPage) (struct {
	Reactions  []domain.Reaction
	NextCursor domain.Cursor
	HasMore    bool
}, error) {
	if value == nil {
		return struct {
			Reactions  []domain.Reaction
			NextCursor domain.Cursor
			HasMore    bool
		}{}, errors.New("typed reaction page is required")
	}
	items := make([]domain.Reaction, 0, len(value.GetReactions()))
	for _, item := range value.GetReactions() {
		decoded, err := decodeProtoReaction(item)
		if err != nil {
			return struct {
				Reactions  []domain.Reaction
				NextCursor domain.Cursor
				HasMore    bool
			}{}, err
		}
		items = append(items, decoded)
	}
	return struct {
		Reactions  []domain.Reaction
		NextCursor domain.Cursor
		HasMore    bool
	}{items, domain.Cursor(value.GetNextCursor()), value.GetHasMore()}, nil
}

func encodeProtoUserReactionPage(page domain.UserReactionPage) *chatv1.UserReactionPage {
	items := make([]*chatv1.UserReaction, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, &chatv1.UserReaction{ConversationId: string(item.Conversation), Message: encodeProtoMessage(item.Message), Reaction: encodeProtoReaction(item.Reaction)})
	}
	return &chatv1.UserReactionPage{Items: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoUserReactionPage(value *chatv1.UserReactionPage) (domain.UserReactionPage, error) {
	if value == nil {
		return domain.UserReactionPage{}, errors.New("typed user reaction page is required")
	}
	items := make([]domain.UserReaction, 0, len(value.GetItems()))
	for _, item := range value.GetItems() {
		if item == nil || item.GetConversationId() == "" {
			return domain.UserReactionPage{}, errors.New("typed user reaction is incomplete")
		}
		message, err := decodeProtoMessage(item.GetMessage())
		if err != nil {
			return domain.UserReactionPage{}, err
		}
		reaction, err := decodeProtoReaction(item.GetReaction())
		if err != nil {
			return domain.UserReactionPage{}, err
		}
		items = append(items, domain.UserReaction{Conversation: domain.ConversationID(item.GetConversationId()), Message: message, Reaction: reaction})
	}
	return domain.UserReactionPage{Items: items, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoPin(value domain.Pin) *chatv1.Pin {
	return &chatv1.Pin{MessageId: string(value.Message), UserId: string(value.UserID), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func decodeProtoPin(value *chatv1.Pin) (domain.Pin, error) {
	if value == nil || value.GetMessageId() == "" || value.GetUserId() == "" {
		return domain.Pin{}, errors.New("typed pin is incomplete")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.Pin{}, errors.New("typed pin created_at is invalid")
	}
	return domain.Pin{Message: domain.MessageID(value.GetMessageId()), UserID: domain.UserID(value.GetUserId()), CreatedAt: created.UTC()}, nil
}

func encodeProtoPinPage(items []domain.Pin, next domain.Cursor, more bool) *chatv1.PinPage {
	result := make([]*chatv1.Pin, 0, len(items))
	for _, item := range items {
		result = append(result, encodeProtoPin(item))
	}
	return &chatv1.PinPage{Pins: result, NextCursor: string(next), HasMore: more}
}

func decodeProtoPinPage(value *chatv1.PinPage) (struct {
	Pins       []domain.Pin
	NextCursor domain.Cursor
	HasMore    bool
}, error) {
	if value == nil {
		return struct {
			Pins       []domain.Pin
			NextCursor domain.Cursor
			HasMore    bool
		}{}, errors.New("typed pin page is required")
	}
	items := make([]domain.Pin, 0, len(value.GetPins()))
	for _, item := range value.GetPins() {
		decoded, err := decodeProtoPin(item)
		if err != nil {
			return struct {
				Pins       []domain.Pin
				NextCursor domain.Cursor
				HasMore    bool
			}{}, err
		}
		items = append(items, decoded)
	}
	return struct {
		Pins       []domain.Pin
		NextCursor domain.Cursor
		HasMore    bool
	}{items, domain.Cursor(value.GetNextCursor()), value.GetHasMore()}, nil
}

func encodeProtoStar(value domain.Star) *chatv1.Star {
	return &chatv1.Star{MessageId: string(value.Message.ID), ConversationId: string(value.Conversation), UserId: string(value.UserID), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Message: encodeProtoMessage(value.Message)}
}

func encodeProtoStarPage(items []domain.Star, next domain.Cursor, more bool) *chatv1.StarPage {
	result := make([]*chatv1.Star, 0, len(items))
	for _, item := range items {
		result = append(result, encodeProtoStar(item))
	}
	return &chatv1.StarPage{Stars: result, NextCursor: string(next), HasMore: more}
}

func encodeProtoBookmark(value domain.Bookmark) *chatv1.Bookmark {
	return &chatv1.Bookmark{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.Conversation), Title: value.Title, Type: value.Type, Link: value.Link, Emoji: value.Emoji, EntityId: value.EntityID, AccessLevel: value.AccessLevel, ParentId: string(value.ParentID), CreatedAt: value.CreatedAt.UTC().Unix(), UpdatedAt: value.UpdatedAt.UTC().Unix(), UpdatedBy: string(value.UpdatedBy)}
}

func decodeProtoBookmark(value *chatv1.Bookmark) (domain.Bookmark, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetConversationId() == "" || value.GetTitle() == "" || value.GetType() == "" || value.GetUpdatedBy() == "" {
		return domain.Bookmark{}, errors.New("typed bookmark is incomplete")
	}
	return domain.Bookmark{ID: domain.BookmarkID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversationId()), Title: value.GetTitle(), Type: value.GetType(), Link: value.GetLink(), Emoji: value.GetEmoji(), EntityID: value.GetEntityId(), AccessLevel: value.GetAccessLevel(), ParentID: domain.BookmarkID(value.GetParentId()), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC(), UpdatedBy: domain.UserID(value.GetUpdatedBy())}, nil
}

func decodeProtoStar(value *chatv1.Star) (domain.Star, error) {
	if value == nil || value.GetMessageId() == "" || value.GetConversationId() == "" || value.GetUserId() == "" || value.GetCreatedAt() == "" {
		return domain.Star{}, errors.New("typed star is incomplete")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.Star{}, errors.New("typed star created_at is invalid")
	}
	message, err := decodeProtoMessage(value.GetMessage())
	if err != nil {
		return domain.Star{}, err
	}
	return domain.Star{Message: message, Conversation: domain.ConversationID(value.GetConversationId()), UserID: domain.UserID(value.GetUserId()), CreatedAt: created.UTC()}, nil
}

func decodeProtoStarPage(value *chatv1.StarPage) (struct {
	Stars      []domain.Star
	NextCursor domain.Cursor
	HasMore    bool
}, error) {
	if value == nil {
		return struct {
			Stars      []domain.Star
			NextCursor domain.Cursor
			HasMore    bool
		}{}, errors.New("typed star page is required")
	}
	items := make([]domain.Star, 0, len(value.GetStars()))
	for _, item := range value.GetStars() {
		decoded, err := decodeProtoStar(item)
		if err != nil {
			return struct {
				Stars      []domain.Star
				NextCursor domain.Cursor
				HasMore    bool
			}{}, err
		}
		items = append(items, decoded)
	}
	return struct {
		Stars      []domain.Star
		NextCursor domain.Cursor
		HasMore    bool
	}{items, domain.Cursor(value.GetNextCursor()), value.GetHasMore()}, nil
}

func encodeProtoReminder(value domain.Reminder) *chatv1.Reminder {
	result := &chatv1.Reminder{WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), CreatorId: string(value.Creator), UserId: string(value.User), Text: value.Text, Time: value.Time.Unix(), Recurring: value.Recurring}
	if !value.CompleteAt.IsZero() {
		result.CompleteTs = value.CompleteAt.Unix()
	}
	return result
}

func decodeProtoReminder(value *chatv1.Reminder) (domain.Reminder, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetCreatorId() == "" || value.GetUserId() == "" || value.GetText() == "" || value.GetTime() <= 0 {
		return domain.Reminder{}, errors.New("typed reminder is incomplete")
	}
	result := domain.Reminder{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.ReminderID(value.GetId()), Creator: domain.UserID(value.GetCreatorId()), User: domain.UserID(value.GetUserId()), Text: value.GetText(), Time: time.Unix(value.GetTime(), 0).UTC(), Recurring: value.GetRecurring()}
	if value.GetCompleteTs() != 0 {
		result.CompleteAt = time.Unix(value.GetCompleteTs(), 0).UTC()
	}
	return result, nil
}

func encodeProtoScheduledMessage(value domain.ScheduledMessage) *chatv1.ScheduledMessage {
	return &chatv1.ScheduledMessage{WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), ChannelId: string(value.Channel), AuthorId: string(value.Author), Text: value.Text, Blocks: value.Blocks, Attachments: value.Attachments, PostAt: value.PostAt.Unix(), CreatedAt: value.CreatedAt.Unix()}
}

func decodeProtoScheduledMessage(value *chatv1.ScheduledMessage) (domain.ScheduledMessage, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetChannelId() == "" || value.GetAuthorId() == "" || (value.GetText() == "" && value.GetBlocks() == "" && value.GetAttachments() == "") || value.GetPostAt() <= 0 || value.GetCreatedAt() <= 0 {
		return domain.ScheduledMessage{}, errors.New("typed scheduled message is incomplete")
	}
	return domain.ScheduledMessage{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.ScheduledMessageID(value.GetId()), Channel: domain.ConversationID(value.GetChannelId()), Author: domain.UserID(value.GetAuthorId()), Text: value.GetText(), Blocks: value.GetBlocks(), Attachments: value.GetAttachments(), PostAt: time.Unix(value.GetPostAt(), 0).UTC(), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC()}, nil
}

func decodeProtoUser(value *chatv1.User) (domain.User, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetName() == "" {
		return domain.User{}, errors.New("typed user response is incomplete")
	}
	presence := domain.Presence(value.GetPresence())
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return domain.User{}, errors.New("typed user presence is invalid")
	}
	profile := value.GetProfile()
	if profile == nil {
		return domain.User{}, errors.New("typed user profile is required")
	}
	return domain.User{
		ID:          domain.UserID(value.GetId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Email:       value.GetEmail(),
		Name:        value.GetName(),
		RealName:    value.GetRealName(),
		Profile: domain.UserProfile{
			DisplayName: profile.GetDisplayName(),
			StatusText:  profile.GetStatusText(),
			StatusEmoji: profile.GetStatusEmoji(),
			Image24:     profile.GetImage_24(),
			Image32:     profile.GetImage_32(),
			Image48:     profile.GetImage_48(),
			Image72:     profile.GetImage_72(),
			Image192:    profile.GetImage_192(),
			Image512:    profile.GetImage_512(),
			Image1024:   profile.GetImage_1024(),
		},
		Presence: presence,
		Deleted:  value.GetDeleted(),
	}, nil
}

func encodeProtoDoNotDisturb(value domain.DoNotDisturb) *chatv1.DoNotDisturb {
	result := &chatv1.DoNotDisturb{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Enabled: value.Enabled}
	if !value.SnoozeUntil.IsZero() {
		result.SnoozeUntil = value.SnoozeUntil.Unix()
	}
	if !value.NextStartAt.IsZero() {
		result.NextStartAt = value.NextStartAt.Unix()
	}
	if !value.NextEndAt.IsZero() {
		result.NextEndAt = value.NextEndAt.Unix()
	}
	return result
}

func decodeProtoDoNotDisturb(value *chatv1.DoNotDisturb) (domain.DoNotDisturb, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.DoNotDisturb{}, errors.New("typed dnd response is incomplete")
	}
	result := domain.DoNotDisturb{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Enabled: value.GetEnabled()}
	if value.GetSnoozeUntil() != 0 {
		result.SnoozeUntil = time.Unix(value.GetSnoozeUntil(), 0).UTC()
	}
	if value.GetNextStartAt() != 0 {
		result.NextStartAt = time.Unix(value.GetNextStartAt(), 0).UTC()
	}
	if value.GetNextEndAt() != 0 {
		result.NextEndAt = time.Unix(value.GetNextEndAt(), 0).UTC()
	}
	return result, nil
}

func encodeProtoUserGroup(value domain.UserGroup) *chatv1.UserGroup {
	users := make([]string, 0, len(value.Users))
	for _, user := range value.Users {
		users = append(users, string(user))
	}
	channels := make([]string, 0, len(value.Channels))
	for _, channel := range value.Channels {
		channels = append(channels, string(channel))
	}
	result := &chatv1.UserGroup{WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), Name: value.Name, Handle: value.Handle, Description: value.Description, CreatorId: string(value.Creator), UpdatedBy: string(value.UpdatedBy), CreatedAt: value.CreatedAt.Unix(), UpdatedAt: value.UpdatedAt.Unix(), Enabled: value.Enabled, Users: users, Channels: channels}
	if !value.DeletedAt.IsZero() {
		result.DeletedAt = value.DeletedAt.Unix()
	}
	return result
}

func decodeProtoUserGroup(value *chatv1.UserGroup) (domain.UserGroup, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetName() == "" || value.GetHandle() == "" || value.GetCreatorId() == "" || value.GetUpdatedBy() == "" || value.GetCreatedAt() <= 0 || value.GetUpdatedAt() <= 0 {
		return domain.UserGroup{}, errors.New("typed user group response is incomplete")
	}
	users := make([]domain.UserID, 0, len(value.GetUsers()))
	for _, user := range value.GetUsers() {
		if user == "" {
			return domain.UserGroup{}, errors.New("typed user group member is empty")
		}
		users = append(users, domain.UserID(user))
	}
	channels := make([]domain.ConversationID, 0, len(value.GetChannels()))
	for _, channel := range value.GetChannels() {
		if channel == "" {
			return domain.UserGroup{}, errors.New("typed user group channel is empty")
		}
		channels = append(channels, domain.ConversationID(channel))
	}
	result := domain.UserGroup{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.UserGroupID(value.GetId()), Name: value.GetName(), Handle: value.GetHandle(), Description: value.GetDescription(), Creator: domain.UserID(value.GetCreatorId()), UpdatedBy: domain.UserID(value.GetUpdatedBy()), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC(), Enabled: value.GetEnabled(), Users: users, Channels: channels}
	if value.GetDeletedAt() != 0 {
		result.DeletedAt = time.Unix(value.GetDeletedAt(), 0).UTC()
	}
	return result, nil
}

func encodeProtoCall(value domain.Call) *chatv1.Call {
	participants := make([]string, 0, len(value.Participants))
	for _, user := range value.Participants {
		participants = append(participants, string(user))
	}
	result := &chatv1.Call{WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), ExternalUniqueId: value.ExternalUniqueID, ExternalDisplayId: value.ExternalDisplayID, JoinUrl: value.JoinURL, DesktopAppJoinUrl: value.DesktopAppJoinURL, Title: value.Title, CreatedBy: string(value.CreatedBy), Participants: participants, StartedAt: value.StartedAt.Unix(), DurationSeconds: value.DurationSeconds}
	if !value.EndedAt.IsZero() {
		result.EndedAt = value.EndedAt.Unix()
	}
	return result
}

func decodeProtoCall(value *chatv1.Call) (domain.Call, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetExternalUniqueId() == "" || value.GetJoinUrl() == "" || value.GetCreatedBy() == "" || value.GetStartedAt() <= 0 {
		return domain.Call{}, errors.New("typed call response is incomplete")
	}
	participants := make([]domain.UserID, 0, len(value.GetParticipants()))
	for _, user := range value.GetParticipants() {
		if user == "" {
			return domain.Call{}, errors.New("typed call participant is empty")
		}
		participants = append(participants, domain.UserID(user))
	}
	result := domain.Call{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.CallID(value.GetId()), ExternalUniqueID: value.GetExternalUniqueId(), ExternalDisplayID: value.GetExternalDisplayId(), JoinURL: value.GetJoinUrl(), DesktopAppJoinURL: value.GetDesktopAppJoinUrl(), Title: value.GetTitle(), CreatedBy: domain.UserID(value.GetCreatedBy()), Participants: participants, StartedAt: time.Unix(value.GetStartedAt(), 0).UTC(), DurationSeconds: value.GetDurationSeconds()}
	if value.GetEndedAt() != 0 {
		result.EndedAt = time.Unix(value.GetEndedAt(), 0).UTC()
	}
	return result, nil
}

func (r Remote) PresentEntityDetails(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, metadata string, userAuthRequired bool, userAuthURL, errorPayload string) error {
	out, err := r.entity.PresentDetails(ctx, &chatv1.EntityDetailsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: triggerID, Metadata: metadata, UserAuthRequired: userAuthRequired, UserAuthUrl: userAuthURL, Error: errorPayload})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "entity details presentation")
}

func (r Remote) PresentEntityComments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, comments, cursor string, canPostComment bool, deleteActionID string, userAuthRequired bool, userAuthURL, errorPayload string) error {
	out, err := r.entity.PresentComments(ctx, &chatv1.EntityCommentsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: triggerID, Comments: comments, Cursor: cursor, CanPostComment: canPostComment, DeleteActionId: deleteActionID, UserAuthRequired: userAuthRequired, UserAuthUrl: userAuthURL, Error: errorPayload})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "entity comments presentation")
}

func (r Remote) AcknowledgeEntityCommentAction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, comment, errorPayload string) error {
	out, err := r.entity.AcknowledgeCommentAction(ctx, &chatv1.EntityCommentActionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: triggerID, Comment: comment, Error: errorPayload})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "entity comment acknowledgement")
}

func (s *Server) PresentDetails(ctx context.Context, input *chatv1.EntityDetailsRequest) (*chatv1.EntityResponse, error) {
	err := s.implementation.PresentEntityDetails(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggerId(), input.GetMetadata(), input.GetUserAuthRequired(), input.GetUserAuthUrl(), input.GetError())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.EntityResponse{Ok: true}, nil
}

func (s *Server) PresentComments(ctx context.Context, input *chatv1.EntityCommentsRequest) (*chatv1.EntityResponse, error) {
	err := s.implementation.PresentEntityComments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggerId(), input.GetComments(), input.GetCursor(), input.GetCanPostComment(), input.GetDeleteActionId(), input.GetUserAuthRequired(), input.GetUserAuthUrl(), input.GetError())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.EntityResponse{Ok: true}, nil
}

func (s *Server) AcknowledgeCommentAction(ctx context.Context, input *chatv1.EntityCommentActionRequest) (*chatv1.EntityResponse, error) {
	err := s.implementation.AcknowledgeEntityCommentAction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggerId(), input.GetComment(), input.GetError())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.EntityResponse{Ok: true}, nil
}

func (r Remote) OpenIDConnectToken(ctx context.Context, clientID, clientSecret, code, redirectURI, grantType, refreshToken, codeVerifier string) (domain.OpenIDToken, error) {
	out, err := r.oauth.OpenIDConnectToken(ctx, &chatv1.OpenIDConnectTokenRequest{ClientId: clientID, ClientSecret: clientSecret, Code: code, RedirectUri: redirectURI, GrantType: grantType, RefreshToken: refreshToken, CodeVerifier: codeVerifier})
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	oauthToken := out.GetOauthToken()
	return domain.OpenIDToken{OAuthToken: domain.OAuthToken{AccessToken: oauthToken.GetAccessToken(), ClientID: oauthToken.GetClientId(), AppID: domain.AppID(oauthToken.GetAppId()), WorkspaceID: domain.WorkspaceID(oauthToken.GetWorkspaceId()), UserID: domain.UserID(oauthToken.GetUserId()), Scopes: append([]string(nil), oauthToken.GetScopes()...), TokenType: oauthToken.GetTokenType()}, IDToken: out.GetIdToken(), RefreshToken: out.GetRefreshToken()}, nil
}

func (r Remote) OpenIDConnectUserInfo(ctx context.Context, token string) (domain.OpenIDUserInfo, error) {
	out, err := r.oauth.OpenIDConnectUserInfo(ctx, &chatv1.OpenIDConnectUserInfoRequest{Token: token})
	if err != nil {
		return domain.OpenIDUserInfo{}, err
	}
	return domain.OpenIDUserInfo{Subject: domain.UserID(out.GetSubject()), UserID: domain.UserID(out.GetUserId()), WorkspaceID: domain.WorkspaceID(out.GetWorkspaceId()), Email: out.GetEmail(), EmailVerified: out.GetEmailVerified(), DateEmailVerified: out.GetDateEmailVerified(), Name: out.GetName(), GivenName: out.GetGivenName(), FamilyName: out.GetFamilyName(), Locale: out.GetLocale(), Picture: out.GetPicture(), TeamName: out.GetTeamName(), TeamDomain: out.GetTeamDomain(), UserImages: out.GetUserImages(), TeamImages: out.GetTeamImages(), TeamImageDefault: out.GetTeamImageDefault()}, nil
}

func (s *Server) OpenIDConnectToken(ctx context.Context, input *chatv1.OpenIDConnectTokenRequest) (*chatv1.OpenIDConnectTokenResponse, error) {
	value, err := s.implementation.OpenIDConnectToken(ctx, input.GetClientId(), input.GetClientSecret(), input.GetCode(), input.GetRedirectUri(), input.GetGrantType(), input.GetRefreshToken(), input.GetCodeVerifier())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.OpenIDConnectTokenResponse{OauthToken: &chatv1.OAuthToken{AccessToken: value.AccessToken, ClientId: value.ClientID, AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Scopes: value.Scopes, TokenType: value.TokenType}, IdToken: value.IDToken, RefreshToken: value.RefreshToken}, nil
}

func (s *Server) OpenIDConnectUserInfo(ctx context.Context, input *chatv1.OpenIDConnectUserInfoRequest) (*chatv1.OpenIDConnectUserInfoResponse, error) {
	value, err := s.implementation.OpenIDConnectUserInfo(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.OpenIDConnectUserInfoResponse{Subject: string(value.Subject), UserId: string(value.UserID), WorkspaceId: string(value.WorkspaceID), Email: value.Email, EmailVerified: value.EmailVerified, DateEmailVerified: value.DateEmailVerified, Name: value.Name, GivenName: value.GivenName, FamilyName: value.FamilyName, Locale: value.Locale, Picture: value.Picture, TeamName: value.TeamName, TeamDomain: value.TeamDomain, UserImages: value.UserImages, TeamImages: value.TeamImages, TeamImageDefault: value.TeamImageDefault}, nil
}

func (r Remote) AdminCreateIncomingWebhook(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, conversationID domain.ConversationID, botUserID domain.UserID) (domain.IncomingWebhook, string, error) {
	out, err := r.messages.AdminCreateIncomingWebhook(ctx, &chatv1.IncomingWebhookCreateRequest{WorkspaceId: string(workspaceID), UserId: string(actorID), AppId: string(appID), ConversationId: string(conversationID), BotUserId: string(botUserID)})
	if err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	return decodeProtoIncomingWebhook(out.GetWebhook())
}

func (r Remote) AdminSetIncomingWebhookEnabled(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, webhookID domain.IncomingWebhookID, enabled bool) error {
	out, err := r.messages.AdminSetIncomingWebhookEnabled(ctx, &chatv1.IncomingWebhookEnableRequest{WorkspaceId: string(workspaceID), UserId: string(actorID), WebhookId: string(webhookID), Enabled: enabled})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("incoming webhook mutation was not acknowledged")
	}
	return nil
}

func (r Remote) PostIncomingWebhook(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret, text, blocks string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	return r.PostIncomingWebhookWithAttachments(ctx, workspaceID, appID, secret, text, blocks, "", threadTimestamp, idempotencyKey)
}

func (s *Server) AdminCreateIncomingWebhook(ctx context.Context, input *chatv1.IncomingWebhookCreateRequest) (*chatv1.IncomingWebhookCreateResponse, error) {
	value, secret, err := s.implementation.AdminCreateIncomingWebhook(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.ConversationID(input.GetConversationId()), domain.UserID(input.GetBotUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.IncomingWebhookCreateResponse{Webhook: encodeProtoIncomingWebhook(value, secret)}, nil
}

func (s *Server) AdminSetIncomingWebhookEnabled(ctx context.Context, input *chatv1.IncomingWebhookEnableRequest) (*chatv1.IncomingWebhookMutationResponse, error) {
	err := s.implementation.AdminSetIncomingWebhookEnabled(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.IncomingWebhookID(input.GetWebhookId()), input.GetEnabled())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.IncomingWebhookMutationResponse{Ok: true}, nil
}

func (s *Server) PostIncomingWebhook(ctx context.Context, input *chatv1.IncomingWebhookPostRequest) (*chatv1.Message, error) {
	value, err := s.implementation.PostIncomingWebhookWithAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.AppID(input.GetAppId()), input.GetSecret(), input.GetText(), input.GetBlocks(), input.GetAttachments(), domain.MessageTimestamp(input.GetThreadTimestamp()), input.GetIdempotencyKey())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func encodeProtoIncomingWebhook(value domain.IncomingWebhook, secret string) *chatv1.IncomingWebhook {
	return &chatv1.IncomingWebhook{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), AppId: string(value.AppID), ConversationId: string(value.ConversationID), UserId: string(value.UserID), Enabled: value.Enabled, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Secret: secret}
}

func decodeProtoIncomingWebhook(value *chatv1.IncomingWebhook) (domain.IncomingWebhook, string, error) {
	if value == nil {
		return domain.IncomingWebhook{}, "", errors.New("missing incoming webhook")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	return domain.IncomingWebhook{ID: domain.IncomingWebhookID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()), ConversationID: domain.ConversationID(value.GetConversationId()), UserID: domain.UserID(value.GetUserId()), Enabled: value.GetEnabled(), CreatedAt: createdAt.UTC()}, value.GetSecret(), nil
}

func (r Remote) PostWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, text, blocks string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	return r.PostWithBlocksAndAttachments(ctx, workspaceID, userID, conversationID, text, blocks, "", threadTimestamp, idempotencyKey)
}

func (r Remote) UpdateWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, text, blocks string) (domain.Message, error) {
	return r.UpdateWithBlocksAndAttachments(ctx, workspaceID, userID, conversationID, timestamp, text, blocks, "")
}

func (s *Server) PostWithBlocks(ctx context.Context, input *chatv1.PostWithBlocksRequest) (*chatv1.Message, error) {
	value, err := s.implementation.PostWithBlocksAndAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetText(), input.GetBlocks(), input.GetAttachments(), domain.MessageTimestamp(input.GetThreadTimestamp()), input.GetIdempotencyKey())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) UpdateWithBlocks(ctx context.Context, input *chatv1.UpdateWithBlocksRequest) (*chatv1.Message, error) {
	value, err := s.implementation.UpdateWithBlocksAndAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetText(), input.GetBlocks(), input.GetAttachments())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (r Remote) ScheduleMessageWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text, blocks string, postAt time.Time) (domain.ScheduledMessage, error) {
	return r.ScheduleMessageWithBlocksAndAttachments(ctx, workspaceID, userID, channel, text, blocks, "", postAt)
}

func (r Remote) PostEphemeralWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, recipientID domain.UserID, text, blocks string) (domain.EphemeralMessage, error) {
	return r.PostEphemeralWithBlocksAndAttachments(ctx, workspaceID, userID, conversationID, recipientID, text, blocks, "")
}

func (r Remote) PostWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, text, blocks, attachments string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	out, err := r.messages.PostWithBlocks(ctx, &chatv1.PostWithBlocksRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Text: text, Blocks: blocks, Attachments: attachments, ThreadTimestamp: string(threadTimestamp), IdempotencyKey: idempotencyKey})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) PostIncomingWebhookWithAttachments(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret, text, blocks, attachments string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	out, err := r.messages.PostIncomingWebhook(ctx, &chatv1.IncomingWebhookPostRequest{WorkspaceId: string(workspaceID), AppId: string(appID), Secret: secret, Text: text, Blocks: blocks, Attachments: attachments, ThreadTimestamp: string(threadTimestamp), IdempotencyKey: idempotencyKey})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) PostEphemeralWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, recipientID domain.UserID, text, blocks, attachments string) (domain.EphemeralMessage, error) {
	out, err := r.messages.PostEphemeral(ctx, &chatv1.PostEphemeralRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), RecipientId: string(recipientID), Text: text, Blocks: blocks, Attachments: attachments})
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	return decodeProtoEphemeralMessage(out)
}

func (r Remote) UpdateWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, text, blocks, attachments string) (domain.Message, error) {
	out, err := r.messages.UpdateWithBlocks(ctx, &chatv1.UpdateWithBlocksRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Text: text, Blocks: blocks, Attachments: attachments})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) ScheduleMessageWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text, blocks, attachments string, postAt time.Time) (domain.ScheduledMessage, error) {
	out, err := r.scheduled.ScheduleMessage(ctx, &chatv1.ScheduleMessageRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(channel), Text: text, Blocks: blocks, Attachments: attachments, PostAt: postAt.Unix()})
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	return decodeProtoScheduledMessage(out)
}

func (r Remote) CreateExternalUpload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, mimeType string, size int64, ttl time.Duration) (domain.ExternalUpload, error) {
	out, err := r.files.CreateExternalUpload(ctx, &chatv1.ExternalUploadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, MimeType: mimeType, Size: size, TtlSeconds: int64(ttl / time.Second)})
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	return decodeProtoExternalUpload(out)
}

func (r Remote) UploadExternalFile(ctx context.Context, id domain.ExternalUploadID, size int64, source io.Reader) error {
	if id == "" || size < 0 || source == nil {
		return errors.New("external upload id, size, and source are required")
	}
	stream, err := r.files.UploadExternalFile(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&chatv1.ExternalUploadPart{Part: &chatv1.ExternalUploadPart_Metadata{Metadata: &chatv1.ExternalUploadRequest{UploadId: string(id), Size: size}}}); err != nil {
		return err
	}
	buffer := make([]byte, 64*1024)
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			if err := stream.Send(&chatv1.ExternalUploadPart{Part: &chatv1.ExternalUploadPart_Chunk{Chunk: append([]byte(nil), buffer[:read]...)}}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	_, err = stream.CloseAndRecv()
	return err
}

func (r Remote) CompleteExternalUpload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ExternalUploadID, title string, channels []domain.ConversationID, initialComment, blocks string, threadTimestamp domain.MessageTimestamp) (domain.File, error) {
	out, err := r.files.CompleteExternalUpload(ctx, &chatv1.CompleteExternalUploadRequest{WorkspaceId: string(workspaceID), UserId: string(userID), UploadId: string(id), Title: title, ChannelIds: conversationStrings(channels), InitialComment: initialComment, Blocks: blocks, ThreadTimestamp: string(threadTimestamp)})
	if err != nil {
		return domain.File{}, err
	}
	return decodeProtoFile(out)
}

func (s *Server) CreateExternalUpload(ctx context.Context, input *chatv1.ExternalUploadRequest) (*chatv1.ExternalUpload, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetName() == "" || input.GetMimeType() == "" || input.GetSize() < 0 || input.GetTtlSeconds() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, name, mime_type, size, and ttl_seconds are required")
	}
	value, err := s.implementation.CreateExternalUpload(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetMimeType(), input.GetSize(), time.Duration(input.GetTtlSeconds())*time.Second)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoExternalUpload(value), nil
}

func (s *Server) UploadExternalFile(stream chatv1.FilesService_UploadExternalFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetMetadata()
	if header == nil || header.GetUploadId() == "" || header.GetSize() < 0 {
		return status.Error(codes.InvalidArgument, "upload stream must begin with upload_id and size metadata")
	}
	reader, writer := io.Pipe()
	readErr := make(chan error, 1)
	go func() {
		defer writer.Close()
		for {
			part, recvErr := stream.Recv()
			if recvErr == io.EOF {
				readErr <- nil
				return
			}
			if recvErr != nil {
				readErr <- recvErr
				return
			}
			chunk := part.GetChunk()
			if chunk == nil {
				readErr <- status.Error(codes.InvalidArgument, "external upload stream contains a non-chunk part")
				return
			}
			if _, writeErr := writer.Write(chunk); writeErr != nil {
				readErr <- writeErr
				return
			}
		}
	}()
	uploadErr := s.implementation.UploadExternalFile(stream.Context(), domain.ExternalUploadID(header.GetUploadId()), header.GetSize(), reader)
	if uploadErr != nil {
		_ = reader.CloseWithError(uploadErr)
	}
	if err := <-readErr; err != nil {
		return err
	}
	if uploadErr != nil {
		return mapError(uploadErr)
	}
	return stream.SendAndClose(&chatv1.MutationResponse{Ok: true})
}

func (s *Server) CompleteExternalUpload(ctx context.Context, input *chatv1.CompleteExternalUploadRequest) (*chatv1.File, error) {
	if input.GetWorkspaceId() == "" || input.GetUserId() == "" || input.GetUploadId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id, user_id, and upload_id are required")
	}
	file, err := s.implementation.CompleteExternalUpload(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ExternalUploadID(input.GetUploadId()), input.GetTitle(), conversationIDs(input.GetChannelIds()), input.GetInitialComment(), input.GetBlocks(), domain.MessageTimestamp(input.GetThreadTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFile(file), nil
}

func encodeProtoExternalUpload(value domain.ExternalUpload) *chatv1.ExternalUpload {
	return &chatv1.ExternalUpload{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Uploader: string(value.Uploader), Name: value.Name, Title: value.Title, MimeType: value.MIMEType, Size: value.Size, Status: string(value.Status), CreatedAt: value.CreatedAt.Format(time.RFC3339Nano), ExpiresAt: value.ExpiresAt.Format(time.RFC3339Nano), UploadedAt: value.UploadedAt.Format(time.RFC3339Nano), CompletedAt: value.CompletedAt.Format(time.RFC3339Nano)}
}

func decodeProtoExternalUpload(value *chatv1.ExternalUpload) (domain.ExternalUpload, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUploader() == "" || value.GetName() == "" || value.GetMimeType() == "" || value.GetSize() < 0 || value.GetStatus() == "" {
		return domain.ExternalUpload{}, errors.New("typed external upload is incomplete")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	expires, err := time.Parse(time.RFC3339Nano, value.GetExpiresAt())
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	return domain.ExternalUpload{ID: domain.ExternalUploadID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Uploader: domain.UserID(value.GetUploader()), Name: value.GetName(), Title: value.GetTitle(), MIMEType: value.GetMimeType(), Size: value.GetSize(), Status: domain.ExternalUploadStatus(value.GetStatus()), CreatedAt: created, ExpiresAt: expires}, nil
}
