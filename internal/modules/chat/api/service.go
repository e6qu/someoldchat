package api

import (
	"context"
	"io"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Service is the process-independent chat boundary. Implementations may be
// local or generated remote clients; callers do not select transport per call.
type Service interface {
	RevokeToken(context.Context, string) error
	LookupAppToken(context.Context, string) (domain.AppTokenRecord, error)
	CreateAppInstallation(context.Context, domain.AppInstallation) error
	ListAppInstallations(context.Context, domain.AppID) ([]domain.AppInstallation, error)
	ListAppEventsAfter(context.Context, domain.AppID, uint64, int) ([]events.Record, error)
	GetSocketModeCursor(context.Context, domain.AppID) (uint64, error)
	SetSocketModeCursor(context.Context, domain.AppID, uint64) error
	RevokeSession(context.Context, string) error
	CreateSession(context.Context, string, domain.SessionRecord) error
	GetAuthMethod(context.Context, domain.WorkspaceID, string) (domain.AuthMethod, error)
	SetAuthMethod(context.Context, domain.AuthMethod) error
	GetExternalIdentity(context.Context, domain.WorkspaceID, string, string) (domain.ExternalIdentity, error)
	CreateExternalIdentity(context.Context, domain.ExternalIdentity) error
	ResetUserSessions(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) error
	Post(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, domain.MessageTimestamp, string) (domain.Message, error)
	Unfurl(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, map[string]string) (domain.Message, error)
	PostEphemeral(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserID, string) (domain.EphemeralMessage, error)
	RecordAccess(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	ListAccessLogs(context.Context, domain.WorkspaceID, domain.UserID, time.Time, int, int) ([]domain.AccessLog, bool, error)
	IntegrationLogs(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string, int, int) (domain.IntegrationLogPage, error)
	Permalink(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (string, error)
	Update(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string) (domain.Message, error)
	Delete(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.Message, error)
	History(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.MessagePage, error)
	Replies(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, domain.PageRequest) (domain.MessagePage, error)
	ConversationInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.Conversation, error)
	UserInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) (domain.User, error)
	RemoveUser(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) error
	SetUserRole(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, domain.WorkspaceRole) error
	SetUserExpiration(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, time.Time) error
	AdminRenameConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string) (domain.Conversation, error)
	AdminSetConversationArchived(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, bool) (domain.Conversation, error)
	AdminDeleteConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) error
	AdminAddConversationAccessGroup(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserGroupID) error
	AdminRemoveConversationAccessGroup(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserGroupID) error
	AdminListConversationAccessGroups(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) ([]domain.UserGroupID, error)
	AdminApproveInviteRequest(context.Context, domain.WorkspaceID, domain.UserID, domain.InviteRequestID) error
	AdminDenyInviteRequest(context.Context, domain.WorkspaceID, domain.UserID, domain.InviteRequestID) error
	AdminListInviteRequests(context.Context, domain.WorkspaceID, domain.UserID, domain.InviteRequestStatus, domain.PageRequest) (domain.InviteRequestPage, error)
	AdminInviteUser(context.Context, domain.WorkspaceID, domain.UserID, string, []domain.ConversationID, string, string, bool, bool, bool, time.Time) error
	AdminCreateUser(context.Context, domain.WorkspaceID, domain.UserID, string, string, domain.WorkspaceRole) (domain.User, error)
	AdminListUsers(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.AdminUserPage, error)
	AdminAssignUser(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, []domain.ConversationID) error
	AdminApproveApp(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.AppRequestID) error
	AdminRestrictApp(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.AppRequestID) error
	AdminListApps(context.Context, domain.WorkspaceID, domain.UserID, domain.AppApprovalStatus, domain.PageRequest) (domain.AppApprovalPage, error)
	RequestAppPermissions(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, []string, string) error
	OpenView(context.Context, domain.WorkspaceID, domain.UserID, string, string) (domain.View, error)
	PublishView(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, string, string) (domain.View, error)
	PushView(context.Context, domain.WorkspaceID, domain.UserID, string, string) (domain.View, error)
	UpdateView(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string) (domain.View, error)
	WorkflowStepCompleted(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	WorkflowStepFailed(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	WorkflowUpdateStep(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string, string) error
	OpenDialog(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	BotInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.BotID) (domain.Bot, error)
	MigrationExchange(context.Context, domain.WorkspaceID, domain.UserID, []domain.UserID, bool) (domain.MigrationExchange, error)
	AdminDisconnectSharedConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.WorkspaceID) error
	AdminConnectedChannelInfo(context.Context, domain.WorkspaceID, domain.UserID, []domain.ConversationID, []domain.WorkspaceID, domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error)
	OAuthExchange(context.Context, string, string, string, string) (domain.OAuthToken, error)
	CreateRTMConnection(context.Context, domain.WorkspaceID, domain.UserID) (domain.RTMConnection, error)
	ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error)
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
	RenewSocketModeConnection(context.Context, string, time.Time) error
	ReleaseSocketModeConnection(context.Context, string) error
	CountSocketModeConnections(context.Context, domain.AppID) (int, error)
	RecordSocketModeResponse(context.Context, domain.SocketModeResponse) error
	ClaimSocketModeResponses(context.Context, domain.AppID, string, int, time.Duration) ([]domain.SocketModeResponse, error)
	AckSocketModeResponses(context.Context, string, []domain.SocketModeResponse) error
	ReleaseSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Time) error
	AdminInviteConversationMembers(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.UserID) (domain.Conversation, error)
	AdminConvertConversationToPrivate(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.Conversation, error)
	AdminGetConversationPrefs(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ConversationPrefs, error)
	AdminSetConversationPrefs(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ConversationPrefs) (domain.ConversationPrefs, error)
	AdminSearchConversations(context.Context, domain.WorkspaceID, domain.UserID, string, domain.PageRequest) (domain.ConversationPage, error)
	AdminConversationTeams(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) ([]domain.WorkspaceID, bool, domain.Cursor, error)
	AdminSetConversationTeams(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.WorkspaceID, bool) error
	Emojis(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.CustomEmoji, error)
	AdminAddEmoji(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	AdminAddEmojiAlias(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	AdminRemoveEmoji(context.Context, domain.WorkspaceID, domain.UserID, string) error
	AdminRenameEmoji(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	UserGroupChannels(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID) ([]domain.ConversationID, error)
	AddUserGroupChannels(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID, []domain.ConversationID) error
	AdminAddUserGroupTeams(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID, []domain.WorkspaceID) error
	RemoveUserGroupChannels(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID, []domain.ConversationID) error
	AdminSetWorkspaceName(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.Workspace, error)
	AdminSetWorkspaceDescription(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.Workspace, error)
	AdminSetWorkspaceDiscoverability(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkspaceDiscoverability) (domain.Workspace, error)
	AdminSetWorkspaceIcon(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.Workspace, error)
	AdminSetWorkspaceDefaultChannels(context.Context, domain.WorkspaceID, domain.UserID, []domain.ConversationID) (domain.Workspace, error)
	AdminTeamUsers(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkspaceRole, domain.PageRequest) (domain.UserPage, error)
	UserByEmail(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.User, error)
	SetUserProfile(context.Context, domain.WorkspaceID, domain.UserID, domain.UserProfile) (domain.User, error)
	SetUserPhoto(context.Context, domain.WorkspaceID, domain.UserID, string, int64, io.Reader) (domain.User, error)
	DeleteUserPhoto(context.Context, domain.WorkspaceID, domain.UserID) error
	OpenUserPhoto(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.User, io.ReadCloser, error)
	SetUserPresence(context.Context, domain.WorkspaceID, domain.UserID, domain.Presence) (domain.User, error)
	DoNotDisturbInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) (domain.DoNotDisturb, error)
	SetSnooze(context.Context, domain.WorkspaceID, domain.UserID, int64) (domain.DoNotDisturb, error)
	EndSnooze(context.Context, domain.WorkspaceID, domain.UserID) (domain.DoNotDisturb, error)
	EndDND(context.Context, domain.WorkspaceID, domain.UserID) error
	Users(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.UserPage, error)
	ConversationMembers(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.UserPage, error)
	WorkspaceInfo(context.Context, domain.WorkspaceID, domain.UserID) (domain.Workspace, error)
	AdminCreateWorkspace(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, domain.WorkspaceDiscoverability) (domain.Workspace, error)
	TeamBillableInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) (domain.BillableInfo, error)
	Conversations(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationListRequest) (domain.ConversationPage, error)
	OpenConversation(context.Context, domain.WorkspaceID, domain.UserID, []domain.UserID) (domain.Conversation, error)
	CreateConversation(context.Context, domain.WorkspaceID, domain.UserID, string, bool) (domain.Conversation, error)
	RenameConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string) (domain.Conversation, error)
	SetConversationTopic(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string) (domain.Conversation, error)
	SetConversationPurpose(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string) (domain.Conversation, error)
	SetConversationArchived(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, bool) (domain.Conversation, error)
	JoinConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.Conversation, error)
	InviteConversationMembers(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.UserID) (domain.Conversation, error)
	LeaveConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) error
	KickConversationMember(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserID) error
	MarkRead(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.ReadCursor, error)
	AddReaction(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string) error
	RemoveReaction(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string) error
	Reactions(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error)
	UserReactions(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.UserReactionPage, error)
	AddPin(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) error
	RemovePin(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) error
	Pins(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error)
	AddStar(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) error
	RemoveStar(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) error
	Stars(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error)
	AddReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, string, time.Time) (domain.Reminder, error)
	CompleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) error
	DeleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) error
	ReminderInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) (domain.Reminder, error)
	Reminders(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.ReminderPage, error)
	ScheduleMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, time.Time) (domain.ScheduledMessage, error)
	ScheduledMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.ScheduledMessagePage, error)
	DeleteScheduledMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ScheduledMessageID) error
	CreateUserGroup(context.Context, domain.WorkspaceID, domain.UserID, string, string, string) (domain.UserGroup, error)
	UpdateUserGroup(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID, string, string, string) (domain.UserGroup, error)
	SetUserGroupEnabled(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID, bool) (domain.UserGroup, error)
	ListUserGroups(context.Context, domain.WorkspaceID, domain.UserID, bool, domain.PageRequest) (domain.UserGroupPage, error)
	UserGroupUsers(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID) ([]domain.UserID, error)
	SetUserGroupUsers(context.Context, domain.WorkspaceID, domain.UserID, domain.UserGroupID, []domain.UserID) (domain.UserGroup, error)
	AddCall(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string, string, time.Time, []domain.UserID) (domain.Call, error)
	GetCall(context.Context, domain.WorkspaceID, domain.UserID, domain.CallID) (domain.Call, error)
	UpdateCall(context.Context, domain.WorkspaceID, domain.UserID, domain.CallID, string, string, string) (domain.Call, error)
	EndCall(context.Context, domain.WorkspaceID, domain.UserID, domain.CallID, int64) error
	AddCallParticipants(context.Context, domain.WorkspaceID, domain.UserID, domain.CallID, []domain.UserID) error
	RemoveCallParticipants(context.Context, domain.WorkspaceID, domain.UserID, domain.CallID, []domain.UserID) error
	Search(context.Context, domain.WorkspaceID, domain.UserID, string, domain.PageRequest) (domain.MessagePage, error)
	UploadFile(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, int64, io.Reader) (domain.File, error)
	OpenFile(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID) (domain.File, io.ReadCloser, error)
	FileInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID) (domain.File, error)
	DeleteFile(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID) error
	DeleteFileComment(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID, domain.FileCommentID) error
	ShareFilePublic(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID) (domain.File, error)
	RevokeFilePublic(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID) (domain.File, error)
	OpenPublicFile(context.Context, string) (domain.File, io.ReadCloser, error)
	Files(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FilePage, error)
	AddRemoteFile(context.Context, domain.WorkspaceID, domain.UserID, domain.RemoteFile) (domain.RemoteFile, error)
	RemoteFileInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.RemoteFileLookup) (domain.RemoteFile, error)
	RemoteFiles(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.RemoteFilePage, error)
	RemoveRemoteFile(context.Context, domain.WorkspaceID, domain.UserID, domain.RemoteFileLookup) error
	ShareRemoteFile(context.Context, domain.WorkspaceID, domain.UserID, domain.RemoteFileLookup, []domain.ConversationID) (domain.RemoteFile, error)
	UpdateRemoteFile(context.Context, domain.WorkspaceID, domain.UserID, domain.RemoteFileUpdate) (domain.RemoteFile, error)
	events.Source
}
