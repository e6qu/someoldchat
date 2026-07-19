package store

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

var (
	ErrNotFound                  = errors.New("not found")
	ErrLeaseConflict             = errors.New("outbox lease conflict")
	ErrIdempotencyConflict       = errors.New("idempotency key already committed")
	ErrAlreadyExists             = errors.New("already exists")
	ErrInvalidConversationType   = errors.New("invalid conversation type")
	ErrInvalidInviteRequest      = errors.New("invalid invite request")
	ErrInvalidAppApproval        = errors.New("invalid app approval")
	ErrConflict                  = errors.New("state conflict")
	ErrSocketModeConnectionLimit = errors.New("Socket Mode connection limit reached")
)

type Store interface {
	AppendEvent(context.Context, events.Event) error
	RecordAccess(context.Context, domain.AccessLog) error
	ListAccessLogs(context.Context, domain.WorkspaceID, time.Time, int, int) ([]domain.AccessLog, bool, error)
	LookupToken(context.Context, string) (domain.TokenRecord, error)
	LookupAppToken(context.Context, string) (domain.AppTokenRecord, error)
	LookupSession(context.Context, string) (domain.SessionRecord, error)
	CreateSession(context.Context, string, domain.SessionRecord) error
	GetAuthMethod(context.Context, domain.WorkspaceID, string) (domain.AuthMethod, error)
	SetAuthMethod(context.Context, domain.AuthMethod) error
	GetExternalIdentity(context.Context, domain.WorkspaceID, string, string) (domain.ExternalIdentity, error)
	CreateExternalIdentity(context.Context, domain.ExternalIdentity) error
	RevokeSession(context.Context, string) error
	RevokeUserSessions(context.Context, domain.WorkspaceID, domain.UserID, events.Event) error
	RevokeToken(context.Context, string) error
	RevokeAppToken(context.Context, string) error
	GetWorkspace(context.Context, domain.WorkspaceID) (domain.Workspace, error)
	CreateWorkspace(context.Context, domain.Workspace, events.Event) error
	SetWorkspaceName(context.Context, domain.WorkspaceID, string, events.Event) (domain.Workspace, error)
	SetWorkspaceDescription(context.Context, domain.WorkspaceID, string, events.Event) (domain.Workspace, error)
	SetWorkspaceDiscoverability(context.Context, domain.WorkspaceID, domain.WorkspaceDiscoverability, events.Event) (domain.Workspace, error)
	SetWorkspaceIcon(context.Context, domain.WorkspaceID, string, events.Event) (domain.Workspace, error)
	SetWorkspaceDefaultChannels(context.Context, domain.WorkspaceID, []domain.ConversationID, events.Event) (domain.Workspace, error)
	GetWorkspaceMembership(context.Context, domain.WorkspaceID, domain.UserID) (domain.WorkspaceMembership, error)
	GetUser(context.Context, domain.UserID) (domain.User, error)
	CreateUser(context.Context, domain.User, domain.WorkspaceMembership, events.Event) error
	FindUserByEmail(context.Context, domain.WorkspaceID, string) (domain.User, error)
	UpdateUserProfile(context.Context, domain.WorkspaceID, domain.UserID, domain.UserProfile, events.Event) (domain.User, error)
	SetUserPresence(context.Context, domain.WorkspaceID, domain.UserID, domain.Presence, events.Event) (domain.User, error)
	SetUserExpiration(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) error
	SetUserDeleted(context.Context, domain.WorkspaceID, domain.UserID, bool, events.Event) error
	AssignUser(context.Context, domain.WorkspaceID, domain.UserID, []domain.ConversationID, events.Event) error
	SetWorkspaceRole(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkspaceRole, events.Event) error
	GetDoNotDisturb(context.Context, domain.WorkspaceID, domain.UserID) (domain.DoNotDisturb, error)
	SetDoNotDisturb(context.Context, domain.DoNotDisturb, events.Event) error
	GetConversation(context.Context, domain.ConversationID) (domain.Conversation, error)
	FindDirectConversation(context.Context, domain.WorkspaceID, []domain.UserID) (domain.Conversation, error)
	CreateDirectConversation(context.Context, domain.Conversation, []domain.UserID, events.Event) error
	CreateConversation(context.Context, domain.Conversation, domain.UserID, events.Event) error
	RenameConversation(context.Context, domain.ConversationID, string, events.Event) (domain.Conversation, error)
	SetConversationTopic(context.Context, domain.ConversationID, string, events.Event) (domain.Conversation, error)
	SetConversationPurpose(context.Context, domain.ConversationID, string, events.Event) (domain.Conversation, error)
	SetConversationArchived(context.Context, domain.ConversationID, bool, events.Event) (domain.Conversation, error)
	DeleteConversation(context.Context, domain.WorkspaceID, domain.ConversationID, events.Event) error
	SetConversationAccessGroups(context.Context, domain.WorkspaceID, domain.ConversationID, []domain.UserGroupID, events.Event) error
	ListConversationAccessGroups(context.Context, domain.WorkspaceID, domain.ConversationID) ([]domain.UserGroupID, error)
	CreateInviteRequest(context.Context, domain.InviteRequest, events.Event) error
	GetInviteRequest(context.Context, domain.WorkspaceID, domain.InviteRequestID) (domain.InviteRequest, error)
	SetInviteRequestStatus(context.Context, domain.WorkspaceID, domain.InviteRequestID, domain.InviteRequestStatus, time.Time, events.Event) error
	ListInviteRequests(context.Context, domain.WorkspaceID, domain.InviteRequestStatus, domain.PageRequest) (domain.InviteRequestPage, error)
	SetAppApproval(context.Context, domain.WorkspaceID, domain.AppID, domain.AppRequestID, domain.AppApprovalStatus, time.Time, events.Event) error
	ListAppApprovals(context.Context, domain.WorkspaceID, domain.AppApprovalStatus, domain.PageRequest) (domain.AppApprovalPage, error)
	CreateAppInstallation(context.Context, domain.AppInstallation) error
	ListAppInstallations(context.Context, domain.AppID) ([]domain.AppInstallation, error)
	CreateAppPermissionRequest(context.Context, domain.AppPermissionRequest, events.Event) error
	CreateView(context.Context, domain.View, events.Event) error
	GetView(context.Context, domain.WorkspaceID, domain.ViewID) (domain.View, error)
	GetViewByExternalID(context.Context, domain.WorkspaceID, string) (domain.View, error)
	GetPublishedView(context.Context, domain.WorkspaceID, domain.UserID) (domain.View, error)
	GetLatestView(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.View, error)
	UpdateView(context.Context, domain.View, string, events.Event) (domain.View, error)
	SetWorkflowStep(context.Context, domain.WorkflowStep, events.Event) error
	GetWorkflowStep(context.Context, domain.WorkspaceID, domain.WorkflowStepID) (domain.WorkflowStep, error)
	CreateDialog(context.Context, domain.Dialog, events.Event) error
	GetDialog(context.Context, domain.WorkspaceID, domain.DialogID) (domain.Dialog, error)
	CreateBot(context.Context, domain.Bot) error
	GetBot(context.Context, domain.WorkspaceID, domain.BotID) (domain.Bot, error)
	CreateUserMigration(context.Context, domain.UserMigration, events.Event) error
	FindUserMigration(context.Context, domain.WorkspaceID, domain.UserID) (domain.UserMigration, error)
	SetConversationTeams(context.Context, domain.WorkspaceID, domain.ConversationID, []domain.WorkspaceID, bool, events.Event) error
	ListConversationTeams(context.Context, domain.WorkspaceID, domain.ConversationID) ([]domain.WorkspaceID, bool, error)
	DisconnectConversationTeams(context.Context, domain.WorkspaceID, domain.ConversationID, []domain.WorkspaceID, events.Event) error
	ListConnectedChannelInfo(context.Context, domain.WorkspaceID, []domain.ConversationID, []domain.WorkspaceID, domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error)
	CreateOAuthClient(context.Context, domain.OAuthClient) error
	GetOAuthClient(context.Context, string) (domain.OAuthClient, error)
	CreateOAuthCode(context.Context, domain.OAuthCode) error
	ExchangeOAuthCode(context.Context, string, string, string, string, string, domain.OAuthToken) (domain.OAuthToken, error)
	CreateRTMConnection(context.Context, domain.RTMConnection) error
	ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error)
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
	RenewSocketModeConnection(context.Context, string, time.Time) error
	ReleaseSocketModeConnection(context.Context, string) error
	CountSocketModeConnections(context.Context, domain.AppID) (int, error)
	RecordSocketModeResponse(context.Context, domain.SocketModeResponse) error
	GetSocketModeCursor(context.Context, domain.AppID) (uint64, error)
	SetSocketModeCursor(context.Context, domain.AppID, uint64) error
	SetConversationPrivate(context.Context, domain.ConversationID, events.Event) (domain.Conversation, error)
	GetConversationPrefs(context.Context, domain.ConversationID) (domain.ConversationPrefs, error)
	SetConversationPrefs(context.Context, domain.ConversationID, domain.ConversationPrefs, events.Event) (domain.ConversationPrefs, error)
	AddEmoji(context.Context, domain.CustomEmoji, events.Event) error
	ListEmojis(context.Context, domain.WorkspaceID) ([]domain.CustomEmoji, error)
	RemoveEmoji(context.Context, domain.WorkspaceID, string, events.Event) error
	RenameEmoji(context.Context, domain.WorkspaceID, string, string, events.Event) error
	AddConversationMember(context.Context, domain.ConversationID, domain.UserID, events.Event) error
	InviteConversationMembers(context.Context, domain.ConversationID, []domain.UserID, events.Event) error
	RemoveConversationMember(context.Context, domain.ConversationID, domain.UserID, events.Event) error
	GetReadCursor(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ReadCursor, error)
	SetReadCursor(context.Context, domain.ReadCursor, events.Event) error
	ListUsers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.UserPage, error)
	ListAdminUsers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.AdminUserPage, error)
	ListUsersByRole(context.Context, domain.WorkspaceID, domain.WorkspaceRole, domain.PageRequest) (domain.UserPage, error)
	ListConversationMembers(context.Context, domain.ConversationID, domain.PageRequest) (domain.UserPage, error)
	ListConversations(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationListRequest) (domain.ConversationPage, error)
	SearchConversations(context.Context, domain.WorkspaceID, string, domain.PageRequest) (domain.ConversationPage, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error)
	ListAppEventsAfter(context.Context, domain.AppID, uint64, int) ([]events.Record, error)
	ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error)
	ClaimEventsForTopic(context.Context, domain.WorkspaceID, string, string, int, time.Duration) ([]events.Record, error)
	RenewEvents(context.Context, string, []uint64, time.Duration) error
	AckEvents(context.Context, string, []uint64) error
	ReleaseEvents(context.Context, string, []uint64, time.Time) error
	GetMessageByCreatedAt(context.Context, domain.ConversationID, time.Time) (domain.Message, error)
	GetIdempotentMessage(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.Message, error)
	UpdateMessage(context.Context, domain.Message, events.Event) error
	CreateMessage(context.Context, domain.Message, events.Event, string) error
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	ListMessages(context.Context, domain.ConversationID, domain.PageRequest) (domain.MessagePage, error)
	ListThreadMessages(context.Context, domain.ConversationID, domain.MessageTimestamp, domain.PageRequest) (domain.MessagePage, error)
	AddReaction(context.Context, domain.Reaction, events.Event) error
	RemoveReaction(context.Context, domain.Reaction, events.Event) error
	ListReactions(context.Context, domain.MessageID, domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error)
	ListUserReactions(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.UserReactionPage, error)
	AddPin(context.Context, domain.Pin, events.Event) error
	RemovePin(context.Context, domain.Pin, events.Event) error
	ListPins(context.Context, domain.ConversationID, domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error)
	AddStar(context.Context, domain.Star, events.Event) error
	RemoveStar(context.Context, domain.Star, events.Event) error
	ListStars(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error)
	CreateReminder(context.Context, domain.Reminder, events.Event) error
	GetReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) (domain.Reminder, error)
	ListReminders(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.ReminderPage, error)
	CompleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID, time.Time, events.Event) error
	DeleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID, events.Event) error
	CreateScheduledMessage(context.Context, domain.ScheduledMessage, events.Event) error
	ListScheduledMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.ScheduledMessagePage, error)
	EarliestScheduledMessage(context.Context, domain.WorkspaceID) (time.Time, error)
	DeleteScheduledMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ScheduledMessageID, events.Event) error
	ClaimScheduledMessages(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]domain.ScheduledMessage, error)
	RenewScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Duration) error
	MarkScheduledMessageDelivered(context.Context, string, domain.ScheduledMessageID) error
	ReleaseScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Time) error
	CreateUserGroup(context.Context, domain.UserGroup, events.Event) error
	GetUserGroup(context.Context, domain.WorkspaceID, domain.UserGroupID) (domain.UserGroup, error)
	ListUserGroups(context.Context, domain.WorkspaceID, bool, domain.PageRequest) (domain.UserGroupPage, error)
	UpdateUserGroup(context.Context, domain.UserGroup, events.Event) error
	SetUserGroupEnabled(context.Context, domain.WorkspaceID, domain.UserGroupID, bool, domain.UserID, events.Event) error
	SetUserGroupUsers(context.Context, domain.WorkspaceID, domain.UserGroupID, []domain.UserID, domain.UserID, events.Event) error
	SetUserGroupChannels(context.Context, domain.WorkspaceID, domain.UserGroupID, []domain.ConversationID, domain.UserID, events.Event) error
	CreateCall(context.Context, domain.Call, events.Event) error
	GetCall(context.Context, domain.WorkspaceID, domain.CallID) (domain.Call, error)
	UpdateCall(context.Context, domain.Call, events.Event) error
	EndCall(context.Context, domain.WorkspaceID, domain.CallID, int64, events.Event) error
	SetCallParticipants(context.Context, domain.WorkspaceID, domain.CallID, []domain.UserID, events.Event) error
	CreateFile(context.Context, domain.File, events.Event) error
	GetFile(context.Context, domain.FileID) (domain.File, error)
	DeleteFile(context.Context, domain.FileID, events.Event) error
	DeleteFileComment(context.Context, domain.WorkspaceID, domain.FileID, domain.FileCommentID, events.Event) error
	ShareFilePublic(context.Context, domain.WorkspaceID, domain.FileID, string, events.Event) error
	RevokeFilePublic(context.Context, domain.WorkspaceID, domain.FileID, events.Event) error
	GetPublicFile(context.Context, string) (domain.File, error)
	ListFiles(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.FilePage, error)
	WalkBlobReferences(context.Context, domain.WorkspaceID, func(string) error) error
	AddRemoteFile(context.Context, domain.RemoteFile, events.Event) error
	GetRemoteFile(context.Context, domain.WorkspaceID, domain.RemoteFileLookup) (domain.RemoteFile, error)
	ListRemoteFiles(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.RemoteFilePage, error)
	RemoveRemoteFile(context.Context, domain.WorkspaceID, domain.RemoteFileLookup, events.Event) error
	SetRemoteFileShares(context.Context, domain.WorkspaceID, domain.RemoteFileLookup, []domain.ConversationID, events.Event) (domain.RemoteFile, error)
	UpdateRemoteFile(context.Context, domain.WorkspaceID, domain.RemoteFile, events.Event) (domain.RemoteFile, error)
	SearchMessages(context.Context, domain.WorkspaceID, domain.UserID, string, domain.PageRequest) (domain.MessagePage, error)
}
