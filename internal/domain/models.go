package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type WorkspaceMembership struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	Role        WorkspaceRole
	Active      bool
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
	WorkspaceID    WorkspaceID
	UserID         UserID
	Type           string
	ExternalID     string
	Payload        string
	Hash           string
	RootViewID     ViewID
	PreviousViewID ViewID
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
}

type OAuthToken struct {
	AccessToken  string
	ClientID     string
	AppID        AppID
	WorkspaceID  WorkspaceID
	UserID       UserID
	Scopes       []string
	TokenType    string
	CodeVerifier string
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
	Scopes      []string
	Revoked     bool
}

type AppTokenRecord struct {
	AppID   AppID
	Scopes  []string
	Revoked bool
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
	return base64.RawURLEncoding.EncodeToString(hash[:]) == codeChallenge
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
	CreatedAt   time.Time
	Deleted     bool
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

type ScheduledMessage struct {
	WorkspaceID WorkspaceID
	ID          ScheduledMessageID
	Channel     ConversationID
	Author      UserID
	Text        string
	PostAt      time.Time
	CreatedAt   time.Time
}

type ScheduledMessagePage struct {
	Items      []ScheduledMessage
	NextCursor Cursor
	HasMore    bool
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
	ID              MessageID
	WorkspaceID     WorkspaceID
	Conversation    ConversationID
	AuthorID        UserID
	Text            string
	ThreadTimestamp MessageTimestamp
	CreatedAt       time.Time
	Deleted         bool
	Unfurls         map[string]string
}

func NormalizeUnfurls(values map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for key, raw := range values {
		key = strings.TrimSpace(key)
		if key == "" || len(result) >= 100 || !json.Valid([]byte(raw)) {
			return nil, errors.New("invalid unfurl")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(raw)); err != nil {
			return nil, err
		}
		result[key] = compact.String()
	}
	return result, nil
}

type EphemeralMessage struct {
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	AuthorID     UserID
	RecipientID  UserID
	Text         string
	Timestamp    MessageTimestamp
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
