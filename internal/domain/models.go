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
	"unicode"
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

// WorkspaceMembershipSummary is one workspace a person belongs to, with the
// identity they hold there. A user row belongs to exactly one workspace, so the
// same person in two workspaces is two rows sharing an email address — which is
// why a switcher resolves by address and reports the local identity for each.
type WorkspaceMembershipSummary struct {
	Workspace Workspace
	UserID    UserID
	Role      WorkspaceRole
}

type BillableUser struct {
	UserID        UserID
	BillingActive bool
}

type BillableInfo struct {
	Users []BillableUser
}

type UserProfile struct {
	DisplayName      string
	StatusText       string
	StatusEmoji      string
	StatusExpiration time.Time
	// ActiveScheduledStatusID is internal lifecycle fencing. It is deliberately
	// absent from Slack Web API profile JSON.
	ActiveScheduledStatusID ScheduledStatusID
	Image24                 string
	Image32                 string
	Image48                 string
	Image72                 string
	Image192                string
	Image512                string
	Image1024               string
}

// ScheduledStatus is a first-party Slack client feature, not a Slack Web API
// resource. Slack lets a member keep at most five future statuses, edit or
// cancel them before they start, and chooses the resulting current-status
// expiration from the scheduled end time.
type ScheduledStatus struct {
	ID          ScheduledStatusID
	WorkspaceID WorkspaceID
	UserID      UserID
	StatusText  string
	StatusEmoji string
	StartsAt    time.Time
	EndsAt      time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type User struct {
	ID          UserID
	WorkspaceID WorkspaceID
	Email       string
	Name        string
	RealName    string
	Profile     UserProfile
	Presence    Presence
	// LastActiveAt is the most recent moment this member was observed
	// interacting. It drives automatic presence and nothing else; it is not a
	// login record and carries no session identity.
	LastActiveAt time.Time
	Deleted      bool
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

// PresenceIdleAfter is how long a member with automatic presence may be
// inactive before Slack reports them away. Slack documents ten minutes.
const PresenceIdleAfter = 10 * time.Minute

// Current reports presence with no knowledge of activity, which means automatic
// presence resolves to active. It is correct only where the caller genuinely
// has no activity to consult.
//
// Prefer CurrentAt. This exists because `auto` used to be the *only* answer:
// every reader called this, so a member who selected automatic presence and
// then closed their laptop stayed "active" indefinitely, and automatic presence
// was indistinguishable from a manual "always active".
func (p Presence) Current() string {
	return p.CurrentAt(time.Time{}, time.Time{})
}

// CurrentAt resolves automatic presence against the member's last observed
// activity. A zero lastActive means nothing has ever been observed, which is
// treated as active rather than away: reporting a member away because the
// deployment has not yet seen them would make every member away at startup.
func (p Presence) CurrentAt(lastActive, now time.Time) string {
	if p == PresenceAway {
		return "away"
	}
	if lastActive.IsZero() || now.IsZero() {
		return "active"
	}
	if now.Sub(lastActive) >= PresenceIdleAfter {
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

// CallKind separates the two things a call record can be. An external call is
// one an app registered through calls.add: the media lives with the app, which
// is why ExternalUniqueID and JoinURL are required for it. A huddle is started
// inside a conversation by a person here, so it has a conversation and no
// external identity at all — the two could not share one shape without one of
// them carrying required fields it has no value for.
type CallKind string

const (
	CallKindExternal CallKind = "external"
	CallKindHuddle   CallKind = "huddle"
)

func (kind CallKind) Valid() bool {
	return kind == CallKindExternal || kind == CallKindHuddle
}

type Call struct {
	ID          CallID
	WorkspaceID WorkspaceID
	Kind        CallKind
	// ConversationID is the conversation a huddle belongs to, and empty for an
	// external call. At most one huddle per conversation may be active at a
	// time; see Store.StartHuddle.
	ConversationID    ConversationID
	ExternalUniqueID  string
	ExternalDisplayID string
	JoinURL           string
	DesktopAppJoinURL string
	Title             string
	CreatedBy         UserID
	// Participants are the people currently in the call, not everyone who ever
	// was. Someone who leaves is removed; the record of their having been there
	// is the huddle.joined and huddle.left pair in the durable journal.
	Participants    []UserID
	StartedAt       time.Time
	EndedAt         time.Time
	DurationSeconds int64
}

// Active reports whether the call is still running.
func (call Call) Active() bool { return call.EndedAt.IsZero() }

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
	WorkflowStepExecuting  WorkflowStepStatus = "executing"
	WorkflowStepWaiting    WorkflowStepStatus = "waiting"
	WorkflowStepCompleted  WorkflowStepStatus = "completed"
	WorkflowStepFailed     WorkflowStepStatus = "failed"
	WorkflowStepCancelled  WorkflowStepStatus = "cancelled"
)

type WorkflowStep struct {
	ID            WorkflowStepID
	WorkflowRunID WorkflowRunID
	WorkspaceID   WorkspaceID
	AppID         AppID
	UserID        UserID
	FunctionID    string
	EditID        string
	Status        WorkflowStepStatus
	Inputs        string
	Outputs       string
	Error         string
	StepName      string
	ImageURL      string
	// ResumeAt is when a delay step becomes due. It is zero for every other
	// kind: a step that waits on a person or an app is woken by them arriving,
	// and only a step that waits on the clock needs the clock recorded. Storing
	// the instant rather than the duration is what makes a delay survive a
	// restart — a duration would start again from whenever the process did.
	ResumeAt  time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AssistantThread is the state an assistant app may set on one thread: the
// title it is given in the client, the transient status it shows while working,
// and the prompts it offers before anyone has typed. All three are written by
// the app through assistant.threads.*; none of them is content, so none is
// authored by a member.
type AssistantThread struct {
	WorkspaceID     WorkspaceID
	Conversation    ConversationID
	ThreadTimestamp MessageTimestamp
	Title           string
	Status          string
	PromptsTitle    string
	Prompts         []AssistantPrompt
	UpdatedAt       time.Time
}

type AssistantPrompt struct {
	Title   string
	Message string
}

// AssistantThreadField names which piece of assistant state a write sets. The
// three API methods each set exactly one, and a store method that took the
// whole record would silently clear the other two whenever a caller left them
// empty — which is what an app setting only its status would do.
type AssistantThreadField string

const (
	AssistantThreadTitle   AssistantThreadField = "title"
	AssistantThreadStatus  AssistantThreadField = "status"
	AssistantThreadPrompts AssistantThreadField = "prompts"
)

func (f AssistantThreadField) Valid() bool {
	return f == AssistantThreadTitle || f == AssistantThreadStatus || f == AssistantThreadPrompts
}

// AssistantPromptLimit is how many prompts one thread may offer. Slack's own
// assistant pane shows a short list; the bound exists so an app cannot turn the
// pane into an unbounded page.
const AssistantPromptLimit = 8

// TypingSignal is one member composing in one conversation, and it is the only
// piece of workspace state in this product that is deliberately not journalled.
// Every other mutation writes an outbox record because the fact it records is
// worth replaying: a message posted an hour ago is still true. "Someone is
// typing" is true for about as long as it takes to read, and a journal entry
// for it would be replayed to a reconnecting client as news, delivered to apps
// that have no use for it, and written twenty times a minute per composing
// member for as long as the workspace exists.
//
// So a signal is state with an expiry rather than an event. It is written by
// replacing the member's own row, read by asking who has not expired, and
// stopped by the clock rather than by a second message — which also means a
// client that closes its laptop mid-word stops appearing without having to
// announce it, and a process that restarts finds every signal it left behind
// already expired.
type TypingSignal struct {
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	UserID       UserID
	// ExpiresAt is when this signal stops being shown. Readers compare it
	// against their own clock, so there is no "stopped typing" write to lose.
	ExpiresAt time.Time
}

func (s TypingSignal) Valid() bool {
	return s.WorkspaceID != "" && s.Conversation != "" && s.UserID != "" && !s.ExpiresAt.IsZero()
}

// Active reports whether this signal should still be shown at the given
// instant. Expiry is exclusive so a signal written and read in the same
// instant — which the tests do — is active.
func (s TypingSignal) Active(now time.Time) bool {
	return s.ExpiresAt.After(now)
}

// TypingSignalsIn narrows a reader's signals to one conversation. It lives
// here rather than in either composition because both of them need it — the
// local service filters what it read from the store, the gRPC client filters
// what it read from the wire — and a filter implemented twice is a rule that
// can disagree with itself.
func TypingSignalsIn(signals []TypingSignal, conversation ConversationID) []TypingSignal {
	filtered := make([]TypingSignal, 0, len(signals))
	for _, signal := range signals {
		if signal.Conversation == conversation {
			filtered = append(filtered, signal)
		}
	}
	return filtered
}

const (
	// TypingSignalTTL is how long one signal lasts without being renewed.
	// Slack's indicator clears a few seconds after the last keystroke rather
	// than the moment a key is released, so a member who pauses to think keeps
	// the indicator rather than making it flicker.
	TypingSignalTTL = 6 * time.Second
	// TypingSignalInterval is how often a composing client renews its signal.
	// It has to be comfortably shorter than the TTL or a member typing steadily
	// would blink out between renewals; it also bounds the write rate, because
	// every keystroke past the first in each interval is discarded by the
	// client rather than sent.
	TypingSignalInterval = 3 * time.Second
)

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
	// The Slack Connect identity conversations.info must emit. A channel is
	// externally shared once another organization has accepted an invitation
	// to it, and pending while one is outstanding: the two are different facts
	// and a client shows different chrome for each, so neither can be derived
	// from the other.
	IsExtShared        bool
	IsPendingExtShared bool
}

// SharedInviteStatus is the state machine CONNECT-02 requires. Approval and
// acceptance are deliberately distinct transitions: the host approves who may
// be invited, and the invited organization decides separately whether to come.
type SharedInviteStatus string

const (
	SharedInvitePending  SharedInviteStatus = "pending"
	SharedInviteApproved SharedInviteStatus = "approved"
	SharedInviteAccepted SharedInviteStatus = "accepted"
	SharedInviteDeclined SharedInviteStatus = "declined"
	SharedInviteRevoked  SharedInviteStatus = "revoked"
)

// SharedInviteTransition reports whether one status may follow another. A
// pending invitation is approved or revoked by the host; an approved one is
// accepted or declined by the invited organization, or revoked by the host
// while it is still outstanding. Accepted and declined are terminal.
func SharedInviteTransition(from, to SharedInviteStatus) bool {
	switch from {
	case SharedInvitePending:
		return to == SharedInviteApproved || to == SharedInviteRevoked
	case SharedInviteApproved:
		return to == SharedInviteAccepted || to == SharedInviteDeclined || to == SharedInviteRevoked
	default:
		return false
	}
}

// SharedInvite is one invitation for an external organization to join one
// conversation. It is modelled on InviteRequest — the same recorded/issued/
// redeemed shape — because it is the same kind of fact about a different
// subject.
type SharedInvite struct {
	ID             SharedInviteID
	WorkspaceID    WorkspaceID
	ConversationID ConversationID
	// TargetWorkspaceID is the organization invited. Within one deployment an
	// organization is another workspace; a cross-deployment federation would
	// carry an external team identifier here instead, and is a recorded gap.
	TargetWorkspaceID WorkspaceID
	// TargetEmail is the person invited, when the invitation names one. Slack
	// allows either, and conversations.inviteShared takes both.
	TargetEmail string
	InvitedBy   UserID
	Status      SharedInviteStatus
	CreatedAt   time.Time
	ReviewedAt  time.Time
	SettledAt   time.Time
	ExpiresAt   time.Time
}

// Acceptable reports whether the invitation can still be accepted or declined.
func (invite SharedInvite) Acceptable(at time.Time) bool {
	if invite.Status != SharedInviteApproved {
		return false
	}
	return invite.ExpiresAt.IsZero() || !at.After(invite.ExpiresAt)
}

type SharedInvitePage struct {
	Invites    []SharedInvite
	NextCursor Cursor
	HasMore    bool
}

// SlackConnectCapacity is the documented maximum number of organizations in
// one Slack Connect channel, including the host. It is checked atomically when
// an invitation is accepted, never from a count read earlier: CONNECT-01
// forbids promising a place from a stale count.
const SlackConnectCapacity = 250

// DirectHistorySelection is the first-party choice Slack presents when people
// are added to a DM. Slack's public Web API does not expose this transition;
// the browser journey deliberately uses a typed application seam instead of
// inventing a conversations.* method.
type DirectHistorySelection string

const (
	DirectHistoryNone DirectHistorySelection = "none"
	DirectHistoryAll  DirectHistorySelection = "all"
)

func (selection DirectHistorySelection) Valid() bool {
	return selection == DirectHistoryNone || selection == DirectHistoryAll
}

// DirectConversationExpansion is the atomic store command behind adding people
// to a DM. The original conversation is never mutated. Target contains the new
// canonical participant set, while the two notices make Slack's visible
// participant notification part of the same commit as history and membership.
type DirectConversationExpansion struct {
	Source       ConversationID
	Target       Conversation
	Members      []UserID
	History      DirectHistorySelection
	SourceNotice Message
	TargetNotice Message
}

// GroupDirectConversion is the atomic store command behind Slack's
// "Change to a private channel" journey.
type GroupDirectConversion struct {
	Conversation ConversationID
	Name         string
	Notice       Message
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

// NotificationLevel is the durable trigger selection behind Slack's
// workspace default and conversation-specific notification controls.
// NotificationInherit is valid only for a conversation override.
type NotificationLevel string

const (
	NotificationInherit  NotificationLevel = "inherit"
	NotificationAll      NotificationLevel = "all"
	NotificationMentions NotificationLevel = "mentions"
	NotificationMute     NotificationLevel = "mute"
)

func (level NotificationLevel) ValidWorkspaceDefault() bool {
	return level == NotificationAll || level == NotificationMentions
}

func (level NotificationLevel) ValidConversationOverride() bool {
	switch level {
	case NotificationInherit, NotificationAll, NotificationMentions, NotificationMute:
		return true
	default:
		return false
	}
}

type WorkspaceNotificationPreferences struct {
	WorkspaceID       WorkspaceID
	UserID            UserID
	Level             NotificationLevel
	Keywords          []string
	ActivityChannels  bool
	ActivityReminders bool
	// BrowserNotifications is off unless the person turns it on. It is only
	// half of what a notification needs — the browser must also grant
	// permission — and the two are deliberately separate: a preference this
	// product stores cannot grant a permission only the browser can.
	BrowserNotifications bool
	// Schedule is the window a member allows notifications in. Outside it,
	// nothing is delivered — which makes it a fourth reason a notification does
	// not arrive, beside the stored preference, the browser's own grant and Do
	// Not Disturb. The page names which one is missing, because a surface that
	// says notifications are on while none arrive is worse than one that admits
	// it is off.
	Schedule NotificationSchedule
}

// NotificationSchedule is Slack's "allow notifications" window: some days, and
// a start and end time on those days.
//
// The time zone is carried rather than inferred. A schedule is a statement
// about the member's day, and the server's clock is not their day; the browser
// supplies its own zone the same way a scheduled message and a reminder already
// do, so the same instant means the same thing to all three.
type NotificationSchedule struct {
	Enabled bool
	// Days are the weekdays the window applies on. Empty with Enabled true is
	// refused rather than read as "every day": a schedule that allows nothing
	// is almost certainly a mistake, and one that allows everything is what
	// turning the schedule off already means.
	Days []time.Weekday
	// StartMinute and EndMinute are minutes from midnight in TimeZone. Start
	// equal to end is refused for the same reason an empty day set is.
	StartMinute int
	EndMinute   int
	TimeZone    string
}

// NotificationScheduleDayMinutes is the number of minutes in a day, and the
// exclusive upper bound of both ends of the window.
const NotificationScheduleDayMinutes = 24 * 60

func (s NotificationSchedule) Valid() bool {
	if !s.Enabled {
		return true
	}
	if len(s.Days) == 0 || s.StartMinute == s.EndMinute {
		return false
	}
	if s.StartMinute < 0 || s.StartMinute >= NotificationScheduleDayMinutes || s.EndMinute < 0 || s.EndMinute >= NotificationScheduleDayMinutes {
		return false
	}
	seen := make(map[time.Weekday]struct{}, len(s.Days))
	for _, day := range s.Days {
		if day < time.Sunday || day > time.Saturday {
			return false
		}
		if _, repeated := seen[day]; repeated {
			return false
		}
		seen[day] = struct{}{}
	}
	if _, err := time.LoadLocation(s.TimeZone); err != nil {
		return false
	}
	return true
}

// AllowsAt reports whether notifications may be delivered at this instant. A
// schedule that is off allows everything, which is what off means.
//
// A window whose end is before its start runs overnight — 22:00 to 07:00 is a
// real answer to "when may I be interrupted", and reading it as an empty window
// would silence a member who asked to be reachable at night. The day a
// wrapped window belongs to is the day it started on, so a Friday-night window
// keeps notifying into Saturday morning without Saturday being selected.
func (s NotificationSchedule) AllowsAt(instant time.Time) bool {
	if !s.Enabled {
		return true
	}
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		// A zone this build cannot resolve must not silence a member. Failing
		// open is the only safe direction: the cost is a notification that
		// should have waited, and the alternative is one that never arrives.
		return true
	}
	local := instant.In(location)
	minute := local.Hour()*60 + local.Minute()
	if s.StartMinute < s.EndMinute {
		return s.allowsDay(local.Weekday()) && minute >= s.StartMinute && minute < s.EndMinute
	}
	if minute >= s.StartMinute {
		return s.allowsDay(local.Weekday())
	}
	if minute < s.EndMinute {
		return s.allowsDay(local.AddDate(0, 0, -1).Weekday())
	}
	return false
}

func (s NotificationSchedule) allowsDay(day time.Weekday) bool {
	for _, allowed := range s.Days {
		if allowed == day {
			return true
		}
	}
	return false
}

func DefaultWorkspaceNotificationPreferences(workspace WorkspaceID, user UserID) WorkspaceNotificationPreferences {
	return WorkspaceNotificationPreferences{
		WorkspaceID: workspace, UserID: user, Level: NotificationMentions,
		ActivityChannels: true, ActivityReminders: true,
	}
}

func (preferences WorkspaceNotificationPreferences) Valid() bool {
	if preferences.WorkspaceID == "" || preferences.UserID == "" || !preferences.Level.ValidWorkspaceDefault() || len(preferences.Keywords) > 50 {
		return false
	}
	for _, keyword := range preferences.Keywords {
		if keyword == "" || len(keyword) > 100 || keyword != strings.TrimSpace(keyword) {
			return false
		}
	}
	if !preferences.Schedule.Valid() {
		return false
	}
	return true
}

type ConversationNotificationPreferences struct {
	WorkspaceID       WorkspaceID
	UserID            UserID
	Conversation      ConversationID
	Level             NotificationLevel
	FollowEveryThread bool
}

func DefaultConversationNotificationPreferences(workspace WorkspaceID, user UserID, conversation ConversationID) ConversationNotificationPreferences {
	return ConversationNotificationPreferences{
		WorkspaceID: workspace, UserID: user, Conversation: conversation, Level: NotificationInherit,
	}
}

func (preferences ConversationNotificationPreferences) Valid() bool {
	return preferences.WorkspaceID != "" && preferences.UserID != "" && preferences.Conversation != "" &&
		preferences.Level.ValidConversationOverride()
}

func (preferences ConversationNotificationPreferences) EffectiveLevel(workspace WorkspaceNotificationPreferences) NotificationLevel {
	if preferences.Level == NotificationInherit {
		return workspace.Level
	}
	return preferences.Level
}

// RetentionMaximumDays is Slack's documented upper bound on a retention
// duration: an integer greater than 0 and less than 36500 days.
const RetentionMaximumDays = 36500

// RetentionPolicy is a workspace's default for the two kinds of bulk content
// it stores. Messages and files carry separate durations because Slack
// configures them separately, and a workspace that wants to keep the
// conversation but not the attachments is an ordinary choice.
//
// Zero means keep forever. Slack's per-channel API cannot express forever —
// its duration must be positive — so forever is reached by having no override
// and a workspace default of zero, which is also the default for a workspace
// nobody has configured.
type RetentionPolicy struct {
	MessageDays int
	FileDays    int
}

// ValidRetentionDays reports whether a duration may be stored. Zero is
// permitted here and means forever; the API layer refuses zero separately,
// because Slack's setCustomRetention has no way to say it.
func ValidRetentionDays(days int) bool {
	return days >= 0 && days < RetentionMaximumDays
}

func (policy RetentionPolicy) Valid() bool {
	return ValidRetentionDays(policy.MessageDays) && ValidRetentionDays(policy.FileDays)
}

// ConversationRetention is one channel's override of the workspace message
// retention. It exists as its own record rather than a column on
// ConversationPrefs because ConversationPrefs is the response body of
// admin.conversations.getConversationPrefs, and Slack does not report
// retention there.
type ConversationRetention struct {
	ConversationID ConversationID
	// DurationDays is always positive when the record exists: an override that
	// meant "forever" would be indistinguishable from having no override, and
	// removing the override is how a channel returns to the workspace default.
	DurationDays int
	UpdatedAt    time.Time
}

// Effective resolves the duration that actually governs a conversation's
// messages, in the shape ConversationNotificationPreferences.EffectiveLevel
// already established: the override wins when it exists, and the workspace
// default applies when it does not.
func (override ConversationRetention) Effective(workspace RetentionPolicy) int {
	if override.DurationDays > 0 {
		return override.DurationDays
	}
	return workspace.MessageDays
}

// RetentionHorizon is the instant before which content governed by this
// duration has expired. A zero duration keeps everything, and returns the zero
// time so a caller can test it with IsZero rather than comparing against a
// sentinel far in the past.
func RetentionHorizon(days int, now time.Time) time.Time {
	if days <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
}

// RetentionSweepRequest is one pass over one conversation. The horizons are
// computed by the caller rather than derived from a policy here, so the store
// never has to resolve an override against a workspace default — and a test
// can ask for an exact horizon instead of arranging a clock.
type RetentionSweepRequest struct {
	WorkspaceID    WorkspaceID
	ConversationID ConversationID
	// MessageHorizon and FileHorizon are zero when that kind of content is
	// kept forever, which the store reads as "sweep nothing of this kind".
	MessageHorizon time.Time
	FileHorizon    time.Time
	SweptAt        time.Time
	// Limit bounds how many messages one pass may delete, so a workspace with
	// years of backlog drains over many cycles instead of holding one
	// transaction open across all of it.
	Limit int
}

// RetentionSweep is what one pass over one conversation removed. The counts
// are what the sweep event carries and what the administration page reports,
// so an operator can see the policy doing something rather than infer it.
type RetentionSweep struct {
	ConversationID ConversationID
	Messages       int
	Files          int
	SweptAt        time.Time
	// Complete is false when the batch limit was reached, so the caller knows
	// this conversation still has expired content and should not advance its
	// watermark past the work it did not do.
	Complete bool
	// ExpiredBlobs names the bytes the sweep orphaned. The store removes the
	// rows; the caller journals one blob-delete event per entry so the
	// existing blob cleanup worker reclaims the storage, which is the path an
	// ordinary file deletion already takes.
	ExpiredBlobs []ExpiredBlob
}

type ExpiredBlob struct {
	FileID  FileID
	BlobKey string
}

func NormalizeNotificationKeywords(keywords []string) []string {
	unique := make(map[string]struct{}, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" {
			unique[keyword] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for keyword := range unique {
		result = append(result, keyword)
	}
	sort.Strings(result)
	return result
}

// MatchesNotificationKeyword implements Slack's documented case-insensitive
// exact-match rule. Letter and number boundaries prevent "ship" from alerting
// on "shipping", while phrases and punctuation remain usable keywords.
func MatchesNotificationKeyword(text string, keywords []string) bool {
	haystack := []rune(strings.ToLower(text))
	for _, keyword := range keywords {
		needle := []rune(strings.ToLower(strings.TrimSpace(keyword)))
		if len(needle) == 0 || len(needle) > len(haystack) {
			continue
		}
		for start := 0; start+len(needle) <= len(haystack); start++ {
			match := true
			for offset := range needle {
				if haystack[start+offset] != needle[offset] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			beforeWord := start > 0 && (unicode.IsLetter(haystack[start-1]) || unicode.IsNumber(haystack[start-1]))
			end := start + len(needle)
			afterWord := end < len(haystack) && (unicode.IsLetter(haystack[end]) || unicode.IsNumber(haystack[end]))
			if !beforeWord && !afterWord {
				return true
			}
		}
	}
	return false
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
	ActivityDM         ActivityKind = "dm"
	ActivityMention    ActivityKind = "mention"
	ActivityThread     ActivityKind = "thread"
	ActivityChannel    ActivityKind = "channel"
	ActivityKeyword    ActivityKind = "keyword"
	ActivityReaction   ActivityKind = "reaction"
	ActivityApp        ActivityKind = "app"
	ActivityReminder   ActivityKind = "reminder"
	ActivityInvitation ActivityKind = "invitation"
)

func (kind ActivityKind) Valid() bool {
	switch kind {
	case ActivityDM, ActivityMention, ActivityThread, ActivityChannel, ActivityKeyword, ActivityReaction, ActivityApp, ActivityReminder, ActivityInvitation:
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
	ID           ActivityID
	WorkspaceID  WorkspaceID
	UserID       UserID
	Kinds        []ActivityKind
	ActorID      UserID
	Conversation ConversationID
	// CanvasID is set when the item is a canvas someone shared with this
	// member. It sits beside Conversation rather than replacing it because an
	// invitation to a channel and a share of a canvas are the same news — you
	// have been given access to something — arriving from different objects,
	// and Slack files both under invitations.
	CanvasID CanvasID
	// ListItemID is set when the item is work someone assigned to this member.
	// It joins the conversation and the canvas as a third kind of "you have
	// been given something". The growth is deliberate and bounded: each names a
	// different object, and one column holding any of them would make every
	// reader guess which it was holding.
	ListItemID   ListItemID
	ListID       ListID
	MessageID    MessageID
	ReminderID   LaterReminderID
	ReactionName string
	OccurredAt   time.Time
	ReadAt       time.Time
	ClearedAt    time.Time
	Message      Message
	Reminder     LaterReminder
	// CanvasTitle is resolved when the item is read, like Message and Reminder,
	// so a row can name what was shared without the reader following the link.
	CanvasTitle string
	// ListItem is what an Activity row needs to describe assigned work: enough
	// to name it and say whether it is late, and no more. It is a type of its
	// own rather than a whole ListItem so the conversion is complete rather
	// than lossy — a row carrying a half-filled item would be a second,
	// differently-shaped copy of the record that nothing else could trust.
	ListItem        ListItemSummary
	ListName        string
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
	ID          FileID
	WorkspaceID WorkspaceID
	Uploader    UserID
	Name        string
	Title       string
	MIMEType    string
	BlobKey     string
	PublicToken string
	Size        int64
	// Description is what the file is, in words, for a reader who cannot see
	// it. Slack calls it an image description and shows a control for it; the
	// pinned Web API snapshot predates the alt_txt parameter that carries it,
	// so this is first-party durable state rather than an invented API field —
	// the same standing as recent searches and Later.
	Description    string
	CreatedAt      time.Time
	Deleted        bool
	SharedChannels []ConversationID
}

// imageMIMEPrefix is the whole test for whether a file is shown rather than
// linked. Sniffing the bytes would be a second opinion about a fact the upload
// already recorded, and a file whose declared type is wrong is wrong
// everywhere else too.
const imageMIMEPrefix = "image/"

// IsImage reports whether this file is one a client should render in place. It
// lives on the domain because the answer decides both what the timeline draws
// and whether a description is worth asking for.
func (f File) IsImage() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(f.MIMEType)), imageMIMEPrefix)
}

// AccessibleName is what a screen reader announces for this file's image. The
// description wins because it was written for this purpose; the title is a
// reasonable second, because a member who titled an upload was describing it
// too. The file name is deliberately not a third: "IMG_4032.png" tells a reader
// nothing they did not already know, and an alt text that adds nothing is worse
// than an empty one, which at least lets the image be skipped.
func (f File) AccessibleName() string {
	if description := strings.TrimSpace(f.Description); description != "" {
		return description
	}
	return strings.TrimSpace(f.Title)
}

type Canvas struct {
	ID              CanvasID
	WorkspaceID     WorkspaceID
	OwnerID         UserID
	Title           string
	DocumentContent string
	// Version is a monotonic compare-and-swap revision. Writers submit the
	// revision they read plus one; stores reject stale writes with ErrConflict.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CanvasRevision is what a canvas said before an edit replaced it.
//
// It records the state that was superseded rather than the state that arrived,
// which is the difference between a history you can read backwards and a log of
// what happened. Version is the version the canvas held while this content was
// current, so restoring revision 3 means putting revision 3's content back — a
// row numbered by the edit that displaced it would make every reader subtract
// one.
type CanvasRevision struct {
	CanvasID        CanvasID
	WorkspaceID     WorkspaceID
	Version         int64
	Title           string
	DocumentContent string
	// EditedBy is who replaced this content, not who wrote it. A history
	// answers "who changed this", and the person who wrote a revision is the
	// EditedBy of the row before it.
	EditedBy  UserID
	CreatedAt time.Time
}

// CanvasComment is a remark anchored to one section of a canvas.
//
// It is anchored rather than attached to the document because a canvas is a
// document people argue about a paragraph at a time, and a comment list with no
// anchor is a chat log next to a page. The anchor is the section identifier the
// editor already assigns, so a comment survives the section's text changing —
// which is the ordinary case, since the comment is usually why it changed.
type CanvasComment struct {
	ID          CanvasCommentID
	CanvasID    CanvasID
	WorkspaceID WorkspaceID
	// SectionID is the section this comment is about. A comment whose section
	// has since been removed keeps its identifier and is shown against the
	// document instead: deleting a paragraph does not unsay what was said about
	// it, and silently dropping the comment would lose the reason it went.
	SectionID string
	UserID    UserID
	Text      string
	CreatedAt time.Time
	Deleted   bool
}

// CanvasCommentLimit bounds one comment. It is a remark on a paragraph, not a
// second document; a comment longer than the section it annotates is a sign the
// conversation belongs in a channel.
const CanvasCommentLimit = 4000

type CanvasCommentPage struct {
	Comments   []CanvasComment
	NextCursor Cursor
	HasMore    bool
}

type CanvasRevisionPage struct {
	Revisions  []CanvasRevision
	NextCursor Cursor
	HasMore    bool
}

// CanvasRevisionLimit bounds how many revisions one canvas keeps. A document
// edited all day would otherwise grow an unbounded table nobody reads past the
// first screen of, and Slack's own history is not infinite either.
const CanvasRevisionLimit = 50

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

// CanvasDocument is the stored shape of a canvas body. It lives here rather
// than beside the editor because the store needs it too: a canvas is searchable
// by its prose, and the folded column that makes it searchable has to be built
// from the same sections the editor writes. Two decoders for one shape is how a
// search index comes to disagree with the document it indexes.
type CanvasDocument struct {
	Sections []CanvasSection `json:"sections"`
}

// CanvasSearchText is the prose a canvas is findable by: its title and the text
// of its sections, and nothing else.
//
// Indexing the stored JSON directly would be simpler and wrong. A member
// searching for "type" would match the key of every section ever written, and a
// member searching for a heading they can see would miss it whenever the
// document held any punctuation JSON escapes. Content that cannot be decoded
// contributes its title alone rather than failing the write: a canvas with a
// body this version cannot read is still a canvas someone should be able to
// find by name.
func CanvasSearchText(title, content string) string {
	var document CanvasDocument
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		return title
	}
	parts := make([]string, 0, len(document.Sections)+1)
	if strings.TrimSpace(title) != "" {
		parts = append(parts, title)
	}
	for _, section := range document.Sections {
		if strings.TrimSpace(section.Text) != "" {
			parts = append(parts, section.Text)
		}
	}
	return strings.Join(parts, "\n")
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
	Metadata        string
	StreamState     string
	ThreadTimestamp MessageTimestamp
	PostAt          time.Time
	CreatedAt       time.Time
	DeliveredAt     time.Time
	FailedAt        time.Time
	FailureCode     string
	FileAttachments []DraftAttachment
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
	Metadata        string
	StreamState     string
	ThreadTimestamp MessageTimestamp
	PostAt          time.Time
	AppID           AppID
	BotID           BotID
	CredentialHash  string
	FileAttachments []DraftAttachment
}

type ScheduledMessageQuery struct {
	CredentialHash string
	Channel        ConversationID
	Oldest         time.Time
	Latest         time.Time
	Page           PageRequest
}

// Draft is private first-party composer state. Conversation and
// ThreadTimestamp form the destination coordinate: a channel composer and each
// thread composer have independent drafts, as Slack exposes them.
type Draft struct {
	WorkspaceID     WorkspaceID
	UserID          UserID
	ConversationID  ConversationID
	ThreadTimestamp MessageTimestamp
	Text            string
	Attachments     []DraftAttachment
	UpdatedAt       time.Time
}

// DraftAttachment is a private reference to an uploaded-but-unshared blob.
// The upload remains owned by the draft's member and is not a workspace File
// until the composer sends it through the ordinary atomic file completion
// path.
type DraftAttachment struct {
	UploadID ExternalUploadID
	Name     string
	Title    string
	MIMEType string
	Size     int64
}

type DraftPage struct {
	Items      []Draft
	NextCursor Cursor
	HasMore    bool
}

// ScheduledMessageUpdate is a first-party client operation, not an invented
// Slack Web API method. Slack's public API updates by delete-plus-schedule;
// SameOldChat's own UI needs one atomic replacement so a failed reschedule
// cannot lose the pending message.
type ScheduledMessageUpdate struct {
	WorkspaceID    WorkspaceID
	ID             ScheduledMessageID
	Channel        ConversationID
	Text           string
	PostAt         time.Time
	CredentialHash string
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
	// InviteRequestAccepted is terminal and is what makes an invitation
	// single-use: the transition to it is the same transaction that creates
	// the member, so a second acceptance finds nothing approved.
	InviteRequestAccepted InviteRequestStatus = "accepted"
	// InviteRequestRevoked is an approved invitation withdrawn before anyone
	// accepted it. Denied is the answer to a request; revoked is the
	// withdrawal of an answer already given, and an administrator reading the
	// record needs to tell those apart.
	InviteRequestRevoked InviteRequestStatus = "revoked"
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
	// ExpiresAt bounds how long the invitation may be accepted for. It is set
	// when the invitation is recorded, not when it is approved, because it is
	// the promise made to the invited person that ages — an invitation that
	// sat in the queue for a month is not fresh because someone finally
	// clicked approve.
	ExpiresAt  time.Time
	AcceptedAt time.Time
	AcceptedBy UserID
}

// InviteRequestAcceptance is everything one acceptance writes. It travels as a
// value because the store applies all of it or none of it.
type InviteRequestAcceptance struct {
	WorkspaceID WorkspaceID
	RequestID   InviteRequestID
	User        User
	Membership  WorkspaceMembership
	Channels    []ConversationID
	AcceptedAt  time.Time
}

// InviteRequestReviewable reports whether an administrator may move an
// invitation from one status to another. Approving and denying answer a
// pending request; revoking withdraws an approval nobody has accepted yet.
// Accepting is not a review — it is the invited person's transition, and it
// only happens inside the transaction that creates the member.
func InviteRequestReviewable(from, to InviteRequestStatus) bool {
	switch {
	case from == InviteRequestPending && (to == InviteRequestApproved || to == InviteRequestDenied):
		return true
	case from == InviteRequestApproved && to == InviteRequestRevoked:
		return true
	default:
		return false
	}
}

// Acceptable reports whether this invitation can still become a member at the
// given instant. Only an approved invitation can be accepted: recording an
// invitation and issuing it are deliberately distinct transitions, so an
// invitation nobody has approved confers nothing.
func (request InviteRequest) Acceptable(at time.Time) bool {
	if request.Status != InviteRequestApproved {
		return false
	}
	return request.ExpiresAt.IsZero() || !at.After(request.ExpiresAt)
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

// AppAuthorization is the non-secret perspective under which an installed app
// may receive Events API traffic. Multiple rotated access tokens can represent
// the same authorization; repositories collapse them by app/workspace/type/
// subject so delivery never duplicates a callback merely because credentials
// rotated.
type AppAuthorization struct {
	AppID       AppID
	WorkspaceID WorkspaceID
	UserID      UserID
	BotID       BotID
	TokenType   string
	Scopes      []string
}

// AppEventCursor is the durable delivery position for one app transport. It is
// intentionally payload-free: administration can explain queue progress and
// retry state without exposing event bodies from installed workspaces.
type AppEventCursor struct {
	AppID                AppID
	Surface              string
	AcknowledgedSequence uint64
	InFlightSequence     uint64
	InFlightUntil        time.Time
	RetryAt              time.Time
	RetryCount           int
	RetryReason          string
}

// AppDeliveryHealth is the developer-facing projection of an app's configured
// event transport and its durable cursor. PendingEvaluation means at least one
// journal record still needs subscription and visibility evaluation; it does
// not incorrectly claim that every pending record will become a callback.
type AppDeliveryHealth struct {
	AppID                AppID
	Surface              string
	Endpoint             string
	Configured           bool
	Installed            bool
	AcknowledgedSequence uint64
	InFlightSequence     uint64
	InFlightUntil        time.Time
	RetryAt              time.Time
	RetryCount           int
	RetryReason          string
	PendingEvaluation    bool
	NextEventTopic       string
	NextEventAt          time.Time
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

type AppDatastoreQuery struct {
	Expression           string
	ExpressionAttributes string
	ExpressionValues     string
	Page                 PageRequest
}

type AppDatastoreQueryPage struct {
	Items      []string
	NextCursor Cursor
	HasMore    bool
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
	// Total is populated by search reads. Ordinary file listings leave it zero.
	Total int
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
	Version           int64
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
	// AssigneeID is who the item belongs to, and DueAt is when it is wanted.
	// Both are columns of their own rather than cells inside Fields: the cells
	// are free-form values a list's own schema names, and these two are asked
	// about by the product itself — who is this for, and is it late — which a
	// value buried in JSON cannot answer without every reader parsing it.
	AssigneeID UserID
	DueAt      time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Archived   bool
	Version    int64
}

// ListItemSummary is the part of an item an Activity row draws.
type ListItemSummary struct {
	ID       ListItemID
	Fields   string
	Archived bool
	DueAt    time.Time
}

// Overdue reports whether this summary is late at the given instant, by the
// same rule the item itself uses.
func (i ListItemSummary) Overdue(now time.Time) bool {
	return !i.Archived && !i.DueAt.IsZero() && i.DueAt.Before(now)
}

// Summary reduces an item to what a row needs.
func (i ListItem) Summary() ListItemSummary {
	return ListItemSummary{ID: i.ID, Fields: i.Fields, Archived: i.Archived, DueAt: i.DueAt}
}

// Overdue reports whether this item is late at the given instant. An archived
// item is never overdue: it has been dealt with, and telling someone that
// finished work is late is noise dressed as urgency.
func (i ListItem) Overdue(now time.Time) bool {
	return !i.Archived && !i.DueAt.IsZero() && i.DueAt.Before(now)
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
	// EditedAt and EditedBy record the last edit. Slack's message object
	// carries an `edited` sub-object, and clients render "(edited)" from it.
	// The edit instant used to live only on the outbox event, so the fact was
	// unavailable to any reader of the message itself and the two could
	// disagree once an event was replayed.
	EditedAt time.Time
	EditedBy UserID
	// Subtype is Slack's message subtype for the messages a workspace
	// generates rather than a person composing them — joins, topic and
	// purpose changes, renames, and /me. An empty subtype is an ordinary
	// message. Subtypes a projection derives from other durable state
	// (file_share, thread_broadcast, bot_message) are deliberately NOT stored
	// here: they are computed where they are read.
	Subtype MessageSubtype
	Deleted bool
	Unfurls map[string]string
	// Files are the durable file shares carried by this message. A file that
	// merely names a channel in its metadata is not visible conversation
	// content; the message relationship supplies ordering, threads, events, API
	// projection, and the first-party timeline.
	Files []File
}

// MessageSubtype is the durable subtype vocabulary for workspace-generated
// messages. Slack renders each of these differently from a composed message:
// no avatar, no author column, and no message actions.
type MessageSubtype string

const (
	MessageSubtypeChannelJoin    MessageSubtype = "channel_join"
	MessageSubtypeChannelLeave   MessageSubtype = "channel_leave"
	MessageSubtypeChannelTopic   MessageSubtype = "channel_topic"
	MessageSubtypeChannelPurpose MessageSubtype = "channel_purpose"
	MessageSubtypeChannelName    MessageSubtype = "channel_name"
	MessageSubtypeMeMessage      MessageSubtype = "me_message"
)

// Valid reports whether a subtype is one this repository writes. An
// unrecognized value is refused at the storage boundary rather than being
// projected to clients as a subtype Slack does not define.
func (s MessageSubtype) Valid() bool {
	switch s {
	case "", MessageSubtypeChannelJoin, MessageSubtypeChannelLeave, MessageSubtypeChannelTopic,
		MessageSubtypeChannelPurpose, MessageSubtypeChannelName, MessageSubtypeMeMessage:
		return true
	}
	return false
}

// System reports whether the subtype marks a message the workspace generated.
// Such a message carries no author identity to render and no actions to take.
func (s MessageSubtype) System() bool {
	return s != "" && s != MessageSubtypeMeMessage
}

// ThreadSummary is what a parent message reports about its replies: how many
// there are, who wrote them, and when the last one landed. Slack renders it
// under the parent as "N replies · last reply …", and it is the reason a
// timeline can show a thread without opening it.
//
// It is computed per parent by one batched query, never by counting a
// per-parent reply page: fifty rendered parents must not become fifty reads.
type ThreadSummary struct {
	ReplyCount   int
	Participants []UserID
	LastReplyAt  time.Time
}

// FollowedThread is one row of Slack's Threads view: a thread the member
// follows, with enough context to decide whether to open it.
//
// Unread is the count of replies after the member's read position in the
// containing conversation. It is deliberately not a separate per-thread cursor:
// the product has one read position per conversation, and inventing a second
// one here would let the Threads view and the conversation disagree about the
// same replies.
type FollowedThread struct {
	Conversation     ConversationID
	ConversationName string
	Root             MessageTimestamp
	RootText         string
	RootAuthorID     UserID
	ReplyCount       int
	UnreadReplies    int
	LastReplyAt      time.Time
}

type FollowedThreadPage struct {
	Threads    []FollowedThread
	NextCursor Cursor
	HasMore    bool
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
	MarkdownText    bool              `json:"markdown_text,omitempty"`
	ReplyBroadcast  bool              `json:"reply_broadcast,omitempty"`
	Parse           string            `json:"parse,omitempty"`
	MrkdwnDisabled  bool              `json:"mrkdwn_disabled,omitempty"`
	LinkNames       bool              `json:"link_names,omitempty"`
	UnfurlLinks     *bool             `json:"unfurl_links,omitempty"`
	UnfurlMedia     *bool             `json:"unfurl_media,omitempty"`
}

// MessagePostRequest is the complete current chat.postMessage payload after
// transport decoding. Keeping it typed prevents the Web API, scheduled worker,
// and first-party composer from silently supporting different message shapes.
type MessagePostRequest struct {
	Conversation    ConversationID
	Text            string
	Blocks          string
	Attachments     string
	Metadata        string
	ThreadTimestamp MessageTimestamp
	IdempotencyKey  string
	AppID           AppID
	MarkdownText    bool
	ReplyBroadcast  bool
	Parse           string
	MrkdwnDisabled  bool
	LinkNames       bool
	UnfurlLinks     *bool
	UnfurlMedia     *bool
	Username        string
	IconEmoji       string
	IconURL         string
	// Subtype marks a message the workspace generated rather than a person
	// composing it. chat.meMessage sets me_message; the conversation
	// lifecycle mutations set their own. A caller cannot choose an arbitrary
	// value — postMessageAs refuses one the vocabulary does not define.
	Subtype MessageSubtype
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

// WorkspaceAnalytics is the shape of the administration dashboard: counts a
// workspace owner needs to answer "how big is this and is it being used",
// derived from the durable rows rather than from a metrics pipeline this
// product does not have.
//
// Every "recent" figure is relative to one instant the caller passes, so the
// dashboard and any export made from the same call describe the same window —
// a store that chose its own window would make two readers disagree.
type WorkspaceAnalytics struct {
	Members          int
	ActiveMembers    int
	Guests           int
	Admins           int
	PublicChannels   int
	PrivateChannels  int
	ArchivedChannels int
	Messages         int
	RecentMessages   int
	Files            int
	RecentFiles      int
	// BusiestChannels are the conversations with the most messages in the
	// window, most first. Direct conversations are excluded: they are private
	// between their members and are not workspace activity an administrator
	// governs.
	BusiestChannels []ChannelActivity
	Since           time.Time
}

type ChannelActivity struct {
	ConversationID ConversationID
	Name           string
	Messages       int
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
	// Cursor is the journal position the stream opens at, captured when
	// rtm.connect issued the ticket.
	//
	// Without it the stream had nowhere to start and began at sequence zero,
	// so an official RTM client — which sends no Last-Event-ID and has no
	// argument to pass one — received the entire workspace journal as live
	// events on every connect. Real Slack sends hello and then only what
	// happens next.
	//
	// It is captured at rtm.connect rather than when the socket opens so the
	// gap between the two loses nothing: whatever is posted while the client
	// is dialling is delivered, exactly once, when it arrives.
	Cursor uint64
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
