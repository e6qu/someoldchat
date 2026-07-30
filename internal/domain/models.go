package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func DirectConversationKey(workspaceID WorkspaceID, members []UserID) string {
	values := make([]string, 0, len(members))
	for _, member := range members {
		values = append(values, string(member))
	}
	sort.Strings(values)
	return string(workspaceID) + "\x00" + strings.Join(values, "\x00")
}

type Workspace struct {
	ID                WorkspaceID
	Domain            string
	Name              string
	Description       string
	Discoverability   WorkspaceDiscoverability
	IconURL           string
	DefaultChannelIDs []ConversationID
}

type WorkspaceDiscoverability string

const (
	WorkspaceDiscoverabilityOpen       WorkspaceDiscoverability = "open"
	WorkspaceDiscoverabilityInviteOnly WorkspaceDiscoverability = "invite_only"
	WorkspaceDiscoverabilityClosed     WorkspaceDiscoverability = "closed"
	WorkspaceDiscoverabilityUnlisted   WorkspaceDiscoverability = "unlisted"
)

func (value WorkspaceDiscoverability) Valid() bool {
	switch value {
	case WorkspaceDiscoverabilityOpen, WorkspaceDiscoverabilityInviteOnly, WorkspaceDiscoverabilityClosed, WorkspaceDiscoverabilityUnlisted:
		return true
	default:
		return false
	}
}

type WorkspaceRole string

const (
	WorkspaceRoleMember WorkspaceRole = "member"
	WorkspaceRoleAdmin  WorkspaceRole = "admin"
	WorkspaceRoleOwner  WorkspaceRole = "owner"
)

// Rank orders the workspace roles so authority can be compared rather than
// enumerated. An unrecognised role ranks below every real one, so a corrupt or
// future value can never be read as authority.
//
// The three roles were previously compared by equality at each call site, which
// made Admin and Owner interchangeable everywhere: an administrator could grant
// themselves Owner and demote the real owner, because "is an administrator" was
// the only question anyone asked.
func (r WorkspaceRole) Rank() int {
	switch r {
	case WorkspaceRoleOwner:
		return 3
	case WorkspaceRoleAdmin:
		return 2
	case WorkspaceRoleMember:
		return 1
	default:
		return 0
	}
}

// Outranks reports whether r holds strictly more authority than other.
func (r WorkspaceRole) Outranks(other WorkspaceRole) bool { return r.Rank() > other.Rank() }

type WorkspaceMembership struct {
	WorkspaceID     WorkspaceID
	UserID          UserID
	Role            WorkspaceRole
	Active          bool
	Restricted      bool
	UltraRestricted bool
}

// Guest reports Slack's two guest membership tiers. Restricted is a
// multi-channel guest and UltraRestricted is a single-channel guest.
func (membership WorkspaceMembership) Guest() bool {
	return membership.Restricted || membership.UltraRestricted
}

type BillableUser struct {
	UserID        UserID
	BillingActive bool
}

type BillableInfo struct {
	Users []BillableUser
}

type UserProfile struct {
	DisplayName string
	StatusText  string
	StatusEmoji string
	Image24     string
	Image32     string
	Image48     string
	Image72     string
	Image192    string
	Image512    string
	Image1024   string
}

type User struct {
	ID          UserID
	WorkspaceID WorkspaceID
	Email       string
	Name        string
	RealName    string
	Profile     UserProfile
	Presence    Presence
	Deleted     bool
}

type AdminUser struct {
	User       User
	Membership WorkspaceMembership
}

type AdminUserPage struct {
	Users      []AdminUser
	NextCursor Cursor
	HasMore    bool
}

type CustomEmoji struct {
	WorkspaceID WorkspaceID
	Name        string
	URL         string
	AliasFor    string
}

type Presence string

const (
	PresenceAuto Presence = "auto"
	PresenceAway Presence = "away"
)

func (p Presence) Current() string {
	if p == PresenceAway {
		return "away"
	}
	return "active"
}

type DoNotDisturb struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	Enabled     bool
	SnoozeUntil time.Time
	NextStartAt time.Time
	NextEndAt   time.Time
}

type Call struct {
	ID                CallID
	WorkspaceID       WorkspaceID
	ExternalUniqueID  string
	ExternalDisplayID string
	JoinURL           string
	DesktopAppJoinURL string
	Title             string
	CreatedBy         UserID
	Participants      []UserID
	StartedAt         time.Time
	EndedAt           time.Time
	DurationSeconds   int64
}

// View stores the validated Slack view envelope without imposing a closed
// schema on Block Kit, whose fields are intentionally extensible.
type View struct {
	ID             ViewID
	AppID          AppID
	WorkspaceID    WorkspaceID
	UserID         UserID
	Type           string
	ExternalID     string
	Payload        string
	State          string
	Errors         map[string]string
	Hash           string
	RootViewID     ViewID
	PreviousViewID ViewID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ViewInteractionResult struct {
	Errors  map[string]string
	Pending bool
}

type WorkflowStepStatus string

const (
	WorkflowStepConfigured WorkflowStepStatus = "configured"
	WorkflowStepCompleted  WorkflowStepStatus = "completed"
	WorkflowStepFailed     WorkflowStepStatus = "failed"
)

type WorkflowStep struct {
	ID          WorkflowStepID
	WorkspaceID WorkspaceID
	UserID      UserID
	EditID      string
	Status      WorkflowStepStatus
	Inputs      string
	Outputs     string
	Error       string
	StepName    string
	ImageURL    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Dialog struct {
	ID          DialogID
	WorkspaceID WorkspaceID
	UserID      UserID
	Payload     string
	CreatedAt   time.Time
}

type Bot struct {
	ID          BotID
	WorkspaceID WorkspaceID
	AppID       AppID
	UserID      UserID
	Name        string
	Image36     string
	Image48     string
	Image72     string
	Deleted     bool
	UpdatedAt   time.Time
}

type UserMigration struct {
	WorkspaceID WorkspaceID
	OldID       UserID
	GlobalID    UserID
}

type MigrationExchange struct {
	WorkspaceID    WorkspaceID
	UserIDMap      map[UserID]UserID
	InvalidUserIDs []UserID
}

type ConnectedChannelInfo struct {
	ChannelID                  ConversationID
	InternalTeamIDs            []WorkspaceID
	OriginalConnectedChannelID ConversationID
	OriginalConnectedHostID    WorkspaceID
}

type OAuthClient struct {
	ID         string
	SecretHash string
	AppID      AppID
}

type AuthMethod struct {
	WorkspaceID WorkspaceID
	Provider    string
	Enabled     bool
}

type ExternalIdentity struct {
	WorkspaceID WorkspaceID
	Provider    string
	Subject     string
	UserID      UserID
}

type OAuthCode struct {
	Code                string
	ClientID            string
	WorkspaceID         WorkspaceID
	UserID              UserID
	Scopes              []string
	BotID               BotID
	BotUserID           UserID
	BotScopes           []string
	UserScopes          []string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
}

type OAuthToken struct {
	AccessToken            string
	ClientID               string
	AppID                  AppID
	WorkspaceID            WorkspaceID
	UserID                 UserID
	InstallerID            UserID
	BotID                  BotID
	Scopes                 []string
	TokenType              string
	RefreshToken           string
	ExpiresAt              time.Time
	AuthedUserAccessToken  string
	AuthedUserScopes       []string
	AuthedUserRefreshToken string
	AuthedUserExpiresAt    time.Time
	CodeVerifier           string
}

// OAuthRefreshGrant is the durable, one-time capability behind a rotating
// OAuth access token. TokenHash and AccessTokenHash are digests; repositories
// never persist either bearer credential in plaintext.
type OAuthRefreshGrant struct {
	TokenHash       string
	AccessTokenHash string
	ClientID        string
	AppID           AppID
	WorkspaceID     WorkspaceID
	UserID          UserID
	InstallerID     UserID
	BotID           BotID
	Scopes          []string
	TokenType       string
	AccessExpiresAt time.Time
	CreatedAt       time.Time
	Revoked         bool
}

type OAuthAuthorizationRequest struct {
	ClientID            string
	WorkspaceID         WorkspaceID
	UserID              UserID
	RedirectURI         string
	BotScopes           []string
	UserScopes          []string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

type OAuthAuthorization struct {
	AppID               AppID
	AppName             string
	ClientID            string
	WorkspaceID         WorkspaceID
	UserID              UserID
	RedirectURI         string
	BotScopes           []string
	UserScopes          []string
	State               string
	Code                string
	BotID               BotID
	BotUserID           UserID
	CodeChallenge       string
	CodeChallengeMethod string
}

type OpenIDToken struct {
	OAuthToken
	IDToken      string
	RefreshToken string
}

type OpenIDRefreshToken struct {
	TokenHash   string
	ClientID    string
	WorkspaceID WorkspaceID
	UserID      UserID
	Scopes      []string
	ExpiresAt   time.Time
}

type OpenIDUserInfo struct {
	Subject           UserID
	UserID            UserID
	WorkspaceID       WorkspaceID
	Email             string
	EmailVerified     bool
	DateEmailVerified int64
	Name              string
	GivenName         string
	FamilyName        string
	Locale            string
	Picture           string
	TeamName          string
	TeamDomain        string
	UserImages        map[string]string
	TeamImages        map[string]string
	TeamImageDefault  bool
}

func (d DoNotDisturb) SnoozeEnabled(now time.Time) bool {
	return d.SnoozeUntil.After(now)
}

func (d DoNotDisturb) SnoozeRemaining(now time.Time) int64 {
	if !d.SnoozeEnabled(now) {
		return 0
	}
	return int64(d.SnoozeUntil.Sub(now).Seconds())
}

type TokenRecord struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	AppID       AppID
	BotID       BotID
	Scopes      []string
	TokenType   string
	ExpiresAt   time.Time
	Revoked     bool
}

type AppTokenRecord struct {
	AppID   AppID
	Scopes  []string
	Revoked bool
}

type AppTokenCredentials struct {
	Token  string
	AppID  AppID
	Scopes []string
}

type SessionRecord struct {
	WorkspaceID  WorkspaceID
	UserID       UserID
	Scopes       []string
	ExpiresAt    time.Time
	Revoked      bool
	OIDCProvider string
	OIDCIDToken  string
	OIDCSubject  string
	OIDCSID      string
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func VerifyPKCE(codeChallenge, method, verifier string) bool {
	codeChallenge = strings.TrimSpace(codeChallenge)
	method = strings.TrimSpace(method)
	verifier = strings.TrimSpace(verifier)
	if codeChallenge == "" {
		return verifier == ""
	}
	if method != "S256" || verifier == "" {
		return false
	}
	hash := sha256.Sum256([]byte(verifier))
	// The challenge arrives from the authorization request and the verifier from
	// the token request, so this comparison runs against attacker-supplied input
	// on every exchange. A data-dependent early exit would report how many
	// leading characters of a guessed verifier hashed correctly.
	return hmac.Equal([]byte(base64.RawURLEncoding.EncodeToString(hash[:])), []byte(codeChallenge))
}

// NormalizeEmail produces the single canonical form of a workspace e-mail
// address. Case-insensitive identity must not be delegated to the database:
// SQLite's built-in lower() folds ASCII only, PostgreSQL's lower() is
// locale-aware, and the in-memory repository used strings.EqualFold, so
// "Ä@x.test" and "ä@x.test" were one identity on PostgreSQL and in memory but
// two distinct users on SQLite and dqlite — a workspace-scoped identity
// collision reachable from sign-in. Normalizing in Go before the value reaches
// any repository makes every profile agree by construction, and lets the
// uniqueness index be a plain column index instead of a per-engine expression.
//
// The fold is strings.ToLower and deliberately NOT strings.EqualFold. Full
// Unicode case folding maps distinct letters onto one another — U+017F LATIN
// SMALL LETTER LONG S folds onto 's', so EqualFold("ſmith@x.test",
// "smith@x.test") is true — which turns a lookup by an attacker-controlled
// address into a lookup of somebody else's account. ToLower leaves U+017F
// alone, so the two addresses stay two identities. Every comparison of a
// workspace e-mail address, on every profile, must go through this function:
// the in-memory repository compared with EqualFold and the SQL repositories
// with this canonical form, so the same address resolved to different users
// depending on the configured storage profile.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// FoldSearchText produces the single canonical form a case-insensitive search
// compares, for both the stored value and the query term.
//
// Case-insensitive MATCHING must not be delegated to the database either, and
// for exactly the reason case-insensitive IDENTITY must not be: SQLite's and
// dqlite's lower() folds ASCII only, PostgreSQL's is locale-aware, and the
// in-memory repository folds with Go. The search paths folded the TERM in Go and
// the COLUMN in SQL, so the two disagreed the moment a character was not ASCII:
// a message containing "ÄPFEL" was found by SearchMessages("äpfel") and by
// SearchMessages("ÄPFEL") in memory and on PostgreSQL, and by neither on SQLite
// or dqlite. The same held for conversations.name, .topic and .purpose. A
// workspace could not find its own data, and which data it could not find
// depended on the configured storage profile.
//
// The repair is a stored, Go-folded copy of each searchable value — see the
// *_folded columns in the SQL schema — maintained by every write path and
// backfilled for rows written before it existed. Both sides of every comparison
// then come out of this one function, so every profile agrees by construction
// and the predicate stays a plain indexable column comparison rather than a
// per-engine expression.
//
// The fold is strings.ToLower and deliberately NOT strings.EqualFold's full
// Unicode case folding, for consistency with NormalizeEmail: full folding maps
// distinct letters onto one another (U+017F LATIN SMALL LETTER LONG S folds onto
// 's'), and one fold for the whole product is worth more here than the handful
// of extra matches the wider one would find.
func FoldSearchText(value string) string {
	return strings.ToLower(value)
}

// UserPhotoBlobKey decodes the blob key behind a stored profile photo URL, and
// reports whether the URL is one this deployment minted.
//
// users.profile.set accepts image_24 as free text, so a member can store an
// ordinary external avatar URL there. Both repositories used to treat that as a
// corrupt database and fail the whole WalkBlobReferences walk, which let any
// member disable blob garbage collection for the entire workspace by editing
// their own profile. A URL that is not one of ours simply names no blob of
// ours, so it is not a reference — reporting false is the whole of the repair.
//
// The user identifier embedded in the URL must match the user the row belongs
// to, so one member cannot mint a reference that pins another member's blob.
func UserPhotoBlobKey(workspace WorkspaceID, user UserID, imageURL string) (string, bool) {
	prefix := "/users/" + string(workspace) + "/" + string(user) + "/photo/"
	if !strings.HasPrefix(imageURL, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(imageURL, prefix)
	if token == "" || strings.Contains(token, "/") {
		return "", false
	}
	return string(workspace) + "/users/" + string(user) + "/" + token, true
}

func NormalizeScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			seen[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for scope := range seen {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}

type Conversation struct {
	ID            ConversationID
	WorkspaceID   WorkspaceID
	Name          string
	Topic         string
	Purpose       string
	Archived      bool
	IsPrivate     bool
	IsDirect      bool
	IsGroupDirect bool
	UnreadCount   int
}

type ConversationPreferenceList struct {
	Types []ConversationPreferenceType
	Users []UserID
}

type ConversationPreferenceType string

type ConversationPrefs struct {
	ConversationID ConversationID
	CanThread      ConversationPreferenceList
	WhoCanPost     ConversationPreferenceList
}

func MatchesConversationType(conversation Conversation, typeValue ConversationType) bool {
	switch typeValue {
	case ConversationTypePublic:
		return !conversation.IsPrivate && !conversation.IsDirect && !conversation.IsGroupDirect
	case ConversationTypePrivate:
		return conversation.IsPrivate && !conversation.IsDirect && !conversation.IsGroupDirect
	case ConversationTypeIM:
		return conversation.IsDirect
	case ConversationTypeMPIM:
		return conversation.IsGroupDirect
	default:
		return false
	}
}

type ReadCursor struct {
	WorkspaceID  WorkspaceID
	UserID       UserID
	Conversation ConversationID
	LastRead     MessageTimestamp
	UpdatedAt    time.Time
}

type Reaction struct {
	Message   MessageID
	Name      string
	UserID    UserID
	CreatedAt time.Time
}

// ActivityKind is one filter in Slack's Activity view. One item may carry more
// than one kind: a direct message that mentions the recipient must appear under
// both DMs and Mentions without creating two independently triageable rows.
type ActivityKind string

const (
	ActivityDM       ActivityKind = "dm"
	ActivityMention  ActivityKind = "mention"
	ActivityThread   ActivityKind = "thread"
	ActivityChannel  ActivityKind = "channel"
	ActivityReaction ActivityKind = "reaction"
	ActivityApp      ActivityKind = "app"
	ActivityReminder ActivityKind = "reminder"
)

func (kind ActivityKind) Valid() bool {
	switch kind {
	case ActivityDM, ActivityMention, ActivityThread, ActivityChannel, ActivityReaction, ActivityApp, ActivityReminder:
		return true
	default:
		return false
	}
}

type ActivityLayout string

const (
	ActivityDetailed ActivityLayout = "detailed"
	ActivityDense    ActivityLayout = "dense"
)

func (layout ActivityLayout) Valid() bool {
	return layout == ActivityDetailed || layout == ActivityDense
}

// ActivityItem is the durable per-recipient notification state. Source content
// is hydrated only after authorization; SourceAvailable distinguishes a
// deleted/inaccessible source from a malformed empty message.
type ActivityItem struct {
	ID              ActivityID
	WorkspaceID     WorkspaceID
	UserID          UserID
	Kinds           []ActivityKind
	ActorID         UserID
	Conversation    ConversationID
	MessageID       MessageID
	ReminderID      LaterReminderID
	ReactionName    string
	OccurredAt      time.Time
	ReadAt          time.Time
	ClearedAt       time.Time
	Message         Message
	Reminder        LaterReminder
	SourceAvailable bool
}

type ActivityQuery struct {
	Kinds       []ActivityKind
	UnreadOnly  bool
	ClearedOnly bool
	Page        PageRequest
}

func (query ActivityQuery) Valid() bool {
	for _, kind := range query.Kinds {
		if !kind.Valid() {
			return false
		}
	}
	return true
}

type ActivityPage struct {
	Items      []ActivityItem
	NextCursor Cursor
	HasMore    bool
}

type ActivityPreferences struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	Layout      ActivityLayout
}

type ActivityMutation string

const (
	ActivityMarkRead   ActivityMutation = "read"
	ActivityMarkUnread ActivityMutation = "unread"
	ActivityClear      ActivityMutation = "clear"
	ActivityRestore    ActivityMutation = "restore"
)

func (mutation ActivityMutation) Valid() bool {
	switch mutation {
	case ActivityMarkRead, ActivityMarkUnread, ActivityClear, ActivityRestore:
		return true
	default:
		return false
	}
}

type UserReaction struct {
	Conversation ConversationID
	Message      Message
	Reaction     Reaction
}

func ReactionKey(reaction Reaction) string { return reaction.Name + "\x00" + string(reaction.UserID) }

type Pin struct {
	Message   MessageID
	UserID    UserID
	CreatedAt time.Time
}

type Star struct {
	Message      Message
	Conversation ConversationID
	UserID       UserID
	CreatedAt    time.Time
}

type SavedItemState string

const (
	SavedItemInProgress SavedItemState = "in_progress"
	SavedItemArchived   SavedItemState = "archived"
	SavedItemCompleted  SavedItemState = "completed"
)

func (state SavedItemState) Valid() bool {
	return state == SavedItemInProgress || state == SavedItemArchived || state == SavedItemCompleted
}

// SavedItem is the private first-party state behind Slack's current Later
// surface. It is intentionally distinct from Star: Slack does not expose
// current Later items through the deprecated stars.* API.
//
// Message is populated only when the requesting member can still read the
// source. SourceAvailable is explicit so a deleted or inaccessible source is
// distinguishable from a malformed empty message without leaking its content.
type SavedItem struct {
	ID              SavedItemID
	WorkspaceID     WorkspaceID
	UserID          UserID
	MessageID       MessageID
	Conversation    ConversationID
	State           SavedItemState
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Message         Message
	SourceAvailable bool
}

type SavedItemPage struct {
	Items      []SavedItem
	NextCursor Cursor
	HasMore    bool
}

type Bookmark struct {
	ID           BookmarkID
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	Title        string
	Type         string
	Link         string
	Emoji        string
	EntityID     string
	AccessLevel  string
	ParentID     BookmarkID
	CreatedAt    time.Time
	UpdatedAt    time.Time
	UpdatedBy    UserID
}

type BookmarkUpdate struct {
	Title    string
	Link     string
	Emoji    string
	SetTitle bool
	SetLink  bool
	SetEmoji bool
}

const MaxBookmarksPerConversation = 100

type File struct {
	ID             FileID
	WorkspaceID    WorkspaceID
	Uploader       UserID
	Name           string
	Title          string
	MIMEType       string
	BlobKey        string
	PublicToken    string
	Size           int64
	CreatedAt      time.Time
	Deleted        bool
	SharedChannels []ConversationID
}

type Canvas struct {
	ID              CanvasID
	WorkspaceID     WorkspaceID
	OwnerID         UserID
	Title           string
	DocumentContent string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CanvasAccess struct {
	CanvasID   CanvasID
	EntityType string
	EntityID   string
	Access     string
}

type CanvasSection struct {
	ID   string
	Type string
	Text string
}

type FileComment struct {
	ID          FileCommentID
	File        FileID
	WorkspaceID WorkspaceID
	UserID      UserID
	Text        string
	Blocks      string
	CreatedAt   time.Time
	Deleted     bool
}

type Reminder struct {
	WorkspaceID WorkspaceID
	ID          ReminderID
	Creator     UserID
	User        UserID
	Text        string
	Time        time.Time
	CompleteAt  time.Time
	Recurring   bool
}

type ReminderPage struct {
	Reminders  []Reminder
	NextCursor Cursor
	HasMore    bool
}

type LaterReminderTarget string

const (
	LaterReminderPersonal LaterReminderTarget = "personal"
	LaterReminderChannel  LaterReminderTarget = "channel"
)

func (target LaterReminderTarget) Valid() bool {
	return target == LaterReminderPersonal || target == LaterReminderChannel
}

type ReminderRecurrence string

const (
	ReminderOnce    ReminderRecurrence = ""
	ReminderDaily   ReminderRecurrence = "daily"
	ReminderWeekly  ReminderRecurrence = "weekly"
	ReminderMonthly ReminderRecurrence = "monthly"
	ReminderYearly  ReminderRecurrence = "yearly"
)

func (recurrence ReminderRecurrence) Valid() bool {
	switch recurrence {
	case ReminderOnce, ReminderDaily, ReminderWeekly, ReminderMonthly, ReminderYearly:
		return true
	default:
		return false
	}
}

// LaterReminder is SameOldChat's private first-party reminder state. It is
// deliberately separate from Reminder, which preserves Slack's deprecated
// reminders.* app contract. Slack exposes no app API for current Later.
//
// Personal reminders are visible only to UserID. Channel reminders have a
// Channel and no UserID, and are listed by Creator. SourceMessageID is optional
// and retains the message selected by "Remind me about this".
type LaterReminder struct {
	ID                 LaterReminderID
	WorkspaceID        WorkspaceID
	Creator            UserID
	UserID             UserID
	Channel            ConversationID
	SourceMessageID    MessageID
	SourceConversation ConversationID
	SourceTimestamp    MessageTimestamp
	Target             LaterReminderTarget
	Text               string
	DueAt              time.Time
	TimeZone           string
	Recurrence         ReminderRecurrence
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        time.Time
	LastDeliveredAt    time.Time
	AcknowledgedAt     time.Time
	FailedAt           time.Time
	FailureCode        string
}

type LaterReminderPage struct {
	Items      []LaterReminder
	NextCursor Cursor
	HasMore    bool
}

type LaterReminderRequest struct {
	Target          LaterReminderTarget
	Channel         ConversationID
	SourceChannel   ConversationID
	SourceTimestamp MessageTimestamp
	Text            string
	DueAt           time.Time
	TimeZone        string
	Recurrence      ReminderRecurrence
}

type ScheduledMessage struct {
	WorkspaceID     WorkspaceID
	ID              ScheduledMessageID
	Channel         ConversationID
	Author          UserID
	AppID           AppID
	BotID           BotID
	CredentialHash  string
	Text            string
	Blocks          string
	Attachments     string
	ThreadTimestamp MessageTimestamp
	PostAt          time.Time
	CreatedAt       time.Time
	DeliveredAt     time.Time
	FailedAt        time.Time
	FailureCode     string
}

type ScheduledMessagePage struct {
	Items      []ScheduledMessage
	NextCursor Cursor
	HasMore    bool
}

type ScheduledMessageRequest struct {
	Channel         ConversationID
	Text            string
	Blocks          string
	Attachments     string
	ThreadTimestamp MessageTimestamp
	PostAt          time.Time
	AppID           AppID
	BotID           BotID
	CredentialHash  string
}

type ScheduledMessageQuery struct {
	CredentialHash string
	Channel        ConversationID
	Oldest         time.Time
	Latest         time.Time
	Page           PageRequest
}

type UserGroup struct {
	WorkspaceID WorkspaceID
	ID          UserGroupID
	Name        string
	Handle      string
	Description string
	Creator     UserID
	UpdatedBy   UserID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   time.Time
	Enabled     bool
	Users       []UserID
	Channels    []ConversationID
}

type UserGroupPage struct {
	Groups     []UserGroup
	NextCursor Cursor
	HasMore    bool
}

type InviteRequestStatus string

const (
	InviteRequestPending  InviteRequestStatus = "pending"
	InviteRequestApproved InviteRequestStatus = "approved"
	InviteRequestDenied   InviteRequestStatus = "denied"
)

type InviteRequest struct {
	ID                InviteRequestID
	WorkspaceID       WorkspaceID
	Email             string
	RequestedBy       UserID
	ChannelIDs        []ConversationID
	CustomMessage     string
	RealName          string
	Resend            bool
	Restricted        bool
	UltraRestricted   bool
	GuestExpirationAt time.Time
	Status            InviteRequestStatus
	CreatedAt         time.Time
	ReviewedAt        time.Time
}

type InviteRequestPage struct {
	Requests   []InviteRequest
	NextCursor Cursor
	HasMore    bool
}

type AppApprovalStatus string

const (
	AppApprovalRequested  AppApprovalStatus = "requested"
	AppApprovalApproved   AppApprovalStatus = "approved"
	AppApprovalRestricted AppApprovalStatus = "restricted"
)

type AppApproval struct {
	ID          AppID
	RequestID   AppRequestID
	WorkspaceID WorkspaceID
	Status      AppApprovalStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type AppApprovalPage struct {
	Apps       []AppApproval
	NextCursor Cursor
	HasMore    bool
}

type AppInstallation struct {
	AppID       AppID
	WorkspaceID WorkspaceID
	Enabled     bool
	CreatedAt   time.Time
}

// App is the durable developer-owned Slack application. Installation records
// deliberately reference this aggregate rather than acting as the app model
// themselves: an uninstalled app still has credentials, configuration,
// collaborators, and manifest history.
type App struct {
	ID                     AppID
	DevelopmentWorkspaceID WorkspaceID
	OwnerID                UserID
	Name                   string
	Description            string
	ClientID               string
	SigningSecretHash      string
	// SigningSecretCiphertext is storage-internal encrypted credential
	// material. It must never cross the public service or gRPC boundary.
	SigningSecretCiphertext     string
	VerificationTokenHash       string
	VerificationTokenCiphertext string
	ManifestVersion             int64
	Distribution                string
	SocketModeEnabled           bool
	TokenRotationEnabled        bool
	Deleted                     bool
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

type AppManifestRevision struct {
	AppID     AppID
	Version   int64
	Manifest  string
	CreatedBy UserID
	CreatedAt time.Time
}

// AppManifestSnapshot is the current durable configuration for an installed
// application. It is an internal storage result used by delivery workers; the
// manifest remains versioned independently of the app aggregate.
type AppManifestSnapshot struct {
	App      App
	Manifest string
}

// InstalledApp is the user-facing, non-secret projection of an app installed
// in one workspace. It deliberately excludes developer credentials and the raw
// manifest from the process boundary.
type InstalledApp struct {
	ID                  AppID
	Name                string
	Description         string
	HomeTabEnabled      bool
	MessagesTabEnabled  bool
	MessagesTabReadOnly bool
	BotDisplayName      string
	BotUserID           UserID
}

// AppTrigger is the durable one-use, short-lived capability produced by an
// interaction. TokenHash is a digest; the plaintext trigger_id is returned only
// in the signed request to the owning app.
type AppTrigger struct {
	TokenHash   string
	AppID       AppID
	WorkspaceID WorkspaceID
	UserID      UserID
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConsumedAt  time.Time
}

// AppResponseURL is the durable capability behind response_url. Slack permits
// at most five uses in thirty minutes. OriginalMessageID is populated for
// interactive-message actions and empty for slash commands.
type AppResponseURL struct {
	TokenHash         string
	AppID             AppID
	WorkspaceID       WorkspaceID
	UserID            UserID
	ConversationID    ConversationID
	OriginalMessageID MessageID
	ThreadTimestamp   MessageTimestamp
	CreatedAt         time.Time
	ExpiresAt         time.Time
	UsesRemaining     int
}

type AppBlockAction struct {
	MessageID MessageID
	BlockID   string
	ActionID  string
	Type      string
	Value     string
}

// AppViewBlockAction is an interaction with an element rendered inside a
// modal or App Home view. State is the complete Slack view.state object at the
// instant of the action, not only the element that changed.
type AppViewBlockAction struct {
	ViewID   ViewID
	BlockID  string
	ActionID string
	Type     string
	Value    string
	State    string
}

// AppOptionQuery identifies one external_select or multi_external_select
// element. Exactly one of MessageID and ViewID is set.
type AppOptionQuery struct {
	AppID     AppID
	MessageID MessageID
	ViewID    ViewID
	BlockID   string
	ActionID  string
	Value     string
}

type AppOption struct {
	Text        string
	Value       string
	Description string
	Group       string
}

type AppShortcut struct {
	AppID        AppID
	AppName      string
	Name         string
	CallbackID   string
	Description  string
	Type         string
	Command      string
	UsageHint    string
	ShouldEscape bool
}

// AppConfigurationToken authenticates the manifest-management APIs. Only
// digests are persisted; plaintext access and refresh tokens are returned once
// by the service that creates or rotates them.
type AppConfigurationToken struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	ExpiresAt   time.Time
	Revoked     bool
}

type AppConfigurationCredentials struct {
	Token        string
	RefreshToken string
	WorkspaceID  WorkspaceID
	UserID       UserID
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

type AppCredentials struct {
	ClientID          string
	ClientSecret      string
	SigningSecret     string
	VerificationToken string
}

type IncomingWebhook struct {
	ID             IncomingWebhookID
	WorkspaceID    WorkspaceID
	AppID          AppID
	ConversationID ConversationID
	UserID         UserID
	SecretHash     string
	Enabled        bool
	CreatedAt      time.Time
}

// AppDatastoreItem is one durable JSON object owned by one installed hosted
// app. Item contains canonical JSON; ID is the value of the datastore's
// declared primary key and is indexed separately so reads never depend on a
// database-specific JSON query implementation.
type AppDatastoreItem struct {
	AppID       AppID
	WorkspaceID WorkspaceID
	Datastore   string
	ID          string
	Item        string
	UpdatedAt   time.Time
}

type AppPermissionRequest struct {
	ID           AppRequestID
	WorkspaceID  WorkspaceID
	RequesterID  UserID
	TargetUserID UserID
	Scopes       []string
	TriggerID    string
	CreatedAt    time.Time
}

type FilePage struct {
	Files      []File
	NextCursor Cursor
	HasMore    bool
}

type RemoteFile struct {
	ID                FileID
	WorkspaceID       WorkspaceID
	ExternalID        string
	Title             string
	FileType          string
	ExternalURL       string
	PreviewImage      string
	IndexableContents string
	CreatedAt         time.Time
	Deleted           bool
	SharedChannels    []ConversationID
}

type RemoteFilePage struct {
	Files      []RemoteFile
	NextCursor Cursor
	HasMore    bool
}

type List struct {
	ID                ListID
	WorkspaceID       WorkspaceID
	OwnerID           UserID
	Name              string
	DescriptionBlocks string
	Schema            string
	TodoMode          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ListItem struct {
	ID           ListItemID
	ListID       ListID
	ParentItemID ListItemID
	WorkspaceID  WorkspaceID
	Fields       string
	CreatedBy    UserID
	UpdatedBy    UserID
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Archived     bool
}

type ListItemPage struct {
	Items      []ListItem
	NextCursor Cursor
	HasMore    bool
}

type ListAccess struct {
	ListID     ListID
	EntityType string
	EntityID   string
	Access     string
}

type ListDownload struct {
	ID              ListDownloadID
	ListID          ListID
	WorkspaceID     WorkspaceID
	Status          string
	URL             string
	IncludeArchived bool
	CreatedAt       time.Time
}

type RemoteFileLookup struct {
	ID         FileID
	ExternalID string
}

type RemoteFileUpdate struct {
	Lookup            RemoteFileLookup
	SetTitle          bool
	Title             string
	SetFileType       bool
	FileType          string
	SetExternalURL    bool
	ExternalURL       string
	SetPreviewImage   bool
	PreviewImage      string
	SetIndexableData  bool
	IndexableContents string
}

func (value RemoteFileLookup) Valid() bool {
	return (strings.TrimSpace(string(value.ID)) != "") != (strings.TrimSpace(value.ExternalID) != "")
}

type Message struct {
	ID           MessageID
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	AuthorID     UserID
	// AppID identifies the Slack app that authored the message. It is empty for
	// first-party human messages. Interactive elements must route through this
	// durable provenance rather than guessing from an action label or bot user.
	AppID       AppID
	Text        string
	Blocks      string
	Attachments string
	// Metadata is Slack's app-authored message metadata object as normalized
	// JSON. StreamState is an internal durable projection of chat.*Stream
	// chunks; it is not emitted by the Web API.
	Metadata        string
	StreamState     string
	ThreadTimestamp MessageTimestamp
	CreatedAt       time.Time
	Deleted         bool
	Unfurls         map[string]string
	// Files are the durable file shares carried by this message. A file that
	// merely names a channel in its metadata is not visible conversation
	// content; the message relationship supplies ordering, threads, events, API
	// projection, and the first-party timeline.
	Files []File
}

type MessageStreamStart struct {
	Conversation    ConversationID
	ThreadTimestamp MessageTimestamp
	AppID           AppID
	BotID           BotID
	RecipientTeamID WorkspaceID
	RecipientUserID UserID
	MarkdownText    string
	Chunks          string
	TaskDisplayMode string
	Username        string
	IconEmoji       string
	IconURL         string
}

type MessageStreamMutation struct {
	Conversation ConversationID
	Timestamp    MessageTimestamp
	AppID        AppID
	MarkdownText string
	Chunks       string
	Blocks       string
	Metadata     string
}

type MessageStreamState struct {
	Active          bool              `json:"active"`
	TaskDisplayMode string            `json:"task_display_mode,omitempty"`
	BotID           BotID             `json:"bot_id,omitempty"`
	Username        string            `json:"username,omitempty"`
	IconEmoji       string            `json:"icon_emoji,omitempty"`
	IconURL         string            `json:"icon_url,omitempty"`
	PlanTitle       string            `json:"plan_title,omitempty"`
	Tasks           []json.RawMessage `json:"tasks,omitempty"`
	ChunkBlocks     []json.RawMessage `json:"chunk_blocks,omitempty"`
	Warnings        []string          `json:"warnings,omitempty"`
}

// MessagePatch preserves the difference between an omitted Slack field and a
// field explicitly set to an empty value. That distinction is observable:
// chat.update retains omitted blocks and attachments, while [] removes them.
// Plain strings cannot represent both states and previously caused the HTTP
// and gRPC compositions to erase rich content that an official SDK omitted.
type MessagePatch struct {
	Text        *string
	Blocks      *string
	Attachments *string
}

func NormalizeBlocks(raw []byte) (string, error) {
	return normalizeJSONArrayObjects(raw, "blocks")
}

func NormalizeAttachments(raw []byte) (string, error) {
	return normalizeJSONArrayObjects(raw, "attachments")
}

func normalizeJSONArrayObjects(raw []byte, name string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || blocks == nil {
		return "", fmt.Errorf("%s must be a JSON array", name)
	}
	if len(blocks) > 100 {
		return "", fmt.Errorf("%s exceed the maximum count", name)
	}
	for _, block := range blocks {
		// The array decode above already rejected malformed JSON, so a value
		// beginning with '{' is an object. Decoding each one into a map to
		// learn only its kind walks every nested value for nothing.
		if trimmed := bytes.TrimLeft(block, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
			return "", fmt.Errorf("each %s item must be a JSON object", name)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func NormalizeUnfurls(values map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for key, raw := range values {
		key = strings.TrimSpace(key)
		if key == "" || len(result) >= 100 {
			return nil, errors.New("invalid unfurl")
		}
		// Compact rejects malformed JSON on its own, so validating first would
		// scan every value twice to learn what the compaction already reports.
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(raw)); err != nil {
			return nil, errors.New("invalid unfurl")
		}
		result[key] = compact.String()
	}
	return result, nil
}

type EphemeralMessage struct {
	ID           MessageID
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	AuthorID     UserID
	AppID        AppID
	RecipientID  UserID
	Text         string
	Blocks       string
	Attachments  string
	Timestamp    MessageTimestamp
	CreatedAt    time.Time
}

type AccessLog struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	Username    string
	CreatedAt   time.Time
	IP          string
	UserAgent   string
}

type IntegrationLog struct {
	AppID       AppID
	AppType     string
	ChangeType  string
	ChannelID   ConversationID
	Date        time.Time
	Scope       string
	ServiceID   string
	ServiceType string
	UserID      UserID
	UserName    string
}

type IntegrationLogPage struct {
	Logs  []IntegrationLog
	Page  int
	Pages int
	Total int
}

type RTMConnection struct {
	ID          string
	WorkspaceID WorkspaceID
	UserID      UserID
	ExpiresAt   time.Time
}

type SocketModeConnection struct {
	ID        string
	AppID     AppID
	ExpiresAt time.Time
}

const SocketModeConnectionLimit = 10

type SocketModeResponse struct {
	AppID          AppID
	EnvelopeID     string
	Payload        string
	ReceivedAt     time.Time
	LeaseOwner     string
	LeaseExpiresAt time.Time
	AcknowledgedAt time.Time
}

// SocketModeInteraction is a durable, app-targeted slash command or
// interactivity envelope. It is separate from the workspace event outbox:
// interactions belong to exactly one app, while an ordinary workspace event
// may be subscribed to by several apps.
type SocketModeInteraction struct {
	EnvelopeID     string
	AppID          AppID
	WorkspaceID    WorkspaceID
	UserID         UserID
	Type           string
	Payload        string
	Response       AppResponseURL
	CreatedAt      time.Time
	LeaseOwner     string
	LeaseExpiresAt time.Time
	RetryAt        time.Time
	RetryCount     int
	RetryReason    string
	AcknowledgedAt time.Time
}

type ExternalUploadStatus string

type ExternalUpload struct {
	ID          ExternalUploadID
	WorkspaceID WorkspaceID
	Uploader    UserID
	Name        string
	Title       string
	MIMEType    string
	BlobKey     string
	FileID      FileID
	Size        int64
	Status      ExternalUploadStatus
	CreatedAt   time.Time
	ExpiresAt   time.Time
	UploadedAt  time.Time
	CompletedAt time.Time
}

const (
	ExternalUploadPending   ExternalUploadStatus = "pending"
	ExternalUploadUploaded  ExternalUploadStatus = "uploaded"
	ExternalUploadCompleted ExternalUploadStatus = "completed"
)

type ExternalUploadCompletion struct {
	ID    ExternalUploadID
	Title string
}
