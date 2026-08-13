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

// AppDeliveryAttemptRetention bounds how many app-event delivery outcomes each
// (app, surface) keeps. Both store profiles honour it so their histories, and
// the metrics computed over them, match.
const AppDeliveryAttemptRetention = 50

// The access levels a list or canvas grant can carry. They are the values the
// service layer already writes through SetListAccess and SetCanvasAccess; naming
// them here keeps the readers, the writers and the authorization decision that
// consumes them from drifting apart.

// ListItemCreation pairs a list item with the event that announces it. The two
// are carried together so a caller cannot hand CreateListWithItems a set of
// items and a set of events that do not correspond.
type ListItemCreation struct {
	Item  domain.ListItem
	Event events.Event
}

// ListItemUpdate is the same pairing for a write to items that already exist.
// UpdateListItems used to take two slices and check their lengths at run time,
// which is a check the caller can fail and the compiler cannot see.
type ListItemUpdate struct {
	Item  domain.ListItem
	Event events.Event
}

// UploadedFile pairs a completed upload ticket with the file it became, and
// PostedMessage pairs a message with the event announcing it. Completing an
// external upload used to take six slices, two of which had to correspond
// pairwise and were checked for it at run time.
type UploadedFile struct {
	Completion domain.ExternalUploadCompletion
	File       domain.File
}

type PostedMessage struct {
	Message domain.Message
	Event   events.Event
}

// ReadCursorUpdate pairs a read cursor with the event announcing it, for the
// same reason: marking several conversations read is one call, and a cursor
// without its event or an event without its cursor is not a state the caller
// should be able to describe.
type ReadCursorUpdate struct {
	Cursor domain.ReadCursor
	Event  events.Event
}

// FileUnshare pairs a file carried by a message with the event to journal if
// deleting that message retracts the file's last share into the conversation.
// The event is a candidate: only the store knows whether another live message
// still holds the share open. See Store.DeleteMessage.
type FileUnshare struct {
	FileID domain.FileID
	Event  events.Event
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
func BetterAccessGrant(entityType domain.GrantEntity, entityID string, level domain.AccessLevel, bestType domain.GrantEntity, bestID string, bestLevel domain.AccessLevel) bool {
	rank, bestRank := level.Rank(), bestLevel.Rank()
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

func accessEntityRank(entityType domain.GrantEntity) int {
	if entityType == domain.GrantUser {
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
	// InviteToHuddle journals a huddle invitation and lands it in the invitee's
	// Activity in one write, so the notification and the durable record agree.
	InviteToHuddle(context.Context, events.Event) error
	RecordAccess(context.Context, domain.AccessLog) error
	ListAccessLogs(context.Context, domain.WorkspaceID, time.Time, int, int) ([]domain.AccessLog, bool, error)
	LookupToken(context.Context, string) (domain.TokenRecord, error)
	LookupAppToken(context.Context, string) (domain.AppTokenRecord, error)
	CreateAppToken(context.Context, string, domain.AppTokenRecord) error
	// RevokeAppTokens marks every token issued for an app revoked. Lookup already
	// refuses a revoked token, so this is the write the enforcement was waiting
	// for. It is idempotent: revoking again, or an app that never issued one,
	// is not an error.
	RevokeAppTokens(context.Context, domain.AppID) error
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
	// ListUserSessions reports one member's live sessions for an administrator
	// to review. It answers with the stored hash as each session's identifier
	// and never the token: telling two sessions apart and ending one need no
	// credential, and a list that handed out tokens would turn "review who is
	// signed in" into a way to become them.
	ListUserSessions(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.WorkspaceSession, error)
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
	// TouchUserActivity records that a member was seen, which is what makes
	// automatic presence automatic. It journals nothing: a heartbeat is derived
	// state, not something a consumer needs delivered.
	TouchUserActivity(context.Context, domain.WorkspaceID, domain.UserID, time.Time) error
	SetUserExpiration(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) error
	// SetRoleAssignments gives members a system role over entities. The store
	// writes one row for each member and entity pair.
	SetRoleAssignments(context.Context, []domain.RoleAssignment, events.Event) error
	// DeleteRoleAssignments removes those rows.
	DeleteRoleAssignments(context.Context, []domain.RoleAssignment, events.Event) error
	// ListRoleAssignments reports the members who hold one role, in a stable
	// order so two reads agree.
	ListRoleAssignments(context.Context, domain.WorkspaceID, string, domain.PageRequest) (domain.RoleAssignmentPage, error)
	// SetAuthPolicyEntities puts entities under one authentication policy. The
	// same entity twice adds no row.
	// SetAppIcon records what a client draws beside an app's messages.
	SetAppIcon(context.Context, domain.WorkspaceID, domain.AppID, string, events.Event) error
	// GetExternalAuthToken reads one of an app's external credentials.
	GetExternalAuthToken(context.Context, domain.WorkspaceID, domain.AppID, string) (domain.ExternalAuthToken, error)
	// SetExternalAuthToken stores one.
	SetExternalAuthToken(context.Context, domain.ExternalAuthToken, events.Event) error
	// DeleteExternalAuthToken removes one, or every one the app holds when the
	// identifier is empty.
	DeleteExternalAuthToken(context.Context, domain.WorkspaceID, domain.AppID, string, events.Event) error
	// GetAnomalyAllowList reports what audit is told not to flag. A workspace
	// that has set nothing answers an empty list rather than not found: an
	// empty allow list is the state a workspace starts in.
	GetAnomalyAllowList(context.Context, domain.WorkspaceID) (domain.AnomalyAllowList, error)
	// SetAnomalyAllowList replaces it.
	SetAnomalyAllowList(context.Context, domain.AnomalyAllowList, events.Event) error
	// AnalyticsRows reports one day of analytics, in entity-identifier order.
	AnalyticsRows(context.Context, domain.WorkspaceID, domain.AnalyticsKind, time.Time) ([]domain.AnalyticsRow, error)
	// RecordAppActivity appends one entry to an app's activity log.
	RecordAppActivity(context.Context, domain.AppActivity) error
	// ListAppActivities reports the entries that match a filter, newest last.
	ListAppActivities(context.Context, domain.WorkspaceID, domain.AppActivityFilter, domain.PageRequest) (domain.AppActivityPage, error)
	// SetConversationsExcludedFromAI marks channels in or out of the
	// workspace's generative features. Exclusion is its own row rather than a
	// column on conversations: every conversation read names its columns
	// explicitly in about twenty places, and a fact only the administrative
	// surface reads does not belong in all of them.
	SetConversationsExcludedFromAI(context.Context, domain.WorkspaceID, []domain.ConversationID, bool, events.Event) error
	// ConversationsExcludedFromAI reports which of the named channels are out.
	ConversationsExcludedFromAI(context.Context, domain.WorkspaceID, []domain.ConversationID) ([]domain.ConversationID, error)
	// MoveConversations reassigns channels to another workspace.
	MoveConversations(context.Context, domain.WorkspaceID, []domain.ConversationID, domain.WorkspaceID, events.Event) error
	// LookupConversations reports the channels that match an administrative
	// search, in identifier order.
	LookupConversations(context.Context, domain.WorkspaceID, domain.ConversationLookup, domain.PageRequest) (domain.ConversationPage, error)
	// LinkConversationObjects links channels to external records.
	LinkConversationObjects(context.Context, []domain.LinkedObject, events.Event) error
	// UnlinkConversationObjects removes every link the named channels hold.
	UnlinkConversationObjects(context.Context, domain.WorkspaceID, []domain.ConversationID, events.Event) error
	// ListConversationObjects reports the records one channel is linked to.
	ListConversationObjects(context.Context, domain.WorkspaceID, domain.ConversationID) ([]domain.LinkedObject, error)
	// SetAppConfig writes one app's administrative configuration, replacing
	// what was there.
	SetAppConfig(context.Context, domain.AppConfig, events.Event) error
	// ListAppConfigs reports the configuration of the named apps. An app with
	// none is absent rather than present with empty lists.
	ListAppConfigs(context.Context, domain.WorkspaceID, []domain.AppID) ([]domain.AppConfig, error)
	// ClearAppApproval removes an app's approval decision, so the app is
	// undecided again rather than approved or restricted.
	ClearAppApproval(context.Context, domain.WorkspaceID, domain.AppID, events.Event) error
	// CreateBarrier stores a new information barrier.
	CreateBarrier(context.Context, domain.InformationBarrier, events.Event) error
	// UpdateBarrier replaces the groups and subjects one barrier holds.
	UpdateBarrier(context.Context, domain.InformationBarrier, events.Event) error
	// DeleteBarrier removes one barrier.
	DeleteBarrier(context.Context, domain.WorkspaceID, domain.BarrierID, events.Event) error
	// ListBarriers reports the workspace's barriers, newest identifier last.
	ListBarriers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.InformationBarrierPage, error)
	// SetSessionSettings writes one member's session settings, replacing what
	// was there. A zero value clears them back to the workspace default.
	SetSessionSettings(context.Context, []domain.SessionSettings, events.Event) error
	// ClearSessionSettings drops the rows, so the members fall back to the
	// workspace default.
	ClearSessionSettings(context.Context, domain.WorkspaceID, []domain.UserID, events.Event) error
	// ListSessionSettings reports the settings the named members hold. A member
	// with none is absent from the result rather than present with zeros.
	ListSessionSettings(context.Context, domain.WorkspaceID, []domain.UserID) ([]domain.SessionSettings, error)
	SetAuthPolicyEntities(context.Context, []domain.AuthPolicyEntity, events.Event) error
	// DeleteAuthPolicyEntities takes them back out.
	DeleteAuthPolicyEntities(context.Context, []domain.AuthPolicyEntity, events.Event) error
	// ListAuthPolicyEntities reports the entities one policy holds, in a stable
	// order, and how many there are in total.
	ListAuthPolicyEntities(context.Context, domain.WorkspaceID, domain.AuthPolicyName, domain.PolicyEntityType, domain.PageRequest) (domain.AuthPolicyEntityPage, error)
	// GetUserExpiration reports when a guest account lapses. A zero time means
	// the account does not lapse.
	GetUserExpiration(context.Context, domain.WorkspaceID, domain.UserID) (time.Time, error)
	// DueUserExpirations reports the accounts whose expiration has arrived and
	// which are still active. A deleted account is not due: expiry is a way of
	// deactivating an account, so one already deactivated has nothing left to
	// do and must not be swept again.
	DueUserExpirations(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.User, error)
	// ExpireUserAccount deactivates one lapsed account, reporting whether this
	// caller is the one that did it. Like the other expiry queues in this port
	// there is no lease: two workers may read the same due account and the
	// compare-and-set on the expiration instant lets exactly one act, so the
	// deactivation event is appended once.
	ExpireUserAccount(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) (bool, error)
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
	RenameConversation(context.Context, domain.ConversationID, string, events.Event, ...domain.Message) (domain.Conversation, error)
	SetConversationTopic(context.Context, domain.ConversationID, string, events.Event, ...domain.Message) (domain.Conversation, error)
	SetConversationPurpose(context.Context, domain.ConversationID, string, events.Event, ...domain.Message) (domain.Conversation, error)
	SetConversationArchived(context.Context, domain.ConversationID, bool, events.Event) (domain.Conversation, error)
	DeleteConversation(context.Context, domain.WorkspaceID, domain.ConversationID, events.Event) error
	SetConversationAccessGroups(context.Context, domain.WorkspaceID, domain.ConversationID, []domain.UserGroupID, events.Event) error
	ListConversationAccessGroups(context.Context, domain.WorkspaceID, domain.ConversationID) ([]domain.UserGroupID, error)
	CreateInviteRequest(context.Context, domain.InviteRequest, events.Event) error
	GetInviteRequest(context.Context, domain.WorkspaceID, domain.InviteRequestID) (domain.InviteRequest, error)
	// SetInviteRequestStatus is a compare-and-set: the caller names the status
	// it read and the status it wants. The previous status used to be an
	// implicit "pending", which is why an approved invitation could not be
	// withdrawn — the update matched no row and reported not found.
	SetInviteRequestStatus(context.Context, domain.WorkspaceID, domain.InviteRequestID, domain.InviteRequestStatus, domain.InviteRequestStatus, time.Time, events.Event) error
	ListInviteRequests(context.Context, domain.WorkspaceID, domain.InviteRequestStatus, domain.PageRequest) (domain.InviteRequestPage, error)
	// ListWorkspacesForEmail returns every workspace in which this address is
	// an active, undeleted member, ordered by workspace identifier so two
	// calls and two profiles list them alike. The address is the join: a user
	// row belongs to one workspace, so the same person elsewhere is a
	// different row with the same address.
	ListWorkspacesForEmail(context.Context, string) ([]domain.WorkspaceMembershipSummary, error)
	// WorkspaceAnalytics counts what one workspace holds, and what has
	// happened in it since a caller-supplied instant. The instant is a
	// parameter so the page and any export built from the same call describe
	// the same window.
	WorkspaceAnalytics(context.Context, domain.WorkspaceID, time.Time, int) (domain.WorkspaceAnalytics, error)
	// FindInviteRequestByEmail returns the one invitation for an address in a
	// given state, or ErrNotFound. The address is the whole match: acceptance
	// is decided against an email a provider has verified, so an invitation
	// nobody has that address cannot be redeemed.
	FindInviteRequestByEmail(context.Context, domain.WorkspaceID, string, domain.InviteRequestStatus) (domain.InviteRequest, error)
	// AcceptInviteRequest turns an approved invitation into the member it
	// promised, in one transaction: the invitation becomes accepted, the user
	// and the workspace membership are created at the recorded guest tier, and
	// every channel the invitation named is joined.
	//
	// Approval used to flip a status and do nothing else, so an accepted
	// invitation produced no user, no membership and no channel: the whole
	// promise was inert. Splitting the work across calls would let a crash
	// leave a member with none of the channels they were invited to, or an
	// invitation consumed with no member behind it.
	AcceptInviteRequest(context.Context, domain.InviteRequestAcceptance, []events.Event) error
	SetAppApproval(context.Context, domain.WorkspaceID, domain.AppID, domain.AppRequestID, domain.AppApprovalStatus, time.Time, events.Event) error
	// GetAppApproval reads one app's approval decision. A transition rule needs
	// the current state, and listing by status to find one app answers a page
	// when the question is about a single row.
	GetAppApproval(context.Context, domain.WorkspaceID, domain.AppID) (domain.AppApproval, error)
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
	UninstallApp(context.Context, domain.WorkspaceID, domain.AppID, ...events.Event) error
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
	// ListWorkflowRunSteps returns one run's step executions in creation
	// order.
	ListWorkflowRunSteps(context.Context, domain.WorkspaceID, domain.WorkflowRunID) ([]domain.WorkflowStep, error)
	CreateWorkflow(context.Context, domain.WorkflowDefinition, events.Event) error
	// UpdateWorkflow writes the definition and expects the stored row to hold
	// Version-1. The caller sets the new version on the value it passes, so one
	// field carries the version instead of a field and an argument that can
	// contradict each other.
	UpdateWorkflow(context.Context, domain.WorkflowDefinition, events.Event) error
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
	// SetWorkflowManagers replaces a workflow's manager list independently of
	// its versioned content.
	SetWorkflowManagers(context.Context, domain.WorkspaceID, domain.WorkflowID, []domain.UserID, events.Event) error
	// SetAppBotToken stores an app's bot access token as sealed ciphertext so a
	// function_executed dispatch can include it, exactly as Slack sends
	// bot_access_token to the app.
	SetAppBotToken(context.Context, domain.AppID, domain.WorkspaceID, string, ...events.Event) error
	// GetAppBotTokenCiphertext returns the sealed bot access token for an
	// installed app, or ErrNotFound when the app has not issued one.
	GetAppBotTokenCiphertext(context.Context, domain.AppID, domain.WorkspaceID) (string, error)
	ListWorkflows(context.Context, domain.WorkspaceID, domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error)
	// SetWorkflowStatus takes a workflow in or out of service without touching
	// what it says. It is deliberately not an edit: an administrator stopping a
	// workflow is not authoring it, the content must not change under its
	// owner, and the version must not move — runs pin the published version,
	// and bumping it would make a stop look like a revision nobody wrote.
	SetWorkflowStatus(context.Context, domain.WorkspaceID, domain.WorkflowID, domain.WorkflowStatus, time.Time, events.Event) error
	ListWorkflowRevisions(context.Context, domain.WorkspaceID, domain.WorkflowID) ([]domain.WorkflowRevision, error)
	SetWorkflowTrigger(context.Context, domain.WorkflowTrigger, events.Event) error
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
	// SummarizeWorkflowRuns reports the per-status run counts for one workflow
	// and its latest runs, newest first, bounded by the given limit.
	SummarizeWorkflowRuns(context.Context, domain.WorkspaceID, domain.WorkflowID, int) (domain.WorkflowActivity, error)
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
	// SetExternalInvitePermission records whether one connected team may invite
	// further organizations into a conversation. Absence means it may: the
	// permission is a restriction a host applies, not a grant it withholds.
	SetExternalInvitePermission(context.Context, domain.WorkspaceID, domain.ConversationID, domain.WorkspaceID, bool, events.Event) error
	// GetExternalInvitePermission reports that stored decision. A team with no
	// recorded restriction may invite.
	GetExternalInvitePermission(context.Context, domain.WorkspaceID, domain.ConversationID, domain.WorkspaceID) (bool, error)
	DisconnectConversationTeams(context.Context, domain.WorkspaceID, domain.ConversationID, []domain.WorkspaceID, events.Event) error
	// ListExternalTeams reports the organizations this workspace shares
	// channels with, derived from the channels themselves so there is one
	// answer rather than two that can disagree.
	ListExternalTeams(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.ExternalTeamPage, error)
	// DisconnectExternalTeam removes one organization from every conversation
	// of this workspace, in one transaction: a disconnection that left the
	// organization in one channel would not be a disconnection.
	DisconnectExternalTeam(context.Context, domain.WorkspaceID, domain.WorkspaceID, events.Event) error
	CreateSharedInvite(context.Context, domain.SharedInvite, events.Event) error
	GetSharedInvite(context.Context, domain.SharedInviteID) (domain.SharedInvite, error)
	// ListSharedInvites pages one workspace's invitations in a given status.
	// The workspace matches either side: the host sees what it sent, and the
	// invited organization sees what it was sent.
	ListSharedInvites(context.Context, domain.WorkspaceID, domain.SharedInviteStatus, domain.PageRequest) (domain.SharedInvitePage, error)
	// SetSharedInviteStatus is a compare-and-set over domain's transition
	// table, so no caller can move an invitation somewhere the state machine
	// does not allow, and two concurrent decisions cannot both win.
	SetSharedInviteStatus(context.Context, domain.SharedInviteID, domain.SharedInviteStatus, domain.SharedInviteStatus, time.Time, events.Event) error
	// AcceptSharedInvite appends the invited organization to the conversation
	// and settles the invitation in one transaction, refusing when the channel
	// is already at domain.SlackConnectCapacity.
	//
	// The capacity is checked here and nowhere else. CONNECT-01 forbids
	// promising a place from a stale count, and a count read before the
	// transaction is stale by definition: two organizations accepting the
	// 250th place concurrently would both be told yes.
	AcceptSharedInvite(context.Context, domain.SharedInviteID, time.Time, []events.Event) (domain.Conversation, error)
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
	// LatestEventSequence is the journal position a new reader should start
	// after: the sequence of the most recent event in the workspace, or zero
	// when the workspace has none.
	LatestEventSequence(context.Context, domain.WorkspaceID) (uint64, error)
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
	// SetConversationPublic is the reverse. It is a separate method rather than
	// a flag on the first because the two are not symmetrical in what they
	// expose: making a channel private hides what was said from people who
	// could already read it, while making it public shows what was said to
	// people who never could.
	SetConversationPublic(context.Context, domain.ConversationID, events.Event) (domain.Conversation, error)
	GetConversationPrefs(context.Context, domain.ConversationID) (domain.ConversationPrefs, error)
	SetConversationPrefs(context.Context, domain.ConversationID, domain.ConversationPrefs, events.Event) (domain.ConversationPrefs, error)
	// GetConversationRetention returns a channel's message-retention override,
	// or a zero value when it has none. An absent override is not an error:
	// most channels follow the workspace default, and making that the error
	// path would force every caller to distinguish "no override" from "could
	// not read".
	GetRetentionPolicy(context.Context, domain.WorkspaceID) (domain.RetentionPolicy, error)
	SetRetentionPolicy(context.Context, domain.WorkspaceID, domain.RetentionPolicy, events.Event) error
	GetConversationRetention(context.Context, domain.WorkspaceID, domain.ConversationID) (domain.ConversationRetention, error)
	SetConversationRetention(context.Context, domain.WorkspaceID, domain.ConversationID, int, time.Time, events.Event) error
	RemoveConversationRetention(context.Context, domain.WorkspaceID, domain.ConversationID, events.Event) error
	// ClaimRetentionSweep returns conversations in this workspace whose
	// retention has not been applied since the given instant, claiming each by
	// advancing its watermark in the same statement that selects it.
	//
	// The advance is the claim: two workers can select the same conversation,
	// only one can move its watermark, and the loser simply finds nothing.
	// This is compare-and-set rather than a lease because the work is
	// idempotent — deleting content that is already gone is a no-op — so a
	// lost race costs a wasted query and nothing else.
	ClaimRetentionSweep(context.Context, domain.WorkspaceID, time.Time, time.Time, int) ([]domain.ConversationID, error)
	// SweepRetention permanently deletes the messages and files in one
	// conversation that its effective policy has expired, together with
	// everything that references them, in one transaction.
	//
	// This is the only hard delete of message content in the product. Slack's
	// retention deletion is permanent, so a tombstone would be a lie: the row
	// goes, and the storage it occupied is actually recovered.
	//
	// A thread is retained until its newest reply expires. Slack documents
	// nothing here, and deleting a root while its replies survive would leave
	// replies with no parent to render under.
	SweepRetention(context.Context, domain.RetentionSweepRequest) (domain.RetentionSweep, error)
	// LastRetentionSweep is the most recent instant any conversation in the
	// workspace was swept, which is the signal that the worker is alive. The
	// oldest watermark would say how far behind it is, but the newest is what
	// distinguishes "running" from "stopped", and a stopped sweep is the
	// failure that silently breaks the promise the policy makes.
	LastRetentionSweep(context.Context, domain.WorkspaceID) (time.Time, error)
	// AppendRetentionEvents journals a completed sweep's announcements. See
	// scheduler.RetentionSource for why this is separate from the deletion.
	AppendRetentionEvents(context.Context, domain.WorkspaceID, []events.Event) error
	AddEmoji(context.Context, domain.CustomEmoji, events.Event) error
	ListEmojis(context.Context, domain.WorkspaceID) ([]domain.CustomEmoji, error)
	RemoveEmoji(context.Context, domain.WorkspaceID, string, events.Event) error
	RenameEmoji(context.Context, domain.WorkspaceID, string, string, events.Event) error
	// AddConversationMember and its siblings accept the notice message the
	// change posts into the conversation — Slack's channel_join,
	// channel_leave, channel_topic, channel_purpose and channel_name
	// messages. The notice commits inside the same transaction as the change
	// it describes, so a crash cannot leave a renamed channel or a new member
	// with no visible record of how that happened.
	AddConversationMember(context.Context, domain.ConversationID, domain.UserID, events.Event, ...domain.Message) error
	InviteConversationMembers(context.Context, domain.ConversationID, []domain.UserID, events.Event) error
	RemoveConversationMember(context.Context, domain.ConversationID, domain.UserID, events.Event, ...domain.Message) error
	// ThreadSummaries reports reply counts, participants and last-reply
	// instants for the named thread roots in one read. A timeline renders
	// fifty parents at a time, so this is deliberately batched: the
	// per-parent alternative is fifty queries per page.
	ThreadSummaries(context.Context, domain.ConversationID, []domain.MessageTimestamp) (map[domain.MessageTimestamp]domain.ThreadSummary, error)
	GetReadCursor(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ReadCursor, error)
	SetReadCursor(context.Context, domain.ReadCursor, events.Event) error
	// SetReadCursors advances several read cursors in one transaction, with one
	// event per cursor and in the same order. It exists because "mark everything
	// read" is one action to the member: doing it as N separate transactions
	// would leave a partially-read workspace behind any failure, and a reader
	// who retried would see the sidebar clear in pieces.
	SetReadCursors(context.Context, []ReadCursorUpdate) error
	// LatestMessageTimestamps reports the newest undeleted message in each named
	// conversation. Conversations with no messages are omitted rather than
	// reported as empty, so a caller cannot mistake "nothing to read" for "read
	// position zero".
	LatestMessageTimestamps(context.Context, domain.WorkspaceID, []domain.ConversationID) (map[domain.ConversationID]domain.MessageTimestamp, error)
	GetWorkspaceNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID) (domain.WorkspaceNotificationPreferences, error)
	SetWorkspaceNotificationPreferences(context.Context, domain.WorkspaceNotificationPreferences, events.Event) error
	GetConversationNotificationPreferences(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ConversationNotificationPreferences, error)
	SetConversationNotificationPreferences(context.Context, domain.ConversationNotificationPreferences, events.Event) error
	IsThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (bool, error)
	SetThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, bool, events.Event) error
	// ListFollowedThreads is the Threads view. It answers newest-reply-first,
	// because a threads view ordered by anything else is a list of threads the
	// member has already dealt with. A followed thread whose root has been
	// deleted is omitted rather than shown as an empty row.
	ListFollowedThreads(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FollowedThreadPage, error)
	// DueWorkflowDelays lists the delay steps whose wake time has passed. A
	// workspace of "" asks across every workspace, which is what the global
	// worker queue does.
	DueWorkflowDelays(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.WorkflowStep, error)
	// SetAssistantThread writes exactly one field of a thread's assistant
	// state. The field is explicit because each API method sets one, and a
	// whole-record write would clear whatever the caller left empty.
	SetAssistantThread(context.Context, domain.AssistantThread, domain.AssistantThreadField, events.Event) error
	GetAssistantThread(context.Context, domain.WorkspaceID, domain.ConversationID, domain.MessageTimestamp) (domain.AssistantThread, error)
	// RecordTyping replaces one member's typing signal in one conversation.
	// It takes no events.Event, and that absence is the design rather than an
	// omission: a typing signal must not enter the outbox, because the outbox
	// is durable, replayed to reconnecting clients and delivered to apps, and
	// none of those is true of "someone is typing". See domain.TypingSignal.
	RecordTyping(context.Context, domain.TypingSignal) error
	// ListTypingSignals reports who is composing, at the given instant, in the
	// conversations this reader belongs to — never the reader themselves, and
	// never a conversation they cannot see. Expired signals are omitted rather
	// than returned for the caller to filter, so no caller can forget to.
	ListTypingSignals(context.Context, domain.WorkspaceID, domain.UserID, time.Time) ([]domain.TypingSignal, error)
	// RecordListAssignment tells a member that work is theirs. It is separate
	// from the item write rather than folded into it because only some writes
	// are assignments — a due date moved on an item someone already holds is
	// not one — and the service knows which.
	RecordListAssignment(context.Context, domain.ListItem, domain.UserID, time.Time) error
	// RecordSharedInviteDecision tells the member who asked for a Slack Connect
	// invitation what was decided. Like a list assignment it is separate from
	// the mutation, because only some transitions are news to them.
	RecordSharedInviteDecision(context.Context, domain.SharedInvite, domain.UserID, time.Time) error
	ListActivity(context.Context, domain.WorkspaceID, domain.UserID, domain.ActivityQuery) (domain.ActivityPage, error)
	MutateActivity(context.Context, domain.WorkspaceID, domain.UserID, []domain.ActivityID, domain.ActivityMutation, time.Time) error
	GetActivityPreferences(context.Context, domain.WorkspaceID, domain.UserID) (domain.ActivityPreferences, error)
	SetActivityPreferences(context.Context, domain.ActivityPreferences) error
	ListUsers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.UserPage, error)
	// SearchUsers is the same directory listing narrowed by a folded name. The
	// workspace directory has no per-reader visibility rule, so this shares the
	// listing's scan rather than introducing a second one that could page
	// differently or handle deleted members differently.
	SearchUsers(context.Context, domain.WorkspaceID, string, domain.PageRequest) (domain.UserPage, error)
	ListAdminUsers(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.AdminUserPage, error)
	ListUsersByRole(context.Context, domain.WorkspaceID, domain.WorkspaceRole, domain.PageRequest) (domain.UserPage, error)
	ListConversationMembers(context.Context, domain.ConversationID, domain.PageRequest) (domain.UserPage, error)
	// CountConversationMembers reports how many members ListConversationMembers
	// would page through: people in the conversation whose accounts are not
	// deleted. One number, so the header does not page an entire channel to
	// print it.
	CountConversationMembers(context.Context, domain.ConversationID) (int, error)
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
	// ListAppDeliveryAttempts returns the retained delivery outcomes for an app's
	// surface, newest first. It is the history behind AppDeliveryHealth, bounded to
	// AppDeliveryAttemptRetention per surface so both profiles keep the same window.
	ListAppDeliveryAttempts(context.Context, domain.AppID, string, int) ([]domain.AppDeliveryAttempt, error)
	ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error)
	ClaimEventsForTopic(context.Context, domain.WorkspaceID, string, string, int, time.Duration) ([]events.Record, error)
	RenewEvents(context.Context, string, []uint64, time.Duration) error
	AckEvents(context.Context, string, []uint64) error
	ReleaseEvents(context.Context, string, []uint64, time.Time) error
	GetMessageByCreatedAt(context.Context, domain.ConversationID, time.Time) (domain.Message, error)
	GetIdempotentMessage(context.Context, domain.WorkspaceID, domain.UserID, string) (domain.Message, error)
	UpdateMessage(context.Context, domain.Message, events.Event) error
	// DeleteMessage marks one message deleted and retracts the file shares that
	// message was carrying. A file is visible to whoever can see a conversation
	// it is shared into, and the share is a row of its own: without this, the
	// only message that ever shared a file into a channel could be deleted and
	// the file stayed readable there, and files.list kept listing it, forever.
	//
	// A share survives while any other live message in the same conversation
	// still carries the file, so the store — not the caller — decides which
	// shares end. The caller supplies one candidate event per file the message
	// carries; the store journals exactly those whose share it removed, in the
	// order given, inside the same transaction as the deletion.
	DeleteMessage(context.Context, domain.Message, events.Event, []FileUnshare) error
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
	// DueReminders reports the reminders that have come due and have not been
	// delivered. Delivery is a compare-and-set on delivered_at, so two workers
	// reading the same batch deliver each reminder once between them.
	DueReminders(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.Reminder, error)
	// MarkReminderDelivered claims one reminder and writes its notice in the
	// same transaction. It reports false when the claim is lost, which covers
	// both somebody else winning it and the reminder not being there: the
	// worker only claims what it has just read as due, and either way it must
	// not deliver.
	MarkReminderDelivered(context.Context, domain.WorkspaceID, domain.ReminderID, time.Time, events.Event) (bool, error)
	// EarliestReminder is the next instant a reminder comes due, so a workspace
	// that is asleep knows when to wake.
	EarliestReminder(context.Context, domain.WorkspaceID) (time.Time, error)
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
	// StartHuddle returns the conversation's active huddle, creating it only if
	// there is none, and adds the caller to it either way. It is one atomic
	// upsert because two people pressing start at the same moment must end up
	// in the same huddle: a read-then-create would give them one each, and the
	// second would silently be the one nobody else joined.
	//
	// The returned bool reports whether this call created the huddle, so the
	// caller knows whether to announce a start or a join.
	StartHuddle(context.Context, domain.Call, events.Event, events.Event) (domain.Call, bool, error)
	// ActiveHuddle returns the conversation's running huddle, or ErrNotFound.
	ActiveHuddle(context.Context, domain.WorkspaceID, domain.ConversationID) (domain.Call, error)
	// JoinCall and LeaveCall move one participant, rather than replacing the
	// whole set as SetCallParticipants does. Two people joining concurrently
	// through a whole-set write lose one of the two additions.
	//
	// LeaveCall ends the call when the last participant leaves: a huddle with
	// nobody in it is over, and leaving it running would let the conversation
	// show a huddle nobody can be in.
	JoinCall(context.Context, domain.WorkspaceID, domain.CallID, domain.UserID, events.Event) (domain.Call, error)
	LeaveCall(context.Context, domain.WorkspaceID, domain.CallID, domain.UserID, events.Event, events.Event) (domain.Call, error)
	CreateFile(context.Context, domain.File, events.Event) error
	CreateExternalUpload(context.Context, domain.ExternalUpload) error
	GetExternalUpload(context.Context, domain.ExternalUploadID) (domain.ExternalUpload, error)
	PendingUploadReferenceExists(context.Context, domain.WorkspaceID, domain.UserID, domain.ExternalUploadID) (bool, error)
	MarkExternalUploadUploaded(context.Context, domain.ExternalUploadID, time.Time) error
	CompleteExternalUpload(context.Context, domain.ExternalUploadID, domain.File, []domain.ConversationID, events.Event) error
	CompleteExternalUploads(context.Context, []UploadedFile, []domain.ConversationID, []events.Event, []PostedMessage) error
	CompleteScheduledExternalUploads(context.Context, domain.ScheduledMessageID, []UploadedFile, []domain.ConversationID, []events.Event, PostedMessage) error
	CreateFileShareMessage(context.Context, []domain.FileID, domain.Message, []events.Event) error
	GetFile(context.Context, domain.FileID) (domain.File, error)
	DeleteFile(context.Context, domain.FileID, events.Event) error
	// SetFileDescription records what an image is, in words, for a reader who
	// cannot see it. The uploader is part of the write rather than checked
	// before it, so the permission cannot be lost between the check and the
	// update.
	SetFileDescription(context.Context, domain.WorkspaceID, domain.FileID, domain.UserID, string, events.Event) error
	DeleteFileComment(context.Context, domain.WorkspaceID, domain.FileID, domain.FileCommentID, events.Event) error
	ShareFilePublic(context.Context, domain.WorkspaceID, domain.FileID, string, events.Event) error
	RevokeFilePublic(context.Context, domain.WorkspaceID, domain.FileID, events.Event) error
	GetPublicFile(context.Context, string) (domain.File, error)
	ListFiles(context.Context, domain.WorkspaceID, domain.PageRequest) (domain.FilePage, error)
	ListVisibleFiles(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FilePage, error)
	SearchFiles(context.Context, domain.WorkspaceID, domain.UserID, domain.FileSearch) (domain.FilePage, error)
	// SearchCanvases answers Slack's Canvases search tab. It applies exactly
	// the visibility rule ListCanvases applies, because a search that matched
	// more would disclose the title of a canvas the reader cannot open.
	SearchCanvases(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasSearch) (domain.CanvasPage, error)
	// ListCanvasRevisions reads what a canvas said before. A revision is
	// readable exactly when the canvas is, so this asks the canvas's own
	// visibility rather than carrying a second rule that could drift.
	ListCanvasRevisions(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID, domain.PageRequest) (domain.CanvasRevisionPage, error)
	// Canvas comments are readable and writable by anyone who may read the
	// canvas: commenting is taking part in a document, not editing it.
	CreateCanvasComment(context.Context, domain.CanvasComment, events.Event) error
	DeleteCanvasComment(context.Context, domain.WorkspaceID, domain.CanvasCommentID, domain.UserID, events.Event) error
	ListCanvasComments(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID, domain.PageRequest) (domain.CanvasCommentPage, error)
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
	// ListCanvasGrants reports every grant on one canvas, which is what a
	// sharing surface shows. It answers who a canvas is shared with; whether
	// the asking member may see that is the service's question, because it is
	// the same question as whether they may open the canvas at all.
	ListCanvasGrants(context.Context, domain.WorkspaceID, domain.CanvasID) ([]domain.CanvasAccess, error)
	// ListListGrants is the same question about a list. Lists and canvases
	// carry the same grant model, so the sharing surface is the same surface
	// and both need the same read behind it.
	ListListGrants(context.Context, domain.WorkspaceID, domain.ListID) ([]domain.ListAccess, error)
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
	SearchLists(context.Context, domain.WorkspaceID, domain.UserID, domain.ListSearch) (domain.ListPage, error)
	UpdateList(context.Context, domain.List, events.Event) error
	// RemoveListColumn drops one column from a list and the cells under it, in
	// one transaction. A schema that no longer declares a column while items
	// still carry values under it is a list nobody can read correctly: the
	// values are invisible, every later edit carries them, and a new column
	// minting the same key would bring them back to life. The list is passed
	// with its schema already rewritten, which is the same shape UpdateList
	// takes, and its version guards the write.
	RemoveListColumn(context.Context, domain.List, string, events.Event) error
	CreateListItem(context.Context, domain.ListItem, events.Event) error
	GetListItem(context.Context, domain.WorkspaceID, domain.ListID, domain.ListItemID) (domain.ListItem, error)
	ListItems(context.Context, domain.WorkspaceID, domain.ListID, domain.PageRequest, bool) (domain.ListItemPage, error)
	UpdateListItem(context.Context, domain.ListItem, events.Event) error
	// UpdateListItems commits every revision and event as one unit and rejects
	// the whole batch if any submitted revision is stale.
	UpdateListItems(context.Context, []ListItemUpdate) error
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
