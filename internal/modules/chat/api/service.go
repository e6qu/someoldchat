package api

import (
	"context"
	"io"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
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
	ListAppAuthorizations(context.Context, domain.AppID, domain.WorkspaceID) ([]domain.AppAuthorization, error)
	IssueAppConfigurationToken(context.Context, domain.WorkspaceID, domain.UserID) (domain.AppConfigurationCredentials, error)
	RotateAppConfigurationToken(context.Context, string) (domain.AppConfigurationCredentials, error)
	ValidateAppManifest(context.Context, string, string, string) ([]appmanifest.Error, error)
	CreateAppFromManifest(context.Context, string, string, domain.WorkspaceID) (domain.App, domain.AppCredentials, error)
	ExportAppManifest(context.Context, string, domain.AppID) (domain.App, string, error)
	UpdateAppFromManifest(context.Context, string, domain.AppID, string) (domain.App, error)
	DeleteDeveloperApp(context.Context, string, domain.AppID) error
	ListDeveloperApps(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.App, error)
	ListWorkspaceApps(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.InstalledApp, error)
	PutAppDatastoreItems(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, []string, bool) ([]string, error)
	GetAppDatastoreItems(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, []string) ([]string, error)
	QueryAppDatastoreItems(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, domain.AppDatastoreQuery) (domain.AppDatastoreQueryPage, error)
	CountAppDatastoreItems(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, domain.AppDatastoreQuery) (int, error)
	DeleteAppDatastoreItems(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, []string) error
	AppHome(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID) (domain.InstalledApp, domain.View, error)
	OpenAppHome(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID) (domain.InstalledApp, domain.View, error)
	GetDeveloperApp(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID) (domain.App, string, error)
	GetDeveloperAppDeliveryHealth(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID) (domain.AppDeliveryHealth, error)
	IssueDeveloperAppToken(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, []string) (domain.AppTokenCredentials, error)
	InspectOAuthAuthorization(context.Context, domain.OAuthAuthorizationRequest) (domain.OAuthAuthorization, error)
	AuthorizeOAuth(context.Context, domain.OAuthAuthorizationRequest) (domain.OAuthAuthorization, error)
	UninstallApp(context.Context, string, string, domain.WorkspaceID, domain.AppID) error
	AdminCreateIncomingWebhook(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.UserID) (domain.IncomingWebhook, string, error)
	AdminSetIncomingWebhookEnabled(context.Context, domain.WorkspaceID, domain.UserID, domain.IncomingWebhookID, bool) error
	PostIncomingWebhook(context.Context, domain.WorkspaceID, domain.AppID, string, string, string, domain.MessageTimestamp, string) (domain.Message, error)
	PostIncomingWebhookWithAttachments(context.Context, domain.WorkspaceID, domain.AppID, string, string, string, string, domain.MessageTimestamp, string) (domain.Message, error)
	ListAppEventsAfter(context.Context, domain.AppID, uint64, int) ([]events.Record, error)
	ListUserEventsAfter(context.Context, domain.WorkspaceID, domain.UserID, uint64, int) ([]events.Record, error)
	ClaimAppEvent(context.Context, domain.AppID, string, string, time.Duration) (events.Record, int, string, bool, error)
	AckAppEvent(context.Context, domain.AppID, string, string, uint64) error
	ReleaseAppEvent(context.Context, domain.AppID, string, string, uint64, string, time.Time) error
	DispatchSlashCommand(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string, string, string) error
	DispatchBlockAction(context.Context, domain.WorkspaceID, domain.UserID, domain.AppBlockAction, string) error
	DispatchViewBlockAction(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.AppViewBlockAction, string) error
	LoadAppOptions(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.AppOptionQuery, string) ([]domain.AppOption, error)
	ListAppShortcuts(context.Context, domain.WorkspaceID, domain.UserID, string) ([]domain.AppShortcut, error)
	DispatchAppShortcut(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.AppID, string, domain.MessageID, string) error
	HandleAppResponse(context.Context, string, string) error
	ClaimSocketModeInteraction(context.Context, domain.AppID, string, time.Duration) (domain.SocketModeInteraction, bool, error)
	AckSocketModeInteraction(context.Context, domain.AppID, string, string) error
	ReleaseSocketModeInteraction(context.Context, domain.AppID, string, string, string, time.Time) error
	HandleSocketModeResponse(context.Context, domain.AppID, string, []byte) error
	GetSocketModeCursor(context.Context, domain.AppID) (uint64, error)
	SetSocketModeCursor(context.Context, domain.AppID, uint64) error
	RevokeSession(context.Context, string) error
	LookupSession(context.Context, string) (domain.SessionRecord, error)
	CreateSession(context.Context, string, domain.SessionRecord) error
	GetAuthMethod(context.Context, domain.WorkspaceID, string) (domain.AuthMethod, error)
	SetAuthMethod(context.Context, domain.AuthMethod) error
	GetExternalIdentity(context.Context, domain.WorkspaceID, string, string) (domain.ExternalIdentity, error)
	CreateExternalIdentity(context.Context, domain.ExternalIdentity) error
	RevokeOIDCSessions(context.Context, domain.WorkspaceID, string, string, string, string, time.Time) error
	ResetUserSessions(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) error
	Post(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, domain.MessageTimestamp, string) (domain.Message, error)
	PostWithBlocks(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, string, domain.MessageTimestamp, string) (domain.Message, error)
	ShareFile(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID, domain.ConversationID, domain.MessageTimestamp) (domain.Message, error)
	Unfurl(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, map[string]string) (domain.Message, error)
	PostEphemeral(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserID, string) (domain.EphemeralMessage, error)
	PostEphemeralWithBlocks(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserID, string, string) (domain.EphemeralMessage, error)
	PostWithBlocksAndAttachments(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, string, string, domain.MessageTimestamp, string, domain.AppID) (domain.Message, error)
	PostMessageAs(context.Context, domain.WorkspaceID, domain.UserID, domain.MessagePostRequest) (domain.Message, error)
	StartMessageStream(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageStreamStart) (domain.Message, error)
	AppendMessageStream(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageStreamMutation) (domain.Message, error)
	StopMessageStream(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageStreamMutation) (domain.Message, error)
	PostEphemeralWithBlocksAndAttachments(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.UserID, string, string, string, domain.AppID) (domain.EphemeralMessage, error)
	ListEphemeralMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, int) ([]domain.EphemeralMessage, error)
	RecordAccess(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	ListAccessLogs(context.Context, domain.WorkspaceID, domain.UserID, time.Time, int, int) ([]domain.AccessLog, bool, error)
	IntegrationLogs(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string, int, int) (domain.IntegrationLogPage, error)
	Permalink(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (string, error)
	Update(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string) (domain.Message, error)
	UpdateWithBlocks(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string, string) (domain.Message, error)
	UpdateWithBlocksAndAttachments(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string, string, string) (domain.Message, error)
	UpdateMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, domain.MessagePatch) (domain.Message, error)
	Delete(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.Message, error)
	History(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.MessagePage, error)
	Replies(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, domain.PageRequest) (domain.MessagePage, error)
	ConversationInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.Conversation, error)
	UserInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) (domain.User, error)
	RemoveUser(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) error
	SetUserRole(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, domain.WorkspaceRole) error

	// WorkspaceMembership reads one membership row. The actor may read their own;
	// reading another user's requires a workspace administrator.
	WorkspaceMembership(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) (domain.WorkspaceMembership, error)

	// ProvisionExternalUser and SynchronizeExternalUserRole are the sign-in
	// operations of a federated workspace. Neither takes an end-user actor and
	// neither performs an end-user authority check; see the contract on
	// service.Messages, which every implementation of this interface inherits.
	// They must be called only after an identity-provider assertion has been
	// verified, and they must never be exposed on the public HTTP surface.
	ProvisionExternalUser(context.Context, domain.WorkspaceID, string, string, domain.WorkspaceRole) (domain.User, error)
	SynchronizeExternalUserRole(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkspaceRole) error
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
	OpenView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, string) (domain.View, error)
	PublishView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.UserID, string, string) (domain.View, error)
	PushView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, string) (domain.View, error)
	UpdateView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, string, string, string) (domain.View, error)
	CurrentModalView(context.Context, domain.WorkspaceID, domain.UserID) (domain.View, error)
	SubmitView(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ViewID, string, string) (domain.ViewInteractionResult, error)
	CloseView(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ViewID, bool, string) error
	WorkflowStepCompleted(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	WorkflowStepFailed(context.Context, domain.WorkspaceID, domain.UserID, string, string) error
	WorkflowUpdateStep(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string, string) error
	CreateWorkflow(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowDefinition) (domain.WorkflowDefinition, error)
	GetWorkflow(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowID) (domain.WorkflowDefinition, error)
	DiscardWorkflowStagedChanges(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowID, uint64) error
	UpdateWorkflow(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowDefinition, uint64, bool) (domain.WorkflowDefinition, error)
	ListWorkflows(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error)
	SetWorkflowTrigger(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowTrigger, uint64) (domain.WorkflowTrigger, error)
	ListWorkflowTriggers(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowID) ([]domain.WorkflowTrigger, error)
	RunWorkflow(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowTriggerID, domain.ConversationID, string, string) (domain.WorkflowRun, error)
	RunAutomaticWorkflow(context.Context, domain.WorkspaceID, domain.WorkflowTriggerID, domain.ConversationID, string, string) (domain.WorkflowRun, error)
	RunWebhookTrigger(context.Context, domain.WorkspaceID, domain.WorkflowTriggerID, string, string) (domain.WorkflowRun, error)
	WebhookTriggerURL(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowTriggerID) (string, error)
	DispatchWorkflowEventTriggers(context.Context, domain.WorkspaceID, int) (int, error)
	GetWorkflowRun(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkflowRunID) (domain.WorkflowRun, error)
	CompleteFunction(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.WorkflowStepID, string, string) error
	GetFunctionPermission(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, string) (domain.AutomationPermission, error)
	SetFunctionPermission(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, string, domain.AutomationPermission) (domain.AutomationPermission, error)
	GetTriggerPermission(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.WorkflowTriggerID) (domain.AutomationPermission, error)
	SetTriggerPermission(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.WorkflowTriggerID, domain.AutomationPermission) (domain.AutomationPermission, error)
	SetFeaturedWorkflows(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.WorkflowTriggerID) error
	ListFeaturedWorkflows(context.Context, domain.WorkspaceID, domain.UserID, []domain.ConversationID) ([]domain.FeaturedWorkflow, error)
	ListFunctionWorkflowSteps(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, domain.WorkflowID, string, domain.AppID) ([]domain.WorkflowStepVersion, error)
	OpenDialog(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string, string) error
	BotInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.BotID) (domain.Bot, error)
	MigrationExchange(context.Context, domain.WorkspaceID, domain.UserID, []domain.UserID, bool) (domain.MigrationExchange, error)
	AdminDisconnectSharedConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.WorkspaceID) error
	AdminConnectedChannelInfo(context.Context, domain.WorkspaceID, domain.UserID, []domain.ConversationID, []domain.WorkspaceID, domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error)
	OAuthExchange(context.Context, string, string, string, string) (domain.OAuthToken, error)
	OAuthV2Exchange(context.Context, string, string, string, string, bool) (domain.OAuthToken, error)
	OAuthV2Refresh(context.Context, string, string, string) (domain.OAuthToken, error)
	OAuthV2ExchangeToken(context.Context, string, string, string) (domain.OAuthToken, error)
	OpenIDConnectToken(context.Context, string, string, string, string, string, string, string) (domain.OpenIDToken, error)
	OpenIDConnectUserInfo(context.Context, string) (domain.OpenIDUserInfo, error)
	CreateRTMConnection(context.Context, domain.WorkspaceID, domain.UserID) (domain.RTMConnection, error)
	ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error)
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
	RenewSocketModeConnection(context.Context, string, time.Time) error
	ReleaseSocketModeConnection(context.Context, string) error
	CountSocketModeConnections(context.Context, domain.AppID) (int, error)
	RecordSocketModeResponse(context.Context, domain.SocketModeResponse) error
	ClaimSocketModeResponses(context.Context, domain.AppID, string, int, time.Duration) ([]domain.SocketModeResponse, error)
	RenewSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Duration) error
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
	ScheduleUserStatus(context.Context, domain.WorkspaceID, domain.UserID, string, string, time.Time, time.Time) (domain.ScheduledStatus, error)
	ScheduledUserStatuses(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.ScheduledStatus, error)
	UpdateScheduledUserStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledStatusID, string, string, time.Time, time.Time) (domain.ScheduledStatus, error)
	DeleteScheduledUserStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledStatusID) error
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
	IsConversationMember(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (bool, error)
	WorkspaceInfo(context.Context, domain.WorkspaceID, domain.UserID) (domain.Workspace, error)
	AuthorizedAppWorkspaces(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.PageRequest) (domain.WorkspacePage, error)
	AdminCreateWorkspace(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, domain.WorkspaceDiscoverability) (domain.Workspace, error)
	TeamBillableInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID) (domain.BillableInfo, error)
	Conversations(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationListRequest) (domain.ConversationPage, error)
	OpenConversation(context.Context, domain.WorkspaceID, domain.UserID, []domain.UserID) (domain.Conversation, error)
	AddPeopleToDirectConversation(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, []domain.UserID, domain.DirectHistorySelection) (domain.Conversation, error)
	ConvertGroupDirectToPrivate(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string) (domain.Conversation, error)
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
	WorkspaceNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID) (domain.WorkspaceNotificationPreferences, error)
	SetWorkspaceNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID, domain.NotificationLevel, []string, bool, bool) (domain.WorkspaceNotificationPreferences, error)
	ConversationNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ConversationNotificationPreferences, error)
	SetConversationNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.NotificationLevel, bool) (domain.ConversationNotificationPreferences, error)
	ThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (bool, error)
	SetThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, bool) error
	Activity(context.Context, domain.WorkspaceID, domain.UserID, domain.ActivityQuery) (domain.ActivityPage, error)
	MutateActivity(context.Context, domain.WorkspaceID, domain.UserID, []domain.ActivityID, domain.ActivityMutation) error
	ActivityPreferences(context.Context, domain.WorkspaceID, domain.UserID) (domain.ActivityPreferences, error)
	SetActivityPreferences(context.Context, domain.WorkspaceID, domain.UserID, domain.ActivityLayout) (domain.ActivityPreferences, error)
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
	SaveForLater(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.SavedItem, error)
	SavedItemForMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageID) (domain.SavedItem, error)
	SavedItemsForMessages(context.Context, domain.WorkspaceID, domain.UserID, []domain.MessageID) ([]domain.SavedItem, error)
	SavedItems(context.Context, domain.WorkspaceID, domain.UserID, domain.SavedItemState, domain.PageRequest) (domain.SavedItemPage, error)
	SetSavedItemState(context.Context, domain.WorkspaceID, domain.UserID, domain.SavedItemID, domain.SavedItemState) (domain.SavedItem, error)
	RemoveSavedItem(context.Context, domain.WorkspaceID, domain.UserID, domain.SavedItemID) error
	AddBookmark(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, string, string, string, string, string, string) (domain.Bookmark, error)
	EditBookmark(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.BookmarkID, domain.BookmarkUpdate) (domain.Bookmark, error)
	Bookmarks(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) ([]domain.Bookmark, error)
	RemoveBookmark(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.BookmarkID) error
	AddReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.UserID, string, time.Time) (domain.Reminder, error)
	CompleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) error
	DeleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) error
	ReminderInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) (domain.Reminder, error)
	Reminders(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.ReminderPage, error)
	CreateLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderRequest) (domain.LaterReminder, error)
	LaterReminderInfo(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID) (domain.LaterReminder, error)
	LaterReminders(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderTarget, domain.PageRequest) (domain.LaterReminderPage, error)
	UpdateLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID, domain.LaterReminderRequest) (domain.LaterReminder, error)
	AcknowledgeLaterReminders(context.Context, domain.WorkspaceID, domain.UserID) error
	CompleteLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID) error
	DeleteLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID) error
	ScheduleMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, time.Time) (domain.ScheduledMessage, error)
	ScheduleMessageWithBlocks(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, string, time.Time) (domain.ScheduledMessage, error)
	ScheduleMessageWithBlocksAndAttachments(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, string, string, time.Time) (domain.ScheduledMessage, error)
	ScheduleMessageAs(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledMessageRequest) (domain.ScheduledMessage, error)
	PostScheduledMessage(context.Context, domain.WorkspaceID, domain.ScheduledMessageID) (domain.Message, error)
	ScheduledMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.ScheduledMessagePage, error)
	ScheduledMessagesForCredential(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledMessageQuery) (domain.ScheduledMessagePage, error)
	ScheduledMessageHistory(context.Context, domain.WorkspaceID, domain.UserID, bool, domain.PageRequest) (domain.ScheduledMessagePage, error)
	UpdateScheduledMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledMessageID, domain.ConversationID, string, time.Time) (domain.ScheduledMessage, error)
	SendScheduledMessageNow(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledMessageID) (domain.Message, error)
	DeleteScheduledMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ScheduledMessageID) error
	DeleteScheduledMessageForCredential(context.Context, domain.WorkspaceID, domain.UserID, string, domain.ConversationID, domain.ScheduledMessageID) error
	SaveDraft(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string) (domain.Draft, error)
	SaveDraftWithAttachments(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string, []domain.DraftAttachment) (domain.Draft, error)
	Draft(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.Draft, error)
	Drafts(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.DraftPage, error)
	DeleteDraft(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) error
	SentMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.MessagePage, error)
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
	SearchMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageSearchRequest) (domain.MessagePage, error)
	SearchFiles(context.Context, domain.WorkspaceID, domain.UserID, domain.FileSearchRequest) (domain.FilePage, error)
	RecordSearch(context.Context, domain.WorkspaceID, domain.UserID, string) error
	RecentSearches(context.Context, domain.WorkspaceID, domain.UserID, int) ([]domain.SearchHistoryEntry, error)
	UploadFile(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, int64, io.Reader) (domain.File, error)
	CreateExternalUpload(context.Context, domain.WorkspaceID, domain.UserID, string, string, int64, time.Duration) (domain.ExternalUpload, error)
	UploadExternalFile(context.Context, domain.ExternalUploadID, int64, io.Reader) error
	CompleteExternalUpload(context.Context, domain.WorkspaceID, domain.UserID, domain.ExternalUploadID, string, []domain.ConversationID, string, string, domain.MessageTimestamp) (domain.File, error)
	CompleteExternalUploads(context.Context, domain.WorkspaceID, domain.UserID, []domain.ExternalUploadCompletion, []domain.ConversationID, string, string, domain.MessageTimestamp) ([]domain.File, error)
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
	CreateCanvas(context.Context, domain.WorkspaceID, domain.UserID, string, string, domain.ConversationID) (domain.Canvas, error)
	CreateConversationCanvas(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, string) (domain.Canvas, error)
	ConversationCanvas(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.Canvas, error)
	Canvas(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID) (domain.Canvas, error)
	CanvasAccess(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID) (domain.CanvasAccess, error)
	Canvases(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.CanvasPage, error)
	EditCanvas(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID, string) error
	DeleteCanvas(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID) error
	SetCanvasAccess(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID, string, []domain.ConversationID, []domain.UserID) error
	DeleteCanvasAccess(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID, []domain.ConversationID, []domain.UserID) error
	LookupCanvasSections(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID, string) ([]domain.CanvasSection, error)
	CreateList(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, domain.ListID, bool, bool) (domain.List, error)
	List(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID) (domain.List, error)
	ListAccess(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID) (domain.ListAccess, error)
	Lists(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.ListPage, error)
	UpdateList(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, string, string, bool, bool) (domain.List, error)
	CreateListItem(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, domain.ListItemID, string) (domain.ListItem, error)
	GetListItem(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, domain.ListItemID) (domain.ListItem, error)
	ListItems(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, domain.PageRequest, bool) (domain.ListItemPage, error)
	UpdateListItem(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, domain.ListItemID, string, bool) (domain.ListItem, error)
	UpdateListCells(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, string) ([]domain.ListItem, error)
	DeleteListItems(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, []domain.ListItemID) error
	SetListAccess(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, string, []domain.ConversationID, []domain.UserID) error
	DeleteListAccess(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, []domain.ConversationID, []domain.UserID) error
	StartListDownload(context.Context, domain.WorkspaceID, domain.UserID, domain.ListID, bool) (domain.ListDownload, error)
	GetListDownload(context.Context, domain.WorkspaceID, domain.UserID, domain.ListDownloadID) (domain.ListDownload, error)
	PresentEntityDetails(context.Context, domain.WorkspaceID, domain.UserID, string, string, bool, string, string) error
	PresentEntityComments(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, bool, string, bool, string, string) error
	AcknowledgeEntityCommentAction(context.Context, domain.WorkspaceID, domain.UserID, string, string, string) error
	events.Source
}
