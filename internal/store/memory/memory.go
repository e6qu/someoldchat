package memory

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type Store struct {
	mu                 sync.RWMutex
	workspaces         map[domain.WorkspaceID]domain.Workspace
	members            map[string]domain.WorkspaceMembership
	users              map[domain.UserID]domain.User
	scheduledStatuses  map[domain.ScheduledStatusID]domain.ScheduledStatus
	userExpirations    map[domain.UserID]time.Time
	conversations      map[domain.ConversationID]domain.Conversation
	conversationPrefs  map[domain.ConversationID]domain.ConversationPrefs
	conversationAccess map[domain.ConversationID][]domain.UserGroupID
	conversationTeams  map[domain.ConversationID]map[domain.WorkspaceID]struct{}
	sharedInvites      map[domain.SharedInviteID]domain.SharedInvite

	conversationOrg               map[domain.ConversationID]bool
	closedDirects                 map[string]struct{}
	inviteRequests                map[domain.InviteRequestID]domain.InviteRequest
	appApprovals                  map[domain.AppID]domain.AppApproval
	appInstallations              map[string]domain.AppInstallation
	appBotTokens                  map[string]string
	apps                          map[domain.AppID]domain.App
	appManifestRevisions          map[domain.AppID][]domain.AppManifestRevision
	appTriggers                   map[string]domain.AppTrigger
	appResponseURLs               map[string]domain.AppResponseURL
	appConfigurationTokens        map[string]domain.AppConfigurationToken
	appConfigurationRefreshTokens map[string]string
	permissionRequests            map[domain.AppRequestID]domain.AppPermissionRequest
	views                         map[domain.ViewID]domain.View
	workflowSteps                 map[domain.WorkflowStepID]domain.WorkflowStep
	workflows                     map[domain.WorkflowID]domain.WorkflowDefinition
	workflowRevisions             map[domain.WorkflowID][]domain.WorkflowRevision
	workflowTriggers              map[domain.WorkflowTriggerID]domain.WorkflowTrigger
	workflowEventCursor           map[domain.WorkspaceID]uint64
	workflowRuns                  map[domain.WorkflowRunID]domain.WorkflowRun
	automationPermissions         map[string]domain.AutomationPermission
	featuredWorkflows             map[domain.ConversationID][]domain.FeaturedWorkflow
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
	retentionPolicies             map[domain.WorkspaceID]domain.RetentionPolicy
	conversationRetention         map[domain.ConversationID]domain.ConversationRetention
	retentionSweptAt              map[domain.ConversationID]time.Time
	nextAttempt                   map[uint64]time.Time
	readCursors                   map[string]domain.ReadCursor
	workspaceNotificationPrefs    map[string]domain.WorkspaceNotificationPreferences
	conversationNotificationPrefs map[string]domain.ConversationNotificationPreferences
	threadFollows                 map[string]bool
	assistantThreads              map[string]domain.AssistantThread
	typing                        map[string]domain.TypingSignal
	activityItems                 map[domain.ActivityID]domain.ActivityItem
	activityPreferences           map[string]domain.ActivityPreferences
	searchHistory                 map[string]domain.SearchHistoryEntry
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
	laterReminders                map[domain.LaterReminderID]domain.LaterReminder
	laterReminderLeases           map[domain.LaterReminderID]memoryLease
	laterReminderNextAttempt      map[domain.LaterReminderID]time.Time
	scheduled                     map[domain.ScheduledMessageID]domain.ScheduledMessage
	scheduledLeases               map[domain.ScheduledMessageID]memoryLease
	scheduledDelivered            map[domain.ScheduledMessageID]bool
	scheduledNextAttempt          map[domain.ScheduledMessageID]time.Time
	drafts                        map[string]domain.Draft
	userGroups                    map[domain.UserGroupID]domain.UserGroup
	calls                         map[domain.CallID]domain.Call
	emojis                        map[string]domain.CustomEmoji
	canvases                      map[domain.CanvasID]domain.Canvas
	canvasAccess                  map[string]domain.CanvasAccess
	canvasRevisions               map[domain.CanvasID][]domain.CanvasRevision
	canvasComments                map[domain.CanvasCommentID]domain.CanvasComment
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
	return &Store{lists: make(map[domain.ListID]domain.List), listItems: make(map[domain.ListID]map[domain.ListItemID]domain.ListItem), listAccess: make(map[string]domain.ListAccess), listDownloads: make(map[domain.ListDownloadID]domain.ListDownload), fileShares: make(map[domain.FileID][]domain.ConversationID), externalUploads: make(map[domain.ExternalUploadID]domain.ExternalUpload), incomingWebhooks: make(map[domain.IncomingWebhookID]domain.IncomingWebhook), appDatastoreItems: make(map[string]domain.AppDatastoreItem), appInstallations: make(map[string]domain.AppInstallation), apps: make(map[domain.AppID]domain.App), appManifestRevisions: make(map[domain.AppID][]domain.AppManifestRevision), appTriggers: make(map[string]domain.AppTrigger), appResponseURLs: make(map[string]domain.AppResponseURL), appConfigurationTokens: make(map[string]domain.AppConfigurationToken), appConfigurationRefreshTokens: make(map[string]string), openidRefreshTokens: make(map[string]domain.OpenIDRefreshToken), workspaces: make(map[domain.WorkspaceID]domain.Workspace), members: make(map[string]domain.WorkspaceMembership), users: make(map[domain.UserID]domain.User), userExpirations: make(map[domain.UserID]time.Time), conversations: make(map[domain.ConversationID]domain.Conversation), conversationPrefs: make(map[domain.ConversationID]domain.ConversationPrefs), conversationAccess: make(map[domain.ConversationID][]domain.UserGroupID), conversationTeams: make(map[domain.ConversationID]map[domain.WorkspaceID]struct{}), sharedInvites: make(map[domain.SharedInviteID]domain.SharedInvite), conversationOrg: make(map[domain.ConversationID]bool), closedDirects: make(map[string]struct{}), inviteRequests: make(map[domain.InviteRequestID]domain.InviteRequest), appApprovals: make(map[domain.AppID]domain.AppApproval), permissionRequests: make(map[domain.AppRequestID]domain.AppPermissionRequest), views: make(map[domain.ViewID]domain.View), workflowSteps: make(map[domain.WorkflowStepID]domain.WorkflowStep), workflows: make(map[domain.WorkflowID]domain.WorkflowDefinition), workflowRevisions: make(map[domain.WorkflowID][]domain.WorkflowRevision), workflowTriggers: make(map[domain.WorkflowTriggerID]domain.WorkflowTrigger), workflowEventCursor: make(map[domain.WorkspaceID]uint64), workflowRuns: make(map[domain.WorkflowRunID]domain.WorkflowRun), automationPermissions: make(map[string]domain.AutomationPermission), featuredWorkflows: make(map[domain.ConversationID][]domain.FeaturedWorkflow), dialogs: make(map[domain.DialogID]domain.Dialog), bots: make(map[domain.BotID]domain.Bot), migrations: make(map[string]domain.UserMigration), oauthClients: make(map[string]domain.OAuthClient), oauthCodes: make(map[string]memoryOAuthCode), oauthRefreshGrants: make(map[string]domain.OAuthRefreshGrant), rtmConnections: make(map[string]domain.RTMConnection), socketConnections: make(map[string]domain.SocketModeConnection), socketConnectionActive: make(map[string]bool), socketResponses: make(map[string]domain.SocketModeResponse), socketInteractions: make(map[string]domain.SocketModeInteraction), socketCursors: make(map[domain.AppID]uint64), appEventCursors: make(map[string]memoryAppEventCursor), memberships: make(map[domain.ConversationID]map[domain.UserID]struct{}), tokens: make(map[string]domain.TokenRecord), appTokens: make(map[string]domain.AppTokenRecord), sessions: make(map[string]domain.SessionRecord), oidcLogoutTokens: make(map[string]time.Time), authMethods: make(map[string]domain.AuthMethod), externalIdentities: make(map[string]domain.ExternalIdentity), messages: make(map[domain.ConversationID][]domain.Message), outboxLeases: make(map[uint64]memoryLease), delivered: make(map[uint64]bool), idempotency: make(map[string]domain.MessageID), retentionPolicies: make(map[domain.WorkspaceID]domain.RetentionPolicy), conversationRetention: make(map[domain.ConversationID]domain.ConversationRetention), retentionSweptAt: make(map[domain.ConversationID]time.Time), nextAttempt: make(map[uint64]time.Time), readCursors: make(map[string]domain.ReadCursor), workspaceNotificationPrefs: make(map[string]domain.WorkspaceNotificationPreferences), conversationNotificationPrefs: make(map[string]domain.ConversationNotificationPreferences), threadFollows: make(map[string]bool), assistantThreads: make(map[string]domain.AssistantThread), typing: make(map[string]domain.TypingSignal), activityItems: make(map[domain.ActivityID]domain.ActivityItem), activityPreferences: make(map[string]domain.ActivityPreferences), reactions: make(map[domain.MessageID]map[string]domain.Reaction), pins: make(map[domain.MessageID]map[domain.UserID]domain.Pin), files: make(map[domain.FileID]domain.File), fileComments: make(map[domain.FileCommentID]domain.FileComment), remoteFiles: make(map[domain.FileID]domain.RemoteFile), remoteFileShares: make(map[domain.FileID][]domain.ConversationID), dnd: make(map[domain.UserID]domain.DoNotDisturb), stars: make(map[domain.UserID]map[domain.MessageID]domain.Star), savedItems: make(map[domain.SavedItemID]domain.SavedItem), reminders: make(map[domain.ReminderID]domain.Reminder), laterReminders: make(map[domain.LaterReminderID]domain.LaterReminder), laterReminderLeases: make(map[domain.LaterReminderID]memoryLease), laterReminderNextAttempt: make(map[domain.LaterReminderID]time.Time), scheduled: make(map[domain.ScheduledMessageID]domain.ScheduledMessage), scheduledLeases: make(map[domain.ScheduledMessageID]memoryLease), scheduledDelivered: make(map[domain.ScheduledMessageID]bool), scheduledNextAttempt: make(map[domain.ScheduledMessageID]time.Time), drafts: make(map[string]domain.Draft), userGroups: make(map[domain.UserGroupID]domain.UserGroup), calls: make(map[domain.CallID]domain.Call), emojis: make(map[string]domain.CustomEmoji), bookmarks: make(map[domain.BookmarkID]domain.Bookmark), canvases: make(map[domain.CanvasID]domain.Canvas), canvasAccess: make(map[string]domain.CanvasAccess), canvasRevisions: make(map[domain.CanvasID][]domain.CanvasRevision), canvasComments: make(map[domain.CanvasCommentID]domain.CanvasComment)}
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
	if canvas.Version == 0 {
		canvas.Version = 1
	}
	s.canvases[canvas.ID] = canvas
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) CreateCanvasWithAccess(_ context.Context, canvas domain.Canvas, event events.Event, access domain.CanvasAccess, accessEvent events.Event) error {
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
	if access.CanvasID != canvas.ID || access.EntityType == "" || access.EntityID == "" || access.Access == "" {
		return store.ErrInvalidArgument
	}
	if canvas.Version == 0 {
		canvas.Version = 1
	}
	s.canvases[canvas.ID] = canvas
	s.canvasAccess[canvasAccessKey(access)] = access
	s.outbox = append(s.outbox, event, accessEvent)
	return nil
}

func (s *Store) CreateChannelCanvas(_ context.Context, canvas domain.Canvas, event events.Event, channel domain.ConversationID, accessEvent events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, ok := s.conversations[channel]
	if !ok || conversation.WorkspaceID != canvas.WorkspaceID {
		return store.ErrNotFound
	}
	for _, existing := range s.canvasAccess {
		if existing.EntityType == "channel_canvas" && existing.EntityID == string(channel) {
			return store.ErrAlreadyExists
		}
	}
	if _, exists := s.canvases[canvas.ID]; exists {
		return store.ErrAlreadyExists
	}
	if user, ok := s.users[canvas.OwnerID]; !ok || user.WorkspaceID != canvas.WorkspaceID {
		return store.ErrNotFound
	}
	if canvas.Version == 0 {
		canvas.Version = 1
	}
	access := domain.CanvasAccess{CanvasID: canvas.ID, EntityType: "channel_canvas", EntityID: string(channel), Access: store.AccessWrite}
	s.canvases[canvas.ID] = canvas
	s.canvasAccess[canvasAccessKey(access)] = access
	s.outbox = append(s.outbox, event, accessEvent)
	return nil
}

func (s *Store) GetChannelCanvas(_ context.Context, workspace domain.WorkspaceID, channel domain.ConversationID) (domain.Canvas, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conversation, ok := s.conversations[channel]
	if !ok || conversation.WorkspaceID != workspace {
		return domain.Canvas{}, store.ErrNotFound
	}
	for _, access := range s.canvasAccess {
		if access.EntityType != "channel_canvas" || access.EntityID != string(channel) {
			continue
		}
		canvas, exists := s.canvases[access.CanvasID]
		if exists && canvas.WorkspaceID == workspace {
			return canvas, nil
		}
	}
	return domain.Canvas{}, store.ErrNotFound
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

func (s *Store) ListCanvases(_ context.Context, workspace domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.CanvasPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.CanvasPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.CanvasPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Canvas, 0, request.Limit+1)
	for _, canvas := range s.canvases {
		if canvas.WorkspaceID != workspace || (after != "" && string(canvas.ID) <= after) {
			continue
		}
		_, _, _, allowed := s.resolveAccessLocked(workspace, canvas.OwnerID, userID, func(visit func(string, string, string)) {
			for _, grant := range s.canvasAccess {
				if grant.CanvasID == canvas.ID {
					visit(grant.EntityType, grant.EntityID, grant.Access)
				}
			}
		})
		if allowed {
			values = append(values, canvas)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.CanvasPage{Canvases: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

// SearchCanvases mirrors the SQL profile, including the part that matters: the
// visibility test is the same one the directory applies, so a search cannot
// reveal the title of a canvas the reader could not have opened.
//
// The folded text is computed here rather than stored because this profile has
// no schema to store it in; the SQL profile keeps a column because decoding
// every document per query is only free when the whole workspace is a map.
func (s *Store) SearchCanvases(_ context.Context, workspace domain.WorkspaceID, userID domain.UserID, search domain.CanvasSearch) (domain.CanvasPage, error) {
	if err := store.CheckAscendingPage(search.Page); err != nil {
		return domain.CanvasPage{}, err
	}
	after, err := domain.DecodeListCursor(search.Page.Cursor)
	if err != nil {
		return domain.CanvasPage{}, err
	}
	terms := make([]string, 0, len(search.Terms))
	for _, term := range search.Terms {
		terms = append(terms, domain.FoldSearchText(term))
	}
	excluded := make([]string, 0, len(search.ExcludedTerms))
	for _, term := range search.ExcludedTerms {
		excluded = append(excluded, domain.FoldSearchText(term))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Canvas, 0, search.Page.Limit+1)
	for _, canvas := range s.canvases {
		if canvas.WorkspaceID != workspace || (after != "" && string(canvas.ID) <= after) {
			continue
		}
		if search.Owner != "" && canvas.OwnerID != search.Owner {
			continue
		}
		if search.ExcludedOwner != "" && canvas.OwnerID == search.ExcludedOwner {
			continue
		}
		if !search.After.IsZero() && canvas.UpdatedAt.Before(search.After) {
			continue
		}
		if !search.Before.IsZero() && !canvas.UpdatedAt.Before(search.Before) {
			continue
		}
		folded := domain.FoldSearchText(domain.CanvasSearchText(canvas.Title, canvas.DocumentContent))
		if !containsEveryTerm(folded, terms) || containsAnyTerm(folded, excluded) {
			continue
		}
		_, _, _, allowed := s.resolveAccessLocked(workspace, canvas.OwnerID, userID, func(visit func(string, string, string)) {
			for _, grant := range s.canvasAccess {
				if grant.CanvasID == canvas.ID {
					visit(grant.EntityType, grant.EntityID, grant.Access)
				}
			}
		})
		if allowed {
			values = append(values, canvas)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	hasMore := len(values) > search.Page.Limit
	if hasMore {
		values = values[:search.Page.Limit]
	}
	page := domain.CanvasPage{Canvases: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func containsEveryTerm(folded string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(folded, term) {
			return false
		}
	}
	return true
}

func containsAnyTerm(folded string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(folded, term) {
			return true
		}
	}
	return false
}

func (s *Store) UpdateCanvas(_ context.Context, canvas domain.Canvas, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.canvases[canvas.ID]
	if !ok || current.WorkspaceID != canvas.WorkspaceID {
		return store.ErrNotFound
	}
	if canvas.Version != current.Version+1 {
		return store.ErrConflict
	}
	canvas.CreatedAt = current.CreatedAt
	// The revision records what was superseded, so it is built from the state
	// being replaced rather than the one arriving.
	s.canvasRevisions[canvas.ID] = append(s.canvasRevisions[canvas.ID], domain.CanvasRevision{
		CanvasID: canvas.ID, WorkspaceID: canvas.WorkspaceID, Version: current.Version,
		Title: current.Title, DocumentContent: current.DocumentContent,
		EditedBy: event.ActorID, CreatedAt: canvas.UpdatedAt.UTC(),
	})
	if overflow := len(s.canvasRevisions[canvas.ID]) - domain.CanvasRevisionLimit; overflow > 0 {
		s.canvasRevisions[canvas.ID] = s.canvasRevisions[canvas.ID][overflow:]
	}
	s.canvases[canvas.ID] = canvas
	s.outbox = append(s.outbox, event)
	return nil
}

// ListCanvasGrants mirrors the SQL profile, including the order: a sharing list
// that reshuffled between page loads would make a member doubt what they just
// changed.
func (s *Store) ListCanvasGrants(_ context.Context, workspace domain.WorkspaceID, id domain.CanvasID) ([]domain.CanvasAccess, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	canvas, ok := s.canvases[id]
	if !ok || canvas.WorkspaceID != workspace {
		return nil, store.ErrNotFound
	}
	grants := make([]domain.CanvasAccess, 0, 8)
	for _, grant := range s.canvasAccess {
		if grant.CanvasID == id {
			grants = append(grants, grant)
		}
	}
	sortGrants(grants, func(grant domain.CanvasAccess) (string, string) { return grant.EntityType, grant.EntityID })
	return grants, nil
}

// ListListGrants answers for a list what ListCanvasGrants answers for a canvas.
func (s *Store) ListListGrants(_ context.Context, workspace domain.WorkspaceID, id domain.ListID) ([]domain.ListAccess, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.lists[id]
	if !ok || value.WorkspaceID != workspace {
		return nil, store.ErrNotFound
	}
	grants := make([]domain.ListAccess, 0, 8)
	for _, grant := range s.listAccess {
		if grant.ListID == id {
			grants = append(grants, grant)
		}
	}
	sortGrants(grants, func(grant domain.ListAccess) (string, string) { return grant.EntityType, grant.EntityID })
	return grants, nil
}

// sortGrants puts a document's grants in the one order both profiles answer in.
// Written once because a sharing list that reshuffled between two documents of
// different kinds would be the same defect twice.
func sortGrants[Grant any](grants []Grant, key func(Grant) (string, string)) {
	sort.Slice(grants, func(left, right int) bool {
		leftType, leftID := key(grants[left])
		rightType, rightID := key(grants[right])
		if leftType != rightType {
			return leftType < rightType
		}
		return leftID < rightID
	})
}

// canvasReadableLocked is the shared access question both comment paths ask,
// answered once so a create and a read cannot disagree about who may take part.
func (s *Store) canvasReadableLocked(workspace domain.WorkspaceID, user domain.UserID, id domain.CanvasID) bool {
	canvas, ok := s.canvases[id]
	if !ok || canvas.WorkspaceID != workspace {
		return false
	}
	_, _, _, allowed := s.resolveAccessLocked(workspace, canvas.OwnerID, user, func(visit func(string, string, string)) {
		for _, grant := range s.canvasAccess {
			if grant.CanvasID == id {
				visit(grant.EntityType, grant.EntityID, grant.Access)
			}
		}
	})
	return allowed
}

func (s *Store) CreateCanvasComment(_ context.Context, comment domain.CanvasComment, event events.Event) error {
	if comment.ID == "" || comment.CanvasID == "" || comment.UserID == "" || strings.TrimSpace(comment.Text) == "" {
		return store.InvalidArgument("a canvas comment requires an identifier, a canvas, an author and text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.canvasReadableLocked(comment.WorkspaceID, comment.UserID, comment.CanvasID) {
		return store.ErrNotFound
	}
	if _, exists := s.canvasComments[comment.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.canvasComments[comment.ID] = comment
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteCanvasComment(_ context.Context, workspace domain.WorkspaceID, id domain.CanvasCommentID, author domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	comment, ok := s.canvasComments[id]
	if !ok || comment.Deleted || comment.WorkspaceID != workspace || comment.UserID != author {
		return store.ErrNotFound
	}
	comment.Deleted = true
	s.canvasComments[id] = comment
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListCanvasComments(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.CanvasID, request domain.PageRequest) (domain.CanvasCommentPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.CanvasCommentPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.CanvasCommentPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.canvasReadableLocked(workspace, user, id) {
		return domain.CanvasCommentPage{}, store.ErrNotFound
	}
	values := make([]domain.CanvasComment, 0, request.Limit+1)
	for _, comment := range s.canvasComments {
		if comment.CanvasID != id || comment.WorkspaceID != workspace || comment.Deleted {
			continue
		}
		if after != "" && string(comment.ID) <= after {
			continue
		}
		values = append(values, comment)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	page := domain.CanvasCommentPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	page.Comments = values
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.CanvasCommentPage{}, err
		}
	}
	return page, nil
}

// ListCanvasRevisions mirrors the SQL profile: newest first, and readable
// exactly when the canvas is.
func (s *Store) ListCanvasRevisions(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.CanvasID, request domain.PageRequest) (domain.CanvasRevisionPage, error) {
	if err := store.CheckPage(request); err != nil {
		return domain.CanvasRevisionPage{}, err
	}
	before := int64(-1)
	if request.Cursor != "" {
		decoded, err := domain.DecodeListCursor(request.Cursor)
		if err != nil {
			return domain.CanvasRevisionPage{}, err
		}
		version, convErr := strconv.ParseInt(decoded, 10, 64)
		if convErr != nil {
			return domain.CanvasRevisionPage{}, domain.ErrInvalidCursor
		}
		before = version
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	canvas, ok := s.canvases[id]
	if !ok || canvas.WorkspaceID != workspace {
		return domain.CanvasRevisionPage{}, store.ErrNotFound
	}
	_, _, _, allowed := s.resolveAccessLocked(workspace, canvas.OwnerID, user, func(visit func(string, string, string)) {
		for _, grant := range s.canvasAccess {
			if grant.CanvasID == id {
				visit(grant.EntityType, grant.EntityID, grant.Access)
			}
		}
	})
	if !allowed {
		return domain.CanvasRevisionPage{}, store.ErrNotFound
	}
	stored := s.canvasRevisions[id]
	values := make([]domain.CanvasRevision, 0, len(stored))
	for index := len(stored) - 1; index >= 0; index-- {
		if before >= 0 && stored[index].Version >= before {
			continue
		}
		values = append(values, stored[index])
		if len(values) > request.Limit {
			break
		}
	}
	page := domain.CanvasRevisionPage{Revisions: values, HasMore: len(values) > request.Limit}
	if page.HasMore {
		page.Revisions = page.Revisions[:request.Limit]
		cursor, err := domain.NewListCursor(strconv.FormatInt(page.Revisions[len(page.Revisions)-1].Version, 10))
		if err != nil {
			return domain.CanvasRevisionPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
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

// RecordSharedInviteDecision mirrors the SQL profile: one row per invitation,
// so a decision that is later changed replaces the news rather than stacking a
// second, contradictory copy of it.
func (s *Store) RecordSharedInviteDecision(_ context.Context, invite domain.SharedInvite, actor domain.UserID, occurredAt time.Time) error {
	if invite.ID == "" || invite.InvitedBy == "" {
		return store.InvalidArgument("a shared invite decision requires an invitation and a requester")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := domain.ActivityIDFor(invite.InvitedBy, "shared_invite:"+string(invite.ID))
	s.activityItems[id] = domain.ActivityItem{
		ID: id, WorkspaceID: invite.WorkspaceID, UserID: invite.InvitedBy,
		Kinds: []domain.ActivityKind{domain.ActivityInvitation}, ActorID: actor,
		SharedInviteID: invite.ID, Conversation: invite.ConversationID, OccurredAt: occurredAt.UTC(),
	}
	return nil
}

// RecordListAssignment mirrors the SQL profile: one item per assignment, keyed
// so re-assigning the same item to the same member replaces the row rather than
// stacking a second copy of the same news.
func (s *Store) RecordListAssignment(_ context.Context, item domain.ListItem, actor domain.UserID, occurredAt time.Time) error {
	if item.AssigneeID == "" || item.ID == "" {
		return store.InvalidArgument("a list assignment requires an item and an assignee")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := domain.ActivityIDFor(item.AssigneeID, "list_assignment:"+string(item.ID))
	s.activityItems[id] = domain.ActivityItem{
		ID: id, WorkspaceID: item.WorkspaceID, UserID: item.AssigneeID,
		Kinds: []domain.ActivityKind{domain.ActivityInvitation}, ActorID: actor,
		ListItemID: item.ID, ListID: item.ListID, OccurredAt: occurredAt.UTC(),
	}
	return nil
}

func (s *Store) SetCanvasAccess(_ context.Context, access domain.CanvasAccess, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	canvas, ok := s.canvases[access.CanvasID]
	if !ok {
		return store.ErrNotFound
	}
	key := canvasAccessKey(access)
	_, existed := s.canvasAccess[key]
	s.canvasAccess[key] = access
	// A share reaches Activity only when it is news: re-granting access
	// someone already has is a no-op to them, and an actor sharing with
	// themselves has not been told anything. Both are the same rules the
	// conversation-invitation item follows.
	if !existed && access.EntityType == "user" && domain.UserID(access.EntityID) != event.ActorID {
		id := domain.ActivityIDFor(domain.UserID(access.EntityID), "canvas_share:"+string(access.CanvasID)+":"+string(event.ID))
		s.activityItems[id] = domain.ActivityItem{
			ID: id, WorkspaceID: canvas.WorkspaceID, UserID: domain.UserID(access.EntityID),
			Kinds: []domain.ActivityKind{domain.ActivityInvitation}, ActorID: event.ActorID,
			CanvasID: access.CanvasID, OccurredAt: event.CreatedAt.UTC(),
		}
	}
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

func (s *Store) ListAppAuthorizations(_ context.Context, appID domain.AppID, workspaceID domain.WorkspaceID) ([]domain.AppAuthorization, error) {
	if appID == "" || workspaceID == "" {
		return nil, store.InvalidArgument("app authorization identity is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	byKey := make(map[string]domain.AppAuthorization)
	for _, token := range s.tokens {
		if token.AppID != appID || token.WorkspaceID != workspaceID || token.Revoked || (!token.ExpiresAt.IsZero() && !token.ExpiresAt.After(now)) {
			continue
		}
		tokenType := strings.TrimSpace(token.TokenType)
		if tokenType != "bot" && tokenType != "user" {
			continue
		}
		key := tokenType + "\x00" + string(token.UserID) + "\x00" + string(token.BotID)
		value := byKey[key]
		value.AppID = appID
		value.WorkspaceID = workspaceID
		value.UserID = token.UserID
		value.BotID = token.BotID
		value.TokenType = tokenType
		value.Scopes = domain.NormalizeScopes(append(value.Scopes, token.Scopes...))
		byKey[key] = value
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]domain.AppAuthorization, 0, len(keys))
	for _, key := range keys {
		value := byKey[key]
		value.Scopes = append([]string(nil), value.Scopes...)
		result = append(result, value)
	}
	return result, nil
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
	alreadyRevoked := record.Revoked
	record.Revoked = true
	s.tokens[key] = record
	// The tokens_revoked announcement is minted here, inside the mutation:
	// revocation arrives at this repository directly across the auth seam, so
	// no service layer is guaranteed to be on the call path. Only an
	// application token produces one — a personal token has no app to tell —
	// and re-revoking announces nothing.
	if record.AppID != "" && !alreadyRevoked {
		event, err := events.TokensRevokedEvent(record.WorkspaceID, record.UserID, record.AppID, record.TokenType, time.Now().UTC())
		if err != nil {
			return err
		}
		s.outbox = append(s.outbox, event)
	}
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

// ListUserSessions mirrors the SQL profile: live sessions only, newest first,
// identified by the stored hash rather than the token.
func (s *Store) ListUserSessions(_ context.Context, workspace domain.WorkspaceID, user domain.UserID) ([]domain.WorkspaceSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	sessions := make([]domain.WorkspaceSession, 0, 8)
	for key, record := range s.sessions {
		if record.WorkspaceID != workspace || record.UserID != user || record.Revoked {
			continue
		}
		if !record.ExpiresAt.After(now) {
			continue
		}
		sessions = append(sessions, domain.WorkspaceSession{ID: key, UserID: record.UserID, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt})
	}
	sort.Slice(sessions, func(left, right int) bool {
		if !sessions[left].CreatedAt.Equal(sessions[right].CreatedAt) {
			return sessions[left].CreatedAt.After(sessions[right].CreatedAt)
		}
		return sessions[left].ID < sessions[right].ID
	})
	return sessions, nil
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
	return s.createUserLocked(user, membership, &event)
}

// createUserLocked is CreateUser without the lock or the event, so a larger
// transaction — accepting an invitation — can create the member it promised
// under the same lock as the rest of its writes.
func (s *Store) createUserLocked(user domain.User, membership domain.WorkspaceMembership, event *events.Event) error {
	if user.ID == "" || user.WorkspaceID == "" || user.Email == "" || user.Name == "" || membership.WorkspaceID != user.WorkspaceID || membership.UserID != user.ID || !membership.Active {
		return store.InvalidArgument("user and active workspace membership are required")
	}
	if membership.Role != domain.WorkspaceRoleMember && membership.Role != domain.WorkspaceRoleAdmin {
		return store.InvalidArgument("user membership role must be member or admin")
	}
	if (membership.Restricted && membership.UltraRestricted) || (membership.Guest() && membership.Role != domain.WorkspaceRoleMember) {
		return store.InvalidArgument("guest membership must have exactly one guest tier and the member role")
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
	if event != nil {
		s.outbox = append(s.outbox, *event)
	}
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
	if user.Profile.StatusText == profile.StatusText && user.Profile.StatusEmoji == profile.StatusEmoji && user.Profile.StatusExpiration.Equal(profile.StatusExpiration) {
		profile.ActiveScheduledStatusID = user.Profile.ActiveScheduledStatusID
	} else {
		profile.ActiveScheduledStatusID = ""
	}
	user.Profile = profile
	s.users[userID] = user
	s.outbox = append(s.outbox, changes...)
	return user, nil
}

func (s *Store) DueUserStatuses(_ context.Context, workspaceID domain.WorkspaceID, now time.Time, limit int) ([]domain.User, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("status expiration limit must be positive")
	}
	now = now.UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]domain.User, 0, limit)
	for _, user := range s.users {
		expiration := user.Profile.StatusExpiration
		if user.Deleted || expiration.IsZero() || expiration.After(now) || (workspaceID != "" && user.WorkspaceID != workspaceID) {
			continue
		}
		users = append(users, user)
	}
	sort.Slice(users, func(left, right int) bool {
		leftAt, rightAt := users[left].Profile.StatusExpiration, users[right].Profile.StatusExpiration
		if leftAt.Equal(rightAt) {
			return users[left].ID < users[right].ID
		}
		return leftAt.Before(rightAt)
	})
	if len(users) > limit {
		users = users[:limit]
	}
	return users, nil
}

func (s *Store) EarliestUserStatusExpiration(_ context.Context, workspaceID domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for _, user := range s.users {
		expiration := user.Profile.StatusExpiration
		if user.Deleted || expiration.IsZero() || (workspaceID != "" && user.WorkspaceID != workspaceID) {
			continue
		}
		if earliest.IsZero() || expiration.Before(earliest) {
			earliest = expiration
		}
	}
	return earliest, nil
}

func (s *Store) ExpireUserStatus(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, expected time.Time, expectedScheduledID domain.ScheduledStatusID, now time.Time, event events.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID || user.Deleted {
		return false, store.ErrNotFound
	}
	expiration := user.Profile.StatusExpiration
	if expiration.IsZero() || !expiration.Equal(expected.UTC()) || user.Profile.ActiveScheduledStatusID != expectedScheduledID || expiration.After(now.UTC()) {
		return false, nil
	}
	user.Profile.StatusText = ""
	user.Profile.StatusEmoji = ""
	user.Profile.StatusExpiration = time.Time{}
	user.Profile.ActiveScheduledStatusID = ""
	s.users[userID] = user
	s.outbox = append(s.outbox, event)
	return true, nil
}

func (s *Store) CreateScheduledStatus(_ context.Context, value domain.ScheduledStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scheduledStatuses == nil {
		s.scheduledStatuses = make(map[domain.ScheduledStatusID]domain.ScheduledStatus)
	}
	if _, exists := s.scheduledStatuses[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	user, ok := s.users[value.UserID]
	if !ok || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	membership, ok := s.members[string(value.WorkspaceID)+"\x00"+string(value.UserID)]
	if !ok || !membership.Active {
		return store.ErrNotFound
	}
	count := 0
	for _, scheduled := range s.scheduledStatuses {
		if scheduled.WorkspaceID == value.WorkspaceID && scheduled.UserID == value.UserID {
			count++
		}
	}
	if count >= 5 {
		return store.ErrScheduledStatusLimit
	}
	s.scheduledStatuses[value.ID] = value
	return nil
}

func (s *Store) GetScheduledStatus(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID) (domain.ScheduledStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.scheduledStatuses[id]
	if !ok || value.WorkspaceID != workspaceID || value.UserID != userID {
		return domain.ScheduledStatus{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListScheduledStatuses(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.ScheduledStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ScheduledStatus, 0, 5)
	for _, value := range s.scheduledStatuses {
		if value.WorkspaceID == workspaceID && value.UserID == userID {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].StartsAt.Equal(values[right].StartsAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].StartsAt.Before(values[right].StartsAt)
	})
	return values, nil
}

func (s *Store) UpdateScheduledStatus(_ context.Context, value domain.ScheduledStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.scheduledStatuses[value.ID]
	if !ok || current.WorkspaceID != value.WorkspaceID || current.UserID != value.UserID {
		return store.ErrNotFound
	}
	value.CreatedAt = current.CreatedAt
	s.scheduledStatuses[value.ID] = value
	return nil
}

func (s *Store) DeleteScheduledStatus(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.scheduledStatuses[id]
	if !ok || current.WorkspaceID != workspaceID || current.UserID != userID {
		return store.ErrNotFound
	}
	delete(s.scheduledStatuses, id)
	return nil
}

func (s *Store) DueScheduledStatuses(_ context.Context, workspaceID domain.WorkspaceID, now time.Time, limit int) ([]domain.ScheduledStatus, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("scheduled status limit must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ScheduledStatus, 0, limit)
	for _, value := range s.scheduledStatuses {
		if (workspaceID == "" || value.WorkspaceID == workspaceID) && !value.StartsAt.After(now.UTC()) {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].StartsAt.Equal(values[right].StartsAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].StartsAt.Before(values[right].StartsAt)
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *Store) EarliestScheduledStatusStart(_ context.Context, workspaceID domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for _, value := range s.scheduledStatuses {
		if workspaceID != "" && value.WorkspaceID != workspaceID {
			continue
		}
		if earliest.IsZero() || value.StartsAt.Before(earliest) {
			earliest = value.StartsAt
		}
	}
	return earliest, nil
}

func (s *Store) ActivateScheduledStatus(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID, expectedUpdatedAt, now time.Time, event events.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.scheduledStatuses[id]
	if !ok || value.WorkspaceID != workspaceID || value.UserID != userID {
		return false, nil
	}
	if !value.UpdatedAt.Equal(expectedUpdatedAt.UTC()) || value.StartsAt.After(now.UTC()) {
		return false, nil
	}
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID || user.Deleted {
		return false, store.ErrNotFound
	}
	delete(s.scheduledStatuses, id)
	if value.EndsAt.After(now.UTC()) {
		user.Profile.StatusText = value.StatusText
		user.Profile.StatusEmoji = value.StatusEmoji
		user.Profile.StatusExpiration = value.EndsAt
		user.Profile.ActiveScheduledStatusID = value.ID
		s.users[userID] = user
	}
	s.outbox = append(s.outbox, event)
	return true, nil
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
	if membership.Guest() && role != domain.WorkspaceRoleMember {
		return store.InvalidArgument("guest membership cannot be promoted")
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

func (s *Store) ListUsers(ctx context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.UserPage, error) {
	return s.listUsers(ctx, workspace, "", request)
}

// SearchUsers mirrors the SQL profile: the directory listing narrowed by a
// folded name, sharing the scan so browsing and searching cannot disagree about
// the page shape or the cursor.
func (s *Store) SearchUsers(ctx context.Context, workspace domain.WorkspaceID, query string, request domain.PageRequest) (domain.UserPage, error) {
	return s.listUsers(ctx, workspace, query, request)
}

func (s *Store) listUsers(_ context.Context, workspace domain.WorkspaceID, search string, request domain.PageRequest) (domain.UserPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	folded := domain.FoldSearchText(strings.TrimSpace(search))
	s.mu.RLock()
	values := make([]domain.User, 0, request.Limit+1)
	for _, user := range s.users {
		if user.WorkspaceID != workspace || (after != "" && string(user.ID) <= after) {
			continue
		}
		if folded != "" && !strings.Contains(domain.FoldSearchText(user.Name+"\n"+user.RealName+"\n"+user.Profile.DisplayName), folded) {
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

func (s *Store) ExpandDirectConversation(_ context.Context, expansion domain.DirectConversationExpansion, emitted []events.Event) error {
	if !expansion.History.Valid() || len(emitted) != 3 {
		return store.InvalidArgument("invalid direct conversation expansion")
	}
	sourceNotice, err := normalizeMessage(expansion.SourceNotice)
	if err != nil {
		return err
	}
	targetNotice, err := normalizeMessage(expansion.TargetNotice)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	source, exists := s.conversations[expansion.Source]
	if !exists || source.WorkspaceID != expansion.Target.WorkspaceID || (!source.IsDirect && !source.IsGroupDirect) {
		return store.ErrNotFound
	}
	if !expansion.Target.IsPrivate || expansion.Target.IsDirect || !expansion.Target.IsGroupDirect || len(expansion.Members) < 3 || len(expansion.Members) > 9 {
		return store.InvalidArgument("expanded conversation must be a group DM")
	}
	if _, exists := s.conversations[expansion.Target.ID]; exists {
		return store.ErrAlreadyExists
	}
	memberSet := make(map[domain.UserID]struct{}, len(expansion.Members))
	for _, member := range expansion.Members {
		if _, duplicate := memberSet[member]; duplicate {
			return store.InvalidArgument("expanded conversation contains duplicate members")
		}
		user, exists := s.users[member]
		if !exists || user.WorkspaceID != expansion.Target.WorkspaceID || user.Deleted {
			return store.ErrNotFound
		}
		memberSet[member] = struct{}{}
	}
	for member := range s.memberships[expansion.Source] {
		if _, retained := memberSet[member]; !retained {
			return store.InvalidArgument("expanded conversation removed a source member")
		}
	}
	if len(memberSet) <= len(s.memberships[expansion.Source]) {
		return store.InvalidArgument("expanded conversation adds no members")
	}
	wantedKey := domain.DirectConversationKey(expansion.Target.WorkspaceID, expansion.Members)
	for id, existing := range s.conversations {
		if existing.WorkspaceID != expansion.Target.WorkspaceID || (!existing.IsDirect && !existing.IsGroupDirect) {
			continue
		}
		current := make([]domain.UserID, 0, len(s.memberships[id]))
		for member := range s.memberships[id] {
			current = append(current, member)
		}
		if domain.DirectConversationKey(existing.WorkspaceID, current) == wantedKey {
			return store.ErrAlreadyExists
		}
	}
	if sourceNotice.Conversation != expansion.Source || targetNotice.Conversation != expansion.Target.ID ||
		sourceNotice.WorkspaceID != expansion.Target.WorkspaceID || targetNotice.WorkspaceID != expansion.Target.WorkspaceID ||
		sourceNotice.ID == "" || targetNotice.ID == "" || sourceNotice.AuthorID == "" || targetNotice.AuthorID == "" {
		return store.InvalidArgument("invalid direct conversation notices")
	}
	for _, messages := range s.messages {
		for _, message := range messages {
			if message.ID == sourceNotice.ID || message.ID == targetNotice.ID {
				return store.ErrAlreadyExists
			}
		}
	}
	if messageInstantExists(s.messages[expansion.Source], sourceNotice.CreatedAt) {
		return store.ErrMessageTimestampTaken
	}

	history := make([]domain.Message, 0)
	if expansion.History == domain.DirectHistoryAll {
		history = make([]domain.Message, 0, len(s.messages[expansion.Source]))
		for _, original := range s.messages[expansion.Source] {
			if original.Deleted {
				continue
			}
			copy := s.cloneMessage(original)
			copy.ID, err = domain.NewMessageID()
			if err != nil {
				return err
			}
			copy.Conversation = expansion.Target.ID
			history = append(history, copy)
		}
	}
	if messageInstantExists(history, targetNotice.CreatedAt) {
		return store.ErrMessageTimestampTaken
	}

	s.conversations[expansion.Target.ID] = expansion.Target
	s.memberships[expansion.Target.ID] = memberSet
	s.conversationTeams[expansion.Target.ID] = map[domain.WorkspaceID]struct{}{expansion.Target.WorkspaceID: {}}
	s.conversationOrg[expansion.Target.ID] = false
	s.messages[expansion.Target.ID] = history
	for _, message := range history {
		for _, file := range message.Files {
			if !slices.Contains(s.fileShares[file.ID], expansion.Target.ID) {
				s.fileShares[file.ID] = append(s.fileShares[file.ID], expansion.Target.ID)
				slices.Sort(s.fileShares[file.ID])
			}
		}
	}
	s.insertCommittedMessageLocked(sourceNotice)
	s.insertCommittedMessageLocked(targetNotice)
	s.outbox = append(s.outbox, emitted...)
	return nil
}

func (s *Store) ConvertGroupDirectToPrivate(_ context.Context, conversion domain.GroupDirectConversion, emitted []events.Event) (domain.Conversation, error) {
	if len(emitted) != 2 {
		return domain.Conversation{}, store.InvalidArgument("invalid group DM conversion")
	}
	notice, err := normalizeMessage(conversion.Notice)
	if err != nil {
		return domain.Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.conversations[conversion.Conversation]
	if !exists {
		return domain.Conversation{}, store.ErrNotFound
	}
	if !value.IsPrivate || value.IsDirect || !value.IsGroupDirect || strings.TrimSpace(conversion.Name) == "" {
		return domain.Conversation{}, store.ErrInvalidConversationType
	}
	for id, existing := range s.conversations {
		if id != value.ID && existing.WorkspaceID == value.WorkspaceID && !existing.IsDirect && !existing.IsGroupDirect && existing.Name == conversion.Name {
			return domain.Conversation{}, store.ErrAlreadyExists
		}
	}
	if notice.Conversation != value.ID || notice.WorkspaceID != value.WorkspaceID || notice.ID == "" || notice.AuthorID == "" {
		return domain.Conversation{}, store.InvalidArgument("invalid group DM conversion notice")
	}
	for _, messages := range s.messages {
		for _, message := range messages {
			if message.ID == notice.ID {
				return domain.Conversation{}, store.ErrAlreadyExists
			}
		}
	}
	if messageInstantExists(s.messages[value.ID], notice.CreatedAt) {
		return domain.Conversation{}, store.ErrMessageTimestampTaken
	}
	value.Name = conversion.Name
	value.IsDirect = false
	value.IsGroupDirect = false
	value.IsPrivate = true
	s.conversations[value.ID] = value
	for key := range s.closedDirects {
		if strings.HasSuffix(key, "\x00"+string(value.ID)) {
			delete(s.closedDirects, key)
		}
	}
	s.insertCommittedMessageLocked(notice)
	s.outbox = append(s.outbox, emitted...)
	return value, nil
}

func messageInstantExists(messages []domain.Message, instant time.Time) bool {
	instant = domain.MessageInstant(instant)
	for _, message := range messages {
		if message.CreatedAt.Equal(instant) {
			return true
		}
	}
	return false
}

// insertCommittedMessageLocked is used only after a compound mutation has
// validated every failure point. It preserves the same ordering, activity, and
// DM-reopen semantics as createMessageLocked without introducing a second lock
// or a partial rollback problem.
func (s *Store) insertCommittedMessageLocked(message domain.Message) {
	values := s.messages[message.Conversation]
	index := sort.Search(len(values), func(index int) bool {
		current := values[index]
		return message.CreatedAt.Before(current.CreatedAt) || (message.CreatedAt.Equal(current.CreatedAt) && string(message.ID) < string(current.ID))
	})
	values = append(values, domain.Message{})
	copy(values[index+1:], values[index:])
	values[index] = message
	s.messages[message.Conversation] = values
	conversation := s.conversations[message.Conversation]
	if conversation.IsDirect || conversation.IsGroupDirect {
		for member := range s.memberships[message.Conversation] {
			delete(s.closedDirects, directOpenKey(message.WorkspaceID, member, message.Conversation))
		}
	}
	s.createMessageActivityLocked(message)
}

func directOpenKey(workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) string {
	return string(workspace) + "\x00" + string(user) + "\x00" + string(conversation)
}

func (s *Store) SetDirectConversationOpen(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversationID domain.ConversationID, open bool, event events.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, exists := s.conversations[conversationID]
	if !exists || conversation.WorkspaceID != workspace || (!conversation.IsDirect && !conversation.IsGroupDirect) {
		return false, store.ErrNotFound
	}
	if _, member := s.memberships[conversationID][user]; !member {
		return false, store.ErrNotFound
	}
	key := directOpenKey(workspace, user, conversationID)
	_, closed := s.closedDirects[key]
	if open {
		if !closed {
			return false, nil
		}
		delete(s.closedDirects, key)
	} else {
		if closed {
			return false, nil
		}
		s.closedDirects[key] = struct{}{}
	}
	s.outbox = append(s.outbox, event)
	return true, nil
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

func (s *Store) RenameConversation(_ context.Context, conversation domain.ConversationID, name string, event events.Event, notices ...domain.Message) (domain.Conversation, error) {
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
	s.appendConversationNotices(notices)
	return value, nil
}

func (s *Store) SetConversationTopic(_ context.Context, conversation domain.ConversationID, topic string, event events.Event, notices ...domain.Message) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	value.Topic = topic
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	s.appendConversationNotices(notices)
	return value, nil
}

func (s *Store) SetConversationPurpose(_ context.Context, conversation domain.ConversationID, purpose string, event events.Event, notices ...domain.Message) (domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return domain.Conversation{}, store.ErrNotFound
	}
	value.Purpose = purpose
	s.conversations[conversation] = value
	s.outbox = append(s.outbox, event)
	s.appendConversationNotices(notices)
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
	for key := range s.closedDirects {
		if strings.HasSuffix(key, "\x00"+string(conversation)) {
			delete(s.closedDirects, key)
		}
	}
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

func (s *Store) SetInviteRequestStatus(_ context.Context, workspace domain.WorkspaceID, id domain.InviteRequestID, from, status domain.InviteRequestStatus, reviewedAt time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.inviteRequests[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if !domain.InviteRequestReviewable(from, status) {
		return store.ErrInvalidInviteRequest
	}
	if value.Status != from {
		return store.ErrNotFound
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

func (s *Store) ListWorkspacesForEmail(_ context.Context, email string) ([]domain.WorkspaceMembershipSummary, error) {
	normalized := domain.NormalizeEmail(email)
	if normalized == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.WorkspaceMembershipSummary, 0, 2)
	for _, user := range s.users {
		if user.Deleted || domain.NormalizeEmail(user.Email) != normalized {
			continue
		}
		membership, exists := s.members[string(user.WorkspaceID)+"\x00"+string(user.ID)]
		if !exists || !membership.Active {
			continue
		}
		workspace, exists := s.workspaces[user.WorkspaceID]
		if !exists {
			continue
		}
		result = append(result, domain.WorkspaceMembershipSummary{Workspace: cloneWorkspace(workspace), UserID: user.ID, Role: membership.Role})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Workspace.ID < result[right].Workspace.ID })
	return result, nil
}

func (s *Store) WorkspaceAnalytics(_ context.Context, workspace domain.WorkspaceID, since time.Time, busiest int) (domain.WorkspaceAnalytics, error) {
	if busiest < 0 {
		return domain.WorkspaceAnalytics{}, store.InvalidArgument("the busiest-channel bound must not be negative")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := domain.WorkspaceAnalytics{Since: since.UTC()}
	for _, user := range s.users {
		if user.WorkspaceID != workspace || user.Deleted {
			continue
		}
		result.Members++
		membership, exists := s.members[string(workspace)+"\x00"+string(user.ID)]
		if !exists {
			continue
		}
		if membership.Active {
			result.ActiveMembers++
		}
		if membership.Guest() {
			result.Guests++
		}
		if membership.Role == domain.WorkspaceRoleAdmin || membership.Role == domain.WorkspaceRoleOwner {
			result.Admins++
		}
	}
	activity := make([]domain.ChannelActivity, 0, len(s.conversations))
	for id, conversation := range s.conversations {
		if conversation.WorkspaceID != workspace {
			continue
		}
		if !conversation.IsDirect && !conversation.IsGroupDirect {
			switch {
			case conversation.Archived:
				result.ArchivedChannels++
			case conversation.IsPrivate:
				result.PrivateChannels++
			default:
				result.PublicChannels++
			}
		}
		recent := 0
		for _, message := range s.messages[id] {
			if message.Deleted {
				continue
			}
			result.Messages++
			if !since.IsZero() && message.CreatedAt.Before(since) {
				continue
			}
			result.RecentMessages++
			recent++
		}
		if recent > 0 && !conversation.IsDirect && !conversation.IsGroupDirect {
			activity = append(activity, domain.ChannelActivity{ConversationID: id, Name: conversation.Name, Messages: recent})
		}
	}
	for _, file := range s.files {
		if file.WorkspaceID != workspace || file.Deleted {
			continue
		}
		result.Files++
		if since.IsZero() || !file.CreatedAt.Before(since) {
			result.RecentFiles++
		}
	}
	// Ties break on the identifier so two profiles, and two calls, order the
	// same list the same way.
	sort.Slice(activity, func(left, right int) bool {
		if activity[left].Messages != activity[right].Messages {
			return activity[left].Messages > activity[right].Messages
		}
		return activity[left].ConversationID < activity[right].ConversationID
	})
	if busiest < len(activity) {
		activity = activity[:busiest]
	}
	result.BusiestChannels = activity
	return result, nil
}

func (s *Store) FindInviteRequestByEmail(_ context.Context, workspace domain.WorkspaceID, email string, status domain.InviteRequestStatus) (domain.InviteRequest, error) {
	normalized := domain.NormalizeEmail(email)
	if normalized == "" {
		return domain.InviteRequest{}, store.ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := domain.InviteRequest{}
	exists := false
	for _, value := range s.inviteRequests {
		if value.WorkspaceID != workspace || value.Status != status || domain.NormalizeEmail(value.Email) != normalized {
			continue
		}
		// The newest invitation wins: an address invited twice is being
		// re-invited, and the older record is the stale one.
		if !exists || value.CreatedAt.After(found.CreatedAt) || (value.CreatedAt.Equal(found.CreatedAt) && value.ID > found.ID) {
			found, exists = value, true
		}
	}
	if !exists {
		return domain.InviteRequest{}, store.ErrNotFound
	}
	return cloneInviteRequest(found), nil
}

func (s *Store) AcceptInviteRequest(_ context.Context, acceptance domain.InviteRequestAcceptance, emitted []events.Event) error {
	if len(emitted) == 0 {
		return store.InvalidArgument("accepting an invitation requires at least one event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, exists := s.inviteRequests[acceptance.RequestID]
	if !exists || request.WorkspaceID != acceptance.WorkspaceID {
		return store.ErrNotFound
	}
	if !request.Acceptable(acceptance.AcceptedAt) {
		return store.ErrInvalidInviteRequest
	}
	if err := s.createUserLocked(acceptance.User, acceptance.Membership, nil); err != nil {
		return err
	}
	for _, channelID := range acceptance.Channels {
		conversation, ok := s.conversations[channelID]
		if !ok || conversation.WorkspaceID != acceptance.WorkspaceID {
			return store.ErrNotFound
		}
		if s.memberships[channelID] == nil {
			s.memberships[channelID] = make(map[domain.UserID]struct{})
		}
		s.memberships[channelID][acceptance.User.ID] = struct{}{}
	}
	request.Status = domain.InviteRequestAccepted
	request.AcceptedAt = acceptance.AcceptedAt.UTC()
	request.AcceptedBy = acceptance.User.ID
	s.inviteRequests[acceptance.RequestID] = request
	s.outbox = append(s.outbox, emitted...)
	return nil
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

func (s *Store) SetAppBotToken(_ context.Context, appID domain.AppID, workspace domain.WorkspaceID, tokenCiphertext string, written ...events.Event) error {
	if appID == "" || workspace == "" || tokenCiphertext == "" {
		return store.InvalidArgument("invalid app bot token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appBotTokens == nil {
		s.appBotTokens = make(map[string]string)
	}
	s.appBotTokens[string(appID)+"\x00"+string(workspace)] = tokenCiphertext
	s.outbox = append(s.outbox, written...)
	return nil
}

func (s *Store) GetAppBotTokenCiphertext(_ context.Context, appID domain.AppID, workspace domain.WorkspaceID) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ciphertext, ok := s.appBotTokens[string(appID)+"\x00"+string(workspace)]
	if !ok {
		return "", store.ErrNotFound
	}
	return ciphertext, nil
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

func (s *Store) UninstallApp(_ context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, written ...events.Event) error {
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
	s.outbox = append(s.outbox, written...)
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
			bot.UpdatedAt = time.Now().UTC()
			s.bots[key] = bot
			s.deactivateBotUserLocked(bot)
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

func (s *Store) ListWorkflowRunSteps(_ context.Context, workspace domain.WorkspaceID, runID domain.WorkflowRunID) ([]domain.WorkflowStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowStep, 0)
	for _, value := range s.workflowSteps {
		if value.WorkspaceID == workspace && value.WorkflowRunID == runID {
			values = append(values, value)
		}
	}
	slices.SortFunc(values, func(left, right domain.WorkflowStep) int {
		if ordering := left.CreatedAt.Compare(right.CreatedAt); ordering != 0 {
			return ordering
		}
		return strings.Compare(string(left.ID), string(right.ID))
	})
	return values, nil
}

func (s *Store) CreateWorkflow(_ context.Context, value domain.WorkflowDefinition, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.AppID == "" || value.OwnerID == "" ||
		value.Title == "" || value.Status == "" || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return store.InvalidArgument("invalid workflow")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workflows[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	if value.Version == 0 {
		value.Version = 1
	}
	s.workflows[value.ID] = value
	s.workflowRevisions[value.ID] = append(s.workflowRevisions[value.ID], domain.WorkflowRevision{
		WorkflowID: value.ID, WorkspaceID: value.WorkspaceID, Version: value.Version, Title: value.Title,
		Description: value.Description, Icon: value.Icon, CallbackID: value.CallbackID, InputSchema: value.InputSchema,
		Steps: value.Steps, Status: value.Status, CreatedAt: value.UpdatedAt,
	})
	s.outbox = append(s.outbox, event)
	return nil
}

// SetWorkflowStatus mirrors the SQL profile: the status and the moment change,
// and nothing else does.
func (s *Store) SetWorkflowStatus(_ context.Context, workspace domain.WorkspaceID, id domain.WorkflowID, status domain.WorkflowStatus, at time.Time, event events.Event) error {
	if id == "" || workspace == "" || status == "" {
		return store.InvalidArgument("invalid workflow status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.workflows[id]
	if !exists || current.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	current.Status = status
	current.UpdatedAt = at.UTC()
	s.workflows[id] = current
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) UpdateWorkflow(_ context.Context, value domain.WorkflowDefinition, expectedVersion uint64, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UpdatedAt.IsZero() || expectedVersion == 0 {
		return store.InvalidArgument("invalid workflow update")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.workflows[value.ID]
	if !exists || current.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if current.Version != expectedVersion {
		return store.ErrConflict
	}
	value.CreatedAt = current.CreatedAt
	value.Version = expectedVersion + 1
	// The manager list is workflow-level metadata written only by
	// SetWorkflowManagers; a content update preserves it, matching the SQL
	// store, whose UPDATE leaves the manager_ids column untouched.
	value.ManagerIDs = current.ManagerIDs
	s.workflows[value.ID] = value
	s.workflowRevisions[value.ID] = append(s.workflowRevisions[value.ID], domain.WorkflowRevision{
		WorkflowID: value.ID, WorkspaceID: value.WorkspaceID, Version: value.Version, Title: value.Title,
		Description: value.Description, Icon: value.Icon, CallbackID: value.CallbackID, InputSchema: value.InputSchema,
		Steps: value.Steps, Status: value.Status, CreatedAt: value.UpdatedAt,
	})
	if value.Status == domain.WorkflowDisabled {
		for runID, run := range s.workflowRuns {
			if run.WorkflowID == value.ID && run.WorkspaceID == value.WorkspaceID && run.Status == domain.WorkflowRunRunning {
				run.Status = domain.WorkflowRunCancelled
				run.Error = "workflow_unpublished"
				run.CompletedAt = value.UpdatedAt
				run.UpdatedAt = value.UpdatedAt
				s.workflowRuns[runID] = run
			}
		}
		for stepID, step := range s.workflowSteps {
			if step.WorkspaceID == value.WorkspaceID && step.Status == domain.WorkflowStepExecuting {
				if run, ok := s.workflowRuns[step.WorkflowRunID]; ok && run.WorkflowID == value.ID && run.Status == domain.WorkflowRunCancelled {
					step.Status = domain.WorkflowStepCancelled
					step.Error = "workflow_unpublished"
					step.UpdatedAt = value.UpdatedAt
					s.workflowSteps[stepID] = step
				}
			}
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) SetWorkflowManagers(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID, managerIDs []domain.UserID, event events.Event) error {
	if workflowID == "" || workspace == "" {
		return store.InvalidArgument("invalid workflow manager update")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.workflows[workflowID]
	if !exists || current.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	current.ManagerIDs = slices.Clone(managerIDs)
	current.UpdatedAt = event.CreatedAt
	s.workflows[workflowID] = current
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteWorkflow(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID, expectedVersion uint64, event events.Event) (bool, error) {
	if workflowID == "" || workspace == "" || expectedVersion == 0 {
		return false, store.InvalidArgument("invalid workflow deletion")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.workflows[workflowID]
	if !exists || current.WorkspaceID != workspace {
		return false, store.ErrNotFound
	}
	if current.Version != expectedVersion {
		return false, store.ErrConflict
	}
	for runID, run := range s.workflowRuns {
		if run.WorkflowID != workflowID || run.WorkspaceID != workspace {
			continue
		}
		if run.Status == domain.WorkflowRunRunning {
			run.Status = domain.WorkflowRunCancelled
			run.Error = "workflow_unpublished"
			run.CompletedAt = event.CreatedAt
			run.UpdatedAt = event.CreatedAt
			s.workflowRuns[runID] = run
		}
		delete(s.workflowRuns, runID)
		for stepID, step := range s.workflowSteps {
			if step.WorkflowRunID != runID {
				continue
			}
			if step.Status == domain.WorkflowStepExecuting {
				step.Status = domain.WorkflowStepCancelled
				step.Error = "workflow_unpublished"
				step.UpdatedAt = event.CreatedAt
				s.workflowSteps[stepID] = step
			}
			delete(s.workflowSteps, stepID)
		}
	}
	for triggerID, trigger := range s.workflowTriggers {
		if trigger.WorkflowID != workflowID || trigger.WorkspaceID != workspace {
			continue
		}
		delete(s.workflowTriggers, triggerID)
		for conversation, featured := range s.featuredWorkflows {
			remaining := make([]domain.FeaturedWorkflow, 0, len(featured))
			for _, entry := range featured {
				if entry.TriggerID != triggerID {
					remaining = append(remaining, entry)
				}
			}
			if len(remaining) == 0 {
				delete(s.featuredWorkflows, conversation)
			} else {
				s.featuredWorkflows[conversation] = remaining
			}
		}
	}
	delete(s.workflowRevisions, workflowID)
	delete(s.workflows, workflowID)
	s.outbox = append(s.outbox, event)
	return true, nil
}

func (s *Store) DiscardWorkflowStagedChanges(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID, expectedVersion uint64, event events.Event) (bool, error) {
	if workflowID == "" || workspace == "" || expectedVersion == 0 {
		return false, store.InvalidArgument("invalid workflow staged-changes discard")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.workflows[workflowID]
	if !exists || current.WorkspaceID != workspace {
		return false, store.ErrNotFound
	}
	if current.Version != expectedVersion {
		return false, store.ErrConflict
	}
	if current.PublishedVersion == 0 || current.Version == current.PublishedVersion {
		return false, store.InvalidArgument("workflow has no staged changes to discard")
	}
	for _, revision := range s.workflowRevisions[workflowID] {
		if revision.Version != current.PublishedVersion {
			continue
		}
		current.Title = revision.Title
		current.Description = revision.Description
		current.Icon = revision.Icon
		current.CallbackID = revision.CallbackID
		current.InputSchema = revision.InputSchema
		current.Steps = revision.Steps
		current.Version = current.PublishedVersion
		current.UpdatedAt = event.CreatedAt
		s.workflows[workflowID] = current
		var remaining []domain.WorkflowRevision
		for _, old := range s.workflowRevisions[workflowID] {
			if old.Version <= current.PublishedVersion {
				remaining = append(remaining, old)
			}
		}
		s.workflowRevisions[workflowID] = remaining
		s.outbox = append(s.outbox, event)
		return true, nil
	}
	return false, store.ErrConflict
}

func (s *Store) GetWorkflow(_ context.Context, workspace domain.WorkspaceID, id domain.WorkflowID) (domain.WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.workflows[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.WorkflowDefinition{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListWorkflows(_ context.Context, workspace domain.WorkspaceID, request domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, false, "", err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, false, "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowDefinition, 0, request.Limit+1)
	for _, value := range s.workflows {
		if value.WorkspaceID != workspace || (after != "" && string(value.ID) <= after) {
			continue
		}
		values = appendSorted(values, value, request.Limit+1, func(left, right domain.WorkflowDefinition) bool {
			return left.ID < right.ID
		})
	}
	more := len(values) > request.Limit
	if more {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if more {
		next, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return values, more, next, err
}

func (s *Store) ListWorkflowRevisions(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID) ([]domain.WorkflowRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if workflow, exists := s.workflows[workflowID]; !exists || workflow.WorkspaceID != workspace {
		return nil, store.ErrNotFound
	}
	return slices.Clone(s.workflowRevisions[workflowID]), nil
}

func (s *Store) SetWorkflowTrigger(_ context.Context, value domain.WorkflowTrigger, expectedVersion uint64, event events.Event) error {
	if value.ID == "" || value.WorkflowID == "" || value.WorkspaceID == "" || value.AppID == "" ||
		value.Type == "" || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return store.InvalidArgument("invalid workflow trigger")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[value.WorkflowID]
	if !exists || workflow.WorkspaceID != value.WorkspaceID || workflow.AppID != value.AppID {
		return store.ErrNotFound
	}
	current, exists := s.workflowTriggers[value.ID]
	if !exists {
		if expectedVersion != 0 {
			return store.ErrConflict
		}
		value.Version = 1
	} else {
		if current.WorkspaceID != value.WorkspaceID || current.WorkflowID != value.WorkflowID || current.AppID != value.AppID {
			return store.ErrNotFound
		}
		if current.Version != expectedVersion {
			return store.ErrConflict
		}
		value.CreatedAt = current.CreatedAt
		value.Version = expectedVersion + 1
	}
	s.workflowTriggers[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetWorkflowTrigger(_ context.Context, workspace domain.WorkspaceID, id domain.WorkflowTriggerID) (domain.WorkflowTrigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.workflowTriggers[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.WorkflowTrigger{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListWorkflowTriggers(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID) ([]domain.WorkflowTrigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowTrigger, 0)
	for _, value := range s.workflowTriggers {
		if value.WorkspaceID == workspace && value.WorkflowID == workflowID {
			values = append(values, value)
		}
	}
	slices.SortFunc(values, func(left, right domain.WorkflowTrigger) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	return values, nil
}

func (s *Store) DueScheduledWorkflowTriggers(_ context.Context, workspace domain.WorkspaceID, now time.Time, limit int) ([]domain.WorkflowTrigger, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("workflow trigger limit must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowTrigger, 0)
	for _, value := range s.workflowTriggers {
		if value.Type != string(domain.WorkflowTriggerScheduled) || !value.Enabled || value.NextRunAt.IsZero() || value.NextRunAt.After(now) {
			continue
		}
		if workspace != "" && value.WorkspaceID != workspace {
			continue
		}
		values = append(values, value)
	}
	slices.SortFunc(values, func(left, right domain.WorkflowTrigger) int {
		if !left.NextRunAt.Equal(right.NextRunAt) {
			return left.NextRunAt.Compare(right.NextRunAt)
		}
		return strings.Compare(string(left.ID), string(right.ID))
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *Store) EarliestScheduledWorkflowTrigger(_ context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for _, value := range s.workflowTriggers {
		if value.Type != string(domain.WorkflowTriggerScheduled) || !value.Enabled || value.NextRunAt.IsZero() {
			continue
		}
		if workspace != "" && value.WorkspaceID != workspace {
			continue
		}
		if earliest.IsZero() || value.NextRunAt.Before(earliest) {
			earliest = value.NextRunAt
		}
	}
	return earliest, nil
}

func (s *Store) CompleteScheduledWorkflowTrigger(_ context.Context, workspace domain.WorkspaceID, triggerID domain.WorkflowTriggerID, expectedNextRunAt, nextRunAt time.Time, event events.Event) (bool, error) {
	if triggerID == "" || expectedNextRunAt.IsZero() || nextRunAt.IsZero() {
		return false, store.InvalidArgument("scheduled workflow trigger completion is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.workflowTriggers[triggerID]
	if !exists || value.WorkspaceID != workspace {
		return false, nil
	}
	if value.Type != string(domain.WorkflowTriggerScheduled) || !value.Enabled || !value.NextRunAt.Equal(expectedNextRunAt) {
		return false, nil
	}
	value.NextRunAt = nextRunAt.UTC()
	s.workflowTriggers[triggerID] = value
	s.outbox = append(s.outbox, event)
	return true, nil
}

func (s *Store) ListWorkflowEventTriggers(_ context.Context, workspace domain.WorkspaceID) ([]domain.WorkflowTrigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowTrigger, 0)
	for _, value := range s.workflowTriggers {
		if !slices.Contains(domain.EventWorkflowTriggerTypes, domain.WorkflowTriggerType(value.Type)) || !value.Enabled {
			continue
		}
		if workspace != "" && value.WorkspaceID != workspace {
			continue
		}
		values = append(values, value)
	}
	slices.SortFunc(values, func(left, right domain.WorkflowTrigger) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	return values, nil
}

func (s *Store) GetWorkflowEventCursor(_ context.Context, workspace domain.WorkspaceID) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workflowEventCursor[workspace], nil
}

func (s *Store) AdvanceWorkflowEventCursor(_ context.Context, workspace domain.WorkspaceID, sequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workflowEventCursor[workspace] < sequence {
		s.workflowEventCursor[workspace] = sequence
	}
	return nil
}

func (s *Store) CreateWorkflowRun(_ context.Context, value domain.WorkflowRun, firstStep *domain.WorkflowStep, emitted []events.Event) error {
	if value.ID == "" || value.WorkflowID == "" || value.WorkspaceID == "" || value.AppID == "" ||
		value.Status == "" || value.WorkflowVersion == 0 || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return store.InvalidArgument("invalid workflow run")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[value.WorkflowID]
	if !exists || workflow.WorkspaceID != value.WorkspaceID || workflow.AppID != value.AppID {
		return store.ErrNotFound
	}
	if _, exists := s.workflowRuns[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	if value.IdempotencyKey != "" {
		for _, current := range s.workflowRuns {
			if current.WorkspaceID == value.WorkspaceID && current.IdempotencyKey == value.IdempotencyKey {
				return store.ErrAlreadyExists
			}
		}
	}
	if firstStep != nil {
		if firstStep.ID == "" || firstStep.WorkflowRunID != value.ID || firstStep.WorkspaceID != value.WorkspaceID ||
			firstStep.AppID == "" || firstStep.UserID == "" ||
			(firstStep.Status != domain.WorkflowStepExecuting && firstStep.Status != domain.WorkflowStepWaiting) ||
			firstStep.CreatedAt.IsZero() || firstStep.UpdatedAt.IsZero() {
			return store.InvalidArgument("invalid first workflow step")
		}
		if _, exists := s.workflowSteps[firstStep.ID]; exists {
			return store.ErrAlreadyExists
		}
	}
	s.workflowRuns[value.ID] = value
	if firstStep != nil {
		s.workflowSteps[firstStep.ID] = *firstStep
	}
	s.outbox = append(s.outbox, emitted...)
	return nil
}

func (s *Store) AdvanceWorkflowRun(_ context.Context, completed domain.WorkflowStep, next *domain.WorkflowStep, value domain.WorkflowRun, expectedStep int, emitted []events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.Status == "" || value.UpdatedAt.IsZero() ||
		completed.ID == "" || completed.WorkflowRunID != value.ID || completed.Status == domain.WorkflowStepExecuting {
		return store.InvalidArgument("invalid workflow run advance")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.workflowRuns[value.ID]
	if !exists || current.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if current.Status != domain.WorkflowRunRunning || current.CurrentStep != expectedStep {
		return store.ErrConflict
	}
	currentExecution, exists := s.workflowSteps[completed.ID]
	if !exists || currentExecution.WorkflowRunID != value.ID ||
		(currentExecution.Status != domain.WorkflowStepExecuting && currentExecution.Status != domain.WorkflowStepWaiting) {
		return store.ErrConflict
	}
	if next != nil {
		if next.ID == "" || next.WorkflowRunID != value.ID || next.WorkspaceID != value.WorkspaceID ||
			next.AppID == "" || next.UserID == "" ||
			(next.Status != domain.WorkflowStepExecuting && next.Status != domain.WorkflowStepWaiting) ||
			next.CreatedAt.IsZero() || next.UpdatedAt.IsZero() {
			return store.InvalidArgument("invalid next workflow step")
		}
		if _, exists := s.workflowSteps[next.ID]; exists {
			return store.ErrAlreadyExists
		}
	}
	completed.CreatedAt = currentExecution.CreatedAt
	s.workflowSteps[completed.ID] = completed
	if next != nil {
		s.workflowSteps[next.ID] = *next
	}
	value.CreatedAt = current.CreatedAt
	s.workflowRuns[value.ID] = value
	s.outbox = append(s.outbox, emitted...)
	return nil
}

func (s *Store) GetWorkflowRun(_ context.Context, workspace domain.WorkspaceID, id domain.WorkflowRunID) (domain.WorkflowRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.workflowRuns[id]
	if !exists || value.WorkspaceID != workspace {
		return domain.WorkflowRun{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) GetWorkflowRunByIdempotency(_ context.Context, workspace domain.WorkspaceID, key string) (domain.WorkflowRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.workflowRuns {
		if value.WorkspaceID == workspace && value.IdempotencyKey == key && key != "" {
			return value, nil
		}
	}
	return domain.WorkflowRun{}, store.ErrNotFound
}

func (s *Store) ListWorkflowRuns(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID, request domain.PageRequest) ([]domain.WorkflowRun, bool, domain.Cursor, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, false, "", err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, false, "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowRun, 0, request.Limit+1)
	for _, value := range s.workflowRuns {
		if value.WorkspaceID != workspace || (workflowID != "" && value.WorkflowID != workflowID) ||
			(after != "" && string(value.ID) <= after) {
			continue
		}
		values = appendSorted(values, value, request.Limit+1, func(left, right domain.WorkflowRun) bool {
			return left.ID < right.ID
		})
	}
	more := len(values) > request.Limit
	if more {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if more {
		next, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return values, more, next, err
}

func (s *Store) SummarizeWorkflowRuns(_ context.Context, workspace domain.WorkspaceID, workflowID domain.WorkflowID, limit int) (domain.WorkflowActivity, error) {
	if workspace == "" || workflowID == "" || limit < 1 {
		return domain.WorkflowActivity{}, store.InvalidArgument("invalid workflow run summary")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	activity := domain.WorkflowActivity{}
	recent := make([]domain.WorkflowRun, 0, limit+1)
	for _, run := range s.workflowRuns {
		if run.WorkspaceID != workspace || run.WorkflowID != workflowID {
			continue
		}
		switch run.Status {
		case domain.WorkflowRunQueued:
			activity.Queued++
		case domain.WorkflowRunRunning:
			activity.Running++
		case domain.WorkflowRunCompleted:
			activity.Completed++
		case domain.WorkflowRunFailed:
			activity.Failed++
		case domain.WorkflowRunCancelled:
			activity.Cancelled++
		}
		recent = appendSorted(recent, run, limit+1, func(left, right domain.WorkflowRun) bool {
			return left.ID > right.ID
		})
	}
	if len(recent) > limit {
		recent = recent[:limit]
	}
	activity.RecentRuns = recent
	return activity, nil
}

func automationPermissionKey(workspace domain.WorkspaceID, resourceType, resourceID string) string {
	return string(workspace) + "\x00" + resourceType + "\x00" + resourceID
}

func cloneAutomationPermission(value domain.AutomationPermission) domain.AutomationPermission {
	value.UserIDs = slices.Clone(value.UserIDs)
	value.ChannelIDs = slices.Clone(value.ChannelIDs)
	value.TeamIDs = slices.Clone(value.TeamIDs)
	value.OrgIDs = slices.Clone(value.OrgIDs)
	return value
}

func (s *Store) SetAutomationPermission(_ context.Context, value domain.AutomationPermission, event events.Event) error {
	if value.WorkspaceID == "" || value.ResourceType == "" || value.ResourceID == "" ||
		value.PermissionType == "" || value.UpdatedAt.IsZero() {
		return store.InvalidArgument("invalid automation permission")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.automationPermissions[automationPermissionKey(value.WorkspaceID, value.ResourceType, value.ResourceID)] = cloneAutomationPermission(value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetAutomationPermission(_ context.Context, workspace domain.WorkspaceID, resourceType, resourceID string) (domain.AutomationPermission, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.automationPermissions[automationPermissionKey(workspace, resourceType, resourceID)]
	if !exists {
		return domain.AutomationPermission{}, store.ErrNotFound
	}
	return cloneAutomationPermission(value), nil
}

func (s *Store) SetFeaturedWorkflows(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, values []domain.FeaturedWorkflow, event events.Event) error {
	if workspace == "" || conversation == "" {
		return store.InvalidArgument("invalid featured workflow target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, exists := s.conversations[conversation]
	if !exists || channel.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	next := make([]domain.FeaturedWorkflow, len(values))
	seen := make(map[domain.WorkflowTriggerID]struct{}, len(values))
	for index, value := range values {
		trigger, exists := s.workflowTriggers[value.TriggerID]
		if value.TriggerID == "" || !exists || trigger.WorkspaceID != workspace {
			return store.ErrNotFound
		}
		if _, duplicate := seen[value.TriggerID]; duplicate {
			return store.ErrAlreadyExists
		}
		seen[value.TriggerID] = struct{}{}
		value.WorkspaceID = workspace
		value.ConversationID = conversation
		value.Position = index
		next[index] = value
	}
	s.featuredWorkflows[conversation] = next
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListFeaturedWorkflows(_ context.Context, workspace domain.WorkspaceID, conversations []domain.ConversationID) ([]domain.FeaturedWorkflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.FeaturedWorkflow, 0)
	for _, conversation := range conversations {
		for _, value := range s.featuredWorkflows[conversation] {
			if value.WorkspaceID == workspace {
				values = append(values, value)
			}
		}
	}
	slices.SortFunc(values, func(left, right domain.FeaturedWorkflow) int {
		if value := strings.Compare(string(left.ConversationID), string(right.ConversationID)); value != 0 {
			return value
		}
		return left.Position - right.Position
	})
	return values, nil
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

func (s *Store) CreateSharedInvite(_ context.Context, value domain.SharedInvite, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.ConversationID == "" || value.Status != domain.SharedInvitePending {
		return store.InvalidArgument("a shared invitation must be pending and name its conversation")
	}
	if value.TargetWorkspaceID == "" && strings.TrimSpace(value.TargetEmail) == "" {
		return store.InvalidArgument("a shared invitation must name an organization or an address")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, exists := s.conversations[value.ConversationID]
	if !exists || conversation.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if value.TargetWorkspaceID != "" {
		if _, exists := s.workspaces[value.TargetWorkspaceID]; !exists {
			return store.ErrNotFound
		}
		if value.TargetWorkspaceID == value.WorkspaceID {
			return store.InvalidArgument("a conversation cannot be shared with its own workspace")
		}
	}
	if _, exists := s.sharedInvites[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	// One outstanding invitation per organization per conversation: a second
	// would let two acceptances each claim a place.
	for _, existing := range s.sharedInvites {
		if existing.ConversationID != value.ConversationID || existing.TargetWorkspaceID == "" || existing.TargetWorkspaceID != value.TargetWorkspaceID {
			continue
		}
		if existing.Status == domain.SharedInvitePending || existing.Status == domain.SharedInviteApproved || existing.Status == domain.SharedInviteAccepted {
			return store.ErrAlreadyExists
		}
	}
	s.sharedInvites[value.ID] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetSharedInvite(_ context.Context, id domain.SharedInviteID) (domain.SharedInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.sharedInvites[id]
	if !exists {
		return domain.SharedInvite{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListSharedInvites(_ context.Context, workspace domain.WorkspaceID, status domain.SharedInviteStatus, request domain.PageRequest) (domain.SharedInvitePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.SharedInvitePage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.SharedInvitePage{}, err
	}
	s.mu.RLock()
	values := make([]domain.SharedInvite, 0, request.Limit+1)
	for _, value := range s.sharedInvites {
		if value.Status != status || string(value.ID) <= after {
			continue
		}
		if value.WorkspaceID != workspace && value.TargetWorkspaceID != workspace {
			continue
		}
		values = appendSorted(values, value, request.Limit+1, func(left, right domain.SharedInvite) bool { return left.ID < right.ID })
	}
	s.mu.RUnlock()
	page := domain.SharedInvitePage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	page.Invites = values
	return page, err
}

func (s *Store) SetSharedInviteStatus(_ context.Context, id domain.SharedInviteID, from, to domain.SharedInviteStatus, at time.Time, event events.Event) error {
	if !domain.SharedInviteTransition(from, to) {
		return store.InvalidArgument("a shared invitation cannot move between those states")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.sharedInvites[id]
	if !exists {
		return store.ErrNotFound
	}
	if value.Status != from {
		return store.ErrConflict
	}
	value.Status = to
	if to == domain.SharedInviteApproved {
		value.ReviewedAt = at.UTC()
	} else {
		value.SettledAt = at.UTC()
	}
	s.sharedInvites[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) AcceptSharedInvite(_ context.Context, id domain.SharedInviteID, at time.Time, emitted []events.Event) (domain.Conversation, error) {
	if len(emitted) == 0 {
		return domain.Conversation{}, store.InvalidArgument("accepting a shared invitation requires at least one event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	invite, exists := s.sharedInvites[id]
	if !exists {
		return domain.Conversation{}, store.ErrNotFound
	}
	if !invite.Acceptable(at) {
		return domain.Conversation{}, store.ErrConflict
	}
	conversation, exists := s.conversations[invite.ConversationID]
	if !exists {
		return domain.Conversation{}, store.ErrNotFound
	}
	teams := s.conversationTeams[invite.ConversationID]
	if teams == nil {
		teams = map[domain.WorkspaceID]struct{}{}
	}
	// The host organization counts towards the capacity, so it is part of the
	// set before the place is claimed.
	participating := map[domain.WorkspaceID]struct{}{conversation.WorkspaceID: {}}
	for team := range teams {
		participating[team] = struct{}{}
	}
	if _, already := participating[invite.TargetWorkspaceID]; !already {
		if len(participating) >= domain.SlackConnectCapacity {
			return domain.Conversation{}, store.ErrConflict
		}
		teams[invite.TargetWorkspaceID] = struct{}{}
		s.conversationTeams[invite.ConversationID] = teams
	}
	invite.Status = domain.SharedInviteAccepted
	invite.SettledAt = at.UTC()
	s.sharedInvites[id] = invite
	s.outbox = append(s.outbox, emitted...)
	return conversation, nil
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

// ListExternalTeams mirrors the SQL derivation: the connections are whatever
// organizations appear in this workspace's channels.
func (s *Store) ListExternalTeams(_ context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.ExternalTeamPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ExternalTeamPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ExternalTeamPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[domain.WorkspaceID]int)
	for conversation, teams := range s.conversationTeams {
		value, exists := s.conversations[conversation]
		if !exists || value.WorkspaceID != workspace {
			continue
		}
		for team := range teams {
			if team == workspace {
				continue
			}
			counts[team]++
		}
	}
	teams := make([]domain.ExternalTeam, 0, len(counts))
	for team, channels := range counts {
		if string(team) <= after {
			continue
		}
		teams = append(teams, domain.ExternalTeam{ID: team, Name: s.workspaces[team].Name, Channels: channels})
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i].ID < teams[j].ID })
	hasMore := len(teams) > request.Limit
	if hasMore {
		teams = teams[:request.Limit]
	}
	page := domain.ExternalTeamPage{Teams: teams, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(teams[len(teams)-1].ID))
	}
	return page, err
}

// DisconnectExternalTeam removes one organization from every conversation of
// this workspace under one lock, so no reader sees it gone from some channels
// and still present in others.
func (s *Store) DisconnectExternalTeam(_ context.Context, workspace domain.WorkspaceID, team domain.WorkspaceID, event events.Event) error {
	if team == "" || team == workspace {
		return store.InvalidArgument("an external organization is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for conversation, teams := range s.conversationTeams {
		value, exists := s.conversations[conversation]
		if !exists || value.WorkspaceID != workspace {
			continue
		}
		if _, present := teams[team]; !present {
			continue
		}
		delete(teams, team)
		removed++
	}
	if removed == 0 {
		return store.ErrNotFound
	}
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

func (s *Store) LatestEventSequence(_ context.Context, workspace domain.WorkspaceID) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	latest := uint64(0)
	for index, event := range s.outbox {
		if event.WorkspaceID != workspace {
			continue
		}
		// The memory profile's sequence is the one-based index the outbox
		// readers already hand out, so the tail must be derived the same way
		// or a cursor taken here would not line up with what a stream reads.
		if sequence := uint64(index + 1); sequence > latest {
			latest = sequence
		}
	}
	return latest, nil
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

func (s *Store) GetRetentionPolicy(_ context.Context, workspace domain.WorkspaceID) (domain.RetentionPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.workspaces[workspace]; !exists {
		return domain.RetentionPolicy{}, store.ErrNotFound
	}
	return s.retentionPolicies[workspace], nil
}

func (s *Store) SetRetentionPolicy(_ context.Context, workspace domain.WorkspaceID, policy domain.RetentionPolicy, event events.Event) error {
	if !policy.Valid() {
		return store.InvalidArgument("retention durations must be zero or a positive number of days below the maximum")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workspaces[workspace]; !exists {
		return store.ErrNotFound
	}
	s.retentionPolicies[workspace] = policy
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetConversationRetention(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) (domain.ConversationRetention, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.conversations[conversation]
	if !exists || value.WorkspaceID != workspace {
		return domain.ConversationRetention{}, store.ErrNotFound
	}
	return s.conversationRetention[conversation], nil
}

func (s *Store) SetConversationRetention(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, days int, updatedAt time.Time, event events.Event) error {
	if days <= 0 || !domain.ValidRetentionDays(days) {
		return store.InvalidArgument("a conversation retention override must be a positive number of days below the maximum")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.conversations[conversation]
	if !exists || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	s.conversationRetention[conversation] = domain.ConversationRetention{ConversationID: conversation, DurationDays: days, UpdatedAt: updatedAt.UTC()}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RemoveConversationRetention(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.conversations[conversation]
	if !exists || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	delete(s.conversationRetention, conversation)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ClaimRetentionSweep(_ context.Context, workspace domain.WorkspaceID, before, sweptAt time.Time, limit int) ([]domain.ConversationID, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("a retention claim requires a positive limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	claimed := make([]domain.ConversationID, 0, limit)
	for id, conversation := range s.conversations {
		if len(claimed) == limit {
			break
		}
		if workspace != "" && conversation.WorkspaceID != workspace {
			continue
		}
		if swept, exists := s.retentionSweptAt[id]; exists && swept.After(before) {
			continue
		}
		// Advancing the watermark IS the claim; holding the lock makes it the
		// same atomic step the SQL profile gets from its conditional UPDATE.
		s.retentionSweptAt[id] = sweptAt.UTC()
		claimed = append(claimed, id)
	}
	slices.Sort(claimed)
	return claimed, nil
}

func (s *Store) SweepRetention(_ context.Context, request domain.RetentionSweepRequest) (domain.RetentionSweep, error) {
	if request.Limit <= 0 {
		return domain.RetentionSweep{}, store.InvalidArgument("a retention sweep requires a positive limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, exists := s.conversations[request.ConversationID]
	if !exists || conversation.WorkspaceID != request.WorkspaceID {
		return domain.RetentionSweep{}, store.ErrNotFound
	}
	result := domain.RetentionSweep{ConversationID: request.ConversationID, SweptAt: request.SweptAt.UTC(), Complete: true}
	if !request.MessageHorizon.IsZero() {
		result.Messages, result.Complete = s.sweepMessagesLocked(request)
	}
	if !request.FileHorizon.IsZero() {
		result.ExpiredBlobs = s.sweepFilesLocked(request)
		result.Files = len(result.ExpiredBlobs)
	}
	return result, nil
}

// sweepMessagesLocked applies the same thread rule the SQL profile applies: a
// thread is retained until its newest reply expires, so a reply is never left
// without a parent to render under.
func (s *Store) sweepMessagesLocked(request domain.RetentionSweepRequest) (int, bool) {
	horizon := request.MessageHorizon.UTC()
	live := map[domain.MessageTimestamp]struct{}{}
	for _, message := range s.messages[request.ConversationID] {
		if message.ThreadTimestamp != "" && !message.CreatedAt.Before(horizon) {
			live[message.ThreadTimestamp] = struct{}{}
		}
	}
	retained := make([]domain.Message, 0, len(s.messages[request.ConversationID]))
	removed := 0
	complete := true
	for _, message := range s.messages[request.ConversationID] {
		switch {
		case !message.CreatedAt.Before(horizon):
			retained = append(retained, message)
		case removed == request.Limit:
			complete = false
			retained = append(retained, message)
		case s.retainedByThreadLocked(message, live):
			retained = append(retained, message)
		default:
			s.forgetMessageLocked(request.ConversationID, message)
			removed++
		}
	}
	s.messages[request.ConversationID] = retained
	return removed, complete
}

func (s *Store) retainedByThreadLocked(message domain.Message, live map[domain.MessageTimestamp]struct{}) bool {
	if message.ThreadTimestamp != "" {
		_, active := live[message.ThreadTimestamp]
		return active
	}
	_, active := live[domain.NewMessageTimestamp(message.CreatedAt)]
	return active
}

// forgetMessageLocked is the memory profile's cascade. It must remove exactly
// what the SQL profile's statement list removes, or the two profiles disagree
// about what a retention sweep destroys.
func (s *Store) forgetMessageLocked(conversation domain.ConversationID, message domain.Message) {
	delete(s.reactions, message.ID)
	delete(s.pins, message.ID)
	for _, stars := range s.stars {
		delete(stars, message.ID)
	}
	for id, item := range s.savedItems {
		if item.MessageID == message.ID {
			delete(s.savedItems, id)
		}
	}
	for id, item := range s.activityItems {
		if item.MessageID == message.ID {
			delete(s.activityItems, id)
		}
	}
	for key, id := range s.idempotency {
		if id == message.ID {
			delete(s.idempotency, key)
		}
	}
	if message.ThreadTimestamp == "" {
		root := domain.NewMessageTimestamp(message.CreatedAt)
		for key := range s.threadFollows {
			if strings.Contains(key, string(conversation)) && strings.HasSuffix(key, string(root)) {
				delete(s.threadFollows, key)
			}
		}
	}
}

// sweepFilesLocked expires a file on its own upload date, and only when no
// other conversation still shares it: a file in two channels outlives the
// stricter of them.
func (s *Store) sweepFilesLocked(request domain.RetentionSweepRequest) []domain.ExpiredBlob {
	horizon := request.FileHorizon.UTC()
	expired := make([]domain.ExpiredBlob, 0)
	for id, file := range s.files {
		if len(expired) == request.Limit {
			break
		}
		if file.WorkspaceID != request.WorkspaceID || file.Deleted || !file.CreatedAt.Before(horizon) {
			continue
		}
		shares := s.fileShares[id]
		if !slices.Contains(shares, request.ConversationID) {
			continue
		}
		if len(shares) > 1 {
			continue
		}
		expired = append(expired, domain.ExpiredBlob{FileID: id, BlobKey: file.BlobKey})
	}
	slices.SortFunc(expired, func(left, right domain.ExpiredBlob) int {
		return strings.Compare(string(left.FileID), string(right.FileID))
	})
	for _, blob := range expired {
		delete(s.files, blob.FileID)
		delete(s.fileShares, blob.FileID)
		for id, comment := range s.fileComments {
			if comment.File == blob.FileID {
				delete(s.fileComments, id)
			}
		}
		for conversation, messages := range s.messages {
			for index := range messages {
				messages[index].Files = slices.DeleteFunc(append([]domain.File(nil), messages[index].Files...), func(file domain.File) bool { return file.ID == blob.FileID })
			}
			s.messages[conversation] = messages
		}
	}
	return expired
}

func (s *Store) LastRetentionSweep(_ context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	latest := time.Time{}
	for id, swept := range s.retentionSweptAt {
		conversation, exists := s.conversations[id]
		if !exists || conversation.WorkspaceID != workspace {
			continue
		}
		if swept.After(latest) {
			latest = swept
		}
	}
	return latest, nil
}

func (s *Store) AppendRetentionEvents(_ context.Context, workspace domain.WorkspaceID, emitted []events.Event) error {
	if len(emitted) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workspaces[workspace]; !exists {
		return store.ErrNotFound
	}
	s.outbox = append(s.outbox, emitted...)
	return nil
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

// appendConversationNotices records the messages a conversation change posts
// into the conversation. The caller holds the lock, so the notice and the
// change it describes become visible together — the in-memory equivalent of
// the SQL transaction.
func (s *Store) appendConversationNotices(notices []domain.Message) {
	for _, notice := range notices {
		for {
			taken := false
			for _, existing := range s.messages[notice.Conversation] {
				if existing.CreatedAt.Equal(notice.CreatedAt) {
					taken = true
					break
				}
			}
			if !taken {
				break
			}
			notice.CreatedAt = notice.CreatedAt.Add(time.Microsecond)
		}
		notice.Unfurls = copyUnfurls(notice.Unfurls)
		s.messages[notice.Conversation] = append(s.messages[notice.Conversation], notice)
	}
}

func (s *Store) AddConversationMember(_ context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event, notices ...domain.Message) error {
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
	s.appendConversationNotices(notices)
	return nil
}

func (s *Store) InviteConversationMembers(_ context.Context, conversation domain.ConversationID, users []domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok {
		return store.ErrNotFound
	}
	if value.IsDirect || value.IsGroupDirect || value.Archived {
		return store.ErrInvalidConversationType
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
	added := make([]domain.UserID, 0, len(users))
	for _, user := range users {
		if _, exists := s.memberships[conversation][user]; exists {
			continue
		}
		s.memberships[conversation][user] = struct{}{}
		added = append(added, user)
	}
	if len(added) == 0 {
		return store.ErrAlreadyExists
	}
	for _, user := range added {
		if user == event.ActorID {
			continue
		}
		id := domain.ActivityIDFor(user, "conversation_invitation:"+string(conversation)+":"+string(event.ID))
		s.activityItems[id] = domain.ActivityItem{
			ID: id, WorkspaceID: value.WorkspaceID, UserID: user,
			Kinds: []domain.ActivityKind{domain.ActivityInvitation}, ActorID: event.ActorID,
			Conversation: conversation, OccurredAt: event.CreatedAt.UTC(),
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) RemoveConversationMember(_ context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event, notices ...domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.conversations[conversation]
	if !exists {
		return store.ErrNotFound
	}
	members := s.memberships[conversation]
	if _, exists := members[user]; !exists {
		return store.ErrNotFound
	}
	delete(members, user)
	prefix := conversationNotificationKey(value.WorkspaceID, user, conversation)
	delete(s.conversationNotificationPrefs, prefix)
	prefix += "\x00"
	for key := range s.threadFollows {
		if strings.HasPrefix(key, prefix) {
			delete(s.threadFollows, key)
		}
	}
	s.outbox = append(s.outbox, event)
	s.appendConversationNotices(notices)
	return nil
}

func readCursorKey(workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) string {
	return string(workspace) + "\x00" + string(user) + "\x00" + string(conversation)
}

func workspaceNotificationKey(workspace domain.WorkspaceID, user domain.UserID) string {
	return string(workspace) + "\x00" + string(user)
}

func conversationNotificationKey(workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) string {
	return workspaceNotificationKey(workspace, user) + "\x00" + string(conversation)
}

func threadFollowKey(workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, root domain.MessageTimestamp) string {
	return conversationNotificationKey(workspace, user, conversation) + "\x00" + string(root)
}

// ListFollowedThreads mirrors the SQL profile's Threads view. The key is
// workspace, user, conversation and root joined by NUL, which no identifier can
// contain, so splitting it back apart is exact rather than a guess.
func (s *Store) ListFollowedThreads(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.FollowedThreadPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.FollowedThreadPage{}, err
	}
	limit := request.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	prefix := workspaceNotificationKey(workspace, user) + "\x00"
	s.mu.RLock()
	defer s.mu.RUnlock()
	threads := make([]domain.FollowedThread, 0, len(s.threadFollows))
	for key := range s.threadFollows {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		parts := strings.Split(key, "\x00")
		if len(parts) != 4 {
			continue
		}
		conversation := domain.ConversationID(parts[2])
		root := domain.MessageTimestamp(parts[3])
		rootMessage, found := s.messageAtLocked(conversation, root)
		if !found {
			// The root was deleted; the thread leaves the view rather than
			// becoming a row that opens onto nothing.
			continue
		}
		var readAt time.Time
		if cursor, ok := s.readCursors[readCursorKey(workspace, user, conversation)]; ok {
			parsed, err := domain.ParseMessageTimestamp(cursor.LastRead)
			if err != nil {
				return domain.FollowedThreadPage{}, err
			}
			readAt = parsed
		}
		thread := domain.FollowedThread{
			Conversation: conversation, ConversationName: s.conversations[conversation].Name,
			Root: root, RootText: rootMessage.Text, RootAuthorID: rootMessage.AuthorID,
		}
		for _, message := range s.messages[conversation] {
			if message.Deleted || message.ThreadTimestamp != root {
				continue
			}
			thread.ReplyCount++
			if message.CreatedAt.After(thread.LastReplyAt) {
				thread.LastReplyAt = message.CreatedAt
			}
			if message.CreatedAt.After(readAt) {
				thread.UnreadReplies++
			}
		}
		threads = append(threads, thread)
	}
	sort.Slice(threads, func(left, right int) bool {
		if !threads[left].LastReplyAt.Equal(threads[right].LastReplyAt) {
			return threads[left].LastReplyAt.After(threads[right].LastReplyAt)
		}
		return threads[left].Root > threads[right].Root
	})
	page := domain.FollowedThreadPage{Threads: threads}
	if len(page.Threads) > limit {
		page.Threads = page.Threads[:limit]
		page.HasMore = true
	}
	return page, nil
}

func (s *Store) messageAtLocked(conversation domain.ConversationID, at domain.MessageTimestamp) (domain.Message, bool) {
	for _, message := range s.messages[conversation] {
		if !message.Deleted && domain.NewMessageTimestamp(message.CreatedAt) == at {
			return message, true
		}
	}
	return domain.Message{}, false
}

func (s *Store) GetWorkspaceNotificationPreferences(_ context.Context, workspace domain.WorkspaceID, user domain.UserID) (domain.WorkspaceNotificationPreferences, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if preferences, ok := s.workspaceNotificationPrefs[workspaceNotificationKey(workspace, user)]; ok {
		preferences.Keywords = append([]string(nil), preferences.Keywords...)
		return preferences, nil
	}
	return domain.DefaultWorkspaceNotificationPreferences(workspace, user), nil
}

func (s *Store) SetWorkspaceNotificationPreferences(_ context.Context, preferences domain.WorkspaceNotificationPreferences, event events.Event) error {
	preferences.Keywords = domain.NormalizeNotificationKeywords(preferences.Keywords)
	if !preferences.Valid() {
		return store.InvalidArgument("workspace notification preferences are invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[preferences.WorkspaceID]; !ok {
		return store.ErrNotFound
	}
	if _, ok := s.members[string(preferences.WorkspaceID)+"\x00"+string(preferences.UserID)]; !ok {
		return store.ErrNotFound
	}
	preferences.Keywords = append([]string(nil), preferences.Keywords...)
	s.workspaceNotificationPrefs[workspaceNotificationKey(preferences.WorkspaceID, preferences.UserID)] = preferences
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetConversationNotificationPreferences(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) (domain.ConversationNotificationPreferences, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.conversations[conversation]
	if !ok || value.WorkspaceID != workspace {
		return domain.ConversationNotificationPreferences{}, store.ErrNotFound
	}
	if _, member := s.memberships[conversation][user]; !member {
		return domain.ConversationNotificationPreferences{}, store.ErrNotFound
	}
	if preferences, ok := s.conversationNotificationPrefs[conversationNotificationKey(workspace, user, conversation)]; ok {
		return preferences, nil
	}
	return domain.DefaultConversationNotificationPreferences(workspace, user, conversation), nil
}

func (s *Store) SetConversationNotificationPreferences(_ context.Context, preferences domain.ConversationNotificationPreferences, event events.Event) error {
	if !preferences.Valid() {
		return store.InvalidArgument("conversation notification preferences are invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[preferences.Conversation]
	if !ok || value.WorkspaceID != preferences.WorkspaceID {
		return store.ErrNotFound
	}
	if _, member := s.memberships[preferences.Conversation][preferences.UserID]; !member {
		return store.ErrNotFound
	}
	s.conversationNotificationPrefs[conversationNotificationKey(preferences.WorkspaceID, preferences.UserID, preferences.Conversation)] = preferences
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) IsThreadFollowed(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, root domain.MessageTimestamp) (bool, error) {
	if _, err := domain.ParseMessageTimestamp(root); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.conversations[conversation]
	if !ok || value.WorkspaceID != workspace {
		return false, store.ErrNotFound
	}
	if _, member := s.memberships[conversation][user]; !member {
		return false, store.ErrNotFound
	}
	return s.threadFollows[threadFollowKey(workspace, user, conversation, root)], nil
}

// TouchUserActivity mirrors the SQL profile: forward-only, so a stale request
// cannot drag a member's presence backwards.
func (s *Store) TouchUserActivity(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, at time.Time) error {
	if at.IsZero() {
		return store.InvalidArgument("user activity requires an instant")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.users[user]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if at.After(value.LastActiveAt) {
		value.LastActiveAt = at.UTC()
		s.users[user] = value
	}
	return nil
}

// DueWorkflowDelays mirrors the SQL profile: only waiting steps that carry a
// wake time are candidates.
// SetAssistantThread mirrors the SQL profile: one field per write, the row
// created on first touch.
func (s *Store) SetAssistantThread(_ context.Context, value domain.AssistantThread, field domain.AssistantThreadField, event events.Event) error {
	if !field.Valid() || value.WorkspaceID == "" || value.Conversation == "" || value.ThreadTimestamp == "" {
		return store.InvalidArgument("assistant thread state requires a workspace, conversation, thread and field")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := assistantThreadKey(value.WorkspaceID, value.Conversation, value.ThreadTimestamp)
	current, ok := s.assistantThreads[key]
	if !ok {
		current = domain.AssistantThread{WorkspaceID: value.WorkspaceID, Conversation: value.Conversation, ThreadTimestamp: value.ThreadTimestamp}
	}
	switch field {
	case domain.AssistantThreadTitle:
		current.Title = value.Title
	case domain.AssistantThreadStatus:
		current.Status = value.Status
	case domain.AssistantThreadPrompts:
		current.PromptsTitle = value.PromptsTitle
		current.Prompts = append([]domain.AssistantPrompt(nil), value.Prompts...)
	}
	current.UpdatedAt = value.UpdatedAt
	s.assistantThreads[key] = current
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetAssistantThread(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AssistantThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.assistantThreads[assistantThreadKey(workspace, conversation, thread)]
	if !ok {
		return domain.AssistantThread{}, store.ErrNotFound
	}
	value.Prompts = append([]domain.AssistantPrompt(nil), value.Prompts...)
	return value, nil
}

func assistantThreadKey(workspace domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) string {
	return string(workspace) + "\x00" + string(conversation) + "\x00" + string(thread)
}

// RecordTyping mirrors the SQL profile: one row per member per conversation,
// replaced rather than appended, and no outbox record. Renewing a signal that
// is already live is the common case — a member typing steadily renews every
// few seconds — so the write is a replacement rather than an insert that would
// have to be de-duplicated on read.
func (s *Store) RecordTyping(_ context.Context, signal domain.TypingSignal) error {
	if !signal.Valid() {
		return store.InvalidArgument("a typing signal requires a workspace, conversation, member and expiry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Expired rows are dropped on write rather than by a worker. There is no
	// cadence to schedule and nothing to recover: a signal nobody renews is
	// already invisible to readers, and this only stops the map from holding
	// every member who has ever typed.
	for key, existing := range s.typing {
		if !existing.Active(signal.ExpiresAt) {
			delete(s.typing, key)
		}
	}
	s.typing[typingKey(signal.WorkspaceID, signal.Conversation, signal.UserID)] = signal
	return nil
}

func (s *Store) ListTypingSignals(_ context.Context, workspace domain.WorkspaceID, reader domain.UserID, now time.Time) ([]domain.TypingSignal, error) {
	if workspace == "" || reader == "" {
		return nil, store.InvalidArgument("typing signals require a workspace and a reader")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	signals := make([]domain.TypingSignal, 0, len(s.typing))
	for _, signal := range s.typing {
		if signal.WorkspaceID != workspace || signal.UserID == reader || !signal.Active(now) {
			continue
		}
		// Membership is the authority here exactly as it is for reading the
		// messages themselves. Without this check a signal would report that
		// someone is composing in a private conversation to a reader who
		// cannot see the conversation, let alone what is being composed.
		if members, ok := s.memberships[signal.Conversation]; ok {
			if _, member := members[reader]; member {
				signals = append(signals, signal)
			}
		}
	}
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Conversation != signals[j].Conversation {
			return signals[i].Conversation < signals[j].Conversation
		}
		return signals[i].UserID < signals[j].UserID
	})
	return signals, nil
}

func typingKey(workspace domain.WorkspaceID, conversation domain.ConversationID, user domain.UserID) string {
	return string(workspace) + "\x00" + string(conversation) + "\x00" + string(user)
}

func (s *Store) DueWorkflowDelays(_ context.Context, workspace domain.WorkspaceID, now time.Time, limit int) ([]domain.WorkflowStep, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("workflow delay limit must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.WorkflowStep, 0, limit)
	for _, step := range s.workflowSteps {
		if step.Status != domain.WorkflowStepWaiting || step.ResumeAt.IsZero() || step.ResumeAt.After(now) {
			continue
		}
		if workspace != "" && step.WorkspaceID != workspace {
			continue
		}
		values = append(values, step)
	}
	sort.Slice(values, func(left, right int) bool {
		if !values[left].ResumeAt.Equal(values[right].ResumeAt) {
			return values[left].ResumeAt.Before(values[right].ResumeAt)
		}
		return values[left].ID < values[right].ID
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *Store) SetThreadFollowed(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, root domain.MessageTimestamp, followed bool, event events.Event) error {
	if _, err := domain.ParseMessageTimestamp(root); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.conversations[conversation]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	if _, member := s.memberships[conversation][user]; !member {
		return store.ErrNotFound
	}
	key := threadFollowKey(workspace, user, conversation, root)
	if followed {
		s.threadFollows[key] = true
	} else {
		delete(s.threadFollows, key)
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ThreadSummaries(_ context.Context, conversation domain.ConversationID, roots []domain.MessageTimestamp) (map[domain.MessageTimestamp]domain.ThreadSummary, error) {
	summaries := make(map[domain.MessageTimestamp]domain.ThreadSummary, len(roots))
	if conversation == "" || len(roots) == 0 {
		return summaries, nil
	}
	wanted := make(map[domain.MessageTimestamp]struct{}, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(string(root)) != "" {
			wanted[root] = struct{}{}
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	participants := make(map[domain.MessageTimestamp]map[domain.UserID]struct{}, len(wanted))
	for _, message := range s.messages[conversation] {
		if message.Deleted || message.ThreadTimestamp == "" {
			continue
		}
		if _, ok := wanted[message.ThreadTimestamp]; !ok {
			continue
		}
		summary := summaries[message.ThreadTimestamp]
		summary.ReplyCount++
		if message.CreatedAt.After(summary.LastReplyAt) {
			summary.LastReplyAt = message.CreatedAt
		}
		summaries[message.ThreadTimestamp] = summary
		if participants[message.ThreadTimestamp] == nil {
			participants[message.ThreadTimestamp] = make(map[domain.UserID]struct{})
		}
		participants[message.ThreadTimestamp][message.AuthorID] = struct{}{}
	}
	for root, authors := range participants {
		summary := summaries[root]
		for author := range authors {
			summary.Participants = append(summary.Participants, author)
		}
		// Map iteration is unordered; the SQL profile returns a sorted list,
		// so sorting here is what makes the two projections identical.
		slices.Sort(summary.Participants)
		summaries[root] = summary
	}
	return summaries, nil
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
	readAt, err := domain.ParseMessageTimestamp(cursor.LastRead)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setReadCursorLocked(cursor, readAt, event)
	return nil
}

// SetReadCursors advances several cursors under one hold of the lock, so a
// concurrent reader sees the whole batch or none of it — the in-memory profile
// is a selectable profile, not a test double, and it owes the same all-or-
// nothing guarantee the SQL profiles give.
func (s *Store) SetReadCursors(_ context.Context, cursors []domain.ReadCursor, batch []events.Event) error {
	if len(cursors) != len(batch) {
		return store.InvalidArgument("each read cursor requires exactly one event")
	}
	readAt := make([]time.Time, len(cursors))
	for index, cursor := range cursors {
		parsed, err := domain.ParseMessageTimestamp(cursor.LastRead)
		if err != nil {
			return err
		}
		readAt[index] = parsed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, cursor := range cursors {
		s.setReadCursorLocked(cursor, readAt[index], batch[index])
	}
	return nil
}

func (s *Store) setReadCursorLocked(cursor domain.ReadCursor, readAt time.Time, event events.Event) {
	s.readCursors[readCursorKey(cursor.WorkspaceID, cursor.UserID, cursor.Conversation)] = cursor
	// Activity follows the cursor in BOTH directions: marking unread moves the
	// cursor backwards, and items after it must reopen, or the sidebar and
	// Activity disagree about the same conversation.
	for id, item := range s.activityItems {
		if item.WorkspaceID != cursor.WorkspaceID || item.UserID != cursor.UserID || item.Conversation != cursor.Conversation {
			continue
		}
		switch {
		case !item.OccurredAt.After(readAt) && item.ReadAt.IsZero():
			item.ReadAt = cursor.UpdatedAt.UTC()
			s.activityItems[id] = item
		case item.OccurredAt.After(readAt) && !item.ReadAt.IsZero():
			item.ReadAt = time.Time{}
			s.activityItems[id] = item
		}
	}
	s.outbox = append(s.outbox, event)
}

// LatestMessageTimestamps reports the newest undeleted message in each named
// conversation, omitting those that have none.
func (s *Store) LatestMessageTimestamps(_ context.Context, workspace domain.WorkspaceID, conversations []domain.ConversationID) (map[domain.ConversationID]domain.MessageTimestamp, error) {
	wanted := make(map[domain.ConversationID]bool, len(conversations))
	for _, conversation := range conversations {
		wanted[conversation] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	newest := make(map[domain.ConversationID]time.Time, len(conversations))
	for conversation, stored := range s.messages {
		if !wanted[conversation] {
			continue
		}
		for _, message := range stored {
			if message.WorkspaceID != workspace || message.Deleted {
				continue
			}
			at := message.CreatedAt
			if current, ok := newest[conversation]; !ok || at.After(current) {
				newest[conversation] = at
			}
		}
	}
	latest := make(map[domain.ConversationID]domain.MessageTimestamp, len(newest))
	for conversation, at := range newest {
		latest[conversation] = domain.NewMessageTimestamp(at)
	}
	return latest, nil
}

func activityPreferencesKey(workspace domain.WorkspaceID, user domain.UserID) string {
	return string(workspace) + "\x00" + string(user)
}

func activityCursorKey(item domain.ActivityItem) string {
	return fmt.Sprintf("%020d:%s", item.OccurredAt.UTC().UnixNano(), item.ID)
}

func (s *Store) ListActivity(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, query domain.ActivityQuery) (domain.ActivityPage, error) {
	if err := store.CheckPage(query.Page); err != nil {
		return domain.ActivityPage{}, err
	}
	if !query.Valid() {
		return domain.ActivityPage{}, store.InvalidArgument("activity filter is invalid")
	}
	after, err := domain.DecodeListCursor(query.Page.Cursor)
	if err != nil {
		return domain.ActivityPage{}, err
	}
	wantedKinds := make(map[domain.ActivityKind]struct{}, len(query.Kinds))
	for _, kind := range query.Kinds {
		wantedKinds[kind] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ActivityItem, 0, query.Page.Limit+1)
	for _, stored := range s.activityItems {
		if stored.WorkspaceID != workspace || stored.UserID != user {
			continue
		}
		if query.ClearedOnly != !stored.ClearedAt.IsZero() || (query.UnreadOnly && !stored.ReadAt.IsZero()) {
			continue
		}
		key := activityCursorKey(stored)
		if after != "" && key >= after {
			continue
		}
		if len(wantedKinds) > 0 {
			matches := false
			for _, kind := range stored.Kinds {
				if _, ok := wantedKinds[kind]; ok {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
		}
		item := stored
		item.Kinds = append([]domain.ActivityKind(nil), stored.Kinds...)
		if item.ReminderID != "" {
			if reminder, ok := s.laterReminders[item.ReminderID]; ok && laterReminderOwnedBy(reminder, workspace, user) {
				item.Reminder = reminder
				item.SourceAvailable = true
			}
		}
		if item.MessageID != "" {
			message, messageErr := s.messageLocked(item.MessageID)
			conversation, conversationExists := s.conversations[item.Conversation]
			if messageErr == nil && !message.Deleted && conversationExists && s.canViewActivitySourceLocked(workspace, user, conversation) {
				item.Message = s.cloneMessage(message)
				item.SourceAvailable = true
			}
		}
		if item.SharedInviteID != "" {
			// The decision stays readable even if the conversation later
			// becomes unreachable: it records what was decided about a request
			// this member made, which remains true either way.
			if invite, ok := s.sharedInvites[item.SharedInviteID]; ok && invite.WorkspaceID == workspace {
				item.SharedInviteStatus = invite.Status
				item.SourceAvailable = true
			}
		}
		if item.ListItemID != "" {
			// Like the canvas share, the row outlives the access that made it
			// reachable: it records that the work was assigned, and the reader
			// is told they can no longer open it rather than being offered a
			// link that would refuse them.
			if stored, ok := s.listItems[item.ListID][item.ListItemID]; ok {
				item.ListItem = stored.Summary()
				if list, listOK := s.lists[item.ListID]; listOK && list.WorkspaceID == workspace {
					item.ListName = list.Name
					_, _, _, allowed := s.resolveAccessLocked(workspace, list.OwnerID, user, func(visit func(string, string, string)) {
						for _, grant := range s.listAccess {
							if grant.ListID == list.ID {
								visit(grant.EntityType, grant.EntityID, grant.Access)
							}
						}
					})
					item.SourceAvailable = allowed
				}
			}
		}
		if item.CanvasID != "" {
			// A share stays in Activity after the grant is withdrawn, like every
			// other item whose source became unreachable: the row records that
			// it happened, and the reader is told they can no longer open it
			// rather than being shown a link that would refuse them.
			if canvas, ok := s.canvases[item.CanvasID]; ok && canvas.WorkspaceID == workspace {
				item.CanvasTitle = canvas.Title
				_, _, _, allowed := s.resolveAccessLocked(workspace, canvas.OwnerID, user, func(visit func(string, string, string)) {
					for _, grant := range s.canvasAccess {
						if grant.CanvasID == canvas.ID {
							visit(grant.EntityType, grant.EntityID, grant.Access)
						}
					}
				})
				item.SourceAvailable = allowed
			}
		}
		if item.CanvasID == "" && item.ListItemID == "" && item.SharedInviteID == "" && item.MessageID == "" && item.ReminderID == "" && slices.Contains(item.Kinds, domain.ActivityInvitation) {
			if conversation, ok := s.conversations[item.Conversation]; ok && s.canViewActivitySourceLocked(workspace, user, conversation) {
				item.SourceAvailable = true
			}
		}
		values = appendSorted(values, item, query.Page.Limit+1, func(left, right domain.ActivityItem) bool {
			return activityCursorKey(left) > activityCursorKey(right)
		})
	}
	page := domain.ActivityPage{Items: values, HasMore: len(values) > query.Page.Limit}
	if page.HasMore {
		page.Items = page.Items[:query.Page.Limit]
		page.NextCursor, err = domain.NewListCursor(activityCursorKey(page.Items[len(page.Items)-1]))
	}
	return page, err
}

func (s *Store) MutateActivity(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, ids []domain.ActivityID, mutation domain.ActivityMutation, changedAt time.Time) error {
	if workspace == "" || user == "" || len(ids) == 0 || !mutation.Valid() || changedAt.IsZero() {
		return store.InvalidArgument("activity mutation is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		item, ok := s.activityItems[id]
		if !ok || item.WorkspaceID != workspace || item.UserID != user {
			return store.ErrNotFound
		}
	}
	changedAt = changedAt.UTC()
	for _, id := range ids {
		item := s.activityItems[id]
		switch mutation {
		case domain.ActivityMarkRead:
			item.ReadAt = changedAt
		case domain.ActivityMarkUnread:
			item.ReadAt = time.Time{}
		case domain.ActivityClear:
			item.ReadAt = changedAt
			item.ClearedAt = changedAt
		case domain.ActivityRestore:
			item.ClearedAt = time.Time{}
		}
		s.activityItems[id] = item
	}
	return nil
}

func (s *Store) GetActivityPreferences(_ context.Context, workspace domain.WorkspaceID, user domain.UserID) (domain.ActivityPreferences, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if preferences, ok := s.activityPreferences[activityPreferencesKey(workspace, user)]; ok {
		return preferences, nil
	}
	return domain.ActivityPreferences{WorkspaceID: workspace, UserID: user, Layout: domain.ActivityDetailed}, nil
}

func (s *Store) SetActivityPreferences(_ context.Context, preferences domain.ActivityPreferences) error {
	if preferences.WorkspaceID == "" || preferences.UserID == "" || !preferences.Layout.Valid() {
		return store.InvalidArgument("activity preferences are invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activityPreferences[activityPreferencesKey(preferences.WorkspaceID, preferences.UserID)] = preferences
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
		if folded := domain.FoldSearchText(strings.TrimSpace(request.Query)); folded != "" {
			haystack := domain.FoldSearchText(conversation.Name + "\n" + conversation.Topic + "\n" + conversation.Purpose)
			if !strings.Contains(haystack, folded) {
				continue
			}
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
			if !request.IncludeClosedDirects && (conversation.IsDirect || conversation.IsGroupDirect) {
				if _, closed := s.closedDirects[directOpenKey(workspace, user, conversation.ID)]; closed {
					continue
				}
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

func (s *Store) CreateScheduledMessagePost(_ context.Context, id domain.ScheduledMessageID, message domain.Message, event events.Event) error {
	message, err := normalizeMessage(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scheduled, exists := s.scheduled[id]
	if id == "" || !exists || s.scheduledDelivered[id] ||
		scheduled.WorkspaceID != message.WorkspaceID || scheduled.Author != message.AuthorID || scheduled.Channel != message.Conversation {
		return store.ErrNotFound
	}
	return s.createMessageLocked(message, event, string(id))
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
	message, err := s.prepareMessageLocked(message, idempotencyKey)
	if err != nil {
		return err
	}
	s.commitMessageLocked(message, event, idempotencyKey)
	return nil
}

func (s *Store) prepareMessageLocked(message domain.Message, idempotencyKey string) (domain.Message, error) {
	if _, exists := s.conversations[message.Conversation]; !exists {
		return domain.Message{}, store.ErrNotFound
	}
	files := make([]domain.File, 0, len(message.Files))
	seenFiles := make(map[domain.FileID]struct{}, len(message.Files))
	for _, requested := range message.Files {
		if requested.ID == "" {
			return domain.Message{}, store.InvalidArgument("message file id is required")
		}
		if _, duplicate := seenFiles[requested.ID]; duplicate {
			return domain.Message{}, store.InvalidArgument("message file ids must be unique")
		}
		file, exists := s.files[requested.ID]
		if !exists || file.Deleted || file.WorkspaceID != message.WorkspaceID || !slices.Contains(s.fileShares[requested.ID], message.Conversation) {
			return domain.Message{}, store.ErrNotFound
		}
		seenFiles[requested.ID] = struct{}{}
		file.SharedChannels = append([]domain.ConversationID(nil), s.fileShares[requested.ID]...)
		files = append(files, file)
	}
	message.Files = files
	if _, exists := s.messageLocked(message.ID); exists == nil {
		return domain.Message{}, store.ErrAlreadyExists
	}
	key := idempotencyKeyFor(message.WorkspaceID, message.AuthorID, idempotencyKey)
	if idempotencyKey != "" {
		if _, exists := s.idempotency[key]; exists {
			return domain.Message{}, store.ErrIdempotencyConflict
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
			return domain.Message{}, store.ErrMessageTimestampTaken
		}
	}
	return message, nil
}

func (s *Store) commitMessageLocked(message domain.Message, event events.Event, idempotencyKey string) {
	values := s.messages[message.Conversation]
	index := sort.Search(len(values), func(index int) bool {
		current := values[index]
		return message.CreatedAt.Before(current.CreatedAt) || (message.CreatedAt.Equal(current.CreatedAt) && string(message.ID) < string(current.ID))
	})
	values = append(values, domain.Message{})
	copy(values[index+1:], values[index:])
	values[index] = message
	s.messages[message.Conversation] = values
	conversation := s.conversations[message.Conversation]
	if conversation.IsDirect || conversation.IsGroupDirect {
		for member := range s.memberships[message.Conversation] {
			delete(s.closedDirects, directOpenKey(message.WorkspaceID, member, message.Conversation))
		}
	}
	s.createMessageActivityLocked(message)
	s.outbox = append(s.outbox, event)
	if idempotencyKey != "" {
		key := idempotencyKeyFor(message.WorkspaceID, message.AuthorID, idempotencyKey)
		s.idempotency[key] = message.ID
	}
}

func (s *Store) createMessageActivityLocked(message domain.Message) {
	conversation := s.conversations[message.Conversation]
	recipients := make(map[domain.UserID]map[domain.ActivityKind]struct{})
	add := func(user domain.UserID, kind domain.ActivityKind) bool {
		if user == "" || user == message.AuthorID {
			return false
		}
		if !s.canViewActivitySourceLocked(message.WorkspaceID, user, conversation) {
			return false
		}
		if recipients[user] == nil {
			recipients[user] = make(map[domain.ActivityKind]struct{})
		}
		recipients[user][kind] = struct{}{}
		return true
	}
	root := domain.NewMessageTimestamp(message.CreatedAt)
	if message.ThreadTimestamp != "" {
		root = message.ThreadTimestamp
	}
	// Posting a root or replying follows that thread. A mention in a thread
	// follows it too, matching Slack's Threads/Activity contract.
	s.threadFollows[threadFollowKey(message.WorkspaceID, message.AuthorID, message.Conversation, root)] = true
	addMention := func(user domain.UserID) {
		if !add(user, domain.ActivityMention) {
			return
		}
		if message.ThreadTimestamp != "" {
			add(user, domain.ActivityThread)
			s.threadFollows[threadFollowKey(message.WorkspaceID, user, message.Conversation, root)] = true
		}
	}
	mentions := domain.MentionsInMessage(message.Text, message.Blocks)
	for _, user := range mentions.Users {
		addMention(user)
	}
	for _, groupID := range mentions.UserGroups {
		group, ok := s.userGroups[groupID]
		if !ok || group.WorkspaceID != message.WorkspaceID || !group.Enabled || !group.DeletedAt.IsZero() {
			continue
		}
		for _, user := range group.Users {
			addMention(user)
		}
	}
	for user := range s.memberships[message.Conversation] {
		if conversation.IsDirect || conversation.IsGroupDirect {
			add(user, domain.ActivityDM)
		}
		workspacePreferences := domain.DefaultWorkspaceNotificationPreferences(message.WorkspaceID, user)
		if stored, ok := s.workspaceNotificationPrefs[workspaceNotificationKey(message.WorkspaceID, user)]; ok {
			workspacePreferences = stored
		}
		conversationPreferences := domain.DefaultConversationNotificationPreferences(message.WorkspaceID, user, message.Conversation)
		if stored, ok := s.conversationNotificationPrefs[conversationNotificationKey(message.WorkspaceID, user, message.Conversation)]; ok {
			conversationPreferences = stored
		}
		effective := conversationPreferences.EffectiveLevel(workspacePreferences)
		if message.ThreadTimestamp == "" && !conversation.IsDirect && !conversation.IsGroupDirect {
			if effective == domain.NotificationAll && workspacePreferences.ActivityChannels {
				add(user, domain.ActivityChannel)
			}
			if effective != domain.NotificationMute && domain.MatchesNotificationKeyword(message.Text, workspacePreferences.Keywords) {
				add(user, domain.ActivityKeyword)
			}
		}
		if message.ThreadTimestamp != "" &&
			(conversationPreferences.FollowEveryThread || s.threadFollows[threadFollowKey(message.WorkspaceID, user, message.Conversation, root)]) {
			add(user, domain.ActivityThread)
		}
	}
	if message.ThreadTimestamp != "" {
		if rootAt, err := domain.ParseMessageTimestamp(message.ThreadTimestamp); err == nil {
			for _, candidate := range s.messages[message.Conversation] {
				if candidate.CreatedAt.Equal(rootAt) {
					add(candidate.AuthorID, domain.ActivityThread)
					s.threadFollows[threadFollowKey(message.WorkspaceID, candidate.AuthorID, message.Conversation, root)] = true
					break
				}
			}
		}
	}
	for user, kindSet := range recipients {
		kinds := make([]domain.ActivityKind, 0, len(kindSet)+1)
		for kind := range kindSet {
			kinds = append(kinds, kind)
		}
		if message.AppID != "" {
			kinds = append(kinds, domain.ActivityApp)
		}
		slices.Sort(kinds)
		id := domain.ActivityIDFor(user, "message:"+string(message.ID))
		s.activityItems[id] = domain.ActivityItem{
			ID: id, WorkspaceID: message.WorkspaceID, UserID: user, Kinds: kinds,
			ActorID: message.AuthorID, Conversation: message.Conversation,
			MessageID: message.ID, OccurredAt: message.CreatedAt.UTC(),
		}
	}
}

// canViewActivitySourceLocked applies the same visibility boundary as a normal
// conversation read. Public-channel mentions can notify an active workspace
// member who has not joined yet; private conversations and DMs require
// membership, and a private access-group restriction remains authoritative.
func (s *Store) canViewActivitySourceLocked(workspace domain.WorkspaceID, user domain.UserID, conversation domain.Conversation) bool {
	account, ok := s.users[user]
	if !ok || account.WorkspaceID != workspace || account.Deleted {
		return false
	}
	membership, ok := s.members[string(workspace)+"\x00"+string(user)]
	if !ok || !membership.Active {
		return false
	}
	private := conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect
	if !private {
		return conversation.WorkspaceID == workspace
	}
	if _, member := s.memberships[conversation.ID][user]; !member {
		return false
	}
	groups := s.conversationAccess[conversation.ID]
	if len(groups) == 0 {
		return true
	}
	for _, groupID := range groups {
		group, exists := s.userGroups[groupID]
		if !exists || group.WorkspaceID != workspace || !group.Enabled || !group.DeletedAt.IsZero() {
			continue
		}
		if slices.Contains(group.Users, user) {
			return true
		}
	}
	return false
}

func (s *Store) CreateFileShareMessage(_ context.Context, fileIDs []domain.FileID, message domain.Message, emitted []events.Event) error {
	if len(fileIDs) == 0 || len(emitted) == 0 {
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
	if err := s.createMessageLocked(normalized, emitted[0], ""); err != nil {
		for fileID, channels := range previous {
			s.fileShares[fileID] = channels
		}
		return err
	}
	s.outbox = append(s.outbox, emitted[1:]...)
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
				return s.cloneMessage(message), nil
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
			return s.cloneMessage(message), nil
		}
	}
	return domain.Message{}, store.ErrNotFound
}

func reactionKey(name string, user domain.UserID) string { return name + "\x00" + string(user) }

func (s *Store) AddReaction(_ context.Context, reaction domain.Reaction, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message, err := s.messageLocked(reaction.Message)
	if err != nil {
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
	if message.AuthorID != reaction.UserID {
		if _, member := s.memberships[message.Conversation][message.AuthorID]; member {
			id := domain.ActivityIDFor(message.AuthorID, "reaction:"+string(reaction.Message)+":"+reaction.Name+":"+string(reaction.UserID))
			s.activityItems[id] = domain.ActivityItem{
				ID: id, WorkspaceID: message.WorkspaceID, UserID: message.AuthorID,
				Kinds: []domain.ActivityKind{domain.ActivityReaction}, ActorID: reaction.UserID,
				Conversation: message.Conversation, MessageID: message.ID,
				ReactionName: reaction.Name, OccurredAt: reaction.CreatedAt.UTC(),
			}
		}
	}
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
	if message, err := s.messageLocked(reaction.Message); err == nil {
		delete(s.activityItems, domain.ActivityIDFor(message.AuthorID, "reaction:"+string(reaction.Message)+":"+reaction.Name+":"+string(reaction.UserID)))
	}
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
				item := domain.UserReaction{Conversation: conversationID, Message: s.cloneMessage(message), Reaction: reaction}
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
		star.Message = s.cloneMessage(star.Message)
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

func (s *Store) CreateLaterReminder(_ context.Context, reminder domain.LaterReminder, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !reminder.Target.Valid() || !reminder.Recurrence.Valid() || reminder.ID == "" || reminder.WorkspaceID == "" || reminder.Creator == "" || reminder.Text == "" || reminder.DueAt.IsZero() {
		return store.InvalidArgument("later reminder is incomplete")
	}
	creator, ok := s.users[reminder.Creator]
	if !ok || creator.WorkspaceID != reminder.WorkspaceID || creator.Deleted {
		return store.ErrNotFound
	}
	switch reminder.Target {
	case domain.LaterReminderPersonal:
		user, ok := s.users[reminder.UserID]
		if !ok || user.WorkspaceID != reminder.WorkspaceID || user.Deleted || reminder.Channel != "" {
			return store.ErrNotFound
		}
	case domain.LaterReminderChannel:
		conversation, ok := s.conversations[reminder.Channel]
		if !ok || conversation.WorkspaceID != reminder.WorkspaceID || reminder.UserID != "" {
			return store.ErrNotFound
		}
	}
	if reminder.SourceMessageID != "" {
		message, ok := memoryMessageByID(s.messages[reminder.SourceConversation], reminder.SourceMessageID)
		if !ok || message.WorkspaceID != reminder.WorkspaceID || domain.NewMessageTimestamp(message.CreatedAt) != reminder.SourceTimestamp {
			return store.ErrNotFound
		}
	}
	if _, exists := s.laterReminders[reminder.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.laterReminders[reminder.ID] = reminder
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetLaterReminder(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.LaterReminderID) (domain.LaterReminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reminder, ok := s.laterReminders[id]
	if !ok || !laterReminderOwnedBy(reminder, workspace, user) {
		return domain.LaterReminder{}, store.ErrNotFound
	}
	return reminder, nil
}

func (s *Store) ListLaterReminders(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, target domain.LaterReminderTarget, request domain.PageRequest) (domain.LaterReminderPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.LaterReminderPage{}, err
	}
	if !target.Valid() {
		return domain.LaterReminderPage{}, store.InvalidArgument("later reminder target is invalid")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.LaterReminderPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.LaterReminder, 0, request.Limit+1)
	for _, reminder := range s.laterReminders {
		if reminder.Target != target || !laterReminderOwnedBy(reminder, workspace, user) || string(reminder.ID) <= after {
			continue
		}
		values = appendSorted(values, reminder, request.Limit+1, func(left, right domain.LaterReminder) bool { return left.ID < right.ID })
	}
	page := domain.LaterReminderPage{Items: values, HasMore: len(values) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(page.Items[len(page.Items)-1].ID))
	}
	return page, err
}

func (s *Store) UpdateLaterReminder(_ context.Context, reminder domain.LaterReminder, event events.Event) (domain.LaterReminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.laterReminders[reminder.ID]
	activeLease, leased := s.laterReminderLeases[reminder.ID]
	if !ok || !laterReminderOwnedBy(current, reminder.WorkspaceID, reminder.Creator) || current.Target != domain.LaterReminderPersonal ||
		(leased && activeLease.Expires.After(time.Now().UTC())) {
		return domain.LaterReminder{}, store.ErrNotFound
	}
	if reminder.Target != current.Target || reminder.UserID != current.UserID || reminder.SourceMessageID != current.SourceMessageID || reminder.SourceConversation != current.SourceConversation || reminder.SourceTimestamp != current.SourceTimestamp || reminder.Text == "" || reminder.DueAt.IsZero() || !reminder.Recurrence.Valid() {
		return domain.LaterReminder{}, store.InvalidArgument("later reminder update is invalid")
	}
	reminder.CreatedAt = current.CreatedAt
	reminder.LastDeliveredAt = time.Time{}
	reminder.AcknowledgedAt = time.Time{}
	reminder.FailedAt = time.Time{}
	reminder.FailureCode = ""
	s.laterReminders[reminder.ID] = reminder
	delete(s.laterReminderLeases, reminder.ID)
	delete(s.laterReminderNextAttempt, reminder.ID)
	s.outbox = append(s.outbox, event)
	return reminder, nil
}

func (s *Store) AcknowledgeLaterReminders(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, acknowledged time.Time, event events.Event) error {
	if workspace == "" || user == "" || acknowledged.IsZero() {
		return store.InvalidArgument("Later reminder acknowledgement is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for id, reminder := range s.laterReminders {
		if reminder.WorkspaceID != workspace || reminder.Target != domain.LaterReminderPersonal || reminder.UserID != user || reminder.LastDeliveredAt.IsZero() || !reminder.LastDeliveredAt.After(reminder.AcknowledgedAt) {
			continue
		}
		reminder.AcknowledgedAt = reminder.LastDeliveredAt
		reminder.UpdatedAt = acknowledged.UTC()
		s.laterReminders[id] = reminder
		activityID := domain.ActivityIDFor(user, "reminder:"+string(id)+":"+string(domain.NewStoredTime(reminder.LastDeliveredAt)))
		if item, ok := s.activityItems[activityID]; ok && item.ReadAt.IsZero() {
			item.ReadAt = acknowledged.UTC()
			s.activityItems[activityID] = item
		}
		found = true
	}
	if found {
		s.outbox = append(s.outbox, event)
	}
	return nil
}

func (s *Store) CompleteLaterReminder(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.LaterReminderID, completed time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.laterReminders[id]
	activeLease, leased := s.laterReminderLeases[id]
	if !ok || !laterReminderOwnedBy(reminder, workspace, user) || reminder.Target != domain.LaterReminderPersonal ||
		(leased && activeLease.Expires.After(time.Now().UTC())) {
		return store.ErrNotFound
	}
	if reminder.CompletedAt.IsZero() {
		reminder.CompletedAt = completed
		reminder.UpdatedAt = completed
		s.laterReminders[id] = reminder
		s.outbox = append(s.outbox, event)
	}
	return nil
}

func (s *Store) DeleteLaterReminder(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.LaterReminderID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.laterReminders[id]
	activeLease, leased := s.laterReminderLeases[id]
	if !ok || !laterReminderOwnedBy(reminder, workspace, user) || (leased && activeLease.Expires.After(time.Now().UTC())) {
		return store.ErrNotFound
	}
	delete(s.laterReminders, id)
	for activityID, item := range s.activityItems {
		if item.ReminderID == id {
			delete(s.activityItems, activityID)
		}
	}
	delete(s.laterReminderLeases, id)
	delete(s.laterReminderNextAttempt, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) EarliestLaterReminder(_ context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for id, reminder := range s.laterReminders {
		if (workspace != "" && reminder.WorkspaceID != workspace) || !reminder.CompletedAt.IsZero() || !reminder.FailedAt.IsZero() {
			continue
		}
		deadline := reminder.DueAt.UTC()
		if next := s.laterReminderNextAttempt[id]; next.After(deadline) {
			deadline = next
		}
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, nil
}

func (s *Store) ClaimDueLaterReminders(_ context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]domain.LaterReminder, error) {
	if owner == "" || limit <= 0 || lease <= 0 || now.IsZero() {
		return nil, store.InvalidArgument("Later reminder claim requires owner, positive limit, lease, and current time")
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]domain.LaterReminder, 0, len(s.laterReminders))
	for id, reminder := range s.laterReminders {
		if (workspace != "" && reminder.WorkspaceID != workspace) || reminder.DueAt.After(now) ||
			!reminder.CompletedAt.IsZero() || !reminder.FailedAt.IsZero() || s.laterReminderNextAttempt[id].After(now) {
			continue
		}
		active, exists := s.laterReminderLeases[id]
		if exists && active.Expires.After(now) {
			continue
		}
		values = append(values, reminder)
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].DueAt.Before(values[right].DueAt) ||
			(values[left].DueAt.Equal(values[right].DueAt) && values[left].ID < values[right].ID)
	})
	if len(values) > limit {
		values = values[:limit]
	}
	for _, reminder := range values {
		s.laterReminderLeases[reminder.ID] = memoryLease{Owner: owner, Expires: now.Add(lease)}
	}
	return values, nil
}

func (s *Store) RenewLaterReminder(_ context.Context, owner string, id domain.LaterReminderID, lease time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.laterReminderLeases[id]
	if owner == "" || lease <= 0 || now.IsZero() || !ok || active.Owner != owner || !active.Expires.After(now.UTC()) {
		return store.ErrLeaseConflict
	}
	active.Expires = now.UTC().Add(lease)
	s.laterReminderLeases[id] = active
	return nil
}

func (s *Store) MarkLaterReminderDelivered(_ context.Context, owner string, id domain.LaterReminderID, deliveredAt, nextDue time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.laterReminderLeases[id]
	reminder, exists := s.laterReminders[id]
	deliveredAt = deliveredAt.UTC()
	if !ok || !exists || active.Owner != owner || !active.Expires.After(deliveredAt) || !reminder.CompletedAt.IsZero() || !reminder.FailedAt.IsZero() {
		return store.ErrLeaseConflict
	}
	if reminder.Recurrence == domain.ReminderOnce {
		if !nextDue.IsZero() {
			return store.InvalidArgument("one-time Later reminder cannot have a next delivery")
		}
		reminder.CompletedAt = deliveredAt
	} else {
		if nextDue.IsZero() || !nextDue.After(reminder.DueAt) {
			return store.InvalidArgument("recurring Later reminder requires a later delivery")
		}
		reminder.DueAt = nextDue.UTC()
	}
	reminder.LastDeliveredAt = deliveredAt
	reminder.UpdatedAt = deliveredAt
	s.laterReminders[id] = reminder
	notificationPreferences := domain.DefaultWorkspaceNotificationPreferences(reminder.WorkspaceID, reminder.UserID)
	if stored, ok := s.workspaceNotificationPrefs[workspaceNotificationKey(reminder.WorkspaceID, reminder.UserID)]; ok {
		notificationPreferences = stored
	}
	if reminder.Target == domain.LaterReminderPersonal && reminder.UserID != "" && notificationPreferences.ActivityReminders {
		activityID := domain.ActivityIDFor(reminder.UserID, "reminder:"+string(id)+":"+string(domain.NewStoredTime(deliveredAt)))
		s.activityItems[activityID] = domain.ActivityItem{
			ID: activityID, WorkspaceID: reminder.WorkspaceID, UserID: reminder.UserID,
			Kinds: []domain.ActivityKind{domain.ActivityReminder}, ReminderID: id,
			Conversation: reminder.SourceConversation, MessageID: reminder.SourceMessageID,
			OccurredAt: deliveredAt,
		}
	}
	delete(s.laterReminderLeases, id)
	delete(s.laterReminderNextAttempt, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) MarkLaterReminderFailed(_ context.Context, owner string, id domain.LaterReminderID, failureCode string, failedAt time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.laterReminderLeases[id]
	reminder, exists := s.laterReminders[id]
	failedAt = failedAt.UTC()
	if failureCode == "" || !ok || !exists || active.Owner != owner || !active.Expires.After(failedAt) || !reminder.CompletedAt.IsZero() || !reminder.FailedAt.IsZero() {
		return store.ErrLeaseConflict
	}
	reminder.FailedAt = failedAt
	reminder.FailureCode = failureCode
	reminder.UpdatedAt = failedAt
	s.laterReminders[id] = reminder
	delete(s.laterReminderLeases, id)
	delete(s.laterReminderNextAttempt, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ReleaseLaterReminder(_ context.Context, owner string, id domain.LaterReminderID, next, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.laterReminderLeases[id]
	if owner == "" || next.IsZero() || now.IsZero() || !ok || active.Owner != owner || !active.Expires.After(now.UTC()) {
		return store.ErrLeaseConflict
	}
	delete(s.laterReminderLeases, id)
	s.laterReminderNextAttempt[id] = next.UTC()
	return nil
}

func laterReminderOwnedBy(reminder domain.LaterReminder, workspace domain.WorkspaceID, user domain.UserID) bool {
	if reminder.WorkspaceID != workspace {
		return false
	}
	if reminder.Target == domain.LaterReminderPersonal {
		return reminder.UserID == user
	}
	return reminder.Target == domain.LaterReminderChannel && reminder.Creator == user
}

func memoryMessageByID(messages []domain.Message, id domain.MessageID) (domain.Message, bool) {
	for _, message := range messages {
		if message.ID == id {
			return message, true
		}
	}
	return domain.Message{}, false
}

func cloneScheduledMessage(value domain.ScheduledMessage) domain.ScheduledMessage {
	value.FileAttachments = append([]domain.DraftAttachment(nil), value.FileAttachments...)
	return value
}

func (s *Store) validateScheduledAttachmentsLocked(value domain.ScheduledMessage) error {
	if len(value.FileAttachments) > 10 {
		return store.InvalidArgument("scheduled message has too many file attachments")
	}
	seen := make(map[domain.ExternalUploadID]struct{}, len(value.FileAttachments))
	for _, attachment := range value.FileAttachments {
		upload, exists := s.externalUploads[attachment.UploadID]
		if attachment.UploadID == "" || attachment.Name == "" || attachment.MIMEType == "" || attachment.Size <= 0 ||
			!exists || upload.WorkspaceID != value.WorkspaceID || upload.Uploader != value.Author ||
			upload.Status != domain.ExternalUploadUploaded {
			return store.InvalidArgument("scheduled message file attachment is incomplete")
		}
		if _, duplicate := seen[attachment.UploadID]; duplicate {
			return store.InvalidArgument("scheduled message file attachment is duplicated")
		}
		for id, scheduled := range s.scheduled {
			if id == value.ID || s.scheduledDelivered[id] {
				continue
			}
			for _, existing := range scheduled.FileAttachments {
				if existing.UploadID == attachment.UploadID {
					return store.ErrAlreadyExists
				}
			}
		}
		seen[attachment.UploadID] = struct{}{}
	}
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
	if err := s.validateScheduledAttachmentsLocked(value); err != nil {
		return err
	}
	s.scheduled[value.ID] = cloneScheduledMessage(value)
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
	if err := s.validateScheduledAttachmentsLocked(value); err != nil {
		return err
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
	s.scheduled[value.ID] = cloneScheduledMessage(value)
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
		if value.WorkspaceID != workspace || value.Author != user || s.scheduledDelivered[value.ID] || !value.FailedAt.IsZero() || (channel != "" && value.Channel != channel) || (after != "" && string(value.ID) <= after) {
			continue
		}
		values = appendSorted(values, cloneScheduledMessage(value), request.Limit+1, func(left, right domain.ScheduledMessage) bool { return left.ID < right.ID })
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
		values = appendSorted(values, cloneScheduledMessage(value), query.Page.Limit+1, func(left, right domain.ScheduledMessage) bool {
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

func (s *Store) ListScheduledMessageHistory(_ context.Context, workspace domain.WorkspaceID, credentialHash string, includeDelivered bool, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if credentialHash == "" {
		return domain.ScheduledMessagePage{}, store.InvalidArgument("scheduled-message credential is required")
	}
	if err := store.CheckPage(request); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	afterTime, afterID, err := store.ParseScheduledMessageCursor(request.Cursor)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ScheduledMessage, 0, request.Limit+1)
	for _, value := range s.scheduled {
		if value.WorkspaceID != workspace || value.CredentialHash != credentialHash ||
			(!includeDelivered && s.scheduledDelivered[value.ID]) {
			continue
		}
		if !afterTime.IsZero() {
			beforeCursor := value.PostAt.Before(afterTime) || (value.PostAt.Equal(afterTime) && value.ID < afterID)
			afterCursor := value.PostAt.After(afterTime) || (value.PostAt.Equal(afterTime) && value.ID > afterID)
			if (request.Descending && !beforeCursor) || (!request.Descending && !afterCursor) {
				continue
			}
		}
		less := func(left, right domain.ScheduledMessage) bool {
			return left.PostAt.Before(right.PostAt) || (left.PostAt.Equal(right.PostAt) && left.ID < right.ID)
		}
		if request.Descending {
			less = func(left, right domain.ScheduledMessage) bool {
				return left.PostAt.After(right.PostAt) || (left.PostAt.Equal(right.PostAt) && left.ID > right.ID)
			}
		}
		values = appendSorted(values, cloneScheduledMessage(value), request.Limit+1, less)
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
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

func (s *Store) GetScheduledMessage(_ context.Context, workspace domain.WorkspaceID, id domain.ScheduledMessageID) (domain.ScheduledMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.scheduled[id]
	if !exists || value.WorkspaceID != workspace || s.scheduledDelivered[id] {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	return cloneScheduledMessage(value), nil
}

func (s *Store) UpdateScheduledMessageWithinLimit(_ context.Context, update domain.ScheduledMessageUpdate, window time.Duration, limit int, event events.Event) (domain.ScheduledMessage, error) {
	if update.WorkspaceID == "" || update.ID == "" || update.Channel == "" || update.CredentialHash == "" || update.PostAt.IsZero() || window <= 0 || limit <= 0 {
		return domain.ScheduledMessage{}, store.InvalidArgument("scheduled-message update is incomplete")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.scheduled[update.ID]
	lease, leased := s.scheduledLeases[update.ID]
	if !ok || value.WorkspaceID != update.WorkspaceID || value.Channel != update.Channel || value.CredentialHash != update.CredentialHash ||
		s.scheduledDelivered[update.ID] || (leased && lease.Expires.After(now)) {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	if strings.TrimSpace(update.Text) == "" && value.Blocks == "" && value.Attachments == "" && len(value.FileAttachments) == 0 {
		return domain.ScheduledMessage{}, store.InvalidArgument("scheduled message would be empty")
	}
	nearby := make([]time.Time, 0, limit)
	for id, scheduled := range s.scheduled {
		if id == update.ID {
			continue
		}
		if scheduled.WorkspaceID == value.WorkspaceID && scheduled.Channel == value.Channel &&
			!s.scheduledDelivered[id] && scheduled.FailedAt.IsZero() &&
			!scheduled.PostAt.Before(update.PostAt.Add(-window)) && !scheduled.PostAt.After(update.PostAt.Add(window)) {
			nearby = append(nearby, scheduled.PostAt)
		}
	}
	if store.ScheduledMessageLimitExceeded(nearby, update.PostAt, window, limit) {
		return domain.ScheduledMessage{}, store.ErrScheduledMessageLimit
	}
	value.Text = update.Text
	value.PostAt = update.PostAt.UTC()
	value.FailedAt = time.Time{}
	value.FailureCode = ""
	s.scheduled[update.ID] = value
	delete(s.scheduledNextAttempt, update.ID)
	s.outbox = append(s.outbox, event)
	return cloneScheduledMessage(value), nil
}

func (s *Store) ClaimScheduledMessageForCredential(_ context.Context, workspace domain.WorkspaceID, credentialHash string, id domain.ScheduledMessageID, owner string, lease time.Duration) (domain.ScheduledMessage, error) {
	if workspace == "" || credentialHash == "" || id == "" || owner == "" || lease <= 0 {
		return domain.ScheduledMessage{}, store.InvalidArgument("scheduled-message claim is incomplete")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.scheduled[id]
	active, leased := s.scheduledLeases[id]
	if !ok || value.WorkspaceID != workspace || value.CredentialHash != credentialHash || s.scheduledDelivered[id] ||
		(leased && active.Expires.After(now)) {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	s.scheduledLeases[id] = memoryLease{Owner: owner, Expires: now.Add(lease)}
	return cloneScheduledMessage(value), nil
}

func (s *Store) DeleteScheduledMessage(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.scheduled[id]
	lease, leased := s.scheduledLeases[id]
	_, posted := s.idempotency[idempotencyKeyFor(workspace, user, string(id))]
	if !ok || posted || value.WorkspaceID != workspace || value.Author != user || value.Channel != channel || (leased && lease.Expires.After(time.Now().UTC())) {
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
	_, posted := s.idempotency[idempotencyKeyFor(value.WorkspaceID, value.Author, string(id))]
	if credentialHash == "" || !ok || value.WorkspaceID != workspace || value.CredentialHash != credentialHash || value.Channel != channel ||
		posted || s.scheduledDelivered[id] || (leased && lease.Expires.After(time.Now().UTC())) {
		return store.ErrNotFound
	}
	delete(s.scheduled, id)
	delete(s.scheduledNextAttempt, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func draftKey(workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) string {
	return string(workspace) + "\x00" + string(user) + "\x00" + string(conversation) + "\x00" + string(thread)
}

func draftCursorKey(value domain.Draft) string {
	return string(domain.NewStoredTime(value.UpdatedAt)) + "\x00" + string(value.ConversationID) + "\x00" + string(value.ThreadTimestamp)
}

func (s *Store) UpsertDraft(_ context.Context, value domain.Draft, event events.Event) (domain.Draft, error) {
	if value.WorkspaceID == "" || value.UserID == "" || value.ConversationID == "" ||
		(strings.TrimSpace(value.Text) == "" && len(value.Attachments) == 0) || len(value.Attachments) > 10 || value.UpdatedAt.IsZero() {
		return domain.Draft{}, store.InvalidArgument("draft is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, userExists := s.users[value.UserID]
	conversation, conversationExists := s.conversations[value.ConversationID]
	if !userExists || user.WorkspaceID != value.WorkspaceID || user.Deleted || !conversationExists || conversation.WorkspaceID != value.WorkspaceID {
		return domain.Draft{}, store.ErrNotFound
	}
	seenUploads := make(map[domain.ExternalUploadID]struct{}, len(value.Attachments))
	for _, attachment := range value.Attachments {
		upload, exists := s.externalUploads[attachment.UploadID]
		if attachment.UploadID == "" || attachment.Name == "" || attachment.MIMEType == "" || attachment.Size <= 0 ||
			!exists || upload.WorkspaceID != value.WorkspaceID || upload.Uploader != value.UserID || upload.Status != domain.ExternalUploadUploaded {
			return domain.Draft{}, store.InvalidArgument("draft attachment is incomplete")
		}
		if _, duplicate := seenUploads[attachment.UploadID]; duplicate {
			return domain.Draft{}, store.InvalidArgument("draft attachment is duplicated")
		}
		seenUploads[attachment.UploadID] = struct{}{}
	}
	value.UpdatedAt = value.UpdatedAt.UTC()
	value.Attachments = append([]domain.DraftAttachment(nil), value.Attachments...)
	s.drafts[draftKey(value.WorkspaceID, value.UserID, value.ConversationID, value.ThreadTimestamp)] = value
	s.outbox = append(s.outbox, event)
	return cloneDraft(value), nil
}

func cloneDraft(value domain.Draft) domain.Draft {
	value.Attachments = append([]domain.DraftAttachment(nil), value.Attachments...)
	return value
}

func (s *Store) GetDraft(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.Draft, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.drafts[draftKey(workspace, user, conversation, thread)]
	if !ok {
		return domain.Draft{}, store.ErrNotFound
	}
	return cloneDraft(value), nil
}

func (s *Store) ListDrafts(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.DraftPage, error) {
	if err := store.CheckPage(request); err != nil {
		return domain.DraftPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.DraftPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Draft, 0, request.Limit+1)
	for _, value := range s.drafts {
		if value.WorkspaceID != workspace || value.UserID != user {
			continue
		}
		key := draftCursorKey(value)
		if after != "" && ((request.Descending && key >= after) || (!request.Descending && key <= after)) {
			continue
		}
		less := func(left, right domain.Draft) bool { return draftCursorKey(left) < draftCursorKey(right) }
		if request.Descending {
			less = func(left, right domain.Draft) bool { return draftCursorKey(left) > draftCursorKey(right) }
		}
		values = appendSorted(values, cloneDraft(value), request.Limit+1, less)
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.DraftPage{Items: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(draftCursorKey(values[len(values)-1]))
	}
	return page, err
}

func (s *Store) DeleteDraft(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := draftKey(workspace, user, conversation, thread)
	if _, ok := s.drafts[key]; !ok {
		return store.ErrNotFound
	}
	delete(s.drafts, key)
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
		values = append(values, cloneScheduledMessage(value))
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
	value.FailedAt = time.Time{}
	value.FailureCode = ""
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

func (s *Store) StartHuddle(_ context.Context, value domain.Call, started, joined events.Event) (domain.Call, bool, error) {
	if value.Kind != domain.CallKindHuddle || value.ConversationID == "" || value.CreatedBy == "" {
		return domain.Call{}, false, store.InvalidArgument("a huddle requires a conversation and a creator")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if conversation, ok := s.conversations[value.ConversationID]; !ok || conversation.WorkspaceID != value.WorkspaceID {
		return domain.Call{}, false, store.ErrNotFound
	}
	for id, existing := range s.calls {
		if existing.WorkspaceID != value.WorkspaceID || existing.Kind != domain.CallKindHuddle || existing.ConversationID != value.ConversationID || !existing.Active() {
			continue
		}
		if !slices.Contains(existing.Participants, value.CreatedBy) {
			existing.Participants = append(existing.Participants, value.CreatedBy)
			s.calls[id] = existing
			s.outbox = append(s.outbox, joined)
		}
		return cloneCall(existing), false, nil
	}
	if _, exists := s.calls[value.ID]; exists {
		return domain.Call{}, false, store.ErrAlreadyExists
	}
	value.Participants = []domain.UserID{value.CreatedBy}
	s.calls[value.ID] = cloneCall(value)
	s.outbox = append(s.outbox, started)
	return cloneCall(value), true, nil
}

func (s *Store) ActiveHuddle(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) (domain.Call, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, existing := range s.calls {
		if existing.WorkspaceID == workspace && existing.Kind == domain.CallKindHuddle && existing.ConversationID == conversation && existing.Active() {
			return cloneCall(existing), nil
		}
	}
	return domain.Call{}, store.ErrNotFound
}

func (s *Store) JoinCall(_ context.Context, workspace domain.WorkspaceID, id domain.CallID, user domain.UserID, event events.Event) (domain.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.calls[id]
	if !ok || value.WorkspaceID != workspace {
		return domain.Call{}, store.ErrNotFound
	}
	if !value.Active() {
		return domain.Call{}, store.ErrConflict
	}
	if slices.Contains(value.Participants, user) {
		return cloneCall(value), nil
	}
	value.Participants = append(value.Participants, user)
	s.calls[id] = value
	s.outbox = append(s.outbox, event)
	return cloneCall(value), nil
}

func (s *Store) LeaveCall(_ context.Context, workspace domain.WorkspaceID, id domain.CallID, user domain.UserID, left, ended events.Event) (domain.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.calls[id]
	if !ok || value.WorkspaceID != workspace {
		return domain.Call{}, store.ErrNotFound
	}
	if !value.Active() {
		return domain.Call{}, store.ErrConflict
	}
	if !slices.Contains(value.Participants, user) {
		return cloneCall(value), nil
	}
	value.Participants = slices.DeleteFunc(append([]domain.UserID(nil), value.Participants...), func(candidate domain.UserID) bool { return candidate == user })
	s.outbox = append(s.outbox, left)
	if len(value.Participants) == 0 {
		value.EndedAt = ended.CreatedAt.UTC()
		value.DurationSeconds = int64(value.EndedAt.Sub(value.StartedAt).Seconds())
		if value.DurationSeconds < 0 {
			value.DurationSeconds = 0
		}
		s.outbox = append(s.outbox, ended)
	}
	s.calls[id] = value
	return cloneCall(value), nil
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
	shares := make([]domain.ConversationID, 0, len(file.SharedChannels))
	seen := make(map[domain.ConversationID]struct{}, len(file.SharedChannels))
	for _, conversationID := range file.SharedChannels {
		conversation, exists := s.conversations[conversationID]
		if !exists || conversation.WorkspaceID != file.WorkspaceID {
			return store.ErrNotFound
		}
		if _, duplicate := seen[conversationID]; duplicate {
			continue
		}
		seen[conversationID] = struct{}{}
		shares = append(shares, conversationID)
	}
	file.SharedChannels = nil
	s.files[file.ID] = file
	s.fileShares[file.ID] = shares
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

// SetFileDescription mirrors the SQL profile, including the part that decides
// who may write: the uploader, checked under the same lock as the write.
func (s *Store) SetFileDescription(_ context.Context, workspace domain.WorkspaceID, id domain.FileID, uploader domain.UserID, description string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[id]
	if !ok || file.Deleted || file.WorkspaceID != workspace || file.Uploader != uploader {
		return store.ErrNotFound
	}
	file.Description = description
	s.files[id] = file
	s.outbox = append(s.outbox, event)
	return nil
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

func (s *Store) ListVisibleFiles(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.FilePage, error) {
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
		if file.WorkspaceID != workspace || file.Deleted || (after != "" && string(file.ID) <= after) || !s.fileVisibleToUser(file, user) {
			continue
		}
		file.SharedChannels = append([]domain.ConversationID(nil), s.fileShares[file.ID]...)
		values = appendSorted(values, file, request.Limit+1, func(left, right domain.File) bool { return left.ID < right.ID })
	}
	s.mu.RUnlock()
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.FilePage{Files: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) SearchFiles(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, search domain.FileSearch) (domain.FilePage, error) {
	if search.Count <= 0 || search.Page <= 0 || search.Page > 100 {
		return domain.FilePage{}, store.InvalidArgument("file search page is invalid")
	}
	s.mu.RLock()
	values := make([]domain.File, 0)
	for _, file := range s.files {
		if file.WorkspaceID != workspace || file.Deleted || !s.fileVisibleToUser(file, user) {
			continue
		}
		text := domain.FoldSearchText(file.Name + " " + file.Title)
		if !searchTextMatches(text, search.Terms, search.ExcludedTerms) ||
			(search.Uploader != "" && file.Uploader != search.Uploader) ||
			(search.ExcludedUploader != "" && file.Uploader == search.ExcludedUploader) ||
			(search.Conversation != "" && !s.fileSharedIn(file.ID, search.Conversation)) ||
			(search.ExcludedConversation != "" && s.fileSharedIn(file.ID, search.ExcludedConversation)) ||
			(search.FileType != "" && !fileMatchesType(file, search.FileType)) ||
			(!search.After.IsZero() && file.CreatedAt.Before(search.After)) ||
			(!search.Before.IsZero() && !file.CreatedAt.Before(search.Before)) {
			continue
		}
		file.SharedChannels = append([]domain.ConversationID(nil), s.fileShares[file.ID]...)
		values = append(values, file)
	}
	s.mu.RUnlock()
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			if search.Direction == domain.SearchDirectionDescending {
				return values[left].ID > values[right].ID
			}
			return values[left].ID < values[right].ID
		}
		if search.Direction == domain.SearchDirectionDescending {
			return values[left].CreatedAt.After(values[right].CreatedAt)
		}
		return values[left].CreatedAt.Before(values[right].CreatedAt)
	})
	total := len(values)
	start := (search.Page - 1) * search.Count
	if start >= total {
		return domain.FilePage{Files: []domain.File{}, Total: total}, nil
	}
	end := min(start+search.Count, total)
	return domain.FilePage{Files: values[start:end], HasMore: end < total, Total: total}, nil
}

func searchHistoryKey(workspace domain.WorkspaceID, user domain.UserID, query string) string {
	return string(workspace) + "\x00" + string(user) + "\x00" + query
}

func (s *Store) RecordSearchHistory(_ context.Context, value domain.SearchHistoryEntry) error {
	value.Query = strings.TrimSpace(value.Query)
	if value.WorkspaceID == "" || value.UserID == "" || value.Query == "" || utf8.RuneCountInString(value.Query) > 500 || value.SearchedAt.IsZero() {
		return store.InvalidArgument("search history entry is invalid")
	}
	value.SearchedAt = value.SearchedAt.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searchHistory == nil {
		s.searchHistory = make(map[string]domain.SearchHistoryEntry)
	}
	s.searchHistory[searchHistoryKey(value.WorkspaceID, value.UserID, value.Query)] = value
	owned := make([]domain.SearchHistoryEntry, 0)
	for _, candidate := range s.searchHistory {
		if candidate.WorkspaceID == value.WorkspaceID && candidate.UserID == value.UserID {
			owned = append(owned, candidate)
		}
	}
	sort.Slice(owned, func(left, right int) bool {
		if owned[left].SearchedAt.Equal(owned[right].SearchedAt) {
			return owned[left].Query < owned[right].Query
		}
		return owned[left].SearchedAt.After(owned[right].SearchedAt)
	})
	if len(owned) > store.MaxSearchHistoryEntries {
		for _, stale := range owned[store.MaxSearchHistoryEntries:] {
			delete(s.searchHistory, searchHistoryKey(stale.WorkspaceID, stale.UserID, stale.Query))
		}
	}
	return nil
}

func (s *Store) ListSearchHistory(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, limit int) ([]domain.SearchHistoryEntry, error) {
	if workspace == "" || user == "" || limit <= 0 || limit > store.MaxSearchHistoryEntries {
		return nil, store.InvalidArgument("search history request is invalid")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.SearchHistoryEntry, 0, limit)
	for _, value := range s.searchHistory {
		if value.WorkspaceID == workspace && value.UserID == user {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].SearchedAt.Equal(values[right].SearchedAt) {
			return values[left].Query < values[right].Query
		}
		return values[left].SearchedAt.After(values[right].SearchedAt)
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *Store) fileVisibleToUser(file domain.File, user domain.UserID) bool {
	if file.Uploader == user {
		return true
	}
	for _, conversationID := range s.fileShares[file.ID] {
		conversation, exists := s.conversations[conversationID]
		if !exists {
			continue
		}
		if !conversation.IsPrivate {
			return true
		}
		if _, member := s.memberships[conversationID][user]; member {
			return true
		}
	}
	return false
}

func (s *Store) fileSharedIn(file domain.FileID, conversation domain.ConversationID) bool {
	for _, candidate := range s.fileShares[file] {
		if candidate == conversation {
			return true
		}
	}
	return false
}

func fileMatchesType(file domain.File, wanted string) bool {
	wanted = strings.TrimPrefix(domain.FoldSearchText(wanted), ".")
	name := domain.FoldSearchText(file.Name)
	mime := domain.FoldSearchText(file.MIMEType)
	return strings.HasSuffix(name, "."+wanted) || strings.Contains(mime, wanted)
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
	for _, draft := range s.drafts {
		if draft.WorkspaceID != workspace {
			continue
		}
		for _, attachment := range draft.Attachments {
			if upload, ok := s.externalUploads[attachment.UploadID]; ok && upload.Status == domain.ExternalUploadUploaded {
				references = append(references, upload.BlobKey)
			}
		}
	}
	for id, scheduled := range s.scheduled {
		if scheduled.WorkspaceID != workspace || s.scheduledDelivered[id] {
			continue
		}
		for _, attachment := range scheduled.FileAttachments {
			if upload, ok := s.externalUploads[attachment.UploadID]; ok && upload.Status == domain.ExternalUploadUploaded {
				references = append(references, upload.BlobKey)
			}
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

func (s *Store) DeleteMessage(_ context.Context, message domain.Message, event events.Event, unshares []store.FileUnshare) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := s.messages[message.Conversation]
	for index := range values {
		if values[index].ID != message.ID {
			continue
		}
		message.Unfurls = copyUnfurls(message.Unfurls)
		values[index] = message
		s.messages[message.Conversation] = values
		s.outbox = append(s.outbox, event)
		for _, unshare := range unshares {
			if s.fileIsCarriedElsewhereLocked(unshare.FileID, message.Conversation, message.ID) {
				continue
			}
			channels := s.fileShares[unshare.FileID]
			retained := slices.DeleteFunc(append([]domain.ConversationID(nil), channels...), func(channel domain.ConversationID) bool {
				return channel == message.Conversation
			})
			if len(retained) == len(channels) {
				continue
			}
			s.fileShares[unshare.FileID] = retained
			s.outbox = append(s.outbox, unshare.Event)
		}
		return nil
	}
	return store.ErrNotFound
}

// fileIsCarriedElsewhereLocked reports whether any live message other than the
// one being deleted still shares the file into the conversation. Sharing the
// same file twice is ordinary, so the share only ends with the last carrier.
func (s *Store) fileIsCarriedElsewhereLocked(fileID domain.FileID, conversation domain.ConversationID, excluding domain.MessageID) bool {
	for _, message := range s.messages[conversation] {
		if message.ID == excluding || message.Deleted {
			continue
		}
		for _, file := range message.Files {
			if file.ID == fileID {
				return true
			}
		}
	}
	return false
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

// cloneMessage re-reads every attached file from the file table rather than
// returning the copy that was current when the message was posted. The SQL
// profiles join message_files to files on every read, so a snapshot here
// diverges the moment a file is deleted, renamed or shared: a deleted file
// kept rendering as a live download with its original title.
func (s *Store) cloneMessage(value domain.Message) domain.Message {
	if value.Unfurls != nil {
		value.Unfurls = copyUnfurls(value.Unfurls)
	}
	files := make([]domain.File, 0, len(value.Files))
	for _, attached := range value.Files {
		current, exists := s.files[attached.ID]
		if !exists {
			current = attached
		}
		current.SharedChannels = append([]domain.ConversationID(nil), s.fileShares[attached.ID]...)
		files = append(files, current)
	}
	value.Files = files
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
		if current <= after || workspace != "" && event.WorkspaceID != workspace || store.InternalTopic(event.Topic) {
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
	// A disabled installation still admits the app.uninstalled topic: the
	// announcement of the uninstall must outlive the installation it announces.
	uninstalled := make(map[domain.WorkspaceID]struct{})
	for _, installation := range s.appInstallations {
		if installation.AppID == appID && installation.Enabled {
			workspaces[installation.WorkspaceID] = struct{}{}
		} else if installation.AppID == appID {
			uninstalled[installation.WorkspaceID] = struct{}{}
		}
	}
	result := make([]events.Record, 0, limit)
	for sequence, event := range s.outbox {
		current := uint64(sequence + 1)
		if current <= after || store.InternalTopic(event.Topic) {
			continue
		}
		if _, ok := workspaces[event.WorkspaceID]; !ok {
			if _, wasInstalled := uninstalled[event.WorkspaceID]; !wasInstalled || event.Topic != "app.uninstalled" {
				continue
			}
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
	uninstalled := make(map[domain.WorkspaceID]struct{})
	for _, installation := range s.appInstallations {
		if installation.AppID == appID && installation.Enabled {
			workspaces[installation.WorkspaceID] = struct{}{}
		} else if installation.AppID == appID {
			uninstalled[installation.WorkspaceID] = struct{}{}
		}
	}
	if len(workspaces) == 0 && len(uninstalled) == 0 {
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
			if _, wasInstalled := uninstalled[event.WorkspaceID]; !wasInstalled || event.Topic != "app.uninstalled" {
				continue
			}
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

func (s *Store) GetAppEventCursor(_ context.Context, appID domain.AppID, surface string) (domain.AppEventCursor, error) {
	if appID == "" || !validAppEventSurface(surface) {
		return domain.AppEventCursor{}, store.InvalidArgument("app ID and event surface are required")
	}
	s.mu.RLock()
	cursor, exists := s.appEventCursors[appEventCursorKey(appID, surface)]
	s.mu.RUnlock()
	if !exists {
		return domain.AppEventCursor{}, store.ErrNotFound
	}
	return domain.AppEventCursor{
		AppID: appID, Surface: surface, AcknowledgedSequence: cursor.Sequence,
		InFlightSequence: cursor.LeasedSequence, InFlightUntil: cursor.LeaseUntil,
		RetryAt: cursor.RetryAt, RetryCount: cursor.RetryCount, RetryReason: cursor.RetryReason,
	}, nil
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
			window = append(window, s.cloneMessage(values[index]))
		}
	} else {
		for index := 0; index < len(values) && len(window) <= request.Limit; index++ {
			if values[index].Deleted {
				continue
			}
			if request.Cursor != "" && !request.PageAfter(values[index].CreatedAt, values[index].ID, createdAt, id) {
				continue
			}
			window = append(window, s.cloneMessage(values[index]))
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

func (s *Store) ListAuthoredMessages(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.MessagePage, error) {
	if err := store.CheckPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	var cursorTime time.Time
	var cursorID domain.MessageID
	if request.Cursor != "" {
		var err error
		cursorTime, cursorID, err = domain.DecodeMessageCursor(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Message, 0, request.Limit+1)
	for _, messages := range s.messages {
		for _, message := range messages {
			if message.WorkspaceID != workspace || message.AuthorID != user || message.Deleted {
				continue
			}
			conversation, exists := s.conversations[message.Conversation]
			if !exists || conversation.WorkspaceID != workspace {
				continue
			}
			if conversation.IsPrivate {
				if _, member := s.memberships[message.Conversation][user]; !member {
					continue
				}
			}
			if request.Cursor != "" && !request.PageAfter(message.CreatedAt, message.ID, cursorTime, cursorID) {
				continue
			}
			less := messageBefore
			if request.Descending {
				less = func(left, right domain.Message) bool { return messageBefore(right, left) }
			}
			values = appendSorted(values, s.cloneMessage(message), request.Limit+1, less)
		}
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore}
	if hasMore {
		cursor, err := domain.NewMessageCursor(values[len(values)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

func (s *Store) SearchMessages(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, search domain.MessageSearch) (domain.MessagePage, error) {
	if err := store.CheckPage(search.Page); err != nil {
		return domain.MessagePage{}, err
	}
	if len(search.Terms) == 0 && search.Conversation == "" && search.Author == "" && search.WithUser == "" && search.After.IsZero() && search.Before.IsZero() && !search.ThreadOnly && !search.HasFiles && !search.HasPins && !search.HasReactions && !search.HasLink && search.SavedBy == "" {
		return domain.MessagePage{}, store.InvalidArgument("search query must not be empty")
	}
	startTime, startID, err := domain.DecodeMessageCursor(search.Page.Cursor)
	if err != nil {
		return domain.MessagePage{}, err
	}
	s.mu.RLock()
	values := make([]domain.Message, 0, search.Page.Limit+1)
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
		if (search.Conversation != "" && conversationID != search.Conversation) ||
			(search.ExcludedConversation != "" && conversationID == search.ExcludedConversation) {
			continue
		}
		if search.WithUser != "" {
			if _, member := s.memberships[conversationID][search.WithUser]; !member {
				continue
			}
		}
		for _, message := range messages {
			if message.Deleted {
				continue
			}
			text := domain.FoldSearchText(message.Text)
			if !searchTextMatches(text, search.Terms, search.ExcludedTerms) ||
				(search.Author != "" && message.AuthorID != search.Author) ||
				(search.ExcludedAuthor != "" && message.AuthorID == search.ExcludedAuthor) ||
				(!search.After.IsZero() && message.CreatedAt.Before(search.After)) ||
				(!search.Before.IsZero() && !message.CreatedAt.Before(search.Before)) ||
				(search.ThreadOnly && message.ThreadTimestamp == "") ||
				(search.HasFiles && len(message.Files) == 0) ||
				(search.HasPins && len(s.pins[message.ID]) == 0) ||
				(search.HasReactions && !s.messageCarriesReaction(message.ID, search.ReactionName)) ||
				(search.HasLink && !domain.TextCarriesLink(message.Text)) ||
				(search.SavedBy != "" && !s.messageSavedBy(message.ID, search.SavedBy)) {
				continue
			}
			total++
			if search.Page.Cursor != "" && !search.Page.PageAfter(message.CreatedAt, message.ID, startTime, startID) {
				continue
			}
			less := messageBefore
			if search.Page.Descending {
				less = func(left, right domain.Message) bool {
					return messageBefore(right, left)
				}
			}
			values = appendSorted(values, s.cloneMessage(message), search.Page.Limit+1, less)
		}
	}
	s.mu.RUnlock()
	hasMore := len(values) > search.Page.Limit
	if hasMore {
		values = values[:search.Page.Limit]
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

func searchTextMatches(text string, terms, excluded []string) bool {
	for _, term := range terms {
		if !strings.Contains(text, domain.FoldSearchText(term)) {
			return false
		}
	}
	for _, term := range excluded {
		if strings.Contains(text, domain.FoldSearchText(term)) {
			return false
		}
	}
	return true
}

// messageCarriesReaction answers both shapes of the question with one rule: an
// empty name asks whether the message carries any reaction, a named one asks
// whether it carries that reaction. Two predicates would be two chances to
// answer "some reaction" where "this reaction" was asked.
func (s *Store) messageCarriesReaction(id domain.MessageID, name string) bool {
	reactions := s.reactions[id]
	if name == "" {
		return len(reactions) > 0
	}
	// The map is keyed by name and reactor, because one emoji from two people
	// is two reactions. The question here is about the emoji, so the values are
	// what to look at rather than the keys.
	for _, reaction := range reactions {
		if reaction.Name == name {
			return true
		}
	}
	return false
}

func (s *Store) messageSavedBy(message domain.MessageID, user domain.UserID) bool {
	for _, item := range s.savedItems {
		if item.UserID == user && item.MessageID == message {
			return true
		}
	}
	return false
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
				values = appendSorted(values, s.cloneMessage(message), request.Limit+1, func(left, right domain.Message) bool { return threadMessageBefore(left, right, timestamp) })
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
		item := creation.Item
		if item.Version == 0 {
			item.Version = 1
		}
		created[item.ID] = item
	}
	if value.Version == 0 {
		value.Version = 1
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

func (s *Store) ListLists(_ context.Context, workspace domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ListPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ListPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ListPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.List, 0, request.Limit+1)
	for _, list := range s.lists {
		if list.WorkspaceID != workspace || (after != "" && string(list.ID) <= after) {
			continue
		}
		_, _, _, allowed := s.resolveAccessLocked(workspace, list.OwnerID, userID, func(visit func(string, string, string)) {
			for _, grant := range s.listAccess {
				if grant.ListID == list.ID {
					visit(grant.EntityType, grant.EntityID, grant.Access)
				}
			}
		})
		if allowed {
			values = append(values, list)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.ListPage{Lists: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) UpdateList(_ context.Context, value domain.List, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.lists[value.ID]
	if !exists || previous.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if value.Version != previous.Version+1 {
		return store.ErrConflict
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
	if value.Version == 0 {
		value.Version = 1
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

func (s *Store) UpdateListItem(ctx context.Context, value domain.ListItem, event events.Event) error {
	return s.UpdateListItems(ctx, []domain.ListItem{value}, []events.Event{event})
}

func (s *Store) UpdateListItems(_ context.Context, values []domain.ListItem, records []events.Event) error {
	if len(values) == 0 || len(values) != len(records) {
		return store.ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[domain.ListItemID]struct{}, len(values))
	for _, value := range values {
		list, exists := s.lists[value.ListID]
		if !exists || list.WorkspaceID != value.WorkspaceID {
			return store.ErrNotFound
		}
		previous, exists := s.listItems[value.ListID][value.ID]
		if !exists {
			return store.ErrNotFound
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return store.ErrInvalidArgument
		}
		seen[value.ID] = struct{}{}
		if value.Version != previous.Version+1 {
			return store.ErrConflict
		}
	}
	for index, value := range values {
		previous := s.listItems[value.ListID][value.ID]
		value.CreatedAt = previous.CreatedAt
		value.CreatedBy = previous.CreatedBy
		s.listItems[value.ListID][value.ID] = value
		s.outbox = append(s.outbox, records[index])
	}
	return nil
}

func (s *Store) DeleteListItem(ctx context.Context, workspace domain.WorkspaceID, listID domain.ListID, id domain.ListItemID, event events.Event) error {
	return s.DeleteListItems(ctx, workspace, listID, []domain.ListItemID{id}, event)
}

// RemoveListColumn mirrors the SQL profile: the schema and every cell under the
// removed column change together, under one lock.
func (s *Store) RemoveListColumn(_ context.Context, value domain.List, key string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.lists[value.ID]
	if !ok || existing.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if existing.Version != value.Version-1 {
		return store.ErrConflict
	}
	s.lists[value.ID] = value
	for id, item := range s.listItems[value.ID] {
		stripped, err := domain.ListFieldsWithout(item.Fields, key)
		if err != nil || stripped == item.Fields {
			// An item whose cells this build cannot read keeps them, as on SQL.
			continue
		}
		item.Fields = stripped
		// The row keeps whoever last changed it, and when, unless the event
		// says — the same rule the SQL profile follows.
		if event.ActorID != "" {
			item.UpdatedBy = event.ActorID
		}
		if !event.CreatedAt.IsZero() {
			item.UpdatedAt = event.CreatedAt.UTC()
		}
		item.Version++
		s.listItems[value.ID][id] = item
	}
	s.outbox = append(s.outbox, event)
	return nil
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
		case "channel", "channel_canvas":
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

func (s *Store) PendingUploadReferenceExists(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, upload domain.ExternalUploadID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, draft := range s.drafts {
		if draft.WorkspaceID != workspace || draft.UserID != user {
			continue
		}
		for _, attachment := range draft.Attachments {
			if attachment.UploadID == upload {
				return true, nil
			}
		}
	}
	for id, scheduled := range s.scheduled {
		if scheduled.WorkspaceID != workspace || scheduled.Author != user || s.scheduledDelivered[id] {
			continue
		}
		for _, attachment := range scheduled.FileAttachments {
			if attachment.UploadID == upload {
				return true, nil
			}
		}
	}
	return false, nil
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

func (s *Store) CompleteExternalUploads(ctx context.Context, completions []domain.ExternalUploadCompletion, files []domain.File, channels []domain.ConversationID, emitted []events.Event, messages []domain.Message, messageEvents []events.Event) error {
	return s.completeExternalUploads(ctx, "", completions, files, channels, emitted, messages, messageEvents)
}

func (s *Store) CompleteScheduledExternalUploads(ctx context.Context, id domain.ScheduledMessageID, completions []domain.ExternalUploadCompletion, files []domain.File, channels []domain.ConversationID, emitted []events.Event, message domain.Message, messageEvent events.Event) error {
	if id == "" {
		return store.InvalidArgument("scheduled external upload completion requires a schedule")
	}
	return s.completeExternalUploads(ctx, id, completions, files, channels, emitted, []domain.Message{message}, []events.Event{messageEvent})
}

func (s *Store) completeExternalUploads(_ context.Context, scheduledID domain.ScheduledMessageID, completions []domain.ExternalUploadCompletion, files []domain.File, channels []domain.ConversationID, emitted []events.Event, messages []domain.Message, messageEvents []events.Event) error {
	if len(completions) == 0 || len(completions) != len(files) || len(emitted) == 0 || len(messages) != len(messageEvents) {
		return store.ErrInvalidArgument
	}
	if scheduledID != "" && len(messages) != 1 {
		return store.InvalidArgument("scheduled external upload completion requires one message")
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
	if scheduledID != "" {
		message := messages[0]
		scheduled, exists := s.scheduled[scheduledID]
		if !exists || s.scheduledDelivered[scheduledID] ||
			scheduled.WorkspaceID != message.WorkspaceID || scheduled.Author != message.AuthorID || scheduled.Channel != message.Conversation ||
			len(scheduled.FileAttachments) != len(completions) {
			return store.ErrNotFound
		}
		for index, attachment := range scheduled.FileAttachments {
			if attachment.UploadID != completions[index].ID {
				return store.ErrNotFound
			}
		}
	}
	seenUploads := make(map[domain.ExternalUploadID]struct{}, len(completions))
	seenFiles := make(map[domain.FileID]struct{}, len(files))
	previousUploads := make(map[domain.ExternalUploadID]domain.ExternalUpload, len(completions))
	for index, completion := range completions {
		value, exists := s.externalUploads[completion.ID]
		if !exists {
			return store.ErrNotFound
		}
		pendingOwned := false
		for _, draft := range s.drafts {
			if draft.WorkspaceID != value.WorkspaceID || draft.UserID != value.Uploader {
				continue
			}
			for _, attachment := range draft.Attachments {
				if attachment.UploadID == completion.ID {
					pendingOwned = true
					break
				}
			}
			if pendingOwned {
				break
			}
		}
		if !pendingOwned {
			for id, scheduled := range s.scheduled {
				if scheduled.WorkspaceID != value.WorkspaceID || scheduled.Author != value.Uploader || s.scheduledDelivered[id] {
					continue
				}
				for _, attachment := range scheduled.FileAttachments {
					if attachment.UploadID == completion.ID {
						pendingOwned = true
						break
					}
				}
				if pendingOwned {
					break
				}
			}
		}
		if value.Status != domain.ExternalUploadUploaded || (!value.ExpiresAt.After(time.Now().UTC()) && !pendingOwned) {
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
	}
	s.outbox = append(s.outbox, emitted...)
	rollback := func() {
		for uploadID, value := range previousUploads {
			s.externalUploads[uploadID] = value
		}
		for _, file := range files {
			delete(s.files, file.ID)
			delete(s.fileShares, file.ID)
		}
		s.outbox = s.outbox[:outboxLength]
	}
	prepared := make([]domain.Message, len(messages))
	seenMessages := make(map[domain.MessageID]struct{}, len(messages))
	seenTimestamps := make(map[string]struct{}, len(messages))
	for index, message := range messages {
		key := ""
		if index == 0 {
			key = string(scheduledID)
		}
		if _, duplicate := seenMessages[message.ID]; duplicate {
			rollback()
			return store.ErrAlreadyExists
		}
		timestampKey := string(message.Conversation) + "\x00" + string(domain.NewStoredTime(message.CreatedAt))
		if _, duplicate := seenTimestamps[timestampKey]; duplicate {
			rollback()
			return store.ErrMessageTimestampTaken
		}
		value, err := s.prepareMessageLocked(message, key)
		if err != nil {
			rollback()
			return err
		}
		prepared[index] = value
		seenMessages[message.ID] = struct{}{}
		seenTimestamps[timestampKey] = struct{}{}
	}
	for index, message := range prepared {
		key := ""
		if index == 0 {
			key = string(scheduledID)
		}
		s.commitMessageLocked(message, messageEvents[index], key)
	}
	return nil
}
