package memory

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type Store struct {
	mu                            sync.RWMutex
	workspaces                    map[domain.WorkspaceID]domain.Workspace
	members                       map[string]domain.WorkspaceMembership
	users                         map[domain.UserID]domain.User
	userExpirations               map[domain.UserID]time.Time
	conversations                 map[domain.ConversationID]domain.Conversation
	conversationPrefs             map[domain.ConversationID]domain.ConversationPrefs
	conversationAccess            map[domain.ConversationID][]domain.UserGroupID
	conversationTeams             map[domain.ConversationID]map[domain.WorkspaceID]struct{}
	conversationOrg               map[domain.ConversationID]bool
	inviteRequests                map[domain.InviteRequestID]domain.InviteRequest
	appApprovals                  map[domain.AppID]domain.AppApproval
	appInstallations              map[string]domain.AppInstallation
	apps                          map[domain.AppID]domain.App
	appManifestRevisions          map[domain.AppID][]domain.AppManifestRevision
	appTriggers                   map[string]domain.AppTrigger
	appResponseURLs               map[string]domain.AppResponseURL
	appConfigurationTokens        map[string]domain.AppConfigurationToken
	appConfigurationRefreshTokens map[string]string
	permissionRequests            map[domain.AppRequestID]domain.AppPermissionRequest
	views                         map[domain.ViewID]domain.View
	workflowSteps                 map[domain.WorkflowStepID]domain.WorkflowStep
	dialogs                       map[domain.DialogID]domain.Dialog
	bots                          map[domain.BotID]domain.Bot
	migrations                    map[string]domain.UserMigration
	oauthClients                  map[string]domain.OAuthClient
	oauthCodes                    map[string]memoryOAuthCode
	oauthRefreshGrants            map[string]domain.OAuthRefreshGrant
	rtmConnections                map[string]domain.RTMConnection
	socketConnections             map[string]domain.SocketModeConnection
	socketConnectionActive        map[string]bool
	socketResponses               map[string]domain.SocketModeResponse
	socketInteractions            map[string]domain.SocketModeInteraction
	socketCursors                 map[domain.AppID]uint64
	appEventCursors               map[string]memoryAppEventCursor
	memberships                   map[domain.ConversationID]map[domain.UserID]struct{}
	tokens                        map[string]domain.TokenRecord
	appTokens                     map[string]domain.AppTokenRecord
	sessions                      map[string]domain.SessionRecord
	oidcLogoutTokens              map[string]time.Time
	authMethods                   map[string]domain.AuthMethod
	externalIdentities            map[string]domain.ExternalIdentity
	messages                      map[domain.ConversationID][]domain.Message
	ephemeralMessages             []domain.EphemeralMessage
	outbox                        []events.Event
	outboxLeases                  map[uint64]memoryLease
	delivered                     map[uint64]bool
	idempotency                   map[string]domain.MessageID
	nextAttempt                   map[uint64]time.Time
	readCursors                   map[string]domain.ReadCursor
	reactions                     map[domain.MessageID]map[string]domain.Reaction
	pins                          map[domain.MessageID]map[domain.UserID]domain.Pin
	files                         map[domain.FileID]domain.File
	fileComments                  map[domain.FileCommentID]domain.FileComment
	remoteFiles                   map[domain.FileID]domain.RemoteFile
	remoteFileShares              map[domain.FileID][]domain.ConversationID
	dnd                           map[domain.UserID]domain.DoNotDisturb
	stars                         map[domain.UserID]map[domain.MessageID]domain.Star
	savedItems                    map[domain.SavedItemID]domain.SavedItem
	bookmarks                     map[domain.BookmarkID]domain.Bookmark
	reminders                     map[domain.ReminderID]domain.Reminder
	scheduled                     map[domain.ScheduledMessageID]domain.ScheduledMessage
	scheduledLeases               map[domain.ScheduledMessageID]memoryLease
	scheduledDelivered            map[domain.ScheduledMessageID]bool
	scheduledNextAttempt          map[domain.ScheduledMessageID]time.Time
	userGroups                    map[domain.UserGroupID]domain.UserGroup
	calls                         map[domain.CallID]domain.Call
	emojis                        map[string]domain.CustomEmoji
	canvases                      map[domain.CanvasID]domain.Canvas
	canvasAccess                  map[string]domain.CanvasAccess
	accessLogs                    []domain.AccessLog
	lists                         map[domain.ListID]domain.List
	listItems                     map[domain.ListID]map[domain.ListItemID]domain.ListItem
	listAccess                    map[string]domain.ListAccess
	listDownloads                 map[domain.ListDownloadID]domain.ListDownload
	openidRefreshTokens           map[string]domain.OpenIDRefreshToken
	incomingWebhooks              map[domain.IncomingWebhookID]domain.IncomingWebhook
	appDatastoreItems             map[string]domain.AppDatastoreItem
	externalUploads               map[domain.ExternalUploadID]domain.ExternalUpload
	fileShares                    map[domain.FileID][]domain.ConversationID
}

var _ store.Store = (*Store)(nil)

func (s *Store) AppendEvent(_ context.Context, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RecordAccess(_ context.Context, value domain.AccessLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessLogs = append(s.accessLogs, value)
	return nil
}
func (s *Store) ListAccessLogs(_ context.Context, workspace domain.WorkspaceID, before time.Time, limit, page int) ([]domain.AccessLog, bool, error) {
	if limit <= 0 || limit > 1000 || page <= 0 {
		return nil, false, store.InvalidArgument("access log page parameters are invalid")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.AccessLog, 0, limit+1)
	start := (page - 1) * limit
	matched := 0
	for index := len(s.accessLogs) - 1; index >= 0; index-- {
		value := s.accessLogs[index]
		if value.WorkspaceID != workspace || (!before.IsZero() && value.CreatedAt.After(before)) {
			continue
		}
		if matched < start {
			matched++
			continue
		}
		if len(values) == limit+1 {
			break
		}
		values = append(values, value)
		matched++
	}
	if len(values) == 0 {
		return []domain.AccessLog{}, false, nil
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	return values, hasMore, nil
}

type memoryLease struct {
	Owner   string
	Expires time.Time
}

type memoryAppEventCursor struct {
	Sequence       uint64
	LeasedSequence uint64
	LeaseOwner     string
	LeaseUntil     time.Time
	RetryAt        time.Time
	RetryCount     int
	RetryReason    string
}

func New() *Store {
	return &Store{lists: make(map[domain.ListID]domain.List), listItems: make(map[domain.ListID]map[domain.ListItemID]domain.ListItem), listAccess: make(map[string]domain.ListAccess), listDownloads: make(map[domain.ListDownloadID]domain.ListDownload), fileShares: make(map[domain.FileID][]domain.ConversationID), externalUploads: make(map[domain.ExternalUploadID]domain.ExternalUpload), incomingWebhooks: make(map[domain.IncomingWebhookID]domain.IncomingWebhook), appDatastoreItems: make(map[string]domain.AppDatastoreItem), appInstallations: make(map[string]domain.AppInstallation), apps: make(map[domain.AppID]domain.App), appManifestRevisions: make(map[domain.AppID][]domain.AppManifestRevision), appTriggers: make(map[string]domain.AppTrigger), appResponseURLs: make(map[string]domain.AppResponseURL), appConfigurationTokens: make(map[string]domain.AppConfigurationToken), appConfigurationRefreshTokens: make(map[string]string), openidRefreshTokens: make(map[string]domain.OpenIDRefreshToken), workspaces: make(map[domain.WorkspaceID]domain.Workspace), members: make(map[string]domain.WorkspaceMembership), users: make(map[domain.UserID]domain.User), userExpirations: make(map[domain.UserID]time.Time), conversations: make(map[domain.ConversationID]domain.Conversation), conversationPrefs: make(map[domain.ConversationID]domain.ConversationPrefs), conversationAccess: make(map[domain.ConversationID][]domain.UserGroupID), conversationTeams: make(map[domain.ConversationID]map[domain.WorkspaceID]struct{}), conversationOrg: make(map[domain.ConversationID]bool), inviteRequests: make(map[domain.InviteRequestID]domain.InviteRequest), appApprovals: make(map[domain.AppID]domain.AppApproval), permissionRequests: make(map[domain.AppRequestID]domain.AppPermissionRequest), views: make(map[domain.ViewID]domain.View), workflowSteps: make(map[domain.WorkflowStepID]domain.WorkflowStep), dialogs: make(map[domain.DialogID]domain.Dialog), bots: make(map[domain.BotID]domain.Bot), migrations: make(map[string]domain.UserMigration), oauthClients: make(map[string]domain.OAuthClient), oauthCodes: make(map[string]memoryOAuthCode), oauthRefreshGrants: make(map[string]domain.OAuthRefreshGrant), rtmConnections: make(map[string]domain.RTMConnection), socketConnections: make(map[string]domain.SocketModeConnection), socketConnectionActive: make(map[string]bool), socketResponses: make(map[string]domain.SocketModeResponse), socketInteractions: make(map[string]domain.SocketModeInteraction), socketCursors: make(map[domain.AppID]uint64), appEventCursors: make(map[string]memoryAppEventCursor), memberships: make(map[domain.ConversationID]map[domain.UserID]struct{}), tokens: make(map[string]domain.TokenRecord), appTokens: make(map[string]domain.AppTokenRecord), sessions: make(map[string]domain.SessionRecord), oidcLogoutTokens: make(map[string]time.Time), authMethods: make(map[string]domain.AuthMethod), externalIdentities: make(map[string]domain.ExternalIdentity), messages: make(map[domain.ConversationID][]domain.Message), outboxLeases: make(map[uint64]memoryLease), delivered: make(map[uint64]bool), idempotency: make(map[string]domain.MessageID), nextAttempt: make(map[uint64]time.Time), readCursors: make(map[string]domain.ReadCursor), reactions: make(map[domain.MessageID]map[string]domain.Reaction), pins: make(map[domain.MessageID]map[domain.UserID]domain.Pin), files: make(map[domain.FileID]domain.File), fileComments: make(map[domain.FileCommentID]domain.FileComment), remoteFiles: make(map[domain.FileID]domain.RemoteFile), remoteFileShares: make(map[domain.FileID][]domain.ConversationID), dnd: make(map[domain.UserID]domain.DoNotDisturb), stars: make(map[domain.UserID]map[domain.MessageID]domain.Star), savedItems: make(map[domain.SavedItemID]domain.SavedItem), reminders: make(map[domain.ReminderID]domain.Reminder), scheduled: make(map[domain.ScheduledMessageID]domain.ScheduledMessage), scheduledLeases: make(map[domain.ScheduledMessageID]memoryLease), scheduledDelivered: make(map[domain.ScheduledMessageID]bool), scheduledNextAttempt: make(map[domain.ScheduledMessageID]time.Time), userGroups: make(map[domain.UserGroupID]domain.UserGroup), calls: make(map[domain.CallID]domain.Call), emojis: make(map[string]domain.CustomEmoji), bookmarks: make(map[domain.BookmarkID]domain.Bookmark), canvases: make(map[domain.CanvasID]domain.Canvas), canvasAccess: make(map[string]domain.CanvasAccess)}
}

func emojiKey(workspace domain.WorkspaceID, name string) string {
	return string(workspace) + "\x00" + name
}

func canvasAccessKey(value domain.CanvasAccess) string {
	return string(value.CanvasID) + "\x00" + value.EntityType + "\x00" + value.EntityID
}

func (s *Store) CreateCanvas(_ context.Context, canvas domain.Canvas, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[canvas.WorkspaceID]; !ok {
		return store.ErrNotFound
	}
	if user, ok := s.users[canvas.OwnerID]; !ok || user.WorkspaceID != canvas.WorkspaceID {
		return store.ErrNotFound
	}
	if _, exists := s.canvases[canvas.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.canvases[canvas.ID] = canvas
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetCanvas(_ context.Context, workspace domain.WorkspaceID, id domain.CanvasID) (domain.Canvas, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	canvas, ok := s.canvases[id]
	if !ok || canvas.WorkspaceID != workspace {
		return domain.Canvas{}, store.ErrNotFound
	}
	return canvas, nil
}

func (s *Store) UpdateCanvas(_ context.Context, canvas domain.Canvas, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.canvases[canvas.ID]
	if !ok || current.WorkspaceID != canvas.WorkspaceID {
		return store.ErrNotFound
	}
	canvas.CreatedAt = current.CreatedAt
	s.canvases[canvas.ID] = canvas
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteCanvas(_ context.Context, workspace domain.WorkspaceID, id domain.CanvasID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	canvas, ok := s.canvases[id]
	if !ok || canvas.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	delete(s.canvases, id)
	for key, access := range s.canvasAccess {
		if access.CanvasID == id {
			delete(s.canvasAccess, key)
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetCanvasAccess(_ context.Context, access domain.CanvasAccess, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.canvases[access.CanvasID]; !ok {
		return store.ErrNotFound
	}
	s.canvasAccess[canvasAccessKey(access)] = access
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteCanvasAccess(_ context.Context, access domain.CanvasAccess, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.canvases[access.CanvasID]; !ok {
		return store.ErrNotFound
	}
	key := canvasAccessKey(access)
	if _, ok := s.canvasAccess[key]; !ok {
		return store.ErrNotFound
	}
	delete(s.canvasAccess, key)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) AddEmoji(_ context.Context, value domain.CustomEmoji, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := emojiKey(value.WorkspaceID, value.Name)
	if _, exists := s.emojis[key]; exists {
		return store.ErrAlreadyExists
	}
	s.emojis[key] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListEmojis(_ context.Context, workspace domain.WorkspaceID) ([]domain.CustomEmoji, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.CustomEmoji, 0)
	for _, value := range s.emojis {
		if value.WorkspaceID == workspace {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Store) RemoveEmoji(_ context.Context, workspace domain.WorkspaceID, name string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := emojiKey(workspace, name)
	if _, exists := s.emojis[key]; !exists {
		return store.ErrNotFound
	}
	delete(s.emojis, key)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RenameEmoji(_ context.Context, workspace domain.WorkspaceID, oldName, newName string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldKey, newKey := emojiKey(workspace, oldName), emojiKey(workspace, newName)
	value, exists := s.emojis[oldKey]
	if !exists {
		return store.ErrNotFound
	}
	if _, exists := s.emojis[newKey]; exists {
		return store.ErrAlreadyExists
	}
	value.Name = newName
	s.emojis[newKey] = value
	delete(s.emojis, oldKey)
	s.outbox = append(s.outbox, event)
	return nil
}

// The Seed helpers report invalid input rather than panicking or silently doing
// nothing, which is what the SQL repositories do. A rejected workspace
// discoverability used to panic here and return an error there, and a rejected
// presence used to be a silent no-op here, so a seeded fixture diverged between
// storage profiles before any request was served.
func (s *Store) SeedWorkspace(workspace domain.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if workspace.Discoverability == "" {
		workspace.Discoverability = domain.WorkspaceDiscoverabilityOpen
	}
	if !workspace.Discoverability.Valid() {
		return store.InvalidArgument("invalid workspace discoverability")
	}
	s.workspaces[workspace.ID] = workspace
	return nil
}

// SeedUser creates the bootstrap identity and then leaves it alone, matching the
// SQL repositories. Seeding used to replace the whole record, so a second seed
// with an empty e-mail blanked the administrator's address and profile and a
// second seed with Deleted: false undid an administrative deactivation. Only an
// e-mail that is still unset is filled in, because no other writer can attach an
// address to an already seeded identity.
func (s *Store) SeedUser(user domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user.Presence == "" {
		user.Presence = domain.PresenceAuto
	}
	if user.Presence != domain.PresenceAuto && user.Presence != domain.PresenceAway {
		return store.InvalidArgument("invalid user presence")
	}
	user.Email = domain.NormalizeEmail(user.Email)
	// The SQL repositories enforce workspace-scoped e-mail uniqueness through the
	// users_workspace_email_normalized index, so seeding a second bootstrap
	// identity onto one address failed there and silently produced two accounts
	// on one identity here.
	//
	// The other half of that schema guarantee — users.workspace_id REFERENCES
	// workspaces(id) — is deliberately NOT enforced here yet: this helper is
	// called from roughly two hundred and fifty fixtures that never seed a
	// workspace, so making it referential is a repository-wide fixture change
	// rather than a store change. Recorded as a follow-up rather than hidden.
	if user.Email != "" {
		for id, existing := range s.users {
			if id != user.ID && existing.WorkspaceID == user.WorkspaceID && domain.NormalizeEmail(existing.Email) == user.Email {
				return store.ErrAlreadyExists
			}
		}
	}
	if existing, exists := s.users[user.ID]; exists {
		if existing.Email == "" {
			existing.Email = user.Email
			s.users[user.ID] = existing
		}
	} else {
		s.users[user.ID] = user
	}
	key := string(user.WorkspaceID) + "\x00" + string(user.ID)
	if _, exists := s.members[key]; !exists {
		s.members[key] = domain.WorkspaceMembership{WorkspaceID: user.WorkspaceID, UserID: user.ID, Role: domain.WorkspaceRoleMember, Active: true}
	}
	return nil
}

// SeedWorkspaceRole establishes a membership role directly, for fixtures whose
// subject is an administrator. It exists so a test never has to obtain
// administrative authority by calling an administrative mutation with an actor
// that does not hold it — doing that grants fake authority and conceals exactly
// the class of defect the role checks exist to prevent.
func (s *Store) SeedWorkspaceRole(workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return store.InvalidArgument("invalid workspace role")
	}
	key := string(workspaceID) + "\x00" + string(userID)
	membership, ok := s.members[key]
	if !ok {
		return store.ErrNotFound
	}
	membership.Role, membership.Active = role, true
	s.members[key] = membership
	return nil
}

func (s *Store) SeedConversation(conversation domain.Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations[conversation.ID] = conversation
	s.conversationTeams[conversation.ID] = map[domain.WorkspaceID]struct{}{conversation.WorkspaceID: {}}
	s.conversationOrg[conversation.ID] = false
	return nil
}
func (s *Store) SeedConversationMember(conversation domain.ConversationID, user domain.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memberships[conversation] == nil {
		s.memberships[conversation] = make(map[domain.UserID]struct{})
	}
	s.memberships[conversation][user] = struct{}{}
	return nil
}

func (s *Store) SeedToken(_ context.Context, token string, record domain.TokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := domain.HashToken(token)
	if _, exists := s.tokens[key]; exists {
		return nil
	}
	record.Scopes = domain.NormalizeScopes(record.Scopes)
	if strings.TrimSpace(record.TokenType) == "" {
		record.TokenType = "user"
	}
	s.tokens[key] = record
	return nil
}
func (s *Store) LookupToken(_ context.Context, token string) (domain.TokenRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.tokens[domain.HashToken(token)]
	if !ok {
		return domain.TokenRecord{}, store.ErrNotFound
	}
	if expiration, exists := s.userExpirations[record.UserID]; exists && !expiration.IsZero() && !expiration.After(time.Now().UTC()) {
		return domain.TokenRecord{}, store.ErrNotFound
	}
	record.Scopes = append([]string(nil), record.Scopes...)
	return record, nil
}

func (s *Store) SeedAppToken(_ context.Context, token string, record domain.AppTokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := domain.HashToken(token)
	if _, exists := s.appTokens[key]; exists {
		return nil
	}
	if record.AppID == "" {
		return store.InvalidArgument("app token requires an app ID")
	}
	record.Scopes = domain.NormalizeScopes(record.Scopes)
	s.appTokens[key] = record
	return nil
}

func (s *Store) CreateAppToken(_ context.Context, token string, record domain.AppTokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(token) == "" || record.AppID == "" || len(domain.NormalizeScopes(record.Scopes)) == 0 || record.Revoked {
		return store.InvalidArgument("invalid app token")
	}
	app, exists := s.apps[record.AppID]
	if !exists || app.Deleted {
		return store.ErrNotFound
	}
	key := domain.HashToken(token)
	if _, exists := s.appTokens[key]; exists {
		return store.ErrAlreadyExists
	}
	record.Scopes = domain.NormalizeScopes(record.Scopes)
	s.appTokens[key] = record
	return nil
}

func (s *Store) LookupAppToken(_ context.Context, token string) (domain.AppTokenRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.appTokens[domain.HashToken(token)]
	if !ok {
		return domain.AppTokenRecord{}, store.ErrNotFound
	}
	record.Scopes = append([]string(nil), record.Scopes...)
	return record, nil
}

func (s *Store) RevokeToken(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := domain.HashToken(token)
	record, ok := s.tokens[key]
	if !ok {
		return store.ErrNotFound
	}
	record.Revoked = true
	s.tokens[key] = record
	return nil
}

func (s *Store) RevokeAppToken(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := domain.HashToken(token)
	record, ok := s.appTokens[key]
	if !ok {
		return store.ErrNotFound
	}
	record.Revoked = true
	s.appTokens[key] = record
	return nil
}

func (s *Store) SeedSession(_ context.Context, token string, record domain.SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := domain.HashToken(token)
	if _, exists := s.sessions[key]; !exists {
		record.Scopes = domain.NormalizeScopes(record.Scopes)
		s.sessions[key] = record
	}
	return nil
}

func (s *Store) CreateSession(_ context.Context, token string, record domain.SessionRecord) error {
	if strings.TrimSpace(token) == "" || record.WorkspaceID == "" || record.UserID == "" || record.ExpiresAt.IsZero() || !record.ExpiresAt.After(time.Now().UTC()) || len(domain.NormalizeScopes(record.Scopes)) == 0 {
		return store.InvalidArgument("invalid session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workspaces[record.WorkspaceID]; !exists {
		return store.ErrNotFound
	}
	user, exists := s.users[record.UserID]
	if !exists || user.WorkspaceID != record.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	key := domain.HashToken(token)
	if _, exists := s.sessions[key]; exists {
		return store.ErrAlreadyExists
	}
	record.Scopes = domain.NormalizeScopes(record.Scopes)
	s.sessions[key] = record
	return nil
}
func (s *Store) LookupSession(_ context.Context, token string) (domain.SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sessions[domain.HashToken(token)]
	if !ok {
		return domain.SessionRecord{}, store.ErrNotFound
	}
	if expiration, exists := s.userExpirations[record.UserID]; exists && !expiration.IsZero() && !expiration.After(time.Now().UTC()) {
		return domain.SessionRecord{}, store.ErrNotFound
	}
	record.Scopes = append([]string(nil), record.Scopes...)
	return record, nil
}

func authMethodKey(workspace domain.WorkspaceID, provider string) string {
	return string(workspace) + "\x00" + provider
}

// GetAuthMethod reports the stored administrative override for an authorization
// provider. A provider with no row reports Enabled: true and a nil error. See
// store.Store.GetAuthMethod for the decision and the reason: absence means "no
// administrator has turned this provider off", not "no such provider", because
// provider existence is decided by the operator's startup configuration and
// absence-means-disabled locks a fresh deployment out with no bootstrap path.
func (s *Store) GetAuthMethod(_ context.Context, workspace domain.WorkspaceID, provider string) (domain.AuthMethod, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.authMethods[authMethodKey(workspace, provider)]
	if !ok {
		return domain.AuthMethod{WorkspaceID: workspace, Provider: provider, Enabled: true}, nil
	}
	return value, nil
}

func (s *Store) SetAuthMethod(_ context.Context, value domain.AuthMethod) error {
	if value.WorkspaceID == "" || value.Provider == "" {
		return store.InvalidArgument("invalid auth method")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[value.WorkspaceID]; !ok {
		return store.ErrNotFound
	}
	s.authMethods[authMethodKey(value.WorkspaceID, value.Provider)] = value
	return nil
}

func externalIdentityKey(workspace domain.WorkspaceID, provider, subject string) string {
	return string(workspace) + "\x00" + provider + "\x00" + subject
}

func (s *Store) GetExternalIdentity(_ context.Context, workspace domain.WorkspaceID, provider, subject string) (domain.ExternalIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.externalIdentities[externalIdentityKey(workspace, provider, subject)]
	if !ok {
		return domain.ExternalIdentity{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) CreateExternalIdentity(_ context.Context, value domain.ExternalIdentity) error {
	if value.WorkspaceID == "" || value.Provider == "" || value.Subject == "" || value.UserID == "" {
		return store.InvalidArgument("invalid external identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[value.UserID]
	if !ok || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	key := externalIdentityKey(value.WorkspaceID, value.Provider, value.Subject)
	if _, exists := s.externalIdentities[key]; exists {
		return store.ErrAlreadyExists
	}
	s.externalIdentities[key] = value
	return nil
}

// revokeSessionLocked is the only way a session is revoked. Revocation clears the
// provider identity token: a revoked session must retain no provider credential,
// and the identity token left behind was a signed bearer assertion for the user
// that outlived the session it belonged to.
func revokeSessionLocked(record domain.SessionRecord) domain.SessionRecord {
	record.Revoked = true
	record.OIDCIDToken = ""
	return record
}

func (s *Store) RevokeSession(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := domain.HashToken(token)
	record, ok := s.sessions[key]
	if !ok {
		return store.ErrNotFound
	}
	s.sessions[key] = revokeSessionLocked(record)
	return nil
}

func (s *Store) RevokeOIDCSessions(_ context.Context, workspaceID domain.WorkspaceID, provider, subject, sid, tokenID string, expiresAt time.Time, event events.Event) error {
	if workspaceID == "" || strings.TrimSpace(provider) == "" || (strings.TrimSpace(subject) == "" && strings.TrimSpace(sid) == "") || strings.TrimSpace(tokenID) == "" || !expiresAt.After(time.Now().UTC()) {
		return store.ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for key, expiry := range s.oidcLogoutTokens {
		if !expiry.After(now) {
			delete(s.oidcLogoutTokens, key)
		}
	}
	// Expired sessions are dropped on the same schedule as the logout tokens,
	// which is the only maintenance schedule this state has. An expired session
	// is unusable, so retaining its provider metadata serves nothing.
	for key, record := range s.sessions {
		if !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
	tokenKey := string(workspaceID) + "\x00" + provider + "\x00" + tokenID
	if _, exists := s.oidcLogoutTokens[tokenKey]; exists {
		return store.ErrConflict
	}
	s.oidcLogoutTokens[tokenKey] = expiresAt.UTC()
	found := false
	for key, record := range s.sessions {
		if record.WorkspaceID != workspaceID || record.OIDCProvider != provider {
			continue
		}
		if (sid != "" && record.OIDCSID != sid) || (subject != "" && record.OIDCSubject != subject) {
			continue
		}
		s.sessions[key] = revokeSessionLocked(record)
		found = true
	}
	if found {
		s.outbox = append(s.outbox, event)
	}
	return nil
}

func (s *Store) RevokeUserSessions(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for key, record := range s.sessions {
		if record.WorkspaceID == workspaceID && record.UserID == userID {
			s.sessions[key] = revokeSessionLocked(record)
			found = true
		}
	}
	if !found {
		return store.ErrNotFound
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetWorkspace(_ context.Context, id domain.WorkspaceID) (domain.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.workspaces[id]
	if !ok {
		return domain.Workspace{}, store.ErrNotFound
	}
	return cloneWorkspace(value), nil
}

func (s *Store) CreateWorkspace(_ context.Context, value domain.Workspace, event events.Event) error {
	if value.ID == "" || strings.TrimSpace(value.Domain) == "" || strings.TrimSpace(value.Name) == "" || !value.Discoverability.Valid() {
		return store.InvalidArgument("invalid workspace")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workspaces[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.workspaces[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetWorkspaceName(_ context.Context, id domain.WorkspaceID, name string, event events.Event) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.workspaces[id]
	if !ok {
		return domain.Workspace{}, store.ErrNotFound
	}
	value.Name = name
	s.workspaces[id] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetWorkspaceDescription(_ context.Context, id domain.WorkspaceID, description string, event events.Event) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.workspaces[id]
	if !ok {
		return domain.Workspace{}, store.ErrNotFound
	}
	value.Description = description
	s.workspaces[id] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetWorkspaceDiscoverability(_ context.Context, id domain.WorkspaceID, discoverability domain.WorkspaceDiscoverability, event events.Event) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.workspaces[id]
	if !ok {
		return domain.Workspace{}, store.ErrNotFound
	}
	value.Discoverability = discoverability
	s.workspaces[id] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetWorkspaceIcon(_ context.Context, id domain.WorkspaceID, iconURL string, event events.Event) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.workspaces[id]
	if !ok {
		return domain.Workspace{}, store.ErrNotFound
	}
	value.IconURL = iconURL
	s.workspaces[id] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetWorkspaceDefaultChannels(_ context.Context, id domain.WorkspaceID, channels []domain.ConversationID, event events.Event) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.workspaces[id]
	if !ok {
		return domain.Workspace{}, store.ErrNotFound
	}
	for _, channel := range channels {
		conversation, exists := s.conversations[channel]
		if !exists || conversation.WorkspaceID != id || conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect {
			return domain.Workspace{}, store.ErrNotFound
		}
	}
	value.DefaultChannelIDs = append([]domain.ConversationID(nil), channels...)
	s.workspaces[id] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) GetWorkspaceMembership(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceMembership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.members[string(workspaceID)+"\x00"+string(userID)]
	if !ok {
		return domain.WorkspaceMembership{}, store.ErrNotFound
	}
	return value, nil
}
func (s *Store) GetUser(_ context.Context, id domain.UserID) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.users[id]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) CreateUser(_ context.Context, user domain.User, membership domain.WorkspaceMembership, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user.ID == "" || user.WorkspaceID == "" || user.Email == "" || user.Name == "" || membership.WorkspaceID != user.WorkspaceID || membership.UserID != user.ID || !membership.Active {
		return store.InvalidArgument("user and active workspace membership are required")
	}
	if membership.Role != domain.WorkspaceRoleMember && membership.Role != domain.WorkspaceRoleAdmin {
		return store.InvalidArgument("user membership role must be member or admin")
	}
	if _, exists := s.workspaces[user.WorkspaceID]; !exists {
		return store.ErrNotFound
	}
	if _, exists := s.users[user.ID]; exists {
		return store.ErrAlreadyExists
	}
	// domain.NormalizeEmail, never strings.EqualFold. EqualFold applies full
	// Unicode simple folding, under which U+017F (ſ) folds onto 's': this
	// repository resolved "ſmith@x.test" to the account owning "smith@x.test"
	// while every SQL profile kept them apart, so an attacker who could name an
	// address at an identity provider took over the folded-onto account on the
	// memory profile alone.
	user.Email = domain.NormalizeEmail(user.Email)
	for _, existing := range s.users {
		if existing.WorkspaceID == user.WorkspaceID && domain.NormalizeEmail(existing.Email) == user.Email {
			return store.ErrAlreadyExists
		}
	}
	if user.Presence == "" {
		user.Presence = domain.PresenceAuto
	}
	s.users[user.ID] = user
	s.members[string(user.WorkspaceID)+"\x00"+string(user.ID)] = membership
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) FindUserByEmail(_ context.Context, workspace domain.WorkspaceID, email string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// See the note in CreateUser: this comparison decides which account an
	// identity provider's asserted address resolves to, so it must use the one
	// canonical form and must not case-fold beyond it.
	normalized := domain.NormalizeEmail(email)
	for _, user := range s.users {
		if user.WorkspaceID == workspace && !user.Deleted && domain.NormalizeEmail(user.Email) == normalized {
			return user, nil
		}
	}
	return domain.User{}, store.ErrNotFound
}

// UpdateUserProfile commits the profile change and every event given with it as
// one unit. See store.Store.UpdateUserProfile.
func (s *Store) UpdateUserProfile(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, profile domain.UserProfile, changes ...events.Event) (domain.User, error) {
	if len(changes) == 0 {
		return domain.User{}, store.InvalidArgument("a profile change requires at least one event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.User{}, store.ErrNotFound
	}
	key := string(workspaceID) + "\x00" + string(userID)
	membership, ok := s.members[key]
	if !ok || !membership.Active {
		return domain.User{}, store.ErrNotFound
	}
	user.Profile = profile
	s.users[userID] = user
	s.outbox = append(s.outbox, changes...)
	return user, nil
}

func (s *Store) SetUserPresence(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, presence domain.Presence, event events.Event) (domain.User, error) {
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return domain.User{}, store.InvalidArgument("invalid user presence")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.User{}, store.ErrNotFound
	}
	// An inactive membership must not be able to change presence, which is what
	// the SQL repositories enforce with AND active = 1.
	membership, ok := s.members[string(workspaceID)+"\x00"+string(userID)]
	if !ok || !membership.Active {
		return domain.User{}, store.ErrNotFound
	}
	user.Presence = presence
	s.users[userID] = user
	s.outbox = append(s.outbox, event)
	return user, nil
}

func (s *Store) SetUserExpiration(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, expiration time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if expiration.IsZero() {
		delete(s.userExpirations, userID)
	} else {
		s.userExpirations[userID] = expiration.UTC()
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetUserDeleted(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, deleted bool, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	user.Deleted = deleted
	s.users[userID] = user
	key := string(workspaceID) + "\x00" + string(userID)
	membership, exists := s.members[key]
	if exists {
		membership.Active = !deleted
		s.members[key] = membership
	}
	if deleted {
		for tokenKey, token := range s.tokens {
			if token.WorkspaceID == workspaceID && token.UserID == userID {
				token.Revoked = true
				s.tokens[tokenKey] = token
			}
		}
		for sessionKey, session := range s.sessions {
			if session.WorkspaceID == workspaceID && session.UserID == userID {
				s.sessions[sessionKey] = revokeSessionLocked(session)
			}
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) AssignUser(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channels []domain.ConversationID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	key := string(workspaceID) + "\x00" + string(userID)
	membership, ok := s.members[key]
	if !ok {
		return store.ErrNotFound
	}
	for _, channelID := range channels {
		conversation, exists := s.conversations[channelID]
		if !exists || conversation.WorkspaceID != workspaceID || conversation.IsDirect {
			return store.ErrNotFound
		}
	}
	user.Deleted = false
	s.users[userID] = user
	membership.Active = true
	s.members[key] = membership
	for _, channelID := range channels {
		members := s.memberships[channelID]
		if members == nil {
			members = make(map[domain.UserID]struct{})
		}
		members[userID] = struct{}{}
		s.memberships[channelID] = members
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetWorkspaceRole(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return store.InvalidArgument("invalid workspace role")
	}
	key := string(workspaceID) + "\x00" + string(userID)
	membership, ok := s.members[key]
	if !ok {
		return store.ErrNotFound
	}
	membership.Role, membership.Active = role, true
	s.members[key] = membership
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetDoNotDisturb(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.DoNotDisturb, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.DoNotDisturb{}, store.ErrNotFound
	}
	value := s.dnd[userID]
	if value.UserID == "" {
		value = domain.DoNotDisturb{WorkspaceID: workspaceID, UserID: userID}
	}
	return value, nil
}

func (s *Store) SetDoNotDisturb(_ context.Context, value domain.DoNotDisturb, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[value.UserID]
	if !ok || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	s.dnd[value.UserID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func appendSorted[T any](values []T, value T, capacity int, less func(T, T) bool) []T {
	index := sort.Search(len(values), func(index int) bool { return less(value, values[index]) })
	values = append(values, value)
	copy(values[index+1:], values[index:len(values)-1])
	values[index] = value
	if len(values) > capacity {
		values = values[:capacity]
	}
	return values
}

func messageBefore(left, right domain.Message) bool {
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID < right.ID
	}
	return left.CreatedAt.Before(right.CreatedAt)
}

func (s *Store) ListUsers(_ context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.UserPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	s.mu.RLock()
	values := make([]domain.User, 0, request.Limit+1)
	for _, user := range s.users {
		if user.WorkspaceID == workspace && (after == "" || string(user.ID) > after) {
			values = appendSorted(values, user, request.Limit+1, func(left, right domain.User) bool { return left.ID < right.ID })
		}
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.UserPage{Users: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.UserPage{}, err
		}
	}
	return page, nil
}

func (s *Store) ListAdminUsers(_ context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.AdminUserPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.AdminUserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.AdminUserPage{}, err
	}
	s.mu.RLock()
	values := make([]domain.AdminUser, 0, request.Limit+1)
	for _, membership := range s.members {
		if membership.WorkspaceID != workspace || (after != "" && string(membership.UserID) <= after) {
			continue
		}
		user, ok := s.users[membership.UserID]
		if !ok || user.WorkspaceID != workspace {
			continue
		}
		values = appendSorted(values, domain.AdminUser{User: user, Membership: membership}, request.Limit+1, func(left, right domain.AdminUser) bool { return left.User.ID < right.User.ID })
	}
	s.mu.RUnlock()
	page := domain.AdminUserPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	page.Users = values
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].User.ID))
	}
	return page, err
}

func (s *Store) ListUsersByRole(_ context.Context, workspace domain.WorkspaceID, role domain.WorkspaceRole, request domain.PageRequest) (domain.UserPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.User, 0, request.Limit+1)
	for _, membership := range s.members {
		if membership.WorkspaceID != workspace || membership.Role != role || !membership.Active || string(membership.UserID) <= after {
			continue
		}
		user, ok := s.users[membership.UserID]
		if !ok || user.Deleted {
			continue
		}
		values = appendSorted(values, user, request.Limit+1, func(left, right domain.User) bool { return left.ID < right.ID })
	}
	page := domain.UserPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	page.Users = values
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) ListConversationMembers(_ context.Context, conversation domain.ConversationID, request domain.PageRequest) (domain.UserPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	s.mu.RLock()
	if _, exists := s.conversations[conversation]; !exists {
		s.mu.RUnlock()
		return domain.UserPage{}, store.ErrNotFound
	}
	memberIDs := s.memberships[conversation]
	values := make([]domain.User, 0, request.Limit+1)
	for userID := range memberIDs {
		if after != "" && string(userID) <= after {
			continue
		}
		user, userExists := s.users[userID]
		if !userExists || user.Deleted {
			continue
		}
		values = appendSorted(values, user, request.Limit+1, func(left, right domain.User) bool { return left.ID < right.ID })
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.UserPage{Users: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.UserPage{}, err
		}
	}
	return page, nil
}
func (s *Store) GetConversation(_ context.Context, id domain.ConversationID) (domain.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.conversations[id]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) FindDirectConversation(_ context.Context, workspaceID domain.WorkspaceID, members []domain.UserID) (domain.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wanted := make(map[domain.UserID]struct{}, len(members))
	for _, member := range members {
		if _, exists := wanted[member]; exists {
			return domain.Conversation{}, store.ErrNotFound
		}
		wanted[member] = struct{}{}
	}
	for id, conversation := range s.conversations {
		if conversation.WorkspaceID != workspaceID || (!conversation.IsDirect && !conversation.IsGroupDirect) {
			continue
		}
		current := s.memberships[id]
		if len(current) != len(wanted) {
			continue
		}
		matched := true
		for member := range wanted {
			if _, ok := current[member]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return conversation, nil
		}
	}
	return domain.Conversation{}, store.ErrNotFound
}

func (s *Store) CreateDirectConversation(_ context.Context, conversation domain.Conversation, members []domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.conversations[conversation.ID]; exists {
		return store.ErrAlreadyExists
	}
	if !conversation.IsPrivate || (!conversation.IsDirect && !conversation.IsGroupDirect) || len(members) < 2 {
		return store.InvalidArgument("invalid direct conversation")
	}
	wantedKey := domain.DirectConversationKey(conversation.WorkspaceID, members)
	for id, existing := range s.conversations {
		if existing.WorkspaceID != conversation.WorkspaceID || (!existing.IsDirect && !existing.IsGroupDirect) {
			continue
		}
		currentMembers := make([]domain.UserID, 0, len(s.memberships[id]))
		for member := range s.memberships[id] {
			currentMembers = append(currentMembers, member)
		}
		if domain.DirectConversationKey(existing.WorkspaceID, currentMembers) == wantedKey {
			return store.ErrAlreadyExists
		}
	}
	memberSet := make(map[domain.UserID]struct{}, len(members))
	for _, member := range members {
		if _, duplicate := memberSet[member]; duplicate {
			return store.InvalidArgument("direct conversation contains duplicate members")
		}
		user, exists := s.users[member]
		if !exists || user.WorkspaceID != conversation.WorkspaceID || user.Deleted {
			return store.ErrNotFound
		}
		memberSet[member] = struct{}{}
	}
	s.conversations[conversation.ID] = conversation
	s.memberships[conversation.ID] = memberSet
	s.conversationTeams[conversation.ID] = map[domain.WorkspaceID]struct{}{conversation.WorkspaceID: {}}
	s.conversationOrg[conversation.ID] = false
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateConversation(_ context.Context, conversation domain.Conversation, creator domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.conversations[conversation.ID]; exists {
		return store.ErrAlreadyExists
	}
	if !conversation.IsDirect && !conversation.IsGroupDirect {
		for _, existing := range s.conversations {
			if existing.WorkspaceID == conversation.WorkspaceID && !existing.IsDirect && !existing.IsGroupDirect && existing.Name == conversation.Name {
				return store.ErrAlreadyExists
			}
		}
	}
	s.conversations[conversation.ID] = conversation
	s.conversationTeams[conversation.ID] = map[domain.WorkspaceID]struct{}{conversation.WorkspaceID: {}}
	s.conversationOrg[conversation.ID] = false
	// The creator joins the conversation, public or private. Joining only on
	// private conversations was invisible while membership was consulted only to
	// decide whether a private conversation could be read; once membership
	// governed writing, it meant a caller could create a public channel and then
	// be refused permission to rename the channel they had just made.
	if s.memberships[conversation.ID] == nil {
		s.memberships[conversation.ID] = make(map[domain.UserID]struct{})
	}
	s.memberships[conversation.ID][creator] = struct{}{}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RenameConversation(_ context.Context, conversation domain.ConversationID, name string, event events.Event) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	if !value.IsDirect && !value.IsGroupDirect {
		for id, existing := range s.conversations {
			if id != conversation && existing.WorkspaceID == value.WorkspaceID && !existing.IsDirect && !existing.IsGroupDirect && existing.Name == name {
				return domain.Conversation{}, store.ErrAlreadyExists
			}
		}
	}
	value.Name = name
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetConversationTopic(_ context.Context, conversation domain.ConversationID, topic string, event events.Event) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	value.Topic = topic
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetConversationPurpose(_ context.Context, conversation domain.ConversationID, purpose string, event events.Event) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	value.Purpose = purpose
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) SetConversationArchived(_ context.Context, conversation domain.ConversationID, archived bool, event events.Event) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	value.Archived = archived
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) DeleteConversation(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if value.IsDirect || value.IsGroupDirect {
		return store.ErrInvalidConversationType
	}
	for _, message := range s.messages[conversation] {
		delete(s.reactions, message.ID)
		delete(s.pins, message.ID)
		for userID, stars := range s.stars {
			delete(stars, message.ID)
			if len(stars) == 0 {
				delete(s.stars, userID)
			}
		}
	}
	delete(s.messages, conversation)
	delete(s.memberships, conversation)
	delete(s.conversationPrefs, conversation)
	delete(s.conversationAccess, conversation)
	for key := range s.readCursors {
		if strings.HasSuffix(key, "\x00"+string(conversation)) {
			delete(s.readCursors, key)
		}
	}
	for id, scheduled := range s.scheduled {
		if scheduled.Channel == conversation {
			delete(s.scheduled, id)
			delete(s.scheduledLeases, id)
			delete(s.scheduledDelivered, id)
			delete(s.scheduledNextAttempt, id)
		}
	}
	for id, channels := range s.remoteFileShares {
		filtered := make([]domain.ConversationID, 0, len(channels))
		for _, channel := range channels {
			if channel != conversation {
				filtered = append(filtered, channel)
			}
		}
		s.remoteFileShares[id] = filtered
		if remote, exists := s.remoteFiles[id]; exists {
			remote.SharedChannels = filtered
			s.remoteFiles[id] = remote
		}
	}
	for id, channels := range s.fileShares {
		filtered := make([]domain.ConversationID, 0, len(channels))
		for _, channel := range channels {
			if channel != conversation {
				filtered = append(filtered, channel)
			}
		}
		s.fileShares[id] = filtered
	}
	for id, group := range s.userGroups {
		filtered := make([]domain.ConversationID, 0, len(group.Channels))
		for _, channel := range group.Channels {
			if channel != conversation {
				filtered = append(filtered, channel)
			}
		}
		group.Channels = filtered
		s.userGroups[id] = group
	}
	for id, workspaceValue := range s.workspaces {
		filtered := make([]domain.ConversationID, 0, len(workspaceValue.DefaultChannelIDs))
		for _, channel := range workspaceValue.DefaultChannelIDs {
			if channel != conversation {
				filtered = append(filtered, channel)
			}
		}
		workspaceValue.DefaultChannelIDs = filtered
		s.workspaces[id] = workspaceValue
	}
	delete(s.conversations, conversation)
	delete(s.conversationTeams, conversation)
	delete(s.conversationOrg, conversation)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetConversationAccessGroups(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, groups []domain.UserGroupID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	for _, groupID := range groups {
		group, exists := s.userGroups[groupID]
		if !exists || group.WorkspaceID != workspace || !group.DeletedAt.IsZero() {
			return store.ErrNotFound
		}
	}
	s.conversationAccess[conversation] = append([]domain.UserGroupID(nil), groups...)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListConversationAccessGroups(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.UserGroupID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.conversations[conversation]
	if !ok || value.WorkspaceID != workspace {
		return nil, store.ErrNotFound
	}
	return append([]domain.UserGroupID(nil), s.conversationAccess[conversation]...), nil
}

func (s *Store) CreateInviteRequest(_ context.Context, value domain.InviteRequest, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" || value.WorkspaceID == "" || strings.TrimSpace(value.Email) == "" || value.Status != domain.InviteRequestPending {
		return store.ErrAlreadyExists
	}
	if _, exists := s.inviteRequests[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.inviteRequests[value.ID] = cloneInviteRequest(value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetInviteRequest(_ context.Context, workspace domain.WorkspaceID, id domain.InviteRequestID) (domain.InviteRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.inviteRequests[id]
	if !ok || value.WorkspaceID != workspace {
		return domain.InviteRequest{}, store.ErrNotFound
	}
	return cloneInviteRequest(value), nil
}

func (s *Store) SetInviteRequestStatus(_ context.Context, workspace domain.WorkspaceID, id domain.InviteRequestID, status domain.InviteRequestStatus, reviewedAt time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.inviteRequests[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if value.Status != domain.InviteRequestPending || (status != domain.InviteRequestApproved && status != domain.InviteRequestDenied) {
		return store.ErrInvalidInviteRequest
	}
	value.Status = status
	value.ReviewedAt = reviewedAt.UTC()
	s.inviteRequests[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListInviteRequests(_ context.Context, workspace domain.WorkspaceID, status domain.InviteRequestStatus, request domain.PageRequest) (domain.InviteRequestPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.InviteRequestPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.InviteRequestPage{}, err
	}
	s.mu.RLock()
	values := make([]domain.InviteRequest, 0, request.Limit+1)
	for _, value := range s.inviteRequests {
		if value.WorkspaceID == workspace && value.Status == status && string(value.ID) > after {
			values = appendSorted(values, cloneInviteRequest(value), request.Limit+1, func(left, right domain.InviteRequest) bool { return left.ID < right.ID })
		}
	}
	s.mu.RUnlock()
	page := domain.InviteRequestPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	page.Requests = values
	return page, err
}

func validAppApprovalStatus(status domain.AppApprovalStatus) bool {
	return status == domain.AppApprovalRequested || status == domain.AppApprovalApproved || status == domain.AppApprovalRestricted
}

func appInstallationKey(appID domain.AppID, workspaceID domain.WorkspaceID) string {
	return string(appID) + "\x00" + string(workspaceID)
}

func (s *Store) CreateAppInstallation(_ context.Context, value domain.AppInstallation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appInstallations == nil {
		s.appInstallations = make(map[string]domain.AppInstallation)
	}
	if value.AppID == "" || value.WorkspaceID == "" || value.CreatedAt.IsZero() {
		return store.ErrInvalidAppApproval
	}
	key := appInstallationKey(value.AppID, value.WorkspaceID)
	if existing, ok := s.appInstallations[key]; ok {
		if existing.Enabled == value.Enabled {
			return nil
		}
		if !existing.Enabled && value.Enabled {
			existing.CreatedAt = value.CreatedAt
		}
		existing.Enabled = value.Enabled
		s.appInstallations[key] = existing
		return nil
	}
	s.appInstallations[key] = value
	return nil
}

func (s *Store) ListAppInstallations(_ context.Context, appID domain.AppID) ([]domain.AppInstallation, error) {
	if appID == "" {
		return nil, store.ErrInvalidAppApproval
	}
	s.mu.RLock()
	values := make([]domain.AppInstallation, 0)
	for _, value := range s.appInstallations {
		if value.AppID == appID && value.Enabled {
			values = append(values, value)
		}
	}
	s.mu.RUnlock()
	slices.SortFunc(values, func(left, right domain.AppInstallation) int {
		return strings.Compare(string(left.WorkspaceID), string(right.WorkspaceID))
	})
	return values, nil
}

func (s *Store) UninstallApp(_ context.Context, workspaceID domain.WorkspaceID, appID domain.AppID) error {
	if workspaceID == "" || appID == "" {
		return store.InvalidArgument("app installation identity is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := appInstallationKey(appID, workspaceID)
	installation, exists := s.appInstallations[key]
	if !exists || !installation.Enabled {
		return store.ErrNotFound
	}
	installation.Enabled = false
	s.appInstallations[key] = installation
	for key, token := range s.tokens {
		if token.WorkspaceID == workspaceID && token.AppID == appID {
			token.Revoked = true
			s.tokens[key] = token
		}
	}
	for key, webhook := range s.incomingWebhooks {
		if webhook.WorkspaceID == workspaceID && webhook.AppID == appID {
			webhook.Enabled = false
			s.incomingWebhooks[key] = webhook
		}
	}
	for key, bot := range s.bots {
		if bot.WorkspaceID == workspaceID && bot.AppID == appID {
			bot.Deleted = true
			s.bots[key] = bot
		}
	}
	for key, item := range s.appDatastoreItems {
		if item.WorkspaceID == workspaceID && item.AppID == appID {
			delete(s.appDatastoreItems, key)
		}
	}
	return nil
}

func (s *Store) SetAppApproval(_ context.Context, workspace domain.WorkspaceID, appID domain.AppID, requestID domain.AppRequestID, status domain.AppApprovalStatus, updatedAt time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(string(workspace)) == "" || strings.TrimSpace(string(appID)) == "" || !validAppApprovalStatus(status) {
		return store.ErrInvalidAppApproval
	}
	value, exists := s.appApprovals[appID]
	if exists && value.WorkspaceID != workspace {
		return store.ErrInvalidAppApproval
	}
	if exists && value.RequestID != "" && requestID != "" && value.RequestID != requestID {
		return store.ErrInvalidAppApproval
	}
	if !exists {
		value = domain.AppApproval{ID: appID, RequestID: requestID, WorkspaceID: workspace, CreatedAt: updatedAt.UTC()}
	}
	value.RequestID = requestID
	value.Status = status
	value.UpdatedAt = updatedAt.UTC()
	s.appApprovals[appID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListAppApprovals(_ context.Context, workspace domain.WorkspaceID, status domain.AppApprovalStatus, request domain.PageRequest) (domain.AppApprovalPage, error) {
	if !validAppApprovalStatus(status) {
		return domain.AppApprovalPage{}, store.ErrInvalidAppApproval
	}
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.AppApprovalPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.AppApprovalPage{}, err
	}
	s.mu.RLock()
	values := make([]domain.AppApproval, 0, request.Limit+1)
	for _, value := range s.appApprovals {
		if value.WorkspaceID == workspace && value.Status == status && string(value.ID) > after {
			values = appendSorted(values, value, request.Limit+1, func(left, right domain.AppApproval) bool { return left.ID < right.ID })
		}
	}
	s.mu.RUnlock()
	page := domain.AppApprovalPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	page.Apps = values
	return page, err
}

func (s *Store) CreateAppPermissionRequest(_ context.Context, value domain.AppPermissionRequest, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.RequesterID == "" || value.TargetUserID == "" || value.TriggerID == "" || len(value.Scopes) == 0 {
		return store.InvalidArgument("invalid app permission request")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.permissionRequests[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	value.Scopes = domain.NormalizeScopes(value.Scopes)
	s.permissionRequests[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateView(_ context.Context, value domain.View, event events.Event) error {
	if value.ID == "" || value.AppID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Type == "" || value.Payload == "" || value.Hash == "" || value.CreatedAt.IsZero() {
		return store.InvalidArgument("invalid view")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.views[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	for _, existing := range s.views {
		if existing.WorkspaceID == value.WorkspaceID && value.ExternalID != "" && existing.ExternalID == value.ExternalID {
			return store.ErrAlreadyExists
		}
	}
	s.views[value.ID] = cloneView(value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetView(_ context.Context, workspace domain.WorkspaceID, id domain.ViewID) (domain.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.views[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.View{}, store.ErrNotFound
	}
	return cloneView(value), nil
}

func (s *Store) GetViewByExternalID(_ context.Context, workspace domain.WorkspaceID, appID domain.AppID, externalID string) (domain.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.views {
		if value.WorkspaceID == workspace && value.AppID == appID && value.ExternalID == externalID {
			return cloneView(value), nil
		}
	}
	return domain.View{}, store.ErrNotFound
}

func (s *Store) GetPublishedView(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, appID domain.AppID) (domain.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found domain.View
	for _, value := range s.views {
		if value.WorkspaceID == workspace && value.UserID == user && value.AppID == appID && value.Type == "home" && (found.ID == "" || value.UpdatedAt.After(found.UpdatedAt)) {
			found = value
		}
	}
	if found.ID == "" {
		return domain.View{}, store.ErrNotFound
	}
	return cloneView(found), nil
}

func (s *Store) GetLatestView(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, appID domain.AppID, viewType string) (domain.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found domain.View
	for _, value := range s.views {
		after := value.UpdatedAt.After(found.UpdatedAt)
		if viewType == "modal" {
			after = value.CreatedAt.After(found.CreatedAt)
		}
		if value.WorkspaceID == workspace && value.UserID == user && value.AppID == appID && value.Type == viewType && (found.ID == "" || after) {
			found = value
		}
	}
	if found.ID == "" {
		return domain.View{}, store.ErrNotFound
	}
	return cloneView(found), nil
}

func (s *Store) GetCurrentView(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, viewType string) (domain.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found domain.View
	for _, value := range s.views {
		after := value.UpdatedAt.After(found.UpdatedAt)
		if viewType == "modal" {
			after = value.CreatedAt.After(found.CreatedAt)
		}
		if value.WorkspaceID == workspace && value.UserID == user && value.Type == viewType && (found.ID == "" || after) {
			found = value
		}
	}
	if found.ID == "" {
		return domain.View{}, store.ErrNotFound
	}
	return cloneView(found), nil
}

func (s *Store) UpdateView(_ context.Context, value domain.View, expectedHash string, event events.Event) (domain.View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.views[value.ID]
	if !exists || current.WorkspaceID != value.WorkspaceID {
		return domain.View{}, store.ErrNotFound
	}
	if current.AppID != value.AppID {
		return domain.View{}, store.ErrNotFound
	}
	if expectedHash != "" && current.Hash != expectedHash {
		return domain.View{}, store.ErrConflict
	}
	if value.AppID == "" || value.Payload == "" || value.Hash == "" {
		return domain.View{}, store.InvalidArgument("invalid view")
	}
	if value.ExternalID != "" {
		for candidateID, candidate := range s.views {
			if candidateID != value.ID && candidate.WorkspaceID == value.WorkspaceID && candidate.ExternalID == value.ExternalID {
				return domain.View{}, store.ErrAlreadyExists
			}
		}
	}
	value.CreatedAt = current.CreatedAt
	value.UpdatedAt = value.UpdatedAt.UTC()
	if value.UserID == "" {
		value.UserID = current.UserID
	}
	if value.RootViewID == "" {
		value.RootViewID = current.RootViewID
	}
	s.views[value.ID] = cloneView(value)
	s.outbox = append(s.outbox, event)
	return cloneView(value), nil
}

func cloneView(value domain.View) domain.View {
	if value.Errors != nil {
		value.Errors = maps.Clone(value.Errors)
	}
	return value
}

func (s *Store) DeleteView(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ViewID, clear bool, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.views[id]
	if !exists || current.WorkspaceID != workspace || current.UserID != user {
		return store.ErrNotFound
	}
	if clear {
		for candidateID, candidate := range s.views {
			if candidate.WorkspaceID == workspace && candidate.UserID == user && candidate.AppID == current.AppID && candidate.RootViewID == current.RootViewID {
				delete(s.views, candidateID)
			}
		}
	} else {
		delete(s.views, id)
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetWorkflowStep(_ context.Context, value domain.WorkflowStep, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Status == "" || value.UpdatedAt.IsZero() {
		return store.InvalidArgument("invalid workflow step")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.workflowSteps[value.ID]; exists {
		if current.WorkspaceID != value.WorkspaceID {
			return store.ErrNotFound
		}
		value.CreatedAt = current.CreatedAt
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = value.UpdatedAt
	}
	s.workflowSteps[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetWorkflowStep(_ context.Context, workspace domain.WorkspaceID, id domain.WorkflowStepID) (domain.WorkflowStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.workflowSteps[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.WorkflowStep{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) CreateDialog(_ context.Context, value domain.Dialog, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Payload == "" || value.CreatedAt.IsZero() {
		return store.InvalidArgument("invalid dialog")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.dialogs[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.dialogs[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetDialog(_ context.Context, workspace domain.WorkspaceID, id domain.DialogID) (domain.Dialog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.dialogs[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.Dialog{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) CreateBot(_ context.Context, value domain.Bot) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Name == "" || value.UpdatedAt.IsZero() {
		return store.InvalidArgument("invalid bot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.bots[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.bots[value.ID] = value
	return nil
}

func (s *Store) GetBot(_ context.Context, workspace domain.WorkspaceID, id domain.BotID) (domain.Bot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.bots[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.Bot{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) GetBotByApp(_ context.Context, workspace domain.WorkspaceID, appID domain.AppID) (domain.Bot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.bots {
		if value.WorkspaceID == workspace && value.AppID == appID && !value.Deleted {
			return value, nil
		}
	}
	return domain.Bot{}, store.ErrNotFound
}

func migrationKey(workspace domain.WorkspaceID, id domain.UserID) string {
	return string(workspace) + "\x00" + string(id)
}

func (s *Store) CreateUserMigration(_ context.Context, value domain.UserMigration, event events.Event) error {
	if value.WorkspaceID == "" || value.OldID == "" || value.GlobalID == "" {
		return store.InvalidArgument("invalid user migration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.migrations {
		if existing.WorkspaceID == value.WorkspaceID && (existing.OldID == value.OldID || existing.GlobalID == value.GlobalID) {
			return store.ErrAlreadyExists
		}
	}
	s.migrations[migrationKey(value.WorkspaceID, value.OldID)] = value
	s.migrations[migrationKey(value.WorkspaceID, value.GlobalID)] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) FindUserMigration(_ context.Context, workspace domain.WorkspaceID, id domain.UserID) (domain.UserMigration, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.migrations[migrationKey(workspace, id)]
	if !exists {
		return domain.UserMigration{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) SetConversationTeams(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, teams []domain.WorkspaceID, orgChannel bool, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.conversations[conversation]
	if !exists || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	set := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, team := range teams {
		if team == "" {
			return store.InvalidArgument("invalid conversation team")
		}
		if _, exists := s.workspaces[team]; !exists {
			return store.ErrNotFound
		}
		set[team] = struct{}{}
	}
	if len(set) == 0 && !orgChannel {
		return store.InvalidArgument("conversation team association is empty")
	}
	s.conversationTeams[conversation] = set
	s.conversationOrg[conversation] = orgChannel
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListConversationTeams(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.WorkspaceID, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.conversations[conversation]
	if !exists || value.WorkspaceID != workspace {
		return nil, false, store.ErrNotFound
	}
	teams := make([]domain.WorkspaceID, 0, len(s.conversationTeams[conversation]))
	for team := range s.conversationTeams[conversation] {
		teams = append(teams, team)
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i] < teams[j] })
	return teams, s.conversationOrg[conversation], nil
}

func (s *Store) DisconnectConversationTeams(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, leaving []domain.WorkspaceID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.conversations[conversation]
	if !exists || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	set := s.conversationTeams[conversation]
	if len(leaving) == 0 {
		delete(s.conversationTeams, conversation)
		s.conversationOrg[conversation] = false
	} else {
		for _, team := range leaving {
			delete(set, team)
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListConnectedChannelInfo(_ context.Context, workspace domain.WorkspaceID, channels []domain.ConversationID, teams []domain.WorkspaceID, request domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, false, "", err
	}
	if _, err := domain.DecodeListCursor(request.Cursor); err != nil {
		return nil, false, "", err
	}
	channelFilter := make(map[domain.ConversationID]struct{}, len(channels))
	for _, channel := range channels {
		channelFilter[channel] = struct{}{}
	}
	teamFilter := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, team := range teams {
		teamFilter[team] = struct{}{}
	}
	s.mu.RLock()
	values := make([]domain.ConnectedChannelInfo, 0)
	for channel, associated := range s.conversationTeams {
		conversation, exists := s.conversations[channel]
		if !exists || conversation.WorkspaceID != workspace || (len(channelFilter) > 0 && !containsConversation(channelFilter, channel)) {
			continue
		}
		info := domain.ConnectedChannelInfo{ChannelID: channel, OriginalConnectedChannelID: channel, OriginalConnectedHostID: workspace, InternalTeamIDs: make([]domain.WorkspaceID, 0, len(associated))}
		for team := range associated {
			if len(teamFilter) == 0 || containsWorkspace(teamFilter, team) {
				info.InternalTeamIDs = append(info.InternalTeamIDs, team)
			}
		}
		if len(info.InternalTeamIDs) == 0 {
			continue
		}
		sort.Slice(info.InternalTeamIDs, func(i, j int) bool { return info.InternalTeamIDs[i] < info.InternalTeamIDs[j] })
		values = append(values, info)
	}
	s.mu.RUnlock()
	sort.Slice(values, func(i, j int) bool { return values[i].ChannelID < values[j].ChannelID })
	after, _ := domain.DecodeListCursor(request.Cursor)
	start := 0
	for start < len(values) && string(values[start].ChannelID) <= after {
		start++
	}
	values = values[start:]
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, _ = domain.NewListCursor(string(values[len(values)-1].ChannelID))
	}
	return values, hasMore, next, nil
}

func containsConversation(values map[domain.ConversationID]struct{}, value domain.ConversationID) bool {
	_, ok := values[value]
	return ok
}
func containsWorkspace(values map[domain.WorkspaceID]struct{}, value domain.WorkspaceID) bool {
	_, ok := values[value]
	return ok
}

func (s *Store) CreateOAuthClient(_ context.Context, value domain.OAuthClient) error {
	if value.ID == "" || value.SecretHash == "" || value.AppID == "" {
		return store.InvalidArgument("invalid oauth client")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.oauthClients[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.oauthClients[value.ID] = value
	return nil
}

func (s *Store) GetOAuthClient(_ context.Context, id string) (domain.OAuthClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.oauthClients[id]
	if !exists {
		return domain.OAuthClient{}, store.ErrNotFound
	}
	return value, nil
}

// memoryOAuthCode holds a grant beside the instant it stops being redeemable.
// The expiry lives here rather than on domain.OAuthCode so that no caller can
// choose it: the repository derives it from store.OAuthCodeLifetime.
type memoryOAuthCode struct {
	grant     domain.OAuthCode
	expiresAt time.Time
}

// CreateOAuthCode keys the grant by domain.HashToken of the code and never keeps
// the code itself, matching every other credential in this repository, and bounds
// redemption to store.OAuthCodeLifetime. Codes used to be stored verbatim and
// never expired.
func (s *Store) CreateOAuthCode(_ context.Context, value domain.OAuthCode) error {
	if value.Code == "" || value.ClientID == "" || value.WorkspaceID == "" || value.UserID == "" {
		return store.InvalidArgument("invalid oauth code")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.oauthClients[value.ClientID]; !exists {
		return store.ErrNotFound
	}
	if workspace, exists := s.workspaces[value.WorkspaceID]; !exists || workspace.ID == "" {
		return store.ErrNotFound
	}
	if user, exists := s.users[value.UserID]; !exists || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if value.BotID != "" || value.BotUserID != "" || len(value.BotScopes) != 0 {
		bot, exists := s.bots[value.BotID]
		client := s.oauthClients[value.ClientID]
		if !exists || value.BotUserID == "" || len(domain.NormalizeScopes(value.BotScopes)) == 0 || bot.WorkspaceID != value.WorkspaceID || bot.AppID != client.AppID || bot.UserID != value.BotUserID || bot.Deleted {
			return store.InvalidArgument("oauth bot grant does not match the app installation")
		}
	}
	codeHash := domain.HashToken(value.Code)
	if _, exists := s.oauthCodes[codeHash]; exists {
		return store.ErrAlreadyExists
	}
	value.Scopes = domain.NormalizeScopes(value.Scopes)
	value.BotScopes = domain.NormalizeScopes(value.BotScopes)
	value.UserScopes = domain.NormalizeScopes(value.UserScopes)
	value.Code = codeHash
	s.oauthCodes[codeHash] = memoryOAuthCode{grant: value, expiresAt: time.Now().UTC().Add(store.OAuthCodeLifetime)}
	return nil
}

func (s *Store) ExchangeOAuthCode(_ context.Context, clientID, secret, code, redirect, accessToken string, token domain.OAuthToken) (domain.OAuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, exists := s.oauthClients[clientID]
	if !exists || !hmac.Equal([]byte(client.SecretHash), []byte(domain.HashToken(secret))) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	// Expired codes are dropped on the redemption path, the only schedule this
	// state has.
	for key, stored := range s.oauthCodes {
		if !stored.expiresAt.After(now) {
			delete(s.oauthCodes, key)
		}
	}
	codeHash := domain.HashToken(code)
	stored, exists := s.oauthCodes[codeHash]
	grant := stored.grant
	if !exists || !stored.expiresAt.After(now) || grant.ClientID != clientID || grant.RedirectURI != redirect {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	if !domain.VerifyPKCE(grant.CodeChallenge, grant.CodeChallengeMethod, token.CodeVerifier) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "user"
	}
	subjectID := grant.UserID
	var tokenBotID domain.BotID
	tokenScopes := grant.UserScopes
	if len(tokenScopes) == 0 {
		tokenScopes = grant.Scopes
	}
	if tokenType == "bot" {
		if grant.BotID == "" || grant.BotUserID == "" {
			return domain.OAuthToken{}, store.ErrNotFound
		}
		subjectID = grant.BotUserID
		tokenBotID = grant.BotID
		tokenScopes = grant.BotScopes
	}
	tokenScopes = domain.NormalizeScopes(tokenScopes)
	if len(tokenScopes) == 0 {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	userScopes := domain.NormalizeScopes(grant.UserScopes)
	if tokenType == "bot" && len(userScopes) != 0 && strings.TrimSpace(token.AuthedUserAccessToken) == "" {
		return domain.OAuthToken{}, store.InvalidArgument("missing installer user access token")
	}
	rotating := strings.TrimSpace(token.RefreshToken) != ""
	if rotating && (!token.ExpiresAt.After(now) || token.TokenType == "bot" && len(userScopes) != 0 && (strings.TrimSpace(token.AuthedUserRefreshToken) == "" || !token.AuthedUserExpiresAt.After(now))) {
		return domain.OAuthToken{}, store.InvalidArgument("invalid rotating OAuth credentials")
	}
	accessHash := domain.HashToken(accessToken)
	if _, exists := s.tokens[accessHash]; exists {
		return domain.OAuthToken{}, store.ErrAlreadyExists
	}
	if rotating {
		if _, exists := s.oauthRefreshGrants[domain.HashToken(token.RefreshToken)]; exists {
			return domain.OAuthToken{}, store.ErrAlreadyExists
		}
		if tokenType == "bot" && len(userScopes) != 0 {
			if _, exists := s.oauthRefreshGrants[domain.HashToken(token.AuthedUserRefreshToken)]; exists {
				return domain.OAuthToken{}, store.ErrAlreadyExists
			}
		}
	}
	// Redeeming a code is the installation commit. The grant, token, and
	// installation deliberately change under the same lock so callers can
	// never observe a consumed code and minted token without an enabled
	// installation.
	installationKey := appInstallationKey(client.AppID, grant.WorkspaceID)
	installation, installed := s.appInstallations[installationKey]
	if !installed {
		installation = domain.AppInstallation{
			AppID:       client.AppID,
			WorkspaceID: grant.WorkspaceID,
			CreatedAt:   now,
		}
	}
	installation.Enabled = true
	s.appInstallations[installationKey] = installation
	delete(s.oauthCodes, codeHash)
	s.tokens[accessHash] = domain.TokenRecord{WorkspaceID: grant.WorkspaceID, UserID: subjectID, AppID: client.AppID, BotID: tokenBotID, Scopes: append([]string(nil), tokenScopes...), TokenType: tokenType, ExpiresAt: token.ExpiresAt}
	if rotating {
		refreshHash := domain.HashToken(token.RefreshToken)
		s.oauthRefreshGrants[refreshHash] = domain.OAuthRefreshGrant{TokenHash: refreshHash, AccessTokenHash: accessHash, ClientID: clientID, AppID: client.AppID, WorkspaceID: grant.WorkspaceID, UserID: subjectID, InstallerID: grant.UserID, BotID: tokenBotID, Scopes: append([]string(nil), tokenScopes...), TokenType: tokenType, AccessExpiresAt: token.ExpiresAt, CreatedAt: now}
	}
	if tokenType == "bot" && len(userScopes) != 0 {
		userAccessHash := domain.HashToken(token.AuthedUserAccessToken)
		s.tokens[userAccessHash] = domain.TokenRecord{WorkspaceID: grant.WorkspaceID, UserID: grant.UserID, AppID: client.AppID, Scopes: append([]string(nil), userScopes...), TokenType: "user", ExpiresAt: token.AuthedUserExpiresAt}
		if rotating {
			userRefreshHash := domain.HashToken(token.AuthedUserRefreshToken)
			s.oauthRefreshGrants[userRefreshHash] = domain.OAuthRefreshGrant{TokenHash: userRefreshHash, AccessTokenHash: userAccessHash, ClientID: clientID, AppID: client.AppID, WorkspaceID: grant.WorkspaceID, UserID: grant.UserID, InstallerID: grant.UserID, Scopes: append([]string(nil), userScopes...), TokenType: "user", AccessExpiresAt: token.AuthedUserExpiresAt, CreatedAt: now}
		}
		token.AuthedUserScopes = append([]string(nil), userScopes...)
	} else {
		token.AuthedUserAccessToken = ""
		token.AuthedUserScopes = nil
	}
	token.AccessToken = accessToken
	token.AppID = client.AppID
	token.ClientID = clientID
	token.WorkspaceID = grant.WorkspaceID
	token.UserID = subjectID
	token.InstallerID = grant.UserID
	token.BotID = tokenBotID
	token.Scopes = append([]string(nil), tokenScopes...)
	token.TokenType = tokenType
	token.CodeVerifier = ""
	return token, nil
}

func (s *Store) ExchangeOAuthRefreshToken(_ context.Context, clientID, secret, oldRefreshToken, nextAccessToken, nextRefreshToken string, expiresAt time.Time) (domain.OAuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, exists := s.oauthClients[strings.TrimSpace(clientID)]
	if !exists || !hmac.Equal([]byte(client.SecretHash), []byte(domain.HashToken(strings.TrimSpace(secret)))) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	oldHash := domain.HashToken(strings.TrimSpace(oldRefreshToken))
	grant, exists := s.oauthRefreshGrants[oldHash]
	if !exists || grant.Revoked || grant.ClientID != clientID || grant.AppID != client.AppID || strings.TrimSpace(nextAccessToken) == "" || strings.TrimSpace(nextRefreshToken) == "" || !expiresAt.After(now) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	nextAccessHash := domain.HashToken(nextAccessToken)
	nextRefreshHash := domain.HashToken(nextRefreshToken)
	if _, exists := s.tokens[nextAccessHash]; exists {
		return domain.OAuthToken{}, store.ErrAlreadyExists
	}
	if _, exists := s.oauthRefreshGrants[nextRefreshHash]; exists {
		return domain.OAuthToken{}, store.ErrAlreadyExists
	}
	grant.Revoked = true
	s.oauthRefreshGrants[oldHash] = grant
	next := grant
	next.TokenHash = nextRefreshHash
	next.AccessTokenHash = nextAccessHash
	next.AccessExpiresAt = expiresAt
	next.CreatedAt = now
	next.Revoked = false
	next.Scopes = append([]string(nil), grant.Scopes...)
	s.oauthRefreshGrants[nextRefreshHash] = next
	s.tokens[nextAccessHash] = domain.TokenRecord{WorkspaceID: grant.WorkspaceID, UserID: grant.UserID, AppID: grant.AppID, BotID: grant.BotID, Scopes: append([]string(nil), grant.Scopes...), TokenType: grant.TokenType, ExpiresAt: expiresAt}
	s.enforceOAuthActiveTokenLimit(next)
	return domain.OAuthToken{AccessToken: nextAccessToken, RefreshToken: nextRefreshToken, ExpiresAt: expiresAt, ClientID: clientID, AppID: grant.AppID, WorkspaceID: grant.WorkspaceID, UserID: grant.UserID, InstallerID: grant.InstallerID, BotID: grant.BotID, Scopes: append([]string(nil), grant.Scopes...), TokenType: grant.TokenType}, nil
}

func (s *Store) LookupOAuthRefreshToken(_ context.Context, clientID, refreshToken string) (domain.OAuthRefreshGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grant, exists := s.oauthRefreshGrants[domain.HashToken(strings.TrimSpace(refreshToken))]
	if !exists || grant.Revoked || grant.ClientID != strings.TrimSpace(clientID) {
		return domain.OAuthRefreshGrant{}, store.ErrNotFound
	}
	grant.Scopes = append([]string(nil), grant.Scopes...)
	return grant, nil
}

func (s *Store) ExchangeOAuthAccessToken(_ context.Context, clientID, secret, oldAccessToken, nextAccessToken, nextRefreshToken string, expiresAt time.Time) (domain.OAuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, exists := s.oauthClients[strings.TrimSpace(clientID)]
	if !exists || !hmac.Equal([]byte(client.SecretHash), []byte(domain.HashToken(strings.TrimSpace(secret)))) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	oldAccessHash := domain.HashToken(strings.TrimSpace(oldAccessToken))
	record, exists := s.tokens[oldAccessHash]
	if !exists || record.Revoked || record.AppID != client.AppID || !record.ExpiresAt.IsZero() || record.TokenType != "bot" && record.TokenType != "user" || strings.TrimSpace(nextAccessToken) == "" || strings.TrimSpace(nextRefreshToken) == "" || !expiresAt.After(now) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	for _, grant := range s.oauthRefreshGrants {
		if grant.AccessTokenHash == oldAccessHash {
			return domain.OAuthToken{}, store.ErrNotFound
		}
	}
	nextAccessHash := domain.HashToken(nextAccessToken)
	nextRefreshHash := domain.HashToken(nextRefreshToken)
	if _, exists := s.tokens[nextAccessHash]; exists {
		return domain.OAuthToken{}, store.ErrAlreadyExists
	}
	if _, exists := s.oauthRefreshGrants[nextRefreshHash]; exists {
		return domain.OAuthToken{}, store.ErrAlreadyExists
	}
	legacyKey := "legacy:" + oldAccessHash
	legacy := domain.OAuthRefreshGrant{TokenHash: legacyKey, AccessTokenHash: oldAccessHash, ClientID: clientID, AppID: record.AppID, WorkspaceID: record.WorkspaceID, UserID: record.UserID, InstallerID: record.UserID, BotID: record.BotID, Scopes: append([]string(nil), record.Scopes...), TokenType: record.TokenType, CreatedAt: now.Add(-time.Nanosecond), Revoked: true}
	next := legacy
	next.TokenHash = nextRefreshHash
	next.AccessTokenHash = nextAccessHash
	next.AccessExpiresAt = expiresAt
	next.CreatedAt = now
	next.Revoked = false
	s.oauthRefreshGrants[legacyKey] = legacy
	s.oauthRefreshGrants[nextRefreshHash] = next
	s.tokens[nextAccessHash] = domain.TokenRecord{WorkspaceID: record.WorkspaceID, UserID: record.UserID, AppID: record.AppID, BotID: record.BotID, Scopes: append([]string(nil), record.Scopes...), TokenType: record.TokenType, ExpiresAt: expiresAt}
	return domain.OAuthToken{AccessToken: nextAccessToken, RefreshToken: nextRefreshToken, ExpiresAt: expiresAt, ClientID: clientID, AppID: record.AppID, WorkspaceID: record.WorkspaceID, UserID: record.UserID, InstallerID: record.UserID, BotID: record.BotID, Scopes: append([]string(nil), record.Scopes...), TokenType: record.TokenType}, nil
}

func (s *Store) enforceOAuthActiveTokenLimit(current domain.OAuthRefreshGrant) {
	grants := make([]domain.OAuthRefreshGrant, 0, 3)
	for _, candidate := range s.oauthRefreshGrants {
		if candidate.ClientID == current.ClientID && candidate.WorkspaceID == current.WorkspaceID && candidate.UserID == current.UserID && candidate.BotID == current.BotID && candidate.TokenType == current.TokenType {
			grants = append(grants, candidate)
		}
	}
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].CreatedAt.Equal(grants[j].CreatedAt) {
			return grants[i].AccessTokenHash > grants[j].AccessTokenHash
		}
		return grants[i].CreatedAt.After(grants[j].CreatedAt)
	})
	for _, stale := range grants[2:] {
		record, exists := s.tokens[stale.AccessTokenHash]
		if !exists {
			continue
		}
		record.Revoked = true
		s.tokens[stale.AccessTokenHash] = record
	}
}

func (s *Store) CreateRTMConnection(_ context.Context, value domain.RTMConnection) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.ExpiresAt.IsZero() {
		return store.InvalidArgument("invalid RTM connection")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workspaces[value.WorkspaceID]; !exists {
		return store.ErrNotFound
	}
	if user, exists := s.users[value.UserID]; !exists || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if _, exists := s.rtmConnections[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.rtmConnections[value.ID] = value
	return nil
}

func (s *Store) ConsumeRTMConnection(_ context.Context, id string) (domain.RTMConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.rtmConnections[id]
	if !exists {
		return domain.RTMConnection{}, store.ErrNotFound
	}
	delete(s.rtmConnections, id)
	if !value.ExpiresAt.After(time.Now().UTC()) {
		return domain.RTMConnection{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) CreateSocketModeConnection(_ context.Context, value domain.SocketModeConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" || value.AppID == "" || !value.ExpiresAt.After(time.Now().UTC()) {
		return store.InvalidArgument("invalid Socket Mode connection")
	}
	if _, exists := s.socketConnections[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	now := time.Now().UTC()
	active := 0
	for id, connection := range s.socketConnections {
		if s.socketConnectionActive[id] && connection.AppID == value.AppID && connection.ExpiresAt.After(now) {
			active++
		}
	}
	if active >= domain.SocketModeConnectionLimit {
		return store.ErrSocketModeConnectionLimit
	}
	s.socketConnections[value.ID] = value
	s.socketConnectionActive[value.ID] = false
	return nil
}

func (s *Store) ConsumeSocketModeConnection(_ context.Context, id string) (domain.SocketModeConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.socketConnections[id]
	if !exists || s.socketConnectionActive[id] || !value.ExpiresAt.After(time.Now().UTC()) {
		if exists && !value.ExpiresAt.After(time.Now().UTC()) {
			delete(s.socketConnections, id)
			delete(s.socketConnectionActive, id)
		}
		return domain.SocketModeConnection{}, store.ErrNotFound
	}
	// Consumption is what makes a connection active, so it is the only place
	// the concurrent-connection limit can be enforced. Checking it when the
	// ticket is issued counts nothing, because a ticket is inactive until it is
	// dialled: an app could take unbounded tickets first and dial them all.
	now := time.Now().UTC()
	active := 0
	for other, connection := range s.socketConnections {
		if s.socketConnectionActive[other] && connection.AppID == value.AppID && connection.ExpiresAt.After(now) {
			active++
		}
	}
	if active >= domain.SocketModeConnectionLimit {
		return domain.SocketModeConnection{}, store.ErrSocketModeConnectionLimit
	}
	s.socketConnectionActive[id] = true
	return value, nil
}

func (s *Store) RenewSocketModeConnection(_ context.Context, id string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !expiresAt.After(time.Now().UTC()) {
		return store.InvalidArgument("invalid Socket Mode connection renewal")
	}
	value, exists := s.socketConnections[id]
	// An expired connection has already given up its slot, so a replacement may
	// have been admitted; reviving it would exceed the concurrency limit.
	if !exists || !s.socketConnectionActive[id] || !value.ExpiresAt.After(time.Now().UTC()) {
		return store.ErrNotFound
	}
	value.ExpiresAt = expiresAt.UTC()
	s.socketConnections[id] = value
	return nil
}

func (s *Store) ReleaseSocketModeConnection(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.socketConnectionActive[id] {
		return store.ErrNotFound
	}
	delete(s.socketConnections, id)
	delete(s.socketConnectionActive, id)
	return nil
}

func (s *Store) CountSocketModeConnections(_ context.Context, appID domain.AppID) (int, error) {
	if appID == "" {
		return 0, store.ErrInvalidAppApproval
	}
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for id, value := range s.socketConnections {
		if s.socketConnectionActive[id] && value.AppID == appID && value.ExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

func socketModeResponseKey(appID domain.AppID, envelopeID string) string {
	return string(appID) + "\x00" + envelopeID
}

func (s *Store) RecordSocketModeResponse(_ context.Context, value domain.SocketModeResponse) error {
	if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" || strings.TrimSpace(value.Payload) == "" || value.ReceivedAt.IsZero() {
		return store.InvalidArgument("invalid Socket Mode response")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := socketModeResponseKey(value.AppID, value.EnvelopeID)
	if existing, ok := s.socketResponses[key]; ok {
		if existing.Payload != value.Payload {
			return store.ErrConflict
		}
		return nil
	}
	s.socketResponses[key] = value
	return nil
}

func (s *Store) GetSocketModeResponse(_ context.Context, appID domain.AppID, envelopeID string) (domain.SocketModeResponse, error) {
	if appID == "" || strings.TrimSpace(envelopeID) == "" {
		return domain.SocketModeResponse{}, store.InvalidArgument("Socket Mode response identity is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.socketResponses[socketModeResponseKey(appID, strings.TrimSpace(envelopeID))]
	if !ok {
		return domain.SocketModeResponse{}, store.ErrNotFound
	}
	return value, nil
}

func validateSocketModeResponseLease(appID domain.AppID, owner string, limit int, lease time.Duration) error {
	if appID == "" || strings.TrimSpace(owner) == "" || limit <= 0 || limit > 1000 || lease <= 0 {
		return store.InvalidArgument("invalid Socket Mode response lease")
	}
	return nil
}

func (s *Store) ClaimSocketModeResponses(_ context.Context, appID domain.AppID, owner string, limit int, lease time.Duration) ([]domain.SocketModeResponse, error) {
	if err := validateSocketModeResponseLease(appID, owner, limit, lease); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(lease)
	s.mu.Lock()
	defer s.mu.Unlock()
	type candidate struct {
		key   string
		value domain.SocketModeResponse
	}
	candidates := make([]candidate, 0, len(s.socketResponses))
	for key, value := range s.socketResponses {
		if value.AppID != appID || !value.AcknowledgedAt.IsZero() || value.LeaseExpiresAt.After(now) {
			continue
		}
		candidates = append(candidates, candidate{key: key, value: value})
	}
	slices.SortFunc(candidates, func(left, right candidate) int {
		if left.value.ReceivedAt.Before(right.value.ReceivedAt) {
			return -1
		}
		if left.value.ReceivedAt.After(right.value.ReceivedAt) {
			return 1
		}
		return strings.Compare(left.value.EnvelopeID, right.value.EnvelopeID)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	values := make([]domain.SocketModeResponse, 0, len(candidates))
	for _, candidate := range candidates {
		value := candidate.value
		value.LeaseOwner = owner
		value.LeaseExpiresAt = expiresAt
		s.socketResponses[candidate.key] = value
		values = append(values, value)
	}
	return values, nil
}

// AckSocketModeResponses validates the whole batch before applying any of it, so
// a rejected envelope leaves no earlier envelope acknowledged. The SQL
// repositories acknowledge the batch in one transaction for the same reason.
func (s *Store) AckSocketModeResponses(_ context.Context, owner string, values []domain.SocketModeResponse) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 {
		return store.InvalidArgument("Socket Mode response owner and a non-empty batch are required")
	}
	for _, value := range values {
		if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" {
			return store.InvalidArgument("invalid Socket Mode response key")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, value := range values {
		stored, ok := s.socketResponses[socketModeResponseKey(value.AppID, value.EnvelopeID)]
		if !ok {
			return store.ErrNotFound
		}
		if !stored.AcknowledgedAt.IsZero() {
			continue
		}
		if stored.LeaseOwner != owner || !stored.LeaseExpiresAt.After(now) {
			return store.ErrConflict
		}
	}
	for _, value := range values {
		key := socketModeResponseKey(value.AppID, value.EnvelopeID)
		stored := s.socketResponses[key]
		if !stored.AcknowledgedAt.IsZero() {
			continue
		}
		stored.AcknowledgedAt = now
		stored.LeaseOwner = ""
		stored.LeaseExpiresAt = time.Time{}
		s.socketResponses[key] = stored
	}
	return nil
}

func (s *Store) RenewSocketModeResponses(_ context.Context, owner string, values []domain.SocketModeResponse, lease time.Duration) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 || lease <= 0 {
		return store.InvalidArgument("Socket Mode response renewal fields are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	expiresAt := now.Add(lease)
	for _, value := range values {
		key := socketModeResponseKey(value.AppID, value.EnvelopeID)
		stored, ok := s.socketResponses[key]
		if !ok {
			return store.ErrNotFound
		}
		if !stored.AcknowledgedAt.IsZero() || stored.LeaseOwner != owner || !stored.LeaseExpiresAt.After(now) {
			return store.ErrConflict
		}
	}
	for _, value := range values {
		key := socketModeResponseKey(value.AppID, value.EnvelopeID)
		stored := s.socketResponses[key]
		stored.LeaseExpiresAt = expiresAt
		s.socketResponses[key] = stored
	}
	return nil
}

func (s *Store) ReleaseSocketModeResponses(_ context.Context, owner string, values []domain.SocketModeResponse, retryAt time.Time) error {
	if strings.TrimSpace(owner) == "" || retryAt.IsZero() {
		return store.InvalidArgument("Socket Mode response release fields are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range values {
		key := socketModeResponseKey(value.AppID, value.EnvelopeID)
		stored, ok := s.socketResponses[key]
		if !ok {
			return store.ErrNotFound
		}
		if !stored.AcknowledgedAt.IsZero() {
			continue
		}
		if stored.LeaseOwner != owner {
			return store.ErrConflict
		}
		stored.LeaseOwner = ""
		stored.LeaseExpiresAt = retryAt.UTC()
		s.socketResponses[key] = stored
	}
	return nil
}

func validSocketModeInteraction(value domain.SocketModeInteraction) bool {
	if value.EnvelopeID == "" || value.AppID == "" || value.WorkspaceID == "" || value.UserID == "" ||
		(value.Type != "slash_commands" && value.Type != "interactive") || strings.TrimSpace(value.Payload) == "" ||
		value.Response.TokenHash == "" || value.Response.AppID != value.AppID || value.Response.WorkspaceID != value.WorkspaceID ||
		value.Response.UserID != value.UserID || value.Response.ConversationID == "" || value.CreatedAt.IsZero() {
		return false
	}
	var payload map[string]any
	return json.Unmarshal([]byte(value.Payload), &payload) == nil && payload != nil
}

func (s *Store) CreateSocketModeInteraction(_ context.Context, value domain.SocketModeInteraction) error {
	if !validSocketModeInteraction(value) {
		return store.InvalidArgument("invalid Socket Mode interaction")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.socketInteractions[value.EnvelopeID]; exists {
		return store.ErrAlreadyExists
	}
	s.socketInteractions[value.EnvelopeID] = value
	return nil
}

func (s *Store) GetSocketModeInteraction(_ context.Context, appID domain.AppID, envelopeID string) (domain.SocketModeInteraction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.socketInteractions[envelopeID]
	if !exists || value.AppID != appID {
		return domain.SocketModeInteraction{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) ClaimSocketModeInteraction(_ context.Context, appID domain.AppID, owner string, lease time.Duration) (domain.SocketModeInteraction, bool, error) {
	if appID == "" || strings.TrimSpace(owner) == "" || lease <= 0 {
		return domain.SocketModeInteraction{}, false, store.InvalidArgument("invalid Socket Mode interaction lease")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	var candidates []domain.SocketModeInteraction
	for _, value := range s.socketInteractions {
		if value.AppID != appID || !value.AcknowledgedAt.IsZero() || value.RetryAt.After(now) ||
			(value.LeaseOwner != "" && value.LeaseExpiresAt.After(now)) {
			continue
		}
		candidates = append(candidates, value)
	}
	slices.SortFunc(candidates, func(left, right domain.SocketModeInteraction) int {
		if order := left.CreatedAt.Compare(right.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.EnvelopeID, right.EnvelopeID)
	})
	if len(candidates) == 0 {
		return domain.SocketModeInteraction{}, false, nil
	}
	value := candidates[0]
	value.LeaseOwner = owner
	value.LeaseExpiresAt = now.Add(lease)
	s.socketInteractions[value.EnvelopeID] = value
	return value, true, nil
}

func (s *Store) AckSocketModeInteraction(_ context.Context, appID domain.AppID, envelopeID, owner string) error {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.socketInteractions[envelopeID]
	if !exists || value.AppID != appID || value.LeaseOwner != owner || !value.LeaseExpiresAt.After(now) || !value.AcknowledgedAt.IsZero() {
		return store.ErrLeaseConflict
	}
	value.AcknowledgedAt = now
	value.LeaseOwner = ""
	value.LeaseExpiresAt = time.Time{}
	s.socketInteractions[envelopeID] = value
	return nil
}

func (s *Store) ReleaseSocketModeInteraction(_ context.Context, appID domain.AppID, envelopeID, owner, reason string, retryAt time.Time) error {
	now := time.Now().UTC()
	if strings.TrimSpace(reason) == "" || retryAt.IsZero() {
		return store.InvalidArgument("invalid Socket Mode interaction release")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.socketInteractions[envelopeID]
	if !exists || value.AppID != appID || value.LeaseOwner != owner || !value.LeaseExpiresAt.After(now) || !value.AcknowledgedAt.IsZero() {
		return store.ErrLeaseConflict
	}
	value.LeaseOwner = ""
	value.LeaseExpiresAt = time.Time{}
	value.RetryAt = retryAt.UTC()
	value.RetryCount++
	value.RetryReason = strings.TrimSpace(reason)
	s.socketInteractions[envelopeID] = value
	return nil
}

func (s *Store) GetSocketModeCursor(_ context.Context, appID domain.AppID) (uint64, error) {
	if appID == "" {
		return 0, store.ErrInvalidAppApproval
	}
	s.mu.RLock()
	cursor := s.socketCursors[appID]
	s.mu.RUnlock()
	return cursor, nil
}

func (s *Store) SetSocketModeCursor(_ context.Context, appID domain.AppID, cursor uint64) error {
	if appID == "" {
		return store.ErrInvalidAppApproval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor < s.socketCursors[appID] {
		return store.ErrConflict
	}
	s.socketCursors[appID] = cursor
	return nil
}

func (s *Store) SetConversationPrivate(_ context.Context, conversation domain.ConversationID, event events.Event) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	if value.IsPrivate || value.IsDirect || value.IsGroupDirect {
		return domain.Conversation{}, store.ErrInvalidConversationType
	}
	value.IsPrivate = true
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) GetConversationPrefs(_ context.Context, conversation domain.ConversationID) (domain.ConversationPrefs, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.conversations[conversation]; !ok {
		return domain.ConversationPrefs{}, store.ErrNotFound
	}
	value := s.conversationPrefs[conversation]
	value.ConversationID = conversation
	value.CanThread.Types = append([]domain.ConversationPreferenceType(nil), value.CanThread.Types...)
	value.CanThread.Users = append([]domain.UserID(nil), value.CanThread.Users...)
	value.WhoCanPost.Types = append([]domain.ConversationPreferenceType(nil), value.WhoCanPost.Types...)
	value.WhoCanPost.Users = append([]domain.UserID(nil), value.WhoCanPost.Users...)
	return value, nil
}

func (s *Store) SetConversationPrefs(_ context.Context, conversation domain.ConversationID, value domain.ConversationPrefs, event events.Event) (domain.ConversationPrefs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[conversation]; !ok {
		return domain.ConversationPrefs{}, store.ErrNotFound
	}
	value.ConversationID = conversation
	value.CanThread.Types = append([]domain.ConversationPreferenceType(nil), value.CanThread.Types...)
	value.CanThread.Users = append([]domain.UserID(nil), value.CanThread.Users...)
	value.WhoCanPost.Types = append([]domain.ConversationPreferenceType(nil), value.WhoCanPost.Types...)
	value.WhoCanPost.Users = append([]domain.UserID(nil), value.WhoCanPost.Users...)
	s.conversationPrefs[conversation] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) AddConversationMember(_ context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return store.ErrNotFound
	}
	if value.IsPrivate {
		return store.ErrNotFound
	}
	if s.memberships[conversation] == nil {
		s.memberships[conversation] = make(map[domain.UserID]struct{})
	}
	if _, exists := s.memberships[conversation][user]; exists {
		return nil
	}
	s.memberships[conversation][user] = struct{}{}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) InviteConversationMembers(_ context.Context, conversation domain.ConversationID, users []domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok || value.IsPrivate {
		return store.ErrNotFound
	}
	for _, user := range users {
		member, exists := s.users[user]
		if !exists || member.Deleted || member.WorkspaceID != value.WorkspaceID {
			return store.ErrNotFound
		}
	}
	if s.memberships[conversation] == nil {
		s.memberships[conversation] = make(map[domain.UserID]struct{})
	}
	for _, user := range users {
		s.memberships[conversation][user] = struct{}{}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RemoveConversationMember(_ context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.conversations[conversation]; !exists {
		return store.ErrNotFound
	}
	members := s.memberships[conversation]
	if _, exists := members[user]; !exists {
		return store.ErrNotFound
	}
	delete(members, user)
	s.outbox = append(s.outbox, event)
	return nil
}

func readCursorKey(workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) string {
	return string(workspace) + "\x00" + string(user) + "\x00" + string(conversation)
}

func (s *Store) GetReadCursor(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) (domain.ReadCursor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cursor, ok := s.readCursors[readCursorKey(workspace, user, conversation)]
	if !ok {
		return domain.ReadCursor{}, store.ErrNotFound
	}
	return cursor, nil
}

func (s *Store) SetReadCursor(_ context.Context, cursor domain.ReadCursor, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readCursors[readCursorKey(cursor.WorkspaceID, cursor.UserID, cursor.Conversation)] = cursor
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListConversations(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.ConversationListRequest) (domain.ConversationPage, error) {
	if request.Limit <= 0 {
		return domain.ConversationPage{}, store.InvalidArgument("page limit must be positive")
	}
	if err := domain.ValidateConversationTypes(request.Types); err != nil {
		return domain.ConversationPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	s.mu.RLock()
	memberUser := user
	if request.MemberUserID != "" {
		memberUser = request.MemberUserID
	}
	values := make([]domain.Conversation, 0, request.Limit+1)
	for _, conversation := range s.conversations {
		if conversation.WorkspaceID != workspace || (after != "" && string(conversation.ID) <= after) {
			continue
		}
		if request.ExcludeArchived && conversation.Archived {
			continue
		}
		if len(request.Types) > 0 {
			matches := false
			for _, typeValue := range request.Types {
				if domain.MatchesConversationType(conversation, typeValue) {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
		}
		if conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect {
			_, viewerMember := s.memberships[conversation.ID][user]
			_, subjectMember := s.memberships[conversation.ID][memberUser]
			if !viewerMember || !subjectMember {
				continue
			}
		}
		lastRead := time.Time{}
		if cursor, ok := s.readCursors[readCursorKey(workspace, user, conversation.ID)]; ok {
			lastRead, err = domain.ParseMessageTimestamp(cursor.LastRead)
			if err != nil {
				s.mu.RUnlock()
				return domain.ConversationPage{}, err
			}
			for _, message := range s.messages[conversation.ID] {
				// Compare instants, not MessageTimestamp text: the textual form has
				// an unpadded seconds field, so string comparison is only accidentally
				// monotonic and breaks for imported pre-2001 timestamps. Both sides are
				// truncated to the microsecond the wire format carries so the SQL
				// repositories and this one count the same messages.
				if !message.Deleted && message.CreatedAt.Truncate(time.Microsecond).After(lastRead.Truncate(time.Microsecond)) {
					conversation.UnreadCount++
				}
			}
		} else {
			for _, message := range s.messages[conversation.ID] {
				if !message.Deleted {
					conversation.UnreadCount++
				}
			}
		}
		values = appendSorted(values, conversation, request.Limit+1, func(left, right domain.Conversation) bool { return left.ID < right.ID })
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.ConversationPage{Conversations: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.ConversationPage{}, err
		}
	}
	return page, nil
}

func (s *Store) SearchConversations(_ context.Context, workspace domain.WorkspaceID, query string, request domain.PageRequest) (domain.ConversationPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ConversationPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	query = domain.FoldSearchText(strings.TrimSpace(query))
	if query == "" {
		return domain.ConversationPage{}, store.InvalidArgument("conversation search query is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Conversation, 0, request.Limit+1)
	for _, conversation := range s.conversations {
		if conversation.WorkspaceID != workspace || (after != "" && string(conversation.ID) <= after) {
			continue
		}
		if !strings.Contains(domain.FoldSearchText(conversation.Name), query) && !strings.Contains(domain.FoldSearchText(conversation.Topic), query) && !strings.Contains(domain.FoldSearchText(conversation.Purpose), query) {
			continue
		}
		values = appendSorted(values, conversation, request.Limit+1, func(left, right domain.Conversation) bool { return left.ID < right.ID })
	}
	page := domain.ConversationPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	page.Conversations = values
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}
func (s *Store) IsConversationMember(_ context.Context, conversation domain.ConversationID, user domain.UserID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.memberships[conversation][user]
	return ok, nil
}

// CreateMessage applies the same normalization and referential checks the SQL
// repositories enforce. Skipping them meant a payload rejected on SQLite was
// persisted verbatim here, an empty attachments field read back as "" instead of
// "[]", a duplicate message identifier silently inserted a second row, and a
// message could reference a conversation that does not exist.
func (s *Store) CreateMessage(_ context.Context, message domain.Message, event events.Event, idempotencyKey string) error {
	message, err := normalizeMessage(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createMessageLocked(message, event, idempotencyKey)
}

func normalizeMessage(message domain.Message) (domain.Message, error) {
	// Matches the SQL backends: see the note there on why a message instant is
	// held to the resolution of its own timestamp.
	message.CreatedAt = domain.MessageInstant(message.CreatedAt)
	blocks, err := domain.NormalizeBlocks([]byte(message.Blocks))
	if err != nil {
		return domain.Message{}, err
	}
	attachments, err := domain.NormalizeAttachments([]byte(message.Attachments))
	if err != nil {
		return domain.Message{}, err
	}
	if attachments == "" {
		attachments = "[]"
	}
	unfurls, err := domain.NormalizeUnfurls(message.Unfurls)
	if err != nil {
		return domain.Message{}, err
	}
	message.Blocks = blocks
	message.Attachments = attachments
	message.Unfurls = copyUnfurls(unfurls)
	return message, nil
}

func (s *Store) createMessageLocked(message domain.Message, event events.Event, idempotencyKey string) error {
	if _, exists := s.conversations[message.Conversation]; !exists {
		return store.ErrNotFound
	}
	files := make([]domain.File, 0, len(message.Files))
	seenFiles := make(map[domain.FileID]struct{}, len(message.Files))
	for _, requested := range message.Files {
		if requested.ID == "" {
			return store.InvalidArgument("message file id is required")
		}
		if _, duplicate := seenFiles[requested.ID]; duplicate {
			return store.InvalidArgument("message file ids must be unique")
		}
		file, exists := s.files[requested.ID]
		if !exists || file.Deleted || file.WorkspaceID != message.WorkspaceID || !slices.Contains(s.fileShares[requested.ID], message.Conversation) {
			return store.ErrNotFound
		}
		seenFiles[requested.ID] = struct{}{}
		file.SharedChannels = append([]domain.ConversationID(nil), s.fileShares[requested.ID]...)
		files = append(files, file)
	}
	message.Files = files
	if _, exists := s.messageLocked(message.ID); exists == nil {
		return store.ErrAlreadyExists
	}
	key := idempotencyKeyFor(message.WorkspaceID, message.AuthorID, idempotencyKey)
	if idempotencyKey != "" {
		if _, exists := s.idempotency[key]; exists {
			return store.ErrIdempotencyConflict
		}
	}
	// A message's timestamp is its public identifier, so two messages in one
	// conversation may not share one microsecond. The SQL profiles enforce this
	// with a UNIQUE index on (conversation, created_at); this one enforces it by
	// looking, and both answer store.ErrMessageTimestampTaken so the caller's
	// remedy is the same everywhere. It is checked AFTER the identifier and the
	// idempotency key, in the same order as the SQL profiles, so a caller that
	// retried a whole request is told that and not this.
	for _, existing := range s.messages[message.Conversation] {
		if existing.CreatedAt.Equal(message.CreatedAt) {
			return store.ErrMessageTimestampTaken
		}
	}
	values := s.messages[message.Conversation]
	index := sort.Search(len(values), func(index int) bool {
		current := values[index]
		return message.CreatedAt.Before(current.CreatedAt) || (message.CreatedAt.Equal(current.CreatedAt) && string(message.ID) < string(current.ID))
	})
	values = append(values, domain.Message{})
	copy(values[index+1:], values[index:])
	values[index] = message
	s.messages[message.Conversation] = values
	s.outbox = append(s.outbox, event)
	if idempotencyKey != "" {
		s.idempotency[key] = message.ID
	}
	return nil
}

func (s *Store) CreateFileShareMessage(_ context.Context, fileIDs []domain.FileID, message domain.Message, event events.Event) error {
	if len(fileIDs) == 0 {
		return store.InvalidArgument("a file share message requires a file")
	}
	message.Files = make([]domain.File, len(fileIDs))
	for index, fileID := range fileIDs {
		message.Files[index].ID = fileID
	}
	normalized, err := normalizeMessage(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := make(map[domain.FileID][]domain.ConversationID, len(fileIDs))
	for _, fileID := range fileIDs {
		file, exists := s.files[fileID]
		if !exists || file.Deleted || file.WorkspaceID != message.WorkspaceID {
			return store.ErrNotFound
		}
		previous[fileID] = append([]domain.ConversationID(nil), s.fileShares[fileID]...)
		if !slices.Contains(s.fileShares[fileID], message.Conversation) {
			s.fileShares[fileID] = append(s.fileShares[fileID], message.Conversation)
			slices.Sort(s.fileShares[fileID])
		}
	}
	if err := s.createMessageLocked(normalized, event, ""); err != nil {
		for fileID, channels := range previous {
			s.fileShares[fileID] = channels
		}
		return err
	}
	return nil
}

func (s *Store) CreateEphemeralMessage(_ context.Context, value domain.EphemeralMessage, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.Conversation == "" || value.AuthorID == "" || value.RecipientID == "" || value.CreatedAt.IsZero() || value.Timestamp == "" {
		return store.InvalidArgument("invalid ephemeral message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, exists := s.conversations[value.Conversation]
	if !exists || conversation.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if _, exists := s.users[value.AuthorID]; !exists {
		return store.ErrNotFound
	}
	if _, exists := s.users[value.RecipientID]; !exists {
		return store.ErrNotFound
	}
	for _, existing := range s.ephemeralMessages {
		if existing.ID == value.ID {
			return store.ErrAlreadyExists
		}
	}
	s.ephemeralMessages = append(s.ephemeralMessages, value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListEphemeralMessages(_ context.Context, workspaceID domain.WorkspaceID, recipientID domain.UserID, conversationID domain.ConversationID, limit int) ([]domain.EphemeralMessage, error) {
	if workspaceID == "" || recipientID == "" || conversationID == "" || limit <= 0 || limit > 1000 {
		return nil, store.InvalidArgument("invalid ephemeral message page")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.EphemeralMessage, 0, limit)
	for index := len(s.ephemeralMessages) - 1; index >= 0 && len(result) < limit; index-- {
		value := s.ephemeralMessages[index]
		if value.WorkspaceID == workspaceID && value.RecipientID == recipientID && value.Conversation == conversationID {
			result = append(result, value)
		}
	}
	slices.Reverse(result)
	return result, nil
}

func (s *Store) GetEphemeralMessage(_ context.Context, workspaceID domain.WorkspaceID, recipientID domain.UserID, id domain.MessageID) (domain.EphemeralMessage, error) {
	if workspaceID == "" || recipientID == "" || id == "" {
		return domain.EphemeralMessage{}, store.InvalidArgument("invalid ephemeral message key")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.ephemeralMessages {
		if value.ID == id && value.WorkspaceID == workspaceID && value.RecipientID == recipientID {
			return value, nil
		}
	}
	return domain.EphemeralMessage{}, store.ErrNotFound
}

func (s *Store) UpdateEphemeralMessage(_ context.Context, value domain.EphemeralMessage, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.Conversation == "" || value.AuthorID == "" || value.RecipientID == "" || value.CreatedAt.IsZero() || value.Timestamp == "" {
		return store.InvalidArgument("invalid ephemeral message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, existing := range s.ephemeralMessages {
		if existing.ID != value.ID || existing.WorkspaceID != value.WorkspaceID || existing.RecipientID != value.RecipientID {
			continue
		}
		if existing.AppID != value.AppID || existing.Conversation != value.Conversation || existing.AuthorID != value.AuthorID ||
			!existing.CreatedAt.Equal(value.CreatedAt) || existing.Timestamp != value.Timestamp {
			return store.ErrConflict
		}
		s.ephemeralMessages[index] = value
		s.outbox = append(s.outbox, event)
		return nil
	}
	return store.ErrNotFound
}

func (s *Store) DeleteEphemeralMessage(_ context.Context, workspaceID domain.WorkspaceID, recipientID domain.UserID, id domain.MessageID, event events.Event) error {
	if workspaceID == "" || recipientID == "" || id == "" {
		return store.InvalidArgument("invalid ephemeral message key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, value := range s.ephemeralMessages {
		if value.ID != id || value.WorkspaceID != workspaceID || value.RecipientID != recipientID {
			continue
		}
		s.ephemeralMessages = slices.Delete(s.ephemeralMessages, index, index+1)
		s.outbox = append(s.outbox, event)
		return nil
	}
	return store.ErrNotFound
}

func idempotencyKeyFor(workspace domain.WorkspaceID, user domain.UserID, key string) string {
	return string(workspace) + "\x00" + string(user) + "\x00" + key
}

func (s *Store) GetMessage(_ context.Context, id domain.MessageID) (domain.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, values := range s.messages {
		for _, message := range values {
			if message.ID == id {
				return cloneMessage(message), nil
			}
		}
	}
	return domain.Message{}, store.ErrNotFound
}

func (s *Store) GetIdempotentMessage(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, key string) (domain.Message, error) {
	s.mu.RLock()
	id, ok := s.idempotency[idempotencyKeyFor(workspace, user, key)]
	s.mu.RUnlock()
	if !ok {
		return domain.Message{}, store.ErrNotFound
	}
	return s.GetMessage(ctx, id)
}

func (s *Store) GetMessageByCreatedAt(_ context.Context, conversation domain.ConversationID, createdAt time.Time) (domain.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Compare instants, not their textual timestamps, and truncate both sides to
	// the resolution a message's public identifier can express — the same key the
	// SQL repositories now build. Returning the stored value directly handed the
	// caller an alias into store state through domain.Message.Unfurls, which is a
	// map; every other read here clones.
	wanted := domain.MessageInstant(createdAt)
	for _, message := range s.messages[conversation] {
		if domain.MessageInstant(message.CreatedAt).Equal(wanted) {
			return cloneMessage(message), nil
		}
	}
	return domain.Message{}, store.ErrNotFound
}

func reactionKey(name string, user domain.UserID) string { return name + "\x00" + string(user) }

func (s *Store) AddReaction(_ context.Context, reaction domain.Reaction, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.messageLocked(reaction.Message); err != nil {
		return err
	}
	if s.reactions[reaction.Message] == nil {
		s.reactions[reaction.Message] = make(map[string]domain.Reaction)
	}
	key := reactionKey(reaction.Name, reaction.UserID)
	if _, exists := s.reactions[reaction.Message][key]; exists {
		return store.ErrAlreadyExists
	}
	s.reactions[reaction.Message][key] = reaction
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RemoveReaction(_ context.Context, reaction domain.Reaction, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.messageLocked(reaction.Message); err != nil {
		return err
	}
	key := reactionKey(reaction.Name, reaction.UserID)
	if _, exists := s.reactions[reaction.Message][key]; !exists {
		return store.ErrNotFound
	}
	delete(s.reactions[reaction.Message], key)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListReactions(_ context.Context, message domain.MessageID, request domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, "", false, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	values := make([]domain.Reaction, 0, request.Limit+1)
	for _, reaction := range s.reactions[message] {
		key := reactionKey(reaction.Name, reaction.UserID)
		if after == "" || key > after {
			values = appendSorted(values, reaction, request.Limit+1, func(left, right domain.Reaction) bool {
				return reactionKey(left.Name, left.UserID) < reactionKey(right.Name, right.UserID)
			})
		}
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(reactionKey(values[len(values)-1].Name, values[len(values)-1].UserID))
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

func (s *Store) ListUserReactions(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.UserReactionPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserReactionPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserReactionPage{}, err
	}
	s.mu.RLock()
	values := make([]domain.UserReaction, 0, request.Limit+1)
	for conversationID, messages := range s.messages {
		for _, message := range messages {
			if message.WorkspaceID != workspace {
				continue
			}
			for _, reaction := range s.reactions[message.ID] {
				if reaction.UserID != user {
					continue
				}
				item := domain.UserReaction{Conversation: conversationID, Message: cloneMessage(message), Reaction: reaction}
				if after == "" || userReactionKey(item) > after {
					values = appendSorted(values, item, request.Limit+1, func(left, right domain.UserReaction) bool { return userReactionKey(left) < userReactionKey(right) })
				}
			}
		}
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.UserReactionPage{Items: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(userReactionKey(values[len(values)-1]))
		if err != nil {
			return domain.UserReactionPage{}, err
		}
	}
	return page, nil
}

// userReactionKey is an ordering key AND a keyset cursor, compared with plain
// string comparison. It must therefore use the fixed-width encoding, exactly as
// the SQL repositories do: time.RFC3339Nano strips trailing zeros, so ".12Z"
// sorts after ".123456Z" and the cursor minted from the earlier row skips the
// later ones on the next page.
func userReactionKey(value domain.UserReaction) string {
	return string(domain.NewStoredTime(value.Message.CreatedAt)) + "\x00" + string(value.Message.ID) + "\x00" + value.Reaction.Name + "\x00" + string(value.Reaction.UserID)
}

func pinKey(pin domain.Pin) string { return string(pin.Message) + "\x00" + string(pin.UserID) }

func (s *Store) AddPin(_ context.Context, pin domain.Pin, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.messageLocked(pin.Message); err != nil {
		return err
	}
	if s.pins[pin.Message] == nil {
		s.pins[pin.Message] = make(map[domain.UserID]domain.Pin)
	}
	if _, exists := s.pins[pin.Message][pin.UserID]; exists {
		return store.ErrAlreadyExists
	}
	s.pins[pin.Message][pin.UserID] = pin
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RemovePin(_ context.Context, pin domain.Pin, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.messageLocked(pin.Message); err != nil {
		return err
	}
	if _, exists := s.pins[pin.Message][pin.UserID]; !exists {
		return store.ErrNotFound
	}
	delete(s.pins[pin.Message], pin.UserID)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListPins(_ context.Context, conversation domain.ConversationID, request domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, "", false, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	values := make([]domain.Pin, 0, request.Limit+1)
	for _, message := range s.messages[conversation] {
		for _, pin := range s.pins[message.ID] {
			if after == "" || pinKey(pin) > after {
				values = appendSorted(values, pin, request.Limit+1, func(left, right domain.Pin) bool { return pinKey(left) < pinKey(right) })
			}
		}
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(pinKey(values[len(values)-1]))
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

// starKey is an ordering key AND a keyset cursor. See userReactionKey: the
// variable-width encoding reordered stars.list and made the next page skip
// every row whose fraction was a strict extension of the cursor's.
func starKey(value domain.Star) string {
	return string(domain.NewStoredTime(value.CreatedAt)) + "\x00" + string(value.Message.ID)
}

func (s *Store) AddStar(_ context.Context, star domain.Star, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message, err := s.messageLocked(star.Message.ID)
	if err != nil {
		return err
	}
	star.Message = message
	if s.stars[star.UserID] == nil {
		s.stars[star.UserID] = make(map[domain.MessageID]domain.Star)
	}
	if _, exists := s.stars[star.UserID][star.Message.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.stars[star.UserID][star.Message.ID] = star
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RemoveStar(_ context.Context, star domain.Star, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.messageLocked(star.Message.ID); err != nil {
		return err
	}
	if _, exists := s.stars[star.UserID][star.Message.ID]; !exists {
		return store.ErrNotFound
	}
	delete(s.stars[star.UserID], star.Message.ID)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListStars(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, "", false, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Star, 0, request.Limit+1)
	for _, star := range s.stars[user] {
		if star.Message.WorkspaceID != workspace || star.Message.Deleted || (after != "" && starKey(star) <= after) {
			continue
		}
		star.Message = cloneMessage(star.Message)
		values = appendSorted(values, star, request.Limit+1, func(left, right domain.Star) bool { return starKey(left) < starKey(right) })
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(starKey(values[len(values)-1]))
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

func savedItemKey(value domain.SavedItem) string {
	return string(domain.NewStoredTime(value.UpdatedAt)) + "\x00" + string(value.ID)
}

func (s *Store) CreateSavedItem(_ context.Context, item domain.SavedItem, event events.Event) (domain.SavedItem, bool, error) {
	if !item.State.Valid() {
		return domain.SavedItem{}, false, store.InvalidArgument("saved item state is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	message, err := s.messageLocked(item.MessageID)
	if err != nil || message.WorkspaceID != item.WorkspaceID || message.Conversation != item.Conversation {
		return domain.SavedItem{}, false, store.ErrNotFound
	}
	for _, existing := range s.savedItems {
		if existing.WorkspaceID == item.WorkspaceID && existing.UserID == item.UserID && existing.MessageID == item.MessageID {
			return existing, false, nil
		}
	}
	if _, exists := s.savedItems[item.ID]; exists {
		return domain.SavedItem{}, false, store.ErrAlreadyExists
	}
	item.Message = domain.Message{}
	item.SourceAvailable = false
	s.savedItems[item.ID] = item
	s.outbox = append(s.outbox, event)
	return item, true, nil
}

func (s *Store) GetSavedItem(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID) (domain.SavedItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.savedItems[id]
	if !exists || item.WorkspaceID != workspace || item.UserID != user {
		return domain.SavedItem{}, store.ErrNotFound
	}
	return item, nil
}

func (s *Store) GetSavedItemByMessage(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, message domain.MessageID) (domain.SavedItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.savedItems {
		if item.WorkspaceID == workspace && item.UserID == user && item.MessageID == message {
			return item, nil
		}
	}
	return domain.SavedItem{}, store.ErrNotFound
}

func (s *Store) ListSavedItemsForMessages(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, messages []domain.MessageID) ([]domain.SavedItem, error) {
	wanted := make(map[domain.MessageID]struct{}, len(messages))
	for _, message := range messages {
		wanted[message] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.SavedItem, 0, len(messages))
	for _, item := range s.savedItems {
		if item.WorkspaceID != workspace || item.UserID != user {
			continue
		}
		if _, ok := wanted[item.MessageID]; ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *Store) ListSavedItems(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, state domain.SavedItemState, request domain.PageRequest) (domain.SavedItemPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.SavedItemPage{}, err
	}
	if !state.Valid() {
		return domain.SavedItemPage{}, store.InvalidArgument("saved item state is invalid")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.SavedItem, 0, request.Limit+1)
	for _, item := range s.savedItems {
		if item.WorkspaceID != workspace || item.UserID != user || item.State != state || after != "" && savedItemKey(item) <= after {
			continue
		}
		values = appendSorted(values, item, request.Limit+1, func(left, right domain.SavedItem) bool {
			return savedItemKey(left) < savedItemKey(right)
		})
	}
	more := len(values) > request.Limit
	if more {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if more {
		next, err = domain.NewListCursor(savedItemKey(values[len(values)-1]))
		if err != nil {
			return domain.SavedItemPage{}, err
		}
	}
	return domain.SavedItemPage{Items: values, NextCursor: next, HasMore: more}, nil
}

func (s *Store) UpdateSavedItem(_ context.Context, item domain.SavedItem, event events.Event) (domain.SavedItem, error) {
	if !item.State.Valid() {
		return domain.SavedItem{}, store.InvalidArgument("saved item state is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, exists := s.savedItems[item.ID]
	if !exists || existing.WorkspaceID != item.WorkspaceID || existing.UserID != item.UserID {
		return domain.SavedItem{}, store.ErrNotFound
	}
	if existing.MessageID != item.MessageID || existing.Conversation != item.Conversation || existing.CreatedAt != item.CreatedAt {
		return domain.SavedItem{}, store.ErrConflict
	}
	existing.State = item.State
	existing.UpdatedAt = item.UpdatedAt
	s.savedItems[item.ID] = existing
	s.outbox = append(s.outbox, event)
	return existing, nil
}

func (s *Store) DeleteSavedItem(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.savedItems[id]
	if !exists || item.WorkspaceID != workspace || item.UserID != user {
		return store.ErrNotFound
	}
	delete(s.savedItems, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateBookmark(_ context.Context, bookmark domain.Bookmark, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[bookmark.Conversation]; !ok {
		return store.ErrNotFound
	}
	if _, ok := s.users[bookmark.UpdatedBy]; !ok {
		return store.ErrNotFound
	}
	if _, exists := s.bookmarks[bookmark.ID]; exists {
		return store.ErrAlreadyExists
	}
	count := 0
	for _, existing := range s.bookmarks {
		if existing.WorkspaceID == bookmark.WorkspaceID && existing.Conversation == bookmark.Conversation {
			count++
		}
	}
	if count >= domain.MaxBookmarksPerConversation {
		return store.ErrBookmarkLimit
	}
	s.bookmarks[bookmark.ID] = bookmark
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetBookmark(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.BookmarkID) (domain.Bookmark, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bookmark, ok := s.bookmarks[id]
	if !ok || bookmark.WorkspaceID != workspace || bookmark.Conversation != conversation {
		return domain.Bookmark{}, store.ErrNotFound
	}
	return bookmark, nil
}

func (s *Store) ListBookmarks(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.Bookmark, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Bookmark, 0)
	for _, bookmark := range s.bookmarks {
		if bookmark.WorkspaceID == workspace && bookmark.Conversation == conversation {
			values = append(values, bookmark)
		}
	}
	slices.SortFunc(values, func(left, right domain.Bookmark) int {
		if left.CreatedAt.Before(right.CreatedAt) {
			return -1
		}
		if left.CreatedAt.After(right.CreatedAt) {
			return 1
		}
		return strings.Compare(string(left.ID), string(right.ID))
	})
	if len(values) > domain.MaxBookmarksPerConversation {
		values = values[:domain.MaxBookmarksPerConversation]
	}
	return values, nil
}

func (s *Store) UpdateBookmark(_ context.Context, bookmark domain.Bookmark, event events.Event) (domain.Bookmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.bookmarks[bookmark.ID]
	if !ok || current.WorkspaceID != bookmark.WorkspaceID || current.Conversation != bookmark.Conversation {
		return domain.Bookmark{}, store.ErrNotFound
	}
	bookmark.CreatedAt = current.CreatedAt
	s.bookmarks[bookmark.ID] = bookmark
	s.outbox = append(s.outbox, event)
	return bookmark, nil
}

func (s *Store) DeleteBookmark(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.BookmarkID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bookmark, ok := s.bookmarks[id]
	if !ok || bookmark.WorkspaceID != workspace || bookmark.Conversation != conversation {
		return store.ErrNotFound
	}
	delete(s.bookmarks, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateReminder(_ context.Context, reminder domain.Reminder, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[reminder.User]
	if !ok || user.WorkspaceID != reminder.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if _, exists := s.reminders[reminder.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.reminders[reminder.ID] = reminder
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetReminder(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID) (domain.Reminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reminder, ok := s.reminders[id]
	if !ok || reminder.WorkspaceID != workspace || reminder.User != user {
		return domain.Reminder{}, store.ErrNotFound
	}
	return reminder, nil
}

func (s *Store) ListReminders(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.ReminderPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ReminderPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ReminderPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Reminder, 0, request.Limit+1)
	for _, reminder := range s.reminders {
		if reminder.WorkspaceID != workspace || reminder.User != user || string(reminder.ID) <= after {
			continue
		}
		values = appendSorted(values, reminder, request.Limit+1, func(left, right domain.Reminder) bool { return left.ID < right.ID })
	}
	page := domain.ReminderPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	page.Reminders = values
	return page, err
}

func (s *Store) CompleteReminder(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID, completed time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.reminders[id]
	if !ok || reminder.WorkspaceID != workspace || reminder.User != user {
		return store.ErrNotFound
	}
	reminder.CompleteAt = completed
	s.reminders[id] = reminder
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteReminder(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.reminders[id]
	if !ok || reminder.WorkspaceID != workspace || reminder.User != user {
		return store.ErrNotFound
	}
	delete(s.reminders, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateScheduledMessage(_ context.Context, value domain.ScheduledMessage, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[value.Author]
	if !ok || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if _, ok := s.conversations[value.Channel]; !ok {
		return store.ErrNotFound
	}
	if _, exists := s.scheduled[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.scheduled[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateScheduledMessageWithinLimit(_ context.Context, value domain.ScheduledMessage, window time.Duration, limit int, event events.Event) error {
	if window <= 0 || limit <= 0 {
		return store.InvalidArgument("scheduled-message window and limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[value.Author]
	if !ok || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if _, ok := s.conversations[value.Channel]; !ok {
		return store.ErrNotFound
	}
	if _, exists := s.scheduled[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	nearby := make([]time.Time, 0, limit)
	for id, scheduled := range s.scheduled {
		if scheduled.WorkspaceID == value.WorkspaceID && scheduled.Channel == value.Channel &&
			!s.scheduledDelivered[id] && scheduled.FailedAt.IsZero() &&
			!scheduled.PostAt.Before(value.PostAt.Add(-window)) && !scheduled.PostAt.After(value.PostAt.Add(window)) {
			nearby = append(nearby, scheduled.PostAt)
		}
	}
	if store.ScheduledMessageLimitExceeded(nearby, value.PostAt, window, limit) {
		return store.ErrScheduledMessageLimit
	}
	s.scheduled[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListScheduledMessages(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ScheduledMessage, 0, request.Limit+1)
	for _, value := range s.scheduled {
		if value.WorkspaceID != workspace || value.Author != user || s.scheduledDelivered[value.ID] || (channel != "" && value.Channel != channel) || (after != "" && string(value.ID) <= after) {
			continue
		}
		values = appendSorted(values, value, request.Limit+1, func(left, right domain.ScheduledMessage) bool { return left.ID < right.ID })
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.ScheduledMessagePage{Items: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) ListScheduledMessagesForCredential(_ context.Context, workspace domain.WorkspaceID, query domain.ScheduledMessageQuery) (domain.ScheduledMessagePage, error) {
	if query.CredentialHash == "" {
		return domain.ScheduledMessagePage{}, store.InvalidArgument("scheduled-message credential is required")
	}
	if err := store.CheckAscendingPage(query.Page); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	afterTime, afterID, err := store.ParseScheduledMessageCursor(query.Page.Cursor)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ScheduledMessage, 0, query.Page.Limit+1)
	for _, value := range s.scheduled {
		if value.WorkspaceID != workspace || value.CredentialHash != query.CredentialHash ||
			s.scheduledDelivered[value.ID] || !value.FailedAt.IsZero() ||
			(query.Channel != "" && value.Channel != query.Channel) ||
			(!query.Oldest.IsZero() && value.PostAt.Before(query.Oldest)) ||
			(!query.Latest.IsZero() && value.PostAt.After(query.Latest)) ||
			(!afterTime.IsZero() && (value.PostAt.Before(afterTime) || (value.PostAt.Equal(afterTime) && value.ID <= afterID))) {
			continue
		}
		values = appendSorted(values, value, query.Page.Limit+1, func(left, right domain.ScheduledMessage) bool {
			return left.PostAt.Before(right.PostAt) || (left.PostAt.Equal(right.PostAt) && left.ID < right.ID)
		})
	}
	hasMore := len(values) > query.Page.Limit
	if hasMore {
		values = values[:query.Page.Limit]
	}
	page := domain.ScheduledMessagePage{Items: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(store.ScheduledMessageCursorKey(values[len(values)-1]))
	}
	return page, err
}

func (s *Store) EarliestScheduledMessage(_ context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for id, value := range s.scheduled {
		if (workspace != "" && value.WorkspaceID != workspace) || s.scheduledDelivered[id] || !value.FailedAt.IsZero() {
			continue
		}
		deadline := value.PostAt.UTC()
		if next := s.scheduledNextAttempt[id]; next.After(deadline) {
			deadline = next
		}
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, nil
}

func (s *Store) DeleteScheduledMessage(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.scheduled[id]
	lease, leased := s.scheduledLeases[id]
	if !ok || value.WorkspaceID != workspace || value.Author != user || value.Channel != channel || (leased && lease.Expires.After(time.Now().UTC())) {
		return store.ErrNotFound
	}
	delete(s.scheduled, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteScheduledMessageForCredential(_ context.Context, workspace domain.WorkspaceID, credentialHash string, channel domain.ConversationID, id domain.ScheduledMessageID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.scheduled[id]
	lease, leased := s.scheduledLeases[id]
	if credentialHash == "" || !ok || value.WorkspaceID != workspace || value.CredentialHash != credentialHash || value.Channel != channel ||
		s.scheduledDelivered[id] || !value.FailedAt.IsZero() || (leased && lease.Expires.After(time.Now().UTC())) {
		return store.ErrNotFound
	}
	delete(s.scheduled, id)
	delete(s.scheduledNextAttempt, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ClaimScheduledMessages(_ context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration) ([]domain.ScheduledMessage, error) {
	if owner == "" || limit <= 0 || lease <= 0 {
		return nil, store.InvalidArgument("scheduled claim requires owner, positive limit, and lease")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]domain.ScheduledMessage, 0, len(s.scheduled))
	for _, value := range s.scheduled {
		if (workspace != "" && value.WorkspaceID != workspace) || value.PostAt.After(now) || s.scheduledDelivered[value.ID] || !value.FailedAt.IsZero() || s.scheduledNextAttempt[value.ID].After(now) {
			continue
		}
		active, exists := s.scheduledLeases[value.ID]
		if exists && active.Expires.After(now) {
			continue
		}
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].PostAt.Before(values[right].PostAt) ||
			(values[left].PostAt.Equal(values[right].PostAt) && values[left].ID < values[right].ID)
	})
	if len(values) > limit {
		values = values[:limit]
	}
	for _, value := range values {
		s.scheduledLeases[value.ID] = memoryLease{Owner: owner, Expires: now.Add(lease)}
	}
	return values, nil
}

func (s *Store) RenewScheduledMessage(_ context.Context, owner string, id domain.ScheduledMessageID, lease time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.scheduledLeases[id]
	if !ok || current.Owner != owner || !current.Expires.After(time.Now().UTC()) {
		return store.ErrLeaseConflict
	}
	current.Expires = time.Now().UTC().Add(lease)
	s.scheduledLeases[id] = current
	return nil
}

func (s *Store) MarkScheduledMessageDelivered(_ context.Context, owner string, id domain.ScheduledMessageID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.scheduledLeases[id]
	if !ok || current.Owner != owner || !current.Expires.After(time.Now().UTC()) {
		return store.ErrLeaseConflict
	}
	s.scheduledDelivered[id] = true
	value := s.scheduled[id]
	value.DeliveredAt = time.Now().UTC()
	s.scheduled[id] = value
	delete(s.scheduledLeases, id)
	return nil
}

func (s *Store) MarkScheduledMessageFailed(_ context.Context, owner string, id domain.ScheduledMessageID, failureCode string, failedAt time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.scheduledLeases[id]
	if failureCode == "" || !ok || current.Owner != owner || !current.Expires.After(time.Now().UTC()) {
		return store.ErrLeaseConflict
	}
	value := s.scheduled[id]
	value.FailureCode = failureCode
	value.FailedAt = failedAt.UTC()
	s.scheduled[id] = value
	delete(s.scheduledLeases, id)
	delete(s.scheduledNextAttempt, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ReleaseScheduledMessage(_ context.Context, owner string, id domain.ScheduledMessageID, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.scheduledLeases[id]
	if !ok || current.Owner != owner || !current.Expires.After(time.Now().UTC()) {
		return store.ErrLeaseConflict
	}
	delete(s.scheduledLeases, id)
	s.scheduledNextAttempt[id] = next.UTC()
	return nil
}

func cloneUserGroup(value domain.UserGroup) domain.UserGroup {
	value.Users = append([]domain.UserID(nil), value.Users...)
	value.Channels = append([]domain.ConversationID(nil), value.Channels...)
	return value
}

func (s *Store) CreateUserGroup(_ context.Context, value domain.UserGroup, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[value.WorkspaceID]; !ok {
		return store.ErrNotFound
	}
	if _, exists := s.userGroups[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	if s.userGroupHandleTakenLocked(value.WorkspaceID, value.Handle, value.ID) {
		return store.ErrAlreadyExists
	}
	s.userGroups[value.ID] = cloneUserGroup(value)
	s.outbox = append(s.outbox, event)
	return nil
}

// userGroupHandleTakenLocked mirrors the user_groups_workspace_handle unique
// index. Without it a workspace could hold two groups answering to the same
// @handle here while the SQL profiles rejected the second one.
func (s *Store) userGroupHandleTakenLocked(workspace domain.WorkspaceID, handle string, exclude domain.UserGroupID) bool {
	for id, group := range s.userGroups {
		if id != exclude && group.WorkspaceID == workspace && group.Handle == handle {
			return true
		}
	}
	return false
}

func (s *Store) GetUserGroup(_ context.Context, workspace domain.WorkspaceID, id domain.UserGroupID) (domain.UserGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.userGroups[id]
	if !ok || value.WorkspaceID != workspace {
		return domain.UserGroup{}, store.ErrNotFound
	}
	return cloneUserGroup(value), nil
}

func (s *Store) ListUserGroups(_ context.Context, workspace domain.WorkspaceID, includeDisabled bool, request domain.PageRequest) (domain.UserGroupPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserGroupPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserGroupPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.UserGroup, 0, request.Limit+1)
	for _, value := range s.userGroups {
		if value.WorkspaceID != workspace || (!includeDisabled && !value.Enabled) || string(value.ID) <= after {
			continue
		}
		values = appendSorted(values, cloneUserGroup(value), request.Limit+1, func(left, right domain.UserGroup) bool { return left.ID < right.ID })
	}
	page := domain.UserGroupPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	page.Groups = values
	return page, err
}

func (s *Store) UpdateUserGroup(_ context.Context, value domain.UserGroup, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.userGroups[value.ID]
	if !ok || current.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if s.userGroupHandleTakenLocked(value.WorkspaceID, value.Handle, value.ID) {
		return store.ErrAlreadyExists
	}
	value.Users = append([]domain.UserID(nil), current.Users...)
	value.Channels = append([]domain.ConversationID(nil), current.Channels...)
	s.userGroups[value.ID] = cloneUserGroup(value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetUserGroupEnabled(_ context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, enabled bool, actor domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.userGroups[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	value.Enabled = enabled
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	if !enabled {
		value.DeletedAt = value.UpdatedAt
	} else {
		value.DeletedAt = time.Time{}
	}
	s.userGroups[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetUserGroupUsers(_ context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, users []domain.UserID, actor domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.userGroups[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	value.Users = append([]domain.UserID(nil), users...)
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	s.userGroups[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetUserGroupChannels(_ context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, channels []domain.ConversationID, actor domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.userGroups[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	for _, channel := range channels {
		conversation, exists := s.conversations[channel]
		if !exists || conversation.WorkspaceID != workspace {
			return store.ErrNotFound
		}
	}
	value.Channels = append([]domain.ConversationID(nil), channels...)
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	s.userGroups[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func cloneCall(value domain.Call) domain.Call {
	value.Participants = append([]domain.UserID(nil), value.Participants...)
	return value
}

func (s *Store) CreateCall(_ context.Context, value domain.Call, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.calls {
		if existing.WorkspaceID == value.WorkspaceID && existing.ExternalUniqueID == value.ExternalUniqueID {
			return store.ErrAlreadyExists
		}
	}
	s.calls[value.ID] = cloneCall(value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetCall(_ context.Context, workspace domain.WorkspaceID, id domain.CallID) (domain.Call, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.calls[id]
	if !ok || value.WorkspaceID != workspace {
		return domain.Call{}, store.ErrNotFound
	}
	return cloneCall(value), nil
}

func (s *Store) UpdateCall(_ context.Context, value domain.Call, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.calls[value.ID]
	if !ok || existing.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if value.Title != "" {
		existing.Title = value.Title
	}
	if value.JoinURL != "" {
		existing.JoinURL = value.JoinURL
	}
	if value.DesktopAppJoinURL != "" {
		existing.DesktopAppJoinURL = value.DesktopAppJoinURL
	}
	s.calls[value.ID] = existing
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) EndCall(_ context.Context, workspace domain.WorkspaceID, id domain.CallID, duration int64, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.calls[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if !value.EndedAt.IsZero() {
		return store.ErrAlreadyExists
	}
	value.EndedAt = time.Now().UTC()
	value.DurationSeconds = duration
	s.calls[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetCallParticipants(_ context.Context, workspace domain.WorkspaceID, id domain.CallID, users []domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.calls[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	value.Participants = append([]domain.UserID(nil), users...)
	s.calls[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateFile(_ context.Context, file domain.File, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.files[file.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.files[file.ID] = file
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SeedFileComment(value domain.FileComment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileComments[value.ID] = value
}

func (s *Store) DeleteFileComment(_ context.Context, workspace domain.WorkspaceID, fileID domain.FileID, commentID domain.FileCommentID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, fileExists := s.files[fileID]
	comment, commentExists := s.fileComments[commentID]
	if !fileExists || file.Deleted || file.WorkspaceID != workspace || !commentExists || comment.Deleted || comment.File != fileID || comment.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	comment.Deleted = true
	s.fileComments[commentID] = comment
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetFile(_ context.Context, id domain.FileID) (domain.File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, ok := s.files[id]
	if !ok || file.Deleted {
		return domain.File{}, store.ErrNotFound
	}
	file.SharedChannels = append([]domain.ConversationID(nil), s.fileShares[id]...)
	return file, nil
}

func (s *Store) DeleteFile(_ context.Context, id domain.FileID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[id]
	if !ok || file.Deleted {
		return store.ErrNotFound
	}
	file.Deleted = true
	s.files[id] = file
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ShareFilePublic(_ context.Context, workspace domain.WorkspaceID, id domain.FileID, token string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[id]
	if !ok || file.Deleted || file.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	for _, existing := range s.files {
		if existing.PublicToken == token && existing.ID != id {
			return store.ErrAlreadyExists
		}
	}
	file.PublicToken = token
	s.files[id] = file
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RevokeFilePublic(_ context.Context, workspace domain.WorkspaceID, id domain.FileID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[id]
	if !ok || file.Deleted || file.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	file.PublicToken = ""
	s.files[id] = file
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetPublicFile(_ context.Context, token string) (domain.File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, file := range s.files {
		if !file.Deleted && file.PublicToken == token && token != "" {
			return file, nil
		}
	}
	return domain.File{}, store.ErrNotFound
}

func (s *Store) ListFiles(_ context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.FilePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.FilePage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.FilePage{}, err
	}
	s.mu.RLock()
	values := make([]domain.File, 0, request.Limit+1)
	for _, file := range s.files {
		if file.WorkspaceID == workspace && !file.Deleted && (after == "" || string(file.ID) > after) {
			values = appendSorted(values, file, request.Limit+1, func(left, right domain.File) bool { return left.ID < right.ID })
		}
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.FilePage{Files: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.FilePage{}, err
		}
	}
	return page, nil
}

func (s *Store) WalkBlobReferences(ctx context.Context, workspace domain.WorkspaceID, visit func(string) error) error {
	if visit == nil {
		return store.InvalidArgument("blob reference visitor is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, reference := range s.blobReferences(workspace) {
		if err := visit(reference); err != nil {
			return err
		}
	}
	return nil
}

// blobReferences exists so the read lock is released by defer rather than by a
// hand-placed RUnlock on every exit path. The previous shape returned early on a
// malformed photo URL while still holding s.mu for reading, which deadlocked the
// process permanently on the next write — and the malformed URL was reachable by
// any member through users.profile.set. A profile photo URL this deployment did
// not mint names no blob of ours, so it is skipped rather than treated as a
// corrupt database.
func (s *Store) blobReferences(workspace domain.WorkspaceID) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	references := make([]string, 0)
	for _, file := range s.files {
		if file.WorkspaceID == workspace && !file.Deleted {
			references = append(references, file.BlobKey)
		}
	}
	for _, user := range s.users {
		if user.WorkspaceID != workspace || user.Deleted {
			continue
		}
		if key, ok := domain.UserPhotoBlobKey(workspace, user.ID, user.Profile.Image24); ok {
			references = append(references, key)
		}
	}
	return references
}

func (s *Store) AddRemoteFile(_ context.Context, value domain.RemoteFile, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.remoteFiles[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	for _, existing := range s.remoteFiles {
		if existing.WorkspaceID == value.WorkspaceID && existing.ExternalID == value.ExternalID && !existing.Deleted {
			return store.ErrAlreadyExists
		}
	}
	s.remoteFiles[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetRemoteFile(_ context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup) (domain.RemoteFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.remoteFiles {
		if value.WorkspaceID != workspace || value.Deleted || (lookup.ID != "" && value.ID != lookup.ID) || (lookup.ExternalID != "" && value.ExternalID != lookup.ExternalID) {
			continue
		}
		value.SharedChannels = append([]domain.ConversationID(nil), s.remoteFileShares[value.ID]...)
		return value, nil
	}
	return domain.RemoteFile{}, store.ErrNotFound
}

func (s *Store) ListRemoteFiles(_ context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.RemoteFilePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.RemoteFilePage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.RemoteFilePage{}, err
	}
	s.mu.RLock()
	values := make([]domain.RemoteFile, 0, request.Limit+1)
	for _, value := range s.remoteFiles {
		if value.WorkspaceID == workspace && !value.Deleted && (after == "" || string(value.ID) > after) {
			value.SharedChannels = append([]domain.ConversationID(nil), s.remoteFileShares[value.ID]...)
			values = appendSorted(values, value, request.Limit+1, func(left, right domain.RemoteFile) bool { return left.ID < right.ID })
		}
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.RemoteFilePage{Files: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.RemoteFilePage{}, err
		}
	}
	return page, nil
}

func (s *Store) RemoveRemoteFile(_ context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.remoteFiles {
		if value.WorkspaceID == workspace && !value.Deleted && ((lookup.ID != "" && id == lookup.ID) || (lookup.ExternalID != "" && value.ExternalID == lookup.ExternalID)) {
			value.Deleted = true
			s.remoteFiles[id] = value
			s.outbox = append(s.outbox, event)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *Store) SetRemoteFileShares(_ context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup, channels []domain.ConversationID, event events.Event) (domain.RemoteFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.remoteFiles {
		if value.WorkspaceID != workspace || value.Deleted || (lookup.ID != "" && id != lookup.ID) || (lookup.ExternalID != "" && value.ExternalID != lookup.ExternalID) {
			continue
		}
		for _, channel := range channels {
			conversation, exists := s.conversations[channel]
			if !exists || conversation.WorkspaceID != workspace || conversation.IsDirect || conversation.IsGroupDirect {
				return domain.RemoteFile{}, store.ErrNotFound
			}
		}
		s.remoteFileShares[id] = append([]domain.ConversationID(nil), channels...)
		value.SharedChannels = append([]domain.ConversationID(nil), channels...)
		s.remoteFiles[id] = value
		s.outbox = append(s.outbox, event)
		return value, nil
	}
	return domain.RemoteFile{}, store.ErrNotFound
}

func (s *Store) UpdateRemoteFile(_ context.Context, workspace domain.WorkspaceID, value domain.RemoteFile, event events.Event) (domain.RemoteFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.remoteFiles[value.ID]
	if !ok || existing.WorkspaceID != workspace || existing.Deleted {
		return domain.RemoteFile{}, store.ErrNotFound
	}
	value.WorkspaceID = workspace
	value.ExternalID = existing.ExternalID
	value.CreatedAt = existing.CreatedAt
	value.Deleted = false
	value.SharedChannels = append([]domain.ConversationID(nil), s.remoteFileShares[value.ID]...)
	s.remoteFiles[value.ID] = value
	s.outbox = append(s.outbox, event)
	return value, nil
}

func (s *Store) messageLocked(id domain.MessageID) (domain.Message, error) {
	for _, values := range s.messages {
		for _, message := range values {
			if message.ID == id {
				return message, nil
			}
		}
	}
	return domain.Message{}, store.ErrNotFound
}

func (s *Store) UpdateMessage(_ context.Context, message domain.Message, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := s.messages[message.Conversation]
	for index := range values {
		if values[index].ID == message.ID {
			message.Unfurls = copyUnfurls(message.Unfurls)
			values[index] = message
			s.messages[message.Conversation] = values
			s.outbox = append(s.outbox, event)
			return nil
		}
	}
	return store.ErrNotFound
}

// cloneInviteRequest, cloneWorkspace and cloneMessage exist for the same reason
// cloneUserGroup and cloneCall do: a value returned from this repository must not
// alias the slice or map that still lives inside it, or a caller can mutate store
// state without the lock. These three aggregates were the ones still handed out
// by reference.
func cloneInviteRequest(value domain.InviteRequest) domain.InviteRequest {
	value.ChannelIDs = append([]domain.ConversationID(nil), value.ChannelIDs...)
	return value
}

func cloneWorkspace(value domain.Workspace) domain.Workspace {
	value.DefaultChannelIDs = append([]domain.ConversationID(nil), value.DefaultChannelIDs...)
	return value
}

func cloneMessage(value domain.Message) domain.Message {
	if value.Unfurls != nil {
		value.Unfurls = copyUnfurls(value.Unfurls)
	}
	value.Files = append([]domain.File(nil), value.Files...)
	for index := range value.Files {
		value.Files[index].SharedChannels = append([]domain.ConversationID(nil), value.Files[index].SharedChannels...)
	}
	return value
}

func copyUnfurls(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, raw := range value {
		result[key] = raw
	}
	return result
}

func (s *Store) Outbox() []events.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]events.Event(nil), s.outbox...)
}
func (s *Store) ListEventsAfter(_ context.Context, workspace domain.WorkspaceID, after uint64, limit int) ([]events.Record, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("event limit must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]events.Record, 0, limit)
	for sequence, event := range s.outbox {
		current := uint64(sequence + 1)
		if current <= after || event.WorkspaceID != workspace || store.InternalTopic(event.Topic) {
			continue
		}
		result = append(result, events.Record{Sequence: current, Event: event})
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *Store) ListAppEventsAfter(_ context.Context, appID domain.AppID, after uint64, limit int) ([]events.Record, error) {
	if appID == "" || limit <= 0 {
		return nil, store.InvalidArgument("app ID and positive event limit are required")
	}
	s.mu.RLock()
	workspaces := make(map[domain.WorkspaceID]struct{})
	for _, installation := range s.appInstallations {
		if installation.AppID == appID && installation.Enabled {
			workspaces[installation.WorkspaceID] = struct{}{}
		}
	}
	result := make([]events.Record, 0, limit)
	for sequence, event := range s.outbox {
		current := uint64(sequence + 1)
		if current <= after || store.InternalTopic(event.Topic) {
			continue
		}
		if _, ok := workspaces[event.WorkspaceID]; !ok {
			continue
		}
		result = append(result, events.Record{Sequence: current, Event: event})
		if len(result) == limit {
			break
		}
	}
	s.mu.RUnlock()
	return result, nil
}

func appEventCursorKey(appID domain.AppID, surface string) string {
	return string(appID) + "\x00" + surface
}

func validAppEventSurface(surface string) bool {
	return surface == "http" || surface == "socket"
}

func (s *Store) ClaimAppEvent(_ context.Context, appID domain.AppID, surface, owner string, lease time.Duration) (events.Record, int, string, bool, error) {
	if appID == "" || !validAppEventSurface(surface) || strings.TrimSpace(owner) == "" || lease <= 0 {
		return events.Record{}, 0, "", false, store.InvalidArgument("app event claim fields are invalid")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaces := make(map[domain.WorkspaceID]struct{})
	for _, installation := range s.appInstallations {
		if installation.AppID == appID && installation.Enabled {
			workspaces[installation.WorkspaceID] = struct{}{}
		}
	}
	if len(workspaces) == 0 {
		return events.Record{}, 0, "", false, store.ErrNotFound
	}
	key := appEventCursorKey(appID, surface)
	cursor := s.appEventCursors[key]
	if cursor.LeasedSequence != 0 && cursor.LeaseUntil.After(now) {
		return events.Record{}, 0, "", false, nil
	}
	if cursor.RetryAt.After(now) {
		return events.Record{}, 0, "", false, nil
	}
	for index, event := range s.outbox {
		sequence := uint64(index + 1)
		if sequence <= cursor.Sequence || store.InternalTopic(event.Topic) {
			continue
		}
		if _, installed := workspaces[event.WorkspaceID]; !installed {
			continue
		}
		cursor.LeasedSequence = sequence
		cursor.LeaseOwner = owner
		cursor.LeaseUntil = now.Add(lease)
		s.appEventCursors[key] = cursor
		return events.Record{Sequence: sequence, Event: event}, cursor.RetryCount, cursor.RetryReason, true, nil
	}
	return events.Record{}, 0, "", false, nil
}

func (s *Store) AckAppEvent(_ context.Context, appID domain.AppID, surface, owner string, sequence uint64) error {
	if appID == "" || !validAppEventSurface(surface) || strings.TrimSpace(owner) == "" || sequence == 0 {
		return store.InvalidArgument("app event acknowledgement fields are invalid")
	}
	now := time.Now().UTC()
	key := appEventCursorKey(appID, surface)
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, exists := s.appEventCursors[key]
	if !exists || cursor.LeasedSequence != sequence || cursor.LeaseOwner != owner {
		return store.ErrLeaseConflict
	}
	if !cursor.LeaseUntil.After(now) {
		return store.ErrLeaseConflict
	}
	cursor.Sequence = sequence
	cursor.LeasedSequence = 0
	cursor.LeaseOwner = ""
	cursor.LeaseUntil = time.Time{}
	cursor.RetryAt = time.Time{}
	cursor.RetryCount = 0
	cursor.RetryReason = ""
	s.appEventCursors[key] = cursor
	return nil
}

func (s *Store) ReleaseAppEvent(_ context.Context, appID domain.AppID, surface, owner string, sequence uint64, reason string, retryAt time.Time) error {
	if appID == "" || !validAppEventSurface(surface) || strings.TrimSpace(owner) == "" || sequence == 0 || strings.TrimSpace(reason) == "" || retryAt.IsZero() {
		return store.InvalidArgument("app event release fields are invalid")
	}
	now := time.Now().UTC()
	key := appEventCursorKey(appID, surface)
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, exists := s.appEventCursors[key]
	if !exists || cursor.LeasedSequence != sequence || cursor.LeaseOwner != owner {
		return store.ErrLeaseConflict
	}
	if !cursor.LeaseUntil.After(now) {
		return store.ErrLeaseConflict
	}
	cursor.LeasedSequence = 0
	cursor.LeaseOwner = ""
	cursor.LeaseUntil = time.Time{}
	cursor.RetryAt = retryAt.UTC()
	cursor.RetryCount++
	cursor.RetryReason = strings.TrimSpace(reason)
	s.appEventCursors[key] = cursor
	return nil
}

func (s *Store) ClaimEvents(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	return s.claimEvents(ctx, workspace, "", owner, limit, lease)
}

func (s *Store) ClaimEventsForTopic(ctx context.Context, workspace domain.WorkspaceID, topic, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	if topic == "" {
		return nil, store.InvalidArgument("topic is required")
	}
	return s.claimEvents(ctx, workspace, topic, owner, limit, lease)
}

func (s *Store) claimEvents(_ context.Context, workspace domain.WorkspaceID, topic, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	if workspace == "" || owner == "" || limit <= 0 || lease <= 0 {
		return nil, store.InvalidArgument("workspace, owner, positive limit, and positive lease are required")
	}
	now := time.Now().UTC()
	expires := now.Add(lease)
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]events.Record, 0, limit)
	for sequence, event := range s.outbox {
		current := uint64(sequence + 1)
		if len(result) == limit || event.WorkspaceID != workspace || (topic == "" && store.InternalTopic(event.Topic)) || (topic != "" && event.Topic != topic) || s.delivered[current] {
			continue
		}
		if next, ok := s.nextAttempt[current]; ok && next.After(now) {
			continue
		}
		active, ok := s.outboxLeases[current]
		if ok && active.Expires.After(now) {
			continue
		}
		s.outboxLeases[current] = memoryLease{Owner: owner, Expires: expires}
		delete(s.nextAttempt, current)
		result = append(result, events.Record{Sequence: current, Event: event})
	}
	return result, nil
}

func (s *Store) ReleaseEvents(_ context.Context, owner string, sequences []uint64, retryAt time.Time) error {
	if owner == "" || len(sequences) == 0 || !retryAt.After(time.Now().UTC()) {
		return store.InvalidArgument("owner, event sequences, and a future retry time are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sequence := range sequences {
		lease, ok := s.outboxLeases[sequence]
		if !ok || lease.Owner != owner || !lease.Expires.After(time.Now().UTC()) {
			return store.ErrLeaseConflict
		}
	}
	for _, sequence := range sequences {
		delete(s.outboxLeases, sequence)
		s.nextAttempt[sequence] = retryAt
	}
	return nil
}

func (s *Store) RenewEvents(_ context.Context, owner string, sequences []uint64, lease time.Duration) error {
	if owner == "" || len(sequences) == 0 || lease <= 0 {
		return store.InvalidArgument("owner, event sequences, and positive lease are required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sequence := range sequences {
		active, ok := s.outboxLeases[sequence]
		if !ok || active.Owner != owner || !active.Expires.After(now) {
			return store.ErrLeaseConflict
		}
	}
	for _, sequence := range sequences {
		leaseRecord := s.outboxLeases[sequence]
		leaseRecord.Expires = now.Add(lease)
		s.outboxLeases[sequence] = leaseRecord
	}
	return nil
}

func (s *Store) AckEvents(_ context.Context, owner string, sequences []uint64) error {
	if owner == "" || len(sequences) == 0 {
		return store.InvalidArgument("owner and event sequences are required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sequence := range sequences {
		lease, ok := s.outboxLeases[sequence]
		if !ok || lease.Owner != owner || !lease.Expires.After(now) {
			return store.ErrLeaseConflict
		}
	}
	for _, sequence := range sequences {
		s.delivered[sequence] = true
		delete(s.outboxLeases, sequence)
	}
	return nil
}

// ListMessages pages one conversation in either direction; see the port for why
// the descending direction exists.
//
// s.messages[conversation] is held in (CreatedAt, ID) order by CreateMessage, so
// the descending page is the same window read from the other end and reversed.
// The page boundary is decided by domain.PageRequest.PageAfter, the same
// predicate the SQL profiles put in their WHERE clause, so the two profiles
// cannot disagree about which row a cursor excludes.
func (s *Store) ListMessages(_ context.Context, conversation domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := store.CheckPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	values := s.messages[conversation]
	var createdAt time.Time
	var id domain.MessageID
	if request.Cursor != "" {
		var err error
		createdAt, id, err = domain.DecodeMessageCursor(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
	}
	window := make([]domain.Message, 0, request.Limit+1)
	if request.Descending {
		for index := len(values) - 1; index >= 0 && len(window) <= request.Limit; index-- {
			if values[index].Deleted {
				continue
			}
			if request.Cursor != "" && !request.PageAfter(values[index].CreatedAt, values[index].ID, createdAt, id) {
				continue
			}
			window = append(window, cloneMessage(values[index]))
		}
	} else {
		for index := 0; index < len(values) && len(window) <= request.Limit; index++ {
			if values[index].Deleted {
				continue
			}
			if request.Cursor != "" && !request.PageAfter(values[index].CreatedAt, values[index].ID, createdAt, id) {
				continue
			}
			window = append(window, cloneMessage(values[index]))
		}
	}
	hasMore := len(window) > request.Limit
	if hasMore {
		window = window[:request.Limit]
	}
	page := domain.MessagePage{Messages: window, HasMore: hasMore}
	if hasMore {
		cursor, err := domain.NewMessageCursor(window[len(window)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

func (s *Store) SearchMessages(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	terms := strings.Fields(domain.FoldSearchText(query))
	if len(terms) == 0 {
		return domain.MessagePage{}, store.InvalidArgument("search query must not be empty")
	}
	startTime, startID, err := domain.DecodeMessageCursor(request.Cursor)
	if err != nil {
		return domain.MessagePage{}, err
	}
	s.mu.RLock()
	values := make([]domain.Message, 0, request.Limit+1)
	total := 0
	for conversationID, messages := range s.messages {
		conversation, exists := s.conversations[conversationID]
		if !exists || conversation.WorkspaceID != workspace || conversation.IsPrivate {
			if !exists || conversation.WorkspaceID != workspace {
				continue
			}
			if _, member := s.memberships[conversationID][user]; !member {
				continue
			}
		}
		for _, message := range messages {
			if message.Deleted {
				continue
			}
			text := domain.FoldSearchText(message.Text)
			matches := true
			for _, term := range terms {
				if !strings.Contains(text, term) {
					matches = false
					break
				}
			}
			if matches {
				total++
				if request.Cursor != "" && (message.CreatedAt.Before(startTime) || (message.CreatedAt.Equal(startTime) && message.ID <= startID)) {
					continue
				}
				values = appendSorted(values, cloneMessage(message), request.Limit+1, messageBefore)
			}
		}
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore, Total: total}
	if hasMore {
		page.NextCursor, err = domain.NewMessageCursor(values[len(values)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
	}
	return page, nil
}

func (s *Store) ListThreadMessages(_ context.Context, conversation domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) (domain.MessagePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	startTime, startID, startRoot, err := domain.DecodeMessageCursorWithRoot(request.Cursor)
	if err != nil {
		return domain.MessagePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Message, 0, request.Limit+1)
	for _, message := range s.messages[conversation] {
		if message.Deleted {
			continue
		}
		if (message.ThreadTimestamp == "" && domain.NewMessageTimestamp(message.CreatedAt) == timestamp) || message.ThreadTimestamp == timestamp {
			if request.Cursor == "" || !threadMessageBeforeOrEqual(message, startTime, startID, startRoot, timestamp) {
				values = appendSorted(values, cloneMessage(message), request.Limit+1, func(left, right domain.Message) bool { return threadMessageBefore(left, right, timestamp) })
			}
		}
	}
	window := values
	hasMore := len(window) > request.Limit
	if hasMore {
		window = window[:request.Limit]
	}
	page := domain.MessagePage{Messages: window, HasMore: hasMore}
	if hasMore {
		cursor, err := domain.NewMessageCursor(window[len(window)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

func threadMessageBefore(left, right domain.Message, rootTimestamp domain.MessageTimestamp) bool {
	leftRoot := left.ThreadTimestamp == "" && domain.NewMessageTimestamp(left.CreatedAt) == rootTimestamp
	rightRoot := right.ThreadTimestamp == "" && domain.NewMessageTimestamp(right.CreatedAt) == rootTimestamp
	if leftRoot != rightRoot {
		return leftRoot
	}
	return messageBefore(left, right)
}

func threadMessageBeforeOrEqual(message domain.Message, cursorTime time.Time, cursorID domain.MessageID, cursorRoot bool, rootTimestamp domain.MessageTimestamp) bool {
	cursor := domain.Message{CreatedAt: cursorTime, ID: cursorID, ThreadTimestamp: rootTimestamp}
	if cursorRoot {
		cursor.ThreadTimestamp = ""
	}
	return !threadMessageBefore(cursor, message, rootTimestamp)
}

func listAccessKey(value domain.ListAccess) string {
	return string(value.ListID) + "\x00" + value.EntityType + "\x00" + value.EntityID
}

func (s *Store) CreateList(ctx context.Context, value domain.List, event events.Event) error {
	return s.CreateListWithItems(ctx, value, event, nil)
}

// CreateListWithItems creates a list and its initial items as one unit: either
// the list, every item and every event exist, or none of them do. See
// store.Store.CreateListWithItems.
func (s *Store) CreateListWithItems(_ context.Context, value domain.List, event events.Event, items []store.ListItemCreation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.lists[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	// Validate the whole batch before applying any of it, so a rejected item
	// cannot leave a partially created list behind. The SQL profile gets this
	// from its transaction; this one has to be written that way.
	created := make(map[domain.ListItemID]domain.ListItem, len(items))
	for _, creation := range items {
		// An empty identifier would key the map on "" here and insert a row with
		// an empty primary key on the SQL profiles. The two validation sets are
		// stated to be equivalent, so they have to reject the same input.
		if creation.Item.ID == "" {
			return store.ErrInvalidArgument
		}
		if creation.Item.ListID != value.ID || creation.Item.WorkspaceID != value.WorkspaceID {
			return store.ErrInvalidArgument
		}
		if _, duplicate := created[creation.Item.ID]; duplicate {
			return store.ErrAlreadyExists
		}
		if creation.Item.ParentItemID != "" {
			if _, exists := created[creation.Item.ParentItemID]; !exists {
				return store.ErrNotFound
			}
		}
		created[creation.Item.ID] = creation.Item
	}
	s.lists[value.ID] = value
	s.listItems[value.ID] = created
	s.outbox = append(s.outbox, event)
	for _, creation := range items {
		s.outbox = append(s.outbox, creation.Event)
	}
	return nil
}

func (s *Store) GetList(_ context.Context, workspace domain.WorkspaceID, id domain.ListID) (domain.List, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.lists[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.List{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) UpdateList(_ context.Context, value domain.List, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.lists[value.ID]
	if !exists || previous.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	value.CreatedAt = previous.CreatedAt
	s.lists[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateListItem(_ context.Context, value domain.ListItem, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, exists := s.lists[value.ListID]
	if !exists || list.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if value.ParentItemID != "" {
		if _, exists := s.listItems[value.ListID][value.ParentItemID]; !exists {
			return store.ErrNotFound
		}
	}
	if _, exists := s.listItems[value.ListID][value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.listItems[value.ListID][value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetListItem(_ context.Context, workspace domain.WorkspaceID, listID domain.ListID, id domain.ListItemID) (domain.ListItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list, exists := s.lists[listID]
	if !exists || list.WorkspaceID != workspace {
		return domain.ListItem{}, store.ErrNotFound
	}
	value, exists := s.listItems[listID][id]
	if !exists {
		return domain.ListItem{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListItems(_ context.Context, workspace domain.WorkspaceID, listID domain.ListID, request domain.PageRequest, archived bool) (domain.ListItemPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ListItemPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ListItemPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	list, exists := s.lists[listID]
	if !exists || list.WorkspaceID != workspace {
		return domain.ListItemPage{}, store.ErrNotFound
	}
	values := make([]domain.ListItem, 0, request.Limit+1)
	for _, value := range s.listItems[listID] {
		if !archived && value.Archived || (after != "" && string(value.ID) <= after) {
			continue
		}
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.ListItemPage{Items: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.ListItemPage{}, err
		}
	}
	return page, nil
}

func (s *Store) UpdateListItem(_ context.Context, value domain.ListItem, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, exists := s.lists[value.ListID]
	if !exists || list.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	previous, exists := s.listItems[value.ListID][value.ID]
	if !exists {
		return store.ErrNotFound
	}
	value.CreatedAt = previous.CreatedAt
	value.CreatedBy = previous.CreatedBy
	s.listItems[value.ListID][value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteListItem(ctx context.Context, workspace domain.WorkspaceID, listID domain.ListID, id domain.ListItemID, event events.Event) error {
	return s.DeleteListItems(ctx, workspace, listID, []domain.ListItemID{id}, event)
}

func (s *Store) DeleteListItems(_ context.Context, workspace domain.WorkspaceID, listID domain.ListID, ids []domain.ListItemID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, exists := s.lists[listID]
	if !exists || list.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if len(ids) == 0 {
		return store.InvalidArgument("list item IDs are required")
	}
	for _, id := range ids {
		if _, exists := s.listItems[listID][id]; !exists {
			return store.ErrNotFound
		}
	}
	for _, id := range ids {
		delete(s.listItems[listID], id)
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetListAccess(_ context.Context, value domain.ListAccess, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.lists[value.ListID]; !exists {
		return store.ErrNotFound
	}
	s.listAccess[listAccessKey(value)] = value
	s.outbox = append(s.outbox, event)
	return nil
}

// resolveAccessLocked reports the highest ranked grant a user holds on one
// document. Ownership counts as an owner grant, a grant recorded for the user
// counts directly, and a grant recorded for a channel counts when the user is a
// member of that channel in the document's workspace. It reports false when no
// grant applies, so a caller cannot mistake an absent grant for an empty one.
// Lists and canvases share it because they share the model.
func (s *Store) resolveAccessLocked(workspace domain.WorkspaceID, owner, userID domain.UserID, grants func(func(entityType, entityID, level string))) (string, string, string, bool) {
	user, exists := s.users[userID]
	if !exists || user.WorkspaceID != workspace || user.Deleted {
		return "", "", "", false
	}
	bestType, bestID, bestLevel := "", "", ""
	if owner == userID {
		bestType, bestID, bestLevel = "user", string(userID), store.AccessOwner
	}
	grants(func(entityType, entityID, level string) {
		switch entityType {
		case "user":
			if domain.UserID(entityID) != userID {
				return
			}
		case "channel":
			conversation, ok := s.conversations[domain.ConversationID(entityID)]
			if !ok || conversation.WorkspaceID != workspace {
				return
			}
			if _, member := s.memberships[domain.ConversationID(entityID)][userID]; !member {
				return
			}
		default:
			return
		}
		if store.BetterAccessGrant(entityType, entityID, level, bestType, bestID, bestLevel) {
			bestType, bestID, bestLevel = entityType, entityID, level
		}
	})
	if bestLevel == "" {
		return "", "", "", false
	}
	return bestType, bestID, bestLevel, true
}

// GetListAccess resolves the effective access one user has to one list. The
// grants written by SetListAccess had no reader, so nothing could enforce them
// and every workspace member could read and delete every other member's list.
func (s *Store) GetListAccess(_ context.Context, listID domain.ListID, userID domain.UserID) (domain.ListAccess, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list, exists := s.lists[listID]
	if !exists {
		return domain.ListAccess{}, store.ErrNotFound
	}
	entityType, entityID, level, ok := s.resolveAccessLocked(list.WorkspaceID, list.OwnerID, userID, func(visit func(string, string, string)) {
		for _, grant := range s.listAccess {
			if grant.ListID == listID {
				visit(grant.EntityType, grant.EntityID, grant.Access)
			}
		}
	})
	if !ok {
		return domain.ListAccess{}, store.ErrNotFound
	}
	return domain.ListAccess{ListID: listID, EntityType: entityType, EntityID: entityID, Access: level}, nil
}

// GetCanvasAccess resolves the effective access one user has to one canvas, by
// the same rules as GetListAccess.
func (s *Store) GetCanvasAccess(_ context.Context, canvasID domain.CanvasID, userID domain.UserID) (domain.CanvasAccess, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	canvas, exists := s.canvases[canvasID]
	if !exists {
		return domain.CanvasAccess{}, store.ErrNotFound
	}
	entityType, entityID, level, ok := s.resolveAccessLocked(canvas.WorkspaceID, canvas.OwnerID, userID, func(visit func(string, string, string)) {
		for _, grant := range s.canvasAccess {
			if grant.CanvasID == canvasID {
				visit(grant.EntityType, grant.EntityID, grant.Access)
			}
		}
	})
	if !ok {
		return domain.CanvasAccess{}, store.ErrNotFound
	}
	return domain.CanvasAccess{CanvasID: canvasID, EntityType: entityType, EntityID: entityID, Access: level}, nil
}

func (s *Store) DeleteListAccess(_ context.Context, value domain.ListAccess, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.lists[value.ListID]; !exists {
		return store.ErrNotFound
	}
	key := listAccessKey(value)
	if _, exists := s.listAccess[key]; !exists {
		return store.ErrNotFound
	}
	delete(s.listAccess, key)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateListDownload(_ context.Context, value domain.ListDownload, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, exists := s.lists[value.ListID]
	if !exists || list.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if _, exists := s.listDownloads[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.listDownloads[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetListDownload(_ context.Context, workspace domain.WorkspaceID, id domain.ListDownloadID) (domain.ListDownload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.listDownloads[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.ListDownload{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) CreateOpenIDRefreshToken(_ context.Context, value domain.OpenIDRefreshToken) error {
	if value.TokenHash == "" || value.ClientID == "" || value.WorkspaceID == "" || value.UserID == "" || !value.ExpiresAt.After(time.Now().UTC()) || len(domain.NormalizeScopes(value.Scopes)) == 0 {
		return store.InvalidArgument("invalid OpenID Connect refresh token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.openidRefreshTokens[value.TokenHash]; exists {
		return store.ErrAlreadyExists
	}
	s.openidRefreshTokens[value.TokenHash] = value
	return nil
}

func (s *Store) ExchangeOpenIDRefreshToken(_ context.Context, clientID, oldToken, accessToken, refreshToken string, token domain.OpenIDToken) (domain.OpenIDToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.openidRefreshTokens[domain.HashToken(oldToken)]
	if !exists || value.ClientID != clientID || !value.ExpiresAt.After(time.Now().UTC()) {
		return domain.OpenIDToken{}, store.ErrNotFound
	}
	delete(s.openidRefreshTokens, domain.HashToken(oldToken))
	value.TokenHash = domain.HashToken(refreshToken)
	value.ExpiresAt = time.Now().UTC().Add(30 * 24 * time.Hour)
	s.openidRefreshTokens[value.TokenHash] = value
	// The minted access token has to be recorded with the rotation. It was not,
	// so the token this method returned authenticated nothing: the bearer is
	// resolved through LookupToken, which reads this map.
	s.tokens[domain.HashToken(accessToken)] = domain.TokenRecord{WorkspaceID: value.WorkspaceID, UserID: value.UserID, Scopes: domain.NormalizeScopes(value.Scopes)}
	token.AccessToken = accessToken
	token.RefreshToken = refreshToken
	token.ClientID = value.ClientID
	token.WorkspaceID = value.WorkspaceID
	token.UserID = value.UserID
	token.Scopes = append([]string(nil), value.Scopes...)
	return token, nil
}

func (s *Store) CreateIncomingWebhook(_ context.Context, value domain.IncomingWebhook) error {
	if value.ID == "" || value.WorkspaceID == "" || value.AppID == "" || value.ConversationID == "" || value.UserID == "" || value.SecretHash == "" || value.CreatedAt.IsZero() {
		return store.ErrInvalidAppApproval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.incomingWebhooks {
		if existing.SecretHash == value.SecretHash {
			return store.ErrAlreadyExists
		}
	}
	s.incomingWebhooks[value.ID] = value
	return nil
}

func (s *Store) LookupIncomingWebhook(_ context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret string) (domain.IncomingWebhook, error) {
	if workspaceID == "" || appID == "" || secret == "" {
		return domain.IncomingWebhook{}, store.ErrNotFound
	}
	hash := domain.HashToken(secret)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.incomingWebhooks {
		if value.WorkspaceID == workspaceID && value.AppID == appID && value.SecretHash == hash && value.Enabled {
			return value, nil
		}
	}
	return domain.IncomingWebhook{}, store.ErrNotFound
}

func (s *Store) SetIncomingWebhookEnabled(_ context.Context, workspaceID domain.WorkspaceID, id domain.IncomingWebhookID, enabled bool, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.incomingWebhooks[id]
	if !ok || value.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	value.Enabled = enabled
	s.incomingWebhooks[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateExternalUpload(_ context.Context, value domain.ExternalUpload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.externalUploads[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.externalUploads[value.ID] = value
	return nil
}

func (s *Store) GetExternalUpload(_ context.Context, id domain.ExternalUploadID) (domain.ExternalUpload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.externalUploads[id]
	if !exists {
		return domain.ExternalUpload{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) MarkExternalUploadUploaded(_ context.Context, id domain.ExternalUploadID, uploadedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.externalUploads[id]
	if !exists {
		return store.ErrNotFound
	}
	if value.Status != domain.ExternalUploadPending || !value.ExpiresAt.After(time.Now().UTC()) {
		return store.ErrConflict
	}
	value.Status = domain.ExternalUploadUploaded
	value.UploadedAt = uploadedAt.UTC()
	s.externalUploads[id] = value
	return nil
}

func (s *Store) CompleteExternalUpload(ctx context.Context, id domain.ExternalUploadID, file domain.File, channels []domain.ConversationID, event events.Event) error {
	return s.CompleteExternalUploads(ctx, []domain.ExternalUploadCompletion{{ID: id, Title: file.Title}}, []domain.File{file}, channels, []events.Event{event}, nil, nil)
}

func (s *Store) CompleteExternalUploads(_ context.Context, completions []domain.ExternalUploadCompletion, files []domain.File, channels []domain.ConversationID, emitted []events.Event, messages []domain.Message, messageEvents []events.Event) error {
	if len(completions) == 0 || len(completions) != len(files) || len(files) != len(emitted) || len(messages) != len(messageEvents) {
		return store.ErrInvalidArgument
	}
	for index := range messages {
		normalized, err := normalizeMessage(messages[index])
		if err != nil {
			return err
		}
		messages[index] = normalized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seenUploads := make(map[domain.ExternalUploadID]struct{}, len(completions))
	seenFiles := make(map[domain.FileID]struct{}, len(files))
	previousUploads := make(map[domain.ExternalUploadID]domain.ExternalUpload, len(completions))
	for index, completion := range completions {
		value, exists := s.externalUploads[completion.ID]
		if !exists {
			return store.ErrNotFound
		}
		if value.Status != domain.ExternalUploadUploaded || !value.ExpiresAt.After(time.Now().UTC()) {
			return store.ErrConflict
		}
		if _, exists := seenUploads[completion.ID]; exists {
			return store.ErrInvalidArgument
		}
		if _, exists := seenFiles[files[index].ID]; exists {
			return store.ErrInvalidArgument
		}
		if _, exists := s.files[files[index].ID]; exists {
			return store.ErrAlreadyExists
		}
		seenUploads[completion.ID] = struct{}{}
		seenFiles[files[index].ID] = struct{}{}
		previousUploads[completion.ID] = value
	}
	outboxLength := len(s.outbox)
	for index, completion := range completions {
		value := s.externalUploads[completion.ID]
		file := files[index]
		value.Status = domain.ExternalUploadCompleted
		value.CompletedAt = file.CreatedAt.UTC()
		value.FileID = file.ID
		s.externalUploads[completion.ID] = value
		s.files[file.ID] = file
		s.fileShares[file.ID] = append([]domain.ConversationID(nil), channels...)
		s.outbox = append(s.outbox, emitted[index])
	}
	for index, message := range messages {
		if err := s.createMessageLocked(message, messageEvents[index], ""); err != nil {
			for uploadID, value := range previousUploads {
				s.externalUploads[uploadID] = value
			}
			for _, file := range files {
				delete(s.files, file.ID)
				delete(s.fileShares, file.ID)
			}
			for _, inserted := range messages[:index] {
				values := s.messages[inserted.Conversation]
				for position := range values {
					if values[position].ID == inserted.ID {
						s.messages[inserted.Conversation] = slices.Delete(values, position, position+1)
						break
					}
				}
			}
			s.outbox = s.outbox[:outboxLength]
			return err
		}
	}
	return nil
}
