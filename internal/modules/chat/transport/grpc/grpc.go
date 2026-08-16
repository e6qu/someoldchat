package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Remote struct {
	apps          chatv1.AppsServiceClient
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
	savedItems    chatv1.SavedItemsServiceClient
	reminders     chatv1.RemindersServiceClient
	activity      chatv1.ActivityServiceClient
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

// mappedClientConn preserves the domain error contract when an implementation is
// moved behind gRPC. Every call and every stream message passes through
// mapRemoteError, which restores the sentinel the chat process failed with from
// the single error table in errors.go, so a caller inspecting the result with
// errors.Is gets the same answer in both compositions.
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
		apps:          chatv1.NewAppsServiceClient(conn),
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
		savedItems:    chatv1.NewSavedItemsServiceClient(conn),
		reminders:     chatv1.NewRemindersServiceClient(conn),
		activity:      chatv1.NewActivityServiceClient(conn),
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

// The huddle family shares one request shape and one response, so the four
// mutations and the read differ only in which rpc they call.
func (r Remote) huddle(ctx context.Context, call func(context.Context, *chatv1.HuddleRequest, ...grpc.CallOption) (*chatv1.Call, error), workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, title string) (domain.Call, error) {
	out, err := call(ctx, &chatv1.HuddleRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Title: title})
	if err != nil {
		return domain.Call{}, err
	}
	return decodeProtoCall(out)
}

func (r Remote) SendCallSignal(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, callID domain.CallID, recipient domain.UserID, kind domain.CallSignalKind, payload string) error {
	out, err := r.calls.SendCallSignal(ctx, &chatv1.CallSignalRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CallId: string(callID),
		RecipientId: string(recipient), Kind: string(kind), Payload: payload,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed call signal was not acknowledged")
	}
	return nil
}

func (r Remote) SendHuddleReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, callID domain.CallID, reaction string) error {
	out, err := r.calls.SendHuddleReaction(ctx, &chatv1.HuddleReactionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CallId: string(callID), Reaction: reaction,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed huddle reaction was not acknowledged")
	}
	return nil
}

func (r Remote) InviteToHuddle(ctx context.Context, workspaceID domain.WorkspaceID, actor, invitee domain.UserID, conversationID domain.ConversationID) error {
	out, err := r.calls.InviteToHuddle(ctx, &chatv1.HuddleInviteRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), InviteeId: string(invitee), ConversationId: string(conversationID),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed huddle invitation was not acknowledged")
	}
	return nil
}

func (r Remote) StartHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, title string) (domain.Call, error) {
	return r.huddle(ctx, r.calls.StartHuddle, workspaceID, userID, conversationID, title)
}

func (r Remote) JoinHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return r.huddle(ctx, r.calls.JoinHuddle, workspaceID, userID, conversationID, "")
}

func (r Remote) LeaveHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return r.huddle(ctx, r.calls.LeaveHuddle, workspaceID, userID, conversationID, "")
}

func (r Remote) EndHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return r.huddle(ctx, r.calls.EndHuddle, workspaceID, userID, conversationID, "")
}

func (r Remote) ActiveHuddle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return r.huddle(ctx, r.calls.GetActiveHuddle, workspaceID, userID, conversationID, "")
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

func (r Remote) ShareFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID, conversationID domain.ConversationID, threadTimestamp domain.MessageTimestamp) (domain.Message, error) {
	out, err := r.messages.ShareFile(ctx, &chatv1.ShareFileRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID),
		ConversationId: string(conversationID), ThreadTimestamp: string(threadTimestamp),
	})
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
func (r Remote) WorkspaceAnalytics(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, since time.Time) (domain.WorkspaceAnalytics, error) {
	input := &chatv1.WorkspaceAnalyticsRequest{WorkspaceId: string(workspaceID), UserId: string(userID)}
	if !since.IsZero() {
		input.Since = proto.Int64(since.Unix())
	}
	out, err := r.audit.GetWorkspaceAnalytics(ctx, input)
	if err != nil {
		return domain.WorkspaceAnalytics{}, err
	}
	return decodeProtoWorkspaceAnalytics(out), nil
}

func encodeProtoWorkspaceAnalytics(value domain.WorkspaceAnalytics) *chatv1.WorkspaceAnalytics {
	busiest := make([]*chatv1.ChannelActivity, 0, len(value.BusiestChannels))
	for _, entry := range value.BusiestChannels {
		busiest = append(busiest, &chatv1.ChannelActivity{ConversationId: string(entry.ConversationID), Name: entry.Name, Messages: int32(entry.Messages)})
	}
	result := &chatv1.WorkspaceAnalytics{
		Members: int32(value.Members), ActiveMembers: int32(value.ActiveMembers), Guests: int32(value.Guests), Admins: int32(value.Admins),
		PublicChannels: int32(value.PublicChannels), PrivateChannels: int32(value.PrivateChannels), ArchivedChannels: int32(value.ArchivedChannels),
		Messages: int32(value.Messages), RecentMessages: int32(value.RecentMessages),
		Files: int32(value.Files), RecentFiles: int32(value.RecentFiles), BusiestChannels: busiest,
	}
	if !value.Since.IsZero() {
		result.Since = proto.Int64(value.Since.Unix())
	}
	return result
}

func decodeProtoWorkspaceAnalytics(value *chatv1.WorkspaceAnalytics) domain.WorkspaceAnalytics {
	busiest := make([]domain.ChannelActivity, 0, len(value.GetBusiestChannels()))
	for _, entry := range value.GetBusiestChannels() {
		busiest = append(busiest, domain.ChannelActivity{ConversationID: domain.ConversationID(entry.GetConversationId()), Name: entry.GetName(), Messages: int(entry.GetMessages())})
	}
	result := domain.WorkspaceAnalytics{
		Members: int(value.GetMembers()), ActiveMembers: int(value.GetActiveMembers()), Guests: int(value.GetGuests()), Admins: int(value.GetAdmins()),
		PublicChannels: int(value.GetPublicChannels()), PrivateChannels: int(value.GetPrivateChannels()), ArchivedChannels: int(value.GetArchivedChannels()),
		Messages: int(value.GetMessages()), RecentMessages: int(value.GetRecentMessages()),
		Files: int(value.GetFiles()), RecentFiles: int(value.GetRecentFiles()), BusiestChannels: busiest,
	}
	if value.Since != nil {
		result.Since = time.Unix(value.GetSince(), 0).UTC()
	}
	return result
}

func (r Remote) ListAccessLogs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, before time.Time, limit, page int) ([]domain.AccessLog, bool, error) {
	input := &chatv1.AccessLogsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(limit), Page: int32(page)}
	if !before.IsZero() {
		// Set through the pointer rather than as a value: the Unix epoch is a
		// real instant whose seconds are 0, and a bare field could not tell it
		// from an absent one. Asking for logs before the epoch answered
		// "nothing" in one composition and "everything" in the other.
		input.Before = proto.Int64(before.Unix())
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
	in := &chatv1.HistoryRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending}
	out, err := r.messages.History(ctx, in)
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
}

func (r Remote) Search(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	return r.SearchMessages(ctx, workspaceID, userID, domain.MessageSearchRequest{Query: query, Page: request})
}

func (r Remote) SearchMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageSearchRequest) (domain.MessagePage, error) {
	in := &chatv1.SearchRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Query: request.Query,
		Limit: int32(request.Page.Limit), Cursor: string(request.Page.Cursor),
		ConversationId: string(request.Conversation), Sort: string(request.Sort), Direction: string(request.Direction),
	}
	out, err := r.messages.Search(ctx, in)
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
}

func (r Remote) RecordSearch(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string) error {
	out, err := r.messages.RecordSearch(ctx, &chatv1.RecordSearchRequest{
		WorkspaceId: string(workspaceID),
		UserId:      string(userID),
		Query:       query,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed record search response is incomplete")
	}
	return nil
}

func (r Remote) RecentSearches(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, limit int) ([]domain.SearchHistoryEntry, error) {
	out, err := r.messages.RecentSearches(ctx, &chatv1.RecentSearchesRequest{
		WorkspaceId: string(workspaceID),
		UserId:      string(userID),
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, err
	}
	values := make([]domain.SearchHistoryEntry, 0, len(out.GetEntries()))
	for _, entry := range out.GetEntries() {
		searchedAt, err := domain.ParseStoredTime(entry.GetSearchedAt())
		if err != nil {
			return nil, err
		}
		values = append(values, domain.SearchHistoryEntry{
			WorkspaceID: domain.WorkspaceID(entry.GetWorkspaceId()),
			UserID:      domain.UserID(entry.GetUserId()),
			Query:       entry.GetQuery(),
			SearchedAt:  searchedAt,
		})
	}
	return values, nil
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
		return domain.File{}, uploadFailure(err, func() error { _, recvErr := stream.CloseAndRecv(); return recvErr })
	}
	if err := sendChunks(source, "file source", func(chunk []byte) error {
		return stream.Send(&chatv1.UploadFilePart{Part: &chatv1.UploadFilePart_Chunk{Chunk: append([]byte(nil), chunk...)}})
	}); err != nil {
		return domain.File{}, uploadFailure(err, func() error { _, recvErr := stream.CloseAndRecv(); return recvErr })
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
		return domain.User{}, uploadFailure(err, func() error { _, recvErr := stream.CloseAndRecv(); return recvErr })
	}
	if err := sendChunks(source, "user photo source", func(chunk []byte) error {
		return stream.Send(&chatv1.UserPhotoUploadPart{Part: &chatv1.UserPhotoUploadPart_Chunk{Chunk: append([]byte(nil), chunk...)}})
	}); err != nil {
		return domain.User{}, uploadFailure(err, func() error { _, recvErr := stream.CloseAndRecv(); return recvErr })
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

func (r Remote) ListAppAuthorizations(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID) ([]domain.AppAuthorization, error) {
	out, err := r.auth.ListAppAuthorizations(ctx, &chatv1.AppAuthorizationsRequest{AppId: string(appID), WorkspaceId: string(workspaceID)})
	if err != nil {
		return nil, err
	}
	values := make([]domain.AppAuthorization, 0, len(out.GetAuthorizations()))
	for _, item := range out.GetAuthorizations() {
		values = append(values, domain.AppAuthorization{
			AppID: domain.AppID(item.GetAppId()), WorkspaceID: domain.WorkspaceID(item.GetWorkspaceId()),
			UserID: domain.UserID(item.GetUserId()), BotID: domain.BotID(item.GetBotId()),
			TokenType: domain.TokenType(item.GetTokenType()), Scopes: domain.NormalizeScopes(item.GetScopes()),
		})
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

func (r Remote) RevokeOIDCSessions(ctx context.Context, workspaceID domain.WorkspaceID, provider, subject, sid, tokenID string, expiresAt time.Time) error {
	out, err := r.auth.RevokeOIDCSessions(ctx, &chatv1.RevokeOIDCSessionsRequest{WorkspaceId: string(workspaceID), Provider: provider, Subject: subject, Sid: sid, TokenId: tokenID, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed OpenID Connect session revocation was not acknowledged")
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

func (r Remote) UninstallApp(ctx context.Context, clientID, clientSecret string, workspaceID domain.WorkspaceID, appID domain.AppID) error {
	out, err := r.auth.UninstallApp(ctx, &chatv1.UninstallAppRequest{ClientId: clientID, ClientSecret: clientSecret, WorkspaceId: string(workspaceID), AppId: string(appID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app uninstall was not acknowledged")
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
	// The stream carries the user the chat process resolved. The client used to
	// synthesise domain.User{ID, WorkspaceID} and ignore the metadata part
	// entirely, so a caller that read any other field of the returned user saw the
	// stored record in the monolith and an identifier-only stub across the seam.
	// A peer that sends no user (a chat process older than the field) still yields
	// the stub, which is what it used to send.
	if metadata.GetUser() != nil {
		user, err := decodeProtoUser(metadata.GetUser())
		if err != nil {
			cancel()
			return domain.User{}, nil, err
		}
		return user, &remoteUserPhotoReader{stream: stream, cancel: cancel, buffer: first.GetChunk()}, nil
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

func (r Remote) List(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID) (domain.List, error) {
	out, err := r.lists.GetList(ctx, &chatv1.ListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(id)})
	if err != nil {
		return domain.List{}, err
	}
	return decodeProtoList(out.GetList())
}

func (r Remote) ListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID) (domain.ListAccess, error) {
	out, err := r.lists.GetListAccess(ctx, &chatv1.ListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(id)})
	if err != nil {
		return domain.ListAccess{}, err
	}
	return decodeProtoListGrant(out), nil
}

func (r Remote) ListGrants(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID) ([]domain.ListAccess, error) {
	out, err := r.lists.ListGrants(ctx, &chatv1.ListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(id)})
	if err != nil {
		return nil, err
	}
	return decodeProtoListGrants(out.GetGrants()), nil
}

func (r Remote) Lists(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ListPage, error) {
	out, err := r.lists.ListLists(ctx, &chatv1.ListsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending})
	if err != nil {
		return domain.ListPage{}, err
	}
	return decodeProtoListPage(out)
}

func (r Remote) SearchLists(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ListSearchRequest) (domain.ListPage, error) {
	out, err := r.lists.SearchLists(ctx, &chatv1.SearchListsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Query: request.Query,
		Limit: int32(request.Page.Limit), Cursor: string(request.Page.Cursor),
		Sort: string(request.Sort), Direction: string(request.Direction),
	})
	if err != nil {
		return domain.ListPage{}, err
	}
	return decodeProtoListPage(out)
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

// ListAccessFor asks the server whether a member may open a list. It reuses the
// read RPC rather than adding one: "can they open it" is exactly "does GetList
// succeed for them", and a second question could answer differently from the
// one the write enforces.
func (r Remote) ListAccessFor(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID) error {
	_, err := r.lists.GetList(ctx, &chatv1.ListItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID)})
	return err
}

func (r Remote) AddListColumn(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, name string, columnType domain.ListColumnType, options []string) (domain.List, error) {
	out, err := r.lists.AddListColumn(ctx, &chatv1.AddListColumnRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(id),
		Name: name, Type: string(columnType), Options: options,
	})
	if err != nil {
		return domain.List{}, err
	}
	return decodeProtoList(out)
}

func (r Remote) RemoveListColumn(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, key string) (domain.List, error) {
	out, err := r.lists.RemoveListColumn(ctx, &chatv1.RemoveListColumnRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(id), Key: key,
	})
	if err != nil {
		return domain.List{}, err
	}
	return decodeProtoList(out)
}

func (r Remote) AssignListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, assignee domain.UserID, dueAt time.Time) (domain.ListItem, error) {
	out, err := r.lists.AssignListItem(ctx, &chatv1.AssignListItemRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID),
		AssigneeId: string(assignee), DueAtUnixNano: unixNanoOrZero(dueAt),
	})
	if err != nil {
		return domain.ListItem{}, err
	}
	return decodeProtoListItem(out)
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

func (r Remote) SetListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, access domain.AccessLevel, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	out, err := r.lists.SetListAccess(ctx, &chatv1.ListAccessRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), Access: string(access), ChannelIds: conversationStrings(channelIDs), UserIds: userStrings(userIDs)})
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

func (r Remote) CommentOnListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, text string) (domain.ListItemComment, error) {
	out, err := r.lists.CommentOnListItem(ctx, &chatv1.CommentOnListItemRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID), Text: text,
	})
	if err != nil {
		return domain.ListItemComment{}, err
	}
	return decodeProtoListItemComment(out), nil
}

func (r Remote) ListItemComments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, request domain.PageRequest) (domain.ListItemCommentPage, error) {
	out, err := r.lists.ListItemComments(ctx, &chatv1.ListItemCommentsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID),
		Limit: int32(request.Limit), Cursor: string(request.Cursor),
	})
	if err != nil {
		return domain.ListItemCommentPage{}, err
	}
	return decodeProtoListItemCommentPage(out)
}

func (r Remote) DeleteListItemComment(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListItemCommentID) error {
	out, err := r.lists.DeleteListItemComment(ctx, &chatv1.DeleteListItemCommentRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CommentId: string(id),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed delete list item comment response was not acknowledged")
	}
	return nil
}

func (r Remote) AttachFileToListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fileID domain.FileID) (domain.ListItemFile, error) {
	out, err := r.lists.AttachFileToListItem(ctx, &chatv1.AttachFileToListItemRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID), FileId: string(fileID),
	})
	if err != nil {
		return domain.ListItemFile{}, err
	}
	return decodeProtoListItemFile(out), nil
}

func (r Remote) ListItemFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID) ([]domain.File, error) {
	out, err := r.lists.ListItemFiles(ctx, &chatv1.ListItemFilesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID),
	})
	if err != nil {
		return nil, err
	}
	files := make([]domain.File, 0, len(out.GetFiles()))
	for _, file := range out.GetFiles() {
		decoded, err := decodeProtoFile(file)
		if err != nil {
			return nil, err
		}
		files = append(files, decoded)
	}
	return files, nil
}

func (r Remote) DetachFileFromListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fileID domain.FileID) error {
	out, err := r.lists.DetachFileFromListItem(ctx, &chatv1.DetachFileFromListItemRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ListId: string(listID), ItemId: string(itemID), FileId: string(fileID),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed detach file from list item response was not acknowledged")
	}
	return nil
}

func (r Remote) SetFileDescription(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID, description string) error {
	out, err := r.files.SetFileDescription(ctx, &chatv1.SetFileDescriptionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), FileId: string(fileID), Description: description,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed set file description response was not acknowledged")
	}
	return nil
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

func (r Remote) SearchFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.FileSearchRequest) (domain.FilePage, error) {
	in := &chatv1.SearchFilesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Query: request.Query,
		Count: int32(request.Count), Page: int32(request.Page), Sort: string(request.Sort), Direction: string(request.Direction),
		ConversationId: string(request.Conversation),
	}
	out, err := r.files.SearchFiles(ctx, in)
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

func (r Remote) CreateConversationCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channelID domain.ConversationID, title, documentContent string) (domain.Canvas, error) {
	out, err := r.canvases.CreateConversationCanvas(ctx, &chatv1.CreateCanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Title: title, DocumentContent: documentContent, ChannelId: string(channelID)})
	if err != nil {
		return domain.Canvas{}, err
	}
	return decodeProtoCanvas(out)
}

func (r Remote) ConversationCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channelID domain.ConversationID) (domain.Canvas, error) {
	out, err := r.canvases.ConversationCanvas(ctx, &chatv1.CreateCanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(channelID)})
	if err != nil {
		return domain.Canvas{}, err
	}
	return decodeProtoCanvas(out)
}

func (r Remote) Canvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) (domain.Canvas, error) {
	out, err := r.canvases.GetCanvas(ctx, &chatv1.CanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id)})
	if err != nil {
		return domain.Canvas{}, err
	}
	return decodeProtoCanvas(out)
}

func (r Remote) CanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) (domain.CanvasAccess, error) {
	out, err := r.canvases.GetCanvasAccess(ctx, &chatv1.CanvasRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id)})
	if err != nil {
		return domain.CanvasAccess{}, err
	}
	return domain.CanvasAccess{CanvasID: domain.CanvasID(out.GetCanvasId()), EntityType: domain.GrantEntity(out.GetEntityType()), EntityID: out.GetEntityId(), Access: domain.AccessLevel(out.GetAccess())}, nil
}

func (r Remote) Canvases(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.CanvasPage, error) {
	out, err := r.canvases.ListCanvases(ctx, &chatv1.CanvasesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending})
	if err != nil {
		return domain.CanvasPage{}, err
	}
	return decodeProtoCanvasPage(out)
}

func (r Remote) CommentOnCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, sectionID, text string) (domain.CanvasComment, error) {
	out, err := r.canvases.CommentOnCanvas(ctx, &chatv1.CommentOnCanvasRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), SectionId: sectionID, Text: text,
	})
	if err != nil {
		return domain.CanvasComment{}, err
	}
	return decodeProtoCanvasComment(out), nil
}

func (r Remote) CanvasComments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, request domain.PageRequest) (domain.CanvasCommentPage, error) {
	out, err := r.canvases.CanvasComments(ctx, &chatv1.CanvasCommentsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id),
		Limit: int32(request.Limit), Cursor: string(request.Cursor),
	})
	if err != nil {
		return domain.CanvasCommentPage{}, err
	}
	return decodeProtoCanvasCommentPage(out)
}

func (r Remote) DeleteCanvasComment(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasCommentID) error {
	out, err := r.canvases.DeleteCanvasComment(ctx, &chatv1.DeleteCanvasCommentRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CommentId: string(id),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed delete canvas comment response was not acknowledged")
	}
	return nil
}

func (r Remote) CanvasGrants(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) ([]domain.CanvasAccess, error) {
	out, err := r.canvases.CanvasGrants(ctx, &chatv1.CanvasGrantsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id)})
	if err != nil {
		return nil, err
	}
	return decodeProtoCanvasGrants(out.GetGrants()), nil
}

func (r Remote) CanvasRevisions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, request domain.PageRequest) (domain.CanvasRevisionPage, error) {
	out, err := r.canvases.CanvasRevisions(ctx, &chatv1.CanvasRevisionsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id),
		Limit: int32(request.Limit), Cursor: string(request.Cursor),
	})
	if err != nil {
		return domain.CanvasRevisionPage{}, err
	}
	return decodeProtoCanvasRevisionPage(out)
}

func (r Remote) RestoreCanvasRevision(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, version int64) (domain.Canvas, error) {
	out, err := r.canvases.RestoreCanvasRevision(ctx, &chatv1.RestoreCanvasRevisionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), Version: version,
	})
	if err != nil {
		return domain.Canvas{}, err
	}
	return decodeProtoCanvas(out)
}

func (r Remote) SearchCanvases(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.CanvasSearchRequest) (domain.CanvasPage, error) {
	out, err := r.canvases.SearchCanvases(ctx, &chatv1.SearchCanvasesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Query: request.Query,
		Limit: int32(request.Page.Limit), Cursor: string(request.Page.Cursor),
		Sort: string(request.Sort), Direction: string(request.Direction),
	})
	if err != nil {
		return domain.CanvasPage{}, err
	}
	return decodeProtoCanvasPage(out)
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

func (r Remote) SetCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, access domain.AccessLevel, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	out, err := r.canvases.SetCanvasAccess(ctx, &chatv1.CanvasAccessRequest{WorkspaceId: string(workspaceID), UserId: string(userID), CanvasId: string(id), AccessLevel: string(access), ChannelIds: conversationStrings(channelIDs), UserIds: userStrings(userIDs)})
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
		result = append(result, domain.CanvasSection{ID: section.GetId(), Type: domain.CanvasSectionType(section.GetType()), Text: section.GetText()})
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

func (r Remote) DiscoverableContacts(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, emails []string) ([]domain.User, error) {
	out, err := r.directory.DiscoverableContacts(ctx, &chatv1.DiscoverableContactsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Emails: emails})
	if err != nil {
		return nil, err
	}
	users := make([]domain.User, 0, len(out.GetUsers()))
	for _, encoded := range out.GetUsers() {
		user, decodeErr := decodeProtoUser(encoded)
		if decodeErr != nil {
			return nil, decodeErr
		}
		users = append(users, user)
	}
	return users, nil
}

func (r Remote) AdminCreateBarrier(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, primary domain.UserGroupID, barrieredFrom []domain.UserGroupID, subjects []domain.BarrierSubject) (domain.InformationBarrier, error) {
	out, err := r.directory.AdminCreateBarrier(ctx, barrierMutation(workspaceID, userID, "", primary, barrieredFrom, subjects))
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	return decodeProtoBarrier(out), nil
}

func (r Remote) AdminUpdateBarrier(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.BarrierID, primary domain.UserGroupID, barrieredFrom []domain.UserGroupID, subjects []domain.BarrierSubject) (domain.InformationBarrier, error) {
	out, err := r.directory.AdminUpdateBarrier(ctx, barrierMutation(workspaceID, userID, id, primary, barrieredFrom, subjects))
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	return decodeProtoBarrier(out), nil
}

func (r Remote) AdminDeleteBarrier(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.BarrierID) error {
	out, err := r.directory.AdminDeleteBarrier(ctx, barrierMutation(workspaceID, userID, id, "", nil, nil))
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed barrier deletion was not acknowledged")
	}
	return nil
}

func (r Remote) AdminBarriers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.InformationBarrierPage, error) {
	out, err := r.directory.AdminBarriers(ctx, &chatv1.BarriersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.InformationBarrierPage{}, err
	}
	barriers := make([]domain.InformationBarrier, 0, len(out.GetBarriers()))
	for _, encoded := range out.GetBarriers() {
		barriers = append(barriers, decodeProtoBarrier(encoded))
	}
	return domain.InformationBarrierPage{Barriers: barriers, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func barrierMutation(workspaceID domain.WorkspaceID, userID domain.UserID, id domain.BarrierID, primary domain.UserGroupID, barrieredFrom []domain.UserGroupID, subjects []domain.BarrierSubject) *chatv1.BarrierMutationRequest {
	return &chatv1.BarrierMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), BarrierId: string(id),
		PrimaryUsergroupId: string(primary), BarrieredFromIds: encodeUserGroupIDs(barrieredFrom),
		Subjects: encodeBarrierSubjects(subjects),
	}
}

func encodeUserGroupIDs(values []domain.UserGroupID) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, string(value))
	}
	return ids
}

func decodeUserGroupIDs(values []string) []domain.UserGroupID {
	ids := make([]domain.UserGroupID, 0, len(values))
	for _, value := range values {
		ids = append(ids, domain.UserGroupID(value))
	}
	return ids
}

func encodeBarrierSubjects(values []domain.BarrierSubject) []string {
	subjects := make([]string, 0, len(values))
	for _, value := range values {
		subjects = append(subjects, string(value))
	}
	return subjects
}

func decodeBarrierSubjects(values []string) []domain.BarrierSubject {
	subjects := make([]domain.BarrierSubject, 0, len(values))
	for _, value := range values {
		subjects = append(subjects, domain.BarrierSubject(value))
	}
	return subjects
}

func encodeProtoBarrier(value domain.InformationBarrier) *chatv1.InformationBarrier {
	return &chatv1.InformationBarrier{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID),
		PrimaryUsergroupId: string(value.PrimaryGroupID),
		BarrieredFromIds:   encodeUserGroupIDs(value.BarrieredFromIDs),
		Subjects:           encodeBarrierSubjects(value.Subjects),
		UpdatedAt:          optionalUnixNano(value.UpdatedAt),
	}
}

func decodeProtoBarrier(value *chatv1.InformationBarrier) domain.InformationBarrier {
	return domain.InformationBarrier{
		ID: domain.BarrierID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		PrimaryGroupID:   domain.UserGroupID(value.GetPrimaryUsergroupId()),
		BarrieredFromIDs: decodeUserGroupIDs(value.GetBarrieredFromIds()),
		Subjects:         decodeBarrierSubjects(value.GetSubjects()),
		UpdatedAt:        optionalTimeFromUnixNano(value.GetUpdatedAt()),
	}
}

func (r Remote) AdminSetSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targets []domain.UserID, settings domain.SessionSettings) error {
	request := sessionSettingsMutation(workspaceID, userID, targets)
	request.Settings = encodeProtoSessionSettings(settings)
	out, err := r.directory.AdminSetSessionSettings(ctx, request)
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed session settings write was not acknowledged")
	}
	return nil
}

func (r Remote) AdminClearSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targets []domain.UserID) error {
	out, err := r.directory.AdminClearSessionSettings(ctx, sessionSettingsMutation(workspaceID, userID, targets))
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed session settings clearance was not acknowledged")
	}
	return nil
}

func (r Remote) AdminSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targets []domain.UserID) ([]domain.SessionSettings, error) {
	out, err := r.directory.AdminSessionSettings(ctx, sessionSettingsMutation(workspaceID, userID, targets))
	if err != nil {
		return nil, err
	}
	settings := make([]domain.SessionSettings, 0, len(out.GetSettings()))
	for _, encoded := range out.GetSettings() {
		settings = append(settings, decodeProtoSessionSettings(encoded))
	}
	return settings, nil
}

func (r Remote) MemberSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.SessionSettings, error) {
	out, err := r.directory.MemberSessionSettings(ctx, &chatv1.WorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.SessionSettings{}, err
	}
	return decodeProtoSessionSettings(out), nil
}

func (r Remote) MemberMustUsePasswordSignIn(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (bool, error) {
	out, err := r.directory.MemberMustUsePasswordSignIn(ctx, &chatv1.WorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return false, err
	}
	return out.GetMustUsePasswordSignIn(), nil
}

func sessionSettingsMutation(workspaceID domain.WorkspaceID, userID domain.UserID, targets []domain.UserID) *chatv1.SessionSettingsMutationRequest {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, string(target))
	}
	return &chatv1.SessionSettingsMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserIds: ids}
}

func encodeProtoSessionSettings(value domain.SessionSettings) *chatv1.SessionSettings {
	return &chatv1.SessionSettings{
		UserId:                string(value.UserID),
		WorkspaceId:           string(value.WorkspaceID),
		DurationSeconds:       int64(value.Duration),
		DesktopAppBrowserQuit: value.DesktopAppBrowserQuit,
		MobileDeviceCheck:     value.MobileDeviceCheck,
		UpdatedAt:             optionalUnixNano(value.UpdatedAt),
	}
}

func decodeProtoSessionSettings(value *chatv1.SessionSettings) domain.SessionSettings {
	return domain.SessionSettings{
		UserID:                domain.UserID(value.GetUserId()),
		WorkspaceID:           domain.WorkspaceID(value.GetWorkspaceId()),
		Duration:              domain.SessionDuration(value.GetDurationSeconds()),
		DesktopAppBrowserQuit: value.GetDesktopAppBrowserQuit(),
		MobileDeviceCheck:     value.GetMobileDeviceCheck(),
		UpdatedAt:             optionalTimeFromUnixNano(value.GetUpdatedAt()),
	}
}

func (r Remote) AdminAssignAuthPolicy(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, entityIDs []string) error {
	out, err := r.directory.AdminAssignAuthPolicy(ctx, authPolicyMutation(workspaceID, userID, policy, kind, entityIDs))
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed authentication policy assignment was not acknowledged")
	}
	return nil
}

func (r Remote) AdminRemoveAuthPolicyEntities(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, entityIDs []string) error {
	out, err := r.directory.AdminRemoveAuthPolicyEntities(ctx, authPolicyMutation(workspaceID, userID, policy, kind, entityIDs))
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed authentication policy removal was not acknowledged")
	}
	return nil
}

func (r Remote) AdminAuthPolicyEntities(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, request domain.PageRequest) (domain.AuthPolicyEntityPage, error) {
	out, err := r.directory.AdminAuthPolicyEntities(ctx, &chatv1.AuthPolicyEntitiesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), PolicyName: string(policy),
		EntityType: string(kind), Limit: int32(request.Limit), Cursor: string(request.Cursor),
	})
	if err != nil {
		return domain.AuthPolicyEntityPage{}, err
	}
	entities := make([]domain.AuthPolicyEntity, 0, len(out.GetEntities()))
	for _, encoded := range out.GetEntities() {
		entities = append(entities, decodeProtoAuthPolicyEntity(encoded))
	}
	return domain.AuthPolicyEntityPage{Entities: entities, TotalCount: int(out.GetTotalCount()),
		NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func authPolicyMutation(workspaceID domain.WorkspaceID, userID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, entityIDs []string) *chatv1.AuthPolicyMutationRequest {
	entities := make([]string, 0, len(entityIDs))
	entities = append(entities, entityIDs...)
	return &chatv1.AuthPolicyMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID),
		PolicyName: string(policy), EntityType: string(kind), EntityIds: entities}
}

func encodeProtoAuthPolicyEntity(value domain.AuthPolicyEntity) *chatv1.AuthPolicyEntity {
	return &chatv1.AuthPolicyEntity{
		PolicyName:  string(value.Policy),
		EntityType:  string(value.EntityType),
		EntityId:    value.EntityID,
		WorkspaceId: string(value.WorkspaceID),
		CreatedAt:   optionalUnixNano(value.CreatedAt),
	}
}

func decodeProtoAuthPolicyEntity(value *chatv1.AuthPolicyEntity) domain.AuthPolicyEntity {
	return domain.AuthPolicyEntity{
		Policy:      domain.AuthPolicyName(value.GetPolicyName()),
		EntityType:  domain.PolicyEntityType(value.GetEntityType()),
		EntityID:    value.GetEntityId(),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		CreatedAt:   optionalTimeFromUnixNano(value.GetCreatedAt()),
	}
}

func (r Remote) AdminRequestExport(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, kind string, bounds map[string]int64) error {
	out, err := r.directory.AdminRequestExport(ctx, &chatv1.ExportRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Kind: kind, Bounds: bounds,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed export request was not acknowledged")
	}
	return nil
}

func (r Remote) RequestWorkflowStepResponsesExport(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, stepID string) error {
	out, err := r.directory.RequestWorkflowStepResponsesExport(ctx, &chatv1.ExportRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID), StepId: stepID,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed step response export was not acknowledged")
	}
	return nil
}

func (r Remote) AdminAnomalyAllowList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.AnomalyAllowList, error) {
	out, err := r.directory.AdminAnomalyAllowList(ctx, &chatv1.AnomalyAllowListRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.AnomalyAllowList{}, err
	}
	return decodeProtoAnomalyAllowList(out), nil
}

func (r Remote) AdminSetAnomalyAllowList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, addresses, reasons []string) (domain.AnomalyAllowList, error) {
	out, err := r.directory.AdminSetAnomalyAllowList(ctx, &chatv1.AnomalyAllowListRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID),
		IpAddresses: append([]string{}, addresses...), Reasons: append([]string{}, reasons...),
	})
	if err != nil {
		return domain.AnomalyAllowList{}, err
	}
	return decodeProtoAnomalyAllowList(out), nil
}

func (r Remote) TeamBillingInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspacePlan, error) {
	out, err := r.directory.TeamBillingInfo(ctx, &chatv1.WorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return "", err
	}
	return domain.WorkspacePlan(out.GetPlan()), nil
}

func encodeProtoAnomalyAllowList(value domain.AnomalyAllowList) *chatv1.AnomalyAllowList {
	return &chatv1.AnomalyAllowList{
		WorkspaceId: string(value.WorkspaceID),
		IpAddresses: append([]string{}, value.IPAddresses...),
		Reasons:     append([]string{}, value.Reasons...),
		UpdatedAt:   optionalUnixNano(value.UpdatedAt),
	}
}

func decodeProtoAnomalyAllowList(value *chatv1.AnomalyAllowList) domain.AnomalyAllowList {
	return domain.AnomalyAllowList{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		IPAddresses: append([]string{}, value.GetIpAddresses()...),
		Reasons:     append([]string{}, value.GetReasons()...),
		UpdatedAt:   optionalTimeFromUnixNano(value.GetUpdatedAt()),
	}
}

func (r Remote) AdminAnalytics(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, kind domain.AnalyticsKind, day time.Time) ([]domain.AnalyticsRow, error) {
	out, err := r.directory.AdminAnalytics(ctx, &chatv1.AnalyticsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Kind: string(kind), Day: optionalUnixNano(day),
	})
	if err != nil {
		return nil, err
	}
	rows := make([]domain.AnalyticsRow, 0, len(out.GetRows()))
	for _, encoded := range out.GetRows() {
		rows = append(rows, decodeProtoAnalyticsRow(encoded))
	}
	return rows, nil
}

func encodeProtoAnalyticsRow(value domain.AnalyticsRow) *chatv1.AnalyticsRow {
	return &chatv1.AnalyticsRow{
		Kind: string(value.Kind), Date: value.Date, EntityId: value.EntityID, Name: value.Name,
		MessagesPosted: int32(value.MessagesPosted), ReactionsAdded: int32(value.ReactionsAdded),
		MemberCount: int32(value.MemberCount), IsActive: value.IsActive,
	}
}

func decodeProtoAnalyticsRow(value *chatv1.AnalyticsRow) domain.AnalyticsRow {
	return domain.AnalyticsRow{
		Kind: domain.AnalyticsKind(value.GetKind()), Date: value.GetDate(), EntityID: value.GetEntityId(),
		Name: value.GetName(), MessagesPosted: int(value.GetMessagesPosted()),
		ReactionsAdded: int(value.GetReactionsAdded()), MemberCount: int(value.GetMemberCount()),
		IsActive: value.GetIsActive(),
	}
}

func (r Remote) AdminAddRoleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, roleID string, entityIDs []string, targets []domain.UserID) error {
	out, err := r.directory.AdminAddRoleAssignments(ctx, roleAssignmentMutation(workspaceID, userID, roleID, entityIDs, targets))
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed role assignment was not acknowledged")
	}
	return nil
}

func (r Remote) AdminRemoveRoleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, roleID string, entityIDs []string, targets []domain.UserID) error {
	out, err := r.directory.AdminRemoveRoleAssignments(ctx, roleAssignmentMutation(workspaceID, userID, roleID, entityIDs, targets))
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed role removal was not acknowledged")
	}
	return nil
}

func (r Remote) AdminListRoleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, roleID string, request domain.PageRequest) (domain.RoleAssignmentPage, error) {
	out, err := r.directory.AdminListRoleAssignments(ctx, &chatv1.RoleAssignmentsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), RoleId: roleID, Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.RoleAssignmentPage{}, err
	}
	assignments := make([]domain.RoleAssignment, 0, len(out.GetAssignments()))
	for _, encoded := range out.GetAssignments() {
		assignments = append(assignments, decodeProtoRoleAssignment(encoded))
	}
	return domain.RoleAssignmentPage{Assignments: assignments, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func roleAssignmentMutation(workspaceID domain.WorkspaceID, userID domain.UserID, roleID string, entityIDs []string, targets []domain.UserID) *chatv1.RoleAssignmentMutationRequest {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, string(target))
	}
	entities := make([]string, 0, len(entityIDs))
	entities = append(entities, entityIDs...)
	return &chatv1.RoleAssignmentMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), RoleId: roleID, EntityIds: entities, TargetUserIds: ids}
}

func encodeProtoRoleAssignment(value domain.RoleAssignment) *chatv1.RoleAssignment {
	return &chatv1.RoleAssignment{
		RoleId:      value.RoleID,
		EntityId:    value.EntityID,
		UserId:      string(value.UserID),
		WorkspaceId: string(value.WorkspaceID),
		CreatedAt:   optionalUnixNano(value.CreatedAt),
	}
}

func decodeProtoRoleAssignment(value *chatv1.RoleAssignment) domain.RoleAssignment {
	return domain.RoleAssignment{
		RoleID:      value.GetRoleId(),
		EntityID:    value.GetEntityId(),
		UserID:      domain.UserID(value.GetUserId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		CreatedAt:   optionalTimeFromUnixNano(value.GetCreatedAt()),
	}
}

func (r Remote) UserExpiration(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID) (time.Time, error) {
	out, err := r.directory.UserExpiration(ctx, &chatv1.SetUserExpirationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID)})
	if err != nil {
		return time.Time{}, err
	}
	return optionalTimeFromUnixNano(out.GetExpirationUnixNano()), nil
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

func (r Remote) UserSessions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID) ([]domain.WorkspaceSession, error) {
	out, err := r.directory.UserSessions(ctx, &chatv1.ResetUserSessionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserId: string(targetID)})
	if err != nil {
		return nil, err
	}
	return decodeProtoWorkspaceSessions(out.GetSessions()), nil
}

func (r Remote) ResetUserSessionsBulk(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targets []domain.UserID) error {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, string(target))
	}
	out, err := r.directory.ResetUserSessionsBulk(ctx, &chatv1.ResetUserSessionsBulkRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TargetUserIds: ids})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed bulk session reset was not acknowledged")
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

func (r Remote) AdminBulkArchiveConversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID) error {
	return r.bulkConversations(ctx, r.mutations.AdminBulkArchiveConversations, workspaceID, userID, ids, "archive")
}

func (r Remote) AdminBulkDeleteConversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID) error {
	return r.bulkConversations(ctx, r.mutations.AdminBulkDeleteConversations, workspaceID, userID, ids, "deletion")
}

func (r Remote) bulkConversations(ctx context.Context, call func(context.Context, *chatv1.BulkConversationsRequest, ...grpc.CallOption) (*chatv1.MutationResponse, error), workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID, what string) error {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, string(id))
	}
	out, err := call(ctx, &chatv1.BulkConversationsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationIds: values})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed bulk conversation " + what + " was not acknowledged")
	}
	return nil
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

func (r Remote) AdminConvertConversationToPublic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	out, err := r.mutations.AdminConvertConversationToPublic(ctx, &chatv1.ConvertConversationToPrivateRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
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

func (r Remote) SearchPeople(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.UserPage, error) {
	out, err := r.directory.SearchPeople(ctx, &chatv1.SearchPeopleRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Query: query, Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.UserPage{}, err
	}
	return decodeProtoUserPage(out)
}

func (r Remote) SearchChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.ConversationPage, error) {
	out, err := r.directory.SearchChannels(ctx, &chatv1.SearchConversationsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Query: query, Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.ConversationPage{}, err
	}
	return decodeProtoConversationPage(out)
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
	out, err := r.directory.SetConversationPrefs(ctx, &chatv1.SetConversationPrefsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Prefs: encodeProtoConversationPrefs(value)})
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

// WorkspaceMembership reads one membership, falling back to the scan it replaced
// when the chat peer does not serve the method yet.
//
// This call is on every sign-in (internal/web/identity.go workspaceRole) and on
// /me and /auth/validation (internal/web/handler.go currentRole). The http and
// chat processes of separate-chat-replicated carry independent replica counts, so
// a rollout always runs an updated http replica against a chat replica that
// predates this method, and an updated http replica asking for a method the peer
// does not serve gets codes.Unimplemented. codes.Unimplemented has no fallback
// class and cannot have one — it is in libraryProducedCodes — so the failure
// reached internal/web as a raw status and every sign-in failed for the whole
// skew window. buf breaking with WIRE_JSON permits adding an RPC, so the proto
// gate does not see it either.
//
// The fallback is the operation this method replaced: page the workspace through
// AdminListUsers, which the older peer serves, and take the one row. It is
// O(workspace) for an O(1) question, which is why it was replaced, and it runs
// only against a peer that predates the method.
//
// It carries that operation's authority, which is the authority the older peer
// applies to it — the change that added this method also made AdminListUsers
// refuse a plain member. So against a peer old enough to answer Unimplemented
// the fallback answers, and against any newer peer it is never reached. If a
// peer ever serves an admin-gated AdminListUsers *and* no GetWorkspaceMembership,
// a member's own read is refused with that peer's own sentinel rather than with a
// bare status, which is still legible. The ordering that avoids the window
// entirely is to update the chat process first.
func (r Remote) WorkspaceMembership(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) (domain.WorkspaceMembership, error) {
	out, err := r.directory.GetWorkspaceMembership(ctx, &chatv1.WorkspaceMembershipRequest{WorkspaceId: string(workspaceID), UserId: string(actorID), TargetUserId: string(targetID)})
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			return domain.WorkspaceMembership{}, err
		}
		return r.workspaceMembershipByScan(ctx, workspaceID, actorID, targetID)
	}
	return decodeProtoWorkspaceMembership(out)
}

// workspaceMembershipByScan is the pre-GetWorkspaceMembership implementation,
// kept only as the rolling-skew fallback above.
func (r Remote) workspaceMembershipByScan(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) (domain.WorkspaceMembership, error) {
	request := domain.PageRequest{Limit: workspaceScanPageSize}
	for {
		page, err := r.AdminListUsers(ctx, workspaceID, actorID, request)
		if err != nil {
			return domain.WorkspaceMembership{}, err
		}
		for _, entry := range page.Users {
			if entry.User.ID == targetID {
				return entry.Membership, nil
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			return domain.WorkspaceMembership{}, storepkg.ErrNotFound
		}
		request.Cursor = page.NextCursor
	}
}

// workspaceScanPageSize is the page the skew fallback reads the directory in. It
// is a page size, not a bound: workspaceMembershipByScan follows the cursor until
// it finds the row or the directory ends.
const workspaceScanPageSize = 200

// ProvisionExternalUser and SynchronizeExternalUserRole carry no actor; see the
// contract on service.ProvisionExternalUser. The connection this crosses is
// mutually authenticated, which is what stands in for an end-user authority
// check.
//
// Neither has a rolling-skew fallback, and neither can have one: every
// old-contract operation that creates a user or changes a role takes an actor
// and requires that actor to be a workspace administrator, and these two exist
// precisely because the identity provider's assertion — not an end user — is the
// authority. skewFailure therefore turns codes.Unimplemented into a statement of
// what is wrong, so the operator sees a version-skew failure rather than a bare
// status. The deployment ordering that avoids it is to update the chat process
// before the HTTP process, which the deployment documentation has to state.
func (r Remote) ProvisionExternalUser(ctx context.Context, workspaceID domain.WorkspaceID, email, realName string, role domain.WorkspaceRole) (domain.User, error) {
	out, err := r.directory.ProvisionExternalUser(ctx, &chatv1.ProvisionExternalUserRequest{WorkspaceId: string(workspaceID), Email: email, RealName: realName, Role: string(role)})
	if err != nil {
		return domain.User{}, skewFailure("ProvisionExternalUser", err)
	}
	return decodeProtoUser(out)
}

func (r Remote) SynchronizeExternalUserRole(ctx context.Context, workspaceID domain.WorkspaceID, targetID domain.UserID, role domain.WorkspaceRole) error {
	out, err := r.directory.SynchronizeExternalUserRole(ctx, &chatv1.SynchronizeExternalUserRoleRequest{WorkspaceId: string(workspaceID), TargetUserId: string(targetID), Role: string(role)})
	if err != nil {
		return skewFailure("SynchronizeExternalUserRole", err)
	}
	if !out.GetOk() {
		return errors.New("typed external role synchronisation was not acknowledged")
	}
	return nil
}

// skewFailure names a version skew for a method that has no fallback.
//
// codes.Unimplemented on this contract means one thing: the chat process is
// older than the HTTP process and does not serve the method. Left alone it
// arrives as "rpc error: code = Unimplemented desc = unknown method ...", which
// internal/web renders as a generic failure page and which names neither the
// cause nor the remedy.
func skewFailure(method string, err error) error {
	if err == nil || status.Code(err) != codes.Unimplemented {
		return err
	}
	return fmt.Errorf("the chat service does not implement %s: it is older than this build, and a rolling deployment must update the chat process first: %w", method, err)
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

func encodeProtoSharedInvite(value domain.SharedInvite) *chatv1.SharedInvite {
	result := &chatv1.SharedInvite{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.ConversationID),
		TargetWorkspaceId: string(value.TargetWorkspaceID), TargetEmail: value.TargetEmail,
		InvitedBy: string(value.InvitedBy), Status: string(value.Status), CreatedAt: value.CreatedAt.Unix(),
	}
	if !value.ReviewedAt.IsZero() {
		result.ReviewedAt = value.ReviewedAt.Unix()
	}
	if !value.SettledAt.IsZero() {
		result.SettledAt = value.SettledAt.Unix()
	}
	if !value.ExpiresAt.IsZero() {
		result.ExpiresAt = value.ExpiresAt.Unix()
	}
	return result
}

func decodeProtoSharedInvite(value *chatv1.SharedInvite) (domain.SharedInvite, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetConversationId() == "" {
		return domain.SharedInvite{}, errors.New("typed shared invitation response is incomplete")
	}
	result := domain.SharedInvite{
		ID: domain.SharedInviteID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		ConversationID: domain.ConversationID(value.GetConversationId()), TargetWorkspaceID: domain.WorkspaceID(value.GetTargetWorkspaceId()),
		TargetEmail: value.GetTargetEmail(), InvitedBy: domain.UserID(value.GetInvitedBy()),
		Status: domain.SharedInviteStatus(value.GetStatus()), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(),
	}
	if value.GetReviewedAt() != 0 {
		result.ReviewedAt = time.Unix(value.GetReviewedAt(), 0).UTC()
	}
	if value.GetSettledAt() != 0 {
		result.SettledAt = time.Unix(value.GetSettledAt(), 0).UTC()
	}
	if value.GetExpiresAt() != 0 {
		result.ExpiresAt = time.Unix(value.GetExpiresAt(), 0).UTC()
	}
	return result, nil
}

func encodeProtoRetentionPolicy(value domain.RetentionPolicy) *chatv1.RetentionPolicy {
	return &chatv1.RetentionPolicy{MessageDays: int32(value.MessageDays), FileDays: int32(value.FileDays)}
}

func decodeProtoRetentionPolicy(value *chatv1.RetentionPolicy) (domain.RetentionPolicy, error) {
	policy := domain.RetentionPolicy{MessageDays: int(value.GetMessageDays()), FileDays: int(value.GetFileDays())}
	if !policy.Valid() {
		return domain.RetentionPolicy{}, errors.New("typed retention policy is outside the permitted range")
	}
	return policy, nil
}

func (r Remote) WorkspaceRetention(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.RetentionPolicy, error) {
	out, err := r.conversations.GetWorkspaceRetention(ctx, &chatv1.RetentionPolicyRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.RetentionPolicy{}, err
	}
	return decodeProtoRetentionPolicy(out)
}

func (r Remote) SetWorkspaceRetention(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, policy domain.RetentionPolicy) (domain.RetentionPolicy, error) {
	out, err := r.conversations.SetWorkspaceRetention(ctx, &chatv1.RetentionPolicyRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Policy: encodeProtoRetentionPolicy(policy)})
	if err != nil {
		return domain.RetentionPolicy{}, err
	}
	return decodeProtoRetentionPolicy(out)
}

func (r Remote) LastRetentionSweep(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (time.Time, error) {
	out, err := r.conversations.GetLastRetentionSweep(ctx, &chatv1.RetentionPolicyRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return time.Time{}, err
	}
	if out.GetSweptAt() == 0 {
		return time.Time{}, nil
	}
	return time.Unix(out.GetSweptAt(), 0).UTC(), nil
}

func (r Remote) ConversationRetention(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationRetention, int, error) {
	out, err := r.conversations.GetConversationRetention(ctx, &chatv1.ConversationRetentionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return domain.ConversationRetention{}, 0, err
	}
	override := domain.ConversationRetention{ConversationID: conversationID, DurationDays: int(out.GetDurationDays())}
	if out.GetUpdatedAt() != 0 {
		override.UpdatedAt = time.Unix(out.GetUpdatedAt(), 0).UTC()
	}
	return override, int(out.GetEffectiveDays()), nil
}

func (r Remote) SetConversationRetention(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, days int) error {
	_, err := r.conversations.SetConversationRetention(ctx, &chatv1.ConversationRetentionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), DurationDays: int32(days)})
	return err
}

func (r Remote) RemoveConversationRetention(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	_, err := r.conversations.RemoveConversationRetention(ctx, &chatv1.ConversationRetentionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	return err
}

func (r Remote) InviteShared(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID, email string) (domain.SharedInvite, error) {
	out, err := r.conversations.InviteShared(ctx, &chatv1.InviteSharedRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), TargetWorkspaceId: string(target), TargetEmail: email})
	if err != nil {
		return domain.SharedInvite{}, err
	}
	return decodeProtoSharedInvite(out)
}

// The four decisions share one request shape and one response, so they differ
// only in which rpc they call.
func (r Remote) sharedInviteDecision(ctx context.Context, call func(context.Context, *chatv1.SharedInviteMutationRequest, ...grpc.CallOption) (*chatv1.SharedInvite, error), workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	out, err := call(ctx, &chatv1.SharedInviteMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SharedInviteId: string(id)})
	if err != nil {
		return domain.SharedInvite{}, err
	}
	return decodeProtoSharedInvite(out)
}

func (r Remote) ApproveSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return r.sharedInviteDecision(ctx, r.conversations.ApproveSharedInvite, workspaceID, userID, id)
}

func (r Remote) DenySharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return r.sharedInviteDecision(ctx, r.conversations.DenySharedInvite, workspaceID, userID, id)
}

func (r Remote) RevokeSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return r.sharedInviteDecision(ctx, r.conversations.RevokeSharedInvite, workspaceID, userID, id)
}

func (r Remote) DeclineSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return r.sharedInviteDecision(ctx, r.conversations.DeclineSharedInvite, workspaceID, userID, id)
}

func (r Remote) AcceptSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SharedInviteID) (domain.Conversation, error) {
	out, err := r.conversations.AcceptSharedInvite(ctx, &chatv1.SharedInviteMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SharedInviteId: string(id)})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) ExternalTeams(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ExternalTeamPage, error) {
	out, err := r.conversations.ExternalTeams(ctx, &chatv1.ExternalTeamsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.ExternalTeamPage{}, err
	}
	return decodeProtoExternalTeamPage(out), nil
}

func (r Remote) DisconnectExternalTeam(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, target domain.WorkspaceID) error {
	_, err := r.conversations.DisconnectExternalTeam(ctx, &chatv1.DisconnectExternalTeamRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TeamId: string(target)})
	return err
}

func (r Remote) ListSharedInvites(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, status domain.SharedInviteStatus, request domain.PageRequest) (domain.SharedInvitePage, error) {
	out, err := r.conversations.ListSharedInvites(ctx, &chatv1.SharedInvitesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Status: string(status), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.SharedInvitePage{}, err
	}
	values := make([]domain.SharedInvite, 0, len(out.GetInvites()))
	for _, item := range out.GetInvites() {
		value, decodeErr := decodeProtoSharedInvite(item)
		if decodeErr != nil {
			return domain.SharedInvitePage{}, decodeErr
		}
		values = append(values, value)
	}
	return domain.SharedInvitePage{Invites: values, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) SetExternalInvitePermissions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID, canInvite bool) (domain.Conversation, error) {
	out, err := r.conversations.SetExternalInvitePermissions(ctx, &chatv1.ExternalInvitePermissionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), TargetWorkspaceId: string(target), CanInvite: canInvite})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) ExternalInvitePermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID) (bool, error) {
	out, err := r.conversations.ExternalInvitePermission(ctx, &chatv1.ExternalInvitePermissionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), TargetWorkspaceId: string(target)})
	if err != nil {
		return false, err
	}
	return out.GetCanInvite(), nil
}

func (r Remote) UserWorkspaces(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.WorkspaceMembershipSummary, error) {
	out, err := r.directory.UserWorkspaces(ctx, &chatv1.WorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.WorkspaceMembershipSummary, 0, len(out.GetWorkspaces()))
	for _, item := range out.GetWorkspaces() {
		summary, decodeErr := decodeProtoWorkspaceMembershipSummary(item)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, summary)
	}
	return result, nil
}

func encodeProtoWorkspaceMembershipSummary(value domain.WorkspaceMembershipSummary) *chatv1.WorkspaceMembershipSummary {
	return &chatv1.WorkspaceMembershipSummary{Workspace: encodeProtoWorkspace(value.Workspace), UserId: string(value.UserID), Role: string(value.Role)}
}

func decodeProtoWorkspaceMembershipSummary(value *chatv1.WorkspaceMembershipSummary) (domain.WorkspaceMembershipSummary, error) {
	workspace, err := decodeProtoWorkspace(value.GetWorkspace())
	if err != nil {
		return domain.WorkspaceMembershipSummary{}, err
	}
	return domain.WorkspaceMembershipSummary{Workspace: workspace, UserID: domain.UserID(value.GetUserId()), Role: domain.WorkspaceRole(value.GetRole())}, nil
}

func (r Remote) InvitationPreview(ctx context.Context, workspaceID domain.WorkspaceID, id domain.InviteRequestID) (domain.InviteRequest, error) {
	out, err := r.directory.InvitationPreview(ctx, &chatv1.InvitationPreviewRequest{WorkspaceId: string(workspaceID), InviteRequestId: string(id)})
	if err != nil {
		return domain.InviteRequest{}, err
	}
	return decodeProtoInviteRequest(out), nil
}

func (r Remote) AcceptInvitationForEmail(ctx context.Context, workspaceID domain.WorkspaceID, email, displayName string) (domain.User, error) {
	out, err := r.directory.AcceptInvitationForEmail(ctx, &chatv1.AcceptInvitationRequest{WorkspaceId: string(workspaceID), Email: email, DisplayName: displayName})
	if err != nil {
		return domain.User{}, err
	}
	return decodeProtoUser(out)
}

func (r Remote) AdminUninstallApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appIDs []domain.AppID) error {
	ids := make([]string, 0, len(appIDs))
	for _, id := range appIDs {
		ids = append(ids, string(id))
	}
	out, err := r.directory.AdminUninstallApps(ctx, &chatv1.AdminUninstallAppsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppIds: ids})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app uninstall was not acknowledged")
	}
	return nil
}

func (r Remote) AdminCancelAppRequest(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	out, err := r.directory.AdminCancelAppRequest(ctx, &chatv1.AppApprovalMutationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), RequestId: string(requestID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app request cancellation was not acknowledged")
	}
	return nil
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

func (r Remote) ScheduleUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, statusText, statusEmoji string, startsAt, endsAt time.Time) (domain.ScheduledStatus, error) {
	out, err := r.presence.ScheduleUserStatus(ctx, &chatv1.ScheduleUserStatusRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), StatusText: statusText, StatusEmoji: statusEmoji,
		StartsAt: unixOrZero(startsAt), EndsAt: unixOrZero(endsAt),
	})
	if err != nil {
		return domain.ScheduledStatus{}, err
	}
	return decodeProtoScheduledStatus(out)
}

func (r Remote) ScheduledUserStatuses(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.ScheduledStatus, error) {
	out, err := r.presence.ScheduledUserStatuses(ctx, &chatv1.ScheduledUserStatusesRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	values := make([]domain.ScheduledStatus, 0, len(out.GetStatuses()))
	for _, value := range out.GetStatuses() {
		decoded, err := decodeProtoScheduledStatus(value)
		if err != nil {
			return nil, err
		}
		values = append(values, decoded)
	}
	return values, nil
}

func (r Remote) UpdateScheduledUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID, statusText, statusEmoji string, startsAt, endsAt time.Time) (domain.ScheduledStatus, error) {
	out, err := r.presence.UpdateScheduledUserStatus(ctx, &chatv1.UpdateScheduledUserStatusRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Id: string(id), StatusText: statusText, StatusEmoji: statusEmoji,
		StartsAt: unixOrZero(startsAt), EndsAt: unixOrZero(endsAt),
	})
	if err != nil {
		return domain.ScheduledStatus{}, err
	}
	return decodeProtoScheduledStatus(out)
}

func (r Remote) DeleteScheduledUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID) error {
	out, err := r.presence.DeleteScheduledUserStatus(ctx, &chatv1.DeleteScheduledUserStatusRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Id: string(id)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "scheduled status deletion")
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

func (r Remote) ConversationMemberCount(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (int, error) {
	out, err := r.directory.ConversationMemberCount(ctx, &chatv1.ConversationMembersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID)})
	if err != nil {
		return 0, err
	}
	return int(out.GetCount()), nil
}

func (r Remote) ConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) (domain.UserPage, error) {
	in := &chatv1.ConversationMembersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(request.Limit), Cursor: string(request.Cursor)}
	out, err := r.directory.ConversationMembers(ctx, in)
	if err != nil {
		return domain.UserPage{}, err
	}
	return decodeProtoUserPage(out)
}

// IsConversationMember is derived from the existing paged member RPC. Keeping
// this as a client-side projection avoids adding a transport-only RPC for a
// fact the directory already exposes, while still giving local and distributed
// web compositions the same exact membership answer.
func (r Remote) IsConversationMember(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (bool, error) {
	request := domain.PageRequest{Limit: 200}
	for {
		page, err := r.ConversationMembers(ctx, workspaceID, userID, conversationID, request)
		if err != nil {
			return false, err
		}
		for _, member := range page.Users {
			if member.ID == userID {
				return true, nil
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			return false, nil
		}
		request.Cursor = page.NextCursor
	}
}

func (r Remote) WorkspaceInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.Workspace, error) {
	in := &chatv1.WorkspaceRequest{WorkspaceId: string(workspaceID), UserId: string(userID)}
	out, err := r.directory.WorkspaceInfo(ctx, in)
	if err != nil {
		return domain.Workspace{}, err
	}
	return decodeProtoWorkspace(out)
}

func (r Remote) AuthorizedAppWorkspaces(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, request domain.PageRequest) (domain.WorkspacePage, error) {
	out, err := r.directory.AuthorizedAppWorkspaces(ctx, &chatv1.AuthorizedAppWorkspacesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending,
	})
	if err != nil {
		return domain.WorkspacePage{}, err
	}
	return decodeProtoWorkspacePage(out)
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
	result := domain.View{ID: domain.ViewID(value.GetId()), AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Type: value.GetType(), ExternalID: value.GetExternalId(), Payload: value.GetPayload(), State: value.GetStateJson(), Hash: value.GetHash(), RootViewID: domain.ViewID(value.GetRootViewId()), PreviousViewID: domain.ViewID(value.GetPreviousViewId()), CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano())}
	if raw := strings.TrimSpace(value.GetErrorsJson()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &result.Errors); err != nil {
			return domain.View{}, err
		}
	}
	return result, nil
}

func (r Remote) OpenView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, triggerID, payload string) (domain.View, error) {
	out, err := r.views.OpenView(ctx, &chatv1.OpenViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), TriggerId: triggerID, Payload: payload})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) PublishView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, target domain.UserID, payload, hash string) (domain.View, error) {
	out, err := r.views.PublishView(ctx, &chatv1.PublishViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), TargetUserId: string(target), Payload: payload, Hash: hash})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) AppHome(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.InstalledApp, domain.View, error) {
	out, err := r.views.AppHome(ctx, &chatv1.AppHomeRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	return decodeProtoAppHome(out)
}

func (r Remote) OpenAppHome(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.InstalledApp, domain.View, error) {
	out, err := r.views.OpenAppHome(ctx, &chatv1.AppHomeRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	return decodeProtoAppHome(out)
}

func decodeProtoAppHome(out *chatv1.AppHomeResponse) (domain.InstalledApp, domain.View, error) {
	if out == nil {
		return domain.InstalledApp{}, domain.View{}, errors.New("typed app home response is nil")
	}
	app, err := decodeProtoInstalledApp(out.GetApp())
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	if !out.GetPublished() {
		return app, domain.View{}, nil
	}
	view, err := decodeProtoView(out.GetView())
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	return app, view, nil
}

func (r Remote) PushView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, triggerID, payload string) (domain.View, error) {
	out, err := r.views.PushView(ctx, &chatv1.PushViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), TriggerId: triggerID, Payload: payload})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) UpdateView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, viewID, externalID, payload, hash string) (domain.View, error) {
	out, err := r.views.UpdateView(ctx, &chatv1.UpdateViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), ViewId: viewID, ExternalId: externalID, Payload: payload, Hash: hash})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) CurrentModalView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.View, error) {
	out, err := r.views.CurrentModalView(ctx, &chatv1.CurrentModalViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.View{}, err
	}
	return decodeProtoView(out)
}

func (r Remote) SubmitView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, viewID domain.ViewID, stateJSON, responseBaseURL string) (domain.ViewInteractionResult, error) {
	out, err := r.views.SubmitView(ctx, &chatv1.ViewSubmissionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		ViewId: string(viewID), StateJson: stateJSON, ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	result := domain.ViewInteractionResult{Pending: out.GetPending()}
	if raw := strings.TrimSpace(out.GetErrorsJson()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &result.Errors); err != nil {
			return domain.ViewInteractionResult{}, err
		}
	}
	return result, nil
}

func (r Remote) CloseView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, viewID domain.ViewID, clear bool, responseBaseURL string) error {
	out, err := r.views.CloseView(ctx, &chatv1.CloseViewRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		ViewId: string(viewID), Clear: clear, ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "close view")
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

// optionalRFC3339 renders an instant a message may not have. The empty string
// means "never edited", so a zero time cannot cross the seam as a real one.
func optionalRFC3339(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func optionalUnixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixNano()
}

func optionalTimeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

func encodeWorkflowDefinition(value domain.WorkflowDefinition) *chatv1.WorkflowDefinition {
	managerIDs := make([]string, 0, len(value.ManagerIDs))
	for _, id := range value.ManagerIDs {
		managerIDs = append(managerIDs, string(id))
	}
	return &chatv1.WorkflowDefinition{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), AppId: string(value.AppID),
		OwnerId: string(value.OwnerID), CallbackId: value.CallbackID, Title: value.Title,
		Description: value.Description, Icon: value.Icon, ManagerIds: managerIDs, InputSchema: value.InputSchema, Steps: value.Steps,
		Status: string(value.Status), Version: value.Version, PublishedVersion: value.PublishedVersion,
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
	}
}

func decodeWorkflowDefinition(value *chatv1.WorkflowDefinition) domain.WorkflowDefinition {
	if value == nil {
		return domain.WorkflowDefinition{}
	}
	return domain.WorkflowDefinition{
		ID: domain.WorkflowID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		AppID: domain.AppID(value.GetAppId()), OwnerID: domain.UserID(value.GetOwnerId()),
		CallbackID: value.GetCallbackId(), Title: value.GetTitle(), Description: value.GetDescription(),
		Icon: value.GetIcon(), ManagerIDs: decodeUserIDs(value.GetManagerIds()), InputSchema: value.GetInputSchema(), Steps: value.GetSteps(), Status: domain.WorkflowStatus(value.GetStatus()),
		Version: value.GetVersion(), PublishedVersion: value.GetPublishedVersion(),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
		UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}
}

// decodeUserIDs answers with an empty list rather than nil when there are
// none. Nil and the empty list are the same thing to most Go code and a
// different answer at the seam: the local composition returns the empty list
// the store holds, so a nil here makes the two compositions disagree.
func decodeUserIDs(ids []string) []domain.UserID {
	values := make([]domain.UserID, 0, len(ids))
	for _, id := range ids {
		values = append(values, domain.UserID(id))
	}
	return values
}

func encodeWorkflowStepChanges(changes []domain.WorkflowStepChange) []*chatv1.WorkflowStepChange {
	encoded := make([]*chatv1.WorkflowStepChange, 0, len(changes))
	for _, change := range changes {
		encoded = append(encoded, &chatv1.WorkflowStepChange{
			Position: int32(change.Position), FunctionId: change.FunctionID, Change: string(change.Change),
		})
	}
	return encoded
}

func decodeWorkflowStepChanges(changes []*chatv1.WorkflowStepChange) []domain.WorkflowStepChange {
	decoded := make([]domain.WorkflowStepChange, 0, len(changes))
	for _, change := range changes {
		if change == nil {
			continue
		}
		decoded = append(decoded, domain.WorkflowStepChange{
			Position: int(change.GetPosition()), FunctionID: change.GetFunctionId(),
			Change: domain.WorkflowStepChangeType(change.GetChange()),
		})
	}
	return decoded
}

func encodeWorkflowTrigger(value domain.WorkflowTrigger) *chatv1.WorkflowTrigger {
	return &chatv1.WorkflowTrigger{
		Id: string(value.ID), WorkflowId: string(value.WorkflowID), WorkspaceId: string(value.WorkspaceID),
		AppId: string(value.AppID), Title: value.Title, Type: string(value.Type), Config: value.Config,
		Enabled: value.Enabled, Version: value.Version,
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
		NextRunAtUnixNano: optionalUnixNano(value.NextRunAt),
	}
}

func decodeWorkflowTrigger(value *chatv1.WorkflowTrigger) domain.WorkflowTrigger {
	if value == nil {
		return domain.WorkflowTrigger{}
	}
	return domain.WorkflowTrigger{
		ID: domain.WorkflowTriggerID(value.GetId()), WorkflowID: domain.WorkflowID(value.GetWorkflowId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()),
		Title: value.GetTitle(), Type: domain.WorkflowTriggerType(value.GetType()), Config: value.GetConfig(),
		Enabled: value.GetEnabled(), Version: value.GetVersion(),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
		UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
		NextRunAt: optionalTimeFromUnixNano(value.GetNextRunAtUnixNano()),
	}
}

func encodeWorkflowRun(value domain.WorkflowRun) *chatv1.WorkflowRun {
	return &chatv1.WorkflowRun{
		Id: string(value.ID), WorkflowId: string(value.WorkflowID), WorkflowVersion: value.WorkflowVersion,
		TriggerId: string(value.TriggerID), WorkspaceId: string(value.WorkspaceID), AppId: string(value.AppID),
		ActorId: string(value.ActorID), ChannelId: string(value.ConversationID), Status: string(value.Status),
		Inputs: value.Inputs, Outputs: value.Outputs, Error: value.Error, CurrentStep: int32(value.CurrentStep),
		IdempotencyKey: value.IdempotencyKey, CreatedAtUnixNano: optionalUnixNano(value.CreatedAt),
		UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt), CompletedAtUnixNano: optionalUnixNano(value.CompletedAt),
	}
}

func encodeWorkflowStep(value domain.WorkflowStep) *chatv1.WorkflowStep {
	return &chatv1.WorkflowStep{
		Id: string(value.ID), WorkflowRunId: string(value.WorkflowRunID), WorkspaceId: string(value.WorkspaceID),
		AppId: string(value.AppID), UserId: string(value.UserID), FunctionId: value.FunctionID, EditId: value.EditID,
		Status: string(value.Status), Inputs: value.Inputs, Outputs: value.Outputs, Error: value.Error,
		StepName: value.StepName, ImageUrl: value.ImageURL, ResumeAtUnixNano: optionalUnixNano(value.ResumeAt),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
	}
}

func decodeWorkflowStep(value *chatv1.WorkflowStep) domain.WorkflowStep {
	if value == nil {
		return domain.WorkflowStep{}
	}
	return domain.WorkflowStep{
		ID: domain.WorkflowStepID(value.GetId()), WorkflowRunID: domain.WorkflowRunID(value.GetWorkflowRunId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()),
		UserID: domain.UserID(value.GetUserId()), FunctionID: value.GetFunctionId(), EditID: value.GetEditId(),
		Status: domain.WorkflowStepStatus(value.GetStatus()), Inputs: value.GetInputs(), Outputs: value.GetOutputs(),
		Error: value.GetError(), StepName: value.GetStepName(), ImageURL: value.GetImageUrl(),
		ResumeAt:  optionalTimeFromUnixNano(value.GetResumeAtUnixNano()),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
		UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}
}

func decodeWorkflowRun(value *chatv1.WorkflowRun) domain.WorkflowRun {
	if value == nil {
		return domain.WorkflowRun{}
	}
	return domain.WorkflowRun{
		ID: domain.WorkflowRunID(value.GetId()), WorkflowID: domain.WorkflowID(value.GetWorkflowId()),
		WorkflowVersion: value.GetWorkflowVersion(), TriggerID: domain.WorkflowTriggerID(value.GetTriggerId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()),
		ActorID: domain.UserID(value.GetActorId()), ConversationID: domain.ConversationID(value.GetChannelId()),
		Status: domain.WorkflowRunStatus(value.GetStatus()), Inputs: value.GetInputs(), Outputs: value.GetOutputs(),
		Error: value.GetError(), CurrentStep: int(value.GetCurrentStep()), IdempotencyKey: value.GetIdempotencyKey(),
		CreatedAt:   optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
		UpdatedAt:   optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
		CompletedAt: optionalTimeFromUnixNano(value.GetCompletedAtUnixNano()),
	}
}

func (r Remote) CreateWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.WorkflowDefinition) (domain.WorkflowDefinition, error) {
	out, err := r.workflows.CreateWorkflow(ctx, &chatv1.WorkflowMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Workflow: encodeWorkflowDefinition(value),
	})
	return decodeWorkflowDefinition(out), err
}

func (r Remote) GetWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) (domain.WorkflowDefinition, error) {
	out, err := r.workflows.GetWorkflow(ctx, &chatv1.WorkflowGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	return decodeWorkflowDefinition(out), err
}

func (r Remote) DiscardWorkflowStagedChanges(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, expectedVersion uint64) error {
	out, err := r.workflows.DiscardWorkflowStagedChanges(ctx, &chatv1.WorkflowDiscardStagedRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "discard workflow staged changes")
}

func (r Remote) WorkflowStepChanges(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) ([]domain.WorkflowStepChange, error) {
	out, err := r.workflows.WorkflowStepChanges(ctx, &chatv1.WorkflowStepChangesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	if err != nil {
		return nil, err
	}
	return decodeWorkflowStepChanges(out.GetChanges()), nil
}

func (r Remote) DuplicateWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) (domain.WorkflowDefinition, error) {
	out, err := r.workflows.DuplicateWorkflow(ctx, &chatv1.WorkflowGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	return decodeWorkflowDefinition(out), err
}

func (r Remote) DeleteWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, expectedVersion uint64) error {
	out, err := r.workflows.DeleteWorkflow(ctx, &chatv1.WorkflowDeleteRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "delete workflow")
}

func (r Remote) SubmitWorkflowForm(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, runID domain.WorkflowRunID, stepID domain.WorkflowStepID, inputs string) error {
	out, err := r.workflows.SubmitWorkflowForm(ctx, &chatv1.WorkflowFormSubmitRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), RunId: string(runID),
		StepId: string(stepID), Inputs: inputs,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "submit workflow form")
}

func (r Remote) CompleteWorkflowButton(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, runID domain.WorkflowRunID, stepID domain.WorkflowStepID) error {
	out, err := r.workflows.CompleteWorkflowButton(ctx, &chatv1.WorkflowButtonRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), RunId: string(runID), StepId: string(stepID),
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "complete workflow button")
}

func (r Remote) WorkflowRunInteraction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, runID domain.WorkflowRunID) (domain.WorkflowInteraction, error) {
	out, err := r.workflows.WorkflowRunInteraction(ctx, &chatv1.WorkflowRunGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowRunId: string(runID),
	})
	if err != nil {
		return domain.WorkflowInteraction{}, err
	}
	return decodeWorkflowInteraction(out), nil
}

func decodeWorkflowInteraction(value *chatv1.WorkflowInteraction) domain.WorkflowInteraction {
	if value == nil {
		return domain.WorkflowInteraction{}
	}
	interaction := domain.WorkflowInteraction{
		StepID: domain.WorkflowStepID(value.GetStepId()), Kind: domain.WorkflowStepType(value.GetKind()),
		Title: value.GetTitle(), Label: value.GetLabel(),
	}
	for _, field := range value.GetFields() {
		interaction.Fields = append(interaction.Fields, domain.WorkflowInteractionField{
			Name: field.GetName(), Label: field.GetLabel(),
		})
	}
	return interaction
}

func (r Remote) WorkflowRunExport(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) ([]domain.WorkflowRun, error) {
	out, err := r.workflows.WorkflowRunExport(ctx, &chatv1.WorkflowGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	if err != nil {
		return nil, err
	}
	runs := make([]domain.WorkflowRun, 0, len(out.GetRuns()))
	for _, run := range out.GetRuns() {
		runs = append(runs, decodeWorkflowRun(run))
	}
	return runs, nil
}

func (r Remote) WorkflowFormResponseExport(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) ([]domain.WorkflowFormResponse, error) {
	out, err := r.workflows.WorkflowFormResponseExport(ctx, &chatv1.WorkflowGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	if err != nil {
		return nil, err
	}
	responses := make([]domain.WorkflowFormResponse, 0, len(out.GetResponses()))
	for _, response := range out.GetResponses() {
		responses = append(responses, domain.WorkflowFormResponse{
			RunID: domain.WorkflowRunID(response.GetRunId()), WorkflowVersion: response.GetWorkflowVersion(),
			FormTitle: response.GetFormTitle(), Field: response.GetField(), Value: response.GetValue(),
			SubmittedAt: optionalTimeFromUnixNano(response.GetSubmittedAtUnixNano()),
		})
	}
	return responses, nil
}

func (r Remote) SetWorkflowManagers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, managerIDs []domain.UserID) (domain.WorkflowDefinition, error) {
	ids := make([]string, 0, len(managerIDs))
	for _, id := range managerIDs {
		ids = append(ids, string(id))
	}
	out, err := r.workflows.SetWorkflowManagers(ctx, &chatv1.WorkflowManagersRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID), ManagerIds: ids,
	})
	return decodeWorkflowDefinition(out), err
}

func (r Remote) CanManageWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) (bool, error) {
	out, err := r.workflows.CanManageWorkflow(ctx, &chatv1.WorkflowGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	if err != nil {
		return false, err
	}
	return out.GetCanManage(), nil
}

func (r Remote) WorkflowActivity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) (domain.WorkflowActivity, error) {
	out, err := r.workflows.WorkflowActivity(ctx, &chatv1.WorkflowGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	if err != nil {
		return domain.WorkflowActivity{}, err
	}
	recent := make([]domain.WorkflowRun, 0, len(out.GetRecentRuns()))
	for _, run := range out.GetRecentRuns() {
		recent = append(recent, decodeWorkflowRun(run))
	}
	return domain.WorkflowActivity{
		Running: int(out.GetRunning()), Completed: int(out.GetCompleted()),
		Failed: int(out.GetFailed()), Cancelled: int(out.GetCancelled()), RecentRuns: recent,
	}, nil
}

func (r Remote) UpdateWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.WorkflowDefinition, expectedVersion uint64, publish bool) (domain.WorkflowDefinition, error) {
	out, err := r.workflows.UpdateWorkflow(ctx, &chatv1.WorkflowMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Workflow: encodeWorkflowDefinition(value),
		ExpectedVersion: expectedVersion, Publish: publish,
	})
	return decodeWorkflowDefinition(out), err
}

func (r Remote) AdminWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error) {
	out, err := r.workflows.AdminWorkflows(ctx, &chatv1.AdminWorkflowListRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit),
		Cursor: string(request.Cursor), Descending: request.Descending, Query: query,
	})
	if err != nil {
		return nil, false, "", err
	}
	values := make([]domain.WorkflowDefinition, 0, len(out.GetWorkflows()))
	for _, value := range out.GetWorkflows() {
		values = append(values, decodeWorkflowDefinition(value))
	}
	return values, out.GetHasMore(), domain.Cursor(out.GetNextCursor()), nil
}

func (r Remote) AddWorkflowCollaborators(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.WorkflowID, collaborators []domain.UserID) error {
	return r.changeWorkflowCollaborators(ctx, workspaceID, userID, ids, collaborators, true)
}

func (r Remote) RemoveWorkflowCollaborators(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.WorkflowID, collaborators []domain.UserID) error {
	return r.changeWorkflowCollaborators(ctx, workspaceID, userID, ids, collaborators, false)
}

func (r Remote) changeWorkflowCollaborators(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.WorkflowID, collaborators []domain.UserID, adding bool) error {
	workflowIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		workflowIDs = append(workflowIDs, string(id))
	}
	collaboratorIDs := make([]string, 0, len(collaborators))
	for _, id := range collaborators {
		collaboratorIDs = append(collaboratorIDs, string(id))
	}
	out, err := r.workflows.ChangeWorkflowCollaborators(ctx, &chatv1.WorkflowCollaboratorsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID),
		WorkflowIds: workflowIDs, CollaboratorIds: collaboratorIDs, Adding: adding,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed workflow collaborator change was not acknowledged")
	}
	return nil
}

func (r Remote) AdminUnpublishWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.WorkflowID) error {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, string(id))
	}
	out, err := r.workflows.AdminUnpublishWorkflows(ctx, &chatv1.AdminUnpublishWorkflowsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowIds: values,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed workflow unpublish was not acknowledged")
	}
	return nil
}

func (r Remote) ListWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error) {
	out, err := r.workflows.ListWorkflows(ctx, &chatv1.WorkflowListRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit),
		Cursor: string(request.Cursor), Descending: request.Descending,
	})
	if err != nil {
		return nil, false, "", err
	}
	values := make([]domain.WorkflowDefinition, 0, len(out.GetWorkflows()))
	for _, value := range out.GetWorkflows() {
		values = append(values, decodeWorkflowDefinition(value))
	}
	return values, out.GetHasMore(), domain.Cursor(out.GetNextCursor()), nil
}

func (r Remote) SetWorkflowTrigger(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.WorkflowTrigger, expectedVersion uint64) (domain.WorkflowTrigger, error) {
	out, err := r.workflows.SetWorkflowTrigger(ctx, &chatv1.WorkflowTriggerMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Trigger: encodeWorkflowTrigger(value), ExpectedVersion: expectedVersion,
	})
	return decodeWorkflowTrigger(out), err
}

func (r Remote) ListWorkflowTriggers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID) ([]domain.WorkflowTrigger, error) {
	out, err := r.workflows.ListWorkflowTriggers(ctx, &chatv1.WorkflowTriggerListRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
	})
	if err != nil {
		return nil, err
	}
	values := make([]domain.WorkflowTrigger, 0, len(out.GetTriggers()))
	for _, value := range out.GetTriggers() {
		values = append(values, decodeWorkflowTrigger(value))
	}
	return values, nil
}

func (r Remote) RunWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID domain.WorkflowTriggerID, conversationID domain.ConversationID, inputs, idempotencyKey string) (domain.WorkflowRun, error) {
	out, err := r.workflows.RunWorkflow(ctx, &chatv1.WorkflowRunRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: string(triggerID),
		ChannelId: string(conversationID), Inputs: inputs, IdempotencyKey: idempotencyKey,
	})
	return decodeWorkflowRun(out), err
}

func (r Remote) RunAutomaticWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, triggerID domain.WorkflowTriggerID, conversationID domain.ConversationID, inputs, idempotencyKey string) (domain.WorkflowRun, error) {
	out, err := r.workflows.RunAutomaticWorkflow(ctx, &chatv1.AutomaticWorkflowRunRequest{
		WorkspaceId: string(workspaceID), TriggerId: string(triggerID),
		ChannelId: string(conversationID), Inputs: inputs, IdempotencyKey: idempotencyKey,
	})
	return decodeWorkflowRun(out), err
}

func (r Remote) RunWebhookTrigger(ctx context.Context, workspaceID domain.WorkspaceID, triggerID domain.WorkflowTriggerID, secret, inputs string) (domain.WorkflowRun, error) {
	out, err := r.workflows.RunWebhookTrigger(ctx, &chatv1.WebhookWorkflowRunRequest{
		WorkspaceId: string(workspaceID), TriggerId: string(triggerID), Secret: secret, Inputs: inputs,
	})
	return decodeWorkflowRun(out), err
}

func (r Remote) WebhookTriggerURL(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID domain.WorkflowTriggerID) (string, error) {
	out, err := r.workflows.WebhookTriggerURL(ctx, &chatv1.WebhookTriggerURLRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), TriggerId: string(triggerID),
	})
	if err != nil {
		return "", err
	}
	return out.GetUrl(), nil
}

func (r Remote) DispatchWorkflowEventTriggers(ctx context.Context, workspaceID domain.WorkspaceID, limit int) (int, error) {
	out, err := r.workflows.DispatchWorkflowEventTriggers(ctx, &chatv1.WorkflowEventDispatchRequest{
		WorkspaceId: string(workspaceID), Limit: int32(limit),
	})
	if err != nil {
		return 0, err
	}
	return int(out.GetStarted()), nil
}

func (r Remote) GetWorkflowRun(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, runID domain.WorkflowRunID) (domain.WorkflowRun, error) {
	out, err := r.workflows.GetWorkflowRun(ctx, &chatv1.WorkflowRunGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowRunId: string(runID),
	})
	return decodeWorkflowRun(out), err
}

func (r Remote) WorkflowRunSteps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, runID domain.WorkflowRunID) ([]domain.WorkflowStep, error) {
	out, err := r.workflows.WorkflowRunSteps(ctx, &chatv1.WorkflowRunGetRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowRunId: string(runID),
	})
	if err != nil {
		return nil, err
	}
	steps := make([]domain.WorkflowStep, 0, len(out.GetSteps()))
	for _, encoded := range out.GetSteps() {
		steps = append(steps, decodeWorkflowStep(encoded))
	}
	return steps, nil
}

func (r Remote) CompleteFunction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, executionID domain.WorkflowStepID, outputs, failure string) error {
	out, err := r.workflows.CompleteFunction(ctx, &chatv1.FunctionCompletionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		FunctionExecutionId: string(executionID), Outputs: outputs, Error: failure,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "complete function")
}

func encodeAutomationPermission(value domain.AutomationPermission) *chatv1.AutomationPermission {
	return &chatv1.AutomationPermission{
		ResourceType: value.ResourceType, ResourceId: value.ResourceID, WorkspaceId: string(value.WorkspaceID),
		AppId: string(value.AppID), PermissionType: string(value.PermissionType),
		UserIds: userStrings(value.UserIDs), ChannelIds: conversationStrings(value.ChannelIDs),
		TeamIds: workspaceStrings(value.TeamIDs), OrgIds: append([]string(nil), value.OrgIDs...),
		UpdatedAtUnixNano: value.UpdatedAt.UnixNano(),
	}
}

func workspaceStrings(values []domain.WorkspaceID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func decodeAutomationPermission(value *chatv1.AutomationPermission) domain.AutomationPermission {
	if value == nil {
		return domain.AutomationPermission{}
	}
	result := domain.AutomationPermission{
		ResourceType: value.GetResourceType(), ResourceID: value.GetResourceId(),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), AppID: domain.AppID(value.GetAppId()),
		PermissionType: domain.PermissionType(value.GetPermissionType()), OrgIDs: append([]string(nil), value.GetOrgIds()...),
	}
	for _, id := range value.GetUserIds() {
		result.UserIDs = append(result.UserIDs, domain.UserID(id))
	}
	for _, id := range value.GetChannelIds() {
		result.ChannelIDs = append(result.ChannelIDs, domain.ConversationID(id))
	}
	for _, id := range value.GetTeamIds() {
		result.TeamIDs = append(result.TeamIDs, domain.WorkspaceID(id))
	}
	if value.GetUpdatedAtUnixNano() != 0 {
		result.UpdatedAt = time.Unix(0, value.GetUpdatedAtUnixNano()).UTC()
	}
	return result
}

func functionPermissionRequest(workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, functionID, callbackID string, permission domain.AutomationPermission) *chatv1.FunctionPermissionRequest {
	return &chatv1.FunctionPermissionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		FunctionId: functionID, FunctionCallbackId: callbackID, Permission: encodeAutomationPermission(permission),
	}
}

func (r Remote) WorkflowStepResponses(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, stepID string) ([]domain.WorkflowStepResponse, error) {
	out, err := r.workflows.WorkflowStepResponses(ctx, &chatv1.WorkflowStepResponsesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID), StepId: stepID,
	})
	if err != nil {
		return nil, err
	}
	responses := make([]domain.WorkflowStepResponse, 0, len(out.GetResponses()))
	for _, encoded := range out.GetResponses() {
		responses = append(responses, decodeProtoWorkflowStepResponse(encoded))
	}
	return responses, nil
}

func encodeProtoWorkflowStepResponse(value domain.WorkflowStepResponse) *chatv1.WorkflowStepResponse {
	return &chatv1.WorkflowStepResponse{
		RunId: string(value.RunID), StepId: string(value.StepID), ActorId: string(value.ActorID),
		Status: string(value.Status), Outputs: value.Outputs, CompletedAt: optionalUnixNano(value.CompletedAt),
	}
}

func decodeProtoWorkflowStepResponse(value *chatv1.WorkflowStepResponse) domain.WorkflowStepResponse {
	return domain.WorkflowStepResponse{
		RunID: domain.WorkflowRunID(value.GetRunId()), StepID: domain.WorkflowStepID(value.GetStepId()),
		ActorID: domain.UserID(value.GetActorId()), Status: domain.WorkflowStepStatus(value.GetStatus()),
		Outputs: value.GetOutputs(), CompletedAt: optionalTimeFromUnixNano(value.GetCompletedAt()),
	}
}

func (r Remote) AdminFunctionPermissions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, functionIDs []string) ([]domain.AutomationPermission, error) {
	out, err := r.workflows.AdminFunctionPermissions(ctx, adminPermissionsRequest(workspaceID, userID, functionIDs))
	if err != nil {
		return nil, err
	}
	return decodeAutomationPermissions(out.GetPermissions()), nil
}

func (r Remote) AdminWorkflowPermissions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowIDs []domain.WorkflowID) ([]domain.AutomationPermission, error) {
	ids := make([]string, 0, len(workflowIDs))
	for _, id := range workflowIDs {
		ids = append(ids, string(id))
	}
	out, err := r.workflows.AdminWorkflowPermissions(ctx, adminPermissionsRequest(workspaceID, userID, ids))
	if err != nil {
		return nil, err
	}
	return decodeAutomationPermissions(out.GetPermissions()), nil
}

func (r Remote) AdminTriggerTypePermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, kind domain.WorkflowTriggerType) (domain.AutomationPermission, error) {
	out, err := r.workflows.AdminTriggerTypePermission(ctx, adminPermissionsRequest(workspaceID, userID, []string{string(kind)}))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func (r Remote) AdminSetFunctionPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, functionID string, permission domain.AutomationPermission) (domain.AutomationPermission, error) {
	out, err := r.workflows.AdminSetFunctionPermission(ctx, adminPermissionMutation(workspaceID, userID, functionID, permission))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func (r Remote) AdminSetTriggerTypePermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, kind domain.WorkflowTriggerType, permission domain.AutomationPermission) (domain.AutomationPermission, error) {
	out, err := r.workflows.AdminSetTriggerTypePermission(ctx, adminPermissionMutation(workspaceID, userID, string(kind), permission))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func adminPermissionsRequest(workspaceID domain.WorkspaceID, userID domain.UserID, ids []string) *chatv1.AdminPermissionsRequest {
	values := make([]string, 0, len(ids))
	values = append(values, ids...)
	return &chatv1.AdminPermissionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ResourceIds: values}
}

func adminPermissionMutation(workspaceID domain.WorkspaceID, userID domain.UserID, id string, permission domain.AutomationPermission) *chatv1.AdminPermissionMutationRequest {
	return &chatv1.AdminPermissionMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ResourceId: id,
		Permission: encodeAutomationPermission(permission),
	}
}

func decodeAutomationPermissions(values []*chatv1.AutomationPermission) []domain.AutomationPermission {
	permissions := make([]domain.AutomationPermission, 0, len(values))
	for _, value := range values {
		permissions = append(permissions, decodeAutomationPermission(value))
	}
	return permissions
}

func (r Remote) GetFunctionPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, functionID, callbackID string) (domain.AutomationPermission, error) {
	out, err := r.workflows.GetFunctionPermission(ctx, functionPermissionRequest(workspaceID, userID, appID, functionID, callbackID, domain.AutomationPermission{}))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func (r Remote) SetFunctionPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, functionID, callbackID string, permission domain.AutomationPermission) (domain.AutomationPermission, error) {
	out, err := r.workflows.SetFunctionPermission(ctx, functionPermissionRequest(workspaceID, userID, appID, functionID, callbackID, permission))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func triggerPermissionRequest(workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, triggerID domain.WorkflowTriggerID, permission domain.AutomationPermission) *chatv1.TriggerPermissionRequest {
	return &chatv1.TriggerPermissionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		TriggerId: string(triggerID), Permission: encodeAutomationPermission(permission),
	}
}

func (r Remote) GetTriggerPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, triggerID domain.WorkflowTriggerID) (domain.AutomationPermission, error) {
	out, err := r.workflows.GetTriggerPermission(ctx, triggerPermissionRequest(workspaceID, userID, appID, triggerID, domain.AutomationPermission{}))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func (r Remote) SetTriggerPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, triggerID domain.WorkflowTriggerID, permission domain.AutomationPermission) (domain.AutomationPermission, error) {
	out, err := r.workflows.SetTriggerPermission(ctx, triggerPermissionRequest(workspaceID, userID, appID, triggerID, permission))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func workflowPermissionRequest(workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, scope string, permission domain.AutomationPermission) *chatv1.WorkflowPermissionRequest {
	return &chatv1.WorkflowPermissionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), WorkflowId: string(workflowID),
		Scope: scope, Permission: encodeAutomationPermission(permission),
	}
}

func (r Remote) GetWorkflowPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, scope string) (domain.AutomationPermission, error) {
	out, err := r.workflows.GetWorkflowPermission(ctx, workflowPermissionRequest(workspaceID, userID, workflowID, scope, domain.AutomationPermission{}))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func (r Remote) SetWorkflowPermission(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, workflowID domain.WorkflowID, scope string, permission domain.AutomationPermission) (domain.AutomationPermission, error) {
	out, err := r.workflows.SetWorkflowPermission(ctx, workflowPermissionRequest(workspaceID, userID, workflowID, scope, permission))
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return decodeAutomationPermission(out), nil
}

func (r Remote) SetFeaturedWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, triggerIDs []domain.WorkflowTriggerID) error {
	ids := make([]string, len(triggerIDs))
	for index, id := range triggerIDs {
		ids[index] = string(id)
	}
	out, err := r.workflows.SetFeaturedWorkflows(ctx, &chatv1.FeaturedWorkflowsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(conversationID), TriggerIds: ids,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "set featured workflows")
}

func (r Remote) ListFeaturedWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationIDs []domain.ConversationID) ([]domain.FeaturedWorkflow, error) {
	ids := make([]string, len(conversationIDs))
	for index, id := range conversationIDs {
		ids[index] = string(id)
	}
	out, err := r.workflows.ListFeaturedWorkflows(ctx, &chatv1.FeaturedWorkflowsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ChannelIds: ids,
	})
	if err != nil {
		return nil, err
	}
	values := make([]domain.FeaturedWorkflow, 0, len(out.GetWorkflows()))
	for _, value := range out.GetWorkflows() {
		values = append(values, domain.FeaturedWorkflow{
			WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ConversationID: domain.ConversationID(value.GetChannelId()),
			TriggerID: domain.WorkflowTriggerID(value.GetTriggerId()), Title: value.GetTitle(), Position: int(value.GetPosition()),
		})
	}
	return values, nil
}

func (r Remote) ListFunctionWorkflowSteps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, functionID string, workflowID domain.WorkflowID, workflowReference string, workflowAppID domain.AppID) ([]domain.WorkflowStepVersion, error) {
	out, err := r.workflows.ListFunctionWorkflowSteps(ctx, &chatv1.FunctionWorkflowStepsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), FunctionId: functionID,
		WorkflowId: string(workflowID), Workflow: workflowReference, WorkflowAppId: string(workflowAppID),
	})
	if err != nil {
		return nil, err
	}
	values := make([]domain.WorkflowStepVersion, 0, len(out.GetStepsVersions()))
	for _, value := range out.GetStepsVersions() {
		values = append(values, domain.WorkflowStepVersion{
			Title: value.GetTitle(), WorkflowID: domain.WorkflowID(value.GetWorkflowId()), StepID: value.GetStepId(),
			IsDeleted: value.GetIsDeleted(), WorkflowVersionCreated: value.GetWorkflowVersionCreated(),
		})
	}
	return values, nil
}

func (r Remote) OpenDialog(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, triggerID, payload string) error {
	out, err := r.dialogs.OpenDialog(ctx, &chatv1.OpenDialogRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), TriggerId: triggerID, Payload: payload})
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
	return decodeOAuthToken(out), nil
}

func (r Remote) OAuthV2Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI string, userOnly bool) (domain.OAuthToken, error) {
	out, err := r.oauth.ExchangeOAuthV2(ctx, &chatv1.OAuthExchangeRequest{ClientId: clientID, ClientSecret: clientSecret, Code: code, RedirectUri: redirectURI, UserOnly: userOnly})
	if err != nil {
		return domain.OAuthToken{}, err
	}
	return decodeOAuthToken(out), nil
}

func (r Remote) OAuthV2Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (domain.OAuthToken, error) {
	out, err := r.oauth.RefreshOAuthV2(ctx, &chatv1.OAuthExchangeRequest{ClientId: clientID, ClientSecret: clientSecret, RefreshToken: refreshToken})
	if err != nil {
		return domain.OAuthToken{}, err
	}
	return decodeOAuthToken(out), nil
}

func (r Remote) OAuthV2ExchangeToken(ctx context.Context, clientID, clientSecret, token string) (domain.OAuthToken, error) {
	out, err := r.oauth.ExchangeOAuthV2Token(ctx, &chatv1.OAuthExchangeRequest{ClientId: clientID, ClientSecret: clientSecret, Token: token})
	if err != nil {
		return domain.OAuthToken{}, err
	}
	return decodeOAuthToken(out), nil
}

func decodeOAuthToken(value *chatv1.OAuthToken) domain.OAuthToken {
	token := domain.OAuthToken{
		AccessToken:            value.GetAccessToken(),
		ClientID:               value.GetClientId(),
		AppID:                  domain.AppID(value.GetAppId()),
		WorkspaceID:            domain.WorkspaceID(value.GetWorkspaceId()),
		UserID:                 domain.UserID(value.GetUserId()),
		InstallerID:            domain.UserID(value.GetInstallerId()),
		BotID:                  domain.BotID(value.GetBotId()),
		Scopes:                 append([]string(nil), value.GetScopes()...),
		TokenType:              domain.TokenType(value.GetTokenType()),
		AuthedUserAccessToken:  value.GetAuthedUserAccessToken(),
		AuthedUserScopes:       append([]string(nil), value.GetAuthedUserScopes()...),
		RefreshToken:           value.GetRefreshToken(),
		AuthedUserRefreshToken: value.GetAuthedUserRefreshToken(),
	}
	if value.GetExpiresAtUnixNano() != 0 {
		token.ExpiresAt = time.Unix(0, value.GetExpiresAtUnixNano()).UTC()
	}
	if value.GetAuthedUserExpiresAtUnixNano() != 0 {
		token.AuthedUserExpiresAt = time.Unix(0, value.GetAuthedUserExpiresAtUnixNano()).UTC()
	}
	return token
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
	in := &chatv1.ConversationsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Types: conversationTypeStrings(request.Types), ExcludeArchived: request.ExcludeArchived, MemberUserId: string(request.MemberUserID), IncludeClosedDirects: request.IncludeClosedDirects}
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

func (r Remote) AddPeopleToDirectConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID, history domain.DirectHistorySelection) (domain.Conversation, error) {
	in := &chatv1.AddPeopleToDirectConversationRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Users: stringIDs(users), History: string(history)}
	out, err := r.mutations.AddPeopleToDirectConversation(ctx, in)
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (r Remote) ConvertGroupDirectToPrivate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	in := &chatv1.ConvertGroupDirectToPrivateRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Name: name}
	out, err := r.mutations.ConvertGroupDirectToPrivate(ctx, in)
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

func (r Remote) SetAssistantThreadTitle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string) error {
	return r.setAssistantThread(ctx, &chatv1.SetAssistantThreadRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), Conversation: string(conversation),
		ThreadTs: string(thread), Field: string(domain.AssistantThreadTitle), Title: title,
	})
}

func (r Remote) SetAssistantThreadStatus(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, status string) error {
	return r.setAssistantThread(ctx, &chatv1.SetAssistantThreadRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), Conversation: string(conversation),
		ThreadTs: string(thread), Field: string(domain.AssistantThreadStatus), Status: status,
	})
}

func (r Remote) SetAssistantThreadSuggestedPrompts(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string, prompts []domain.AssistantPrompt) error {
	return r.setAssistantThread(ctx, &chatv1.SetAssistantThreadRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), Conversation: string(conversation),
		ThreadTs: string(thread), Field: string(domain.AssistantThreadPrompts), PromptsTitle: title,
		Prompts: encodeProtoAssistantPrompts(prompts),
	})
}

func (r Remote) setAssistantThread(ctx context.Context, in *chatv1.SetAssistantThreadRequest) error {
	_, err := r.interactions.SetAssistantThread(ctx, in)
	return err
}

func (r Remote) AssistantThread(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AssistantThread, error) {
	out, err := r.interactions.GetAssistantThread(ctx, &chatv1.AssistantThreadRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), Conversation: string(conversation), ThreadTs: string(thread),
	})
	if err != nil {
		return domain.AssistantThread{}, err
	}
	return decodeProtoAssistantThread(out)
}

func (r Remote) SetTyping(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversation domain.ConversationID) error {
	_, err := r.interactions.SetTyping(ctx, &chatv1.SetTypingRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), Conversation: string(conversation),
	})
	return err
}

func (r Remote) TypingSignals(ctx context.Context, workspaceID domain.WorkspaceID, reader domain.UserID) ([]domain.TypingSignal, error) {
	out, err := r.interactions.TypingSignals(ctx, &chatv1.TypingSignalsRequest{WorkspaceId: string(workspaceID), UserId: string(reader)})
	if err != nil {
		return nil, err
	}
	return decodeProtoTypingSignals(out.GetSignals()), nil
}

// TypingIn narrows the remote read with the same domain filter the local
// service uses, rather than adding a per-conversation RPC. The set is bounded
// by how many people are typing at this instant, so the saving a narrower call
// would buy is a few strings, and the cost would be a second query shape whose
// visibility rule could drift from the first one's.
func (r Remote) TypingIn(ctx context.Context, workspaceID domain.WorkspaceID, reader domain.UserID, conversation domain.ConversationID) ([]domain.TypingSignal, error) {
	signals, err := r.TypingSignals(ctx, workspaceID, reader)
	if err != nil {
		return nil, err
	}
	return domain.TypingSignalsIn(signals, conversation), nil
}

func (r Remote) ResumeWorkflowDelays(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time, limit int) (int, error) {
	out, err := r.interactions.ResumeWorkflowDelays(ctx, &chatv1.ResumeWorkflowDelaysRequest{
		WorkspaceId: string(workspaceID), NowUnixNano: unixNanoOrZero(now), Limit: int32(limit),
	})
	if err != nil {
		return 0, err
	}
	return int(out.GetResumed()), nil
}

func (r Remote) RecordActivity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	_, err := r.interactions.RecordActivity(ctx, &chatv1.RecordActivityRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	return err
}

func (r Remote) FollowedThreads(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.FollowedThreadPage, error) {
	out, err := r.interactions.ListFollowedThreads(ctx, &chatv1.FollowedThreadsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor)})
	if err != nil {
		return domain.FollowedThreadPage{}, err
	}
	return decodeProtoFollowedThreadPage(out)
}

func (r Remote) MarkAllRead(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (int, error) {
	out, err := r.interactions.MarkAllRead(ctx, &chatv1.MarkAllReadRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return 0, err
	}
	return int(out.GetConversations()), nil
}

func (r Remote) ReadCursor(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ReadCursor, error) {
	out, err := r.interactions.GetReadCursor(ctx, &chatv1.ReadCursorRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
	})
	if err != nil {
		return domain.ReadCursor{}, err
	}
	return decodeProtoReadCursor(out)
}

func (r Remote) MessageAt(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.Message, error) {
	out, err := r.interactions.MessageAt(ctx, &chatv1.MessageAtRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp),
	})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) ThreadSummaries(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, roots []domain.MessageTimestamp) (map[domain.MessageTimestamp]domain.ThreadSummary, error) {
	timestamps := make([]string, 0, len(roots))
	for _, root := range roots {
		timestamps = append(timestamps, string(root))
	}
	out, err := r.interactions.ThreadSummaries(ctx, &chatv1.ThreadSummariesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		ThreadTimestamps: timestamps,
	})
	if err != nil {
		return nil, err
	}
	summaries := make(map[domain.MessageTimestamp]domain.ThreadSummary, len(out.GetSummaries()))
	for root, encoded := range out.GetSummaries() {
		summary, err := decodeProtoThreadSummary(encoded)
		if err != nil {
			return nil, err
		}
		summaries[domain.MessageTimestamp(root)] = summary
	}
	return summaries, nil
}

func (r Remote) DispatchSlashCommand(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, threadTimestamp domain.MessageTimestamp, command, text, responseBaseURL string) error {
	out, err := r.interactions.DispatchSlashCommand(ctx, &chatv1.SlashCommandRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		ThreadTimestamp: string(threadTimestamp), Command: command, Text: text, ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "slash command dispatch")
}

func (r Remote) DispatchBlockAction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, action domain.AppBlockAction, responseBaseURL string) error {
	out, err := r.interactions.DispatchBlockAction(ctx, &chatv1.BlockActionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), MessageId: string(action.MessageID),
		BlockId: action.BlockID, ActionId: action.ActionID, ActionType: action.Type, Value: action.Value,
		ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "block action dispatch")
}

func (r Remote) ListAppShortcuts(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, shortcutType string) ([]domain.AppShortcut, error) {
	out, err := r.interactions.ListAppShortcuts(ctx, &chatv1.AppShortcutListRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Type: shortcutType,
	})
	if err != nil {
		return nil, err
	}
	values := make([]domain.AppShortcut, 0, len(out.GetShortcuts()))
	for _, value := range out.GetShortcuts() {
		if value == nil {
			return nil, errors.New("typed application shortcut is missing")
		}
		values = append(values, domain.AppShortcut{
			AppID: domain.AppID(value.GetAppId()), AppName: value.GetAppName(), Name: value.GetName(),
			CallbackID: value.GetCallbackId(), Description: value.GetDescription(), Type: value.GetType(),
			Command: value.GetCommand(), UsageHint: value.GetUsageHint(), ShouldEscape: value.GetShouldEscape(),
		})
	}
	return values, nil
}

func (r Remote) DispatchAppShortcut(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, appID domain.AppID, callbackID string, messageID domain.MessageID, responseBaseURL string) error {
	out, err := r.interactions.DispatchAppShortcut(ctx, &chatv1.AppShortcutDispatchRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		AppId: string(appID), CallbackId: callbackID, MessageId: string(messageID), ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "application shortcut dispatch")
}

func (r Remote) HandleAppResponse(ctx context.Context, token, payload string) error {
	out, err := r.interactions.HandleAppResponse(ctx, &chatv1.AppResponseRequest{Token: token, Payload: payload})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "application response")
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

func (r Remote) DispatchViewBlockAction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, action domain.AppViewBlockAction, responseBaseURL string) error {
	out, err := r.interactions.DispatchViewBlockAction(ctx, &chatv1.ViewBlockActionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID),
		ConversationId: string(conversationID), ViewId: string(action.ViewID),
		BlockId: action.BlockID, ActionId: action.ActionID, ActionType: action.Type,
		Value: action.Value, StateJson: action.State, ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "view block action dispatch")
}

func (r Remote) LoadAppOptions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, query domain.AppOptionQuery, responseBaseURL string) ([]domain.AppOption, error) {
	out, err := r.interactions.LoadAppOptions(ctx, &chatv1.AppOptionQueryRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		AppId: string(query.AppID), MessageId: string(query.MessageID), ViewId: string(query.ViewID),
		BlockId: query.BlockID, ActionId: query.ActionID, Value: query.Value, ResponseBaseUrl: responseBaseURL,
	})
	if err != nil {
		return nil, err
	}
	options := make([]domain.AppOption, 0, len(out.GetOptions()))
	for _, option := range out.GetOptions() {
		if option == nil || option.GetText() == "" || option.GetValue() == "" {
			return nil, errors.New("typed application option is incomplete")
		}
		options = append(options, domain.AppOption{
			Text: option.GetText(), Value: option.GetValue(), Description: option.GetDescription(), Group: option.GetGroup(),
		})
	}
	return options, nil
}

func (r Remote) ClaimSocketModeInteraction(ctx context.Context, appID domain.AppID, owner string, lease time.Duration) (domain.SocketModeInteraction, bool, error) {
	out, err := r.interactions.ClaimSocketModeInteraction(ctx, &chatv1.SocketModeInteractionClaimRequest{AppId: string(appID), Owner: owner, LeaseNanos: int64(lease)})
	if err != nil {
		return domain.SocketModeInteraction{}, false, err
	}
	if !out.GetFound() {
		return domain.SocketModeInteraction{}, false, nil
	}
	value, err := decodeProtoSocketModeInteraction(out)
	return value, err == nil, err
}

func (r Remote) AckSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID, owner string) error {
	out, err := r.interactions.AckSocketModeInteraction(ctx, &chatv1.SocketModeInteractionAckRequest{AppId: string(appID), EnvelopeId: envelopeID, Owner: owner})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed Socket Mode interaction acknowledgement was not acknowledged")
	}
	return nil
}

func (r Remote) ReleaseSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID, owner, reason string, retryAt time.Time) error {
	out, err := r.interactions.ReleaseSocketModeInteraction(ctx, &chatv1.SocketModeInteractionReleaseRequest{AppId: string(appID), EnvelopeId: envelopeID, Owner: owner, Reason: reason, RetryAt: string(domain.NewStoredTime(retryAt))})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed Socket Mode interaction release was not acknowledged")
	}
	return nil
}

func (r Remote) HandleSocketModeResponse(ctx context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	out, err := r.interactions.HandleSocketModeResponse(ctx, &chatv1.SocketModeInteractionResponseRequest{AppId: string(appID), EnvelopeId: envelopeID, Payload: append([]byte(nil), payload...)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed Socket Mode response was not acknowledged")
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

func (r Remote) SaveForLater(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.SavedItem, error) {
	out, err := r.savedItems.SaveForLater(ctx, &chatv1.SaveForLaterRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)})
	if err != nil {
		return domain.SavedItem{}, err
	}
	return decodeProtoSavedItem(out)
}

func (r Remote) SavedItemForMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, messageID domain.MessageID) (domain.SavedItem, error) {
	out, err := r.savedItems.SavedItemForMessage(ctx, &chatv1.SavedItemForMessageRequest{WorkspaceId: string(workspaceID), UserId: string(userID), MessageId: string(messageID)})
	if err != nil {
		return domain.SavedItem{}, err
	}
	return decodeProtoSavedItem(out)
}

func (r Remote) SavedItemsForMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, messageIDs []domain.MessageID) ([]domain.SavedItem, error) {
	ids := make([]string, 0, len(messageIDs))
	for _, id := range messageIDs {
		ids = append(ids, string(id))
	}
	out, err := r.savedItems.SavedItemsForMessages(ctx, &chatv1.SavedItemsForMessagesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), MessageIds: ids})
	if err != nil {
		return nil, err
	}
	items := make([]domain.SavedItem, 0, len(out.GetItems()))
	for _, item := range out.GetItems() {
		decoded, err := decodeProtoSavedItem(item)
		if err != nil {
			return nil, err
		}
		items = append(items, decoded)
	}
	return items, nil
}

func (r Remote) SavedItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, state domain.SavedItemState, request domain.PageRequest) (domain.SavedItemPage, error) {
	out, err := r.savedItems.SavedItems(ctx, &chatv1.SavedItemsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), State: string(state), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending})
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	return decodeProtoSavedItemPage(out)
}

func (r Remote) SetSavedItemState(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID, state domain.SavedItemState) (domain.SavedItem, error) {
	out, err := r.savedItems.SetSavedItemState(ctx, &chatv1.SetSavedItemStateRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SavedItemId: string(id), State: string(state)})
	if err != nil {
		return domain.SavedItem{}, err
	}
	return decodeProtoSavedItem(out)
}

func (r Remote) RemoveSavedItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID) error {
	out, err := r.savedItems.RemoveSavedItem(ctx, &chatv1.RemoveSavedItemRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SavedItemId: string(id)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "saved item removal")
}

func (r Remote) AddBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, title string, bookmarkType domain.BookmarkType, link, emoji, entityID, accessLevel, parentID string) (domain.Bookmark, error) {
	out, err := r.bookmarks.AddBookmark(ctx, &chatv1.AddBookmarkRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Title: title, Type: string(bookmarkType), Link: link, Emoji: emoji, EntityId: entityID, AccessLevel: accessLevel, ParentId: parentID})
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

func (r Remote) CreateLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.LaterReminderRequest) (domain.LaterReminder, error) {
	out, err := r.reminders.CreateLaterReminder(ctx, &chatv1.CreateLaterReminderRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Target: string(request.Target),
		ChannelId: string(request.Channel), SourceChannelId: string(request.SourceChannel),
		SourceTimestamp: string(request.SourceTimestamp), Text: request.Text, DueAt: request.DueAt.Unix(),
		Timezone: request.TimeZone, Recurrence: string(request.Recurrence),
	})
	if err != nil {
		return domain.LaterReminder{}, err
	}
	return decodeProtoLaterReminder(out)
}

func (r Remote) LaterReminderInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.LaterReminderID) (domain.LaterReminder, error) {
	out, err := r.reminders.LaterReminderInfo(ctx, &chatv1.LaterReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID)})
	if err != nil {
		return domain.LaterReminder{}, err
	}
	return decodeProtoLaterReminder(out)
}

func (r Remote) LaterReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, target domain.LaterReminderTarget, request domain.PageRequest) (domain.LaterReminderPage, error) {
	out, err := r.reminders.LaterReminders(ctx, &chatv1.LaterRemindersRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Target: string(target),
		Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending,
	})
	if err != nil {
		return domain.LaterReminderPage{}, err
	}
	items := make([]domain.LaterReminder, 0, len(out.GetReminders()))
	for _, value := range out.GetReminders() {
		reminder, decodeErr := decodeProtoLaterReminder(value)
		if decodeErr != nil {
			return domain.LaterReminderPage{}, decodeErr
		}
		items = append(items, reminder)
	}
	return domain.LaterReminderPage{Items: items, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) UpdateLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.LaterReminderID, request domain.LaterReminderRequest) (domain.LaterReminder, error) {
	out, err := r.reminders.UpdateLaterReminder(ctx, &chatv1.UpdateLaterReminderRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID),
		Target: string(request.Target), ChannelId: string(request.Channel), Text: request.Text,
		DueAt: request.DueAt.Unix(), Timezone: request.TimeZone, Recurrence: string(request.Recurrence),
	})
	if err != nil {
		return domain.LaterReminder{}, err
	}
	return decodeProtoLaterReminder(out)
}

func (r Remote) AcknowledgeLaterReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	out, err := r.reminders.AcknowledgeLaterReminders(ctx, &chatv1.AcknowledgeLaterRemindersRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "Later reminder acknowledgement")
}

func (r Remote) Activity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query domain.ActivityQuery) (domain.ActivityPage, error) {
	kinds := make([]string, 0, len(query.Kinds))
	for _, kind := range query.Kinds {
		kinds = append(kinds, string(kind))
	}
	out, err := r.activity.ListActivity(ctx, &chatv1.ActivityRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Kinds: kinds,
		UnreadOnly: query.UnreadOnly, ClearedOnly: query.ClearedOnly,
		Limit: int32(query.Page.Limit), Cursor: string(query.Page.Cursor),
	})
	if err != nil {
		return domain.ActivityPage{}, err
	}
	items := make([]domain.ActivityItem, 0, len(out.GetItems()))
	for _, encoded := range out.GetItems() {
		item, err := decodeProtoActivityItem(encoded)
		if err != nil {
			return domain.ActivityPage{}, err
		}
		items = append(items, item)
	}
	return domain.ActivityPage{Items: items, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) MutateActivity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ActivityID, mutation domain.ActivityMutation) error {
	encodedIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		encodedIDs = append(encodedIDs, string(id))
	}
	out, err := r.activity.MutateActivity(ctx, &chatv1.MutateActivityRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ActivityIds: encodedIDs, Mutation: string(mutation)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "activity mutation")
}

func (r Remote) ActivityPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.ActivityPreferences, error) {
	out, err := r.activity.GetActivityPreferences(ctx, &chatv1.ActivityPreferencesRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.ActivityPreferences{}, err
	}
	return decodeProtoActivityPreferences(out), nil
}

func (r Remote) SetActivityPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, layout domain.ActivityLayout) (domain.ActivityPreferences, error) {
	out, err := r.activity.SetActivityPreferences(ctx, &chatv1.SetActivityPreferencesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Layout: string(layout)})
	if err != nil {
		return domain.ActivityPreferences{}, err
	}
	return decodeProtoActivityPreferences(out), nil
}

func (r Remote) CreateActivitySavedView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string, kinds []domain.ActivityKind) (domain.ActivitySavedView, error) {
	encoded := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		encoded = append(encoded, string(kind))
	}
	out, err := r.activity.CreateActivitySavedView(ctx, &chatv1.CreateActivitySavedViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name, Kinds: encoded})
	if err != nil {
		return domain.ActivitySavedView{}, err
	}
	return decodeProtoActivitySavedView(out), nil
}

func (r Remote) DeleteActivitySavedView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ActivitySavedViewID) error {
	out, err := r.activity.DeleteActivitySavedView(ctx, &chatv1.DeleteActivitySavedViewRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ViewId: string(id)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed delete activity saved view response was not acknowledged")
	}
	return nil
}

func (r Remote) WorkspaceNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceNotificationPreferences, error) {
	out, err := r.activity.GetWorkspaceNotificationPreferences(ctx, &chatv1.NotificationPreferencesRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return decodeProtoWorkspaceNotificationPreferences(out)
}

func (r Remote) SetNotificationSchedule(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, schedule domain.NotificationSchedule) (domain.WorkspaceNotificationPreferences, error) {
	out, err := r.activity.SetNotificationSchedule(ctx, &chatv1.SetNotificationScheduleRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Schedule: encodeProtoNotificationSchedule(schedule),
	})
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return decodeProtoWorkspaceNotificationPreferences(out)
}

func (r Remote) SetNotificationVIP(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, target domain.UserID, add bool) error {
	out, err := r.activity.SetNotificationVIP(ctx, &chatv1.SetNotificationVIPRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), TargetId: string(target), Add: add,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed set notification VIP response was not acknowledged")
	}
	return nil
}

func encodeProtoSidebarSection(value domain.SidebarSection) *chatv1.SidebarSection {
	conversations := make([]string, 0, len(value.Conversations))
	for _, id := range value.Conversations {
		conversations = append(conversations, string(id))
	}
	return &chatv1.SidebarSection{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		Name: value.Name, Position: int32(value.Position), Collapsed: value.Collapsed,
		CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt), Conversations: conversations,
		NotificationLevel: string(value.NotificationLevel),
	}
}

func decodeProtoSidebarSection(value *chatv1.SidebarSection) domain.SidebarSection {
	if value == nil {
		return domain.SidebarSection{}
	}
	section := domain.SidebarSection{
		ID: domain.SidebarSectionID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		UserID: domain.UserID(value.GetUserId()), Name: value.GetName(), Position: int(value.GetPosition()),
		Collapsed: value.GetCollapsed(), NotificationLevel: domain.NotificationLevel(value.GetNotificationLevel()),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
	}
	for _, id := range value.GetConversations() {
		section.Conversations = append(section.Conversations, domain.ConversationID(id))
	}
	return section
}

func (r Remote) SidebarSections(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.SidebarSection, error) {
	out, err := r.activity.SidebarSections(ctx, &chatv1.SidebarSectionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	sections := make([]domain.SidebarSection, 0, len(out.GetSections()))
	for _, section := range out.GetSections() {
		sections = append(sections, decodeProtoSidebarSection(section))
	}
	return sections, nil
}

func (r Remote) CreateSidebarSection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string) (domain.SidebarSection, error) {
	out, err := r.activity.CreateSidebarSection(ctx, &chatv1.CreateSidebarSectionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Name: name})
	if err != nil {
		return domain.SidebarSection{}, err
	}
	return decodeProtoSidebarSection(out), nil
}

func (r Remote) RenameSidebarSection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SidebarSectionID, name string) error {
	out, err := r.activity.RenameSidebarSection(ctx, &chatv1.RenameSidebarSectionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SectionId: string(id), Name: name})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed rename sidebar section response was not acknowledged")
	}
	return nil
}

func (r Remote) SetSidebarSectionCollapsed(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SidebarSectionID, collapsed bool) error {
	out, err := r.activity.SetSidebarSectionCollapsed(ctx, &chatv1.SetSidebarSectionCollapsedRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SectionId: string(id), Collapsed: collapsed})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed set sidebar section collapsed response was not acknowledged")
	}
	return nil
}

func (r Remote) SetSidebarSectionNotificationLevel(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SidebarSectionID, level domain.NotificationLevel) error {
	out, err := r.activity.SetSidebarSectionNotificationLevel(ctx, &chatv1.SetSidebarSectionNotificationLevelRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SectionId: string(id), Level: string(level)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed set sidebar section notification level response was not acknowledged")
	}
	return nil
}

func (r Remote) DeleteSidebarSection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SidebarSectionID) error {
	out, err := r.activity.DeleteSidebarSection(ctx, &chatv1.DeleteSidebarSectionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SectionId: string(id)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed delete sidebar section response was not acknowledged")
	}
	return nil
}

func (r Remote) ReorderSidebarSections(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, order []domain.SidebarSectionID) error {
	ids := make([]string, 0, len(order))
	for _, id := range order {
		ids = append(ids, string(id))
	}
	out, err := r.activity.ReorderSidebarSections(ctx, &chatv1.ReorderSidebarSectionsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SectionIds: ids})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed reorder sidebar sections response was not acknowledged")
	}
	return nil
}

func (r Remote) AssignConversationToSidebarSection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, sectionID domain.SidebarSectionID, after domain.ConversationID) error {
	out, err := r.activity.AssignConversationToSidebarSection(ctx, &chatv1.AssignConversationToSidebarSectionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), SectionId: string(sectionID), AfterConversationId: string(after),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed assign conversation to sidebar section response was not acknowledged")
	}
	return nil
}

func (r Remote) SetWorkspaceNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, level domain.NotificationLevel, keywords []string, activityChannels, activityReminders, browserNotifications bool) (domain.WorkspaceNotificationPreferences, error) {
	out, err := r.activity.SetWorkspaceNotificationPreferences(ctx, &chatv1.SetWorkspaceNotificationPreferencesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Level: string(level),
		Keywords: keywords, ActivityChannels: activityChannels, ActivityReminders: activityReminders,
		BrowserNotifications: browserNotifications,
	})
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return decodeProtoWorkspaceNotificationPreferences(out)
}

func (r Remote) ConversationNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationNotificationPreferences, error) {
	out, err := r.activity.GetConversationNotificationPreferences(ctx, &chatv1.ConversationNotificationPreferencesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
	})
	if err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	return decodeProtoConversationNotificationPreferences(out)
}

func (r Remote) SetConversationNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, level domain.NotificationLevel, followEveryThread bool) (domain.ConversationNotificationPreferences, error) {
	out, err := r.activity.SetConversationNotificationPreferences(ctx, &chatv1.SetConversationNotificationPreferencesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID),
		Level: string(level), FollowEveryThread: followEveryThread,
	})
	if err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	return decodeProtoConversationNotificationPreferences(out)
}

func (r Remote) ThreadFollowed(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, root domain.MessageTimestamp) (bool, error) {
	out, err := r.activity.GetThreadFollow(ctx, &chatv1.ThreadFollowRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), ThreadTimestamp: string(root),
	})
	if err != nil {
		return false, err
	}
	return out.GetFollowed(), nil
}

func (r Remote) SetThreadFollowed(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, root domain.MessageTimestamp, followed bool) error {
	out, err := r.activity.SetThreadFollow(ctx, &chatv1.SetThreadFollowRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), ThreadTimestamp: string(root), Followed: followed,
	})
	if err != nil {
		return err
	}
	if out.GetFollowed() != followed {
		return errors.New("thread follow mutation was not acknowledged")
	}
	return nil
}

func (r Remote) CompleteLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.LaterReminderID) error {
	out, err := r.reminders.CompleteLaterReminder(ctx, &chatv1.LaterReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "Later reminder completion")
}

func (r Remote) DeleteLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.LaterReminderID) error {
	out, err := r.reminders.DeleteLaterReminder(ctx, &chatv1.LaterReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(reminderID)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "Later reminder deletion")
}

func (r Remote) ScheduleMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text string, postAt time.Time) (domain.ScheduledMessage, error) {
	return r.ScheduleMessageWithBlocks(ctx, workspaceID, userID, channel, text, "", postAt)
}

func (r Remote) ScheduledMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	return r.ScheduledMessagesForCredential(ctx, workspaceID, userID, domain.ScheduledMessageQuery{
		CredentialHash: grpcScheduledCredential(workspaceID, userID),
		Channel:        channel,
		Page:           request,
	})
}

func (r Remote) ScheduledMessagesForCredential(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query domain.ScheduledMessageQuery) (domain.ScheduledMessagePage, error) {
	out, err := r.scheduled.ScheduledMessages(ctx, &chatv1.ScheduledMessagesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(query.Channel),
		Limit: int32(query.Page.Limit), Cursor: string(query.Page.Cursor), CredentialHash: query.CredentialHash,
		Oldest: unixOrZero(query.Oldest), Latest: unixOrZero(query.Latest),
	})
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

func (r Remote) ScheduledMessageHistory(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, includeDelivered bool, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	out, err := r.scheduled.ScheduledMessageHistory(ctx, &chatv1.ScheduledMessageHistoryRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit),
		Cursor: string(request.Cursor), Descending: request.Descending, IncludeDelivered: includeDelivered,
	})
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

func (r Remote) UpdateScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledMessageID, channel domain.ConversationID, text string, postAt time.Time) (domain.ScheduledMessage, error) {
	out, err := r.scheduled.UpdateScheduledMessage(ctx, &chatv1.UpdateScheduledMessageRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ScheduledMessageId: string(id),
		ChannelId: string(channel), Text: text, PostAt: postAt.UTC().Unix(),
	})
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	return decodeProtoScheduledMessage(out)
}

func (r Remote) SendScheduledMessageNow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledMessageID) (domain.Message, error) {
	out, err := r.scheduled.SendScheduledMessageNow(ctx, &chatv1.SendScheduledMessageNowRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ScheduledMessageId: string(id),
	})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) PostScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, id domain.ScheduledMessageID) (domain.Message, error) {
	out, err := r.scheduled.PostScheduledMessage(ctx, &chatv1.PostScheduledMessageRequest{
		WorkspaceId: string(workspaceID), ScheduledMessageId: string(id),
	})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) DeleteScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID) error {
	return r.DeleteScheduledMessageForCredential(ctx, workspaceID, userID, grpcScheduledCredential(workspaceID, userID), channel, id)
}

func (r Remote) DeleteScheduledMessageForCredential(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, credentialHash string, channel domain.ConversationID, id domain.ScheduledMessageID) error {
	out, err := r.scheduled.DeleteScheduledMessage(ctx, &chatv1.DeleteScheduledMessageRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(channel), ScheduledMessageId: string(id), CredentialHash: credentialHash})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed scheduled message deletion was not acknowledged")
	}
	return nil
}

func (r Remote) SaveDraft(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, text string) (domain.Draft, error) {
	return r.SaveDraftWithAttachments(ctx, workspaceID, userID, conversation, thread, text, nil)
}

func (r Remote) SaveDraftWithAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, text string, attachments []domain.DraftAttachment) (domain.Draft, error) {
	out, err := r.scheduled.SaveDraft(ctx, &chatv1.DraftRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversation),
		ThreadTs: string(thread), Text: text, Attachments: encodeProtoDraftAttachments(attachments),
	})
	if err != nil {
		return domain.Draft{}, err
	}
	return decodeProtoDraft(out)
}

func (r Remote) Draft(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.Draft, error) {
	out, err := r.scheduled.GetDraft(ctx, &chatv1.DraftRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversation), ThreadTs: string(thread),
	})
	if err != nil {
		return domain.Draft{}, err
	}
	return decodeProtoDraft(out)
}

func (r Remote) Drafts(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.DraftPage, error) {
	out, err := r.scheduled.Drafts(ctx, &chatv1.DraftsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending,
	})
	if err != nil {
		return domain.DraftPage{}, err
	}
	items := make([]domain.Draft, 0, len(out.GetDrafts()))
	for _, value := range out.GetDrafts() {
		item, err := decodeProtoDraft(value)
		if err != nil {
			return domain.DraftPage{}, err
		}
		items = append(items, item)
	}
	return domain.DraftPage{Items: items, NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}, nil
}

func (r Remote) DeleteDraft(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) error {
	out, err := r.scheduled.DeleteDraft(ctx, &chatv1.DraftRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversation), ThreadTs: string(thread),
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "draft deletion")
}

func (r Remote) SentMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.MessagePage, error) {
	out, err := r.scheduled.SentMessages(ctx, &chatv1.SentMessagesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit),
		Cursor: string(request.Cursor), Descending: request.Descending,
	})
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
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

func (r Remote) ListUserEventsAfter(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, after uint64, limit int) ([]events.Record, error) {
	in := &chatv1.EventsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), After: after, Limit: int32(limit)}
	out, err := r.events.ListEventsAfter(ctx, in)
	if err != nil {
		return nil, err
	}
	return decodeProtoEvents(out)
}

func (r Remote) ClaimAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, lease time.Duration) (events.Record, int, string, bool, error) {
	out, err := r.events.ClaimAppEvent(ctx, &chatv1.AppEventClaimRequest{AppId: string(appID), Surface: surface, Owner: owner, LeaseNanos: int64(lease)})
	if err != nil {
		return events.Record{}, 0, "", false, err
	}
	if !out.GetFound() {
		return events.Record{}, int(out.GetAttempt()), out.GetRetryReason(), false, nil
	}
	record, err := decodeProtoEventRecord(out.GetRecord())
	if err != nil {
		return events.Record{}, 0, "", false, err
	}
	return record, int(out.GetAttempt()), out.GetRetryReason(), true, nil
}

func (r Remote) AckAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64) error {
	out, err := r.events.AckAppEvent(ctx, &chatv1.AppEventAckRequest{AppId: string(appID), Surface: surface, Owner: owner, Sequence: sequence})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "app event acknowledgement")
}

func (r Remote) ReleaseAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64, reason string, retryAt time.Time) error {
	out, err := r.events.ReleaseAppEvent(ctx, &chatv1.AppEventReleaseRequest{AppId: string(appID), Surface: surface, Owner: owner, Sequence: sequence, RetryReason: reason, RetryAtUnixNano: retryAt.UTC().UnixNano()})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "app event release")
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
	_ chatv1.SavedItemsServiceServer              = (*Server)(nil)
	_ chatv1.ActivityServiceServer                = (*Server)(nil)
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
	_ chatv1.AppsServiceServer                    = (*Server)(nil)
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
	chatv1.RegisterSavedItemsServiceServer(registrar, server)
	chatv1.RegisterActivityServiceServer(registrar, server)
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
	chatv1.RegisterAppsServiceServer(registrar, server)
	return nil
}

func (s *Server) CreateCanvas(ctx context.Context, input *chatv1.CreateCanvasRequest) (*chatv1.Canvas, error) {
	return s.createCanvasProto(ctx, input)
}

func (s *Server) CreateConversationCanvas(ctx context.Context, input *chatv1.CreateCanvasRequest) (*chatv1.Canvas, error) {
	canvas, err := s.implementation.CreateConversationCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetChannelId()), input.GetTitle(), input.GetDocumentContent())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvas(canvas), nil
}

func (s *Server) ConversationCanvas(ctx context.Context, input *chatv1.CreateCanvasRequest) (*chatv1.Canvas, error) {
	canvas, err := s.implementation.ConversationCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetChannelId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvas(canvas), nil
}

func (s *Server) GetCanvas(ctx context.Context, input *chatv1.CanvasRequest) (*chatv1.Canvas, error) {
	value, err := s.implementation.Canvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvas(value), nil
}

func (s *Server) GetCanvasAccess(ctx context.Context, input *chatv1.CanvasRequest) (*chatv1.CanvasAccessResponse, error) {
	value, err := s.implementation.CanvasAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.CanvasAccessResponse{CanvasId: string(value.CanvasID), EntityType: string(value.EntityType), EntityId: value.EntityID, Access: string(value.Access)}, nil
}

func (s *Server) CommentOnCanvas(ctx context.Context, input *chatv1.CommentOnCanvasRequest) (*chatv1.CanvasComment, error) {
	value, err := s.implementation.CommentOnCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetSectionId(), input.GetText())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvasComment(value), nil
}

func (s *Server) CanvasComments(ctx context.Context, input *chatv1.CanvasCommentsRequest) (*chatv1.CanvasCommentPage, error) {
	value, err := s.implementation.CanvasComments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvasCommentPage(value), nil
}

func (s *Server) DeleteCanvasComment(ctx context.Context, input *chatv1.DeleteCanvasCommentRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteCanvasComment(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasCommentID(input.GetCommentId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func encodeProtoCanvasComment(value domain.CanvasComment) *chatv1.CanvasComment {
	return &chatv1.CanvasComment{
		Id: string(value.ID), CanvasId: string(value.CanvasID), WorkspaceId: string(value.WorkspaceID),
		SectionId: value.SectionID, UserId: string(value.UserID), Text: value.Text,
		CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt), Deleted: value.Deleted,
	}
}

func decodeProtoCanvasComment(value *chatv1.CanvasComment) domain.CanvasComment {
	if value == nil {
		return domain.CanvasComment{}
	}
	return domain.CanvasComment{
		ID: domain.CanvasCommentID(value.GetId()), CanvasID: domain.CanvasID(value.GetCanvasId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), SectionID: value.GetSectionId(),
		UserID: domain.UserID(value.GetUserId()), Text: value.GetText(),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), Deleted: value.GetDeleted(),
	}
}

func encodeProtoCanvasCommentPage(value domain.CanvasCommentPage) *chatv1.CanvasCommentPage {
	comments := make([]*chatv1.CanvasComment, 0, len(value.Comments))
	for _, comment := range value.Comments {
		comments = append(comments, encodeProtoCanvasComment(comment))
	}
	return &chatv1.CanvasCommentPage{Comments: comments, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoCanvasCommentPage(value *chatv1.CanvasCommentPage) (domain.CanvasCommentPage, error) {
	if value == nil {
		return domain.CanvasCommentPage{}, errors.New("typed canvas comment page is incomplete")
	}
	comments := make([]domain.CanvasComment, 0, len(value.GetComments()))
	for _, comment := range value.GetComments() {
		comments = append(comments, decodeProtoCanvasComment(comment))
	}
	return domain.CanvasCommentPage{Comments: comments, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func (s *Server) CommentOnListItem(ctx context.Context, input *chatv1.CommentOnListItemRequest) (*chatv1.ListItemComment, error) {
	value, err := s.implementation.CommentOnListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()), input.GetText())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListItemComment(value), nil
}

func (s *Server) ListItemComments(ctx context.Context, input *chatv1.ListItemCommentsRequest) (*chatv1.ListItemCommentPage, error) {
	value, err := s.implementation.ListItemComments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListItemCommentPage(value), nil
}

func (s *Server) DeleteListItemComment(ctx context.Context, input *chatv1.DeleteListItemCommentRequest) (*chatv1.ListOKResponse, error) {
	if err := s.implementation.DeleteListItemComment(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListItemCommentID(input.GetCommentId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListOKResponse{Ok: true}, nil
}

func (s *Server) AttachFileToListItem(ctx context.Context, input *chatv1.AttachFileToListItemRequest) (*chatv1.ListItemFile, error) {
	value, err := s.implementation.AttachFileToListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListItemFile(value), nil
}

func (s *Server) ListItemFiles(ctx context.Context, input *chatv1.ListItemFilesRequest) (*chatv1.ListItemFilesResponse, error) {
	files, err := s.implementation.ListItemFiles(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()))
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.File, 0, len(files))
	for _, file := range files {
		encoded = append(encoded, encodeProtoFile(file))
	}
	return &chatv1.ListItemFilesResponse{Files: encoded}, nil
}

func (s *Server) DetachFileFromListItem(ctx context.Context, input *chatv1.DetachFileFromListItemRequest) (*chatv1.ListOKResponse, error) {
	if err := s.implementation.DetachFileFromListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()), domain.FileID(input.GetFileId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListOKResponse{Ok: true}, nil
}

func encodeProtoListItemFile(value domain.ListItemFile) *chatv1.ListItemFile {
	return &chatv1.ListItemFile{
		Id: string(value.ID), ListId: string(value.ListID), ItemId: string(value.ItemID),
		WorkspaceId: string(value.WorkspaceID), FileId: string(value.FileID),
		CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt),
	}
}

func decodeProtoListItemFile(value *chatv1.ListItemFile) domain.ListItemFile {
	if value == nil {
		return domain.ListItemFile{}
	}
	return domain.ListItemFile{
		ID: domain.ListItemFileID(value.GetId()), ListID: domain.ListID(value.GetListId()), ItemID: domain.ListItemID(value.GetItemId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), FileID: domain.FileID(value.GetFileId()),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
	}
}

func encodeProtoListItemComment(value domain.ListItemComment) *chatv1.ListItemComment {
	return &chatv1.ListItemComment{
		Id: string(value.ID), ListId: string(value.ListID), ItemId: string(value.ItemID), WorkspaceId: string(value.WorkspaceID),
		UserId: string(value.UserID), Text: value.Text,
		CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt), Deleted: value.Deleted,
	}
}

func decodeProtoListItemComment(value *chatv1.ListItemComment) domain.ListItemComment {
	if value == nil {
		return domain.ListItemComment{}
	}
	return domain.ListItemComment{
		ID: domain.ListItemCommentID(value.GetId()), ListID: domain.ListID(value.GetListId()), ItemID: domain.ListItemID(value.GetItemId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Text: value.GetText(),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), Deleted: value.GetDeleted(),
	}
}

func encodeProtoListItemCommentPage(value domain.ListItemCommentPage) *chatv1.ListItemCommentPage {
	comments := make([]*chatv1.ListItemComment, 0, len(value.Comments))
	for _, comment := range value.Comments {
		comments = append(comments, encodeProtoListItemComment(comment))
	}
	return &chatv1.ListItemCommentPage{Comments: comments, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoListItemCommentPage(value *chatv1.ListItemCommentPage) (domain.ListItemCommentPage, error) {
	if value == nil {
		return domain.ListItemCommentPage{}, errors.New("typed list item comment page is incomplete")
	}
	comments := make([]domain.ListItemComment, 0, len(value.GetComments()))
	for _, comment := range value.GetComments() {
		comments = append(comments, decodeProtoListItemComment(comment))
	}
	return domain.ListItemCommentPage{Comments: comments, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func (s *Server) CanvasGrants(ctx context.Context, input *chatv1.CanvasGrantsRequest) (*chatv1.CanvasGrantsResponse, error) {
	grants, err := s.implementation.CanvasGrants(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.CanvasGrantsResponse{Grants: encodeProtoCanvasGrants(grants)}, nil
}

func encodeProtoCanvasGrant(value domain.CanvasAccess) *chatv1.CanvasGrant {
	return &chatv1.CanvasGrant{
		CanvasId: string(value.CanvasID), EntityType: string(value.EntityType),
		EntityId: value.EntityID, Access: string(value.Access),
	}
}

func decodeProtoCanvasGrant(value *chatv1.CanvasGrant) domain.CanvasAccess {
	if value == nil {
		return domain.CanvasAccess{}
	}
	return domain.CanvasAccess{
		CanvasID: domain.CanvasID(value.GetCanvasId()), EntityType: domain.GrantEntity(value.GetEntityType()),
		EntityID: value.GetEntityId(), Access: domain.AccessLevel(value.GetAccess()),
	}
}

func encodeProtoCanvasGrants(values []domain.CanvasAccess) []*chatv1.CanvasGrant {
	grants := make([]*chatv1.CanvasGrant, 0, len(values))
	for _, value := range values {
		grants = append(grants, encodeProtoCanvasGrant(value))
	}
	return grants
}

func decodeProtoCanvasGrants(values []*chatv1.CanvasGrant) []domain.CanvasAccess {
	grants := make([]domain.CanvasAccess, 0, len(values))
	for _, value := range values {
		grants = append(grants, decodeProtoCanvasGrant(value))
	}
	return grants
}

func (s *Server) CanvasRevisions(ctx context.Context, input *chatv1.CanvasRevisionsRequest) (*chatv1.CanvasRevisionPage, error) {
	value, err := s.implementation.CanvasRevisions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvasRevisionPage(value), nil
}

func (s *Server) RestoreCanvasRevision(ctx context.Context, input *chatv1.RestoreCanvasRevisionRequest) (*chatv1.Canvas, error) {
	value, err := s.implementation.RestoreCanvasRevision(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetVersion())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvas(value), nil
}

func encodeProtoCanvasRevision(value domain.CanvasRevision) *chatv1.CanvasRevision {
	return &chatv1.CanvasRevision{
		CanvasId: string(value.CanvasID), WorkspaceId: string(value.WorkspaceID), Version: value.Version,
		Title: value.Title, DocumentContent: value.DocumentContent, EditedBy: string(value.EditedBy),
		CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt),
	}
}

func decodeProtoCanvasRevision(value *chatv1.CanvasRevision) domain.CanvasRevision {
	if value == nil {
		return domain.CanvasRevision{}
	}
	return domain.CanvasRevision{
		CanvasID: domain.CanvasID(value.GetCanvasId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Version: value.GetVersion(), Title: value.GetTitle(), DocumentContent: value.GetDocumentContent(),
		EditedBy: domain.UserID(value.GetEditedBy()), CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
	}
}

func encodeProtoCanvasRevisionPage(value domain.CanvasRevisionPage) *chatv1.CanvasRevisionPage {
	revisions := make([]*chatv1.CanvasRevision, 0, len(value.Revisions))
	for _, revision := range value.Revisions {
		revisions = append(revisions, encodeProtoCanvasRevision(revision))
	}
	return &chatv1.CanvasRevisionPage{Revisions: revisions, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoCanvasRevisionPage(value *chatv1.CanvasRevisionPage) (domain.CanvasRevisionPage, error) {
	if value == nil {
		return domain.CanvasRevisionPage{}, errors.New("typed canvas revision page is incomplete")
	}
	revisions := make([]domain.CanvasRevision, 0, len(value.GetRevisions()))
	for _, revision := range value.GetRevisions() {
		revisions = append(revisions, decodeProtoCanvasRevision(revision))
	}
	return domain.CanvasRevisionPage{Revisions: revisions, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func (s *Server) SearchCanvases(ctx context.Context, input *chatv1.SearchCanvasesRequest) (*chatv1.CanvasPage, error) {
	value, err := s.implementation.SearchCanvases(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasSearchRequest{
		Query:     input.GetQuery(),
		Sort:      domain.SearchSort(input.GetSort()),
		Direction: domain.SearchDirection(input.GetDirection()),
		Page:      protoPageRequest(input.GetLimit(), input.GetCursor()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvasPage(value), nil
}

func (s *Server) ListCanvases(ctx context.Context, input *chatv1.CanvasesRequest) (*chatv1.CanvasPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	request.Descending = input.GetDescending()
	value, err := s.implementation.Canvases(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvasPage(value), nil
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
	canvas, err := s.implementation.CreateCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTitle(), input.GetDocumentContent(), domain.ConversationID(input.GetChannelId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCanvas(canvas), nil
}

func (s *Server) editCanvasProto(ctx context.Context, input *chatv1.EditCanvasRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.EditCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetChanges()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteCanvasProto(ctx context.Context, input *chatv1.CanvasRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) setCanvasAccessProto(ctx context.Context, input *chatv1.CanvasAccessRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetCanvasAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), domain.AccessLevel(input.GetAccessLevel()), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteCanvasAccessProto(ctx context.Context, input *chatv1.CanvasAccessDeleteRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteCanvasAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) lookupCanvasSectionsProto(ctx context.Context, input *chatv1.CanvasSectionsLookupRequest) (*chatv1.CanvasSectionsResponse, error) {
	sections, err := s.implementation.LookupCanvasSections(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.CanvasID(input.GetCanvasId()), input.GetCriteria())
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.CanvasSection, 0, len(sections))
	for _, section := range sections {
		result = append(result, &chatv1.CanvasSection{Id: section.ID, Type: string(section.Type), Text: section.Text})
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
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
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
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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

func (s *Server) SendCallSignal(ctx context.Context, input *chatv1.CallSignalRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SendCallSignal(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.CallID(input.GetCallId()), domain.UserID(input.GetRecipientId()),
		domain.CallSignalKind(input.GetKind()), input.GetPayload()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SendHuddleReaction(ctx context.Context, input *chatv1.HuddleReactionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SendHuddleReaction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.CallID(input.GetCallId()), input.GetReaction()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) InviteToHuddle(ctx context.Context, input *chatv1.HuddleInviteRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.InviteToHuddle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.UserID(input.GetInviteeId()), domain.ConversationID(input.GetConversationId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) StartHuddle(ctx context.Context, input *chatv1.HuddleRequest) (*chatv1.Call, error) {
	return huddleResponse(s.implementation.StartHuddle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetTitle()))
}

func (s *Server) JoinHuddle(ctx context.Context, input *chatv1.HuddleRequest) (*chatv1.Call, error) {
	return huddleResponse(s.implementation.JoinHuddle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())))
}

func (s *Server) LeaveHuddle(ctx context.Context, input *chatv1.HuddleRequest) (*chatv1.Call, error) {
	return huddleResponse(s.implementation.LeaveHuddle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())))
}

func (s *Server) EndHuddle(ctx context.Context, input *chatv1.HuddleRequest) (*chatv1.Call, error) {
	return huddleResponse(s.implementation.EndHuddle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())))
}

// GetActiveHuddle is the transport name; an rpc named ActiveHuddle would be
// fine here, but the Get prefix keeps it beside GetReadCursor and
// GetWorkspaceAnalytics, which had to take it to avoid shadowing their types.
func (s *Server) GetActiveHuddle(ctx context.Context, input *chatv1.HuddleRequest) (*chatv1.Call, error) {
	return huddleResponse(s.implementation.ActiveHuddle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())))
}

func huddleResponse(value domain.Call, err error) (*chatv1.Call, error) {
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCall(value), nil
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

func (s *Server) AdminCreateBarrier(ctx context.Context, input *chatv1.BarrierMutationRequest) (*chatv1.InformationBarrier, error) {
	barrier, err := s.implementation.AdminCreateBarrier(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.UserGroupID(input.GetPrimaryUsergroupId()), decodeUserGroupIDs(input.GetBarrieredFromIds()), decodeBarrierSubjects(input.GetSubjects()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBarrier(barrier), nil
}

func (s *Server) AdminUpdateBarrier(ctx context.Context, input *chatv1.BarrierMutationRequest) (*chatv1.InformationBarrier, error) {
	barrier, err := s.implementation.AdminUpdateBarrier(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.BarrierID(input.GetBarrierId()), domain.UserGroupID(input.GetPrimaryUsergroupId()),
		decodeUserGroupIDs(input.GetBarrieredFromIds()), decodeBarrierSubjects(input.GetSubjects()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBarrier(barrier), nil
}

func (s *Server) AdminDeleteBarrier(ctx context.Context, input *chatv1.BarrierMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminDeleteBarrier(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.BarrierID(input.GetBarrierId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminBarriers(ctx context.Context, input *chatv1.BarriersRequest) (*chatv1.InformationBarrierPage, error) {
	page, err := s.implementation.AdminBarriers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	barriers := make([]*chatv1.InformationBarrier, 0, len(page.Barriers))
	for _, value := range page.Barriers {
		barriers = append(barriers, encodeProtoBarrier(value))
	}
	return &chatv1.InformationBarrierPage{Barriers: barriers, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) AdminSetSessionSettings(ctx context.Context, input *chatv1.SessionSettingsMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminSetSessionSettings(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeUserIDs(input.GetTargetUserIds()), decodeProtoSessionSettings(input.GetSettings())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminClearSessionSettings(ctx context.Context, input *chatv1.SessionSettingsMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminClearSessionSettings(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeUserIDs(input.GetTargetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) MemberSessionSettings(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.SessionSettings, error) {
	settings, err := s.implementation.MemberSessionSettings(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSessionSettings(settings), nil
}

func (s *Server) MemberMustUsePasswordSignIn(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.MemberAuthPolicyResponse, error) {
	required, err := s.implementation.MemberMustUsePasswordSignIn(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MemberAuthPolicyResponse{MustUsePasswordSignIn: required}, nil
}

func (s *Server) AdminSessionSettings(ctx context.Context, input *chatv1.SessionSettingsMutationRequest) (*chatv1.SessionSettingsResponse, error) {
	settings, err := s.implementation.AdminSessionSettings(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeUserIDs(input.GetTargetUserIds()))
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.SessionSettings, 0, len(settings))
	for _, value := range settings {
		encoded = append(encoded, encodeProtoSessionSettings(value))
	}
	return &chatv1.SessionSettingsResponse{Settings: encoded}, nil
}

func (s *Server) AdminAssignAuthPolicy(ctx context.Context, input *chatv1.AuthPolicyMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminAssignAuthPolicy(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AuthPolicyName(input.GetPolicyName()), domain.PolicyEntityType(input.GetEntityType()), input.GetEntityIds()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminRemoveAuthPolicyEntities(ctx context.Context, input *chatv1.AuthPolicyMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRemoveAuthPolicyEntities(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AuthPolicyName(input.GetPolicyName()), domain.PolicyEntityType(input.GetEntityType()), input.GetEntityIds()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminAuthPolicyEntities(ctx context.Context, input *chatv1.AuthPolicyEntitiesRequest) (*chatv1.AuthPolicyEntityPage, error) {
	page, err := s.implementation.AdminAuthPolicyEntities(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AuthPolicyName(input.GetPolicyName()), domain.PolicyEntityType(input.GetEntityType()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	entities := make([]*chatv1.AuthPolicyEntity, 0, len(page.Entities))
	for _, value := range page.Entities {
		entities = append(entities, encodeProtoAuthPolicyEntity(value))
	}
	return &chatv1.AuthPolicyEntityPage{Entities: entities, NextCursor: string(page.NextCursor),
		HasMore: page.HasMore, TotalCount: int32(page.TotalCount)}, nil
}

func (s *Server) AdminRequestExport(ctx context.Context, input *chatv1.ExportRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRequestExport(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		input.GetKind(), input.GetBounds()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) RequestWorkflowStepResponsesExport(ctx context.Context, input *chatv1.ExportRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RequestWorkflowStepResponsesExport(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), input.GetStepId()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminAnomalyAllowList(ctx context.Context, input *chatv1.AnomalyAllowListRequest) (*chatv1.AnomalyAllowList, error) {
	value, err := s.implementation.AdminAnomalyAllowList(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAnomalyAllowList(value), nil
}

func (s *Server) AdminSetAnomalyAllowList(ctx context.Context, input *chatv1.AnomalyAllowListRequest) (*chatv1.AnomalyAllowList, error) {
	value, err := s.implementation.AdminSetAnomalyAllowList(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		input.GetIpAddresses(), input.GetReasons())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAnomalyAllowList(value), nil
}

func (s *Server) TeamBillingInfo(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.BillingInfoResponse, error) {
	plan, err := s.implementation.TeamBillingInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.BillingInfoResponse{Plan: string(plan)}, nil
}

func (s *Server) AdminAnalytics(ctx context.Context, input *chatv1.AnalyticsRequest) (*chatv1.AnalyticsResponse, error) {
	rows, err := s.implementation.AdminAnalytics(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AnalyticsKind(input.GetKind()), optionalTimeFromUnixNano(input.GetDay()))
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.AnalyticsRow, 0, len(rows))
	for _, row := range rows {
		encoded = append(encoded, encodeProtoAnalyticsRow(row))
	}
	return &chatv1.AnalyticsResponse{Rows: encoded}, nil
}

func (s *Server) AdminAddRoleAssignments(ctx context.Context, input *chatv1.RoleAssignmentMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminAddRoleAssignments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetRoleId(), input.GetEntityIds(), decodeUserIDs(input.GetTargetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminRemoveRoleAssignments(ctx context.Context, input *chatv1.RoleAssignmentMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminRemoveRoleAssignments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetRoleId(), input.GetEntityIds(), decodeUserIDs(input.GetTargetUserIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminListRoleAssignments(ctx context.Context, input *chatv1.RoleAssignmentsRequest) (*chatv1.RoleAssignmentPage, error) {
	page, err := s.implementation.AdminListRoleAssignments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetRoleId(), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	assignments := make([]*chatv1.RoleAssignment, 0, len(page.Assignments))
	for _, value := range page.Assignments {
		assignments = append(assignments, encodeProtoRoleAssignment(value))
	}
	return &chatv1.RoleAssignmentPage{Assignments: assignments, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) DiscoverableContacts(ctx context.Context, input *chatv1.DiscoverableContactsRequest) (*chatv1.DiscoverableContactsResponse, error) {
	users, err := s.implementation.DiscoverableContacts(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetEmails())
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.User, 0, len(users))
	for _, user := range users {
		encoded = append(encoded, encodeProtoUser(user))
	}
	return &chatv1.DiscoverableContactsResponse{Users: encoded}, nil
}

func (s *Server) UserExpiration(ctx context.Context, input *chatv1.SetUserExpirationRequest) (*chatv1.UserExpirationResponse, error) {
	expiration, err := s.implementation.UserExpiration(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.UserExpirationResponse{ExpirationUnixNano: optionalUnixNano(expiration)}, nil
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

func (s *Server) UserSessions(ctx context.Context, input *chatv1.ResetUserSessionsRequest) (*chatv1.UserSessionsResponse, error) {
	sessions, err := s.implementation.UserSessions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.UserSessionsResponse{Sessions: encodeProtoWorkspaceSessions(sessions)}, nil
}

func (s *Server) ResetUserSessionsBulk(ctx context.Context, input *chatv1.ResetUserSessionsBulkRequest) (*chatv1.MutationResponse, error) {
	targets := make([]domain.UserID, 0, len(input.GetTargetUserIds()))
	for _, target := range input.GetTargetUserIds() {
		targets = append(targets, domain.UserID(target))
	}
	if err := s.implementation.ResetUserSessionsBulk(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), targets); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func encodeProtoWorkspaceSession(value domain.WorkspaceSession) *chatv1.WorkspaceSession {
	return &chatv1.WorkspaceSession{
		Id: value.ID, UserId: string(value.UserID),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), ExpiresAtUnixNano: optionalUnixNano(value.ExpiresAt),
	}
}

func decodeProtoWorkspaceSession(value *chatv1.WorkspaceSession) domain.WorkspaceSession {
	if value == nil {
		return domain.WorkspaceSession{}
	}
	return domain.WorkspaceSession{
		ID: value.GetId(), UserID: domain.UserID(value.GetUserId()),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), ExpiresAt: optionalTimeFromUnixNano(value.GetExpiresAtUnixNano()),
	}
}

func encodeProtoWorkspaceSessions(values []domain.WorkspaceSession) []*chatv1.WorkspaceSession {
	sessions := make([]*chatv1.WorkspaceSession, 0, len(values))
	for _, value := range values {
		sessions = append(sessions, encodeProtoWorkspaceSession(value))
	}
	return sessions
}

func decodeProtoWorkspaceSessions(values []*chatv1.WorkspaceSession) []domain.WorkspaceSession {
	sessions := make([]domain.WorkspaceSession, 0, len(values))
	for _, value := range values {
		sessions = append(sessions, decodeProtoWorkspaceSession(value))
	}
	return sessions
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

func (s *Server) AdminBulkArchiveConversations(ctx context.Context, input *chatv1.BulkConversationsRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminBulkArchiveConversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), bulkConversationIDs(input)); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminBulkDeleteConversations(ctx context.Context, input *chatv1.BulkConversationsRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminBulkDeleteConversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), bulkConversationIDs(input)); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func bulkConversationIDs(input *chatv1.BulkConversationsRequest) []domain.ConversationID {
	ids := make([]domain.ConversationID, 0, len(input.GetConversationIds()))
	for _, id := range input.GetConversationIds() {
		ids = append(ids, domain.ConversationID(id))
	}
	return ids
}

func (s *Server) AdminConvertConversationToPublic(ctx context.Context, input *chatv1.ConvertConversationToPrivateRequest) (*chatv1.Conversation, error) {
	value, err := s.implementation.AdminConvertConversationToPublic(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(value), nil
}

func (s *Server) AdminConvertConversationToPrivate(ctx context.Context, input *chatv1.ConvertConversationToPrivateRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.AdminConvertConversationToPrivate(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) AdminConversationTeams(ctx context.Context, input *chatv1.AdminConversationTeamsRequest) (*chatv1.AdminConversationTeamsResponse, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	value, err := s.implementation.AdminSearchConversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery(), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPage(value), nil
}

func (s *Server) SearchPeople(ctx context.Context, input *chatv1.SearchPeopleRequest) (*chatv1.UserPage, error) {
	value, err := s.implementation.SearchPeople(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery(), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserPage(value), nil
}

func (s *Server) SearchChannels(ctx context.Context, input *chatv1.SearchConversationsRequest) (*chatv1.ConversationPage, error) {
	value, err := s.implementation.SearchChannels(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery(), protoPageRequest(input.GetLimit(), input.GetCursor()))
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
	// The target is the request's conversation_id, which is the parameter the
	// module contract takes. The server used to re-derive it from
	// prefs.conversation_id while the client dropped the parameter, so a call
	// whose parameter and payload disagreed mutated one conversation in process
	// and another across the seam. prefs.conversation_id remains the fallback only
	// for a client old enough not to send the field, which a rolling deployment of
	// the two processes can still produce.
	target := domain.ConversationID(input.GetConversationId())
	if target == "" {
		target = domain.ConversationID(input.GetPrefs().GetConversationId())
	}
	value, err := s.implementation.AdminSetConversationPrefs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), target, decodeProtoConversationPrefsValue(input.GetPrefs()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPrefs(value), nil
}

func (s *Server) AdminTeamUsers(ctx context.Context, input *chatv1.AdminTeamUsersRequest) (*chatv1.UserPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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

func (s *Server) GetWorkspaceMembership(ctx context.Context, input *chatv1.WorkspaceMembershipRequest) (*chatv1.WorkspaceMembership, error) {
	value, err := s.implementation.WorkspaceMembership(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspaceMembership(value), nil
}

// ProvisionExternalUser and SynchronizeExternalUserRole take no actor by design;
// see the contract on service.ProvisionExternalUser. They are reachable only over
// the mutually authenticated chat connection, so the required fields are the ones
// the operation itself needs and there is no user_id to validate.
func (s *Server) ProvisionExternalUser(ctx context.Context, input *chatv1.ProvisionExternalUserRequest) (*chatv1.User, error) {
	value, err := s.implementation.ProvisionExternalUser(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetEmail(), input.GetRealName(), domain.WorkspaceRole(input.GetRole()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(value), nil
}

func (s *Server) SynchronizeExternalUserRole(ctx context.Context, input *chatv1.SynchronizeExternalUserRoleRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SynchronizeExternalUserRole(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetTargetUserId()), domain.WorkspaceRole(input.GetRole())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminListUsers(ctx context.Context, input *chatv1.AdminUsersRequest) (*chatv1.AdminUserPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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

// GetWorkspaceRetention is the transport name; see the comment on the rpc.
func (s *Server) GetWorkspaceRetention(ctx context.Context, input *chatv1.RetentionPolicyRequest) (*chatv1.RetentionPolicy, error) {
	policy, err := s.implementation.WorkspaceRetention(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRetentionPolicy(policy), nil
}

func (s *Server) SetWorkspaceRetention(ctx context.Context, input *chatv1.RetentionPolicyRequest) (*chatv1.RetentionPolicy, error) {
	policy, err := decodeProtoRetentionPolicy(input.GetPolicy())
	if err != nil {
		return nil, mapError(err)
	}
	saved, err := s.implementation.SetWorkspaceRetention(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), policy)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoRetentionPolicy(saved), nil
}

// GetLastRetentionSweep is the transport name; the Get prefix keeps it beside
// GetWorkspaceRetention, which had to take one to avoid shadowing its type.
func (s *Server) GetLastRetentionSweep(ctx context.Context, input *chatv1.RetentionPolicyRequest) (*chatv1.LastRetentionSweepResponse, error) {
	swept, err := s.implementation.LastRetentionSweep(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	response := &chatv1.LastRetentionSweepResponse{}
	if !swept.IsZero() {
		response.SweptAt = swept.Unix()
	}
	return response, nil
}

func (s *Server) GetConversationRetention(ctx context.Context, input *chatv1.ConversationRetentionRequest) (*chatv1.ConversationRetentionResponse, error) {
	override, effective, err := s.implementation.ConversationRetention(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	response := &chatv1.ConversationRetentionResponse{DurationDays: int32(override.DurationDays), EffectiveDays: int32(effective)}
	if !override.UpdatedAt.IsZero() {
		response.UpdatedAt = override.UpdatedAt.Unix()
	}
	return response, nil
}

func (s *Server) SetConversationRetention(ctx context.Context, input *chatv1.ConversationRetentionRequest) (*chatv1.RetentionMutationResponse, error) {
	if err := s.implementation.SetConversationRetention(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), int(input.GetDurationDays())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.RetentionMutationResponse{Ok: true}, nil
}

func (s *Server) RemoveConversationRetention(ctx context.Context, input *chatv1.ConversationRetentionRequest) (*chatv1.RetentionMutationResponse, error) {
	if err := s.implementation.RemoveConversationRetention(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.RetentionMutationResponse{Ok: true}, nil
}

func (s *Server) InviteShared(ctx context.Context, input *chatv1.InviteSharedRequest) (*chatv1.SharedInvite, error) {
	return sharedInviteResponse(s.implementation.InviteShared(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.WorkspaceID(input.GetTargetWorkspaceId()), input.GetTargetEmail()))
}

func (s *Server) ApproveSharedInvite(ctx context.Context, input *chatv1.SharedInviteMutationRequest) (*chatv1.SharedInvite, error) {
	return sharedInviteResponse(s.implementation.ApproveSharedInvite(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SharedInviteID(input.GetSharedInviteId())))
}

func (s *Server) DenySharedInvite(ctx context.Context, input *chatv1.SharedInviteMutationRequest) (*chatv1.SharedInvite, error) {
	return sharedInviteResponse(s.implementation.DenySharedInvite(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SharedInviteID(input.GetSharedInviteId())))
}

func (s *Server) RevokeSharedInvite(ctx context.Context, input *chatv1.SharedInviteMutationRequest) (*chatv1.SharedInvite, error) {
	return sharedInviteResponse(s.implementation.RevokeSharedInvite(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SharedInviteID(input.GetSharedInviteId())))
}

func (s *Server) DeclineSharedInvite(ctx context.Context, input *chatv1.SharedInviteMutationRequest) (*chatv1.SharedInvite, error) {
	return sharedInviteResponse(s.implementation.DeclineSharedInvite(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SharedInviteID(input.GetSharedInviteId())))
}

func (s *Server) AcceptSharedInvite(ctx context.Context, input *chatv1.SharedInviteMutationRequest) (*chatv1.Conversation, error) {
	value, err := s.implementation.AcceptSharedInvite(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SharedInviteID(input.GetSharedInviteId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(value), nil
}

func (s *Server) ExternalTeams(ctx context.Context, input *chatv1.ExternalTeamsRequest) (*chatv1.ExternalTeamPage, error) {
	page, err := s.implementation.ExternalTeams(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoExternalTeamPage(page), nil
}

func (s *Server) DisconnectExternalTeam(ctx context.Context, input *chatv1.DisconnectExternalTeamRequest) (*chatv1.DisconnectExternalTeamResponse, error) {
	if err := s.implementation.DisconnectExternalTeam(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkspaceID(input.GetTeamId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DisconnectExternalTeamResponse{Ok: true}, nil
}

func encodeProtoExternalTeam(value domain.ExternalTeam) *chatv1.ExternalTeam {
	return &chatv1.ExternalTeam{TeamId: string(value.ID), Name: value.Name, Channels: int32(value.Channels)}
}

func decodeProtoExternalTeam(value *chatv1.ExternalTeam) domain.ExternalTeam {
	if value == nil {
		return domain.ExternalTeam{}
	}
	return domain.ExternalTeam{ID: domain.WorkspaceID(value.GetTeamId()), Name: value.GetName(), Channels: int(value.GetChannels())}
}

func encodeProtoExternalTeamPage(page domain.ExternalTeamPage) *chatv1.ExternalTeamPage {
	teams := make([]*chatv1.ExternalTeam, 0, len(page.Teams))
	for _, team := range page.Teams {
		teams = append(teams, encodeProtoExternalTeam(team))
	}
	return &chatv1.ExternalTeamPage{Teams: teams, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoExternalTeamPage(value *chatv1.ExternalTeamPage) domain.ExternalTeamPage {
	teams := make([]domain.ExternalTeam, 0, len(value.GetTeams()))
	for _, team := range value.GetTeams() {
		teams = append(teams, decodeProtoExternalTeam(team))
	}
	return domain.ExternalTeamPage{Teams: teams, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}
}

func (s *Server) ListSharedInvites(ctx context.Context, input *chatv1.SharedInvitesRequest) (*chatv1.SharedInvitePage, error) {
	page, err := s.implementation.ListSharedInvites(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SharedInviteStatus(input.GetStatus()), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	values := make([]*chatv1.SharedInvite, 0, len(page.Invites))
	for _, value := range page.Invites {
		values = append(values, encodeProtoSharedInvite(value))
	}
	return &chatv1.SharedInvitePage{Invites: values, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) SetExternalInvitePermissions(ctx context.Context, input *chatv1.ExternalInvitePermissionsRequest) (*chatv1.Conversation, error) {
	value, err := s.implementation.SetExternalInvitePermissions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.WorkspaceID(input.GetTargetWorkspaceId()), input.GetCanInvite())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(value), nil
}

func (s *Server) ExternalInvitePermission(ctx context.Context, input *chatv1.ExternalInvitePermissionRequest) (*chatv1.ExternalInvitePermissionResponse, error) {
	permitted, err := s.implementation.ExternalInvitePermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.WorkspaceID(input.GetTargetWorkspaceId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ExternalInvitePermissionResponse{CanInvite: permitted}, nil
}

func sharedInviteResponse(value domain.SharedInvite, err error) (*chatv1.SharedInvite, error) {
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSharedInvite(value), nil
}

func (s *Server) UserWorkspaces(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.UserWorkspacesResponse, error) {
	values, err := s.implementation.UserWorkspaces(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.WorkspaceMembershipSummary, 0, len(values))
	for _, value := range values {
		result = append(result, encodeProtoWorkspaceMembershipSummary(value))
	}
	return &chatv1.UserWorkspacesResponse{Workspaces: result}, nil
}

func (s *Server) InvitationPreview(ctx context.Context, input *chatv1.InvitationPreviewRequest) (*chatv1.InviteRequest, error) {
	value, err := s.implementation.InvitationPreview(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.InviteRequestID(input.GetInviteRequestId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoInviteRequest(value), nil
}

func (s *Server) AcceptInvitationForEmail(ctx context.Context, input *chatv1.AcceptInvitationRequest) (*chatv1.User, error) {
	user, err := s.implementation.AcceptInvitationForEmail(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetEmail(), input.GetDisplayName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(user), nil
}

func (s *Server) AdminListInviteRequests(ctx context.Context, input *chatv1.InviteRequestsRequest) (*chatv1.InviteRequestPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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

func (s *Server) AdminUninstallApps(ctx context.Context, input *chatv1.AdminUninstallAppsRequest) (*chatv1.MutationResponse, error) {
	ids := make([]domain.AppID, 0, len(input.GetAppIds()))
	for _, id := range input.GetAppIds() {
		ids = append(ids, domain.AppID(id))
	}
	if err := s.implementation.AdminUninstallApps(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminCancelAppRequest(ctx context.Context, input *chatv1.AppApprovalMutationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminCancelAppRequest(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.AppRequestID(input.GetRequestId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
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
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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
	value, err := s.implementation.LookupAppToken(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppTokenRecord{AppId: string(value.AppID), Scopes: value.Scopes, Revoked: value.Revoked}, nil
}

func (s *Server) CreateAppInstallation(ctx context.Context, input *chatv1.AppInstallationRequest) (*chatv1.AuthRevokeResponse, error) {
	value := input.GetInstallation()
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return nil, invalidArgument("installation created_at is invalid")
	}
	if err := s.implementation.CreateAppInstallation(ctx, domain.AppInstallation{AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Enabled: value.GetEnabled(), CreatedAt: created}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) ListAppInstallations(ctx context.Context, input *chatv1.AppInstallationRequest) (*chatv1.AppInstallationsResponse, error) {
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

func (s *Server) ListAppAuthorizations(ctx context.Context, input *chatv1.AppAuthorizationsRequest) (*chatv1.AppAuthorizationsResponse, error) {
	values, err := s.implementation.ListAppAuthorizations(ctx, domain.AppID(input.GetAppId()), domain.WorkspaceID(input.GetWorkspaceId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.AppAuthorizationsResponse{Authorizations: make([]*chatv1.AppAuthorization, 0, len(values))}
	for _, value := range values {
		result.Authorizations = append(result.Authorizations, &chatv1.AppAuthorization{
			AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID),
			UserId: string(value.UserID), BotId: string(value.BotID),
			TokenType: string(value.TokenType), Scopes: domain.NormalizeScopes(value.Scopes),
		})
	}
	return result, nil
}

func (s *Server) LookupSession(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.SessionRecord, error) {
	return s.lookupSessionProto(ctx, input)
}

func (s *Server) CreateSession(ctx context.Context, input *chatv1.CreateSessionRequest) (*chatv1.AuthRevokeResponse, error) {
	record, err := decodeProtoSession(input.GetSession())
	if err != nil {
		return nil, invalidArgumentFrom(err)
	}
	if err := s.implementation.CreateSession(ctx, input.GetToken(), record); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) GetAuthMethod(ctx context.Context, input *chatv1.AuthMethodRequest) (*chatv1.AuthMethod, error) {
	value, err := s.implementation.GetAuthMethod(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetProvider())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthMethod{WorkspaceId: string(value.WorkspaceID), Provider: value.Provider, Enabled: value.Enabled}, nil
}

func (s *Server) SetAuthMethod(ctx context.Context, input *chatv1.AuthMethodRequest) (*chatv1.AuthRevokeResponse, error) {
	if err := s.implementation.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: domain.WorkspaceID(input.GetWorkspaceId()), Provider: input.GetProvider(), Enabled: input.GetEnabled()}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) GetExternalIdentity(ctx context.Context, input *chatv1.ExternalIdentityRequest) (*chatv1.ExternalIdentity, error) {
	value, err := s.implementation.GetExternalIdentity(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetProvider(), input.GetSubject())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ExternalIdentity{WorkspaceId: string(value.WorkspaceID), Provider: value.Provider, Subject: value.Subject, UserId: string(value.UserID)}, nil
}

func (s *Server) CreateExternalIdentity(ctx context.Context, input *chatv1.ExternalIdentityRequest) (*chatv1.AuthRevokeResponse, error) {
	if err := s.implementation.CreateExternalIdentity(ctx, domain.ExternalIdentity{WorkspaceID: domain.WorkspaceID(input.GetWorkspaceId()), Provider: input.GetProvider(), Subject: input.GetSubject(), UserID: domain.UserID(input.GetUserId())}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) RevokeOIDCSessions(ctx context.Context, input *chatv1.RevokeOIDCSessionsRequest) (*chatv1.AuthRevokeResponse, error) {
	expiresAt, err := time.Parse(time.RFC3339Nano, input.GetExpiresAt())
	if err != nil {
		return nil, invalidArgument("OpenID Connect logout token expiry is invalid")
	}
	if err := s.implementation.RevokeOIDCSessions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetProvider(), input.GetSubject(), input.GetSid(), input.GetTokenId(), expiresAt); err != nil {
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

func (s *Server) UninstallApp(ctx context.Context, input *chatv1.UninstallAppRequest) (*chatv1.AuthRevokeResponse, error) {
	if err := s.implementation.UninstallApp(ctx, input.GetClientId(), input.GetClientSecret(), domain.WorkspaceID(input.GetWorkspaceId()), domain.AppID(input.GetAppId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) OpenConversation(ctx context.Context, input *chatv1.OpenConversationRequest) (*chatv1.Conversation, error) {
	return s.openConversationProto(ctx, input)
}

func (s *Server) AddPeopleToDirectConversation(ctx context.Context, input *chatv1.AddPeopleToDirectConversationRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.AddPeopleToDirectConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), protoUserIDs(input.GetUsers()), domain.DirectHistorySelection(input.GetHistory()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) ConvertGroupDirectToPrivate(ctx context.Context, input *chatv1.ConvertGroupDirectToPrivateRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.ConvertGroupDirectToPrivate(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
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

func (s *Server) ConversationMemberCount(ctx context.Context, input *chatv1.ConversationMembersRequest) (*chatv1.MemberCountResponse, error) {
	count, err := s.implementation.ConversationMemberCount(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MemberCountResponse{Count: int32(count)}, nil
}

func (s *Server) ConversationMembers(ctx context.Context, input *chatv1.ConversationMembersRequest) (*chatv1.UserPage, error) {
	return s.conversationMembersProto(ctx, input)
}

func (s *Server) WorkspaceInfo(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.Workspace, error) {
	return s.workspaceInfoProto(ctx, input)
}

func (s *Server) AuthorizedAppWorkspaces(ctx context.Context, input *chatv1.AuthorizedAppWorkspacesRequest) (*chatv1.WorkspacePage, error) {
	page, err := s.implementation.AuthorizedAppWorkspaces(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), domain.PageRequest{Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor()), Descending: input.GetDescending()},
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspacePage(page), nil
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
	encodedErrors, _ := json.Marshal(value.Errors)
	return &chatv1.View{Id: string(value.ID), AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Type: value.Type, ExternalId: value.ExternalID, Payload: value.Payload, StateJson: value.State, ErrorsJson: string(encodedErrors), Hash: value.Hash, RootViewId: string(value.RootViewID), PreviousViewId: string(value.PreviousViewID), CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt)}
}

func (s *Server) OpenView(ctx context.Context, input *chatv1.OpenViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.OpenView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), input.GetTriggerId(), input.GetPayload())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) PublishView(ctx context.Context, input *chatv1.PublishViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.PublishView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.UserID(input.GetTargetUserId()), input.GetPayload(), input.GetHash())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) AppHome(ctx context.Context, input *chatv1.AppHomeRequest) (*chatv1.AppHomeResponse, error) {
	app, view, err := s.implementation.AppHome(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppHome(app, view), nil
}

func (s *Server) OpenAppHome(ctx context.Context, input *chatv1.AppHomeRequest) (*chatv1.AppHomeResponse, error) {
	app, view, err := s.implementation.OpenAppHome(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppHome(app, view), nil
}

func encodeProtoAppHome(app domain.InstalledApp, view domain.View) *chatv1.AppHomeResponse {
	result := &chatv1.AppHomeResponse{App: encodeProtoInstalledApp(app), Published: view.ID != ""}
	if result.Published {
		result.View = encodeProtoView(view)
	}
	return result
}

func (s *Server) PushView(ctx context.Context, input *chatv1.PushViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.PushView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), input.GetTriggerId(), input.GetPayload())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) UpdateView(ctx context.Context, input *chatv1.UpdateViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.UpdateView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), input.GetViewId(), input.GetExternalId(), input.GetPayload(), input.GetHash())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) CurrentModalView(ctx context.Context, input *chatv1.CurrentModalViewRequest) (*chatv1.View, error) {
	value, err := s.implementation.CurrentModalView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoView(value), nil
}

func (s *Server) SubmitView(ctx context.Context, input *chatv1.ViewSubmissionRequest) (*chatv1.ViewInteractionResult, error) {
	value, err := s.implementation.SubmitView(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()), domain.ViewID(input.GetViewId()),
		input.GetStateJson(), input.GetResponseBaseUrl(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	encodedErrors := ""
	if len(value.Errors) != 0 {
		encoded, err := json.Marshal(value.Errors)
		if err != nil {
			return nil, mapError(err)
		}
		encodedErrors = string(encoded)
	}
	return &chatv1.ViewInteractionResult{ErrorsJson: encodedErrors, Pending: value.Pending}, nil
}

func (s *Server) CloseView(ctx context.Context, input *chatv1.CloseViewRequest) (*chatv1.ViewMutationResponse, error) {
	if err := s.implementation.CloseView(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()), domain.ViewID(input.GetViewId()),
		input.GetClear(), input.GetResponseBaseUrl(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ViewMutationResponse{Ok: true}, nil
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

func (s *Server) CreateWorkflow(ctx context.Context, input *chatv1.WorkflowMutationRequest) (*chatv1.WorkflowDefinition, error) {
	value, err := s.implementation.CreateWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeWorkflowDefinition(input.GetWorkflow()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowDefinition(value), nil
}

func (s *Server) GetWorkflow(ctx context.Context, input *chatv1.WorkflowGetRequest) (*chatv1.WorkflowDefinition, error) {
	value, err := s.implementation.GetWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowDefinition(value), nil
}

func (s *Server) DiscardWorkflowStagedChanges(ctx context.Context, input *chatv1.WorkflowDiscardStagedRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.DiscardWorkflowStagedChanges(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), input.GetExpectedVersion(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) WorkflowStepChanges(ctx context.Context, input *chatv1.WorkflowStepChangesRequest) (*chatv1.WorkflowStepChangesResponse, error) {
	changes, err := s.implementation.WorkflowStepChanges(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepChangesResponse{Changes: encodeWorkflowStepChanges(changes)}, nil
}

func (s *Server) DuplicateWorkflow(ctx context.Context, input *chatv1.WorkflowGetRequest) (*chatv1.WorkflowDefinition, error) {
	value, err := s.implementation.DuplicateWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowDefinition(value), nil
}

func (s *Server) DeleteWorkflow(ctx context.Context, input *chatv1.WorkflowDeleteRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.DeleteWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), input.GetExpectedVersion(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) WorkflowActivity(ctx context.Context, input *chatv1.WorkflowGetRequest) (*chatv1.WorkflowActivitySummary, error) {
	activity, err := s.implementation.WorkflowActivity(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	recent := make([]*chatv1.WorkflowRun, 0, len(activity.RecentRuns))
	for _, run := range activity.RecentRuns {
		recent = append(recent, encodeWorkflowRun(run))
	}
	return &chatv1.WorkflowActivitySummary{
		Running: int32(activity.Running), Completed: int32(activity.Completed),
		Failed: int32(activity.Failed), Cancelled: int32(activity.Cancelled), RecentRuns: recent,
	}, nil
}

func (s *Server) SubmitWorkflowForm(ctx context.Context, input *chatv1.WorkflowFormSubmitRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.SubmitWorkflowForm(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowRunID(input.GetRunId()), domain.WorkflowStepID(input.GetStepId()), input.GetInputs(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) CompleteWorkflowButton(ctx context.Context, input *chatv1.WorkflowButtonRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.CompleteWorkflowButton(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowRunID(input.GetRunId()), domain.WorkflowStepID(input.GetStepId()),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) WorkflowRunInteraction(ctx context.Context, input *chatv1.WorkflowRunGetRequest) (*chatv1.WorkflowInteraction, error) {
	interaction, err := s.implementation.WorkflowRunInteraction(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowRunID(input.GetWorkflowRunId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	fields := make([]*chatv1.WorkflowInteractionField, 0, len(interaction.Fields))
	for _, field := range interaction.Fields {
		fields = append(fields, &chatv1.WorkflowInteractionField{Name: field.Name, Label: field.Label})
	}
	return &chatv1.WorkflowInteraction{
		StepId: string(interaction.StepID), Kind: string(interaction.Kind), Title: interaction.Title,
		Label: interaction.Label, Fields: fields,
	}, nil
}

func (s *Server) WorkflowRunExport(ctx context.Context, input *chatv1.WorkflowGetRequest) (*chatv1.WorkflowRunListResponse, error) {
	runs, err := s.implementation.WorkflowRunExport(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.WorkflowRunListResponse{Runs: make([]*chatv1.WorkflowRun, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, encodeWorkflowRun(run))
	}
	return out, nil
}

func (s *Server) WorkflowFormResponseExport(ctx context.Context, input *chatv1.WorkflowGetRequest) (*chatv1.WorkflowFormResponseListResponse, error) {
	responses, err := s.implementation.WorkflowFormResponseExport(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.WorkflowFormResponseListResponse{Responses: make([]*chatv1.WorkflowFormResponse, 0, len(responses))}
	for _, response := range responses {
		out.Responses = append(out.Responses, &chatv1.WorkflowFormResponse{
			RunId: string(response.RunID), WorkflowVersion: response.WorkflowVersion,
			FormTitle: response.FormTitle, Field: response.Field, Value: response.Value,
			SubmittedAtUnixNano: optionalUnixNano(response.SubmittedAt),
		})
	}
	return out, nil
}

func (s *Server) SetWorkflowManagers(ctx context.Context, input *chatv1.WorkflowManagersRequest) (*chatv1.WorkflowDefinition, error) {
	value, err := s.implementation.SetWorkflowManagers(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), decodeUserIDs(input.GetManagerIds()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowDefinition(value), nil
}

func (s *Server) CanManageWorkflow(ctx context.Context, input *chatv1.WorkflowGetRequest) (*chatv1.WorkflowManagePermissionResponse, error) {
	allowed, err := s.implementation.CanManageWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowManagePermissionResponse{CanManage: allowed}, nil
}

func (s *Server) UpdateWorkflow(ctx context.Context, input *chatv1.WorkflowMutationRequest) (*chatv1.WorkflowDefinition, error) {
	value, err := s.implementation.UpdateWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeWorkflowDefinition(input.GetWorkflow()), input.GetExpectedVersion(), input.GetPublish(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowDefinition(value), nil
}

func (s *Server) AdminWorkflows(ctx context.Context, input *chatv1.AdminWorkflowListRequest) (*chatv1.WorkflowListResponse, error) {
	values, more, next, err := s.implementation.AdminWorkflows(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery(),
		domain.PageRequest{Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor()), Descending: input.GetDescending()},
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.WorkflowListResponse{HasMore: more, NextCursor: string(next), Workflows: make([]*chatv1.WorkflowDefinition, 0, len(values))}
	for _, value := range values {
		out.Workflows = append(out.Workflows, encodeWorkflowDefinition(value))
	}
	return out, nil
}

func (s *Server) ChangeWorkflowCollaborators(ctx context.Context, input *chatv1.WorkflowCollaboratorsRequest) (*chatv1.AdminUnpublishWorkflowsResponse, error) {
	ids := make([]domain.WorkflowID, 0, len(input.GetWorkflowIds()))
	for _, id := range input.GetWorkflowIds() {
		ids = append(ids, domain.WorkflowID(id))
	}
	collaborators := make([]domain.UserID, 0, len(input.GetCollaboratorIds()))
	for _, id := range input.GetCollaboratorIds() {
		collaborators = append(collaborators, domain.UserID(id))
	}
	change := s.implementation.RemoveWorkflowCollaborators
	if input.GetAdding() {
		change = s.implementation.AddWorkflowCollaborators
	}
	if err := change(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids, collaborators); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AdminUnpublishWorkflowsResponse{Ok: true}, nil
}

func (s *Server) AdminUnpublishWorkflows(ctx context.Context, input *chatv1.AdminUnpublishWorkflowsRequest) (*chatv1.AdminUnpublishWorkflowsResponse, error) {
	ids := make([]domain.WorkflowID, 0, len(input.GetWorkflowIds()))
	for _, id := range input.GetWorkflowIds() {
		ids = append(ids, domain.WorkflowID(id))
	}
	if err := s.implementation.AdminUnpublishWorkflows(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AdminUnpublishWorkflowsResponse{Ok: true}, nil
}

func (s *Server) ListWorkflows(ctx context.Context, input *chatv1.WorkflowListRequest) (*chatv1.WorkflowListResponse, error) {
	values, more, next, err := s.implementation.ListWorkflows(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.PageRequest{Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor()), Descending: input.GetDescending()},
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.WorkflowListResponse{HasMore: more, NextCursor: string(next), Workflows: make([]*chatv1.WorkflowDefinition, 0, len(values))}
	for _, value := range values {
		out.Workflows = append(out.Workflows, encodeWorkflowDefinition(value))
	}
	return out, nil
}

func (s *Server) SetWorkflowTrigger(ctx context.Context, input *chatv1.WorkflowTriggerMutationRequest) (*chatv1.WorkflowTrigger, error) {
	value, err := s.implementation.SetWorkflowTrigger(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeWorkflowTrigger(input.GetTrigger()), input.GetExpectedVersion(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowTrigger(value), nil
}

func (s *Server) ListWorkflowTriggers(ctx context.Context, input *chatv1.WorkflowTriggerListRequest) (*chatv1.WorkflowTriggerListResponse, error) {
	values, err := s.implementation.ListWorkflowTriggers(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkflowID(input.GetWorkflowId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.WorkflowTriggerListResponse{Triggers: make([]*chatv1.WorkflowTrigger, 0, len(values))}
	for _, value := range values {
		out.Triggers = append(out.Triggers, encodeWorkflowTrigger(value))
	}
	return out, nil
}

func (s *Server) RunWorkflow(ctx context.Context, input *chatv1.WorkflowRunRequest) (*chatv1.WorkflowRun, error) {
	value, err := s.implementation.RunWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowTriggerID(input.GetTriggerId()), domain.ConversationID(input.GetChannelId()),
		input.GetInputs(), input.GetIdempotencyKey(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowRun(value), nil
}

func (s *Server) RunAutomaticWorkflow(ctx context.Context, input *chatv1.AutomaticWorkflowRunRequest) (*chatv1.WorkflowRun, error) {
	value, err := s.implementation.RunAutomaticWorkflow(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.WorkflowTriggerID(input.GetTriggerId()),
		domain.ConversationID(input.GetChannelId()), input.GetInputs(), input.GetIdempotencyKey(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowRun(value), nil
}

func (s *Server) RunWebhookTrigger(ctx context.Context, input *chatv1.WebhookWorkflowRunRequest) (*chatv1.WorkflowRun, error) {
	value, err := s.implementation.RunWebhookTrigger(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.WorkflowTriggerID(input.GetTriggerId()),
		input.GetSecret(), input.GetInputs(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowRun(value), nil
}

func (s *Server) WebhookTriggerURL(ctx context.Context, input *chatv1.WebhookTriggerURLRequest) (*chatv1.WebhookTriggerURLResponse, error) {
	value, err := s.implementation.WebhookTriggerURL(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowTriggerID(input.GetTriggerId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WebhookTriggerURLResponse{Url: value}, nil
}

func (s *Server) DispatchWorkflowEventTriggers(ctx context.Context, input *chatv1.WorkflowEventDispatchRequest) (*chatv1.WorkflowEventDispatchResponse, error) {
	started, err := s.implementation.DispatchWorkflowEventTriggers(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), int(input.GetLimit()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowEventDispatchResponse{Started: int32(started)}, nil
}

func (s *Server) WorkflowRunSteps(ctx context.Context, input *chatv1.WorkflowRunGetRequest) (*chatv1.WorkflowRunStepsResponse, error) {
	steps, err := s.implementation.WorkflowRunSteps(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkflowRunID(input.GetWorkflowRunId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.WorkflowStep, 0, len(steps))
	for _, step := range steps {
		encoded = append(encoded, encodeWorkflowStep(step))
	}
	return &chatv1.WorkflowRunStepsResponse{Steps: encoded}, nil
}

func (s *Server) GetWorkflowRun(ctx context.Context, input *chatv1.WorkflowRunGetRequest) (*chatv1.WorkflowRun, error) {
	value, err := s.implementation.GetWorkflowRun(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.WorkflowRunID(input.GetWorkflowRunId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeWorkflowRun(value), nil
}

func (s *Server) CompleteFunction(ctx context.Context, input *chatv1.FunctionCompletionRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	if err := s.implementation.CompleteFunction(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.WorkflowStepID(input.GetFunctionExecutionId()), input.GetOutputs(), input.GetError(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) WorkflowStepResponses(ctx context.Context, input *chatv1.WorkflowStepResponsesRequest) (*chatv1.WorkflowStepResponsesResponse, error) {
	responses, err := s.implementation.WorkflowStepResponses(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), input.GetStepId())
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.WorkflowStepResponse, 0, len(responses))
	for _, response := range responses {
		encoded = append(encoded, encodeProtoWorkflowStepResponse(response))
	}
	return &chatv1.WorkflowStepResponsesResponse{Responses: encoded}, nil
}

func (s *Server) AdminFunctionPermissions(ctx context.Context, input *chatv1.AdminPermissionsRequest) (*chatv1.AdminPermissionsResponse, error) {
	values, err := s.implementation.AdminFunctionPermissions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetResourceIds())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AdminPermissionsResponse{Permissions: encodeAutomationPermissions(values)}, nil
}

func (s *Server) AdminWorkflowPermissions(ctx context.Context, input *chatv1.AdminPermissionsRequest) (*chatv1.AdminPermissionsResponse, error) {
	ids := make([]domain.WorkflowID, 0, len(input.GetResourceIds()))
	for _, id := range input.GetResourceIds() {
		ids = append(ids, domain.WorkflowID(id))
	}
	values, err := s.implementation.AdminWorkflowPermissions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AdminPermissionsResponse{Permissions: encodeAutomationPermissions(values)}, nil
}

func (s *Server) AdminTriggerTypePermission(ctx context.Context, input *chatv1.AdminPermissionsRequest) (*chatv1.AutomationPermission, error) {
	kind := domain.WorkflowTriggerType("")
	if ids := input.GetResourceIds(); len(ids) > 0 {
		kind = domain.WorkflowTriggerType(ids[0])
	}
	value, err := s.implementation.AdminTriggerTypePermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), kind)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) AdminSetFunctionPermission(ctx context.Context, input *chatv1.AdminPermissionMutationRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.AdminSetFunctionPermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		input.GetResourceId(), decodeAutomationPermission(input.GetPermission()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) AdminSetTriggerTypePermission(ctx context.Context, input *chatv1.AdminPermissionMutationRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.AdminSetTriggerTypePermission(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowTriggerType(input.GetResourceId()), decodeAutomationPermission(input.GetPermission()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func encodeAutomationPermissions(values []domain.AutomationPermission) []*chatv1.AutomationPermission {
	encoded := make([]*chatv1.AutomationPermission, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, encodeAutomationPermission(value))
	}
	return encoded
}

func (s *Server) GetFunctionPermission(ctx context.Context, input *chatv1.FunctionPermissionRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.GetFunctionPermission(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		input.GetFunctionId(), input.GetFunctionCallbackId(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) SetFunctionPermission(ctx context.Context, input *chatv1.FunctionPermissionRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.SetFunctionPermission(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		input.GetFunctionId(), input.GetFunctionCallbackId(), decodeAutomationPermission(input.GetPermission()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) GetTriggerPermission(ctx context.Context, input *chatv1.TriggerPermissionRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.GetTriggerPermission(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.WorkflowTriggerID(input.GetTriggerId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) SetTriggerPermission(ctx context.Context, input *chatv1.TriggerPermissionRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.SetTriggerPermission(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.WorkflowTriggerID(input.GetTriggerId()), decodeAutomationPermission(input.GetPermission()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) GetWorkflowPermission(ctx context.Context, input *chatv1.WorkflowPermissionRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.GetWorkflowPermission(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), input.GetScope(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) SetWorkflowPermission(ctx context.Context, input *chatv1.WorkflowPermissionRequest) (*chatv1.AutomationPermission, error) {
	value, err := s.implementation.SetWorkflowPermission(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.WorkflowID(input.GetWorkflowId()), input.GetScope(),
		decodeAutomationPermission(input.GetPermission()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeAutomationPermission(value), nil
}

func (s *Server) SetFeaturedWorkflows(ctx context.Context, input *chatv1.FeaturedWorkflowsRequest) (*chatv1.WorkflowStepMutationResponse, error) {
	triggerIDs := make([]domain.WorkflowTriggerID, len(input.GetTriggerIds()))
	for index, id := range input.GetTriggerIds() {
		triggerIDs[index] = domain.WorkflowTriggerID(id)
	}
	if err := s.implementation.SetFeaturedWorkflows(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetChannelId()), triggerIDs,
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.WorkflowStepMutationResponse{Ok: true}, nil
}

func (s *Server) ListFeaturedWorkflows(ctx context.Context, input *chatv1.FeaturedWorkflowsRequest) (*chatv1.FeaturedWorkflowsResponse, error) {
	channelIDs := make([]domain.ConversationID, len(input.GetChannelIds()))
	for index, id := range input.GetChannelIds() {
		channelIDs[index] = domain.ConversationID(id)
	}
	values, err := s.implementation.ListFeaturedWorkflows(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), channelIDs,
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.FeaturedWorkflowsResponse{Workflows: make([]*chatv1.FeaturedWorkflow, 0, len(values))}
	for _, value := range values {
		out.Workflows = append(out.Workflows, &chatv1.FeaturedWorkflow{
			WorkspaceId: string(value.WorkspaceID), ChannelId: string(value.ConversationID),
			TriggerId: string(value.TriggerID), Title: value.Title, Position: int32(value.Position),
		})
	}
	return out, nil
}

func (s *Server) ListFunctionWorkflowSteps(ctx context.Context, input *chatv1.FunctionWorkflowStepsRequest) (*chatv1.WorkflowStepVersionsResponse, error) {
	values, err := s.implementation.ListFunctionWorkflowSteps(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		input.GetFunctionId(), domain.WorkflowID(input.GetWorkflowId()), input.GetWorkflow(), domain.AppID(input.GetWorkflowAppId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.WorkflowStepVersionsResponse{StepsVersions: make([]*chatv1.WorkflowStepVersion, 0, len(values))}
	for _, value := range values {
		out.StepsVersions = append(out.StepsVersions, &chatv1.WorkflowStepVersion{
			Title: value.Title, WorkflowId: string(value.WorkflowID), StepId: value.StepID,
			IsDeleted: value.IsDeleted, WorkflowVersionCreated: value.WorkflowVersionCreated,
		})
	}
	return out, nil
}

func (s *Server) OpenDialog(ctx context.Context, input *chatv1.OpenDialogRequest) (*chatv1.DialogMutationResponse, error) {
	if err := s.implementation.OpenDialog(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), input.GetTriggerId(), input.GetPayload()); err != nil {
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
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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
	return encodeOAuthToken(value), nil
}

func (s *Server) ExchangeOAuthV2(ctx context.Context, input *chatv1.OAuthExchangeRequest) (*chatv1.OAuthToken, error) {
	value, err := s.implementation.OAuthV2Exchange(ctx, input.GetClientId(), input.GetClientSecret(), input.GetCode(), input.GetRedirectUri(), input.GetUserOnly())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeOAuthToken(value), nil
}

func (s *Server) RefreshOAuthV2(ctx context.Context, input *chatv1.OAuthExchangeRequest) (*chatv1.OAuthToken, error) {
	value, err := s.implementation.OAuthV2Refresh(ctx, input.GetClientId(), input.GetClientSecret(), input.GetRefreshToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeOAuthToken(value), nil
}

func (s *Server) ExchangeOAuthV2Token(ctx context.Context, input *chatv1.OAuthExchangeRequest) (*chatv1.OAuthToken, error) {
	value, err := s.implementation.OAuthV2ExchangeToken(ctx, input.GetClientId(), input.GetClientSecret(), input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeOAuthToken(value), nil
}

func encodeOAuthToken(value domain.OAuthToken) *chatv1.OAuthToken {
	token := &chatv1.OAuthToken{
		AccessToken:            value.AccessToken,
		ClientId:               value.ClientID,
		AppId:                  string(value.AppID),
		WorkspaceId:            string(value.WorkspaceID),
		UserId:                 string(value.UserID),
		InstallerId:            string(value.InstallerID),
		BotId:                  string(value.BotID),
		Scopes:                 value.Scopes,
		TokenType:              string(value.TokenType),
		AuthedUserAccessToken:  value.AuthedUserAccessToken,
		AuthedUserScopes:       value.AuthedUserScopes,
		RefreshToken:           value.RefreshToken,
		AuthedUserRefreshToken: value.AuthedUserRefreshToken,
	}
	if !value.ExpiresAt.IsZero() {
		token.ExpiresAtUnixNano = value.ExpiresAt.UTC().UnixNano()
	}
	if !value.AuthedUserExpiresAt.IsZero() {
		token.AuthedUserExpiresAtUnixNano = value.AuthedUserExpiresAt.UTC().UnixNano()
	}
	return token
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

func (s *Server) ShareFile(ctx context.Context, input *chatv1.ShareFileRequest) (*chatv1.Message, error) {
	value, err := s.implementation.ShareFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) Unfurl(ctx context.Context, input *chatv1.UnfurlRequest) (*chatv1.Message, error) {
	value, err := s.implementation.Unfurl(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetUnfurls())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) PostEphemeral(ctx context.Context, input *chatv1.PostEphemeralRequest) (*chatv1.EphemeralMessage, error) {
	value, err := s.implementation.PostEphemeralWithBlocksAndAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.UserID(input.GetRecipientId()), input.GetText(), input.GetBlocks(), input.GetAttachments(), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoEphemeralMessage(value), nil
}

func (s *Server) ListEphemeral(ctx context.Context, input *chatv1.EphemeralMessagesRequest) (*chatv1.EphemeralMessagesResponse, error) {
	values, err := s.implementation.ListEphemeralMessages(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), seamPage(int(input.GetLimit())))
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.EphemeralMessage, 0, len(values))
	for _, value := range values {
		result = append(result, encodeProtoEphemeralMessage(value))
	}
	return &chatv1.EphemeralMessagesResponse{Messages: result}, nil
}

// GetWorkspaceAnalytics is the transport name; see the comment on the rpc.
func (s *Server) GetWorkspaceAnalytics(ctx context.Context, input *chatv1.WorkspaceAnalyticsRequest) (*chatv1.WorkspaceAnalytics, error) {
	since := time.Time{}
	if input.Since != nil {
		since = time.Unix(input.GetSince(), 0).UTC()
	}
	value, err := s.implementation.WorkspaceAnalytics(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), since)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspaceAnalytics(value), nil
}

func (s *Server) RecordAccess(ctx context.Context, input *chatv1.RecordAccessRequest) (*chatv1.AccessMutationResponse, error) {
	if err := s.implementation.RecordAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetIp(), input.GetUserAgent()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AccessMutationResponse{Ok: true}, nil
}
func (s *Server) AccessLogs(ctx context.Context, input *chatv1.AccessLogsRequest) (*chatv1.AccessLogsResponse, error) {
	var before time.Time
	if input.Before != nil {
		before = time.Unix(input.GetBefore(), 0).UTC()
	}
	values, hasMore, err := s.implementation.ListAccessLogs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), before, seamPage(int(input.GetLimit())), int(input.GetPage()))
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
	value := domain.SocketModeConnection{ID: input.GetId(), AppID: domain.AppID(input.GetAppId()), ExpiresAt: time.Unix(0, input.GetExpiresAtUnixNano()).UTC()}
	if err := s.implementation.CreateSocketModeConnection(ctx, value); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: value.ID, AppId: string(value.AppID), ExpiresAtUnixNano: value.ExpiresAt.UnixNano()}, nil
}

func (s *Server) ConsumeSocketModeConnection(ctx context.Context, input *chatv1.RTMConnectionIDRequest) (*chatv1.SocketModeConnection, error) {
	value, err := s.implementation.ConsumeSocketModeConnection(ctx, input.GetId())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: value.ID, AppId: string(value.AppID), ExpiresAtUnixNano: value.ExpiresAt.UnixNano()}, nil
}

func (s *Server) RenewSocketModeConnection(ctx context.Context, input *chatv1.SocketModeConnectionRenewalRequest) (*chatv1.SocketModeConnection, error) {
	if err := s.implementation.RenewSocketModeConnection(ctx, input.GetId(), time.Unix(0, input.GetExpiresAtUnixNano()).UTC()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: input.GetId(), ExpiresAtUnixNano: input.GetExpiresAtUnixNano()}, nil
}

func (s *Server) ReleaseSocketModeConnection(ctx context.Context, input *chatv1.RTMConnectionIDRequest) (*chatv1.SocketModeConnection, error) {
	if err := s.implementation.ReleaseSocketModeConnection(ctx, input.GetId()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnection{Id: input.GetId()}, nil
}

func (s *Server) CountSocketModeConnections(ctx context.Context, input *chatv1.SocketModeCursorRequest) (*chatv1.SocketModeConnectionCount, error) {
	count, err := s.implementation.CountSocketModeConnections(ctx, domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeConnectionCount{AppId: input.GetAppId(), Count: int32(count)}, nil
}

func (s *Server) GetSocketModeCursor(ctx context.Context, input *chatv1.SocketModeCursorRequest) (*chatv1.SocketModeCursor, error) {
	cursor, err := s.implementation.GetSocketModeCursor(ctx, domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeCursor{AppId: input.GetAppId(), Sequence: cursor}, nil
}

func (s *Server) SetSocketModeCursor(ctx context.Context, input *chatv1.SocketModeCursorRequest) (*chatv1.SocketModeCursor, error) {
	if err := s.implementation.SetSocketModeCursor(ctx, domain.AppID(input.GetAppId()), input.GetSequence()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeCursor{AppId: input.GetAppId(), Sequence: input.GetSequence()}, nil
}

func (s *Server) RecordSocketModeResponse(ctx context.Context, input *chatv1.SocketModeResponseRequest) (*chatv1.SocketModeResponse, error) {
	value := domain.SocketModeResponse{AppID: domain.AppID(input.GetAppId()), EnvelopeID: input.GetEnvelopeId(), Payload: input.GetPayload(), ReceivedAt: time.Unix(0, input.GetReceivedAtUnixNano()).UTC()}
	if err := s.implementation.RecordSocketModeResponse(ctx, value); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponse{AppId: string(value.AppID), EnvelopeId: value.EnvelopeID, Payload: value.Payload, ReceivedAtUnixNano: value.ReceivedAt.UnixNano()}, nil
}

func (s *Server) ClaimSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseLeaseRequest) (*chatv1.SocketModeResponseBatch, error) {
	values, err := s.implementation.ClaimSocketModeResponses(ctx, domain.AppID(input.GetAppId()), input.GetOwner(), seamPage(int(input.GetLimit())), time.Duration(input.GetLeaseNanos()))
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
	values, err := socketModeResponseKeys(input.GetResponses())
	if err != nil {
		return nil, invalidArgumentFrom(err)
	}
	if err := s.implementation.RenewSocketModeResponses(ctx, input.GetOwner(), values, time.Duration(input.GetLeaseNanos())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponseBatch{}, nil
}

func (s *Server) AckSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseAckRequest) (*chatv1.SocketModeResponseBatch, error) {
	values, err := socketModeResponseKeys(input.GetResponses())
	if err != nil {
		return nil, invalidArgumentFrom(err)
	}
	if err := s.implementation.AckSocketModeResponses(ctx, input.GetOwner(), values); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SocketModeResponseBatch{}, nil
}

func (s *Server) ReleaseSocketModeResponses(ctx context.Context, input *chatv1.SocketModeResponseReleaseRequest) (*chatv1.SocketModeResponseBatch, error) {
	values, err := socketModeResponseKeys(input.GetResponses())
	if err != nil {
		return nil, invalidArgumentFrom(err)
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

func (s *Server) RecordSearch(ctx context.Context, input *chatv1.RecordSearchRequest) (*chatv1.SearchHistoryResponse, error) {
	if err := s.implementation.RecordSearch(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetQuery()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SearchHistoryResponse{Ok: true}, nil
}

func (s *Server) RecentSearches(ctx context.Context, input *chatv1.RecentSearchesRequest) (*chatv1.SearchHistoryResponse, error) {
	values, err := s.implementation.RecentSearches(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), int(input.GetLimit()))
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.SearchHistoryResponse{Ok: true, Entries: make([]*chatv1.SearchHistoryEntry, 0, len(values))}
	for _, value := range values {
		out.Entries = append(out.Entries, &chatv1.SearchHistoryEntry{
			WorkspaceId: string(value.WorkspaceID),
			UserId:      string(value.UserID),
			Query:       value.Query,
			SearchedAt:  string(domain.NewStoredTime(value.SearchedAt)),
		})
	}
	return out, nil
}

func (s *Server) CreateList(ctx context.Context, input *chatv1.CreateListRequest) (*chatv1.ListResponse, error) {
	value, err := s.implementation.CreateList(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetDescriptionBlocks(), input.GetSchema(), domain.ListID(input.GetCopyFromListId()), input.GetIncludeCopiedRecords(), input.GetTodoMode())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListResponse{Ok: true, List: encodeProtoList(value)}, nil
}

func (s *Server) GetList(ctx context.Context, input *chatv1.ListItemRequest) (*chatv1.ListResponse, error) {
	value, err := s.implementation.List(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListResponse{Ok: true, List: encodeProtoList(value)}, nil
}

func (s *Server) GetListAccess(ctx context.Context, input *chatv1.ListItemRequest) (*chatv1.ListAccessResponse, error) {
	value, err := s.implementation.ListAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListGrant(value), nil
}

func (s *Server) ListGrants(ctx context.Context, input *chatv1.ListItemRequest) (*chatv1.ListGrantsResponse, error) {
	grants, err := s.implementation.ListGrants(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ListGrantsResponse{Grants: encodeProtoListGrants(grants)}, nil
}

func encodeProtoListGrant(value domain.ListAccess) *chatv1.ListAccessResponse {
	return &chatv1.ListAccessResponse{ListId: string(value.ListID), EntityType: string(value.EntityType), EntityId: value.EntityID, Access: string(value.Access)}
}

func decodeProtoListGrant(value *chatv1.ListAccessResponse) domain.ListAccess {
	if value == nil {
		return domain.ListAccess{}
	}
	return domain.ListAccess{ListID: domain.ListID(value.GetListId()), EntityType: domain.GrantEntity(value.GetEntityType()), EntityID: value.GetEntityId(), Access: domain.AccessLevel(value.GetAccess())}
}

func encodeProtoListGrants(values []domain.ListAccess) []*chatv1.ListAccessResponse {
	grants := make([]*chatv1.ListAccessResponse, 0, len(values))
	for _, value := range values {
		grants = append(grants, encodeProtoListGrant(value))
	}
	return grants
}

func decodeProtoListGrants(values []*chatv1.ListAccessResponse) []domain.ListAccess {
	grants := make([]domain.ListAccess, 0, len(values))
	for _, value := range values {
		grants = append(grants, decodeProtoListGrant(value))
	}
	return grants
}

func (s *Server) ListLists(ctx context.Context, input *chatv1.ListsRequest) (*chatv1.ListPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	request.Descending = input.GetDescending()
	value, err := s.implementation.Lists(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListPage(value), nil
}

func (s *Server) SearchLists(ctx context.Context, input *chatv1.SearchListsRequest) (*chatv1.ListPage, error) {
	value, err := s.implementation.SearchLists(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListSearchRequest{
		Query:     input.GetQuery(),
		Sort:      domain.SearchSort(input.GetSort()),
		Direction: domain.SearchDirection(input.GetDirection()),
		Page:      protoPageRequest(input.GetLimit(), input.GetCursor()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListPage(value), nil
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
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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

func (s *Server) AddListColumn(ctx context.Context, input *chatv1.AddListColumnRequest) (*chatv1.List, error) {
	value, err := s.implementation.AddListColumn(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), input.GetName(), domain.ListColumnType(input.GetType()), input.GetOptions())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoList(value), nil
}

func (s *Server) RemoveListColumn(ctx context.Context, input *chatv1.RemoveListColumnRequest) (*chatv1.List, error) {
	value, err := s.implementation.RemoveListColumn(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), input.GetKey())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoList(value), nil
}

func (s *Server) AssignListItem(ctx context.Context, input *chatv1.AssignListItemRequest) (*chatv1.ListItem, error) {
	value, err := s.implementation.AssignListItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.ListItemID(input.GetItemId()), domain.UserID(input.GetAssigneeId()), optionalTimeFromUnixNano(input.GetDueAtUnixNano()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoListItem(value), nil
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
	if err := s.implementation.SetListAccess(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ListID(input.GetListId()), domain.AccessLevel(input.GetAccess()), conversationIDs(input.GetChannelIds()), userIDs(input.GetUserIds())); err != nil {
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

func (s *Server) SetFileDescription(ctx context.Context, input *chatv1.SetFileDescriptionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetFileDescription(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()), input.GetDescription()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
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

func (s *Server) SearchFiles(ctx context.Context, input *chatv1.SearchFilesRequest) (*chatv1.FilePage, error) {
	page, err := s.implementation.SearchFiles(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileSearchRequest{
		Query: input.GetQuery(), Count: int(input.GetCount()), Page: int(input.GetPage()),
		Sort: domain.SearchSort(input.GetSort()), Direction: domain.SearchDirection(input.GetDirection()),
		Conversation: domain.ConversationID(input.GetConversationId()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFilePage(page), nil
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
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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
			return nil, invalidArgument("unknown remote file update field")
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

func (s *Server) MarkAllRead(ctx context.Context, input *chatv1.MarkAllReadRequest) (*chatv1.MarkAllReadResponse, error) {
	count, err := s.implementation.MarkAllRead(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MarkAllReadResponse{Conversations: int32(count)}, nil
}

func (s *Server) SetAssistantThread(ctx context.Context, input *chatv1.SetAssistantThreadRequest) (*chatv1.SetAssistantThreadResponse, error) {
	workspace := domain.WorkspaceID(input.GetWorkspaceId())
	actor := domain.UserID(input.GetUserId())
	conversation := domain.ConversationID(input.GetConversation())
	thread := domain.MessageTimestamp(input.GetThreadTs())
	var err error
	switch domain.AssistantThreadField(input.GetField()) {
	case domain.AssistantThreadTitle:
		err = s.implementation.SetAssistantThreadTitle(ctx, workspace, actor, conversation, thread, input.GetTitle())
	case domain.AssistantThreadStatus:
		err = s.implementation.SetAssistantThreadStatus(ctx, workspace, actor, conversation, thread, input.GetStatus())
	case domain.AssistantThreadPrompts:
		err = s.implementation.SetAssistantThreadSuggestedPrompts(ctx, workspace, actor, conversation, thread, input.GetPromptsTitle(), decodeProtoAssistantPrompts(input.GetPrompts()))
	default:
		return nil, mapError(errors.New("unknown assistant thread field"))
	}
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SetAssistantThreadResponse{Ok: true}, nil
}

func (s *Server) GetAssistantThread(ctx context.Context, input *chatv1.AssistantThreadRequest) (*chatv1.AssistantThread, error) {
	value, err := s.implementation.AssistantThread(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetThreadTs()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAssistantThread(value), nil
}

func (s *Server) SetTyping(ctx context.Context, input *chatv1.SetTypingRequest) (*chatv1.SetTypingResponse, error) {
	if err := s.implementation.SetTyping(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversation())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SetTypingResponse{Ok: true}, nil
}

func (s *Server) TypingSignals(ctx context.Context, input *chatv1.TypingSignalsRequest) (*chatv1.TypingSignalsResponse, error) {
	signals, err := s.implementation.TypingSignals(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.TypingSignalsResponse{Signals: encodeProtoTypingSignals(signals)}, nil
}

func encodeProtoTypingSignals(values []domain.TypingSignal) []*chatv1.TypingSignal {
	signals := make([]*chatv1.TypingSignal, 0, len(values))
	for _, signal := range values {
		signals = append(signals, &chatv1.TypingSignal{
			WorkspaceId:       string(signal.WorkspaceID),
			Conversation:      string(signal.Conversation),
			UserId:            string(signal.UserID),
			ExpiresAtUnixNano: unixNanoOrZero(signal.ExpiresAt),
		})
	}
	return signals
}

func decodeProtoTypingSignals(values []*chatv1.TypingSignal) []domain.TypingSignal {
	signals := make([]domain.TypingSignal, 0, len(values))
	for _, signal := range values {
		signals = append(signals, domain.TypingSignal{
			WorkspaceID:  domain.WorkspaceID(signal.GetWorkspaceId()),
			Conversation: domain.ConversationID(signal.GetConversation()),
			UserID:       domain.UserID(signal.GetUserId()),
			ExpiresAt:    optionalTimeFromUnixNano(signal.GetExpiresAtUnixNano()),
		})
	}
	return signals
}

func encodeProtoAssistantPrompts(values []domain.AssistantPrompt) []*chatv1.AssistantPrompt {
	prompts := make([]*chatv1.AssistantPrompt, 0, len(values))
	for _, prompt := range values {
		prompts = append(prompts, &chatv1.AssistantPrompt{Title: prompt.Title, Message: prompt.Message})
	}
	return prompts
}

func decodeProtoAssistantPrompts(values []*chatv1.AssistantPrompt) []domain.AssistantPrompt {
	prompts := make([]domain.AssistantPrompt, 0, len(values))
	for _, prompt := range values {
		prompts = append(prompts, domain.AssistantPrompt{Title: prompt.GetTitle(), Message: prompt.GetMessage()})
	}
	return prompts
}

func encodeProtoAssistantThread(value domain.AssistantThread) *chatv1.AssistantThread {
	return &chatv1.AssistantThread{
		WorkspaceId: string(value.WorkspaceID), Conversation: string(value.Conversation),
		ThreadTs: string(value.ThreadTimestamp), Title: value.Title, Status: value.Status,
		PromptsTitle: value.PromptsTitle, Prompts: encodeProtoAssistantPrompts(value.Prompts),
		UpdatedAtUnixNano: unixNanoOrZero(value.UpdatedAt),
	}
}

func decodeProtoAssistantThread(value *chatv1.AssistantThread) (domain.AssistantThread, error) {
	return domain.AssistantThread{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversation()),
		ThreadTimestamp: domain.MessageTimestamp(value.GetThreadTs()), Title: value.GetTitle(), Status: value.GetStatus(),
		PromptsTitle: value.GetPromptsTitle(), Prompts: decodeProtoAssistantPrompts(value.GetPrompts()),
		UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}, nil
}

func (s *Server) ResumeWorkflowDelays(ctx context.Context, input *chatv1.ResumeWorkflowDelaysRequest) (*chatv1.ResumeWorkflowDelaysResponse, error) {
	resumed, err := s.implementation.ResumeWorkflowDelays(ctx, domain.WorkspaceID(input.GetWorkspaceId()), time.Unix(0, input.GetNowUnixNano()).UTC(), int(input.GetLimit()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ResumeWorkflowDelaysResponse{Resumed: int32(resumed)}, nil
}

func (s *Server) RecordActivity(ctx context.Context, input *chatv1.RecordActivityRequest) (*chatv1.RecordActivityResponse, error) {
	if err := s.implementation.RecordActivity(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.RecordActivityResponse{Ok: true}, nil
}

func (s *Server) ListFollowedThreads(ctx context.Context, input *chatv1.FollowedThreadsRequest) (*chatv1.FollowedThreadPage, error) {
	page, err := s.implementation.FollowedThreads(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.PageRequest{Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor())})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFollowedThreadPage(page), nil
}

func encodeProtoFollowedThreadPage(page domain.FollowedThreadPage) *chatv1.FollowedThreadPage {
	threads := make([]*chatv1.FollowedThread, 0, len(page.Threads))
	for _, thread := range page.Threads {
		threads = append(threads, &chatv1.FollowedThread{
			Conversation: string(thread.Conversation), ConversationName: thread.ConversationName,
			Root: string(thread.Root), RootText: thread.RootText, RootAuthorId: string(thread.RootAuthorID),
			ReplyCount: int32(thread.ReplyCount), UnreadReplies: int32(thread.UnreadReplies),
			LastReplyAtUnixNano: optionalUnixNano(thread.LastReplyAt),
		})
	}
	return &chatv1.FollowedThreadPage{Threads: threads, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func decodeProtoFollowedThreadPage(page *chatv1.FollowedThreadPage) (domain.FollowedThreadPage, error) {
	threads := make([]domain.FollowedThread, 0, len(page.GetThreads()))
	for _, thread := range page.GetThreads() {
		threads = append(threads, domain.FollowedThread{
			Conversation: domain.ConversationID(thread.GetConversation()), ConversationName: thread.GetConversationName(),
			Root: domain.MessageTimestamp(thread.GetRoot()), RootText: thread.GetRootText(),
			RootAuthorID: domain.UserID(thread.GetRootAuthorId()),
			ReplyCount:   int(thread.GetReplyCount()), UnreadReplies: int(thread.GetUnreadReplies()),
			LastReplyAt: optionalTimeFromUnixNano(thread.GetLastReplyAtUnixNano()),
		})
	}
	return domain.FollowedThreadPage{Threads: threads, NextCursor: domain.Cursor(page.GetNextCursor()), HasMore: page.GetHasMore()}, nil
}

func (s *Server) GetReadCursor(ctx context.Context, input *chatv1.ReadCursorRequest) (*chatv1.ReadCursor, error) {
	cursor, err := s.implementation.ReadCursor(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReadCursor(cursor), nil
}

func (s *Server) MessageAt(ctx context.Context, input *chatv1.MessageAtRequest) (*chatv1.Message, error) {
	message, err := s.implementation.MessageAt(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(message), nil
}

func (s *Server) ThreadSummaries(ctx context.Context, input *chatv1.ThreadSummariesRequest) (*chatv1.ThreadSummariesResponse, error) {
	roots := make([]domain.MessageTimestamp, 0, len(input.GetThreadTimestamps()))
	for _, root := range input.GetThreadTimestamps() {
		roots = append(roots, domain.MessageTimestamp(root))
	}
	summaries, err := s.implementation.ThreadSummaries(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), roots)
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make(map[string]*chatv1.ThreadSummary, len(summaries))
	for root, summary := range summaries {
		encoded[string(root)] = encodeProtoThreadSummary(summary)
	}
	return &chatv1.ThreadSummariesResponse{Summaries: encoded}, nil
}

func (s *Server) DispatchSlashCommand(ctx context.Context, input *chatv1.SlashCommandRequest) (*chatv1.InteractionMutationResponse, error) {
	if err := s.implementation.DispatchSlashCommand(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTimestamp()), input.GetCommand(), input.GetText(), input.GetResponseBaseUrl()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) DispatchBlockAction(ctx context.Context, input *chatv1.BlockActionRequest) (*chatv1.InteractionMutationResponse, error) {
	action := domain.AppBlockAction{
		MessageID: domain.MessageID(input.GetMessageId()), BlockID: input.GetBlockId(),
		ActionID: input.GetActionId(), Type: input.GetActionType(), Value: input.GetValue(),
	}
	if err := s.implementation.DispatchBlockAction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), action, input.GetResponseBaseUrl()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) DispatchViewBlockAction(ctx context.Context, input *chatv1.ViewBlockActionRequest) (*chatv1.InteractionMutationResponse, error) {
	action := domain.AppViewBlockAction{
		ViewID: domain.ViewID(input.GetViewId()), BlockID: input.GetBlockId(),
		ActionID: input.GetActionId(), Type: input.GetActionType(),
		Value: input.GetValue(), State: input.GetStateJson(),
	}
	if err := s.implementation.DispatchViewBlockAction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), action, input.GetResponseBaseUrl()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) LoadAppOptions(ctx context.Context, input *chatv1.AppOptionQueryRequest) (*chatv1.AppOptionListResponse, error) {
	options, err := s.implementation.LoadAppOptions(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()),
		domain.AppOptionQuery{
			AppID: domain.AppID(input.GetAppId()), MessageID: domain.MessageID(input.GetMessageId()), ViewID: domain.ViewID(input.GetViewId()),
			BlockID: input.GetBlockId(), ActionID: input.GetActionId(), Value: input.GetValue(),
		},
		input.GetResponseBaseUrl(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.AppOptionListResponse{Options: make([]*chatv1.AppOption, 0, len(options))}
	for _, option := range options {
		out.Options = append(out.Options, &chatv1.AppOption{
			Text: option.Text, Value: option.Value, Description: option.Description, Group: option.Group,
		})
	}
	return out, nil
}

func (s *Server) ListAppShortcuts(ctx context.Context, input *chatv1.AppShortcutListRequest) (*chatv1.AppShortcutListResponse, error) {
	values, err := s.implementation.ListAppShortcuts(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetType())
	if err != nil {
		return nil, mapError(err)
	}
	out := &chatv1.AppShortcutListResponse{Shortcuts: make([]*chatv1.AppShortcut, 0, len(values))}
	for _, value := range values {
		out.Shortcuts = append(out.Shortcuts, &chatv1.AppShortcut{
			AppId: string(value.AppID), AppName: value.AppName, Name: value.Name,
			CallbackId: value.CallbackID, Description: value.Description, Type: value.Type,
			Command: value.Command, UsageHint: value.UsageHint, ShouldEscape: value.ShouldEscape,
		})
	}
	return out, nil
}

func (s *Server) DispatchAppShortcut(ctx context.Context, input *chatv1.AppShortcutDispatchRequest) (*chatv1.InteractionMutationResponse, error) {
	if err := s.implementation.DispatchAppShortcut(ctx,
		domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()),
		domain.AppID(input.GetAppId()), input.GetCallbackId(), domain.MessageID(input.GetMessageId()), input.GetResponseBaseUrl(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) HandleAppResponse(ctx context.Context, input *chatv1.AppResponseRequest) (*chatv1.InteractionMutationResponse, error) {
	if err := s.implementation.HandleAppResponse(ctx, input.GetToken(), input.GetPayload()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) ClaimSocketModeInteraction(ctx context.Context, input *chatv1.SocketModeInteractionClaimRequest) (*chatv1.SocketModeInteraction, error) {
	value, found, err := s.implementation.ClaimSocketModeInteraction(ctx, domain.AppID(input.GetAppId()), input.GetOwner(), time.Duration(input.GetLeaseNanos()))
	if err != nil {
		return nil, mapError(err)
	}
	if !found {
		return &chatv1.SocketModeInteraction{Found: false}, nil
	}
	return encodeProtoSocketModeInteraction(value, true), nil
}

func (s *Server) AckSocketModeInteraction(ctx context.Context, input *chatv1.SocketModeInteractionAckRequest) (*chatv1.InteractionMutationResponse, error) {
	if err := s.implementation.AckSocketModeInteraction(ctx, domain.AppID(input.GetAppId()), input.GetEnvelopeId(), input.GetOwner()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) ReleaseSocketModeInteraction(ctx context.Context, input *chatv1.SocketModeInteractionReleaseRequest) (*chatv1.InteractionMutationResponse, error) {
	retryAt, err := domain.ParseStoredTime(input.GetRetryAt())
	if err != nil {
		return nil, mapError(storepkg.InvalidArgument("invalid Socket Mode interaction retry instant"))
	}
	if err := s.implementation.ReleaseSocketModeInteraction(ctx, domain.AppID(input.GetAppId()), input.GetEnvelopeId(), input.GetOwner(), input.GetReason(), retryAt); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func (s *Server) HandleSocketModeResponse(ctx context.Context, input *chatv1.SocketModeInteractionResponseRequest) (*chatv1.InteractionMutationResponse, error) {
	if err := s.implementation.HandleSocketModeResponse(ctx, domain.AppID(input.GetAppId()), input.GetEnvelopeId(), input.GetPayload()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.InteractionMutationResponse{Ok: true}, nil
}

func encodeProtoSocketModeInteraction(value domain.SocketModeInteraction, found bool) *chatv1.SocketModeInteraction {
	return &chatv1.SocketModeInteraction{
		EnvelopeId: value.EnvelopeID, AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		Type: value.Type, Payload: value.Payload, ResponseTokenHash: value.Response.TokenHash,
		ResponseConversationId: string(value.Response.ConversationID), ResponseOriginalMessageId: string(value.Response.OriginalMessageID),
		ResponseThreadTimestamp: string(value.Response.ThreadTimestamp), ResponseCreatedAt: string(domain.NewStoredTime(value.Response.CreatedAt)),
		ResponseExpiresAt: string(domain.NewStoredTime(value.Response.ExpiresAt)), ResponseUsesRemaining: int32(value.Response.UsesRemaining),
		ResponseAppId: string(value.Response.AppID), ResponseWorkspaceId: string(value.Response.WorkspaceID), ResponseUserId: string(value.Response.UserID),
		CreatedAt: string(domain.NewStoredTime(value.CreatedAt)), RetryCount: int32(value.RetryCount), RetryReason: value.RetryReason, Found: found,
		LeaseOwner: value.LeaseOwner, LeaseExpiresAt: string(domain.NewStoredTime(value.LeaseExpiresAt)),
		RetryAt: string(domain.NewStoredTime(value.RetryAt)), AcknowledgedAt: string(domain.NewStoredTime(value.AcknowledgedAt)),
	}
}

func decodeProtoSocketModeInteraction(value *chatv1.SocketModeInteraction) (domain.SocketModeInteraction, error) {
	if value == nil || value.GetEnvelopeId() == "" || value.GetAppId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" ||
		value.GetPayload() == "" || value.GetResponseTokenHash() == "" || value.GetResponseConversationId() == "" {
		return domain.SocketModeInteraction{}, errors.New("typed Socket Mode interaction is incomplete")
	}
	createdAt, err := domain.ParseStoredTime(value.GetCreatedAt())
	if err != nil {
		return domain.SocketModeInteraction{}, err
	}
	responseCreatedAt, err := domain.ParseStoredTime(value.GetResponseCreatedAt())
	if err != nil {
		return domain.SocketModeInteraction{}, err
	}
	responseExpiresAt, err := domain.ParseStoredTime(value.GetResponseExpiresAt())
	if err != nil {
		return domain.SocketModeInteraction{}, err
	}
	leaseExpiresAt, err := domain.ParseStoredTime(value.GetLeaseExpiresAt())
	if err != nil {
		return domain.SocketModeInteraction{}, err
	}
	retryAt, err := domain.ParseStoredTime(value.GetRetryAt())
	if err != nil {
		return domain.SocketModeInteraction{}, err
	}
	acknowledgedAt, err := domain.ParseStoredTime(value.GetAcknowledgedAt())
	if err != nil {
		return domain.SocketModeInteraction{}, err
	}
	return domain.SocketModeInteraction{
		EnvelopeID: value.GetEnvelopeId(), AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		UserID: domain.UserID(value.GetUserId()), Type: value.GetType(), Payload: value.GetPayload(), CreatedAt: createdAt,
		LeaseOwner: value.GetLeaseOwner(), LeaseExpiresAt: leaseExpiresAt, RetryAt: retryAt,
		RetryCount: int(value.GetRetryCount()), RetryReason: value.GetRetryReason(), AcknowledgedAt: acknowledgedAt,
		Response: domain.AppResponseURL{
			TokenHash: value.GetResponseTokenHash(), AppID: domain.AppID(value.GetResponseAppId()), WorkspaceID: domain.WorkspaceID(value.GetResponseWorkspaceId()),
			UserID: domain.UserID(value.GetResponseUserId()), ConversationID: domain.ConversationID(value.GetResponseConversationId()),
			OriginalMessageID: domain.MessageID(value.GetResponseOriginalMessageId()), ThreadTimestamp: domain.MessageTimestamp(value.GetResponseThreadTimestamp()),
			CreatedAt: responseCreatedAt, ExpiresAt: responseExpiresAt, UsesRemaining: int(value.GetResponseUsesRemaining()),
		},
	}, nil
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

func (s *Server) SaveForLater(ctx context.Context, input *chatv1.SaveForLaterRequest) (*chatv1.SavedItem, error) {
	item, err := s.implementation.SaveForLater(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSavedItem(item), nil
}

func (s *Server) SavedItemForMessage(ctx context.Context, input *chatv1.SavedItemForMessageRequest) (*chatv1.SavedItem, error) {
	item, err := s.implementation.SavedItemForMessage(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.MessageID(input.GetMessageId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSavedItem(item), nil
}

func (s *Server) SavedItemsForMessages(ctx context.Context, input *chatv1.SavedItemsForMessagesRequest) (*chatv1.SavedItemsForMessagesResponse, error) {
	ids := make([]domain.MessageID, 0, len(input.GetMessageIds()))
	for _, id := range input.GetMessageIds() {
		ids = append(ids, domain.MessageID(id))
	}
	items, err := s.implementation.SavedItemsForMessages(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*chatv1.SavedItem, 0, len(items))
	for _, item := range items {
		result = append(result, encodeProtoSavedItem(item))
	}
	return &chatv1.SavedItemsForMessagesResponse{Items: result}, nil
}

func (s *Server) SavedItems(ctx context.Context, input *chatv1.SavedItemsRequest) (*chatv1.SavedItemPage, error) {
	page, err := s.implementation.SavedItems(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SavedItemState(input.GetState()), protoDirectionalPageRequest(input.GetLimit(), input.GetCursor(), input.GetDescending()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSavedItemPage(page), nil
}

func (s *Server) SetSavedItemState(ctx context.Context, input *chatv1.SetSavedItemStateRequest) (*chatv1.SavedItem, error) {
	item, err := s.implementation.SetSavedItemState(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SavedItemID(input.GetSavedItemId()), domain.SavedItemState(input.GetState()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSavedItem(item), nil
}

func (s *Server) RemoveSavedItem(ctx context.Context, input *chatv1.RemoveSavedItemRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RemoveSavedItem(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SavedItemID(input.GetSavedItemId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
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

func (s *Server) CreateLaterReminder(ctx context.Context, input *chatv1.CreateLaterReminderRequest) (*chatv1.LaterReminder, error) {
	return s.createLaterReminderProto(ctx, input)
}

func (s *Server) LaterReminderInfo(ctx context.Context, input *chatv1.LaterReminderRequest) (*chatv1.LaterReminder, error) {
	return s.laterReminderInfoProto(ctx, input)
}

func (s *Server) LaterReminders(ctx context.Context, input *chatv1.LaterRemindersRequest) (*chatv1.LaterReminderPage, error) {
	return s.laterRemindersProto(ctx, input)
}

func (s *Server) UpdateLaterReminder(ctx context.Context, input *chatv1.UpdateLaterReminderRequest) (*chatv1.LaterReminder, error) {
	return s.updateLaterReminderProto(ctx, input)
}

func (s *Server) AcknowledgeLaterReminders(ctx context.Context, input *chatv1.AcknowledgeLaterRemindersRequest) (*chatv1.MutationResponse, error) {
	return s.acknowledgeLaterRemindersProto(ctx, input)
}

func (s *Server) CompleteLaterReminder(ctx context.Context, input *chatv1.LaterReminderRequest) (*chatv1.MutationResponse, error) {
	return s.completeLaterReminderProto(ctx, input)
}

func (s *Server) DeleteLaterReminder(ctx context.Context, input *chatv1.LaterReminderRequest) (*chatv1.MutationResponse, error) {
	return s.deleteLaterReminderProto(ctx, input)
}

func (s *Server) ListActivity(ctx context.Context, input *chatv1.ActivityRequest) (*chatv1.ActivityPage, error) {
	kinds := make([]domain.ActivityKind, 0, len(input.GetKinds()))
	for _, kind := range input.GetKinds() {
		kinds = append(kinds, domain.ActivityKind(kind))
	}
	page, err := s.implementation.Activity(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ActivityQuery{
		Kinds: kinds, UnreadOnly: input.GetUnreadOnly(), ClearedOnly: input.GetClearedOnly(),
		Page: domain.PageRequest{Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor())},
	})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.ActivityItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, encodeProtoActivityItem(item))
	}
	return &chatv1.ActivityPage{Items: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) MutateActivity(ctx context.Context, input *chatv1.MutateActivityRequest) (*chatv1.MutationResponse, error) {
	ids := make([]domain.ActivityID, 0, len(input.GetActivityIds()))
	for _, id := range input.GetActivityIds() {
		ids = append(ids, domain.ActivityID(id))
	}
	if err := s.implementation.MutateActivity(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids, domain.ActivityMutation(input.GetMutation())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) GetActivityPreferences(ctx context.Context, input *chatv1.ActivityPreferencesRequest) (*chatv1.ActivityPreferences, error) {
	preferences, err := s.implementation.ActivityPreferences(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoActivityPreferences(preferences), nil
}

func (s *Server) SetActivityPreferences(ctx context.Context, input *chatv1.SetActivityPreferencesRequest) (*chatv1.ActivityPreferences, error) {
	preferences, err := s.implementation.SetActivityPreferences(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ActivityLayout(input.GetLayout()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoActivityPreferences(preferences), nil
}

func (s *Server) CreateActivitySavedView(ctx context.Context, input *chatv1.CreateActivitySavedViewRequest) (*chatv1.ActivitySavedView, error) {
	kinds := make([]domain.ActivityKind, 0, len(input.GetKinds()))
	for _, kind := range input.GetKinds() {
		kinds = append(kinds, domain.ActivityKind(kind))
	}
	view, err := s.implementation.CreateActivitySavedView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), kinds)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoActivitySavedView(view), nil
}

func (s *Server) DeleteActivitySavedView(ctx context.Context, input *chatv1.DeleteActivitySavedViewRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteActivitySavedView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ActivitySavedViewID(input.GetViewId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) GetWorkspaceNotificationPreferences(ctx context.Context, input *chatv1.NotificationPreferencesRequest) (*chatv1.WorkspaceNotificationPreferences, error) {
	preferences, err := s.implementation.WorkspaceNotificationPreferences(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspaceNotificationPreferences(preferences), nil
}

func (s *Server) SetNotificationSchedule(ctx context.Context, input *chatv1.SetNotificationScheduleRequest) (*chatv1.WorkspaceNotificationPreferences, error) {
	preferences, err := s.implementation.SetNotificationSchedule(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeProtoNotificationSchedule(input.GetSchedule()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspaceNotificationPreferences(preferences), nil
}

func (s *Server) SetNotificationVIP(ctx context.Context, input *chatv1.SetNotificationVIPRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetNotificationVIP(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetId()), input.GetAdd()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SidebarSections(ctx context.Context, input *chatv1.SidebarSectionsRequest) (*chatv1.SidebarSectionsResponse, error) {
	sections, err := s.implementation.SidebarSections(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.SidebarSection, 0, len(sections))
	for _, section := range sections {
		encoded = append(encoded, encodeProtoSidebarSection(section))
	}
	return &chatv1.SidebarSectionsResponse{Sections: encoded}, nil
}

func (s *Server) CreateSidebarSection(ctx context.Context, input *chatv1.CreateSidebarSectionRequest) (*chatv1.SidebarSection, error) {
	section, err := s.implementation.CreateSidebarSection(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSidebarSection(section), nil
}

func (s *Server) RenameSidebarSection(ctx context.Context, input *chatv1.RenameSidebarSectionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RenameSidebarSection(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SidebarSectionID(input.GetSectionId()), input.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SetSidebarSectionCollapsed(ctx context.Context, input *chatv1.SetSidebarSectionCollapsedRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetSidebarSectionCollapsed(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SidebarSectionID(input.GetSectionId()), input.GetCollapsed()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SetSidebarSectionNotificationLevel(ctx context.Context, input *chatv1.SetSidebarSectionNotificationLevelRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetSidebarSectionNotificationLevel(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SidebarSectionID(input.GetSectionId()), domain.NotificationLevel(input.GetLevel())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) DeleteSidebarSection(ctx context.Context, input *chatv1.DeleteSidebarSectionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteSidebarSection(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SidebarSectionID(input.GetSectionId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) ReorderSidebarSections(ctx context.Context, input *chatv1.ReorderSidebarSectionsRequest) (*chatv1.MutationResponse, error) {
	order := make([]domain.SidebarSectionID, 0, len(input.GetSectionIds()))
	for _, id := range input.GetSectionIds() {
		order = append(order, domain.SidebarSectionID(id))
	}
	if err := s.implementation.ReorderSidebarSections(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), order); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AssignConversationToSidebarSection(ctx context.Context, input *chatv1.AssignConversationToSidebarSectionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AssignConversationToSidebarSection(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.SidebarSectionID(input.GetSectionId()), domain.ConversationID(input.GetAfterConversationId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SetWorkspaceNotificationPreferences(ctx context.Context, input *chatv1.SetWorkspaceNotificationPreferencesRequest) (*chatv1.WorkspaceNotificationPreferences, error) {
	preferences, err := s.implementation.SetWorkspaceNotificationPreferences(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.NotificationLevel(input.GetLevel()), input.GetKeywords(), input.GetActivityChannels(), input.GetActivityReminders(), input.GetBrowserNotifications(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspaceNotificationPreferences(preferences), nil
}

func (s *Server) GetConversationNotificationPreferences(ctx context.Context, input *chatv1.ConversationNotificationPreferencesRequest) (*chatv1.ConversationNotificationPreferences, error) {
	preferences, err := s.implementation.ConversationNotificationPreferences(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationNotificationPreferences(preferences), nil
}

func (s *Server) SetConversationNotificationPreferences(ctx context.Context, input *chatv1.SetConversationNotificationPreferencesRequest) (*chatv1.ConversationNotificationPreferences, error) {
	preferences, err := s.implementation.SetConversationNotificationPreferences(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()),
		domain.NotificationLevel(input.GetLevel()), input.GetFollowEveryThread(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationNotificationPreferences(preferences), nil
}

func (s *Server) GetThreadFollow(ctx context.Context, input *chatv1.ThreadFollowRequest) (*chatv1.ThreadFollow, error) {
	followed, err := s.implementation.ThreadFollowed(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTimestamp()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ThreadFollow{Followed: followed}, nil
}

func (s *Server) SetThreadFollow(ctx context.Context, input *chatv1.SetThreadFollowRequest) (*chatv1.ThreadFollow, error) {
	if err := s.implementation.SetThreadFollowed(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTimestamp()), input.GetFollowed(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ThreadFollow{Followed: input.GetFollowed()}, nil
}

func (s *Server) ScheduleMessage(ctx context.Context, input *chatv1.ScheduleMessageRequest) (*chatv1.ScheduledMessage, error) {
	return s.scheduleMessageProto(ctx, input)
}

func (s *Server) ScheduledMessages(ctx context.Context, input *chatv1.ScheduledMessagesRequest) (*chatv1.ScheduledMessagePage, error) {
	return s.scheduledMessagesProto(ctx, input)
}

func (s *Server) ScheduledMessageHistory(ctx context.Context, input *chatv1.ScheduledMessageHistoryRequest) (*chatv1.ScheduledMessagePage, error) {
	page, err := s.implementation.ScheduledMessageHistory(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetIncludeDelivered(), domain.PageRequest{
		Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor()), Descending: input.GetDescending(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.ScheduledMessage, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, encodeProtoScheduledMessage(value))
	}
	return &chatv1.ScheduledMessagePage{ScheduledMessages: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) UpdateScheduledMessage(ctx context.Context, input *chatv1.UpdateScheduledMessageRequest) (*chatv1.ScheduledMessage, error) {
	value, err := s.implementation.UpdateScheduledMessage(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ScheduledMessageID(input.GetScheduledMessageId()), domain.ConversationID(input.GetChannelId()),
		input.GetText(), time.Unix(input.GetPostAt(), 0).UTC(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoScheduledMessage(value), nil
}

func (s *Server) SendScheduledMessageNow(ctx context.Context, input *chatv1.SendScheduledMessageNowRequest) (*chatv1.Message, error) {
	value, err := s.implementation.SendScheduledMessageNow(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ScheduledMessageID(input.GetScheduledMessageId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) PostScheduledMessage(ctx context.Context, input *chatv1.PostScheduledMessageRequest) (*chatv1.Message, error) {
	value, err := s.implementation.PostScheduledMessage(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.ScheduledMessageID(input.GetScheduledMessageId()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) DeleteScheduledMessage(ctx context.Context, input *chatv1.DeleteScheduledMessageRequest) (*chatv1.MutationResponse, error) {
	return s.deleteScheduledMessageProto(ctx, input)
}

func (s *Server) SaveDraft(ctx context.Context, input *chatv1.DraftRequest) (*chatv1.Draft, error) {
	value, err := s.implementation.SaveDraftWithAttachments(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTs()), input.GetText(),
		decodeProtoDraftAttachments(input.GetAttachments()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDraft(value), nil
}

func (s *Server) GetDraft(ctx context.Context, input *chatv1.DraftRequest) (*chatv1.Draft, error) {
	value, err := s.implementation.Draft(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTs()),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDraft(value), nil
}

func (s *Server) Drafts(ctx context.Context, input *chatv1.DraftsRequest) (*chatv1.DraftPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	request.Descending = input.GetDescending()
	page, err := s.implementation.Drafts(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.Draft, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, encodeProtoDraft(value))
	}
	return &chatv1.DraftPage{Drafts: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) DeleteDraft(ctx context.Context, input *chatv1.DraftRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteDraft(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetThreadTs()),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) SentMessages(ctx context.Context, input *chatv1.SentMessagesRequest) (*chatv1.MessagePage, error) {
	page, err := s.implementation.SentMessages(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.PageRequest{
		Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor()), Descending: input.GetDescending(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) ListEventsAfter(ctx context.Context, input *chatv1.EventsRequest) (*chatv1.EventsResponse, error) {
	return s.listEventsAfterProto(ctx, input)
}

func (s *Server) ClaimAppEvent(ctx context.Context, input *chatv1.AppEventClaimRequest) (*chatv1.AppEventLease, error) {
	record, attempt, reason, found, err := s.implementation.ClaimAppEvent(ctx, domain.AppID(input.GetAppId()), input.GetSurface(), input.GetOwner(), time.Duration(input.GetLeaseNanos()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.AppEventLease{Attempt: int32(attempt), RetryReason: reason, Found: found}
	if found {
		result.Record = encodeProtoEventRecord(record)
	}
	return result, nil
}

func (s *Server) AckAppEvent(ctx context.Context, input *chatv1.AppEventAckRequest) (*chatv1.AppEventMutationResponse, error) {
	if err := s.implementation.AckAppEvent(ctx, domain.AppID(input.GetAppId()), input.GetSurface(), input.GetOwner(), input.GetSequence()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppEventMutationResponse{Ok: true}, nil
}

func (s *Server) ReleaseAppEvent(ctx context.Context, input *chatv1.AppEventReleaseRequest) (*chatv1.AppEventMutationResponse, error) {
	if err := s.implementation.ReleaseAppEvent(ctx, domain.AppID(input.GetAppId()), input.GetSurface(), input.GetOwner(), input.GetSequence(), input.GetRetryReason(), time.Unix(0, input.GetRetryAtUnixNano()).UTC()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppEventMutationResponse{Ok: true}, nil
}

func (s *Server) UserByEmail(ctx context.Context, input *chatv1.UserByEmailRequest) (*chatv1.User, error) {
	return s.userByEmailProto(ctx, input)
}

func (s *Server) SetUserProfile(ctx context.Context, input *chatv1.SetUserProfileRequest) (*chatv1.User, error) {
	return s.setUserProfileProto(ctx, input)
}

func (s *Server) ScheduleUserStatus(ctx context.Context, input *chatv1.ScheduleUserStatusRequest) (*chatv1.ScheduledStatus, error) {
	value, err := s.implementation.ScheduleUserStatus(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetStatusText(), input.GetStatusEmoji(), time.Unix(input.GetStartsAt(), 0).UTC(), time.Unix(input.GetEndsAt(), 0).UTC())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoScheduledStatus(value), nil
}

func (s *Server) ScheduledUserStatuses(ctx context.Context, input *chatv1.ScheduledUserStatusesRequest) (*chatv1.ScheduledUserStatusesResponse, error) {
	values, err := s.implementation.ScheduledUserStatuses(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.ScheduledUserStatusesResponse{Statuses: make([]*chatv1.ScheduledStatus, 0, len(values))}
	for _, value := range values {
		result.Statuses = append(result.Statuses, encodeProtoScheduledStatus(value))
	}
	return result, nil
}

func (s *Server) UpdateScheduledUserStatus(ctx context.Context, input *chatv1.UpdateScheduledUserStatusRequest) (*chatv1.ScheduledStatus, error) {
	value, err := s.implementation.UpdateScheduledUserStatus(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ScheduledStatusID(input.GetId()), input.GetStatusText(), input.GetStatusEmoji(), time.Unix(input.GetStartsAt(), 0).UTC(), time.Unix(input.GetEndsAt(), 0).UTC())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoScheduledStatus(value), nil
}

func (s *Server) DeleteScheduledUserStatus(ctx context.Context, input *chatv1.DeleteScheduledUserStatusRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteScheduledUserStatus(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ScheduledStatusID(input.GetId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
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
	message, err := s.implementation.Update(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetText())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(message), nil
}

func (s *Server) deleteProto(ctx context.Context, input *chatv1.DeleteRequest) (*chatv1.Message, error) {
	message, err := s.implementation.Delete(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(message), nil
}

func (s *Server) permalinkProto(ctx context.Context, input *chatv1.PermalinkRequest) (*chatv1.PermalinkResponse, error) {
	value, err := s.implementation.Permalink(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.PermalinkResponse{Permalink: value}, nil
}

func (s *Server) historyProto(ctx context.Context, input *chatv1.HistoryRequest) (*chatv1.MessagePage, error) {
	request := protoDirectionalPageRequest(input.GetLimit(), input.GetCursor(), input.GetDescending())
	page, err := s.implementation.History(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) searchProto(ctx context.Context, input *chatv1.SearchRequest) (*chatv1.MessagePage, error) {
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
	request := domain.MessageSearchRequest{
		Query: input.GetQuery(), Conversation: domain.ConversationID(input.GetConversationId()),
		Sort: domain.SearchSort(input.GetSort()), Direction: domain.SearchDirection(input.GetDirection()),
		Page: protoPageRequest(input.GetLimit(), input.GetCursor()),
	}
	page, err := s.implementation.SearchMessages(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) fileInfoProto(ctx context.Context, input *chatv1.FileRequest) (*chatv1.File, error) {
	file, err := s.implementation.FileInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFile(file), nil
}

func (s *Server) deleteFileProto(ctx context.Context, input *chatv1.FileRequest) (*chatv1.DeleteFileResponse, error) {
	if err := s.implementation.DeleteFile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DeleteFileResponse{Ok: true}, nil
}

func (s *Server) filesProto(ctx context.Context, input *chatv1.FilesRequest) (*chatv1.FilePage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	page, err := s.implementation.Files(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFilePage(page), nil
}

// maxEmptyUploadFrames bounds how many frames in a row an upload stream may
// send that carry no bytes.
//
// An empty frame is a no-op rather than a rejection, because in the monolith the
// same upload hands the implementation an io.Reader and a zero-length write on
// it does nothing. What was missing is a progress requirement: a peer that sends
// empty frames forever holds the stream, the handler goroutine, the io.Pipe and
// the implementation goroutine open indefinitely, and keepalive never fires
// because frames are arriving. This package already refuses the mirror image on
// the client side (transport.go: "an io.Reader that keeps returning (0, nil)
// spun that loop forever"), and the server-side equivalent was deleted while
// that comment stayed.
//
// The bound is a resource bound, not a domain judgement: it reads no field of
// any request, it counts frames that carried nothing. A real client cannot
// reach it — a thousand consecutive flushes with no bytes is not a flush
// pattern, it is a peer that has stopped making progress.
const maxEmptyUploadFrames = 1024

func (s *Server) UploadFile(stream chatv1.ChatService_UploadFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetMetadata()
	if header == nil {
		return invalidArgument("upload stream must begin with metadata")
	}
	reader, writer := io.Pipe()
	type uploadResult struct {
		file domain.File
		err  error
	}
	result := make(chan uploadResult, 1)
	emptyFrames := 0
	go func() {
		// The implementation runs on its own stack, which no interceptor
		// unwinds: a panic here would terminate the whole chat process and
		// every other in-flight request with it, where the monolith loses one
		// request. Recovering turns it into the same codes.Internal the
		// interceptor produces, and closes the pipe so the receive loop below
		// cannot block forever waiting for a reader that no longer exists.
		defer func() {
			if recovered := recover(); recovered != nil {
				failure := panicError(recovered)
				result <- uploadResult{err: failure}
				_ = writer.CloseWithError(failure)
			}
		}()
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
		// An empty frame is a no-op, not a rejection. In the monolith the same
		// upload hands the implementation an io.Reader, and a zero-length Write on
		// it does nothing; rejecting it here made a client that flushes an empty
		// frame succeed in process and fail with invalid_arg_name across the seam.
		// A stream that never makes progress is a different thing: see
		// maxEmptyUploadFrames.
		if len(chunk) == 0 {
			emptyFrames++
			if emptyFrames > maxEmptyUploadFrames {
				refusal := invalidArgument("upload stream sent no data in consecutive frames and is making no progress")
				_ = writer.CloseWithError(refusal)
				<-result
				return refusal
			}
			continue
		}
		emptyFrames = 0
		if _, err := writer.Write(chunk); err != nil {
			_ = writer.CloseWithError(err)
			// A write fails because the implementation stopped reading, so its
			// error is the cause and the pipe error is the symptom. Reporting the
			// symptom made the seam answer codes.Unavailable with no domain class
			// for a failure the monolith reports as service.ErrBlobUnavailable.
			if completed := <-result; completed.err != nil {
				return mapError(completed.err)
			}
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
		return invalidArgument("user photo stream must begin with metadata")
	}
	reader, writer := io.Pipe()
	type result struct {
		user domain.User
		err  error
	}
	done := make(chan result, 1)
	emptyFrames := 0
	go func() {
		// See Server.UploadFile: a panic on this stack is not reachable by the
		// stream interceptor, so it is recovered where it happens.
		defer func() {
			if recovered := recover(); recovered != nil {
				failure := panicError(recovered)
				done <- result{err: failure}
				_ = writer.CloseWithError(failure)
			}
		}()
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
		// See Server.UploadFile: an empty frame is the no-op it is in process,
		// and a stream of nothing but empty frames is a stream making no
		// progress.
		if len(chunk) == 0 {
			emptyFrames++
			if emptyFrames > maxEmptyUploadFrames {
				refusal := invalidArgument("user photo stream sent no data in consecutive frames and is making no progress")
				_ = writer.CloseWithError(refusal)
				<-done
				return refusal
			}
			continue
		}
		emptyFrames = 0
		if _, err := writer.Write(chunk); err != nil {
			_ = writer.CloseWithError(err)
			// See Server.UploadFile: the implementation's error is the cause of a
			// failed pipe write.
			if completed := <-done; completed.err != nil {
				return mapError(completed.err)
			}
			return mapError(err)
		}
	}
}

func (s *Server) DeleteUserPhoto(ctx context.Context, input *chatv1.UserPhotoDeleteRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteUserPhoto(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) DownloadFile(input *chatv1.DownloadFileRequest, stream chatv1.ChatService_DownloadFileServer) error {
	file, reader, err := s.implementation.OpenFile(stream.Context(), domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.FileID(input.GetFileId()))
	if err != nil {
		return mapError(err)
	}
	defer reader.Close()
	if err := stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Metadata{Metadata: encodeProtoFile(file)}}); err != nil {
		return err
	}
	return mapError(sendChunks(reader, "file reader", func(chunk []byte) error {
		return stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Chunk{Chunk: append([]byte(nil), chunk...)}})
	}))
}

func (s *Server) DownloadPublicFile(input *chatv1.PublicFileTokenRequest, stream chatv1.ChatService_DownloadPublicFileServer) error {
	file, reader, err := s.implementation.OpenPublicFile(stream.Context(), input.GetToken())
	if err != nil {
		return mapError(err)
	}
	defer reader.Close()
	if err := stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Metadata{Metadata: encodeProtoFile(file)}}); err != nil {
		return err
	}
	return mapError(sendChunks(reader, "public file reader", func(chunk []byte) error {
		return stream.Send(&chatv1.DownloadFilePart{Part: &chatv1.DownloadFilePart_Chunk{Chunk: append([]byte(nil), chunk...)}})
	}))
}

func (s *Server) DownloadUserPhoto(input *chatv1.UserPhotoDownloadRequest, stream chatv1.ChatService_DownloadUserPhotoServer) error {
	user, reader, err := s.implementation.OpenUserPhoto(stream.Context(), domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetToken())
	if err != nil {
		return mapError(err)
	}
	defer reader.Close()
	// The resolved user is sent rather than discarded, and no MIME type is
	// invented: the module contract returns a user and a reader and exposes no
	// media type, so a hardcoded "application/octet-stream" claimed knowledge the
	// server does not have.
	if err := stream.Send(&chatv1.UserPhotoDownloadPart{Part: &chatv1.UserPhotoDownloadPart_Metadata{Metadata: &chatv1.UserPhotoMetadata{Token: input.GetToken(), User: encodeProtoUser(user)}}}); err != nil {
		return err
	}
	return mapError(sendChunks(reader, "user photo reader", func(chunk []byte) error {
		return stream.Send(&chatv1.UserPhotoDownloadPart{Part: &chatv1.UserPhotoDownloadPart_Chunk{Chunk: append([]byte(nil), chunk...)}})
	}))
}

func (s *Server) lookupTokenProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.TokenRecord, error) {
	record, err := s.tokens.LookupToken(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoToken(record), nil
}

func (s *Server) lookupSessionProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.SessionRecord, error) {
	record, err := s.sessions.LookupSession(ctx, input.GetToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSession(record), nil
}

func (s *Server) revokeSessionProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AuthRevokeResponse, error) {
	if err := s.revoker.RevokeSession(ctx, input.GetToken()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) revokeTokenProto(ctx context.Context, input *chatv1.TokenRequest) (*chatv1.AuthRevokeResponse, error) {
	if err := s.tokenRevoker.RevokeToken(ctx, input.GetToken()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AuthRevokeResponse{Ok: true}, nil
}

func (s *Server) repliesProto(ctx context.Context, input *chatv1.RepliesRequest) (*chatv1.MessagePage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	page, err := s.implementation.Replies(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) conversationInfoProto(ctx context.Context, input *chatv1.ConversationInfoRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.ConversationInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) userInfoProto(ctx context.Context, input *chatv1.UserRequest) (*chatv1.User, error) {
	result, err := s.implementation.UserInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetRequestedUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) userByEmailProto(ctx context.Context, input *chatv1.UserByEmailRequest) (*chatv1.User, error) {
	result, err := s.implementation.UserByEmail(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetEmail())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) setUserProfileProto(ctx context.Context, input *chatv1.SetUserProfileRequest) (*chatv1.User, error) {
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
	// An absent profile is the empty profile, which is a valid request: the local
	// path accepts it and clears the fields.
	p := input.GetProfile()
	profile := domain.UserProfile{
		DisplayName: p.GetDisplayName(), StatusText: p.GetStatusText(), StatusEmoji: p.GetStatusEmoji(),
		Image24: p.GetImage_24(), Image32: p.GetImage_32(), Image48: p.GetImage_48(), Image72: p.GetImage_72(),
		Image192: p.GetImage_192(), Image512: p.GetImage_512(), Image1024: p.GetImage_1024(),
	}
	if p.GetStatusExpiration() != 0 {
		profile.StatusExpiration = time.Unix(p.GetStatusExpiration(), 0).UTC()
	}
	result, err := s.implementation.SetUserProfile(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), profile)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) setUserPresenceProto(ctx context.Context, input *chatv1.SetUserPresenceRequest) (*chatv1.User, error) {
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
	result, err := s.implementation.SetUserPresence(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.Presence(input.GetPresence()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUser(result), nil
}

func (s *Server) doNotDisturbInfoProto(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.DoNotDisturb, error) {
	result, err := s.implementation.DoNotDisturbInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetRequestedUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDoNotDisturb(result), nil
}

func (s *Server) setSnoozeProto(ctx context.Context, input *chatv1.SetSnoozeRequest) (*chatv1.DoNotDisturb, error) {
	result, err := s.implementation.SetSnooze(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetMinutes())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDoNotDisturb(result), nil
}

func (s *Server) endSnoozeProto(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.DoNotDisturb, error) {
	result, err := s.implementation.EndSnooze(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoDoNotDisturb(result), nil
}

func (s *Server) endDNDProto(ctx context.Context, input *chatv1.DoNotDisturbRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.EndDND(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) usersProto(ctx context.Context, input *chatv1.UsersRequest) (*chatv1.UserPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	page, err := s.implementation.Users(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserPage(page), nil
}

func (s *Server) conversationMembersProto(ctx context.Context, input *chatv1.ConversationMembersRequest) (*chatv1.UserPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	page, err := s.implementation.ConversationMembers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserPage(page), nil
}

func (s *Server) workspaceInfoProto(ctx context.Context, input *chatv1.WorkspaceRequest) (*chatv1.Workspace, error) {
	result, err := s.implementation.WorkspaceInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoWorkspace(result), nil
}

func (s *Server) conversationsProto(ctx context.Context, input *chatv1.ConversationsRequest) (*chatv1.ConversationPage, error) {
	request, err := protoConversationListRequest(input)
	if err != nil {
		return nil, invalidArgumentFrom(err)
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
	page := protoPageRequest(input.GetLimit(), input.GetCursor())
	types, err := domain.NormalizeConversationTypes(input.GetTypes())
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	return domain.ConversationListRequest{Limit: page.Limit, Cursor: page.Cursor, ExcludeArchived: input.GetExcludeArchived(), Types: types, MemberUserID: domain.UserID(strings.TrimSpace(input.GetMemberUserId())), IncludeClosedDirects: input.GetIncludeClosedDirects()}, nil
}

func (s *Server) openConversationProto(ctx context.Context, input *chatv1.OpenConversationRequest) (*chatv1.Conversation, error) {
	users := protoUserIDs(input.GetUsers())
	conversation, err := s.implementation.OpenConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), users)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) createConversationProto(ctx context.Context, input *chatv1.CreateConversationRequest) (*chatv1.Conversation, error) {
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
	conversation, err := s.implementation.CreateConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetName(), input.GetPrivate())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) joinConversationProto(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.Conversation, error) {
	conversation, err := s.implementation.JoinConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) inviteConversationMembersProto(ctx context.Context, input *chatv1.InviteConversationMembersRequest) (*chatv1.Conversation, error) {
	users := protoUserIDs(input.GetUsers())
	conversation, err := s.implementation.InviteConversationMembers(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), users)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func (s *Server) leaveConversationProto(ctx context.Context, input *chatv1.ConversationRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.LeaveConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) kickConversationMemberProto(ctx context.Context, input *chatv1.KickConversationMemberRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.KickConversationMember(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.UserID(input.GetTargetId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) renameConversationProto(ctx context.Context, input *chatv1.RenameConversationRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.RenameConversation(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) setConversationTopicProto(ctx context.Context, input *chatv1.SetConversationTopicRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.SetConversationTopic(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetTopic())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) setConversationPurposeProto(ctx context.Context, input *chatv1.SetConversationPurposeRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.SetConversationPurpose(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetPurpose())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) setConversationArchivedProto(ctx context.Context, input *chatv1.SetConversationArchivedRequest) (*chatv1.Conversation, error) {
	result, err := s.implementation.SetConversationArchived(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetArchived())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(result), nil
}

func (s *Server) markReadProto(ctx context.Context, input *chatv1.MarkReadRequest) (*chatv1.ReadCursor, error) {
	cursor, err := s.implementation.MarkRead(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReadCursor(cursor), nil
}

func (s *Server) addReactionProto(ctx context.Context, input *chatv1.ReactionRequest) (*chatv1.MutationResponse, error) {
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
	if err := s.implementation.AddReaction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) removeReactionProto(ctx context.Context, input *chatv1.ReactionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RemoveReaction(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) reactionsProto(ctx context.Context, input *chatv1.ReactionPageRequest) (*chatv1.ReactionPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	items, next, more, err := s.implementation.Reactions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReactionPage(items, next, more), nil
}

func (s *Server) userReactionsProto(ctx context.Context, input *chatv1.UserReactionsRequest) (*chatv1.UserReactionPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	page, err := s.implementation.UserReactions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoUserReactionPage(page), nil
}

func (s *Server) addPinProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AddPin(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) removePinProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RemovePin(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) pinsProto(ctx context.Context, input *chatv1.PinsRequest) (*chatv1.PinPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	items, next, more, err := s.implementation.Pins(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoPinPage(items, next, more), nil
}

func (s *Server) addStarProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AddStar(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) removeStarProto(ctx context.Context, input *chatv1.PinRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.RemoveStar(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) starsProto(ctx context.Context, input *chatv1.StarsRequest) (*chatv1.StarPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	items, next, more, err := s.implementation.Stars(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoStarPage(items, next, more), nil
}

func (s *Server) addBookmarkProto(ctx context.Context, input *chatv1.AddBookmarkRequest) (*chatv1.Bookmark, error) {
	// Required fields are validated by the implementation, not here. A duplicate
	// check in the transport answered a different error than the monolith for the
	// same request — the module returns its own validation sentinel, and the
	// authorisation it performs first can turn a missing workspace into
	// store.ErrNotFound — so the caller derived a different HTTP status depending
	// on the composition. Delegating makes the two identical for every input
	// rather than for the inputs a test happens to cover.
	bookmark, err := s.implementation.AddBookmark(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), input.GetTitle(), domain.BookmarkType(input.GetType()), input.GetLink(), input.GetEmoji(), input.GetEntityId(), input.GetAccessLevel(), input.GetParentId())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBookmark(bookmark), nil
}

func (s *Server) editBookmarkProto(ctx context.Context, input *chatv1.EditBookmarkRequest) (*chatv1.Bookmark, error) {
	bookmark, err := s.implementation.EditBookmark(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.BookmarkID(input.GetBookmarkId()), domain.BookmarkUpdate{Title: input.GetTitle(), Link: input.GetLink(), Emoji: input.GetEmoji(), SetTitle: input.Title != nil, SetLink: input.Link != nil, SetEmoji: input.Emoji != nil})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoBookmark(bookmark), nil
}

func (s *Server) bookmarksProto(ctx context.Context, input *chatv1.BookmarksRequest) (*chatv1.BookmarksResponse, error) {
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
	if err := s.implementation.RemoveBookmark(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.BookmarkID(input.GetBookmarkId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) addReminderProto(ctx context.Context, input *chatv1.AddReminderRequest) (*chatv1.Reminder, error) {
	reminder, err := s.implementation.AddReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.UserID(input.GetTargetUserId()), input.GetText(), time.Unix(input.GetTime(), 0).UTC())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReminder(reminder), nil
}

func (s *Server) reminderInfoProto(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.Reminder, error) {
	reminder, err := s.implementation.ReminderInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ReminderID(input.GetReminderId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoReminder(reminder), nil
}

func (s *Server) remindersProto(ctx context.Context, input *chatv1.RemindersRequest) (*chatv1.ReminderPage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
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
	if err := s.implementation.CompleteReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteReminderProto(ctx context.Context, input *chatv1.ReminderRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) createLaterReminderProto(ctx context.Context, input *chatv1.CreateLaterReminderRequest) (*chatv1.LaterReminder, error) {
	reminder, err := s.implementation.CreateLaterReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.LaterReminderRequest{
		Target: domain.LaterReminderTarget(input.GetTarget()), Channel: domain.ConversationID(input.GetChannelId()),
		SourceChannel: domain.ConversationID(input.GetSourceChannelId()), SourceTimestamp: domain.MessageTimestamp(input.GetSourceTimestamp()),
		Text: input.GetText(), DueAt: timeFromUnix(input.GetDueAt()), TimeZone: input.GetTimezone(),
		Recurrence: domain.ReminderRecurrence(input.GetRecurrence()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoLaterReminder(reminder), nil
}

func (s *Server) laterReminderInfoProto(ctx context.Context, input *chatv1.LaterReminderRequest) (*chatv1.LaterReminder, error) {
	reminder, err := s.implementation.LaterReminderInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.LaterReminderID(input.GetReminderId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoLaterReminder(reminder), nil
}

func (s *Server) laterRemindersProto(ctx context.Context, input *chatv1.LaterRemindersRequest) (*chatv1.LaterReminderPage, error) {
	page, err := s.implementation.LaterReminders(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.LaterReminderTarget(input.GetTarget()), protoDirectionalPageRequest(input.GetLimit(), input.GetCursor(), input.GetDescending()))
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.LaterReminder, 0, len(page.Items))
	for _, reminder := range page.Items {
		items = append(items, encodeProtoLaterReminder(reminder))
	}
	return &chatv1.LaterReminderPage{Reminders: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) updateLaterReminderProto(ctx context.Context, input *chatv1.UpdateLaterReminderRequest) (*chatv1.LaterReminder, error) {
	reminder, err := s.implementation.UpdateLaterReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.LaterReminderID(input.GetReminderId()), domain.LaterReminderRequest{
		Target: domain.LaterReminderTarget(input.GetTarget()), Channel: domain.ConversationID(input.GetChannelId()),
		Text: input.GetText(), DueAt: timeFromUnix(input.GetDueAt()), TimeZone: input.GetTimezone(),
		Recurrence: domain.ReminderRecurrence(input.GetRecurrence()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoLaterReminder(reminder), nil
}

func (s *Server) acknowledgeLaterRemindersProto(ctx context.Context, input *chatv1.AcknowledgeLaterRemindersRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AcknowledgeLaterReminders(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) completeLaterReminderProto(ctx context.Context, input *chatv1.LaterReminderRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.CompleteLaterReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.LaterReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) deleteLaterReminderProto(ctx context.Context, input *chatv1.LaterReminderRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteLaterReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.LaterReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) scheduleMessageProto(ctx context.Context, input *chatv1.ScheduleMessageRequest) (*chatv1.ScheduledMessage, error) {
	workspaceID := domain.WorkspaceID(input.GetWorkspaceId())
	userID := domain.UserID(input.GetUserId())
	credentialHash := input.GetCredentialHash()
	if credentialHash == "" {
		credentialHash = grpcScheduledCredential(workspaceID, userID)
	}
	value, err := s.implementation.ScheduleMessageAs(ctx, workspaceID, userID, domain.ScheduledMessageRequest{
		Channel: domain.ConversationID(input.GetChannelId()), Text: input.GetText(), Blocks: input.GetBlocks(),
		Attachments: input.GetAttachments(), AppID: domain.AppID(input.GetAppId()), BotID: domain.BotID(input.GetBotId()),
		CredentialHash: credentialHash, ThreadTimestamp: domain.MessageTimestamp(input.GetThreadTs()),
		Metadata: input.GetMetadata(), StreamState: input.GetStreamState(), PostAt: time.Unix(input.GetPostAt(), 0).UTC(),
		FileAttachments: decodeProtoDraftAttachments(input.GetFileAttachments()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoScheduledMessage(value), nil
}

func (s *Server) scheduledMessagesProto(ctx context.Context, input *chatv1.ScheduledMessagesRequest) (*chatv1.ScheduledMessagePage, error) {
	request := protoPageRequest(input.GetLimit(), input.GetCursor())
	workspaceID := domain.WorkspaceID(input.GetWorkspaceId())
	userID := domain.UserID(input.GetUserId())
	credentialHash := input.GetCredentialHash()
	if credentialHash == "" {
		credentialHash = grpcScheduledCredential(workspaceID, userID)
	}
	page, err := s.implementation.ScheduledMessagesForCredential(ctx, workspaceID, userID, domain.ScheduledMessageQuery{
		CredentialHash: credentialHash, Channel: domain.ConversationID(input.GetChannelId()), Page: request,
		Oldest: timeFromUnix(input.GetOldest()), Latest: timeFromUnix(input.GetLatest()),
	})
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
	workspaceID := domain.WorkspaceID(input.GetWorkspaceId())
	userID := domain.UserID(input.GetUserId())
	credentialHash := input.GetCredentialHash()
	if credentialHash == "" {
		credentialHash = grpcScheduledCredential(workspaceID, userID)
	}
	if err := s.implementation.DeleteScheduledMessageForCredential(ctx, workspaceID, userID, credentialHash, domain.ConversationID(input.GetChannelId()), domain.ScheduledMessageID(input.GetScheduledMessageId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) listEventsAfterProto(ctx context.Context, input *chatv1.EventsRequest) (*chatv1.EventsResponse, error) {
	var records []events.Record
	var err error
	if input.GetUserId() != "" {
		records, err = s.implementation.ListUserEventsAfter(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetAfter(), seamPage(int(input.GetLimit())))
	} else if input.GetAppId() != "" {
		records, err = s.implementation.ListAppEventsAfter(ctx, domain.AppID(input.GetAppId()), input.GetAfter(), seamPage(int(input.GetLimit())))
	} else {
		records, err = s.implementation.ListEventsAfter(ctx, domain.WorkspaceID(input.GetWorkspaceId()), input.GetAfter(), seamPage(int(input.GetLimit())))
	}
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoEvents(records), nil
}

// unixNanoOrZero is the encoding a "no instant" time needs. UnixNano on a zero
// time is not zero, and every field that forgets this turns "never" into a date
// in 1754.
func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
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

		// A zero time must encode as zero. time.Time{}.UnixNano() is a large
		// negative number, which the decoder would read back as an instant in
		// 1754 — so a member who had never been seen would come back from the
		// remote composition as having been seen, three centuries ago.
		LastActiveAtUnixNano: unixNanoOrZero(value.LastActiveAt),
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
	if !value.ExpiresAt.IsZero() {
		result.ExpiresAt = value.ExpiresAt.Unix()
	}
	if !value.AcceptedAt.IsZero() {
		result.AcceptedAt = value.AcceptedAt.Unix()
	}
	result.AcceptedBy = string(value.AcceptedBy)
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
	if value.GetExpiresAt() != 0 {
		result.ExpiresAt = time.Unix(value.GetExpiresAt(), 0).UTC()
	}
	if value.GetAcceptedAt() != 0 {
		result.AcceptedAt = time.Unix(value.GetAcceptedAt(), 0).UTC()
	}
	result.AcceptedBy = domain.UserID(value.GetAcceptedBy())
	return result
}

func encodeProtoProfile(value domain.UserProfile) *chatv1.UserProfile {
	result := &chatv1.UserProfile{
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
	if !value.StatusExpiration.IsZero() {
		result.StatusExpiration = value.StatusExpiration.UTC().Unix()
	}
	result.ActiveScheduledStatusId = string(value.ActiveScheduledStatusID)
	return result
}

func encodeProtoScheduledStatus(value domain.ScheduledStatus) *chatv1.ScheduledStatus {
	return &chatv1.ScheduledStatus{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		StatusText: value.StatusText, StatusEmoji: value.StatusEmoji,
		StartsAt: unixOrZero(value.StartsAt), EndsAt: unixOrZero(value.EndsAt),
		CreatedAtUnixNano: value.CreatedAt.UTC().UnixNano(), UpdatedAtUnixNano: value.UpdatedAt.UTC().UnixNano(),
	}
}

func decodeProtoScheduledStatus(value *chatv1.ScheduledStatus) (domain.ScheduledStatus, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetStartsAt() <= 0 || value.GetEndsAt() <= 0 || value.GetCreatedAtUnixNano() <= 0 || value.GetUpdatedAtUnixNano() <= 0 {
		return domain.ScheduledStatus{}, errors.New("typed scheduled status is incomplete")
	}
	return domain.ScheduledStatus{
		ID: domain.ScheduledStatusID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()),
		StatusText: value.GetStatusText(), StatusEmoji: value.GetStatusEmoji(),
		StartsAt: timeFromUnix(value.GetStartsAt()), EndsAt: timeFromUnix(value.GetEndsAt()),
		CreatedAt: time.Unix(0, value.GetCreatedAtUnixNano()).UTC(), UpdatedAt: time.Unix(0, value.GetUpdatedAtUnixNano()).UTC(),
	}, nil
}

// maxSeamPage bounds what one request may make the server allocate on the
// caller's behalf.
//
// This is a resource bound, not a domain judgement, which is the distinction
// the "no rejection may read a request field's value" rule needs and did not
// make. Both stores preallocate from the page bound — make([]T, 0, limit) in
// internal/store/memory and internal/store/sqlstore — so a single request for
// 2147483647 took the process from 18 MB to 320 MB of resident memory for a
// two-record answer, and nothing below this line re-bounds it:
// domain.PageRequest is a plain struct with no normaliser and internal/service
// passes it straight through. The package already owns bounds of exactly this
// kind (MaxMessageBytes, MaxHeaderListBytes, maxStatusMessageBytes).
//
// It clamps rather than rejects, so the two compositions cannot answer
// differently: no caller is refused and no page a caller can really ask for is
// changed (internal/api/slack clamps at 100/200 before it reaches here). The
// *product* page budget still belongs in internal/service, where both
// compositions read it, and does not exist yet — this is the allocation
// backstop, not that budget.
const maxSeamPage = 10000

// seamPage clamps a caller-supplied page bound to maxSeamPage. A non-positive
// bound is left exactly as it arrived: what a zero or negative page means is a
// domain question, and the implementation owns it in both compositions.
func seamPage(limit int) int {
	if limit > maxSeamPage {
		return maxSeamPage
	}
	return limit
}

// protoPageRequest carries a page request across the seam, bounded only by the
// transport's own allocation limit.
//
// It used to reject a limit outside 1..200. Neither internal/service nor either
// store has that bound, so conversations.history?limit=201 returned a page in the
// monolith and invalid_arg_name in the split deployment, and limit=0 failed with
// two different classes. A bound that exists on one composition only is a
// divergence, not a limit: the page budget belongs to service.Messages, where
// both compositions read it. What survives here is maxSeamPage, which refuses
// nothing and only stops one request from reserving gigabytes.
func protoPageRequest(limit int32, cursor string) domain.PageRequest {
	return protoDirectionalPageRequest(limit, cursor, false)
}

func protoDirectionalPageRequest(limit int32, cursor string, descending bool) domain.PageRequest {
	return domain.PageRequest{
		Limit:      seamPage(int(limit)),
		Cursor:     domain.Cursor(cursor),
		Descending: descending,
	}
}

func stringIDs(values []domain.UserID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

// protoUserIDs converts a user list without changing it.
//
// It used to trim each entry and reject an empty one. internal/api/slack already
// trims, deduplicates and rejects an empty entry before it calls the module, in
// both compositions, so the guard was unreachable through the real path — and
// the trim was a normalisation the local composition does not perform at this
// layer, so a caller that sent " U1 " reached the implementation with a
// different value depending on how the deployment was composed.
func protoUserIDs(values []string) []domain.UserID {
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		result = append(result, domain.UserID(value))
	}
	return result
}

func encodeProtoUserPage(page domain.UserPage) *chatv1.UserPage {
	users := make([]*chatv1.User, 0, len(page.Users))
	for _, user := range page.Users {
		users = append(users, encodeProtoUser(user))
	}
	return &chatv1.UserPage{Users: users, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func encodeProtoWorkspaceMembership(value domain.WorkspaceMembership) *chatv1.WorkspaceMembership {
	return &chatv1.WorkspaceMembership{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Role: string(value.Role), Active: value.Active, Restricted: value.Restricted, UltraRestricted: value.UltraRestricted}
}

func decodeProtoWorkspaceMembership(value *chatv1.WorkspaceMembership) (domain.WorkspaceMembership, error) {
	if value == nil {
		return domain.WorkspaceMembership{}, errors.New("missing workspace membership")
	}
	return domain.WorkspaceMembership{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Role: domain.WorkspaceRole(value.GetRole()), Active: value.GetActive(), Restricted: value.GetRestricted(), UltraRestricted: value.GetUltraRestricted()}, nil
}

func encodeProtoAdminUserPage(page domain.AdminUserPage) *chatv1.AdminUserPage {
	users := make([]*chatv1.AdminUser, 0, len(page.Users))
	for _, value := range page.Users {
		users = append(users, &chatv1.AdminUser{User: encodeProtoUser(value.User), Role: string(value.Membership.Role), Active: value.Membership.Active, Restricted: value.Membership.Restricted, UltraRestricted: value.Membership.UltraRestricted})
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
		users = append(users, domain.AdminUser{User: user, Membership: domain.WorkspaceMembership{WorkspaceID: user.WorkspaceID, UserID: user.ID, Role: domain.WorkspaceRole(item.GetRole()), Active: item.GetActive(), Restricted: item.GetRestricted(), UltraRestricted: item.GetUltraRestricted()}})
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
	return &chatv1.Workspace{Id: string(value.ID), Domain: value.Domain, Name: value.Name, Description: value.Description, Discoverability: string(value.Discoverability), IconUrl: value.IconURL, DefaultChannelIds: channels, Plan: string(value.Plan)}
}

func decodeProtoWorkspace(value *chatv1.Workspace) (domain.Workspace, error) {
	if value == nil || value.GetId() == "" || value.GetName() == "" {
		return domain.Workspace{}, errors.New("typed workspace response is incomplete")
	}
	channels := make([]domain.ConversationID, 0, len(value.GetDefaultChannelIds()))
	for _, channel := range value.GetDefaultChannelIds() {
		channels = append(channels, domain.ConversationID(channel))
	}
	return domain.Workspace{ID: domain.WorkspaceID(value.GetId()), Domain: value.GetDomain(), Name: value.GetName(), Description: value.GetDescription(), Discoverability: domain.WorkspaceDiscoverability(value.GetDiscoverability()), IconURL: value.GetIconUrl(), DefaultChannelIDs: channels, Plan: domain.WorkspacePlan(value.GetPlan())}, nil
}

func encodeProtoWorkspacePage(value domain.WorkspacePage) *chatv1.WorkspacePage {
	workspaces := make([]*chatv1.Workspace, 0, len(value.Workspaces))
	for _, workspace := range value.Workspaces {
		workspaces = append(workspaces, encodeProtoWorkspace(workspace))
	}
	return &chatv1.WorkspacePage{Workspaces: workspaces, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoWorkspacePage(value *chatv1.WorkspacePage) (domain.WorkspacePage, error) {
	if value == nil {
		return domain.WorkspacePage{}, errors.New("typed workspace page response is nil")
	}
	workspaces := make([]domain.Workspace, 0, len(value.GetWorkspaces()))
	for _, item := range value.GetWorkspaces() {
		workspace, err := decodeProtoWorkspace(item)
		if err != nil {
			return domain.WorkspacePage{}, err
		}
		workspaces = append(workspaces, workspace)
	}
	return domain.WorkspacePage{Workspaces: workspaces, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
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
	return &chatv1.List{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), OwnerId: string(value.OwnerID), Name: value.Name, DescriptionBlocks: value.DescriptionBlocks, Schema: value.Schema, TodoMode: value.TodoMode, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano), Version: value.Version}
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
	return domain.List{ID: domain.ListID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), OwnerID: domain.UserID(value.GetOwnerId()), Name: value.GetName(), DescriptionBlocks: value.GetDescriptionBlocks(), Schema: value.GetSchema(), TodoMode: value.GetTodoMode(), Version: value.GetVersion(), CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func encodeProtoListPage(value domain.ListPage) *chatv1.ListPage {
	items := make([]*chatv1.List, 0, len(value.Lists))
	for _, item := range value.Lists {
		items = append(items, encodeProtoList(item))
	}
	return &chatv1.ListPage{Lists: items, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoListPage(value *chatv1.ListPage) (domain.ListPage, error) {
	if value == nil {
		return domain.ListPage{}, errors.New("typed list page is required")
	}
	items := make([]domain.List, 0, len(value.GetLists()))
	for _, item := range value.GetLists() {
		decoded, err := decodeProtoList(item)
		if err != nil {
			return domain.ListPage{}, err
		}
		items = append(items, decoded)
	}
	return domain.ListPage{Lists: items, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoListItem(value domain.ListItem) *chatv1.ListItem {
	return &chatv1.ListItem{Id: string(value.ID), ListId: string(value.ListID), ParentItemId: string(value.ParentItemID), WorkspaceId: string(value.WorkspaceID), Fields: value.Fields, CreatedBy: string(value.CreatedBy), UpdatedBy: string(value.UpdatedBy), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano), Archived: value.Archived, Version: value.Version, AssigneeId: string(value.AssigneeID), DueAtUnixNano: unixNanoOrZero(value.DueAt)}
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
	return domain.ListItem{ID: domain.ListItemID(value.GetId()), ListID: domain.ListID(value.GetListId()), ParentItemID: domain.ListItemID(value.GetParentItemId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Fields: value.GetFields(), CreatedBy: domain.UserID(value.GetCreatedBy()), UpdatedBy: domain.UserID(value.GetUpdatedBy()), CreatedAt: createdAt, UpdatedAt: updatedAt, Archived: value.GetArchived(), Version: value.GetVersion(), AssigneeID: domain.UserID(value.GetAssigneeId()), DueAt: optionalTimeFromUnixNano(value.GetDueAtUnixNano())}, nil
}

// encodeProtoListItemSummary carries only what an Activity row draws. Sending
// the whole item would mean a second, differently-shaped copy of the list item
// on the wire, and a row that showed fields the reader may not have access to
// through the list itself.
func encodeProtoListItemSummary(value domain.ListItemSummary) *chatv1.ListItemSummary {
	if value.ID == "" {
		return nil
	}
	return &chatv1.ListItemSummary{
		Id: string(value.ID), Fields: value.Fields, Archived: value.Archived,
		DueAtUnixNano: unixNanoOrZero(value.DueAt),
	}
}

func decodeProtoListItemSummary(value *chatv1.ListItemSummary) domain.ListItemSummary {
	if value == nil {
		return domain.ListItemSummary{}
	}
	return domain.ListItemSummary{
		ID: domain.ListItemID(value.GetId()), Fields: value.GetFields(), Archived: value.GetArchived(),
		DueAt: optionalTimeFromUnixNano(value.GetDueAtUnixNano()),
	}
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

func (r Remote) AdminLookupConversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.ConversationLookup, request domain.PageRequest) (domain.ConversationPage, error) {
	teams := make([]string, 0, len(lookup.TeamIDs))
	for _, id := range lookup.TeamIDs {
		teams = append(teams, string(id))
	}
	out, err := r.mutations.AdminLookupConversations(ctx, &chatv1.ConversationLookupRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), TeamIds: teams,
		LastMessageActivityBefore: optionalUnixNano(lookup.LastMessageActivityBefore),
		MaxMemberCount:            int32(lookup.MaxMemberCount),
		Limit:                     int32(request.Limit), Cursor: string(request.Cursor),
	})
	if err != nil {
		return domain.ConversationPage{}, err
	}
	return decodeProtoConversationPage(out)
}

func (r Remote) AdminBulkMoveConversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID, target domain.WorkspaceID) error {
	out, err := r.mutations.AdminBulkMoveConversations(ctx, &chatv1.BulkMoveConversationsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID),
		ConversationIds: conversationStrings(ids), TargetTeamId: string(target),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed bulk channel move was not acknowledged")
	}
	return nil
}

func (r Remote) AdminSetConversationsExcludedFromAI(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID, excluded bool) error {
	out, err := r.mutations.AdminSetConversationsExcludedFromAI(ctx, &chatv1.ConversationAIExclusionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationIds: conversationStrings(ids), Excluded: excluded,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed channel exclusion was not acknowledged")
	}
	return nil
}

func (r Remote) AdminConversationsExcludedFromAI(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID) ([]domain.ConversationID, error) {
	out, err := r.mutations.AdminConversationsExcludedFromAI(ctx, &chatv1.ConversationAIExclusionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationIds: conversationStrings(ids),
	})
	if err != nil {
		return nil, err
	}
	return decodeConversationIDs(out.GetConversationIds()), nil
}

func (r Remote) AdminLinkConversationObjects(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ConversationID, orgID string, recordIDs []string) error {
	out, err := r.mutations.AdminLinkConversationObjects(ctx, &chatv1.LinkConversationObjectsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(id),
		OrgId: orgID, RecordIds: append([]string{}, recordIDs...),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed channel object link was not acknowledged")
	}
	return nil
}

func (r Remote) AdminUnlinkConversationObjects(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID) error {
	out, err := r.mutations.AdminUnlinkConversationObjects(ctx, &chatv1.UnlinkConversationObjectsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationIds: conversationStrings(ids),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed channel object unlink was not acknowledged")
	}
	return nil
}

func (r Remote) AdminConversationObjects(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ConversationID) ([]domain.LinkedObject, error) {
	out, err := r.mutations.AdminConversationObjects(ctx, &chatv1.ConversationObjectsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(id),
	})
	if err != nil {
		return nil, err
	}
	objects := make([]domain.LinkedObject, 0, len(out.GetObjects()))
	for _, encoded := range out.GetObjects() {
		objects = append(objects, decodeProtoLinkedObject(encoded))
	}
	return objects, nil
}

func (r Remote) AdminCreateConversationForObjects(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, orgID, recordID string, private bool) (domain.Conversation, error) {
	out, err := r.mutations.AdminCreateConversationForObjects(ctx, &chatv1.CreateConversationForObjectsRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Name: name,
		OrgId: orgID, RecordId: recordID, IsPrivate: private,
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	return decodeProtoConversation(out)
}

func (s *Server) AdminLookupConversations(ctx context.Context, input *chatv1.ConversationLookupRequest) (*chatv1.ConversationPage, error) {
	teams := make([]domain.WorkspaceID, 0, len(input.GetTeamIds()))
	for _, id := range input.GetTeamIds() {
		teams = append(teams, domain.WorkspaceID(id))
	}
	page, err := s.implementation.AdminLookupConversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationLookup{
			TeamIDs:                   teams,
			LastMessageActivityBefore: optionalTimeFromUnixNano(input.GetLastMessageActivityBefore()),
			MaxMemberCount:            int(input.GetMaxMemberCount()),
		}, protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversationPage(page), nil
}

func (s *Server) AdminBulkMoveConversations(ctx context.Context, input *chatv1.BulkMoveConversationsRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminBulkMoveConversations(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeConversationIDs(input.GetConversationIds()), domain.WorkspaceID(input.GetTargetTeamId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminSetConversationsExcludedFromAI(ctx context.Context, input *chatv1.ConversationAIExclusionRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminSetConversationsExcludedFromAI(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeConversationIDs(input.GetConversationIds()), input.GetExcluded()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminConversationsExcludedFromAI(ctx context.Context, input *chatv1.ConversationAIExclusionRequest) (*chatv1.ConversationAIExclusionResponse, error) {
	ids, err := s.implementation.AdminConversationsExcludedFromAI(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeConversationIDs(input.GetConversationIds()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ConversationAIExclusionResponse{ConversationIds: conversationStrings(ids)}, nil
}

func (s *Server) AdminLinkConversationObjects(ctx context.Context, input *chatv1.LinkConversationObjectsRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminLinkConversationObjects(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()), input.GetOrgId(), input.GetRecordIds()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminUnlinkConversationObjects(ctx context.Context, input *chatv1.UnlinkConversationObjectsRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminUnlinkConversationObjects(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		decodeConversationIDs(input.GetConversationIds())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AdminConversationObjects(ctx context.Context, input *chatv1.ConversationObjectsRequest) (*chatv1.ConversationObjectsResponse, error) {
	objects, err := s.implementation.AdminConversationObjects(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversationId()))
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.LinkedObject, 0, len(objects))
	for _, object := range objects {
		encoded = append(encoded, encodeProtoLinkedObject(object))
	}
	return &chatv1.ConversationObjectsResponse{Objects: encoded}, nil
}

func (s *Server) AdminCreateConversationForObjects(ctx context.Context, input *chatv1.CreateConversationForObjectsRequest) (*chatv1.Conversation, error) {
	conversation, err := s.implementation.AdminCreateConversationForObjects(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		input.GetName(), input.GetOrgId(), input.GetRecordId(), input.GetIsPrivate())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoConversation(conversation), nil
}

func decodeConversationIDs(values []string) []domain.ConversationID {
	ids := make([]domain.ConversationID, 0, len(values))
	for _, value := range values {
		ids = append(ids, domain.ConversationID(value))
	}
	return ids
}

func encodeProtoLinkedObject(value domain.LinkedObject) *chatv1.LinkedObject {
	return &chatv1.LinkedObject{
		ConversationId: string(value.ConversationID), WorkspaceId: string(value.WorkspaceID),
		OrgId: value.OrgID, RecordId: value.RecordID, CreatedAt: optionalUnixNano(value.CreatedAt),
	}
}

func decodeProtoLinkedObject(value *chatv1.LinkedObject) domain.LinkedObject {
	return domain.LinkedObject{
		ConversationID: domain.ConversationID(value.GetConversationId()),
		WorkspaceID:    domain.WorkspaceID(value.GetWorkspaceId()),
		OrgID:          value.GetOrgId(), RecordID: value.GetRecordId(),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAt()),
	}
}

func encodeProtoConversation(value domain.Conversation) *chatv1.Conversation {
	return &chatv1.Conversation{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Name: value.Name,
		Topic: value.Topic, Purpose: value.Purpose, Archived: value.Archived,
		IsPrivate: value.PrivateFlag(), IsDirect: value.Kind == domain.ConversationTypeIM, IsGroupDirect: value.Kind == domain.ConversationTypeMPIM,
		UnreadCount: int64(value.UnreadCount),
		IsExtShared: value.IsExtShared, IsPendingExtShared: value.IsPendingExtShared,
	}
}

func decodeProtoConversation(value *chatv1.Conversation) (domain.Conversation, error) {
	// Name is not required: a stored conversation with an empty name is returned
	// by the local path, and rejecting it here would diverge.
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" {
		return domain.Conversation{}, errors.New("typed conversation response is incomplete")
	}
	if value.GetUnreadCount() < 0 || value.GetUnreadCount() > int64(^uint(0)>>1) {
		return domain.Conversation{}, errors.New("typed unread_count is outside platform integer range")
	}
	return domain.Conversation{
		ID: domain.ConversationID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Name: value.GetName(),
		Topic: value.GetTopic(), Purpose: value.GetPurpose(), Archived: value.GetArchived(),
		Kind:        domain.ConversationKindFor(value.GetIsPrivate(), value.GetIsDirect(), value.GetIsGroupDirect()),
		UnreadCount: int(value.GetUnreadCount()),
		IsExtShared: value.GetIsExtShared(), IsPendingExtShared: value.GetIsPendingExtShared(),
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
	files := make([]*chatv1.File, 0, len(value.Files))
	for _, file := range value.Files {
		files = append(files, encodeProtoFile(file))
	}
	return &chatv1.Message{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.Conversation),
		AuthorId: string(value.AuthorID), Text: value.Text, ThreadTimestamp: string(value.ThreadTimestamp),
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Deleted: value.Deleted, Unfurls: value.Unfurls,
		Blocks: value.Blocks, Attachments: value.Attachments, AppId: string(value.AppID),
		Metadata: value.Metadata, StreamState: value.StreamState, Files: files,
		EditedAt: optionalRFC3339(value.EditedAt), EditedBy: string(value.EditedBy), Subtype: string(value.Subtype),
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
	var edited time.Time
	if raw := value.GetEditedAt(); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return domain.Message{}, errors.New("typed message edited_at is invalid")
		}
		edited = parsed.UTC()
	}
	files := make([]domain.File, 0, len(value.GetFiles()))
	for _, encoded := range value.GetFiles() {
		file, err := decodeProtoFile(encoded)
		if err != nil {
			return domain.Message{}, err
		}
		files = append(files, file)
	}
	return domain.Message{
		ID: domain.MessageID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Conversation: domain.ConversationID(value.GetConversationId()), AuthorID: domain.UserID(value.GetAuthorId()),
		AppID: domain.AppID(value.GetAppId()), Text: value.GetText(), Blocks: value.GetBlocks(),
		Attachments: value.GetAttachments(), Metadata: value.GetMetadata(), StreamState: value.GetStreamState(),
		ThreadTimestamp: domain.MessageTimestamp(value.GetThreadTimestamp()), CreatedAt: created.UTC(), Deleted: value.GetDeleted(), Unfurls: value.GetUnfurls(), Files: files,
		EditedAt: edited, EditedBy: domain.UserID(value.GetEditedBy()), Subtype: domain.MessageSubtype(value.GetSubtype()),
	}, nil
}

// The converter pair is over one summary, not the map: the map is routing,
// and a per-entity pair is what the round-trip property can fill and compare
// field by field.
func encodeProtoThreadSummary(value domain.ThreadSummary) *chatv1.ThreadSummary {
	participants := make([]string, 0, len(value.Participants))
	for _, participant := range value.Participants {
		participants = append(participants, string(participant))
	}
	return &chatv1.ThreadSummary{
		ReplyCount: int32(value.ReplyCount), Participants: participants,
		LastReplyAt: optionalRFC3339(value.LastReplyAt),
	}
}

func decodeProtoThreadSummary(value *chatv1.ThreadSummary) (domain.ThreadSummary, error) {
	summary := domain.ThreadSummary{ReplyCount: int(value.GetReplyCount())}
	for _, participant := range value.GetParticipants() {
		summary.Participants = append(summary.Participants, domain.UserID(participant))
	}
	if raw := value.GetLastReplyAt(); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return domain.ThreadSummary{}, errors.New("typed thread summary last_reply_at is invalid")
		}
		summary.LastReplyAt = parsed.UTC()
	}
	return summary, nil
}

func encodeProtoRTMConnection(value domain.RTMConnection) *chatv1.RTMConnection {
	return &chatv1.RTMConnection{Id: value.ID, WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), ExpiresAtUnixNano: optionalUnixNano(value.ExpiresAt), Cursor: value.Cursor}
}

func decodeProtoRTMConnection(value *chatv1.RTMConnection) domain.RTMConnection {
	return domain.RTMConnection{ID: value.GetId(), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), ExpiresAt: optionalTimeFromUnixNano(value.GetExpiresAtUnixNano()), Cursor: value.GetCursor()}
}

func encodeProtoEphemeralMessage(value domain.EphemeralMessage) *chatv1.EphemeralMessage {
	return &chatv1.EphemeralMessage{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.Conversation), AuthorId: string(value.AuthorID), AppId: string(value.AppID), RecipientId: string(value.RecipientID), Text: value.Text, Blocks: value.Blocks, Attachments: value.Attachments, Timestamp: string(value.Timestamp), CreatedAt: string(domain.NewStoredTime(value.CreatedAt))}
}
func decodeProtoEphemeralMessage(value *chatv1.EphemeralMessage) (domain.EphemeralMessage, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetConversationId() == "" || value.GetAuthorId() == "" || value.GetRecipientId() == "" || (value.GetText() == "" && value.GetBlocks() == "" && value.GetAttachments() == "") || value.GetTimestamp() == "" || value.GetCreatedAt() == "" {
		return domain.EphemeralMessage{}, errors.New("typed ephemeral message is incomplete")
	}
	createdAt, err := domain.ParseStoredTime(value.GetCreatedAt())
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if _, err := domain.ParseMessageTimestamp(domain.MessageTimestamp(value.GetTimestamp())); err != nil {
		return domain.EphemeralMessage{}, err
	}
	return domain.EphemeralMessage{ID: domain.MessageID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversationId()), AuthorID: domain.UserID(value.GetAuthorId()), AppID: domain.AppID(value.GetAppId()), RecipientID: domain.UserID(value.GetRecipientId()), Text: value.GetText(), Blocks: value.GetBlocks(), Attachments: value.GetAttachments(), Timestamp: domain.MessageTimestamp(value.GetTimestamp()), CreatedAt: createdAt}, nil
}

func encodeProtoAccessLog(value domain.AccessLog) *chatv1.AccessLog {
	return &chatv1.AccessLog{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Username: value.Username, CreatedAt: value.CreatedAt.Unix(), Ip: value.IP, UserAgent: value.UserAgent}
}
func decodeProtoAccessLog(value *chatv1.AccessLog) (domain.AccessLog, error) {
	// created_at is a Unix timestamp and is decoded as sent; the local path does
	// not require it to be positive.
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetUsername() == "" {
		return domain.AccessLog{}, errors.New("typed access log is incomplete")
	}
	return domain.AccessLog{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Username: value.GetUsername(), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), IP: value.GetIp(), UserAgent: value.GetUserAgent()}, nil
}

func encodeProtoMessagePage(page domain.MessagePage) *chatv1.MessagePage {
	messages := make([]*chatv1.Message, 0, len(page.Messages))
	for _, message := range page.Messages {
		messages = append(messages, encodeProtoMessage(message))
	}
	return &chatv1.MessagePage{Messages: messages, NextCursor: string(page.NextCursor), HasMore: page.HasMore, Total: int64(page.Total)}
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
	return domain.MessagePage{Messages: messages, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore(), Total: int(value.GetTotal())}, nil
}

func encodeProtoCanvas(value domain.Canvas) *chatv1.Canvas {
	return &chatv1.Canvas{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), OwnerId: string(value.OwnerID), Title: value.Title, DocumentContent: value.DocumentContent, CreatedAt: value.CreatedAt.UTC().Unix(), UpdatedAt: value.UpdatedAt.UTC().Unix(), Version: value.Version}
}

func decodeProtoCanvas(value *chatv1.Canvas) (domain.Canvas, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetOwnerId() == "" {
		return domain.Canvas{}, errors.New("invalid canvas response")
	}
	return domain.Canvas{ID: domain.CanvasID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), OwnerID: domain.UserID(value.GetOwnerId()), Title: value.GetTitle(), DocumentContent: value.GetDocumentContent(), Version: value.GetVersion(), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC()}, nil
}

func encodeProtoCanvasPage(value domain.CanvasPage) *chatv1.CanvasPage {
	items := make([]*chatv1.Canvas, 0, len(value.Canvases))
	for _, item := range value.Canvases {
		items = append(items, encodeProtoCanvas(item))
	}
	return &chatv1.CanvasPage{Canvases: items, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoCanvasPage(value *chatv1.CanvasPage) (domain.CanvasPage, error) {
	if value == nil {
		return domain.CanvasPage{}, errors.New("typed canvas page is required")
	}
	items := make([]domain.Canvas, 0, len(value.GetCanvases()))
	for _, item := range value.GetCanvases() {
		decoded, err := decodeProtoCanvas(item)
		if err != nil {
			return domain.CanvasPage{}, err
		}
		items = append(items, decoded)
	}
	return domain.CanvasPage{Canvases: items, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}
func encodeProtoFile(value domain.File) *chatv1.File {
	return &chatv1.File{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Uploader: string(value.Uploader),
		Name: value.Name, Title: value.Title, MimeType: value.MIMEType, Size: value.Size,
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Deleted: value.Deleted, PublicToken: value.PublicToken,
		SharedChannels: conversationStrings(value.SharedChannels), Description: value.Description,
	}
}

func decodeProtoFile(value *chatv1.File) (domain.File, error) {
	// Title and mime_type are not required: the local path returns a stored file
	// with either one empty, so requiring them here would fail a call in the split
	// composition that succeeds in the monolith.
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUploader() == "" || value.GetName() == "" {
		return domain.File{}, errors.New("typed file response is incomplete")
	}
	if value.GetSize() < 0 {
		return domain.File{}, errors.New("typed file size is invalid")
	}
	created, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.File{}, errors.New("typed file created_at is invalid")
	}
	return domain.File{ID: domain.FileID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Uploader: domain.UserID(value.GetUploader()), Name: value.GetName(), Title: value.GetTitle(), MIMEType: value.GetMimeType(), Size: value.GetSize(), CreatedAt: created.UTC(), Deleted: value.GetDeleted(), PublicToken: value.GetPublicToken(), SharedChannels: conversationIDs(value.GetSharedChannels()), Description: value.GetDescription()}, nil
}

func encodeProtoFilePage(page domain.FilePage) *chatv1.FilePage {
	files := make([]*chatv1.File, 0, len(page.Files))
	for _, file := range page.Files {
		files = append(files, encodeProtoFile(file))
	}
	return &chatv1.FilePage{Files: files, NextCursor: string(page.NextCursor), HasMore: page.HasMore, Total: int32(page.Total)}
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
	return domain.FilePage{Files: files, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore(), Total: int(value.GetTotal())}, nil
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
	expiresAt := ""
	if !value.ExpiresAt.IsZero() {
		expiresAt = value.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return &chatv1.TokenRecord{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), AppId: string(value.AppID), BotId: string(value.BotID), Scopes: domain.NormalizeScopes(value.Scopes), TokenType: string(value.TokenType), ExpiresAt: expiresAt, Revoked: value.Revoked}
}

func decodeProtoToken(value *chatv1.TokenRecord) (domain.TokenRecord, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.TokenRecord{}, errors.New("typed token record is incomplete")
	}
	expiresAt, err := decodeOptionalProtoTime(value.GetExpiresAt())
	if err != nil {
		return domain.TokenRecord{}, err
	}
	return domain.TokenRecord{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), AppID: domain.AppID(value.GetAppId()), BotID: domain.BotID(value.GetBotId()), Scopes: domain.NormalizeScopes(value.GetScopes()), TokenType: domain.TokenType(value.GetTokenType()), ExpiresAt: expiresAt, Revoked: value.GetRevoked()}, nil
}

func encodeProtoSession(value domain.SessionRecord) *chatv1.SessionRecord {
	return &chatv1.SessionRecord{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Scopes: domain.NormalizeScopes(value.Scopes), ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano), CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), Revoked: value.Revoked, OidcProvider: value.OIDCProvider, OidcIdToken: value.OIDCIDToken, OidcSubject: value.OIDCSubject, OidcSid: value.OIDCSID}
}

func decodeProtoSession(value *chatv1.SessionRecord) (domain.SessionRecord, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.SessionRecord{}, errors.New("typed session record is incomplete")
	}
	expires, err := time.Parse(time.RFC3339Nano, value.GetExpiresAt())
	if err != nil {
		return domain.SessionRecord{}, errors.New("typed session expires_at is invalid")
	}
	return domain.SessionRecord{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Scopes: domain.NormalizeScopes(value.GetScopes()), ExpiresAt: expires.UTC(), CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), Revoked: value.GetRevoked(), OIDCProvider: value.GetOidcProvider(), OIDCIDToken: value.GetOidcIdToken(), OIDCSubject: value.GetOidcSubject(), OIDCSID: value.GetOidcSid()}, nil
}

func encodeProtoEvents(records []events.Record) *chatv1.EventsResponse {
	result := make([]*chatv1.EventRecord, 0, len(records))
	for _, record := range records {
		result = append(result, encodeProtoEventRecord(record))
	}
	return &chatv1.EventsResponse{Records: result}
}

func encodeProtoEventRecord(record events.Record) *chatv1.EventRecord {
	authorizations := make([]*chatv1.EventAuthorization, 0, len(record.Event.Authorizations))
	for _, value := range record.Event.Authorizations {
		authorizations = append(authorizations, &chatv1.EventAuthorization{
			EnterpriseId: value.EnterpriseID, TeamId: string(value.TeamID), UserId: string(value.UserID),
			IsBot: value.IsBot, IsEnterpriseInstall: value.IsEnterpriseInstall,
		})
	}
	return &chatv1.EventRecord{Sequence: record.Sequence, Id: string(record.Event.ID), WorkspaceId: string(record.Event.WorkspaceID), ActorId: string(record.Event.ActorID), Topic: record.Event.Topic, Payload: record.Event.Payload, PrivatePayload: record.Event.PrivatePayload, CreatedAtUnixNano: optionalUnixNano(record.Event.CreatedAt), Authorizations: authorizations}
}

func decodeProtoEvents(value *chatv1.EventsResponse) ([]events.Record, error) {
	if value == nil {
		return nil, errors.New("typed events response is required")
	}
	result := make([]events.Record, 0, len(value.GetRecords()))
	for _, item := range value.GetRecords() {
		record, err := decodeProtoEventRecord(item)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func decodeProtoEventRecord(item *chatv1.EventRecord) (events.Record, error) {
	if item == nil || item.GetSequence() == 0 || item.GetId() == "" || item.GetWorkspaceId() == "" || item.GetTopic() == "" || item.GetCreatedAtUnixNano() == 0 {
		return events.Record{}, errors.New("typed event record is incomplete")
	}
	authorizations := make([]events.Authorization, 0, len(item.GetAuthorizations()))
	for _, value := range item.GetAuthorizations() {
		authorizations = append(authorizations, events.Authorization{
			EnterpriseID: value.GetEnterpriseId(), TeamID: domain.WorkspaceID(value.GetTeamId()),
			UserID: domain.UserID(value.GetUserId()), IsBot: value.GetIsBot(), IsEnterpriseInstall: value.GetIsEnterpriseInstall(),
		})
	}
	return events.Record{Sequence: item.GetSequence(), Event: events.Event{ID: domain.EventID(item.GetId()), WorkspaceID: domain.WorkspaceID(item.GetWorkspaceId()), ActorID: domain.UserID(item.GetActorId()), Topic: item.GetTopic(), Payload: item.GetPayload(), PrivatePayload: item.GetPrivatePayload(), Authorizations: authorizations, CreatedAt: time.Unix(0, item.GetCreatedAtUnixNano()).UTC()}}, nil
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

func encodeProtoSavedItem(value domain.SavedItem) *chatv1.SavedItem {
	item := &chatv1.SavedItem{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		MessageId: string(value.MessageID), ConversationId: string(value.Conversation), State: string(value.State),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
		SourceAvailable: value.SourceAvailable,
	}
	if value.SourceAvailable {
		item.Message = encodeProtoMessage(value.Message)
	}
	return item
}

func decodeProtoSavedItem(value *chatv1.SavedItem) (domain.SavedItem, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetMessageId() == "" || value.GetConversationId() == "" {
		return domain.SavedItem{}, errors.New("typed saved item is incomplete")
	}
	state := domain.SavedItemState(value.GetState())
	if !state.Valid() {
		return domain.SavedItem{}, errors.New("typed saved item state is invalid")
	}
	item := domain.SavedItem{
		ID: domain.SavedItemID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		UserID: domain.UserID(value.GetUserId()), MessageID: domain.MessageID(value.GetMessageId()),
		Conversation: domain.ConversationID(value.GetConversationId()), State: state,
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
		SourceAvailable: value.GetSourceAvailable(),
	}
	if item.SourceAvailable {
		message, err := decodeProtoMessage(value.GetMessage())
		if err != nil {
			return domain.SavedItem{}, err
		}
		item.Message = message
	}
	return item, nil
}

func encodeProtoSavedItemPage(value domain.SavedItemPage) *chatv1.SavedItemPage {
	items := make([]*chatv1.SavedItem, 0, len(value.Items))
	for _, item := range value.Items {
		items = append(items, encodeProtoSavedItem(item))
	}
	return &chatv1.SavedItemPage{Items: items, NextCursor: string(value.NextCursor), HasMore: value.HasMore}
}

func decodeProtoSavedItemPage(value *chatv1.SavedItemPage) (domain.SavedItemPage, error) {
	if value == nil {
		return domain.SavedItemPage{}, errors.New("typed saved item page is required")
	}
	items := make([]domain.SavedItem, 0, len(value.GetItems()))
	for _, item := range value.GetItems() {
		decoded, err := decodeProtoSavedItem(item)
		if err != nil {
			return domain.SavedItemPage{}, err
		}
		items = append(items, decoded)
	}
	return domain.SavedItemPage{Items: items, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}, nil
}

func encodeProtoBookmark(value domain.Bookmark) *chatv1.Bookmark {
	return &chatv1.Bookmark{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), ConversationId: string(value.Conversation), Title: value.Title, Type: string(value.Type), Link: value.Link, Emoji: value.Emoji, EntityId: value.EntityID, AccessLevel: value.AccessLevel, ParentId: string(value.ParentID), CreatedAt: value.CreatedAt.UTC().Unix(), UpdatedAt: value.UpdatedAt.UTC().Unix(), UpdatedBy: string(value.UpdatedBy)}
}

func decodeProtoBookmark(value *chatv1.Bookmark) (domain.Bookmark, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetConversationId() == "" || value.GetTitle() == "" || value.GetType() == "" || value.GetUpdatedBy() == "" {
		return domain.Bookmark{}, errors.New("typed bookmark is incomplete")
	}
	return domain.Bookmark{ID: domain.BookmarkID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversationId()), Title: value.GetTitle(), Type: domain.BookmarkType(value.GetType()), Link: value.GetLink(), Emoji: value.GetEmoji(), EntityID: value.GetEntityId(), AccessLevel: value.GetAccessLevel(), ParentID: domain.BookmarkID(value.GetParentId()), CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), UpdatedAt: time.Unix(value.GetUpdatedAt(), 0).UTC(), UpdatedBy: domain.UserID(value.GetUpdatedBy())}, nil
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

func encodeProtoLaterReminder(value domain.LaterReminder) *chatv1.LaterReminder {
	return &chatv1.LaterReminder{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), CreatorId: string(value.Creator),
		UserId: string(value.UserID), ChannelId: string(value.Channel), SourceMessageId: string(value.SourceMessageID),
		SourceConversationId: string(value.SourceConversation), SourceTimestamp: string(value.SourceTimestamp),
		Target: string(value.Target), Text: value.Text,
		DueAt: unixOrZero(value.DueAt), Timezone: value.TimeZone, Recurrence: string(value.Recurrence),
		RecurrenceAnchor: unixOrZero(value.RecurrenceAnchor),
		CreatedAt:        unixOrZero(value.CreatedAt), UpdatedAt: unixOrZero(value.UpdatedAt),
		CompletedAt: unixOrZero(value.CompletedAt), LastDeliveredAt: unixOrZero(value.LastDeliveredAt),
		AcknowledgedAt: unixOrZero(value.AcknowledgedAt), FailedAt: unixOrZero(value.FailedAt), FailureCode: value.FailureCode,
	}
}

func decodeProtoLaterReminder(value *chatv1.LaterReminder) (domain.LaterReminder, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetCreatorId() == "" ||
		value.GetText() == "" || value.GetDueAt() <= 0 || value.GetCreatedAt() <= 0 || value.GetUpdatedAt() <= 0 ||
		value.GetTimezone() == "" {
		return domain.LaterReminder{}, errors.New("typed Later reminder is incomplete")
	}
	target := domain.LaterReminderTarget(value.GetTarget())
	recurrence := domain.ReminderRecurrence(value.GetRecurrence())
	if !target.Valid() || !recurrence.Valid() ||
		(target == domain.LaterReminderPersonal && (value.GetUserId() == "" || value.GetChannelId() != "")) ||
		(target == domain.LaterReminderChannel && (value.GetChannelId() == "" || value.GetUserId() != "")) ||
		((value.GetSourceMessageId() == "") != (value.GetSourceConversationId() == "")) ||
		((value.GetSourceMessageId() == "") != (value.GetSourceTimestamp() == "")) {
		return domain.LaterReminder{}, errors.New("typed Later reminder has invalid targeting")
	}
	return domain.LaterReminder{
		ID: domain.LaterReminderID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Creator: domain.UserID(value.GetCreatorId()), UserID: domain.UserID(value.GetUserId()),
		Channel: domain.ConversationID(value.GetChannelId()), SourceMessageID: domain.MessageID(value.GetSourceMessageId()),
		SourceConversation: domain.ConversationID(value.GetSourceConversationId()), SourceTimestamp: domain.MessageTimestamp(value.GetSourceTimestamp()),
		Target: target, Text: value.GetText(),
		DueAt: timeFromUnix(value.GetDueAt()), TimeZone: value.GetTimezone(), Recurrence: recurrence,
		RecurrenceAnchor: timeFromUnix(value.GetRecurrenceAnchor()),
		CreatedAt:        timeFromUnix(value.GetCreatedAt()), UpdatedAt: timeFromUnix(value.GetUpdatedAt()),
		CompletedAt: timeFromUnix(value.GetCompletedAt()), LastDeliveredAt: timeFromUnix(value.GetLastDeliveredAt()),
		AcknowledgedAt: timeFromUnix(value.GetAcknowledgedAt()), FailedAt: timeFromUnix(value.GetFailedAt()), FailureCode: value.GetFailureCode(),
	}, nil
}

func encodeProtoActivityItem(value domain.ActivityItem) *chatv1.ActivityItem {
	kinds := make([]string, 0, len(value.Kinds))
	for _, kind := range value.Kinds {
		kinds = append(kinds, string(kind))
	}
	result := &chatv1.ActivityItem{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		Kinds: kinds, ActorId: string(value.ActorID), ConversationId: string(value.Conversation),
		MessageId: string(value.MessageID), ReminderId: string(value.ReminderID), AppReminderId: string(value.AppReminderID),
		ReactionName: value.ReactionName, OccurredAt: value.OccurredAt.UTC().UnixNano(),
		SourceAvailable: value.SourceAvailable,
		CanvasId:        string(value.CanvasID), CanvasTitle: value.CanvasTitle,
		ListItemId: string(value.ListItemID), ListId: string(value.ListID), ListName: value.ListName,
		SharedInviteId: string(value.SharedInviteID), SharedInviteStatus: string(value.SharedInviteStatus),
		ListItem: encodeProtoListItemSummary(value.ListItem),
	}
	if !value.ReadAt.IsZero() {
		result.ReadAt = value.ReadAt.UTC().UnixNano()
	}
	if !value.ClearedAt.IsZero() {
		result.ClearedAt = value.ClearedAt.UTC().UnixNano()
	}
	if value.SourceAvailable && value.Message.ID != "" {
		result.Message = encodeProtoMessage(value.Message)
	}
	if value.SourceAvailable && value.Reminder.ID != "" {
		result.Reminder = encodeProtoLaterReminder(value.Reminder)
	}
	if value.SourceAvailable && value.AppReminder.ID != "" {
		result.AppReminder = encodeProtoReminder(value.AppReminder)
	}
	return result
}

func decodeProtoActivityItem(value *chatv1.ActivityItem) (domain.ActivityItem, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetOccurredAt() == 0 || len(value.GetKinds()) == 0 {
		return domain.ActivityItem{}, errors.New("typed activity item is incomplete")
	}
	item := domain.ActivityItem{
		ID: domain.ActivityID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		UserID: domain.UserID(value.GetUserId()), ActorID: domain.UserID(value.GetActorId()),
		Conversation: domain.ConversationID(value.GetConversationId()), MessageID: domain.MessageID(value.GetMessageId()),
		ReminderID: domain.LaterReminderID(value.GetReminderId()), AppReminderID: domain.ReminderID(value.GetAppReminderId()), ReactionName: value.GetReactionName(),
		OccurredAt: time.Unix(0, value.GetOccurredAt()).UTC(), SourceAvailable: value.GetSourceAvailable(),
		CanvasID: domain.CanvasID(value.GetCanvasId()), CanvasTitle: value.GetCanvasTitle(),
		ListItemID: domain.ListItemID(value.GetListItemId()), ListID: domain.ListID(value.GetListId()), ListName: value.GetListName(),
		SharedInviteID: domain.SharedInviteID(value.GetSharedInviteId()), SharedInviteStatus: domain.SharedInviteStatus(value.GetSharedInviteStatus()),
		ListItem: decodeProtoListItemSummary(value.GetListItem()),
	}
	for _, encoded := range value.GetKinds() {
		kind := domain.ActivityKind(encoded)
		if !kind.Valid() {
			return domain.ActivityItem{}, errors.New("typed activity item has an invalid kind")
		}
		item.Kinds = append(item.Kinds, kind)
	}
	if value.GetReadAt() != 0 {
		item.ReadAt = time.Unix(0, value.GetReadAt()).UTC()
	}
	if value.GetClearedAt() != 0 {
		item.ClearedAt = time.Unix(0, value.GetClearedAt()).UTC()
	}
	var err error
	if value.GetMessage() != nil {
		item.Message, err = decodeProtoMessage(value.GetMessage())
		if err != nil {
			return domain.ActivityItem{}, err
		}
	}
	if value.GetReminder() != nil {
		item.Reminder, err = decodeProtoLaterReminder(value.GetReminder())
		if err != nil {
			return domain.ActivityItem{}, err
		}
	}
	if value.GetAppReminder() != nil {
		item.AppReminder, err = decodeProtoReminder(value.GetAppReminder())
		if err != nil {
			return domain.ActivityItem{}, err
		}
	}
	return item, nil
}

func encodeProtoActivityPreferences(value domain.ActivityPreferences) *chatv1.ActivityPreferences {
	views := make([]*chatv1.ActivitySavedView, 0, len(value.SavedViews))
	for _, view := range value.SavedViews {
		views = append(views, encodeProtoActivitySavedView(view))
	}
	return &chatv1.ActivityPreferences{WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Layout: string(value.Layout), SavedViews: views}
}

func decodeProtoActivityPreferences(value *chatv1.ActivityPreferences) domain.ActivityPreferences {
	if value == nil {
		return domain.ActivityPreferences{}
	}
	preferences := domain.ActivityPreferences{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), Layout: domain.ActivityLayout(value.GetLayout())}
	for _, view := range value.GetSavedViews() {
		preferences.SavedViews = append(preferences.SavedViews, decodeProtoActivitySavedView(view))
	}
	return preferences
}

func encodeProtoActivitySavedView(value domain.ActivitySavedView) *chatv1.ActivitySavedView {
	kinds := make([]string, 0, len(value.Kinds))
	for _, kind := range value.Kinds {
		kinds = append(kinds, string(kind))
	}
	return &chatv1.ActivitySavedView{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		Name: value.Name, Kinds: kinds, CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt),
	}
}

func decodeProtoActivitySavedView(value *chatv1.ActivitySavedView) domain.ActivitySavedView {
	if value == nil {
		return domain.ActivitySavedView{}
	}
	view := domain.ActivitySavedView{
		ID: domain.ActivitySavedViewID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		UserID: domain.UserID(value.GetUserId()), Name: value.GetName(), CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
	}
	for _, kind := range value.GetKinds() {
		view.Kinds = append(view.Kinds, domain.ActivityKind(kind))
	}
	return view
}

func encodeProtoWorkspaceNotificationPreferences(value domain.WorkspaceNotificationPreferences) *chatv1.WorkspaceNotificationPreferences {
	vips := make([]string, 0, len(value.VIPs))
	for _, id := range value.VIPs {
		vips = append(vips, string(id))
	}
	return &chatv1.WorkspaceNotificationPreferences{
		WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), Level: string(value.Level),
		Keywords: append([]string(nil), value.Keywords...), ActivityChannels: value.ActivityChannels, ActivityReminders: value.ActivityReminders,
		BrowserNotifications: value.BrowserNotifications,
		Schedule:             encodeProtoNotificationSchedule(value.Schedule),
		Vips:                 vips,
	}
}

func encodeProtoNotificationSchedule(value domain.NotificationSchedule) *chatv1.NotificationSchedule {
	days := make([]int32, 0, len(value.Days))
	for _, day := range value.Days {
		days = append(days, int32(day))
	}
	return &chatv1.NotificationSchedule{
		Enabled: value.Enabled, Days: days,
		StartMinute: int32(value.StartMinute), EndMinute: int32(value.EndMinute), TimeZone: value.TimeZone,
	}
}

func decodeProtoNotificationSchedule(value *chatv1.NotificationSchedule) domain.NotificationSchedule {
	if value == nil {
		return domain.NotificationSchedule{}
	}
	var days []time.Weekday
	for _, day := range value.GetDays() {
		days = append(days, time.Weekday(day))
	}
	return domain.NotificationSchedule{
		Enabled: value.GetEnabled(), Days: days,
		StartMinute: int(value.GetStartMinute()), EndMinute: int(value.GetEndMinute()), TimeZone: value.GetTimeZone(),
	}
}

func decodeProtoWorkspaceNotificationPreferences(value *chatv1.WorkspaceNotificationPreferences) (domain.WorkspaceNotificationPreferences, error) {
	if value == nil {
		return domain.WorkspaceNotificationPreferences{}, errors.New("typed workspace notification preferences are incomplete")
	}
	preferences := domain.WorkspaceNotificationPreferences{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()),
		Level: domain.NotificationLevel(value.GetLevel()), Keywords: domain.NormalizeNotificationKeywords(value.GetKeywords()),
		ActivityChannels: value.GetActivityChannels(), ActivityReminders: value.GetActivityReminders(),
		BrowserNotifications: value.GetBrowserNotifications(),
		Schedule:             decodeProtoNotificationSchedule(value.GetSchedule()),
	}
	for _, id := range value.GetVips() {
		preferences.VIPs = append(preferences.VIPs, domain.UserID(id))
	}
	preferences.VIPs = domain.NormalizeUserIDs(preferences.VIPs)
	if !preferences.Valid() {
		return domain.WorkspaceNotificationPreferences{}, errors.New("typed workspace notification preferences are invalid")
	}
	return preferences, nil
}

func encodeProtoConversationNotificationPreferences(value domain.ConversationNotificationPreferences) *chatv1.ConversationNotificationPreferences {
	return &chatv1.ConversationNotificationPreferences{
		WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), ConversationId: string(value.Conversation),
		Level: string(value.Level), FollowEveryThread: value.FollowEveryThread,
	}
}

func decodeProtoConversationNotificationPreferences(value *chatv1.ConversationNotificationPreferences) (domain.ConversationNotificationPreferences, error) {
	if value == nil {
		return domain.ConversationNotificationPreferences{}, errors.New("typed conversation notification preferences are incomplete")
	}
	preferences := domain.ConversationNotificationPreferences{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()),
		Conversation: domain.ConversationID(value.GetConversationId()), Level: domain.NotificationLevel(value.GetLevel()),
		FollowEveryThread: value.GetFollowEveryThread(),
	}
	if !preferences.Valid() {
		return domain.ConversationNotificationPreferences{}, errors.New("typed conversation notification preferences are invalid")
	}
	return preferences, nil
}

func encodeProtoScheduledMessage(value domain.ScheduledMessage) *chatv1.ScheduledMessage {
	return &chatv1.ScheduledMessage{
		WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), ChannelId: string(value.Channel), AuthorId: string(value.Author),
		Text: value.Text, Blocks: value.Blocks, Attachments: value.Attachments, PostAt: value.PostAt.Unix(), CreatedAt: value.CreatedAt.Unix(),
		AppId: string(value.AppID), BotId: string(value.BotID), CredentialHash: value.CredentialHash, ThreadTs: string(value.ThreadTimestamp),
		Metadata: value.Metadata, StreamState: value.StreamState,
		DeliveredAt: unixOrZero(value.DeliveredAt), FailedAt: unixOrZero(value.FailedAt), FailureCode: value.FailureCode,
		FileAttachments: encodeProtoDraftAttachments(value.FileAttachments),
	}
}

func grpcScheduledCredential(workspaceID domain.WorkspaceID, userID domain.UserID) string {
	return domain.HashToken("internal-scheduled\x00" + string(workspaceID) + "\x00" + string(userID))
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Unix()
}

func timeFromUnix(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func decodeProtoScheduledMessage(value *chatv1.ScheduledMessage) (domain.ScheduledMessage, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetChannelId() == "" || value.GetAuthorId() == "" || (value.GetText() == "" && value.GetBlocks() == "" && value.GetAttachments() == "" && len(value.GetFileAttachments()) == 0) || value.GetPostAt() <= 0 || value.GetCreatedAt() <= 0 {
		return domain.ScheduledMessage{}, errors.New("typed scheduled message is incomplete")
	}
	return domain.ScheduledMessage{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.ScheduledMessageID(value.GetId()),
		Channel: domain.ConversationID(value.GetChannelId()), Author: domain.UserID(value.GetAuthorId()),
		AppID: domain.AppID(value.GetAppId()), BotID: domain.BotID(value.GetBotId()), CredentialHash: value.GetCredentialHash(),
		Text: value.GetText(), Blocks: value.GetBlocks(), Attachments: value.GetAttachments(), Metadata: value.GetMetadata(), StreamState: value.GetStreamState(),
		ThreadTimestamp: domain.MessageTimestamp(value.GetThreadTs()), PostAt: time.Unix(value.GetPostAt(), 0).UTC(),
		CreatedAt: time.Unix(value.GetCreatedAt(), 0).UTC(), DeliveredAt: timeFromUnix(value.GetDeliveredAt()),
		FailedAt: timeFromUnix(value.GetFailedAt()), FailureCode: value.GetFailureCode(),
		FileAttachments: decodeProtoDraftAttachments(value.GetFileAttachments()),
	}, nil
}

func encodeProtoDraft(value domain.Draft) *chatv1.Draft {
	return &chatv1.Draft{
		WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), ConversationId: string(value.ConversationID),
		ThreadTs: string(value.ThreadTimestamp), Text: value.Text, UpdatedAtUnixNano: value.UpdatedAt.UTC().UnixNano(),
		Attachments: encodeProtoDraftAttachments(value.Attachments),
	}
}

func encodeProtoDraftAttachments(values []domain.DraftAttachment) []*chatv1.DraftAttachment {
	result := make([]*chatv1.DraftAttachment, 0, len(values))
	for _, value := range values {
		result = append(result, &chatv1.DraftAttachment{
			UploadId: string(value.UploadID), Name: value.Name, Title: value.Title, MimeType: value.MIMEType, Size: value.Size,
		})
	}
	return result
}

func decodeProtoDraftAttachments(values []*chatv1.DraftAttachment) []domain.DraftAttachment {
	result := make([]domain.DraftAttachment, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, domain.DraftAttachment{
			UploadID: domain.ExternalUploadID(value.GetUploadId()), Name: value.GetName(), Title: value.GetTitle(),
			MIMEType: value.GetMimeType(), Size: value.GetSize(),
		})
	}
	return result
}

func decodeProtoDraft(value *chatv1.Draft) (domain.Draft, error) {
	if value == nil || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetConversationId() == "" ||
		(value.GetText() == "" && len(value.GetAttachments()) == 0) || value.GetUpdatedAtUnixNano() <= 0 {
		return domain.Draft{}, errors.New("typed draft is incomplete")
	}
	return domain.Draft{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()),
		ConversationID: domain.ConversationID(value.GetConversationId()), ThreadTimestamp: domain.MessageTimestamp(value.GetThreadTs()),
		Text: value.GetText(), Attachments: decodeProtoDraftAttachments(value.GetAttachments()),
		UpdatedAt: time.Unix(0, value.GetUpdatedAtUnixNano()).UTC(),
	}, nil
}

func decodeProtoUser(value *chatv1.User) (domain.User, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetName() == "" {
		return domain.User{}, errors.New("typed user response is incomplete")
	}
	// Presence and profile are decoded as sent. Requiring "auto" or "away" and a
	// non-nil profile made the remote path reject a record the local path
	// returns: the store defaults presence today, so the rejection was
	// unreachable, but it was an invariant that existed only across the seam and
	// would have turned a stored user into a hard error in one composition and a
	// value in the other. The invariant belongs to the store, not to a decoder.
	presence := domain.Presence(value.GetPresence())
	profile := value.GetProfile()
	result := domain.User{
		ID:          domain.UserID(value.GetId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Email:       value.GetEmail(),
		Name:        value.GetName(),
		RealName:    value.GetRealName(),
		Profile: domain.UserProfile{
			DisplayName:             profile.GetDisplayName(),
			StatusText:              profile.GetStatusText(),
			StatusEmoji:             profile.GetStatusEmoji(),
			Image24:                 profile.GetImage_24(),
			Image32:                 profile.GetImage_32(),
			Image48:                 profile.GetImage_48(),
			Image72:                 profile.GetImage_72(),
			Image192:                profile.GetImage_192(),
			Image512:                profile.GetImage_512(),
			Image1024:               profile.GetImage_1024(),
			ActiveScheduledStatusID: domain.ScheduledStatusID(profile.GetActiveScheduledStatusId()),
		},
		Presence: presence,
		Deleted:  value.GetDeleted(),
	}
	if profile.GetStatusExpiration() != 0 {
		result.Profile.StatusExpiration = time.Unix(profile.GetStatusExpiration(), 0).UTC()
	}
	if value.GetLastActiveAtUnixNano() != 0 {
		result.LastActiveAt = time.Unix(0, value.GetLastActiveAtUnixNano()).UTC()
	}
	return result, nil
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
	// created_at and updated_at are Unix timestamps and are decoded as sent; the
	// local path does not require them to be positive.
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetName() == "" || value.GetHandle() == "" || value.GetCreatorId() == "" || value.GetUpdatedBy() == "" {
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
	result := &chatv1.Call{WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), ExternalUniqueId: value.ExternalUniqueID, ExternalDisplayId: value.ExternalDisplayID, JoinUrl: value.JoinURL, DesktopAppJoinUrl: value.DesktopAppJoinURL, Title: value.Title, CreatedBy: string(value.CreatedBy), Participants: participants, StartedAt: value.StartedAt.Unix(), DurationSeconds: value.DurationSeconds, Kind: string(value.Kind), ConversationId: string(value.ConversationID)}
	if !value.EndedAt.IsZero() {
		result.EndedAt = value.EndedAt.Unix()
	}
	return result
}

func decodeProtoCall(value *chatv1.Call) (domain.Call, error) {
	// started_at is not required to be positive: it is a Unix timestamp, and the
	// local path returns calls whose start is unset or predates the epoch.
	if value == nil || value.GetWorkspaceId() == "" || value.GetId() == "" || value.GetCreatedBy() == "" {
		return domain.Call{}, errors.New("typed call response is incomplete")
	}
	// The external identity is required of an external call and meaningless for
	// a huddle, which has a conversation instead. Requiring it of both refused
	// every huddle that crossed the seam.
	kind := domain.CallKind(value.GetKind())
	if kind == "" {
		kind = domain.CallKindExternal
	}
	switch kind {
	case domain.CallKindExternal:
		if value.GetExternalUniqueId() == "" || value.GetJoinUrl() == "" {
			return domain.Call{}, errors.New("typed external call response is incomplete")
		}
	case domain.CallKindHuddle:
		if value.GetConversationId() == "" {
			return domain.Call{}, errors.New("typed huddle response carries no conversation")
		}
	default:
		return domain.Call{}, errors.New("typed call response names an unknown kind")
	}
	participants := make([]domain.UserID, 0, len(value.GetParticipants()))
	for _, user := range value.GetParticipants() {
		if user == "" {
			return domain.Call{}, errors.New("typed call participant is empty")
		}
		participants = append(participants, domain.UserID(user))
	}
	result := domain.Call{WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.CallID(value.GetId()), ExternalUniqueID: value.GetExternalUniqueId(), ExternalDisplayID: value.GetExternalDisplayId(), JoinURL: value.GetJoinUrl(), DesktopAppJoinURL: value.GetDesktopAppJoinUrl(), Title: value.GetTitle(), CreatedBy: domain.UserID(value.GetCreatedBy()), Participants: participants, StartedAt: time.Unix(value.GetStartedAt(), 0).UTC(), DurationSeconds: value.GetDurationSeconds(), Kind: kind, ConversationID: domain.ConversationID(value.GetConversationId())}
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
	return domain.OpenIDToken{OAuthToken: decodeOAuthToken(oauthToken), IDToken: out.GetIdToken(), RefreshToken: out.GetRefreshToken()}, nil
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
	return &chatv1.OpenIDConnectTokenResponse{OauthToken: encodeOAuthToken(value.OAuthToken), IdToken: value.IDToken, RefreshToken: value.RefreshToken}, nil
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
	return r.PostWithBlocksAndAttachments(ctx, workspaceID, userID, conversationID, text, blocks, "", threadTimestamp, idempotencyKey, "")
}

func (r Remote) UpdateWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, text, blocks string) (domain.Message, error) {
	return r.UpdateWithBlocksAndAttachments(ctx, workspaceID, userID, conversationID, timestamp, text, blocks, "")
}

func (s *Server) PostWithBlocks(ctx context.Context, input *chatv1.PostWithBlocksRequest) (*chatv1.Message, error) {
	request := decodeProtoMessagePostRequest(input)
	value, err := s.implementation.PostMessageAs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func decodeProtoMessagePostRequest(input *chatv1.PostWithBlocksRequest) domain.MessagePostRequest {
	request := domain.MessagePostRequest{
		Conversation: domain.ConversationID(input.GetConversationId()), Text: input.GetText(),
		Blocks: input.GetBlocks(), Attachments: input.GetAttachments(), Metadata: input.GetMetadata(),
		ThreadTimestamp: domain.MessageTimestamp(input.GetThreadTimestamp()), IdempotencyKey: input.GetIdempotencyKey(),
		AppID: domain.AppID(input.GetAppId()), MarkdownText: input.GetMarkdownText(),
		ReplyBroadcast: input.GetReplyBroadcast(), Parse: input.GetParse(), MrkdwnDisabled: input.GetMrkdwnDisabled(),
		LinkNames: input.GetLinkNames(), Username: input.GetUsername(), IconEmoji: input.GetIconEmoji(), IconURL: input.GetIconUrl(),
	}
	if input.GetUnfurlLinksSet() {
		value := input.GetUnfurlLinks()
		request.UnfurlLinks = &value
	}
	if input.GetUnfurlMediaSet() {
		value := input.GetUnfurlMedia()
		request.UnfurlMedia = &value
	}
	return request
}

func (s *Server) UpdateWithBlocks(ctx context.Context, input *chatv1.UpdateWithBlocksRequest) (*chatv1.Message, error) {
	value, err := s.implementation.UpdateWithBlocksAndAttachments(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), input.GetText(), input.GetBlocks(), input.GetAttachments())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) UpdateMessage(ctx context.Context, input *chatv1.UpdateMessageRequest) (*chatv1.Message, error) {
	patch := domain.MessagePatch{Text: input.Text, Blocks: input.Blocks, Attachments: input.Attachments}
	value, err := s.implementation.UpdateMessage(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()), patch)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) StartMessageStream(ctx context.Context, input *chatv1.StartMessageStreamRequest) (*chatv1.Message, error) {
	value, err := s.implementation.StartMessageStream(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.MessageStreamStart{
		Conversation: domain.ConversationID(input.GetConversationId()), ThreadTimestamp: domain.MessageTimestamp(input.GetThreadTimestamp()),
		AppID: domain.AppID(input.GetAppId()), BotID: domain.BotID(input.GetBotId()), RecipientTeamID: domain.WorkspaceID(input.GetRecipientTeamId()),
		RecipientUserID: domain.UserID(input.GetRecipientUserId()), MarkdownText: input.GetMarkdownText(), Chunks: input.GetChunks(),
		TaskDisplayMode: input.GetTaskDisplayMode(), Username: input.GetUsername(),
		IconEmoji: input.GetIconEmoji(), IconURL: input.GetIconUrl(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) AppendMessageStream(ctx context.Context, input *chatv1.MutateMessageStreamRequest) (*chatv1.Message, error) {
	value, err := s.implementation.AppendMessageStream(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeStreamMutation(input))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func (s *Server) StopMessageStream(ctx context.Context, input *chatv1.MutateMessageStreamRequest) (*chatv1.Message, error) {
	value, err := s.implementation.StopMessageStream(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeStreamMutation(input))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessage(value), nil
}

func decodeStreamMutation(input *chatv1.MutateMessageStreamRequest) domain.MessageStreamMutation {
	return domain.MessageStreamMutation{
		Conversation: domain.ConversationID(input.GetConversationId()), Timestamp: domain.MessageTimestamp(input.GetTimestamp()),
		AppID: domain.AppID(input.GetAppId()), MarkdownText: input.GetMarkdownText(), Chunks: input.GetChunks(),
		Blocks: input.GetBlocks(), Metadata: input.GetMetadata(),
	}
}

func (r Remote) ScheduleMessageWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text, blocks string, postAt time.Time) (domain.ScheduledMessage, error) {
	return r.ScheduleMessageWithBlocksAndAttachments(ctx, workspaceID, userID, channel, text, blocks, "", postAt)
}

func (r Remote) PostEphemeralWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, recipientID domain.UserID, text, blocks string) (domain.EphemeralMessage, error) {
	return r.PostEphemeralWithBlocksAndAttachments(ctx, workspaceID, userID, conversationID, recipientID, text, blocks, "", "")
}

func (r Remote) PostWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, text, blocks, attachments string, threadTimestamp domain.MessageTimestamp, idempotencyKey string, appID domain.AppID) (domain.Message, error) {
	return r.PostMessageAs(ctx, workspaceID, userID, domain.MessagePostRequest{
		Conversation: conversationID, Text: text, Blocks: blocks, Attachments: attachments,
		ThreadTimestamp: threadTimestamp, IdempotencyKey: idempotencyKey, AppID: appID,
	})
}

func (r Remote) PostMessageAs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessagePostRequest) (domain.Message, error) {
	input := &chatv1.PostWithBlocksRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(request.Conversation),
		Text: request.Text, Blocks: request.Blocks, Attachments: request.Attachments, Metadata: request.Metadata,
		ThreadTimestamp: string(request.ThreadTimestamp), IdempotencyKey: request.IdempotencyKey, AppId: string(request.AppID),
		MarkdownText: request.MarkdownText, ReplyBroadcast: request.ReplyBroadcast, Parse: request.Parse,
		MrkdwnDisabled: request.MrkdwnDisabled, LinkNames: request.LinkNames,
		Username: request.Username, IconEmoji: request.IconEmoji, IconUrl: request.IconURL,
	}
	if request.UnfurlLinks != nil {
		input.UnfurlLinksSet = true
		input.UnfurlLinks = *request.UnfurlLinks
	}
	if request.UnfurlMedia != nil {
		input.UnfurlMediaSet = true
		input.UnfurlMedia = *request.UnfurlMedia
	}
	out, err := r.messages.PostWithBlocks(ctx, input)
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) StartMessageStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamStart) (domain.Message, error) {
	out, err := r.messages.StartMessageStream(ctx, &chatv1.StartMessageStreamRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(request.Conversation),
		ThreadTimestamp: string(request.ThreadTimestamp), AppId: string(request.AppID),
		BotId: string(request.BotID), TaskDisplayMode: request.TaskDisplayMode,
		Username: request.Username, IconEmoji: request.IconEmoji, IconUrl: request.IconURL,
		RecipientTeamId: string(request.RecipientTeamID), RecipientUserId: string(request.RecipientUserID),
		MarkdownText: request.MarkdownText, Chunks: request.Chunks,
	})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) AppendMessageStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamMutation) (domain.Message, error) {
	out, err := r.messages.AppendMessageStream(ctx, encodeStreamMutation(workspaceID, userID, request))
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) StopMessageStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamMutation) (domain.Message, error) {
	out, err := r.messages.StopMessageStream(ctx, encodeStreamMutation(workspaceID, userID, request))
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func encodeStreamMutation(workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamMutation) *chatv1.MutateMessageStreamRequest {
	return &chatv1.MutateMessageStreamRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(request.Conversation),
		Timestamp: string(request.Timestamp), AppId: string(request.AppID), MarkdownText: request.MarkdownText,
		Chunks: request.Chunks, Blocks: request.Blocks, Metadata: request.Metadata,
	}
}

func (r Remote) PostIncomingWebhookWithAttachments(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret, text, blocks, attachments string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	out, err := r.messages.PostIncomingWebhook(ctx, &chatv1.IncomingWebhookPostRequest{WorkspaceId: string(workspaceID), AppId: string(appID), Secret: secret, Text: text, Blocks: blocks, Attachments: attachments, ThreadTimestamp: string(threadTimestamp), IdempotencyKey: idempotencyKey})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) PostEphemeralWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, recipientID domain.UserID, text, blocks, attachments string, appID domain.AppID) (domain.EphemeralMessage, error) {
	out, err := r.messages.PostEphemeral(ctx, &chatv1.PostEphemeralRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), RecipientId: string(recipientID), Text: text, Blocks: blocks, Attachments: attachments, AppId: string(appID)})
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	return decodeProtoEphemeralMessage(out)
}

func (r Remote) ListEphemeralMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, limit int) ([]domain.EphemeralMessage, error) {
	out, err := r.messages.ListEphemeral(ctx, &chatv1.EphemeralMessagesRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.EphemeralMessage, 0, len(out.GetMessages()))
	for _, item := range out.GetMessages() {
		value, err := decodeProtoEphemeralMessage(item)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (r Remote) UpdateWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, text, blocks, attachments string) (domain.Message, error) {
	out, err := r.messages.UpdateWithBlocks(ctx, &chatv1.UpdateWithBlocksRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp), Text: text, Blocks: blocks, Attachments: attachments})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) UpdateMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, patch domain.MessagePatch) (domain.Message, error) {
	out, err := r.messages.UpdateMessage(ctx, &chatv1.UpdateMessageRequest{
		WorkspaceId:    string(workspaceID),
		UserId:         string(userID),
		ConversationId: string(conversationID),
		Timestamp:      string(timestamp),
		Text:           patch.Text,
		Blocks:         patch.Blocks,
		Attachments:    patch.Attachments,
	})
	if err != nil {
		return domain.Message{}, err
	}
	return decodeProtoMessage(out)
}

func (r Remote) ScheduleMessageWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text, blocks, attachments string, postAt time.Time) (domain.ScheduledMessage, error) {
	return r.ScheduleMessageAs(ctx, workspaceID, userID, domain.ScheduledMessageRequest{
		Channel: channel, Text: text, Blocks: blocks, Attachments: attachments, PostAt: postAt,
		CredentialHash: grpcScheduledCredential(workspaceID, userID),
	})
}

func (r Remote) ScheduleMessageAs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ScheduledMessageRequest) (domain.ScheduledMessage, error) {
	out, err := r.scheduled.ScheduleMessage(ctx, &chatv1.ScheduleMessageRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(request.Channel),
		Text: request.Text, Blocks: request.Blocks, Attachments: request.Attachments, PostAt: request.PostAt.Unix(),
		AppId: string(request.AppID), BotId: string(request.BotID), CredentialHash: request.CredentialHash,
		ThreadTs: string(request.ThreadTimestamp), Metadata: request.Metadata, StreamState: request.StreamState,
		FileAttachments: encodeProtoDraftAttachments(request.FileAttachments),
	})
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
		return uploadFailure(err, func() error { _, recvErr := stream.CloseAndRecv(); return recvErr })
	}
	if err := sendChunks(source, "external upload source", func(chunk []byte) error {
		return stream.Send(&chatv1.ExternalUploadPart{Part: &chatv1.ExternalUploadPart_Chunk{Chunk: append([]byte(nil), chunk...)}})
	}); err != nil {
		return uploadFailure(err, func() error { _, recvErr := stream.CloseAndRecv(); return recvErr })
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
	if header == nil {
		return invalidArgument("upload stream must begin with metadata")
	}
	reader, writer := io.Pipe()
	readErr := make(chan error, 1)
	go func() {
		defer writer.Close()
		// A panic on this stack is not reachable by the stream interceptor; see
		// Server.UploadFile.
		defer func() {
			if recovered := recover(); recovered != nil {
				readErr <- panicError(recovered)
			}
		}()
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
				readErr <- invalidArgument("external upload stream contains a non-chunk part")
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
	file, err := s.implementation.CompleteExternalUpload(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ExternalUploadID(input.GetUploadId()), input.GetTitle(), conversationIDs(input.GetChannelIds()), input.GetInitialComment(), input.GetBlocks(), domain.MessageTimestamp(input.GetThreadTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFile(file), nil
}

// encodeProtoExternalUpload formats in UTC, like every other encoder on this
// contract. It was the only one that formatted in time.Local, so a chat process
// running outside UTC emitted offset-bearing timestamps where the rest of the
// surface emits Z.
func encodeProtoExternalUpload(value domain.ExternalUpload) *chatv1.ExternalUpload {
	result := &chatv1.ExternalUpload{Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), Uploader: string(value.Uploader), Name: value.Name, Title: value.Title, MimeType: value.MIMEType, Size: value.Size, Status: string(value.Status), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	if !value.UploadedAt.IsZero() {
		result.UploadedAt = value.UploadedAt.UTC().Format(time.RFC3339Nano)
	}
	if !value.CompletedAt.IsZero() {
		result.CompletedAt = value.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
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
	// uploaded_at and completed_at were carried on the wire and dropped here, so
	// CreateExternalUpload and CompleteExternalUpload returned uploads whose
	// UploadedAt and CompletedAt were populated in the monolith and zero across
	// the seam. Both are optional, so an absent value stays zero rather than
	// failing the decode.
	uploaded, err := decodeOptionalProtoTime(value.GetUploadedAt())
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	completed, err := decodeOptionalProtoTime(value.GetCompletedAt())
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	return domain.ExternalUpload{ID: domain.ExternalUploadID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Uploader: domain.UserID(value.GetUploader()), Name: value.GetName(), Title: value.GetTitle(), MIMEType: value.GetMimeType(), Size: value.GetSize(), Status: domain.ExternalUploadStatus(value.GetStatus()), CreatedAt: created, ExpiresAt: expires, UploadedAt: uploaded, CompletedAt: completed}, nil
}

// decodeOptionalProtoTime decodes an RFC3339Nano timestamp that the sender omits
// when it has no value, distinguishing "absent" from "malformed" instead of
// silently zeroing both.
func decodeOptionalProtoTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func (r Remote) CompleteExternalUploads(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, completions []domain.ExternalUploadCompletion, channels []domain.ConversationID, initialComment, blocks string, threadTimestamp domain.MessageTimestamp) ([]domain.File, error) {
	entries := make([]*chatv1.ExternalUploadCompletion, 0, len(completions))
	for _, completion := range completions {
		entries = append(entries, &chatv1.ExternalUploadCompletion{UploadId: string(completion.ID), Title: completion.Title})
	}
	out, err := r.files.CompleteExternalUploads(ctx, &chatv1.CompleteExternalUploadsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Files: entries, ChannelIds: conversationStrings(channels), InitialComment: initialComment, Blocks: blocks, ThreadTimestamp: string(threadTimestamp)})
	if err != nil {
		return nil, err
	}
	page, err := decodeProtoFilePage(out)
	if err != nil {
		return nil, err
	}
	return page.Files, nil
}

func (s *Server) CompleteExternalUploads(ctx context.Context, input *chatv1.CompleteExternalUploadsRequest) (*chatv1.FilePage, error) {
	completions := make([]domain.ExternalUploadCompletion, 0, len(input.GetFiles()))
	for _, value := range input.GetFiles() {
		completions = append(completions, domain.ExternalUploadCompletion{ID: domain.ExternalUploadID(value.GetUploadId()), Title: value.GetTitle()})
	}
	files, err := s.implementation.CompleteExternalUploads(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), completions, conversationIDs(input.GetChannelIds()), input.GetInitialComment(), input.GetBlocks(), domain.MessageTimestamp(input.GetThreadTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoFilePage(domain.FilePage{Files: files}), nil
}

func (r Remote) IssueAppConfigurationToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.AppConfigurationCredentials, error) {
	out, err := r.apps.IssueAppConfigurationToken(ctx, &chatv1.AppConfigurationTokenRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	return decodeProtoAppConfigurationCredentials(out)
}

func (r Remote) RotateAppConfigurationToken(ctx context.Context, refreshToken string) (domain.AppConfigurationCredentials, error) {
	out, err := r.apps.RotateAppConfigurationToken(ctx, &chatv1.AppConfigurationTokenRotateRequest{RefreshToken: refreshToken})
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	return decodeProtoAppConfigurationCredentials(out)
}

func (r Remote) ValidateAppManifest(ctx context.Context, token, appID, manifest string) ([]appmanifest.Error, error) {
	out, err := r.apps.ValidateAppManifest(ctx, &chatv1.AppManifestRequest{Token: token, AppId: appID, Manifest: manifest})
	if err != nil {
		return nil, err
	}
	result := make([]appmanifest.Error, 0, len(out.GetErrors()))
	for _, problem := range out.GetErrors() {
		if problem == nil {
			return nil, errors.New("typed app manifest validation contains an empty error")
		}
		result = append(result, appmanifest.Error{Message: problem.GetMessage(), Pointer: problem.GetPointer()})
	}
	return result, nil
}

func (r Remote) CreateAppFromManifest(ctx context.Context, token, manifest string, teamID domain.WorkspaceID) (domain.App, domain.AppCredentials, error) {
	out, err := r.apps.CreateAppFromManifest(ctx, &chatv1.AppManifestRequest{Token: token, Manifest: manifest, TeamId: string(teamID)})
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	app, err := decodeProtoDeveloperApp(out.GetApp())
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	credentials := out.GetCredentials()
	if credentials == nil || credentials.GetClientId() == "" || credentials.GetClientSecret() == "" || credentials.GetSigningSecret() == "" || credentials.GetVerificationToken() == "" {
		return domain.App{}, domain.AppCredentials{}, errors.New("typed app credentials are incomplete")
	}
	return app, domain.AppCredentials{ClientID: credentials.GetClientId(), ClientSecret: credentials.GetClientSecret(), SigningSecret: credentials.GetSigningSecret(), VerificationToken: credentials.GetVerificationToken()}, nil
}

func (r Remote) ExportAppManifest(ctx context.Context, token string, appID domain.AppID) (domain.App, string, error) {
	out, err := r.apps.ExportAppManifest(ctx, &chatv1.AppManifestRequest{Token: token, AppId: string(appID)})
	if err != nil {
		return domain.App{}, "", err
	}
	app, err := decodeProtoDeveloperApp(out.GetApp())
	if err != nil {
		return domain.App{}, "", err
	}
	return app, out.GetManifest(), nil
}

func (r Remote) UpdateAppFromManifest(ctx context.Context, token string, appID domain.AppID, manifest string) (domain.App, error) {
	out, err := r.apps.UpdateAppFromManifest(ctx, &chatv1.AppManifestRequest{Token: token, AppId: string(appID), Manifest: manifest})
	if err != nil {
		return domain.App{}, err
	}
	return decodeProtoDeveloperApp(out.GetApp())
}

func (r Remote) DeleteDeveloperApp(ctx context.Context, token string, appID domain.AppID) error {
	out, err := r.apps.DeleteDeveloperApp(ctx, &chatv1.AppManifestRequest{Token: token, AppId: string(appID)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "developer app deletion")
}

func (r Remote) ListDeveloperApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.App, error) {
	out, err := r.apps.ListDeveloperApps(ctx, &chatv1.AppListRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.App, 0, len(out.GetApps()))
	for _, encoded := range out.GetApps() {
		app, err := decodeProtoDeveloperApp(encoded)
		if err != nil {
			return nil, err
		}
		result = append(result, app)
	}
	return result, nil
}

func (r Remote) AdminFunctions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.AppFunction, error) {
	out, err := r.apps.AdminFunctions(ctx, &chatv1.AppListRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	return decodeProtoAppFunctions(out.GetFunctions()), nil
}

func encodeProtoAppFunction(value domain.AppFunction) *chatv1.AppFunction {
	return &chatv1.AppFunction{
		AppId: string(value.AppID), AppName: value.AppName, CallbackId: value.CallbackID,
		Title: value.Title, Description: value.Description,
	}
}

func decodeProtoAppFunction(value *chatv1.AppFunction) domain.AppFunction {
	if value == nil {
		return domain.AppFunction{}
	}
	return domain.AppFunction{
		AppID: domain.AppID(value.GetAppId()), AppName: value.GetAppName(), CallbackID: value.GetCallbackId(),
		Title: value.GetTitle(), Description: value.GetDescription(),
	}
}

func encodeProtoAppFunctions(values []domain.AppFunction) []*chatv1.AppFunction {
	functions := make([]*chatv1.AppFunction, 0, len(values))
	for _, value := range values {
		functions = append(functions, encodeProtoAppFunction(value))
	}
	return functions
}

func decodeProtoAppFunctions(values []*chatv1.AppFunction) []domain.AppFunction {
	functions := make([]domain.AppFunction, 0, len(values))
	for _, value := range values {
		functions = append(functions, decodeProtoAppFunction(value))
	}
	return functions
}

func (r Remote) SetAppIcon(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, iconURL string) error {
	out, err := r.apps.SetAppIcon(ctx, &chatv1.AppIconRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), IconUrl: iconURL,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app icon change was not acknowledged")
	}
	return nil
}

func (r Remote) ExternalAuthToken(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, id string) (domain.ExternalAuthToken, error) {
	out, err := r.apps.ExternalAuthToken(ctx, &chatv1.ExternalAuthTokenRequest{
		WorkspaceId: string(workspaceID), AppId: string(appID), ExternalTokenId: id,
	})
	if err != nil {
		return domain.ExternalAuthToken{}, err
	}
	return decodeProtoExternalAuthToken(out), nil
}

func (r Remote) DeleteExternalAuthToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, id string) error {
	out, err := r.apps.DeleteExternalAuthToken(ctx, &chatv1.ExternalAuthTokenRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), ExternalTokenId: id,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed external credential revocation was not acknowledged")
	}
	return nil
}

func (r Remote) UpdateUserAppConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) error {
	out, err := r.apps.UpdateUserAppConnection(ctx, &chatv1.UserConnectionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app connection refresh was not acknowledged")
	}
	return nil
}

func (r Remote) AssistantSearchAvailability(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.AssistantSearchAvailability, error) {
	out, err := r.apps.AssistantSearchAvailability(ctx, &chatv1.UserConnectionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID),
	})
	if err != nil {
		return domain.AssistantSearchAvailability{}, err
	}
	return decodeProtoAssistantSearchAvailability(out), nil
}

func (r Remote) AssistantSearchContext(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	out, err := r.apps.AssistantSearchContext(ctx, &chatv1.AssistantSearchRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Query: query,
		Limit: int32(request.Limit), Cursor: string(request.Cursor),
	})
	if err != nil {
		return domain.MessagePage{}, err
	}
	return decodeProtoMessagePage(out)
}

func encodeProtoExternalAuthToken(value domain.ExternalAuthToken) *chatv1.ExternalAuthTokenValue {
	// The ciphertext is deliberately absent: an external credential's secret
	// belongs to the store, in the same way an app's signing secret does.
	return &chatv1.ExternalAuthTokenValue{
		Id: value.ID, AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID),
		UserId: string(value.UserID), Provider: value.Provider,
		ExpiresAt: optionalUnixNano(value.ExpiresAt), CreatedAt: optionalUnixNano(value.CreatedAt),
	}
}

func decodeProtoExternalAuthToken(value *chatv1.ExternalAuthTokenValue) domain.ExternalAuthToken {
	return domain.ExternalAuthToken{
		ID: value.GetId(), AppID: domain.AppID(value.GetAppId()),
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()),
		Provider:  value.GetProvider(),
		ExpiresAt: optionalTimeFromUnixNano(value.GetExpiresAt()),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAt()),
	}
}

func encodeProtoAssistantSearchAvailability(value domain.AssistantSearchAvailability) *chatv1.AssistantSearchAvailabilityValue {
	return &chatv1.AssistantSearchAvailabilityValue{
		Enabled: value.Enabled, SearchableSources: append([]string{}, value.SearchableSources...),
	}
}

func decodeProtoAssistantSearchAvailability(value *chatv1.AssistantSearchAvailabilityValue) domain.AssistantSearchAvailability {
	return domain.AssistantSearchAvailability{
		Enabled: value.GetEnabled(), SearchableSources: append([]string{}, value.GetSearchableSources()...),
	}
}

func (r Remote) AppActivities(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, filter domain.AppActivityFilter, request domain.PageRequest) (domain.AppActivityPage, error) {
	out, err := r.apps.AppActivities(ctx, appActivitiesRequest(workspaceID, "", appID, filter, request))
	if err != nil {
		return domain.AppActivityPage{}, err
	}
	return decodeProtoAppActivityPage(out), nil
}

func (r Remote) AdminAppActivities(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, filter domain.AppActivityFilter, request domain.PageRequest) (domain.AppActivityPage, error) {
	out, err := r.apps.AdminAppActivities(ctx, appActivitiesRequest(workspaceID, userID, filter.AppID, filter, request))
	if err != nil {
		return domain.AppActivityPage{}, err
	}
	return decodeProtoAppActivityPage(out), nil
}

func appActivitiesRequest(workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, filter domain.AppActivityFilter, request domain.PageRequest) *chatv1.AppActivitiesRequest {
	return &chatv1.AppActivitiesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		MinLevel: string(filter.MinLevel), MinCreatedAt: optionalUnixNano(filter.MinCreatedAt),
		MaxCreatedAt: optionalUnixNano(filter.MaxCreatedAt), ComponentType: filter.ComponentType,
		ComponentId: filter.ComponentID, Source: filter.Source, TraceId: filter.TraceID,
		Limit: int32(request.Limit), Cursor: string(request.Cursor),
	}
}

func appActivityFilter(input *chatv1.AppActivitiesRequest) domain.AppActivityFilter {
	return domain.AppActivityFilter{
		AppID: domain.AppID(input.GetAppId()), MinLevel: domain.ActivityLevel(input.GetMinLevel()),
		MinCreatedAt:  optionalTimeFromUnixNano(input.GetMinCreatedAt()),
		MaxCreatedAt:  optionalTimeFromUnixNano(input.GetMaxCreatedAt()),
		ComponentType: input.GetComponentType(), ComponentID: input.GetComponentId(),
		Source: input.GetSource(), TraceID: input.GetTraceId(),
	}
}

func encodeProtoAppActivity(value domain.AppActivity) *chatv1.AppActivity {
	return &chatv1.AppActivity{
		Id: value.ID, AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID),
		ComponentType: value.ComponentType, ComponentId: value.ComponentID, Level: string(value.Level),
		EventType: value.EventType, Source: value.Source, Message: value.Message,
		TraceId: value.TraceID, CreatedAt: optionalUnixNano(value.CreatedAt),
	}
}

func decodeProtoAppActivity(value *chatv1.AppActivity) domain.AppActivity {
	return domain.AppActivity{
		ID: value.GetId(), AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		ComponentType: value.GetComponentType(), ComponentID: value.GetComponentId(),
		Level: domain.ActivityLevel(value.GetLevel()), EventType: value.GetEventType(),
		Source: value.GetSource(), Message: value.GetMessage(), TraceID: value.GetTraceId(),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAt()),
	}
}

func decodeProtoAppActivityPage(value *chatv1.AppActivityPage) domain.AppActivityPage {
	activities := make([]domain.AppActivity, 0, len(value.GetActivities()))
	for _, encoded := range value.GetActivities() {
		activities = append(activities, decodeProtoAppActivity(encoded))
	}
	return domain.AppActivityPage{Activities: activities, NextCursor: domain.Cursor(value.GetNextCursor()), HasMore: value.GetHasMore()}
}

func encodeProtoAppActivityPage(page domain.AppActivityPage) *chatv1.AppActivityPage {
	activities := make([]*chatv1.AppActivity, 0, len(page.Activities))
	for _, value := range page.Activities {
		activities = append(activities, encodeProtoAppActivity(value))
	}
	return &chatv1.AppActivityPage{Activities: activities, NextCursor: string(page.NextCursor), HasMore: page.HasMore}
}

func (r Remote) AdminAppConfigs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appIDs []domain.AppID) ([]domain.AppConfig, error) {
	ids := make([]string, 0, len(appIDs))
	for _, id := range appIDs {
		ids = append(ids, string(id))
	}
	out, err := r.apps.AdminAppConfigs(ctx, &chatv1.AppConfigsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppIds: ids})
	if err != nil {
		return nil, err
	}
	configs := make([]domain.AppConfig, 0, len(out.GetConfigs()))
	for _, encoded := range out.GetConfigs() {
		configs = append(configs, decodeProtoAppConfig(encoded))
	}
	return configs, nil
}

func (r Remote) AdminSetAppConfig(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, config domain.AppConfig) (domain.AppConfig, error) {
	out, err := r.apps.AdminSetAppConfig(ctx, &chatv1.AppConfigMutationRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Config: encodeProtoAppConfig(config),
	})
	if err != nil {
		return domain.AppConfig{}, err
	}
	return decodeProtoAppConfig(out), nil
}

func (r Remote) AdminClearAppResolution(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) error {
	out, err := r.apps.AdminClearAppResolution(ctx, &chatv1.AppResolutionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app resolution clearance was not acknowledged")
	}
	return nil
}

func encodeProtoAppConfig(value domain.AppConfig) *chatv1.AppConfig {
	return &chatv1.AppConfig{
		AppId: string(value.AppID), WorkspaceId: string(value.WorkspaceID),
		DomainUrls: append([]string{}, value.DomainURLs...), DomainEmails: append([]string{}, value.DomainEmails...),
		WorkflowAuthStrategy: string(value.WorkflowAuthStrategy), UpdatedAt: optionalUnixNano(value.UpdatedAt),
	}
}

func decodeProtoAppConfig(value *chatv1.AppConfig) domain.AppConfig {
	return domain.AppConfig{
		AppID: domain.AppID(value.GetAppId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		DomainURLs: append([]string{}, value.GetDomainUrls()...), DomainEmails: append([]string{}, value.GetDomainEmails()...),
		WorkflowAuthStrategy: domain.WorkflowAuthStrategy(value.GetWorkflowAuthStrategy()),
		UpdatedAt:            optionalTimeFromUnixNano(value.GetUpdatedAt()),
	}
}

func (r Remote) ListWorkspaceApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.InstalledApp, error) {
	out, err := r.apps.ListWorkspaceApps(ctx, &chatv1.AppListRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	result := make([]domain.InstalledApp, 0, len(out.GetApps()))
	for _, encoded := range out.GetApps() {
		app, err := decodeProtoInstalledApp(encoded)
		if err != nil {
			return nil, err
		}
		result = append(result, app)
	}
	return result, nil
}

func (r Remote) PutAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, items []string, merge bool) ([]string, error) {
	out, err := r.apps.PutAppDatastoreItems(ctx, &chatv1.AppDatastoreRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		Datastore: datastore, Items: append([]string(nil), items...), Merge: merge,
	})
	if err != nil {
		return nil, err
	}
	return append([]string(nil), out.GetItems()...), nil
}

func (r Remote) GetAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, ids []string) ([]string, error) {
	out, err := r.apps.GetAppDatastoreItems(ctx, &chatv1.AppDatastoreRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		Datastore: datastore, Ids: append([]string(nil), ids...),
	})
	if err != nil {
		return nil, err
	}
	return append([]string(nil), out.GetItems()...), nil
}

func (r Remote) QueryAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, query domain.AppDatastoreQuery) (domain.AppDatastoreQueryPage, error) {
	out, err := r.apps.QueryAppDatastoreItems(ctx, &chatv1.AppDatastoreRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), Datastore: datastore,
		Expression: query.Expression, ExpressionAttributes: query.ExpressionAttributes, ExpressionValues: query.ExpressionValues,
		Limit: int32(query.Page.Limit), Cursor: string(query.Page.Cursor),
	})
	if err != nil {
		return domain.AppDatastoreQueryPage{}, err
	}
	return domain.AppDatastoreQueryPage{
		Items: append([]string(nil), out.GetItems()...), NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore(),
	}, nil
}

func (r Remote) CountAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, query domain.AppDatastoreQuery) (int, error) {
	out, err := r.apps.CountAppDatastoreItems(ctx, &chatv1.AppDatastoreRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), Datastore: datastore,
		Expression: query.Expression, ExpressionAttributes: query.ExpressionAttributes, ExpressionValues: query.ExpressionValues,
	})
	if err != nil {
		return 0, err
	}
	return int(out.GetCount()), nil
}

func (r Remote) DeleteAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, ids []string) error {
	out, err := r.apps.DeleteAppDatastoreItems(ctx, &chatv1.AppDatastoreRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID),
		Datastore: datastore, Ids: append([]string(nil), ids...),
	})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "app datastore deletion")
}

func (r Remote) GetDeveloperApp(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.App, string, error) {
	out, err := r.apps.GetDeveloperApp(ctx, &chatv1.AppGetRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return domain.App{}, "", err
	}
	app, err := decodeProtoDeveloperApp(out.GetApp())
	if err != nil {
		return domain.App{}, "", err
	}
	return app, out.GetManifest(), nil
}

func (r Remote) GetDeveloperAppDeliveryHealth(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.AppDeliveryHealth, error) {
	out, err := r.apps.GetDeveloperAppDeliveryHealth(ctx, &chatv1.AppGetRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	return decodeProtoAppDeliveryHealth(out)
}

func (r Remote) IssueDeveloperAppToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, scopes []string) (domain.AppTokenCredentials, error) {
	out, err := r.apps.IssueDeveloperAppToken(ctx, &chatv1.AppTokenIssueRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), Scopes: append([]string(nil), scopes...)})
	if err != nil {
		return domain.AppTokenCredentials{}, err
	}
	if out.GetToken() == "" || out.GetAppId() == "" || len(out.GetScopes()) == 0 {
		return domain.AppTokenCredentials{}, errors.New("typed app token credentials are incomplete")
	}
	return domain.AppTokenCredentials{Token: out.GetToken(), AppID: domain.AppID(out.GetAppId()), Scopes: append([]string(nil), out.GetScopes()...)}, nil
}

func (r Remote) RevokeDeveloperAppTokens(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) error {
	out, err := r.apps.RevokeDeveloperAppTokens(ctx, &chatv1.AppTokenRevokeRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app token revocation was not acknowledged")
	}
	return nil
}

func (r Remote) ListDeveloperAppTokens(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) ([]domain.AppTokenSummary, error) {
	out, err := r.apps.ListDeveloperAppTokens(ctx, &chatv1.AppTokenListRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID)})
	if err != nil {
		return nil, err
	}
	summaries := make([]domain.AppTokenSummary, 0, len(out.GetTokens()))
	for _, token := range out.GetTokens() {
		summaries = append(summaries, decodeProtoAppTokenSummary(token))
	}
	return summaries, nil
}

func (r Remote) RevokeDeveloperAppToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, id string) error {
	out, err := r.apps.RevokeDeveloperAppToken(ctx, &chatv1.AppTokenRevokeOneRequest{WorkspaceId: string(workspaceID), UserId: string(userID), AppId: string(appID), TokenId: id})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed app token revocation was not acknowledged")
	}
	return nil
}

func encodeProtoAppTokenSummary(value domain.AppTokenSummary) *chatv1.AppTokenSummary {
	return &chatv1.AppTokenSummary{
		Id: value.ID, IssuedAtUnixNano: unixNanoOrZero(value.IssuedAt),
		Scopes: append([]string(nil), value.Scopes...), Revoked: value.Revoked,
	}
}

func decodeProtoAppTokenSummary(value *chatv1.AppTokenSummary) domain.AppTokenSummary {
	if value == nil {
		return domain.AppTokenSummary{}
	}
	return domain.AppTokenSummary{
		ID: value.GetId(), IssuedAt: optionalTimeFromUnixNano(value.GetIssuedAtUnixNano()),
		Scopes: append([]string(nil), value.GetScopes()...), Revoked: value.GetRevoked(),
	}
}

func (r Remote) InspectOAuthAuthorization(ctx context.Context, request domain.OAuthAuthorizationRequest) (domain.OAuthAuthorization, error) {
	out, err := r.apps.InspectOAuthAuthorization(ctx, encodeProtoOAuthAuthorizationRequest(request))
	if err != nil {
		return domain.OAuthAuthorization{}, err
	}
	return decodeProtoOAuthAuthorization(out)
}

func (r Remote) AuthorizeOAuth(ctx context.Context, request domain.OAuthAuthorizationRequest) (domain.OAuthAuthorization, error) {
	out, err := r.apps.AuthorizeOAuth(ctx, encodeProtoOAuthAuthorizationRequest(request))
	if err != nil {
		return domain.OAuthAuthorization{}, err
	}
	return decodeProtoOAuthAuthorization(out)
}

func (s *Server) IssueAppConfigurationToken(ctx context.Context, input *chatv1.AppConfigurationTokenRequest) (*chatv1.AppConfigurationCredentials, error) {
	value, err := s.implementation.IssueAppConfigurationToken(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppConfigurationCredentials(value), nil
}

func (s *Server) RotateAppConfigurationToken(ctx context.Context, input *chatv1.AppConfigurationTokenRotateRequest) (*chatv1.AppConfigurationCredentials, error) {
	value, err := s.implementation.RotateAppConfigurationToken(ctx, input.GetRefreshToken())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppConfigurationCredentials(value), nil
}

func (s *Server) ValidateAppManifest(ctx context.Context, input *chatv1.AppManifestRequest) (*chatv1.AppManifestValidation, error) {
	problems, err := s.implementation.ValidateAppManifest(ctx, input.GetToken(), input.GetAppId(), input.GetManifest())
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.AppManifestValidation{Errors: make([]*chatv1.AppManifestError, 0, len(problems))}
	for _, problem := range problems {
		result.Errors = append(result.Errors, &chatv1.AppManifestError{Message: problem.Message, Pointer: problem.Pointer})
	}
	return result, nil
}

func (s *Server) CreateAppFromManifest(ctx context.Context, input *chatv1.AppManifestRequest) (*chatv1.AppCreateResponse, error) {
	app, credentials, err := s.implementation.CreateAppFromManifest(ctx, input.GetToken(), input.GetManifest(), domain.WorkspaceID(input.GetTeamId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppCreateResponse{App: encodeProtoDeveloperApp(app), Credentials: &chatv1.AppCredentials{ClientId: credentials.ClientID, ClientSecret: credentials.ClientSecret, SigningSecret: credentials.SigningSecret, VerificationToken: credentials.VerificationToken}}, nil
}

func (s *Server) ExportAppManifest(ctx context.Context, input *chatv1.AppManifestRequest) (*chatv1.AppExportResponse, error) {
	app, manifest, err := s.implementation.ExportAppManifest(ctx, input.GetToken(), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppExportResponse{App: encodeProtoDeveloperApp(app), Manifest: manifest}, nil
}

func (s *Server) UpdateAppFromManifest(ctx context.Context, input *chatv1.AppManifestRequest) (*chatv1.AppMutationResponse, error) {
	app, err := s.implementation.UpdateAppFromManifest(ctx, input.GetToken(), domain.AppID(input.GetAppId()), input.GetManifest())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true, App: encodeProtoDeveloperApp(app)}, nil
}

func (s *Server) DeleteDeveloperApp(ctx context.Context, input *chatv1.AppManifestRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.DeleteDeveloperApp(ctx, input.GetToken(), domain.AppID(input.GetAppId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) ListDeveloperApps(ctx context.Context, input *chatv1.AppListRequest) (*chatv1.AppListResponse, error) {
	apps, err := s.implementation.ListDeveloperApps(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.AppListResponse{Apps: make([]*chatv1.DeveloperApp, 0, len(apps))}
	for _, app := range apps {
		result.Apps = append(result.Apps, encodeProtoDeveloperApp(app))
	}
	return result, nil
}

func (s *Server) AdminFunctions(ctx context.Context, input *chatv1.AppListRequest) (*chatv1.AppFunctionListResponse, error) {
	functions, err := s.implementation.AdminFunctions(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppFunctionListResponse{Functions: encodeProtoAppFunctions(functions)}, nil
}

func (s *Server) SetAppIcon(ctx context.Context, input *chatv1.AppIconRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.SetAppIcon(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetIconUrl()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) ExternalAuthToken(ctx context.Context, input *chatv1.ExternalAuthTokenRequest) (*chatv1.ExternalAuthTokenValue, error) {
	value, err := s.implementation.ExternalAuthToken(ctx, domain.WorkspaceID(input.GetWorkspaceId()),
		domain.AppID(input.GetAppId()), input.GetExternalTokenId())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoExternalAuthToken(value), nil
}

func (s *Server) DeleteExternalAuthToken(ctx context.Context, input *chatv1.ExternalAuthTokenRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.DeleteExternalAuthToken(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetExternalTokenId()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) UpdateUserAppConnection(ctx context.Context, input *chatv1.UserConnectionRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.UpdateUserAppConnection(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) AssistantSearchAvailability(ctx context.Context, input *chatv1.UserConnectionRequest) (*chatv1.AssistantSearchAvailabilityValue, error) {
	value, err := s.implementation.AssistantSearchAvailability(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAssistantSearchAvailability(value), nil
}

func (s *Server) AssistantSearchContext(ctx context.Context, input *chatv1.AssistantSearchRequest) (*chatv1.MessagePage, error) {
	page, err := s.implementation.AssistantSearchContext(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		input.GetQuery(), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoMessagePage(page), nil
}

func (s *Server) AppActivities(ctx context.Context, input *chatv1.AppActivitiesRequest) (*chatv1.AppActivityPage, error) {
	page, err := s.implementation.AppActivities(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.AppID(input.GetAppId()),
		appActivityFilter(input), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppActivityPage(page), nil
}

func (s *Server) AdminAppActivities(ctx context.Context, input *chatv1.AppActivitiesRequest) (*chatv1.AppActivityPage, error) {
	page, err := s.implementation.AdminAppActivities(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		appActivityFilter(input), protoPageRequest(input.GetLimit(), input.GetCursor()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppActivityPage(page), nil
}

func (s *Server) AdminAppConfigs(ctx context.Context, input *chatv1.AppConfigsRequest) (*chatv1.AppConfigsResponse, error) {
	ids := make([]domain.AppID, 0, len(input.GetAppIds()))
	for _, id := range input.GetAppIds() {
		ids = append(ids, domain.AppID(id))
	}
	configs, err := s.implementation.AdminAppConfigs(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), ids)
	if err != nil {
		return nil, mapError(err)
	}
	encoded := make([]*chatv1.AppConfig, 0, len(configs))
	for _, config := range configs {
		encoded = append(encoded, encodeProtoAppConfig(config))
	}
	return &chatv1.AppConfigsResponse{Configs: encoded}, nil
}

func (s *Server) AdminSetAppConfig(ctx context.Context, input *chatv1.AppConfigMutationRequest) (*chatv1.AppConfig, error) {
	config, err := s.implementation.AdminSetAppConfig(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), decodeProtoAppConfig(input.GetConfig()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppConfig(config), nil
}

func (s *Server) AdminClearAppResolution(ctx context.Context, input *chatv1.AppResolutionRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.AdminClearAppResolution(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) ListWorkspaceApps(ctx context.Context, input *chatv1.AppListRequest) (*chatv1.InstalledAppListResponse, error) {
	apps, err := s.implementation.ListWorkspaceApps(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	result := &chatv1.InstalledAppListResponse{Apps: make([]*chatv1.InstalledApp, 0, len(apps))}
	for _, app := range apps {
		result.Apps = append(result.Apps, encodeProtoInstalledApp(app))
	}
	return result, nil
}

func (s *Server) PutAppDatastoreItems(ctx context.Context, input *chatv1.AppDatastoreRequest) (*chatv1.AppDatastoreResponse, error) {
	items, err := s.implementation.PutAppDatastoreItems(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetDatastore(), input.GetItems(), input.GetMerge(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppDatastoreResponse{Items: items}, nil
}

func (s *Server) GetAppDatastoreItems(ctx context.Context, input *chatv1.AppDatastoreRequest) (*chatv1.AppDatastoreResponse, error) {
	items, err := s.implementation.GetAppDatastoreItems(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetDatastore(), input.GetIds(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppDatastoreResponse{Items: items}, nil
}

func (s *Server) QueryAppDatastoreItems(ctx context.Context, input *chatv1.AppDatastoreRequest) (*chatv1.AppDatastoreResponse, error) {
	page, err := s.implementation.QueryAppDatastoreItems(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetDatastore(), domain.AppDatastoreQuery{
			Expression: input.GetExpression(), ExpressionAttributes: input.GetExpressionAttributes(), ExpressionValues: input.GetExpressionValues(),
			Page: domain.PageRequest{Limit: int(input.GetLimit()), Cursor: domain.Cursor(input.GetCursor())},
		},
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppDatastoreResponse{Items: page.Items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) CountAppDatastoreItems(ctx context.Context, input *chatv1.AppDatastoreRequest) (*chatv1.AppDatastoreResponse, error) {
	count, err := s.implementation.CountAppDatastoreItems(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetDatastore(), domain.AppDatastoreQuery{
			Expression: input.GetExpression(), ExpressionAttributes: input.GetExpressionAttributes(), ExpressionValues: input.GetExpressionValues(),
		},
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppDatastoreResponse{Count: int64(count)}, nil
}

func (s *Server) DeleteAppDatastoreItems(ctx context.Context, input *chatv1.AppDatastoreRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.DeleteAppDatastoreItems(
		ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.AppID(input.GetAppId()), input.GetDatastore(), input.GetIds(),
	); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) GetDeveloperApp(ctx context.Context, input *chatv1.AppGetRequest) (*chatv1.AppExportResponse, error) {
	app, manifest, err := s.implementation.GetDeveloperApp(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppExportResponse{App: encodeProtoDeveloperApp(app), Manifest: manifest}, nil
}

func (s *Server) GetDeveloperAppDeliveryHealth(ctx context.Context, input *chatv1.AppGetRequest) (*chatv1.AppDeliveryHealth, error) {
	health, err := s.implementation.GetDeveloperAppDeliveryHealth(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAppDeliveryHealth(health), nil
}

func (s *Server) IssueDeveloperAppToken(ctx context.Context, input *chatv1.AppTokenIssueRequest) (*chatv1.AppTokenCredentials, error) {
	value, err := s.implementation.IssueDeveloperAppToken(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), input.GetScopes())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppTokenCredentials{Token: value.Token, AppId: string(value.AppID), Scopes: value.Scopes}, nil
}

func (s *Server) RevokeDeveloperAppTokens(ctx context.Context, input *chatv1.AppTokenRevokeRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.RevokeDeveloperAppTokens(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) ListDeveloperAppTokens(ctx context.Context, input *chatv1.AppTokenListRequest) (*chatv1.AppTokenListResponse, error) {
	values, err := s.implementation.ListDeveloperAppTokens(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()))
	if err != nil {
		return nil, mapError(err)
	}
	tokens := make([]*chatv1.AppTokenSummary, 0, len(values))
	for _, value := range values {
		tokens = append(tokens, encodeProtoAppTokenSummary(value))
	}
	return &chatv1.AppTokenListResponse{Tokens: tokens}, nil
}

func (s *Server) RevokeDeveloperAppToken(ctx context.Context, input *chatv1.AppTokenRevokeOneRequest) (*chatv1.AppMutationResponse, error) {
	if err := s.implementation.RevokeDeveloperAppToken(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), input.GetTokenId()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AppMutationResponse{Ok: true}, nil
}

func (s *Server) InspectOAuthAuthorization(ctx context.Context, input *chatv1.OAuthAuthorizationRequest) (*chatv1.OAuthAuthorization, error) {
	request, err := decodeProtoOAuthAuthorizationRequest(input)
	if err != nil {
		return nil, mapError(storepkg.InvalidArgument(err.Error()))
	}
	value, err := s.implementation.InspectOAuthAuthorization(ctx, request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoOAuthAuthorization(value), nil
}

func (s *Server) AuthorizeOAuth(ctx context.Context, input *chatv1.OAuthAuthorizationRequest) (*chatv1.OAuthAuthorization, error) {
	request, err := decodeProtoOAuthAuthorizationRequest(input)
	if err != nil {
		return nil, mapError(storepkg.InvalidArgument(err.Error()))
	}
	value, err := s.implementation.AuthorizeOAuth(ctx, request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoOAuthAuthorization(value), nil
}

func encodeProtoAppConfigurationCredentials(value domain.AppConfigurationCredentials) *chatv1.AppConfigurationCredentials {
	return &chatv1.AppConfigurationCredentials{Token: value.Token, RefreshToken: value.RefreshToken, WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), IssuedAt: value.IssuedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano)}
}

func decodeProtoAppConfigurationCredentials(value *chatv1.AppConfigurationCredentials) (domain.AppConfigurationCredentials, error) {
	if value == nil || value.GetToken() == "" || value.GetRefreshToken() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.AppConfigurationCredentials{}, errors.New("typed app configuration credentials are incomplete")
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, value.GetIssuedAt())
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, value.GetExpiresAt())
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	return domain.AppConfigurationCredentials{Token: value.GetToken(), RefreshToken: value.GetRefreshToken(), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), IssuedAt: issuedAt.UTC(), ExpiresAt: expiresAt.UTC()}, nil
}

func encodeProtoDeveloperApp(value domain.App) *chatv1.DeveloperApp {
	return &chatv1.DeveloperApp{Id: string(value.ID), DevelopmentWorkspaceId: string(value.DevelopmentWorkspaceID), OwnerId: string(value.OwnerID), Name: value.Name, Description: value.Description, ClientId: value.ClientID, ManifestVersion: value.ManifestVersion, Distribution: value.Distribution, SocketModeEnabled: value.SocketModeEnabled, TokenRotationEnabled: value.TokenRotationEnabled, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano), Deleted: value.Deleted, IconUrl: value.IconURL}
}

func decodeProtoDeveloperApp(value *chatv1.DeveloperApp) (domain.App, error) {
	if value == nil || value.GetId() == "" || value.GetDevelopmentWorkspaceId() == "" || value.GetOwnerId() == "" || value.GetName() == "" || value.GetClientId() == "" || value.GetManifestVersion() < 1 || value.GetDistribution() == "" {
		return domain.App{}, errors.New("typed developer app is incomplete")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.GetCreatedAt())
	if err != nil {
		return domain.App{}, err
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, value.GetUpdatedAt())
	if err != nil {
		return domain.App{}, err
	}
	return domain.App{ID: domain.AppID(value.GetId()), DevelopmentWorkspaceID: domain.WorkspaceID(value.GetDevelopmentWorkspaceId()), OwnerID: domain.UserID(value.GetOwnerId()), Name: value.GetName(), Description: value.GetDescription(), ClientID: value.GetClientId(), ManifestVersion: value.GetManifestVersion(), Distribution: value.GetDistribution(), SocketModeEnabled: value.GetSocketModeEnabled(), TokenRotationEnabled: value.GetTokenRotationEnabled(), Deleted: value.GetDeleted(), IconURL: value.GetIconUrl(), CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC()}, nil
}

func encodeProtoAppDeliveryHealth(value domain.AppDeliveryHealth) *chatv1.AppDeliveryHealth {
	result := &chatv1.AppDeliveryHealth{
		AppId: string(value.AppID), Surface: value.Surface, Endpoint: value.Endpoint,
		Configured: value.Configured, Installed: value.Installed,
		AcknowledgedSequence: value.AcknowledgedSequence, InFlightSequence: value.InFlightSequence,
		RetryCount: int32(value.RetryCount), RetryReason: value.RetryReason,
		PendingEvaluation: value.PendingEvaluation, NextEventTopic: value.NextEventTopic,
		DeliveredCount: int32(value.DeliveredCount), FailedCount: int32(value.FailedCount),
	}
	for _, attempt := range value.RecentAttempts {
		result.RecentAttempts = append(result.RecentAttempts, encodeProtoAppDeliveryAttempt(attempt))
	}
	if !value.InFlightUntil.IsZero() {
		result.InFlightUntil = value.InFlightUntil.UTC().Format(time.RFC3339Nano)
	}
	if !value.RetryAt.IsZero() {
		result.RetryAt = value.RetryAt.UTC().Format(time.RFC3339Nano)
	}
	if !value.NextEventAt.IsZero() {
		result.NextEventAt = value.NextEventAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func encodeProtoAppDeliveryAttempt(value domain.AppDeliveryAttempt) *chatv1.AppDeliveryAttempt {
	result := &chatv1.AppDeliveryAttempt{
		AppId: string(value.AppID), Surface: value.Surface, Sequence: value.Sequence,
		Attempt: int32(value.Attempt), Delivered: value.Delivered, Reason: value.Reason,
	}
	if !value.AttemptedAt.IsZero() {
		result.AttemptedAt = value.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func decodeProtoAppDeliveryAttempt(value *chatv1.AppDeliveryAttempt) (domain.AppDeliveryAttempt, error) {
	attemptedAt, err := decodeOptionalProtoTime(value.GetAttemptedAt())
	if err != nil {
		return domain.AppDeliveryAttempt{}, err
	}
	return domain.AppDeliveryAttempt{
		AppID: domain.AppID(value.GetAppId()), Surface: value.GetSurface(), Sequence: value.GetSequence(),
		Attempt: int(value.GetAttempt()), Delivered: value.GetDelivered(), Reason: value.GetReason(), AttemptedAt: attemptedAt,
	}, nil
}

func decodeProtoAppDeliveryHealth(value *chatv1.AppDeliveryHealth) (domain.AppDeliveryHealth, error) {
	if value == nil || value.GetAppId() == "" || (value.GetConfigured() && value.GetSurface() != "http" && value.GetSurface() != "socket") || value.GetRetryCount() < 0 {
		return domain.AppDeliveryHealth{}, errors.New("typed app delivery health is invalid")
	}
	inFlightUntil, err := decodeOptionalProtoTime(value.GetInFlightUntil())
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	retryAt, err := decodeOptionalProtoTime(value.GetRetryAt())
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	nextEventAt, err := decodeOptionalProtoTime(value.GetNextEventAt())
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	health := domain.AppDeliveryHealth{
		AppID: domain.AppID(value.GetAppId()), Surface: value.GetSurface(), Endpoint: value.GetEndpoint(),
		Configured: value.GetConfigured(), Installed: value.GetInstalled(),
		AcknowledgedSequence: value.GetAcknowledgedSequence(), InFlightSequence: value.GetInFlightSequence(),
		InFlightUntil: inFlightUntil, RetryAt: retryAt, RetryCount: int(value.GetRetryCount()), RetryReason: value.GetRetryReason(),
		PendingEvaluation: value.GetPendingEvaluation(), NextEventTopic: value.GetNextEventTopic(), NextEventAt: nextEventAt,
		DeliveredCount: int(value.GetDeliveredCount()), FailedCount: int(value.GetFailedCount()),
	}
	for _, attempt := range value.GetRecentAttempts() {
		decoded, err := decodeProtoAppDeliveryAttempt(attempt)
		if err != nil {
			return domain.AppDeliveryHealth{}, err
		}
		health.RecentAttempts = append(health.RecentAttempts, decoded)
	}
	return health, nil
}

func encodeProtoInstalledApp(value domain.InstalledApp) *chatv1.InstalledApp {
	return &chatv1.InstalledApp{
		Id:                  string(value.ID),
		Name:                value.Name,
		Description:         value.Description,
		HomeTabEnabled:      value.HomeTabEnabled,
		MessagesTabEnabled:  value.MessagesTabEnabled,
		MessagesTabReadOnly: value.MessagesTabReadOnly,
		BotDisplayName:      value.BotDisplayName,
		BotUserId:           string(value.BotUserID),
	}
}

func decodeProtoInstalledApp(value *chatv1.InstalledApp) (domain.InstalledApp, error) {
	if value == nil || value.GetId() == "" || value.GetName() == "" {
		return domain.InstalledApp{}, errors.New("typed installed app is incomplete")
	}
	return domain.InstalledApp{
		ID:                  domain.AppID(value.GetId()),
		Name:                value.GetName(),
		Description:         value.GetDescription(),
		HomeTabEnabled:      value.GetHomeTabEnabled(),
		MessagesTabEnabled:  value.GetMessagesTabEnabled(),
		MessagesTabReadOnly: value.GetMessagesTabReadOnly(),
		BotDisplayName:      value.GetBotDisplayName(),
		BotUserID:           domain.UserID(value.GetBotUserId()),
	}, nil
}

func encodeProtoOAuthAuthorizationRequest(value domain.OAuthAuthorizationRequest) *chatv1.OAuthAuthorizationRequest {
	return &chatv1.OAuthAuthorizationRequest{ClientId: value.ClientID, WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), RedirectUri: value.RedirectURI, BotScopes: value.BotScopes, UserScopes: value.UserScopes, State: value.State, CodeChallenge: value.CodeChallenge, CodeChallengeMethod: value.CodeChallengeMethod}
}

func decodeProtoOAuthAuthorizationRequest(value *chatv1.OAuthAuthorizationRequest) (domain.OAuthAuthorizationRequest, error) {
	if value == nil || value.GetClientId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" {
		return domain.OAuthAuthorizationRequest{}, errors.New("typed oauth authorization request is incomplete")
	}
	return domain.OAuthAuthorizationRequest{ClientID: value.GetClientId(), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), RedirectURI: value.GetRedirectUri(), BotScopes: append([]string(nil), value.GetBotScopes()...), UserScopes: append([]string(nil), value.GetUserScopes()...), State: value.GetState(), CodeChallenge: value.GetCodeChallenge(), CodeChallengeMethod: value.GetCodeChallengeMethod()}, nil
}

func encodeProtoOAuthAuthorization(value domain.OAuthAuthorization) *chatv1.OAuthAuthorization {
	return &chatv1.OAuthAuthorization{AppId: string(value.AppID), AppName: value.AppName, ClientId: value.ClientID, WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID), RedirectUri: value.RedirectURI, BotScopes: value.BotScopes, UserScopes: value.UserScopes, State: value.State, Code: value.Code, BotId: string(value.BotID), BotUserId: string(value.BotUserID), CodeChallenge: value.CodeChallenge, CodeChallengeMethod: value.CodeChallengeMethod}
}

func decodeProtoOAuthAuthorization(value *chatv1.OAuthAuthorization) (domain.OAuthAuthorization, error) {
	if value == nil || value.GetAppId() == "" || value.GetAppName() == "" || value.GetClientId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetRedirectUri() == "" {
		return domain.OAuthAuthorization{}, errors.New("typed oauth authorization is incomplete")
	}
	return domain.OAuthAuthorization{AppID: domain.AppID(value.GetAppId()), AppName: value.GetAppName(), ClientID: value.GetClientId(), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()), RedirectURI: value.GetRedirectUri(), BotScopes: append([]string(nil), value.GetBotScopes()...), UserScopes: append([]string(nil), value.GetUserScopes()...), State: value.GetState(), Code: value.GetCode(), BotID: domain.BotID(value.GetBotId()), BotUserID: domain.UserID(value.GetBotUserId()), CodeChallenge: value.GetCodeChallenge(), CodeChallengeMethod: value.GetCodeChallengeMethod()}, nil
}
