package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func ScheduledMessageCursorKey(value domain.ScheduledMessage) string {
	return fmt.Sprintf("%020d:%s", value.PostAt.UTC().Unix(), value.ID)
}

func ParseScheduledMessageCursor(cursor domain.Cursor) (time.Time, domain.ScheduledMessageID, error) {
	raw, err := domain.DecodeListCursor(cursor)
	if err != nil || raw == "" {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", domain.ErrInvalidCursor
	}
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", domain.ErrInvalidCursor
	}
	return time.Unix(seconds, 0).UTC(), domain.ScheduledMessageID(parts[1]), nil
}

// ScheduledMessageLimitExceeded reports whether adding candidate would place
// more than limit timestamps in any rolling window. A fixed clock bucket is
// insufficient here: Slack's contract says "within a 5-minute window", so a
// burst spanning a wall-clock bucket boundary must still be rejected.
func ScheduledMessageLimitExceeded(existing []time.Time, candidate time.Time, window time.Duration, limit int) bool {
	if window <= 0 || limit <= 0 {
		return true
	}
	timestamps := append(append([]time.Time(nil), existing...), candidate.UTC())
	sort.Slice(timestamps, func(left, right int) bool { return timestamps[left].Before(timestamps[right]) })
	left := 0
	for right := range timestamps {
		for timestamps[right].Sub(timestamps[left]) > window {
			left++
		}
		if right-left+1 > limit {
			return true
		}
	}
	return false
}

// InternalTopic reports whether an outbox topic carries a repository-internal
// payload — a blob storage key — that exists only for a dedicated cleanup
// worker. Internal topics must never be claimed by the general outbox worker and
// must never appear in a client-facing replay, so both repositories consult this
// one predicate instead of each maintaining its own list.
func InternalTopic(topic string) bool {
	return topic == events.FileBlobDeleteTopic || topic == events.UserPhotoBlobDeleteTopic
}

// InternalTopics is the same set in the form a SQL IN predicate needs.
func InternalTopics() []string {
	return []string{events.FileBlobDeleteTopic, events.UserPhotoBlobDeleteTopic}
}

// OAuthCodeLifetime bounds how long an issued authorization code may be
// redeemed. RFC 6749 section 4.1.2 requires a short lifetime and recommends a
// maximum of ten minutes; both repositories derive the expiry from this one
// constant so a code cannot outlive it on one storage profile and not another.
const OAuthCodeLifetime = 10 * time.Minute

// MaxSearchHistoryEntries bounds private per-member recent-search state. The
// first-party UI displays a smaller window, while the repository keeps enough
// history for useful matching without allowing an unbounded query log.
const MaxSearchHistoryEntries = 50

// The access levels a list or canvas grant can carry. They are the values the
// service layer already writes through SetListAccess and SetCanvasAccess; naming
// them here keeps the readers, the writers and the authorization decision that
// consumes them from drifting apart.
const (
	AccessRead  = "read"
	AccessWrite = "write"
	AccessOwner = "owner"
)

// AccessRank orders the access levels so a resolved grant can be compared with
// the level an operation requires: AccessRank(granted) >= AccessRank(required).
// An unrecognised level ranks 0, below every real grant, so an unknown string
// can never satisfy a requirement.
func AccessRank(level string) int {
	switch level {
	case AccessRead:
		return 1
	case AccessWrite:
		return 2
	case AccessOwner:
		return 3
	default:
		return 0
	}
}

// ListItemCreation pairs a list item with the event that announces it. The two
// are carried together so a caller cannot hand CreateListWithItems a set of
// items and a set of events that do not correspond.
type ListItemCreation struct {
	Item  domain.ListItem
	Event events.Event
}

// BetterAccessGrant reports whether one grant should replace another as the
// grant that decided a resolved access level.
//
// The port promises that the returned grant "names the grant that decided the
// outcome, so a caller can report why access was allowed". Both repositories
// broke that promise the same way: they kept the first grant of the highest rank
// they happened to see, over a randomised Go map in one and over a query with no
// ORDER BY in the other. A user holding write through two channels got a
// different answer on successive identical calls. The level was stable, so this
// was never an authorization defect — it made the documented "why" unusable and
// any test asserting it flaky.
//
// The order is: higher access rank first, then a direct user grant ahead of a
// channel grant, then the lower entity identifier. A level this build does not
// recognise ranks zero and can never win, so an unknown string cannot become the
// reported reason.
func BetterAccessGrant(entityType, entityID, level, bestType, bestID, bestLevel string) bool {
	rank, bestRank := AccessRank(level), AccessRank(bestLevel)
	if rank == 0 {
		return false
	}
	if rank != bestRank {
		return rank > bestRank
	}
	if accessEntityRank(entityType) != accessEntityRank(bestType) {
		return accessEntityRank(entityType) < accessEntityRank(bestType)
	}
	return entityID < bestID
}

func accessEntityRank(entityType string) int {
	if entityType == "user" {
		return 0
	}
	return 1
}

var (
	ErrNotFound                  = errors.New("not found")
	ErrLeaseConflict             = errors.New("outbox lease conflict")
	ErrIdempotencyConflict       = errors.New("idempotency key already committed")
	ErrAlreadyExists             = errors.New("already exists")
	ErrInvalidArgument           = errors.New("invalid argument")
	ErrInvalidConversationType   = errors.New("invalid conversation type")
	ErrInvalidInviteRequest      = errors.New("invalid invite request")
	ErrInvalidAppApproval        = errors.New("invalid app approval")
	ErrConflict                  = errors.New("state conflict")
	ErrBookmarkLimit             = errors.New("bookmark limit reached")
	ErrScheduledMessageLimit     = errors.New("scheduled message channel window limit reached")
	ErrScheduledStatusLimit      = errors.New("scheduled status limit reached")
	ErrSocketModeConnectionLimit = errors.New("Socket Mode connection limit reached")
	// ErrMessageTimestampTaken reports that another message in the same
	// conversation already owns the microsecond the new message was given.
	//
	// A message's Slack-style timestamp IS its public identifier: chat.update,
	// chat.delete, reactions.add and thread-root resolution all address a message
	// by it, and it carries microseconds and no more. Two messages on one
	// microsecond therefore share one identifier, and the second becomes
	// permanently unaddressable. The real Slack timestamp is unique by
	// construction, and so is this one: the repository refuses the collision and
	// the caller advances by one microsecond, which is what the service does.
	ErrMessageTimestampTaken = errors.New("message timestamp already taken")
	// ErrTransient reports a condition the engine expects the caller to retry:
	// a serialization failure, a deadlock victim, a lock timeout, a lost leader.
	// It exists so a routine retryable outcome reaches the transport as a
	// classified error — AGENTS.md: handled errors must not become HTTP 500 —
	// instead of as raw driver text.
	ErrTransient = errors.New("transient storage failure")
)

// InvalidArgument classifies a malformed request as a caller mistake.
//
// Every guard on a request path returns through here. They used to return bare
// errors.New values, which no transport had classified, so a missing identifier
// or a non-positive page limit reached the caller as HTTP 500 and as
// codes.Unavailable across the chat seam — an answer that asks a caller to retry
// a request that can never succeed. AGENTS.md: handled errors must not become
// HTTP 500 responses. The guard belongs here rather than at the transport,
// because a transport-side guard is a second copy of the rule that can diverge
// from this one, and one such divergence was deliberately deleted already.
func InvalidArgument(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, reason)
}

// CheckPage validates the bounds every paged read shares. It permits either
// direction, so only a read that implements domain.PageRequest.Descending may
// use it; every other paged read uses CheckAscendingPage.
func CheckPage(request domain.PageRequest) error {
	if request.Limit <= 0 {
		return InvalidArgument("page limit must be positive")
	}
	return nil
}

// CheckAscendingPage is CheckPage plus the refusal that keeps
// domain.PageRequest.Descending honest.
//
// Descending is one field on the one page request every read shares, and only
// the message reads implement it. A read that ignored it would answer the oldest
// page to a caller that asked for the newest and report success, which is a
// worse outcome than the walk the field exists to remove. Refusing names the
// limitation at the boundary that has it.
func CheckAscendingPage(request domain.PageRequest) error {
	if err := CheckPage(request); err != nil {
		return err
	}
	if request.Descending {
		return InvalidArgument("this read does not page in descending order")
	}
	return nil
}

type Store interface {
	AppendEvent(context.Context, events.Event) error
	RecordAccess(context.Context, domain.AccessLog) error
	ListAccessLogs(context.Context, domain.WorkspaceID, time.Time, int, int) ([]domain.AccessLog, bool, error)
	LookupToken(context.Context, string) (domain.TokenRecord, error)
	LookupAppToken(context.Context, string) (domain.AppTokenRecord, error)
	CreateAppToken(context.Context, string, domain.AppTokenRecord) error
	LookupSession(context.Context, string) (domain.SessionRecord, error)
	CreateSession(context.Context, string, domain.SessionRecord) error
	// GetAuthMethod reports the persisted ADMINISTRATIVE OVERRIDE for one
	// authorization provider. A provider with no stored row reports
	// Enabled: true and a nil error, and that is the specified behaviour, not an
	// accident: this table records an administrator's decision to turn a
	// provider OFF. Whether a provider exists at all is decided by the
	// operator's startup configuration — issuer, client identifier, secret — so
	// a provider that reaches this call is one the operator configured.
	//
	// Do not "fix" this to report ErrNotFound or to fail closed. A new
	// deployment has no rows in this table, so absence-means-disabled disables
	// every provider at once, and the administrator who would re-enable one
	// cannot sign in either: there is no bootstrap path out of that state. The
	// port, the SQL repository and the in-memory repository all documented the
	// opposite of what all three of them did, which is how a security-relevant
	// contract came to be stated three times and wrong three times.
	//
	// An implementation of this port that returns ErrNotFound for a missing row
	// is a locked-out deployment.
	GetAuthMethod(context.Context, domain.WorkspaceID, string) (domain.AuthMethod, error)
	SetAuthMethod(context.Context, domain.AuthMethod) error
	GetExternalIdentity(context.Context, domain.WorkspaceID, string, string) (domain.ExternalIdentity, error)
	CreateExternalIdentity(context.Context, domain.ExternalIdentity) error
	RevokeSession(context.Context, string) error
	RevokeOIDCSessions(context.Context, domain.WorkspaceID, string, string, string, string, time.Time, events.Event) error
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
	// UpdateUserProfile commits the profile change and EVERY event given with it
	// in one transaction.
	//
	// It is variadic because the photo journeys need two: the
	// user.profile_changed announcement and the internal
	// user.photo_blob_delete instruction that retires the bytes the old profile
	// referenced. Appending the second through a separate AppendEvent left a
	// window in which the profile no longer names the old blob and nothing has
	// been told to delete it, so a crash there orphaned the blob permanently —
	// a leak with no reconciler behind it. A domain change and the events that
	// describe it are one unit of work.
	UpdateUserProfile(context.Context, domain.WorkspaceID, domain.UserID, domain.UserProfile, ...events.Event) (domain.User, error)
	// DueUserStatuses and ExpireUserStatus form a compare-and-set queue. More
	// than one worker may observe the same due profile, but only the worker whose
	// expected deadline still matches may clear it and publish the event.
	DueUserStatuses(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.User, error)
	EarliestUserStatusExpiration(context.Context, domain.WorkspaceID) (time.Time, error)
	ExpireUserStatus(context.Context, domain.WorkspaceID, domain.UserID, time.Time, domain.ScheduledStatusID, time.Time, events.Event) (bool, error)
	CreateScheduledStatus(context.Context, domain.ScheduledStatus) error
	GetScheduledStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledStatusID) (domain.ScheduledStatus, error)
	ListScheduledStatuses(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.ScheduledStatus, error)
	UpdateScheduledStatus(context.Context, domain.ScheduledStatus) error
	DeleteScheduledStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledStatusID) error
	DueScheduledStatuses(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.ScheduledStatus, error)
	EarliestScheduledStatusStart(context.Context, domain.WorkspaceID) (time.Time, error)
	ActivateScheduledStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledStatusID, time.Time, time.Time, events.Event) (bool, error)
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
	// ExpandDirectConversation atomically creates a new canonical group DM,
	// optionally copies the source history and its file visibility, and posts
	// Slack's participant notices to both conversations. The source membership
	// is immutable.
	ExpandDirectConversation(context.Context, domain.DirectConversationExpansion, []events.Event) error
	// ConvertGroupDirectToPrivate atomically changes an MPIM into a private
	// channel and posts the conversion notice without replacing its identity,
	// membership, messages, files, drafts, or read state.
	ConvertGroupDirectToPrivate(context.Context, domain.GroupDirectConversion, []events.Event) (domain.Conversation, error)
	// SetDirectConversationOpen changes only one member's navigation state.
	// Closing a DM must never remove conversation membership or history.
	// The bool reports whether durable state changed.
	SetDirectConversationOpen(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, bool, events.Event) (bool, error)
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
	CreateAppConfigurationToken(context.Context, string, string, domain.AppConfigurationToken) error
	LookupAppConfigurationToken(context.Context, string) (domain.AppConfigurationToken, error)
	LookupAppConfigurationRefreshToken(context.Context, string) (domain.AppConfigurationToken, error)
	RotateAppConfigurationToken(context.Context, string, string, string, domain.AppConfigurationToken) error
	CreateApp(context.Context, domain.App, domain.AppManifestRevision, domain.OAuthClient) error
	GetApp(context.Context, domain.AppID) (domain.App, domain.AppManifestRevision, error)
	GetAppByClientID(context.Context, string) (domain.App, domain.AppManifestRevision, error)
	ListDeveloperApps(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.App, error)
	UpdateApp(context.Context, domain.App, domain.AppManifestRevision) error
	DeleteApp(context.Context, domain.AppID, domain.UserID, time.Time) error
	CreateAppInstallation(context.Context, domain.AppInstallation) error
	ListAppInstallations(context.Context, domain.AppID) ([]domain.AppInstallation, error)
	ListAppAuthorizations(context.Context, domain.AppID, domain.WorkspaceID) ([]domain.AppAuthorization, error)
	UninstallApp(context.Context, domain.WorkspaceID, domain.AppID) error
	CreateIncomingWebhook(context.Context, domain.IncomingWebhook) error
	LookupIncomingWebhook(context.Context, domain.WorkspaceID, domain.AppID, string) (domain.IncomingWebhook, error)
	SetIncomingWebhookEnabled(context.Context, domain.WorkspaceID, domain.IncomingWebhookID, bool, events.Event) error
	PutAppDatastoreItems(context.Context, []domain.AppDatastoreItem) error
	MergeAppDatastoreItems(context.Context, []domain.AppDatastoreItem) ([]domain.AppDatastoreItem, error)
	GetAppDatastoreItems(context.Context, domain.AppID, domain.WorkspaceID, string, []string) ([]domain.AppDatastoreItem, error)
	ListAppDatastoreItems(context.Context, domain.AppID, domain.WorkspaceID, string, domain.PageRequest) ([]domain.AppDatastoreItem, bool, domain.Cursor, error)
	DeleteAppDatastoreItems(context.Context, domain.AppID, domain.WorkspaceID, string, []string) error
	CreateAppPermissionRequest(context.Context, domain.AppPermissionRequest, events.Event) error
	CreateView(context.Context, domain.View, events.Event) error
	GetView(context.Context, domain.WorkspaceID, domain.ViewID) (domain.View, error)
	GetViewByExternalID(context.Context, domain.WorkspaceID, domain.AppID, string) (domain.View, error)
	GetPublishedView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID) (domain.View, error)
	GetLatestView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, string) (domain.View, error)
	GetCurrentView(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.View, error)
	UpdateView(context.Context, domain.View, string, events.Event) (domain.View, error)
	DeleteView(context.Context, domain.WorkspaceID, domain.UserID, domain.ViewID, bool, events.Event) error
	SetWorkflowStep(context.Context, domain.WorkflowStep, events.Event) error
	GetWorkflowStep(context.Context, domain.WorkspaceID, domain.WorkflowStepID) (domain.WorkflowStep, error)
	CreateWorkflow(context.Context, domain.WorkflowDefinition, events.Event) error
	UpdateWorkflow(context.Context, domain.WorkflowDefinition, uint64, events.Event) error
	// DiscardWorkflowStagedChanges reverts the head row to the published
	// revision and realigns Version with PublishedVersion. The expectedVersion
	// is the staged version being discarded.
	DiscardWorkflowStagedChanges(context.Context, domain.WorkspaceID, domain.WorkflowID, uint64, events.Event) (bool, error)
	// DeleteWorkflow removes a workflow with its revisions, triggers, runs,
	// and steps in one transaction, cancelling every running execution with
	// the workflow_unpublished error first. It reports false when the expected
	// version moved underneath the caller.
	DeleteWorkflow(context.Context, domain.WorkspaceID, domain.WorkflowID, uint64, events.Event) (bool, error)
	GetWorkflow(context.Context, domain.WorkspaceID, domain.WorkflowID) (domain.WorkflowDefinition, error)
	ListWorkflows(context.Context, domain.WorkspaceID, domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error)
	ListWorkflowRevisions(context.Context, domain.WorkspaceID, domain.WorkflowID) ([]domain.WorkflowRevision, error)
	SetWorkflowTrigger(context.Context, domain.WorkflowTrigger, uint64, events.Event) error
	GetWorkflowTrigger(context.Context, domain.WorkspaceID, domain.WorkflowTriggerID) (domain.WorkflowTrigger, error)
	ListWorkflowTriggers(context.Context, domain.WorkspaceID, domain.WorkflowID) ([]domain.WorkflowTrigger, error)
	// The scheduled/event trigger queue methods define an empty workspace as
	// the global queue, matching the scheduled-message and reminder queues the
	// same worker process drives.
	DueScheduledWorkflowTriggers(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.WorkflowTrigger, error)
	EarliestScheduledWorkflowTrigger(context.Context, domain.WorkspaceID) (time.Time, error)
	CompleteScheduledWorkflowTrigger(context.Context, domain.WorkspaceID, domain.WorkflowTriggerID, time.Time, time.Time, events.Event) (bool, error)
	ListWorkflowEventTriggers(context.Context, domain.WorkspaceID) ([]domain.WorkflowTrigger, error)
	GetWorkflowEventCursor(context.Context, domain.WorkspaceID) (uint64, error)
	AdvanceWorkflowEventCursor(context.Context, domain.WorkspaceID, uint64) error
	CreateWorkflowRun(context.Context, domain.WorkflowRun, *domain.WorkflowStep, []events.Event) error
	AdvanceWorkflowRun(context.Context, domain.WorkflowStep, *domain.WorkflowStep, domain.WorkflowRun, int, []events.Event) error
	GetWorkflowRun(context.Context, domain.WorkspaceID, domain.WorkflowRunID) (domain.WorkflowRun, error)
	GetWorkflowRunByIdempotency(context.Context, domain.WorkspaceID, string) (domain.WorkflowRun, error)
	ListWorkflowRuns(context.Context, domain.WorkspaceID, domain.WorkflowID, domain.PageRequest) ([]domain.WorkflowRun, bool, domain.Cursor, error)
	SetAutomationPermission(context.Context, domain.AutomationPermission, events.Event) error
	GetAutomationPermission(context.Context, domain.WorkspaceID, string, string) (domain.AutomationPermission, error)
	SetFeaturedWorkflows(context.Context, domain.WorkspaceID, domain.ConversationID, []domain.FeaturedWorkflow, events.Event) error
	ListFeaturedWorkflows(context.Context, domain.WorkspaceID, []domain.ConversationID) ([]domain.FeaturedWorkflow, error)
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
	CreateOAuthAuthorization(context.Context, domain.User, domain.Bot, domain.OAuthCode) error
	ExchangeOAuthCode(context.Context, string, string, string, string, string, domain.OAuthToken) (domain.OAuthToken, error)
	LookupOAuthRefreshToken(context.Context, string, string) (domain.OAuthRefreshGrant, error)
	ExchangeOAuthRefreshToken(context.Context, string, string, string, string, string, time.Time) (domain.OAuthToken, error)
	ExchangeOAuthAccessToken(context.Context, string, string, string, string, string, time.Time) (domain.OAuthToken, error)
	CreateOpenIDRefreshToken(context.Context, domain.OpenIDRefreshToken) error
	ExchangeOpenIDRefreshToken(context.Context, string, string, string, string, domain.OpenIDToken) (domain.OpenIDToken, error)
	CreateRTMConnection(context.Context, domain.RTMConnection) error
	ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error)
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
	RenewSocketModeConnection(context.Context, string, time.Time) error
	ReleaseSocketModeConnection(context.Context, string) error
	CountSocketModeConnections(context.Context, domain.AppID) (int, error)
	RecordSocketModeResponse(context.Context, domain.SocketModeResponse) error
	GetSocketModeResponse(context.Context, domain.AppID, string) (domain.SocketModeResponse, error)
	ClaimSocketModeResponses(context.Context, domain.AppID, string, int, time.Duration) ([]domain.SocketModeResponse, error)
	RenewSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Duration) error
	AckSocketModeResponses(context.Context, string, []domain.SocketModeResponse) error
	ReleaseSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Time) error
	CreateSocketModeInteraction(context.Context, domain.SocketModeInteraction) error
	GetSocketModeInteraction(context.Context, domain.AppID, string) (domain.SocketModeInteraction, error)
	ClaimSocketModeInteraction(context.Context, domain.AppID, string, time.Duration) (domain.SocketModeInteraction, bool, error)
	AckSocketModeInteraction(context.Context, domain.AppID, string, string) error
	ReleaseSocketModeInteraction(context.Context, domain.AppID, string, string, string, time.Time) error
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
	GetWorkspaceNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID) (domain.WorkspaceNotificationPreferences, error)
	SetWorkspaceNotificationPreferences(context.Context, domain.WorkspaceNotificationPreferences, events.Event) error
	GetConversationNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ConversationNotificationPreferences, error)
	SetConversationNotificationPreferences(context.Context, domain.ConversationNotificationPreferences, events.Event) error
	IsThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (bool, error)
	SetThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, bool, events.Event) error
	ListActivity(context.Context, domain.WorkspaceID, domain.UserID, domain.ActivityQuery) (domain.ActivityPage, error)
	MutateActivity(context.Context, domain.WorkspaceID, domain.UserID, []domain.ActivityID, domain.ActivityMutation, time.Time) error
	GetActivityPreferences(context.Context, domain.WorkspaceID, domain.UserID) (domain.ActivityPreferences, error)
	SetActivityPreferences(context.Context, domain.ActivityPreferences) error
	ListUsers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.UserPage, error)
	ListAdminUsers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.AdminUserPage, error)
	ListUsersByRole(context.Context, domain.WorkspaceID, domain.WorkspaceRole, domain.PageRequest) (domain.UserPage, error)
	ListConversationMembers(context.Context, domain.ConversationID, domain.PageRequest) (domain.UserPage, error)
	ListConversations(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationListRequest) (domain.ConversationPage, error)
	SearchConversations(context.Context, domain.WorkspaceID, string, domain.PageRequest) (domain.ConversationPage, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error)
	ListAppEventsAfter(context.Context, domain.AppID, uint64, int) ([]events.Record, error)
	ListInstalledApps(context.Context) ([]domain.AppManifestSnapshot, error)
	CreateAppTrigger(context.Context, domain.AppTrigger) error
	CreateAppInteractionCapabilities(context.Context, domain.AppTrigger, domain.AppResponseURL) error
	ConsumeAppTrigger(context.Context, string, domain.AppID) (domain.AppTrigger, error)
	UseAppResponseURL(context.Context, string) (domain.AppResponseURL, error)
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
	ClaimAppEvent(context.Context, domain.AppID, string, string, time.Duration) (events.Record, int, string, bool, error)
	AckAppEvent(context.Context, domain.AppID, string, string, uint64) error
	ReleaseAppEvent(context.Context, domain.AppID, string, string, uint64, string, time.Time) error
	GetAppEventCursor(context.Context, domain.AppID, string) (domain.AppEventCursor, error)
	ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error)
	ClaimEventsForTopic(context.Context, domain.WorkspaceID, string, string, int, time.Duration) ([]events.Record, error)
	RenewEvents(context.Context, string, []uint64, time.Duration) error
	AckEvents(context.Context, string, []uint64) error
	ReleaseEvents(context.Context, string, []uint64, time.Time) error
	GetMessageByCreatedAt(context.Context, domain.ConversationID, time.Time) (domain.Message, error)
	GetIdempotentMessage(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.Message, error)
	UpdateMessage(context.Context, domain.Message, events.Event) error
	// CreateMessage stores a message and the event announcing it in one
	// transaction, at an instant no other message in the same conversation owns.
	//
	// created_at is truncated to the microsecond its own Slack-style timestamp
	// can express, and (conversation, created_at) is UNIQUE. Both halves are the
	// contract: without the truncation a read cursor built from a message's own
	// ts can never cover it, and without the uniqueness two messages share one
	// public identifier and the second becomes permanently unaddressable —
	// chat.update, chat.delete, reactions.add and thread-root resolution all
	// address a message by that string.
	//
	// A caller that supplies an instant already taken is told so with
	// ErrMessageTimestampTaken and advances by one microsecond. The choice to
	// report rather than to silently relocate is deliberate: the event announcing
	// the message carries the timestamp too, and a repository that moved the row
	// without the caller's knowledge would publish an announcement naming an
	// identifier that is not in the database. There is exactly one producer in
	// the product — service.Messages.PostWithBlocksAndAttachments — it rebuilds
	// the event from the instant it retries with, and no API surface can observe
	// the sentinel. A fixture that hands the repository two colliding instants is
	// in the same position as one that hands it two identical identifiers.
	CreateMessage(context.Context, domain.Message, events.Event, string) error
	CreateScheduledMessagePost(context.Context, domain.ScheduledMessageID, domain.Message, events.Event) error
	CreateEphemeralMessage(context.Context, domain.EphemeralMessage, events.Event) error
	GetEphemeralMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageID) (domain.EphemeralMessage, error)
	ListEphemeralMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, int) ([]domain.EphemeralMessage, error)
	UpdateEphemeralMessage(context.Context, domain.EphemeralMessage, events.Event) error
	DeleteEphemeralMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageID, events.Event) error
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	// ListMessages pages the non-deleted messages in one conversation in either
	// direction. Deleted rows remain individually addressable through GetMessage
	// for mutation and audit paths, but history must not expose their retained
	// text or let tombstones consume a whole visible page.
	//
	// domain.PageRequest.Descending selects `ORDER BY created_at DESC, id DESC`
	// with a backward-walking cursor, and it is the only read that implements it.
	// Without it the newest window of a conversation was reachable only by walking
	// the whole conversation forward: internal/web bounded that walk, so a
	// conversation past the bound had NO reachable newest window — paging forward,
	// "jump to the latest" and a search permalink all landed on the same stale
	// window — and internal/api/slack scanned and filtered for the same reason.
	// One descending page of Limit+1 rows answers both.
	//
	// The two directions are the same read: a full forward walk and a full
	// backward walk of one conversation visit exactly the same rows in opposite
	// order, across page boundaries and while messages are being written, because
	// both compare the same (created_at, id) key through
	// domain.PageRequest.PageAfter and both take NextCursor from the last row of
	// the page. A cursor carries no direction, so one minted walking backwards
	// resumes a forward walk from the same row.
	ListMessages(context.Context, domain.ConversationID, domain.PageRequest) (domain.MessagePage, error)
	// ListThreadMessages has the same non-deleted history boundary as
	// ListMessages, in chronological order.
	ListThreadMessages(context.Context, domain.ConversationID, domain.MessageTimestamp, domain.PageRequest) (domain.MessagePage, error)
	ListAuthoredMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.MessagePage, error)
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
	CreateSavedItem(context.Context, domain.SavedItem, events.Event) (domain.SavedItem, bool, error)
	GetSavedItem(context.Context, domain.WorkspaceID, domain.UserID, domain.SavedItemID) (domain.SavedItem, error)
	GetSavedItemByMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageID) (domain.SavedItem, error)
	ListSavedItemsForMessages(context.Context, domain.WorkspaceID, domain.UserID, []domain.MessageID) ([]domain.SavedItem, error)
	ListSavedItems(context.Context, domain.WorkspaceID, domain.UserID, domain.SavedItemState, domain.PageRequest) (domain.SavedItemPage, error)
	UpdateSavedItem(context.Context, domain.SavedItem, events.Event) (domain.SavedItem, error)
	DeleteSavedItem(context.Context, domain.WorkspaceID, domain.UserID, domain.SavedItemID, events.Event) error
	CreateBookmark(context.Context, domain.Bookmark, events.Event) error
	GetBookmark(context.Context, domain.WorkspaceID, domain.ConversationID, domain.BookmarkID) (domain.Bookmark, error)
	ListBookmarks(context.Context, domain.WorkspaceID, domain.ConversationID) ([]domain.Bookmark, error)
	UpdateBookmark(context.Context, domain.Bookmark, events.Event) (domain.Bookmark, error)
	DeleteBookmark(context.Context, domain.WorkspaceID, domain.ConversationID, domain.BookmarkID, events.Event) error
	CreateReminder(context.Context, domain.Reminder, events.Event) error
	GetReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID) (domain.Reminder, error)
	ListReminders(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.ReminderPage, error)
	CompleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID, time.Time, events.Event) error
	DeleteReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.ReminderID, events.Event) error
	CreateLaterReminder(context.Context, domain.LaterReminder, events.Event) error
	GetLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID) (domain.LaterReminder, error)
	ListLaterReminders(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderTarget, domain.PageRequest) (domain.LaterReminderPage, error)
	UpdateLaterReminder(context.Context, domain.LaterReminder, events.Event) (domain.LaterReminder, error)
	AcknowledgeLaterReminders(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) error
	CompleteLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID, time.Time, events.Event) error
	DeleteLaterReminder(context.Context, domain.WorkspaceID, domain.UserID, domain.LaterReminderID, events.Event) error
	EarliestLaterReminder(context.Context, domain.WorkspaceID) (time.Time, error)
	ClaimDueLaterReminders(context.Context, domain.WorkspaceID, string, int, time.Duration, time.Time) ([]domain.LaterReminder, error)
	RenewLaterReminder(context.Context, string, domain.LaterReminderID, time.Duration, time.Time) error
	MarkLaterReminderDelivered(context.Context, string, domain.LaterReminderID, time.Time, time.Time, events.Event) error
	MarkLaterReminderFailed(context.Context, string, domain.LaterReminderID, string, time.Time, events.Event) error
	ReleaseLaterReminder(context.Context, string, domain.LaterReminderID, time.Time, time.Time) error
	CreateScheduledMessage(context.Context, domain.ScheduledMessage, events.Event) error
	CreateScheduledMessageWithinLimit(context.Context, domain.ScheduledMessage, time.Duration, int, events.Event) error
	ListScheduledMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.PageRequest) (domain.ScheduledMessagePage, error)
	ListScheduledMessagesForCredential(context.Context, domain.WorkspaceID, domain.ScheduledMessageQuery) (domain.ScheduledMessagePage, error)
	ListScheduledMessageHistory(context.Context, domain.WorkspaceID, string, bool, domain.PageRequest) (domain.ScheduledMessagePage, error)
	EarliestScheduledMessage(context.Context, domain.WorkspaceID) (time.Time, error)
	GetScheduledMessage(context.Context, domain.WorkspaceID, domain.ScheduledMessageID) (domain.ScheduledMessage, error)
	UpdateScheduledMessageWithinLimit(context.Context, domain.ScheduledMessageUpdate, time.Duration, int, events.Event) (domain.ScheduledMessage, error)
	ClaimScheduledMessageForCredential(context.Context, domain.WorkspaceID, string, domain.ScheduledMessageID, string, time.Duration) (domain.ScheduledMessage, error)
	DeleteScheduledMessage(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.ScheduledMessageID, events.Event) error
	DeleteScheduledMessageForCredential(context.Context, domain.WorkspaceID, string, domain.ConversationID, domain.ScheduledMessageID, events.Event) error
	ClaimScheduledMessages(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]domain.ScheduledMessage, error)
	RenewScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Duration) error
	MarkScheduledMessageDelivered(context.Context, string, domain.ScheduledMessageID) error
	MarkScheduledMessageFailed(context.Context, string, domain.ScheduledMessageID, string, time.Time, events.Event) error
	ReleaseScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Time) error
	UpsertDraft(context.Context, domain.Draft, events.Event) (domain.Draft, error)
	GetDraft(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.Draft, error)
	ListDrafts(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.DraftPage, error)
	DeleteDraft(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, events.Event) error
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
	CreateExternalUpload(context.Context, domain.ExternalUpload) error
	GetExternalUpload(context.Context, domain.ExternalUploadID) (domain.ExternalUpload, error)
	PendingUploadReferenceExists(context.Context, domain.WorkspaceID, domain.UserID, domain.ExternalUploadID) (bool, error)
	MarkExternalUploadUploaded(context.Context, domain.ExternalUploadID, time.Time) error
	CompleteExternalUpload(context.Context, domain.ExternalUploadID, domain.File, []domain.ConversationID, events.Event) error
	CompleteExternalUploads(context.Context, []domain.ExternalUploadCompletion, []domain.File, []domain.ConversationID, []events.Event, []domain.Message, []events.Event) error
	CompleteScheduledExternalUploads(context.Context, domain.ScheduledMessageID, []domain.ExternalUploadCompletion, []domain.File, []domain.ConversationID, []events.Event, domain.Message, events.Event) error
	CreateFileShareMessage(context.Context, []domain.FileID, domain.Message, []events.Event) error
	GetFile(context.Context, domain.FileID) (domain.File, error)
	DeleteFile(context.Context, domain.FileID, events.Event) error
	DeleteFileComment(context.Context, domain.WorkspaceID, domain.FileID, domain.FileCommentID, events.Event) error
	ShareFilePublic(context.Context, domain.WorkspaceID, domain.FileID, string, events.Event) error
	RevokeFilePublic(context.Context, domain.WorkspaceID, domain.FileID, events.Event) error
	GetPublicFile(context.Context, string) (domain.File, error)
	ListFiles(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.FilePage, error)
	ListVisibleFiles(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FilePage, error)
	SearchFiles(context.Context, domain.WorkspaceID, domain.UserID, domain.FileSearch) (domain.FilePage, error)
	RecordSearchHistory(context.Context, domain.SearchHistoryEntry) error
	ListSearchHistory(context.Context, domain.WorkspaceID, domain.UserID, int) ([]domain.SearchHistoryEntry, error)
	WalkBlobReferences(context.Context, domain.WorkspaceID, func(string) error) error
	AddRemoteFile(context.Context, domain.RemoteFile, events.Event) error
	GetRemoteFile(context.Context, domain.WorkspaceID, domain.RemoteFileLookup) (domain.RemoteFile, error)
	ListRemoteFiles(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.RemoteFilePage, error)
	RemoveRemoteFile(context.Context, domain.WorkspaceID, domain.RemoteFileLookup, events.Event) error
	SetRemoteFileShares(context.Context, domain.WorkspaceID, domain.RemoteFileLookup, []domain.ConversationID, events.Event) (domain.RemoteFile, error)
	UpdateRemoteFile(context.Context, domain.WorkspaceID, domain.RemoteFile, events.Event) (domain.RemoteFile, error)
	CreateCanvas(context.Context, domain.Canvas, events.Event) error
	CreateCanvasWithAccess(context.Context, domain.Canvas, events.Event, domain.CanvasAccess, events.Event) error
	CreateChannelCanvas(context.Context, domain.Canvas, events.Event, domain.ConversationID, events.Event) error
	GetChannelCanvas(context.Context, domain.WorkspaceID, domain.ConversationID) (domain.Canvas, error)
	GetCanvas(context.Context, domain.WorkspaceID, domain.CanvasID) (domain.Canvas, error)
	ListCanvases(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.CanvasPage, error)
	UpdateCanvas(context.Context, domain.Canvas, events.Event) error
	DeleteCanvas(context.Context, domain.WorkspaceID, domain.CanvasID, events.Event) error
	SetCanvasAccess(context.Context, domain.CanvasAccess, events.Event) error
	DeleteCanvasAccess(context.Context, domain.CanvasAccess, events.Event) error
	// GetCanvasAccess resolves the effective access one user has to one canvas.
	// See GetListAccess for the resolution rules; canvases follow them exactly.
	GetCanvasAccess(context.Context, domain.CanvasID, domain.UserID) (domain.CanvasAccess, error)
	SearchMessages(context.Context, domain.WorkspaceID, domain.UserID, domain.MessageSearch) (domain.MessagePage, error)
	CreateList(context.Context, domain.List, events.Event) error
	// CreateListWithItems creates a list and its initial items as one unit.
	//
	// lists.create with copy_from used to create the list, publish list.created,
	// and then copy the source items one store call at a time. A failure partway
	// through left a half-copied list that clients had already been told about,
	// with no cleanup path: the caller could neither finish nor undo. Pairing
	// each item with its own event in ListItemCreation is what keeps the two
	// from getting out of step, and committing all of them together is what
	// makes the outcome all or nothing.
	//
	// The whole copy is one transaction, so the caller is responsible for
	// bounding it; it is a list's item count, which the product already bounds.
	CreateListWithItems(context.Context, domain.List, events.Event, []ListItemCreation) error
	GetList(context.Context, domain.WorkspaceID, domain.ListID) (domain.List, error)
	ListLists(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.ListPage, error)
	UpdateList(context.Context, domain.List, events.Event) error
	CreateListItem(context.Context, domain.ListItem, events.Event) error
	GetListItem(context.Context, domain.WorkspaceID, domain.ListID, domain.ListItemID) (domain.ListItem, error)
	ListItems(context.Context, domain.WorkspaceID, domain.ListID, domain.PageRequest, bool) (domain.ListItemPage, error)
	UpdateListItem(context.Context, domain.ListItem, events.Event) error
	// UpdateListItems commits every revision and event as one unit and rejects
	// the whole batch if any submitted revision is stale.
	UpdateListItems(context.Context, []domain.ListItem, []events.Event) error
	DeleteListItem(context.Context, domain.WorkspaceID, domain.ListID, domain.ListItemID, events.Event) error
	DeleteListItems(context.Context, domain.WorkspaceID, domain.ListID, []domain.ListItemID, events.Event) error
	SetListAccess(context.Context, domain.ListAccess, events.Event) error
	DeleteListAccess(context.Context, domain.ListAccess, events.Event) error
	// GetListAccess resolves the effective access one user has to one list.
	// Without a reader the grants written by SetListAccess were unenforceable,
	// so every workspace member could read and delete every other member's
	// list. Resolution considers, and returns the highest ranked of:
	//
	//   - list ownership, reported as an owner grant on the owning user;
	//   - a grant recorded directly for the user;
	//   - a grant recorded for a channel the user is a member of.
	//
	// The returned value names the grant that decided the outcome, so a caller
	// can report why access was allowed. It reports ErrNotFound when the list
	// does not exist, when the user is not a live member of the list's
	// workspace, and when no grant applies — absence of a grant is never an
	// empty grant that compares equal to a real one.
	GetListAccess(context.Context, domain.ListID, domain.UserID) (domain.ListAccess, error)
	CreateListDownload(context.Context, domain.ListDownload, events.Event) error
	GetListDownload(context.Context, domain.WorkspaceID, domain.ListDownloadID) (domain.ListDownload, error)
}
