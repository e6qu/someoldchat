package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/slackemoji"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

var (
	ErrInvalidMessage        = errors.New("message text and conversation are required")
	ErrInvalidTimestamp      = errors.New("message timestamp is invalid")
	ErrMessageNotOwned       = errors.New("message is not owned by user")
	ErrMessageAlreadyDeleted = errors.New("message is already deleted")
	// The message describes the class rather than one member of it. It used to
	// read "conversation name is invalid", which is right for a rejected name
	// and wrong for the other forty-odd sites that raise it — a foreign team
	// id, a kind that cannot carry the operation, a conversation that is not a
	// channel — each of which told the caller to go and look at a name.
	ErrInvalidConversation = errors.New("conversation is invalid for this operation")
	// ErrBarrieredFromMember is refusal by an information barrier. It is its
	// own error because "you may not reach this person" is a different fact
	// from a malformed request or a member who is not here, and an
	// administrator reading a support question needs to tell them apart.
	ErrBarrieredFromMember      = errors.New("an information barrier separates these members")
	ErrInvalidWorkspace         = errors.New("workspace settings are invalid")
	ErrInvalidConversationPrefs = errors.New("conversation preferences are invalid")
	ErrInvalidReaction          = errors.New("reaction name is invalid")
	ErrBlobUnavailable          = errors.New("blob storage is unavailable")
	ErrInvalidFile              = errors.New("file metadata is invalid")
	ErrInvalidSearch            = errors.New("search query is invalid")
	ErrInvalidProfile           = errors.New("user profile is invalid")
	ErrInvalidScheduledStatus   = errors.New("scheduled status is invalid")
	ErrScheduledStatusLimit     = errors.New("five statuses are already scheduled")
	ErrInvalidPresence          = errors.New("user presence is invalid")
	ErrInvalidSnooze            = errors.New("snooze duration must be between 1 and 1440 minutes")
	ErrInvalidReminder          = errors.New("reminder text, user, and time are required")
	ErrInvalidLaterReminder     = errors.New("Later reminder arguments are invalid")
	ErrReminderTimeInPast       = errors.New("reminder time is in the past")
	ErrScheduledTimeInPast      = errors.New("scheduled message time is in the past")
	ErrScheduledTimeTooFar      = errors.New("scheduled message time is more than 120 days away")
	ErrScheduledTooMany         = errors.New("too many messages are scheduled in the channel window")
	ErrInvalidUserGroup         = errors.New("user group name, handle, and members are invalid")
	ErrInvalidCall              = errors.New("call external id and join URL are required")
	ErrInvalidEphemeral         = errors.New("ephemeral message recipient, conversation, and text are required")
	ErrInvalidAccessLog         = errors.New("access log fields are invalid")
	ErrInvalidEmoji             = errors.New("custom emoji name or URL is invalid")
	ErrEmojiAlreadyExists       = errors.New("custom emoji already exists")
	ErrInvalidRemoteFile        = errors.New("remote file metadata is invalid")
	ErrInvalidInviteRequest     = errors.New("invite request is invalid")
	// ErrInvitationExpired is distinct from ErrInvalidInviteRequest because the
	// person reading it needs to know whether to ask for a new invitation or
	// to check which address they signed in with.
	ErrInvitationExpired = errors.New("invitation has expired")
	// ErrHuddleNotOwned refuses to end a huddle on everyone else's behalf.
	ErrHuddleNotOwned              = errors.New("huddle is not owned by this actor")
	ErrInvalidAppApproval          = errors.New("app approval is invalid")
	ErrInvalidView                 = errors.New("view payload is invalid")
	ErrAppHomeNotEnabled           = errors.New("app home tab is not enabled")
	ErrInvalidList                 = errors.New("list payload is invalid")
	ErrInvalidEntity               = errors.New("entity payload is invalid")
	ErrInvalidWorkflowStep         = errors.New("workflow step payload is invalid")
	ErrWorkflowPermissionDenied    = errors.New("workflow trigger is not available to this actor")
	ErrFunctionAccessDenied        = errors.New("actor does not have access to this function execution")
	ErrFunctionNotRunning          = errors.New("function execution is not running")
	ErrAutomationUserNotFound      = errors.New("automation permission user was not found")
	ErrAutomationChannelNotFound   = errors.New("automation permission channel was not found")
	ErrAutomationTeamNotFound      = errors.New("automation permission workspace was not found")
	ErrAutomationOrgNotFound       = errors.New("automation permission organization was not found")
	ErrAutomationEntitiesEmpty     = errors.New("automation named entities cannot be empty")
	ErrWorkflowFunctionNotFound    = errors.New("workflow function was not found")
	ErrInvalidDialog               = errors.New("dialog payload is invalid")
	ErrInvalidBot                  = errors.New("bot identifier is required")
	ErrInvalidMigration            = errors.New("migration user identifiers are invalid")
	ErrInvalidOAuth                = errors.New("oauth authorization is invalid")
	ErrInvalidOAuthClient          = errors.New("oauth client is invalid")
	ErrOAuthAppMismatch            = errors.New("oauth client and token app do not match")
	ErrInvalidIntegrationLogs      = errors.New("integration log arguments are invalid")
	ErrInvalidBookmark             = errors.New("bookmark title, type, and link are invalid")
	ErrInvalidCanvas               = errors.New("canvas content or access arguments are invalid")
	ErrInvalidExternalUpload       = errors.New("external upload is invalid")
	ErrConversationAlreadyArchived = errors.New("conversation is already archived")
	ErrConversationNotArchived     = errors.New("conversation is not archived")
	ErrCannotArchiveDefault        = errors.New("required conversation cannot be archived")
	ErrCannotLeaveDefault          = errors.New("required conversation cannot be left")
	ErrCannotInviteSelf            = errors.New("a conversation member cannot invite themselves")

	// ErrNotWorkspaceAdmin refuses an administrative operation to an actor whose
	// durable workspace membership is not an administrator or an owner.
	//
	// It is distinct from store.ErrNotFound, which every administrative method
	// already returned for an actor who is not a member of the workspace at all.
	// Collapsing the two would tell an authenticated member that the workspace
	// does not exist, and would hide the real reason from the operator reading the
	// audit trail.
	ErrNotWorkspaceAdmin = errors.New("actor is not a workspace administrator")
	// ErrUserIsRestricted and ErrUserIsUltraRestricted are the two guest tiers
	// refusing an action Slack keeps away from guests. They are separate
	// sentinels because the pinned enums for conversations.join and
	// conversations.invite declare both codes: a caller told
	// user_is_ultra_restricted knows the person is confined to one channel,
	// which user_is_restricted does not say.
	ErrUserIsRestricted      = errors.New("actor is a guest")
	ErrUserIsUltraRestricted = errors.New("actor is a single-channel guest")

	// ErrNotInConversation refuses an operation whose Slack contract requires the
	// actor to be a member of the conversation it names.
	//
	// authorizeConversation checks membership only for a PRIVATE conversation,
	// because a public channel is readable by every member of the workspace. That
	// is right for a read and wrong for the ten operations whose pinned enums
	// declare `not_in_channel` — chat.postMessage, chat.meMessage,
	// chat.scheduleMessage, conversations.invite, conversations.kick,
	// conversations.leave, conversations.mark, conversations.rename,
	// conversations.setPurpose and conversations.setTopic — all of which act on
	// the channel as one of its members. Before this, a member could post into,
	// rename or set the topic of a public channel they had never joined, and
	// `not_in_channel` was declared by nine enums and produced nowhere in the
	// repository.
	//
	// It is distinct from store.ErrNotFound, which stays the answer for a channel
	// the actor cannot see at all: naming a public channel proves nothing about
	// the actor, so the refusal may say what it is.
	ErrNotInConversation = errors.New("actor is not a member of the conversation")

	// ErrLastWorkspaceOwner refuses a change that would leave a workspace with no
	// owner. Ownership is the authority that appoints administrators, so a
	// workspace that loses its last owner cannot appoint another and is
	// permanently unadministrable.
	ErrLastWorkspaceOwner = errors.New("workspace must retain an owner")
)

const (
	// MaxMessageTextRunes is the single text ceiling for every way a message can
	// enter or change in the product. It is measured in Unicode code points, as
	// Slack's documented 40,000-character contract is; byte length would reject
	// non-ASCII text earlier than an equally long ASCII message.
	MaxMessageTextRunes = 40000

	// MaxMessageBodyBytes bounds the combined normalized Block Kit and legacy
	// attachment document. The Slack HTTP adapter enforced this first, but the
	// shared service did not, so gRPC and incoming-webhook callers could persist
	// a structured message large enough to amplify every later history read.
	MaxMessageBodyBytes = 256 << 10
)

type Messages struct {
	Store            store.Store
	Blob             blob.Store
	AppCredentialKey []byte
	AppHTTPClient    *http.Client
}

type conversationInviteFailureReason string

const (
	conversationInviteUserNotFound     conversationInviteFailureReason = "user_not_found"
	conversationInviteSelf             conversationInviteFailureReason = "cant_invite_self"
	conversationInviteAlreadyInChannel conversationInviteFailureReason = "already_in_channel"
)

type conversationInviteFailure struct {
	UserID domain.UserID
	Reason conversationInviteFailureReason
}

type conversationInviteResult struct {
	Conversation domain.Conversation
	Failures     []conversationInviteFailure
	InvitedCount int
}

var _ chatapi.Service = Messages{}

func (m Messages) LookupToken(ctx context.Context, token string) (domain.TokenRecord, error) {
	return m.Store.LookupToken(ctx, token)
}

func (m Messages) LookupAppToken(ctx context.Context, token string) (domain.AppTokenRecord, error) {
	return m.Store.LookupAppToken(ctx, token)
}

func (m Messages) CreateAppInstallation(ctx context.Context, value domain.AppInstallation) error {
	return m.Store.CreateAppInstallation(ctx, value)
}

func (m Messages) ListAppInstallations(ctx context.Context, appID domain.AppID) ([]domain.AppInstallation, error) {
	return m.Store.ListAppInstallations(ctx, appID)
}

func (m Messages) ListAppAuthorizations(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID) ([]domain.AppAuthorization, error) {
	return m.Store.ListAppAuthorizations(ctx, appID, workspaceID)
}

func (m Messages) UninstallApp(ctx context.Context, clientID, clientSecret string, workspaceID domain.WorkspaceID, appID domain.AppID) error {
	client, err := m.Store.GetOAuthClient(ctx, strings.TrimSpace(clientID))
	if err != nil || !secretDigestsEqual(client.SecretHash, domain.HashToken(strings.TrimSpace(clientSecret))) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return ErrInvalidOAuthClient
	}
	if client.AppID != appID {
		return ErrOAuthAppMismatch
	}
	// The announcement commits with the uninstall. It is target-routed and
	// automatic, and the storage reads carve its topic out of the
	// enabled-installation scope so the app can still receive the one event
	// that explains why everything else stopped.
	announcement, err := newEvent(workspaceID, "", events.NewPayload("app.uninstalled",
		events.String("app_id", string(appID)),
		events.String("target_app_id", string(appID)),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.UninstallApp(ctx, workspaceID, appID, announcement)
}

func (m Messages) ListAppEventsAfter(ctx context.Context, appID domain.AppID, after uint64, limit int) ([]events.Record, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("event limit must be positive")
	}
	result := make([]events.Record, 0, limit)
	cursor := after
	for len(result) < limit {
		records, err := m.Store.ListAppEventsAfter(ctx, appID, cursor, limit)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return result, nil
		}
		for _, record := range records {
			cursor = record.Sequence
			prepared, visible, prepareErr := PrepareAppEvent(ctx, m.Store, m.AppCredentialKey, appID, record)
			if prepareErr != nil {
				return nil, prepareErr
			}
			if visible {
				result = append(result, prepared)
				if len(result) == limit {
					return result, nil
				}
			}
		}
		if len(records) < limit {
			return result, nil
		}
	}
	return result, nil
}

func (m Messages) ListUserEventsAfter(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, after uint64, limit int) ([]events.Record, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("event limit must be positive")
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	result := make([]events.Record, 0, limit)
	cursor := after
	for len(result) < limit {
		records, err := m.Store.ListEventsAfter(ctx, workspaceID, cursor, limit)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return result, nil
		}
		for _, record := range records {
			cursor = record.Sequence
			prepared, visible, prepareErr := PrepareUserEvent(ctx, m.Store, workspaceID, userID, record)
			if prepareErr != nil {
				return nil, prepareErr
			}
			if visible {
				result = append(result, prepared)
				if len(result) == limit {
					return result, nil
				}
			}
		}
		if len(records) < limit {
			return result, nil
		}
	}
	return result, nil
}

func (m Messages) ClaimAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, lease time.Duration) (events.Record, int, string, bool, error) {
	for {
		record, attempt, reason, found, err := m.Store.ClaimAppEvent(ctx, appID, surface, owner, lease)
		if err != nil || !found || surface != "socket" {
			return record, attempt, reason, found, err
		}
		prepared, visible, err := PrepareAppEvent(ctx, m.Store, m.AppCredentialKey, appID, record)
		if err != nil {
			_ = m.Store.ReleaseAppEvent(ctx, appID, surface, owner, record.Sequence, "event_projection_failed", time.Now().UTC())
			return events.Record{}, 0, "", false, err
		}
		if !visible {
			if err := m.Store.AckAppEvent(ctx, appID, surface, owner, record.Sequence); err != nil {
				return events.Record{}, 0, "", false, err
			}
			continue
		}
		record = prepared
		// The uninstall announcement needs no manifest: it is automatic (no
		// subscription can be consulted — the installation is gone) and
		// SlackEventBodies already applies its target routing. Consulting
		// installedApp here would fail on the disabled installation and park
		// the record in retry forever.
		if record.Event.Topic == "app.uninstalled" {
			bodies, err := events.SlackEventBodies(record, string(appID))
			if err != nil || len(bodies) == 0 {
				if err := m.Store.AckAppEvent(ctx, appID, surface, owner, record.Sequence); err != nil {
					return events.Record{}, 0, "", false, err
				}
				continue
			}
			return record, attempt, reason, true, nil
		}
		snapshot, parsed, err := m.installedApp(ctx, record.Event.WorkspaceID, appID)
		if err != nil {
			_ = m.Store.ReleaseAppEvent(ctx, appID, surface, owner, record.Sequence, "app_configuration_unavailable", time.Now().UTC())
			return events.Record{}, 0, "", false, err
		}
		bodies, err := events.SlackEventBodies(record, string(snapshot.App.ID))
		if err != nil {
			// The Socket Mode handler owns the established malformed-record
			// policy and its operator diagnostics.
			return record, attempt, reason, true, nil
		}
		filtered, err := events.FilterSubscribedSlackEventBodies(ctx, bodies, parsed.BotEvents, parsed.UserEvents, m.Store.GetConversation)
		if err != nil {
			_ = m.Store.ReleaseAppEvent(ctx, appID, surface, owner, record.Sequence, "subscription_filter_failed", time.Now().UTC())
			return events.Record{}, 0, "", false, err
		}
		if len(filtered) != 0 {
			record.Event.Authorizations, err = events.SlackEventBodyAuthorizations(filtered[0])
			if err != nil {
				_ = m.Store.ReleaseAppEvent(ctx, appID, surface, owner, record.Sequence, "authorization_filter_failed", time.Now().UTC())
				return events.Record{}, 0, "", false, err
			}
			return record, attempt, reason, true, nil
		}
		if err := m.Store.AckAppEvent(ctx, appID, surface, owner, record.Sequence); err != nil {
			return events.Record{}, 0, "", false, err
		}
	}
}

func (m Messages) AckAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64) error {
	return m.Store.AckAppEvent(ctx, appID, surface, owner, sequence)
}

func (m Messages) ReleaseAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64, reason string, retryAt time.Time) error {
	return m.Store.ReleaseAppEvent(ctx, appID, surface, owner, sequence, reason, retryAt)
}

func (m Messages) GetSocketModeCursor(ctx context.Context, appID domain.AppID) (uint64, error) {
	return m.Store.GetSocketModeCursor(ctx, appID)
}

func (m Messages) SetSocketModeCursor(ctx context.Context, appID domain.AppID, cursor uint64) error {
	return m.Store.SetSocketModeCursor(ctx, appID, cursor)
}

func (m Messages) CreateSocketModeConnection(ctx context.Context, value domain.SocketModeConnection) error {
	return m.Store.CreateSocketModeConnection(ctx, value)
}

func (m Messages) ConsumeSocketModeConnection(ctx context.Context, id string) (domain.SocketModeConnection, error) {
	return m.Store.ConsumeSocketModeConnection(ctx, id)
}

func (m Messages) RenewSocketModeConnection(ctx context.Context, id string, expiresAt time.Time) error {
	return m.Store.RenewSocketModeConnection(ctx, id, expiresAt)
}

func (m Messages) ReleaseSocketModeConnection(ctx context.Context, id string) error {
	return m.Store.ReleaseSocketModeConnection(ctx, id)
}

func (m Messages) CountSocketModeConnections(ctx context.Context, appID domain.AppID) (int, error) {
	return m.Store.CountSocketModeConnections(ctx, appID)
}

func (m Messages) RecordSocketModeResponse(ctx context.Context, value domain.SocketModeResponse) error {
	return m.Store.RecordSocketModeResponse(ctx, value)
}

func (m Messages) ClaimSocketModeResponses(ctx context.Context, appID domain.AppID, owner string, limit int, lease time.Duration) ([]domain.SocketModeResponse, error) {
	return m.Store.ClaimSocketModeResponses(ctx, appID, owner, limit, lease)
}

func (m Messages) RenewSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, lease time.Duration) error {
	return m.Store.RenewSocketModeResponses(ctx, owner, values, lease)
}

func (m Messages) AckSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse) error {
	return m.Store.AckSocketModeResponses(ctx, owner, values)
}

func (m Messages) ReleaseSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, retryAt time.Time) error {
	return m.Store.ReleaseSocketModeResponses(ctx, owner, values, retryAt)
}

func (m Messages) ClaimSocketModeInteraction(ctx context.Context, appID domain.AppID, owner string, lease time.Duration) (domain.SocketModeInteraction, bool, error) {
	return m.Store.ClaimSocketModeInteraction(ctx, appID, owner, lease)
}

func (m Messages) AckSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID, owner string) error {
	return m.Store.AckSocketModeInteraction(ctx, appID, envelopeID, owner)
}

func (m Messages) ReleaseSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID, owner, reason string, retryAt time.Time) error {
	return m.Store.ReleaseSocketModeInteraction(ctx, appID, envelopeID, owner, reason, retryAt)
}

func (m Messages) RevokeToken(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return store.ErrNotFound
	}
	return m.Store.RevokeToken(ctx, token)
}

func (m Messages) RevokeSession(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return store.ErrNotFound
	}
	return m.Store.RevokeSession(ctx, token)
}

func (m Messages) ResetUserSessions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return store.ErrNotFound
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.sessions_reset", events.String("user_id", string(targetID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.RevokeUserSessions(ctx, workspaceID, targetID, event)
}

// UserSessions reports one member's live sessions to an administrator. It never
// carries a session token: reviewing who is signed in and ending a session both
// work from the stored hash, and answering with the credential would make the
// review itself a way to impersonate the member.
func (m Messages) UserSessions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) ([]domain.WorkspaceSession, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return nil, store.ErrNotFound
	}
	return m.Store.ListUserSessions(ctx, workspaceID, targetID)
}

// ResetUserSessionsBulk signs several members out at once.
//
// It is not the single reset in a loop with the loop hidden: a member named in
// the request who is not a member of this workspace stops the whole thing
// before anything is revoked, so an administrator acting on a list they pasted
// finds out they were wrong instead of signing out an arbitrary prefix of it.
func (m Messages) ResetUserSessionsBulk(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targets []domain.UserID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(targets) == 0 {
		return store.InvalidArgument("at least one member is required")
	}
	for _, targetID := range targets {
		target, err := m.Store.GetUser(ctx, targetID)
		if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
			return store.ErrNotFound
		}
	}
	for _, targetID := range targets {
		event, err := newEvent(workspaceID, actorID, events.NewPayload("user.sessions_reset", events.String("user_id", string(targetID))), time.Now().UTC())
		if err != nil {
			return err
		}
		// A member with no live session is not a failure of the request: they
		// are already signed out, which is what was asked for. That rule lives
		// in the store now, so this reads like every other write.
		if err := m.Store.RevokeUserSessions(ctx, workspaceID, targetID, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) LookupSession(ctx context.Context, token string) (domain.SessionRecord, error) {
	return m.Store.LookupSession(ctx, token)
}

func (m Messages) CreateSession(ctx context.Context, token string, record domain.SessionRecord) error {
	return m.Store.CreateSession(ctx, token, record)
}

func (m Messages) GetAuthMethod(ctx context.Context, workspaceID domain.WorkspaceID, provider string) (domain.AuthMethod, error) {
	return m.Store.GetAuthMethod(ctx, workspaceID, strings.ToLower(strings.TrimSpace(provider)))
}

func (m Messages) SetAuthMethod(ctx context.Context, method domain.AuthMethod) error {
	method.Provider = strings.ToLower(strings.TrimSpace(method.Provider))
	if method.WorkspaceID == "" || method.Provider == "" {
		// A bare errors.New here carried no domain class, so the chat gRPC
		// boundary could only answer codes.Unavailable with fixed text: a caller
		// mistake became "retry, the dependency is down" in the split deployment
		// and an HTTP 503 to the operator. Which sign-in providers a workspace
		// accepts is a workspace setting, which is what ErrInvalidWorkspace names.
		return ErrInvalidWorkspace
	}
	return m.Store.SetAuthMethod(ctx, method)
}

func (m Messages) GetExternalIdentity(ctx context.Context, workspaceID domain.WorkspaceID, provider, subject string) (domain.ExternalIdentity, error) {
	return m.Store.GetExternalIdentity(ctx, workspaceID, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(subject))
}

func (m Messages) CreateExternalIdentity(ctx context.Context, identity domain.ExternalIdentity) error {
	identity.Provider = strings.ToLower(strings.TrimSpace(identity.Provider))
	identity.Subject = strings.TrimSpace(identity.Subject)
	if identity.WorkspaceID == "" || identity.Provider == "" || identity.Subject == "" || identity.UserID == "" {
		// store.ErrInvalidArgument for the same reason as SetAuthMethod above: an
		// unclassified error degrades to codes.Unavailable across the transport.
		return store.ErrInvalidArgument
	}
	return m.Store.CreateExternalIdentity(ctx, identity)
}

func (m Messages) RevokeOIDCSessions(ctx context.Context, workspaceID domain.WorkspaceID, provider, subject, sid, tokenID string, expiresAt time.Time) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	subject = strings.TrimSpace(subject)
	sid = strings.TrimSpace(sid)
	tokenID = strings.TrimSpace(tokenID)
	if workspaceID == "" || provider == "" || (subject == "" && sid == "") || tokenID == "" || !expiresAt.After(time.Now().UTC()) {
		return store.ErrInvalidArgument
	}
	event, err := newEvent(workspaceID, "", events.NewPayload("user.sessions_revoked_by_oidc", events.String("provider", provider)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.RevokeOIDCSessions(ctx, workspaceID, provider, subject, sid, tokenID, expiresAt, event)
}

func (m Messages) UploadFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, title, mimeType string, size int64, source io.Reader) (domain.File, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.File{}, err
	}
	if m.Blob == nil {
		return domain.File{}, ErrBlobUnavailable
	}
	name = strings.TrimSpace(name)
	title = strings.TrimSpace(title)
	mimeType = strings.TrimSpace(mimeType)
	if name == "" || title == "" || mimeType == "" || size < 0 || source == nil {
		return domain.File{}, ErrInvalidFile
	}
	id, err := domain.NewFileID()
	if err != nil {
		return domain.File{}, err
	}
	// The recorded type is published to every API client as mimetype and
	// filetype, and it decides what a viewer does with the bytes. It may not be
	// a client assertion the bytes contradict.
	mimeType, source, err = resolveUploadContentType(mimeType, source)
	if err != nil {
		return domain.File{}, err
	}
	file := domain.File{ID: id, WorkspaceID: workspaceID, Uploader: userID, Name: name, Title: title, MIMEType: mimeType, BlobKey: string(workspaceID) + "/" + string(id), Size: size, CreatedAt: time.Now().UTC()}
	if _, err := m.Blob.Put(ctx, file.BlobKey, size, source); err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return domain.File{}, ErrBlobUnavailable
		}
		return domain.File{}, err
	}
	event, err := fileEventAt(workspaceID, userID, "file.created", file, "", file.CreatedAt)
	if err != nil {
		cleanupErr := m.Blob.Delete(context.Background(), file.BlobKey)
		if cleanupErr != nil {
			return domain.File{}, errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		return domain.File{}, err
	}
	if err := m.Store.CreateFile(ctx, file, event); err != nil {
		cleanupErr := m.Blob.Delete(context.Background(), file.BlobKey)
		if cleanupErr != nil {
			return domain.File{}, fmt.Errorf("create file: %w; blob cleanup: %v", err, cleanupErr)
		}
		return domain.File{}, err
	}
	return file, nil
}

func (m Messages) FileInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.File{}, err
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID {
		return domain.File{}, store.ErrNotFound
	}
	if err := m.authorizeFileAccess(ctx, userID, file); err != nil {
		return domain.File{}, err
	}
	return file, nil
}

func (m Messages) OpenFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, io.ReadCloser, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.File{}, nil, err
	}
	if m.Blob == nil {
		return domain.File{}, nil, ErrBlobUnavailable
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID {
		return domain.File{}, nil, store.ErrNotFound
	}
	if err := m.authorizeFileAccess(ctx, userID, file); err != nil {
		return domain.File{}, nil, err
	}
	object, reader, err := m.Blob.Open(ctx, file.BlobKey)
	if err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return domain.File{}, nil, ErrBlobUnavailable
		}
		return domain.File{}, nil, err
	}
	if object.Size != file.Size {
		closeErr := reader.Close()
		return domain.File{}, nil, errors.Join(errors.New("blob size does not match file metadata"), closeErr)
	}
	return file, reader, nil
}

func (m Messages) authorizeFileAccess(ctx context.Context, userID domain.UserID, file domain.File) error {
	if file.Uploader == userID {
		return nil
	}
	for _, conversationID := range file.SharedChannels {
		conversation, err := m.Store.GetConversation(ctx, conversationID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		if conversation.WorkspaceID != file.WorkspaceID {
			continue
		}
		if !conversation.PrivateFlag() {
			return nil
		}
		member, err := m.Store.IsConversationMember(ctx, conversationID, userID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if member {
			return nil
		}
	}
	return store.ErrNotFound
}

func (m Messages) DeleteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID || file.Uploader != userID {
		return store.ErrNotFound
	}
	event, err := newEvent(workspaceID, userID, events.BlobKey(events.FileBlobDeleteTopic, file.BlobKey), time.Now().UTC())
	if err != nil {
		return err
	}
	if err := m.Store.DeleteFile(ctx, fileID, event); err != nil {
		return err
	}
	return nil
}

// FileDescriptionLimit bounds a description. It is generous enough for the
// sentence or two a useful alt text is and short enough that the field cannot
// become a second message body, which is the failure mode of an unbounded
// description: readers start putting content there that only some of them see.
const FileDescriptionLimit = 1000

// SetFileDescription records what an image is, in words. Only the uploader may
// write it, matching deletion: the description is presented as the uploader's
// account of their own file, and a description anyone could rewrite would be a
// caption instead.
//
// An empty description is accepted, because removing one is the only way to
// correct a description that was wrong.
func (m Messages) SetFileDescription(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID, description string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	description = strings.TrimSpace(description)
	if fileID == "" || utf8.RuneCountInString(description) > FileDescriptionLimit {
		return ErrInvalidFile
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("file.description_changed",
		events.String("file_id", string(fileID)),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetFileDescription(ctx, workspaceID, fileID, userID, description, event)
}

func (m Messages) DeleteFileComment(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID, commentID domain.FileCommentID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if fileID == "" || commentID == "" {
		return ErrInvalidFile
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("file.comment_deleted", events.String("file_id", string(fileID)), events.String("comment_id", string(commentID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteFileComment(ctx, workspaceID, fileID, commentID, event)
}

func (m Messages) ShareFilePublic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.File{}, err
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID {
		return domain.File{}, store.ErrNotFound
	}
	if file.PublicToken == "" {
		token, err := domain.PublicID("pub_")
		if err != nil {
			return domain.File{}, err
		}
		event, err := newEvent(workspaceID, userID, events.NewPayload("file.public_shared", events.String("file_id", string(fileID)), events.String("user_id", string(userID))), time.Now().UTC())
		if err != nil {
			return domain.File{}, err
		}
		if err := m.Store.ShareFilePublic(ctx, workspaceID, fileID, token, event); err != nil {
			return domain.File{}, err
		}
		file.PublicToken = token
	}
	return file, nil
}

func (m Messages) RevokeFilePublic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) (domain.File, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.File{}, err
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID {
		return domain.File{}, store.ErrNotFound
	}
	if file.PublicToken != "" {
		event, err := newEvent(workspaceID, userID, events.NewPayload("file.public_revoked", events.String("file_id", string(fileID))), time.Now().UTC())
		if err != nil {
			return domain.File{}, err
		}
		if err := m.Store.RevokeFilePublic(ctx, workspaceID, fileID, event); err != nil {
			return domain.File{}, err
		}
		file.PublicToken = ""
	}
	return file, nil
}

func (m Messages) OpenPublicFile(ctx context.Context, token string) (domain.File, io.ReadCloser, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.File{}, nil, store.ErrNotFound
	}
	if m.Blob == nil {
		return domain.File{}, nil, ErrBlobUnavailable
	}
	file, err := m.Store.GetPublicFile(ctx, token)
	if err != nil {
		return domain.File{}, nil, err
	}
	_, reader, err := m.Blob.Open(ctx, file.BlobKey)
	if err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return domain.File{}, nil, ErrBlobUnavailable
		}
		return domain.File{}, nil, err
	}
	return file, reader, nil
}

func (m Messages) Files(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.FilePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.FilePage{}, err
	}
	return m.Store.ListVisibleFiles(ctx, workspaceID, userID, request)
}

func (m Messages) AddRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, value domain.RemoteFile) (domain.RemoteFile, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.RemoteFile{}, err
	}
	value.WorkspaceID = workspaceID
	value.ExternalID = strings.TrimSpace(value.ExternalID)
	value.Title = strings.Join(strings.Fields(strings.TrimSpace(value.Title)), " ")
	value.FileType = strings.TrimSpace(value.FileType)
	value.ExternalURL = strings.TrimSpace(value.ExternalURL)
	value.PreviewImage = strings.TrimSpace(value.PreviewImage)
	if value.ExternalID == "" || value.Title == "" || len(value.ExternalID) > 255 || len(value.Title) > 255 || len(value.FileType) > 100 || len(value.ExternalURL) > 2048 || len(value.PreviewImage) > 2048 || len(value.IndexableContents) > 1<<20 {
		return domain.RemoteFile{}, ErrInvalidRemoteFile
	}
	parsed, err := url.ParseRequestURI(value.ExternalURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return domain.RemoteFile{}, ErrInvalidRemoteFile
	}
	if value.PreviewImage != "" {
		preview, previewErr := url.ParseRequestURI(value.PreviewImage)
		if previewErr != nil || (preview.Scheme != "http" && preview.Scheme != "https") || preview.Host == "" {
			return domain.RemoteFile{}, ErrInvalidRemoteFile
		}
	}
	value.ID, err = domain.NewFileID()
	if err != nil {
		return domain.RemoteFile{}, err
	}
	value.CreatedAt = time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload("remote_file.created", events.String("file_id", string(value.ID)), events.String("external_id", value.ExternalID)), value.CreatedAt)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	if err := m.Store.AddRemoteFile(ctx, value, event); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return domain.RemoteFile{}, store.ErrAlreadyExists
		}
		return domain.RemoteFile{}, err
	}
	return value, nil
}

func (m Messages) RemoteFileInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.RemoteFileLookup) (domain.RemoteFile, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.RemoteFile{}, err
	}
	lookup.ID = domain.FileID(strings.TrimSpace(string(lookup.ID)))
	lookup.ExternalID = strings.TrimSpace(lookup.ExternalID)
	if !lookup.Valid() {
		return domain.RemoteFile{}, ErrInvalidRemoteFile
	}
	return m.Store.GetRemoteFile(ctx, workspaceID, lookup)
}

func (m Messages) RemoteFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.RemoteFilePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.RemoteFilePage{}, err
	}
	return m.Store.ListRemoteFiles(ctx, workspaceID, request)
}

func (m Messages) RemoveRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.RemoteFileLookup) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	lookup.ID = domain.FileID(strings.TrimSpace(string(lookup.ID)))
	lookup.ExternalID = strings.TrimSpace(lookup.ExternalID)
	if !lookup.Valid() {
		return ErrInvalidRemoteFile
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("remote_file.removed", events.String("file_id", string(lookup.ID)), events.String("external_id", lookup.ExternalID)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.RemoveRemoteFile(ctx, workspaceID, lookup, event)
}

func (m Messages) ShareRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, lookup domain.RemoteFileLookup, channels []domain.ConversationID) (domain.RemoteFile, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.RemoteFile{}, err
	}
	lookup.ID = domain.FileID(strings.TrimSpace(string(lookup.ID)))
	lookup.ExternalID = strings.TrimSpace(lookup.ExternalID)
	if !lookup.Valid() {
		return domain.RemoteFile{}, ErrInvalidRemoteFile
	}
	channels, err := normalizeRemoteFileChannels(channels)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("remote_file.shared", events.String("file_id", string(lookup.ID)), events.String("external_id", lookup.ExternalID), events.Strings("channel_ids", conversationIDStrings(channels))), time.Now().UTC())
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return m.Store.SetRemoteFileShares(ctx, workspaceID, lookup, channels, event)
}

func (m Messages) UpdateRemoteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, update domain.RemoteFileUpdate) (domain.RemoteFile, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.RemoteFile{}, err
	}
	update.Lookup.ID = domain.FileID(strings.TrimSpace(string(update.Lookup.ID)))
	update.Lookup.ExternalID = strings.TrimSpace(update.Lookup.ExternalID)
	if !update.Lookup.Valid() || !(update.SetTitle || update.SetFileType || update.SetExternalURL || update.SetPreviewImage || update.SetIndexableData) {
		return domain.RemoteFile{}, ErrInvalidRemoteFile
	}
	value, err := m.Store.GetRemoteFile(ctx, workspaceID, update.Lookup)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	if update.SetTitle {
		value.Title = strings.Join(strings.Fields(strings.TrimSpace(update.Title)), " ")
		if value.Title == "" || len(value.Title) > 255 {
			return domain.RemoteFile{}, ErrInvalidRemoteFile
		}
	}
	if update.SetFileType {
		value.FileType = strings.TrimSpace(update.FileType)
		if len(value.FileType) > 100 {
			return domain.RemoteFile{}, ErrInvalidRemoteFile
		}
	}
	if update.SetExternalURL {
		value.ExternalURL = strings.TrimSpace(update.ExternalURL)
		if !validRemoteFileURL(value.ExternalURL, 2048) {
			return domain.RemoteFile{}, ErrInvalidRemoteFile
		}
	}
	if update.SetPreviewImage {
		value.PreviewImage = strings.TrimSpace(update.PreviewImage)
		if value.PreviewImage != "" && !validRemoteFileURL(value.PreviewImage, 2048) {
			return domain.RemoteFile{}, ErrInvalidRemoteFile
		}
	}
	if update.SetIndexableData {
		if len(update.IndexableContents) > 1<<20 {
			return domain.RemoteFile{}, ErrInvalidRemoteFile
		}
		value.IndexableContents = update.IndexableContents
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("remote_file.updated", events.String("file_id", string(value.ID)), events.String("external_id", value.ExternalID)), time.Now().UTC())
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return m.Store.UpdateRemoteFile(ctx, workspaceID, value, event)
}

func validRemoteFileURL(value string, maxLength int) bool {
	if value == "" || len(value) > maxLength {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func normalizeRemoteFileChannels(values []domain.ConversationID) ([]domain.ConversationID, error) {
	seen := make(map[domain.ConversationID]struct{}, len(values))
	result := make([]domain.ConversationID, 0, len(values))
	for _, value := range values {
		value = domain.ConversationID(strings.TrimSpace(string(value)))
		if value == "" {
			return nil, ErrInvalidRemoteFile
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 || len(result) > 100 {
		return nil, ErrInvalidRemoteFile
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (m Messages) Search(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	return m.SearchMessages(ctx, workspaceID, userID, domain.MessageSearchRequest{Query: query, Page: request})
}

func (m Messages) SearchMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageSearchRequest) (domain.MessagePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.MessagePage{}, err
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || utf8.RuneCountInString(request.Query) > 500 {
		return domain.MessagePage{}, ErrInvalidSearch
	}
	sortOrder, direction, err := domain.NormalizeSearchOrder(string(request.Sort), string(request.Direction))
	if err != nil {
		return domain.MessagePage{}, ErrInvalidSearch
	}
	parsed, err := parseSearchQuery(request.Query)
	if err != nil {
		return domain.MessagePage{}, ErrInvalidSearch
	}
	search := domain.MessageSearch{
		Terms: parsed.terms, ExcludedTerms: parsed.excludedTerms,
		After: parsed.after, Before: parsed.before,
		ThreadOnly: parsed.threadOnly, HasFiles: parsed.hasFiles,
		HasPins: parsed.hasPins, HasReactions: parsed.hasReactions, ReactionName: parsed.reactionName, HasLink: parsed.hasLink,
		Sort: sortOrder, Direction: direction, Page: request.Page,
	}
	search.Page.Descending = direction == domain.SearchDirectionDescending
	if request.Conversation != "" {
		if _, err := m.ConversationInfo(ctx, workspaceID, userID, request.Conversation); err != nil {
			return domain.MessagePage{}, err
		}
		search.Conversation = request.Conversation
	}
	if parsed.conversation != "" {
		search.Conversation = m.resolveSearchConversation(ctx, workspaceID, userID, parsed.conversation)
	}
	if parsed.excludedConversation != "" {
		search.ExcludedConversation = m.resolveSearchConversation(ctx, workspaceID, userID, parsed.excludedConversation)
	}
	if parsed.author != "" {
		search.Author = m.resolveSearchUser(ctx, workspaceID, userID, parsed.author)
	}
	if parsed.excludedAuthor != "" {
		search.ExcludedAuthor = m.resolveSearchUser(ctx, workspaceID, userID, parsed.excludedAuthor)
	}
	if parsed.withUser != "" {
		search.WithUser = m.resolveSearchUser(ctx, workspaceID, userID, parsed.withUser)
	}
	if parsed.saved {
		search.SavedBy = userID
	}
	return m.Store.SearchMessages(ctx, workspaceID, userID, search)
}

func (m Messages) SearchFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.FileSearchRequest) (domain.FilePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.FilePage{}, err
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || utf8.RuneCountInString(request.Query) > 500 || request.Count <= 0 || request.Count > 100 || request.Page <= 0 || request.Page > 100 {
		return domain.FilePage{}, ErrInvalidSearch
	}
	sortOrder, direction, err := domain.NormalizeSearchOrder(string(request.Sort), string(request.Direction))
	if err != nil {
		return domain.FilePage{}, ErrInvalidSearch
	}
	parsed, err := parseSearchQuery(request.Query)
	if err != nil {
		return domain.FilePage{}, ErrInvalidSearch
	}
	search := domain.FileSearch{
		Terms: parsed.terms, ExcludedTerms: parsed.excludedTerms,
		FileType: parsed.fileType, After: parsed.after, Before: parsed.before,
		Sort: sortOrder, Direction: direction, Count: request.Count, Page: request.Page,
	}
	if request.Conversation != "" {
		if _, err := m.ConversationInfo(ctx, workspaceID, userID, request.Conversation); err != nil {
			return domain.FilePage{}, err
		}
		search.Conversation = request.Conversation
	}
	if parsed.author != "" {
		search.Uploader = m.resolveSearchUser(ctx, workspaceID, userID, parsed.author)
	}
	if parsed.excludedAuthor != "" {
		search.ExcludedUploader = m.resolveSearchUser(ctx, workspaceID, userID, parsed.excludedAuthor)
	}
	if parsed.conversation != "" {
		search.Conversation = m.resolveSearchConversation(ctx, workspaceID, userID, parsed.conversation)
	}
	if parsed.excludedConversation != "" {
		search.ExcludedConversation = m.resolveSearchConversation(ctx, workspaceID, userID, parsed.excludedConversation)
	}
	return m.Store.SearchFiles(ctx, workspaceID, userID, search)
}

func (m Messages) RecordSearch(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 500 {
		return ErrInvalidSearch
	}
	return m.Store.RecordSearchHistory(ctx, domain.SearchHistoryEntry{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Query:       query,
		SearchedAt:  time.Now().UTC(),
	})
}

func (m Messages) RecentSearches(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, limit int) ([]domain.SearchHistoryEntry, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > store.MaxSearchHistoryEntries {
		return nil, ErrInvalidSearch
	}
	return m.Store.ListSearchHistory(ctx, workspaceID, userID, limit)
}

type parsedSearchQuery struct {
	terms, excludedTerms                 []string
	conversation, excludedConversation   string
	author, excludedAuthor, withUser     string
	fileType                             string
	after, before                        time.Time
	threadOnly, hasFiles, hasPins, saved bool
	hasReactions                         bool
	reactionName                         string
	hasLink                              bool
}

// parseSearchPeriod accepts the forms Slack's search help shows for during:.
// A month alone means that month of the current year, which is what a member
// typing "during:July" in July means; a year alone means the whole year. The
// numeric form is kept because it is unambiguous and was already accepted.
func parseSearchPeriod(value string) (time.Time, time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, time.Time{}, false
	}
	if month, err := time.Parse("2006-01", value); err == nil {
		return month, month.AddDate(0, 1, 0), true
	}
	for _, layout := range []string{"January 2006", "Jan 2006"} {
		if month, err := time.Parse(layout, value); err == nil {
			return month, month.AddDate(0, 1, 0), true
		}
	}
	if year, err := time.Parse("2006", value); err == nil {
		return year, year.AddDate(1, 0, 0), true
	}
	for _, layout := range []string{"January", "Jan"} {
		if month, err := time.Parse(layout, value); err == nil {
			// A bare month means this year. Reading it as year zero would
			// return nothing and look like "no results" rather than a
			// misunderstanding.
			dated := time.Date(time.Now().UTC().Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
			return dated, dated.AddDate(0, 1, 0), true
		}
	}
	return time.Time{}, time.Time{}, false
}

func parseSearchQuery(raw string) (parsedSearchQuery, error) {
	tokens, err := domain.SearchQueryTokens(raw)
	if err != nil {
		return parsedSearchQuery{}, err
	}
	var result parsedSearchQuery
	for _, token := range tokens {
		excluded := strings.HasPrefix(token, "-") && len(token) > 1
		if excluded {
			token = strings.TrimPrefix(token, "-")
		}
		name, value, modifier := strings.Cut(token, ":")
		if modifier {
			name, value = strings.ToLower(name), strings.TrimSpace(value)
			switch name {
			case "in":
				if excluded {
					result.excludedConversation = value
				} else {
					result.conversation = value
				}
				continue
			case "from":
				if excluded {
					result.excludedAuthor = value
				} else {
					result.author = value
				}
				continue
			case "with":
				if !excluded {
					result.withUser = value
					continue
				}
			case "before", "after", "on":
				if excluded {
					break
				}
				date, parseErr := time.Parse("2006-01-02", value)
				if parseErr != nil {
					return parsedSearchQuery{}, parseErr
				}
				switch name {
				case "before":
					result.before = date
				case "after":
					result.after = date
				case "on":
					result.after, result.before = date, date.AddDate(0, 0, 1)
				}
				continue
			case "during":
				if excluded {
					break
				}
				after, before, parsed := parseSearchPeriod(value)
				if !parsed {
					return parsedSearchQuery{}, errors.New("invalid during: period")
				}
				result.after, result.before = after, before
				continue
			case "is":
				if excluded {
					break
				}
				switch strings.ToLower(value) {
				case "thread":
					result.threadOnly = true
					continue
				case "saved":
					result.saved = true
					continue
				}
			case "has":
				if excluded {
					break
				}
				switch strings.ToLower(value) {
				case "file":
					result.hasFiles = true
					continue
				case "pin":
					result.hasPins = true
					continue
				case "reaction":
					result.hasReactions = true
					continue
				}
				if strings.EqualFold(value, "link") {
					result.hasLink = true
					continue
				}
				if len(value) > 2 && strings.HasPrefix(value, ":") && strings.HasSuffix(value, ":") {
					// The emoji is the question. Treating `has::eyes:` as "has
					// some reaction" returns messages a member can see are
					// wrong, which is worse than returning nothing because it
					// looks like an answer.
					result.hasReactions = true
					result.reactionName = strings.Trim(value, ":")
					continue
				}
			case "type":
				if !excluded {
					result.fileType = value
					continue
				}
			}
		}
		if excluded {
			result.excludedTerms = append(result.excludedTerms, domain.FoldSearchText(token))
		} else {
			result.terms = append(result.terms, domain.FoldSearchText(token))
		}
	}
	if len(result.terms) == 0 && result.conversation == "" && result.author == "" && result.withUser == "" && result.after.IsZero() && result.before.IsZero() && !result.threadOnly && !result.hasFiles && !result.hasPins && !result.hasReactions && !result.hasLink && !result.saved && result.fileType == "" {
		return parsedSearchQuery{}, ErrInvalidSearch
	}
	return result, nil
}

func (m Messages) resolveSearchConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reference string) domain.ConversationID {
	reference = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(reference, "#"), "<#"))
	reference = strings.TrimSuffix(strings.SplitN(reference, "|", 2)[0], ">")
	if reference == "" {
		return domain.ConversationID("__no_search_match__")
	}
	if conversation, err := m.ConversationInfo(ctx, workspaceID, userID, domain.ConversationID(reference)); err == nil {
		return conversation.ID
	}
	folded := domain.FoldSearchText(reference)
	request := domain.ConversationListRequest{Limit: 200, IncludeClosedDirects: true}
	for {
		page, err := m.Conversations(ctx, workspaceID, userID, request)
		if err != nil {
			return domain.ConversationID("__no_search_match__")
		}
		for _, conversation := range page.Conversations {
			if domain.FoldSearchText(conversation.Name) == folded {
				return conversation.ID
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			break
		}
		request.Cursor = page.NextCursor
	}
	return domain.ConversationID("__no_search_match__")
}

func (m Messages) resolveSearchUser(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reference string) domain.UserID {
	reference = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(reference, "@"), "<@"))
	reference = strings.TrimSuffix(strings.SplitN(reference, "|", 2)[0], ">")
	if reference == "" {
		return domain.UserID("__no_search_match__")
	}
	if user, err := m.UserInfo(ctx, workspaceID, userID, domain.UserID(reference)); err == nil {
		return user.ID
	}
	folded := domain.FoldSearchText(reference)
	request := domain.PageRequest{Limit: 200}
	for {
		page, err := m.Users(ctx, workspaceID, userID, request)
		if err != nil {
			return domain.UserID("__no_search_match__")
		}
		for _, user := range page.Users {
			for _, candidate := range []string{user.Name, user.RealName, user.Profile.DisplayName, user.Email} {
				if domain.FoldSearchText(candidate) == folded {
					return user.ID
				}
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			break
		}
		request.Cursor = page.NextCursor
	}
	return domain.UserID("__no_search_match__")
}

func (m Messages) ListEventsAfter(ctx context.Context, workspace domain.WorkspaceID, after uint64, limit int) ([]events.Record, error) {
	return m.Store.ListEventsAfter(ctx, workspace, after, limit)
}

// IntegrationLogs answers team.integrationLogs, the administrative record of who
// installed, approved, restricted or removed an app.
//
// Authority: requireWorkspaceAdmin. It gated on workspace membership, although
// the disclosure is administrative and the implementation scans the workspace
// journal from sequence zero with a user read per matching record — a request
// any member could issue in a loop, each one costing the server a full journal
// scan.
//
// The scan itself is still O(journal); it needs a store operation that filters,
// offsets and joins in the backend. See the ListIntegrationLogs signature
// reported with this change.
func (m Messages) IntegrationLogs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID, changeType, serviceID, userFilter string, count, page int) (domain.IntegrationLogPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.IntegrationLogPage{}, err
	}
	appID = strings.TrimSpace(appID)
	changeType = strings.TrimSpace(changeType)
	serviceID = strings.TrimSpace(serviceID)
	userFilter = strings.TrimSpace(userFilter)
	if count <= 0 || count > 1000 || page <= 0 || page > 100 {
		return domain.IntegrationLogPage{}, ErrInvalidIntegrationLogs
	}
	if changeType != "" {
		validChangeType := changeType == "added" || changeType == "removed" || changeType == "enabled" || changeType == "disabled" || changeType == "expanded" || changeType == "updated"
		if !validChangeType {
			return domain.IntegrationLogPage{}, ErrInvalidIntegrationLogs
		}
	}
	start := (page - 1) * count
	total := 0
	logs := make([]domain.IntegrationLog, 0, count)
	var after uint64
	for {
		records, err := m.Store.ListEventsAfter(ctx, workspaceID, after, 100)
		if err != nil {
			return domain.IntegrationLogPage{}, err
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			after = record.Sequence
			if record.Event.ActorID == "" || !strings.HasPrefix(record.Event.Topic, "app.") {
				continue
			}
			change := strings.TrimPrefix(record.Event.Topic, "app.")
			switch change {
			case "approved":
				change = "added"
			case "restricted":
				change = "disabled"
			case "added", "removed", "enabled", "disabled", "expanded", "updated":
			default:
				continue
			}
			delivered, decodeErr := events.Deliverable(record.Event)
			if decodeErr != nil {
				continue
			}
			appIdentifier, hasApp := delivered.Field("app_id")
			if !hasApp {
				continue
			}
			value := domain.IntegrationLog{AppID: domain.AppID(strings.TrimSpace(appIdentifier)), AppType: "app", ChangeType: change, Date: record.Event.CreatedAt, Scope: "", UserID: record.Event.ActorID}
			if value.AppID == "" || (appID != "" && string(value.AppID) != appID) || (changeType != "" && value.ChangeType != changeType) || (serviceID != "" && value.ServiceID != serviceID) || (userFilter != "" && string(value.UserID) != userFilter) {
				continue
			}
			user, err := m.Store.GetUser(ctx, value.UserID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return domain.IntegrationLogPage{}, err
			}
			value.UserName = user.Name
			if total >= start && len(logs) < count {
				logs = append(logs, value)
			}
			total++
		}
		if len(records) < 100 {
			break
		}
	}
	pages := 0
	if total > 0 {
		pages = (total + count - 1) / count
	}
	return domain.IntegrationLogPage{Page: page, Pages: pages, Total: total, Logs: logs}, nil
}

func (m Messages) History(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.MessagePage{}, err
	}
	return m.Store.ListMessages(ctx, conversation, request)
}

func (m Messages) Replies(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) (domain.MessagePage, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.MessagePage{}, err
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return domain.MessagePage{}, ErrInvalidTimestamp
	}
	root, err := m.Store.GetMessageByCreatedAt(ctx, conversation, createdAt)
	if err != nil {
		return domain.MessagePage{}, err
	}
	if root.WorkspaceID != workspaceID {
		return domain.MessagePage{}, store.ErrNotFound
	}
	return m.Store.ListThreadMessages(ctx, conversation, timestamp, request)
}

func (m Messages) ConversationInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.withSharedIdentity(ctx, conversation), nil
}

// withSharedIdentity fills the Slack Connect fields conversations.info emits.
// They are derived from the teams already attached and the invitations still
// outstanding, rather than stored as flags someone has to remember to clear: a
// decided invitation that left a pending flag set would show a pending badge
// forever, and the derived answer cannot drift from the rows it is read from.
//
// A failure to read either leaves the fields false rather than failing the
// conversation: a channel that will not render because its sharing state could
// not be read is a worse outcome than one whose Connect badge is missing.
func (m Messages) withSharedIdentity(ctx context.Context, conversation domain.Conversation) domain.Conversation {
	if conversation.ID == "" || conversation.IsDirectOrGroup() {
		return conversation
	}
	if teams, _, err := m.Store.ListConversationTeams(ctx, conversation.WorkspaceID, conversation.ID); err == nil {
		for _, team := range teams {
			if team != conversation.WorkspaceID {
				conversation.IsExtShared = true
				break
			}
		}
	}
	for _, status := range []domain.SharedInviteStatus{domain.SharedInvitePending, domain.SharedInviteApproved} {
		page, err := m.Store.ListSharedInvites(ctx, conversation.WorkspaceID, status, domain.PageRequest{Limit: 50})
		if err != nil {
			continue
		}
		for _, invite := range page.Invites {
			if invite.ConversationID == conversation.ID {
				conversation.IsPendingExtShared = true
			}
		}
	}
	return conversation
}

func (m Messages) UserInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, requestedID domain.UserID) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.User{}, err
	}
	user, err := m.Store.GetUser(ctx, requestedID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.User{}, store.ErrNotFound
	}
	return user, nil
}

func (m Messages) RemoveUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) error {
	actor, err := m.requireWorkspaceRole(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	// Removal carries the same authority as demotion: it ends the target's
	// participation entirely, so an administrator must not be able to apply it
	// to an owner, and the last owner must not be removable.
	membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, targetID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		if membership.Role.Outranks(actor.Role) {
			return ErrNotWorkspaceAdmin
		}
		if membership.Role == domain.WorkspaceRoleOwner {
			if err := m.refuseLastOwnerChange(ctx, workspaceID, targetID); err != nil {
				return err
			}
		}
	}
	target, getErr := m.Store.GetUser(ctx, targetID)
	if getErr != nil || target.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	payload, err := events.UserChangePayload("user.removed", target, true, false, time.Now().UTC())
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, payload, time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetUserDeleted(ctx, workspaceID, targetID, true, event)
}

func (m Messages) SetUserRole(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID, role domain.WorkspaceRole) error {
	actor, err := m.requireWorkspaceRole(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}
	if err := m.authorizeRoleChange(ctx, workspaceID, actor, targetID, role); err != nil {
		return err
	}
	return m.setWorkspaceRole(ctx, workspaceID, actorID, targetID, role)
}

// authorizeRoleChange enforces the workspace role hierarchy on a role mutation.
//
// Before this, "is the actor an administrator" was the only question asked, and
// Admin and Owner answered it identically. An administrator could therefore
// grant themselves Owner, demote the real owner, and remove them — taking a
// workspace from its owner permanently. Three rules close that:
//
//   - nobody may grant a role at or above their own, so an administrator cannot
//     mint an owner and cannot promote themselves;
//   - nobody may change the role of someone who outranks them, so an
//     administrator cannot demote an owner;
//   - the last remaining owner cannot be demoted, because a workspace with no
//     owner cannot appoint one and is permanently unadministrable.
func (m Messages) authorizeRoleChange(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.WorkspaceMembership, targetID domain.UserID, role domain.WorkspaceRole) error {
	// An actor may grant any role up to and including their own, so an
	// administrator can appoint administrators but not owners. Requiring the
	// actor to strictly outrank the granted role would stop an administrator
	// appointing another administrator, which is ordinary workspace management.
	if role.Rank() > actor.Role.Rank() {
		return ErrNotWorkspaceAdmin
	}
	target, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, targetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return err
	}
	if target.Role.Outranks(actor.Role) {
		return ErrNotWorkspaceAdmin
	}
	if target.Role == domain.WorkspaceRoleOwner && role != domain.WorkspaceRoleOwner {
		return m.refuseLastOwnerChange(ctx, workspaceID, targetID)
	}
	return nil
}

// refuseLastOwnerChange reports ErrLastWorkspaceOwner when targetID is the only
// active owner the workspace has.
func (m Messages) refuseLastOwnerChange(ctx context.Context, workspaceID domain.WorkspaceID, targetID domain.UserID) error {
	page, err := m.Store.ListUsersByRole(ctx, workspaceID, domain.WorkspaceRoleOwner, domain.PageRequest{Limit: 2})
	if err != nil {
		return err
	}
	for _, owner := range page.Users {
		if owner.ID != targetID && !owner.Deleted {
			return nil
		}
	}
	return ErrLastWorkspaceOwner
}

// requireWorkspaceRole is requireWorkspaceAdmin, returning the membership so a
// caller can compare authority rather than re-reading it.
func (m Messages) requireWorkspaceRole(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (domain.WorkspaceMembership, error) {
	membership, err := m.activeWorkspaceMembership(ctx, workspaceID, actorID)
	if err != nil {
		return domain.WorkspaceMembership{}, err
	}
	if membership.Role != domain.WorkspaceRoleAdmin && membership.Role != domain.WorkspaceRoleOwner {
		return domain.WorkspaceMembership{}, ErrNotWorkspaceAdmin
	}
	return membership, nil
}

// setWorkspaceRole is the validation and write shared by the administrative
// SetUserRole and the provider-driven SynchronizeExternalUserRole. It performs no
// authority check of its own; every caller must have decided authority first.
func (m Messages) setWorkspaceRole(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID, role domain.WorkspaceRole) error {
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		// This is reachable from admin.users.setRole AND from the provider-driven
		// SynchronizeExternalUserRole. As a bare errors.New it had no domain class,
		// so the chat gRPC boundary answered codes.Unavailable with fixed text and
		// the caller was told to retry a request that can never succeed.
		return ErrInvalidWorkspace
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("workspace.role_changed", events.String("user_id", string(targetID)), events.String("role", string(role))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetWorkspaceRole(ctx, workspaceID, targetID, role, event)
}

// UserExpiration reports when a guest account lapses. A zero time means the
// account does not lapse.
func (m Messages) UserExpiration(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) (time.Time, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return time.Time{}, err
	}
	return m.Store.GetUserExpiration(ctx, workspaceID, targetID)
}

func (m Messages) SetUserExpiration(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID, expiration time.Time) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if targetID == "" || (!expiration.IsZero() && expiration.Before(time.Unix(0, 0).UTC())) {
		return ErrInvalidWorkspace
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return store.ErrNotFound
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.expiration_changed", events.String("user_id", string(targetID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetUserExpiration(ctx, workspaceID, targetID, expiration.UTC(), event)
}

func (m Messages) AdminRenameConversation(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	event, err := newEvent(workspaceID, actorID, conversationPayload("conversation.renamed_by_admin", conversationID), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.RenameConversation(ctx, conversationID, name, event)
}

// AdminBulkArchiveConversations archives several channels. The service checks
// every channel before it archives one, so an administrator who names a channel
// that is not here learns it before the request changes anything. A channel that
// is already archived is the state the request asked for.
func (m Messages) AdminBulkArchiveConversations(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrInvalidConversation
	}
	pending := make([]domain.Conversation, 0, len(ids))
	for _, id := range ids {
		conversation, err := m.Store.GetConversation(ctx, id)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
		if conversation.IsDirectOrGroup() {
			return ErrInvalidConversation
		}
		pending = append(pending, conversation)
	}
	for _, conversation := range pending {
		if conversation.Archived {
			continue
		}
		if _, err := m.AdminSetConversationArchived(ctx, workspaceID, actorID, conversation.ID, true); err != nil {
			return err
		}
	}
	return nil
}

// AdminBulkDeleteConversations deletes several channels under the same rule.
func (m Messages) AdminBulkDeleteConversations(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrInvalidConversation
	}
	for _, id := range ids {
		conversation, err := m.Store.GetConversation(ctx, id)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
		if conversation.IsDirectOrGroup() {
			return ErrInvalidConversation
		}
	}
	for _, id := range ids {
		if err := m.AdminDeleteConversation(ctx, workspaceID, actorID, id); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) AdminSetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsDirectOrGroup() {
		return domain.Conversation{}, ErrInvalidConversation
	}
	if archived == conversation.Archived {
		if archived {
			return domain.Conversation{}, ErrConversationAlreadyArchived
		}
		return domain.Conversation{}, ErrConversationNotArchived
	}
	if archived {
		required, err := m.isDefaultConversation(ctx, workspaceID, conversationID)
		if err != nil {
			return domain.Conversation{}, err
		}
		if required {
			return domain.Conversation{}, ErrCannotArchiveDefault
		}
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("conversation.archive_changed_by_admin", events.String("channel_id", string(conversationID)), events.Bool("archived", archived)), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationArchived(ctx, conversationID, archived, event)
}

func (m Messages) AdminDeleteConversation(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if conversation.IsDirectOrGroup() {
		return ErrInvalidConversation
	}
	event, err := conversationLifecycleEvent(workspaceID, "conversation.deleted", conversation, actorID)
	if err != nil {
		return err
	}
	return m.Store.DeleteConversation(ctx, workspaceID, conversationID, event)
}

func (m Messages) AdminAddConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID) error {
	return m.changeConversationAccessGroup(ctx, workspaceID, actorID, conversationID, groupID, true)
}

func (m Messages) AdminRemoveConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID) error {
	return m.changeConversationAccessGroup(ctx, workspaceID, actorID, conversationID, groupID, false)
}

func (m Messages) changeConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID, add bool) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if conversation.Kind != domain.ConversationTypePrivate || groupID == "" {
		return ErrInvalidConversation
	}
	if _, err := m.Store.GetUserGroup(ctx, workspaceID, groupID); err != nil {
		return err
	}
	groups, err := m.Store.ListConversationAccessGroups(ctx, workspaceID, conversationID)
	if err != nil {
		return err
	}
	set := make(map[domain.UserGroupID]struct{}, len(groups)+1)
	for _, current := range groups {
		set[current] = struct{}{}
	}
	if add {
		if _, exists := set[groupID]; exists {
			return store.ErrAlreadyExists
		}
		set[groupID] = struct{}{}
	} else {
		if _, exists := set[groupID]; !exists {
			return store.ErrNotFound
		}
		delete(set, groupID)
	}
	groups = make([]domain.UserGroupID, 0, len(set))
	for current := range set {
		groups = append(groups, current)
	}
	sort.Slice(groups, func(left, right int) bool { return groups[left] < groups[right] })
	topic := "conversation.access_group_added"
	if !add {
		topic = "conversation.access_group_removed"
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload(topic, events.String("channel_id", string(conversationID)), events.String("usergroup_id", string(groupID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetConversationAccessGroups(ctx, workspaceID, conversationID, groups, event)
}

func (m Messages) AdminListConversationAccessGroups(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) ([]domain.UserGroupID, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	if conversation.Kind != domain.ConversationTypePrivate {
		return nil, ErrInvalidConversation
	}
	return m.Store.ListConversationAccessGroups(ctx, workspaceID, conversationID)
}

func (m Messages) AdminApproveInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.InviteRequestID) error {
	return m.changeInviteRequestStatus(ctx, workspaceID, actorID, id, domain.InviteRequestApproved)
}

// AdminDenyInviteRequest answers a pending request, and withdraws an approved
// invitation nobody has accepted yet. The two are different facts and are
// recorded as different statuses: denied is the answer to a request, revoked is
// the withdrawal of an answer already given.
func (m Messages) AdminDenyInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.InviteRequestID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidInviteRequest
	}
	request, err := m.Store.GetInviteRequest(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if request.Status == domain.InviteRequestApproved {
		return m.reviewInviteRequest(ctx, workspaceID, actorID, id, domain.InviteRequestApproved, domain.InviteRequestRevoked)
	}
	return m.changeInviteRequestStatus(ctx, workspaceID, actorID, id, domain.InviteRequestDenied)
}

func (m Messages) changeInviteRequestStatus(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.InviteRequestID, status domain.InviteRequestStatus) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidInviteRequest
	}
	request, err := m.Store.GetInviteRequest(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if request.Status != domain.InviteRequestPending {
		return ErrInvalidInviteRequest
	}
	// Approving a lapsed request issues an invitation acceptance will refuse on
	// the same deadline. Denying one stays available, because clearing a queue
	// of lapsed requests is the remaining useful action.
	if status == domain.InviteRequestApproved && request.Expired(time.Now().UTC()) {
		return ErrInvitationExpired
	}
	return m.reviewInviteRequest(ctx, workspaceID, actorID, id, domain.InviteRequestPending, status)
}

func (m Messages) reviewInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.InviteRequestID, from, status domain.InviteRequestStatus) error {
	topic := "invite_request.approved"
	switch status {
	case domain.InviteRequestDenied:
		topic = "invite_request.denied"
	case domain.InviteRequestRevoked:
		topic = "invite_request.revoked"
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload(topic, events.String("invite_request_id", string(id))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetInviteRequestStatus(ctx, workspaceID, id, from, status, time.Now().UTC(), event)
}

// InvitationLifetime bounds how long an invitation may be accepted for. It runs
// from when the invitation is recorded: an invitation that sat in the queue for
// a month is not made fresh by someone finally approving it.
const InvitationLifetime = 14 * 24 * time.Hour

// InvitationPreview reads one invitation with no authority check, because the
// person it is for has no account and no session yet — that is the whole point
// of the page it feeds. It is safe to read without authority because it grants
// nothing: acceptance is decided against an address the identity provider has
// verified, so knowing an invitation exists does not let anyone redeem it.
func (m Messages) InvitationPreview(ctx context.Context, workspaceID domain.WorkspaceID, id domain.InviteRequestID) (domain.InviteRequest, error) {
	if strings.TrimSpace(string(id)) == "" {
		return domain.InviteRequest{}, ErrInvalidInviteRequest
	}
	return m.Store.GetInviteRequest(ctx, workspaceID, id)
}

// AcceptInvitationForEmail redeems the approved invitation for a
// provider-verified address, creating the member it promised at the recorded
// guest tier and joining every channel it recorded.
//
// The caller MUST have verified the address with the identity provider first.
// The address is the whole authorization: this method has no session and no
// actor behind it, exactly like ProvisionExternalUser, and the invitation is
// the standing decision an administrator already made about that address.
func (m Messages) AcceptInvitationForEmail(ctx context.Context, workspaceID domain.WorkspaceID, email, displayName string) (domain.User, error) {
	email = domain.NormalizeEmail(email)
	if workspaceID == "" || email == "" {
		return domain.User{}, ErrInvalidInviteRequest
	}
	request, err := m.Store.FindInviteRequestByEmail(ctx, workspaceID, email, domain.InviteRequestApproved)
	if err != nil {
		return domain.User{}, err
	}
	now := time.Now().UTC()
	if !request.Acceptable(now) {
		return domain.User{}, ErrInvitationExpired
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = strings.TrimSpace(request.RealName)
	}
	if name == "" {
		name = email
	}
	id, err := domain.NewUserID()
	if err != nil {
		return domain.User{}, err
	}
	user := domain.User{ID: id, WorkspaceID: workspaceID, Email: email, Name: name, RealName: name, Presence: domain.PresenceAuto}
	membership := domain.WorkspaceMembership{
		WorkspaceID: workspaceID, UserID: id, Role: domain.WorkspaceRoleMember, Active: true,
		Restricted: request.Restricted, UltraRestricted: request.UltraRestricted,
	}
	createdPayload, err := events.UserChangePayload("user.created", user, false, false, now)
	if err != nil {
		return domain.User{}, err
	}
	created, err := newEvent(workspaceID, "", createdPayload, now)
	if err != nil {
		return domain.User{}, err
	}
	accepted, err := newEvent(workspaceID, id, events.NewPayload("invite_request.accepted",
		events.String("invite_request_id", string(request.ID)), events.String("user_id", string(id))), now)
	if err != nil {
		return domain.User{}, err
	}
	acceptance := domain.InviteRequestAcceptance{
		WorkspaceID: workspaceID, RequestID: request.ID, User: user, Membership: membership,
		Channels: append([]domain.ConversationID(nil), request.ChannelIDs...), AcceptedAt: now,
	}
	if err := m.Store.AcceptInviteRequest(ctx, acceptance, []events.Event{created, accepted}); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (m Messages) AdminListInviteRequests(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, status domain.InviteRequestStatus, request domain.PageRequest) (domain.InviteRequestPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.InviteRequestPage{}, err
	}
	if status != domain.InviteRequestPending && status != domain.InviteRequestApproved && status != domain.InviteRequestDenied {
		return domain.InviteRequestPage{}, ErrInvalidInviteRequest
	}
	return m.Store.ListInviteRequests(ctx, workspaceID, status, request)
}

func (m Messages) AdminInviteUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, email string, channels []domain.ConversationID, customMessage, realName string, resend, restricted, ultraRestricted bool, guestExpirationAt time.Time) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") || strings.TrimSpace(customMessage) != customMessage || strings.TrimSpace(realName) != realName {
		return ErrInvalidInviteRequest
	}
	if len(channels) == 0 || (!restricted && !ultraRestricted && !guestExpirationAt.IsZero()) || (restricted && ultraRestricted) {
		return ErrInvalidInviteRequest
	}
	seen := make(map[domain.ConversationID]struct{}, len(channels))
	normalizedChannels := make([]domain.ConversationID, 0, len(channels))
	for _, channelID := range channels {
		channelID = domain.ConversationID(strings.TrimSpace(string(channelID)))
		if channelID == "" {
			return ErrInvalidInviteRequest
		}
		if _, exists := seen[channelID]; exists {
			continue
		}
		conversation, err := m.Store.GetConversation(ctx, channelID)
		if err != nil || conversation.WorkspaceID != workspaceID || conversation.Kind == domain.ConversationTypeIM {
			return ErrInvalidInviteRequest
		}
		seen[channelID] = struct{}{}
		normalizedChannels = append(normalizedChannels, channelID)
	}
	if len(normalizedChannels) == 0 {
		return ErrInvalidInviteRequest
	}
	if !guestExpirationAt.IsZero() && !restricted && !ultraRestricted {
		return ErrInvalidInviteRequest
	}
	id, err := domain.PublicID("IR_")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	value := domain.InviteRequest{ID: domain.InviteRequestID(id), WorkspaceID: workspaceID, Email: email, RequestedBy: actorID, ChannelIDs: normalizedChannels, CustomMessage: customMessage, RealName: realName, Resend: resend, Restricted: restricted, UltraRestricted: ultraRestricted, GuestExpirationAt: guestExpirationAt.UTC(), Status: domain.InviteRequestPending, CreatedAt: now, ExpiresAt: now.Add(InvitationLifetime)}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("invite_request.created", events.String("invite_request_id", string(value.ID))), now)
	if err != nil {
		return err
	}
	return m.Store.CreateInviteRequest(ctx, value, event)
}

func (m Messages) AdminCreateUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, email, realName string, role domain.WorkspaceRole) (domain.User, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.User{}, err
	}
	return m.createWorkspaceUser(ctx, workspaceID, actorID, email, realName, role)
}

// createWorkspaceUser is the validation and write shared by the administrative
// AdminCreateUser and the provider-driven ProvisionExternalUser. It performs no
// authority check of its own; every caller must have decided authority first.
func (m Messages) createWorkspaceUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, email, realName string, role domain.WorkspaceRole) (domain.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	realName = strings.TrimSpace(realName)
	if workspaceID == "" || email == "" || !strings.Contains(email, "@") || len(email) > 320 || realName == "" || len(realName) > 200 {
		return domain.User{}, ErrInvalidInviteRequest
	}
	// The role a user is created with is a workspace role, not an invite-request
	// field; ErrInvalidWorkspace is the sentinel setWorkspaceRole and
	// SynchronizeExternalUserRole already use for exactly this refusal, so the
	// three places that decide "is this a role a caller may confer" now answer
	// alike. Both sentinels map to the same client-visible code.
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin {
		return domain.User{}, ErrInvalidWorkspace
	}
	if _, err := m.Store.FindUserByEmail(ctx, workspaceID, email); err == nil {
		return domain.User{}, store.ErrAlreadyExists
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.User{}, err
	}
	id, err := domain.NewUserID()
	if err != nil {
		return domain.User{}, err
	}
	now := time.Now().UTC()
	user := domain.User{ID: id, WorkspaceID: workspaceID, Email: email, Name: realName, RealName: realName, Presence: domain.PresenceAuto}
	payload, err := events.UserChangePayload("user.created", user, false, false, now)
	if err != nil {
		return domain.User{}, err
	}
	event, err := newEvent(workspaceID, actorID, payload, now)
	if err != nil {
		return domain.User{}, err
	}
	membership := domain.WorkspaceMembership{WorkspaceID: workspaceID, UserID: id, Role: role, Active: true}
	if err := m.Store.CreateUser(ctx, user, membership, event); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (m Messages) AdminAssignUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID, targetID domain.UserID, channels []domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	seen := make(map[domain.ConversationID]struct{}, len(channels))
	normalized := make([]domain.ConversationID, 0, len(channels))
	for _, channelID := range channels {
		channelID = domain.ConversationID(strings.TrimSpace(string(channelID)))
		if channelID == "" {
			return ErrInvalidInviteRequest
		}
		if _, exists := seen[channelID]; exists {
			continue
		}
		seen[channelID] = struct{}{}
		normalized = append(normalized, channelID)
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.assigned", events.String("user_id", string(targetID)), events.Strings("channel_ids", conversationIDStrings(normalized))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.AssignUser(ctx, workspaceID, targetID, normalized, event)
}

// AdminUninstallApps removes apps from the workspace.
//
// UninstallApp already existed and is the app's own: it is proven by the
// client credentials, which is how an app removes itself. An administrator
// holds no app's client secret, so until now the workspace could approve an
// app and never take it back — the one direction a governance surface most
// needs.
//
// Every named app is checked before any is removed, for the reason a bulk
// sign-out is: an administrator acting on a list finds out they were wrong
// rather than discovering later that an arbitrary prefix of it was applied.
// An app that is not installed is not an error — it is the state that was
// asked for.
func (m Messages) AdminUninstallApps(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appIDs []domain.AppID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(appIDs) == 0 {
		return ErrInvalidAppApproval
	}
	installed := make([]domain.AppID, 0, len(appIDs))
	for _, appID := range appIDs {
		if strings.TrimSpace(string(appID)) == "" {
			return ErrInvalidAppApproval
		}
		if _, _, err := m.Store.GetApp(ctx, appID); err != nil {
			return err
		}
		installations, err := m.Store.ListAppInstallations(ctx, appID)
		if err != nil {
			return err
		}
		for _, installation := range installations {
			if installation.WorkspaceID == workspaceID && installation.Enabled {
				installed = append(installed, appID)
				break
			}
		}
	}
	for _, appID := range installed {
		// The same announcement the app's own uninstall commits, for the same
		// reason: the app can still receive the one event that explains why
		// everything else stopped, whoever ended the installation.
		announcement, err := newEvent(workspaceID, actorID, events.NewPayload("app.uninstalled",
			events.String("app_id", string(appID)),
			events.String("target_app_id", string(appID)),
		), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := m.Store.UninstallApp(ctx, workspaceID, appID, announcement); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) AdminApproveApp(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	return m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalApproved)
}

// AdminRestrictApp records the policy and applies it.
//
// It used to only write the approval row, so restricting an app moved it
// between two administrative lists and changed nothing else: the installation
// stayed enabled, its tokens stayed live, its bot stayed in every conversation,
// and it went on posting, opening modals and answering interactions. An
// administrator shutting down a misbehaving app was told it had worked.
//
// Restriction therefore uninstalls the app from the workspace, which is the
// operation that already revokes its credentials, disables its hooks, clears
// its datastore and removes its bot. Journey 12 states the requirement this
// meets: a disabled app does not remain operational through a stale token.
// Doing it through the existing uninstall keeps one definition of what
// stopping an app means, rather than a second, weaker one for policy.
func (m Messages) AdminRestrictApp(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	if err := m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalRestricted); err != nil {
		return err
	}
	// A restriction may name only a request, in which case the approval lives
	// under a synthesised app id and there is no installation to act on.
	installed := domain.AppID(strings.TrimSpace(string(appID)))
	if installed == "" {
		return nil
	}
	announcement, err := newEvent(workspaceID, actorID, events.NewPayload("app.uninstalled",
		events.String("app_id", string(installed)),
		events.String("target_app_id", string(installed)),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	// Restricting an app the workspace never installed is a legitimate
	// pre-emptive decision, so an absent installation is not a failure.
	if err := m.Store.UninstallApp(ctx, workspaceID, installed, announcement); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// AdminCancelAppRequest withdraws an app request. The member who asked or an
// administrator may cancel it, and cancelling records that nobody decided it.
// AdminCancelAppRequest withdraws a request nobody has decided. Cancelling is
// not a decision an administrator may take back: cancelling an approved request
// used to write cancelled straight over the approval, which reads as "the
// request went away" while the app stays installed and approved.
func (m Messages) AdminCancelAppRequest(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	// The identifier has to be resolved the same way the write resolves it, or
	// the guard reads one row and the write changes another. A request named by
	// its own identifier alone is stored under a synthesised app id.
	current, err := m.Store.GetAppApproval(ctx, workspaceID, appApprovalKey(appID, requestID))
	if err != nil {
		return err
	}
	if current.Status != domain.AppApprovalRequested {
		return ErrInvalidAppApproval
	}
	return m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalCancelled)
}

// appApprovalKey is where one approval row lives. A request named only by its
// own identifier is stored under a synthesised app id, and every reader and
// writer has to agree on that or they touch different rows.
func appApprovalKey(appID domain.AppID, requestID domain.AppRequestID) domain.AppID {
	appID = domain.AppID(strings.TrimSpace(string(appID)))
	if appID != "" {
		return appID
	}
	return domain.AppID("request:" + strings.TrimSpace(string(requestID)))
}

func (m Messages) changeAppApproval(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID, status domain.AppApprovalStatus) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	appID = domain.AppID(strings.TrimSpace(string(appID)))
	requestID = domain.AppRequestID(strings.TrimSpace(string(requestID)))
	if appID == "" && requestID == "" {
		return ErrInvalidAppApproval
	}
	appID = appApprovalKey(appID, requestID)
	// Cancelling is final in both directions. AdminCancelAppRequest already
	// refused to cancel a decision an administrator had taken; nothing refused
	// the reverse, so approving or restricting a withdrawn request wrote a live
	// decision over it and the request came back from the dead. Restricting one
	// went further and uninstalled the app, on the strength of a request its
	// author had taken back.
	//
	// The check lives here rather than in the three callers because it is one
	// rule about one row: a caller that forgets it is the defect, and a rule
	// written three times is a rule two of them can drift from.
	//
	// An approval that does not exist yet is not a resurrection: the first
	// decision on a request nobody has recorded is how the row is created.
	current, err := m.Store.GetAppApproval(ctx, workspaceID, appID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil && current.Status == domain.AppApprovalCancelled && status != domain.AppApprovalCancelled {
		return ErrInvalidAppApproval
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, actorID, events.NewPayload("app."+string(status), events.String("app_id", string(appID)), events.String("app_request_id", string(requestID))), now)
	if err != nil {
		return err
	}
	return m.Store.SetAppApproval(ctx, workspaceID, appID, requestID, status, now, event)
}

func (m Messages) AdminListApps(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, status domain.AppApprovalStatus, request domain.PageRequest) (domain.AppApprovalPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AppApprovalPage{}, err
	}
	if status != domain.AppApprovalRequested && status != domain.AppApprovalApproved && status != domain.AppApprovalRestricted && status != domain.AppApprovalCancelled {
		return domain.AppApprovalPage{}, ErrInvalidAppApproval
	}
	return m.Store.ListAppApprovals(ctx, workspaceID, status, request)
}

func (m Messages) RequestAppPermissions(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, target domain.UserID, scopes []string, triggerID string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	if target == "" {
		target = actor
	}
	user, err := m.Store.GetUser(ctx, target)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return store.ErrNotFound
	}
	scopes = domain.NormalizeScopes(scopes)
	triggerID = strings.TrimSpace(triggerID)
	if len(scopes) == 0 || triggerID == "" {
		return ErrInvalidAppApproval
	}
	id, err := domain.NewAppRequestID()
	if err != nil {
		return err
	}
	value := domain.AppPermissionRequest{ID: id, WorkspaceID: workspaceID, RequesterID: actor, TargetUserID: target, Scopes: scopes, TriggerID: triggerID, CreatedAt: time.Now().UTC()}
	event, err := newEvent(workspaceID, actor, events.NewPayload("app.permissions_requested", events.String("app_request_id", string(id)), events.String("user_id", string(target)), events.Strings("scopes", scopes)), value.CreatedAt)
	if err != nil {
		return err
	}
	return m.Store.CreateAppPermissionRequest(ctx, value, event)
}

func viewPayload(payload string) (string, string, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" || len(payload) > 250*1024 {
		return "", "", ErrInvalidView
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil || fields == nil {
		return "", "", ErrInvalidView
	}
	viewType := strings.TrimSpace(stringValue(fields["type"]))
	if viewType != "modal" && viewType != "home" {
		return "", "", ErrInvalidView
	}
	externalID, ok := fields["external_id"].(string)
	if fields["external_id"] != nil && !ok {
		return "", "", ErrInvalidView
	}
	externalID = strings.TrimSpace(externalID)
	if utf8.RuneCountInString(externalID) > 255 {
		return "", "", ErrInvalidView
	}
	blocks, ok := fields["blocks"].([]any)
	if !ok || len(blocks) > 100 {
		return "", "", ErrInvalidView
	}
	callback, callbackOK := fields["callback_id"].(string)
	if (!callbackOK && fields["callback_id"] != nil) || utf8.RuneCountInString(callback) > 255 {
		return "", "", ErrInvalidView
	}
	metadata, metadataOK := fields["private_metadata"].(string)
	if (!metadataOK && fields["private_metadata"] != nil) || utf8.RuneCountInString(metadata) > 3000 {
		return "", "", ErrInvalidView
	}
	if viewType == "modal" {
		if !validPlainTextObject(fields["title"], true) || !validPlainTextObject(fields["close"], false) || !validPlainTextObject(fields["submit"], false) {
			return "", "", ErrInvalidView
		}
		hasInput := false
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				return "", "", ErrInvalidView
			}
			blockID, blockIDOK := block["block_id"].(string)
			if (!blockIDOK && block["block_id"] != nil) || utf8.RuneCountInString(blockID) > 255 {
				return "", "", ErrInvalidView
			}
			if stringValue(block["type"]) != "input" {
				continue
			}
			hasInput = true
			if !validPlainTextObject(block["label"], true) || !validPlainTextObject(block["hint"], false) {
				return "", "", ErrInvalidView
			}
			element, ok := block["element"].(map[string]any)
			if !ok {
				return "", "", ErrInvalidView
			}
			actionID := strings.TrimSpace(stringValue(element["action_id"]))
			if actionID == "" || utf8.RuneCountInString(actionID) > 255 {
				return "", "", ErrInvalidView
			}
		}
		if hasInput && fields["submit"] == nil {
			return "", "", ErrInvalidView
		}
	}
	return viewType, externalID, nil
}

func validPlainTextObject(value any, required bool) bool {
	if value == nil {
		return !required
	}
	object, ok := value.(map[string]any)
	if !ok || stringValue(object["type"]) != "plain_text" {
		return false
	}
	text, ok := object["text"].(string)
	if !ok {
		return false
	}
	length := utf8.RuneCountInString(text)
	return length > 0 && length <= 24
}

func normalizeViewPayload(id domain.ViewID, payload string) (string, error) {
	var fields map[string]any
	if json.Unmarshal([]byte(payload), &fields) != nil || fields == nil {
		return "", ErrInvalidView
	}
	for _, name := range []string{"id", "team_id", "app_id", "hash", "root_view_id", "previous_view_id", "state", "bot_id"} {
		delete(fields, name)
	}
	blocks, _ := fields["blocks"].([]any)
	seen := make(map[string]struct{}, len(blocks))
	for index, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return "", ErrInvalidView
		}
		blockID := strings.TrimSpace(stringValue(block["block_id"]))
		if blockID == "" {
			blockID = fmt.Sprintf("block_%s_%d", id, index)
			block["block_id"] = blockID
		}
		if _, duplicate := seen[blockID]; duplicate {
			return "", ErrInvalidView
		}
		seen[blockID] = struct{}{}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", ErrInvalidView
	}
	return string(encoded), nil
}

func viewHash(id domain.ViewID, payload string, now time.Time) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(string(id)+"\x00"+payload+"\x00"+now.UTC().Format(time.RFC3339Nano))))
}

func (m Messages) OpenView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, triggerID, payload string) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	trigger, err := m.consumeAppTrigger(ctx, workspaceID, appID, triggerID)
	if err != nil {
		return domain.View{}, err
	}
	return m.createView(ctx, workspaceID, appID, trigger.UserID, payload, "", "", "", "view.opened")
}

// PublishView replaces the App Home surface a workspace member sees.
//
// Slack defines this as an app-owned surface published for a target user, with
// no method scope required. AppID is therefore the security boundary: an
// authenticated app can publish any workspace member's instance of its own Home
// tab, but cannot read or replace another app's surface.
func (m Messages) PublishView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, target domain.UserID, payload, expectedHash string) (domain.View, error) {
	if appID == "" {
		return domain.View{}, ErrInvalidView
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	_, parsed, err := m.installedApp(ctx, workspaceID, appID)
	if err != nil {
		return domain.View{}, err
	}
	if !parsed.HomeTabEnabled {
		return domain.View{}, ErrAppHomeNotEnabled
	}
	user, err := m.Store.GetUser(ctx, target)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.View{}, store.ErrNotFound
	}
	viewType, _, err := viewPayload(payload)
	if err != nil || viewType != "home" {
		return domain.View{}, ErrInvalidView
	}
	current, err := m.Store.GetPublishedView(ctx, workspaceID, target, appID)
	if errors.Is(err, store.ErrNotFound) {
		return m.createView(ctx, workspaceID, appID, target, payload, "", "", "", "view.published")
	}
	if err != nil {
		return domain.View{}, err
	}
	return m.updateView(ctx, workspaceID, actor, current, payload, expectedHash, "view.published")
}

func (m Messages) PushView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, triggerID, payload string) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	trigger, err := m.consumeAppTrigger(ctx, workspaceID, appID, triggerID)
	if err != nil {
		return domain.View{}, err
	}
	parent, err := m.Store.GetLatestView(ctx, workspaceID, trigger.UserID, appID, "modal")
	if errors.Is(err, store.ErrNotFound) {
		return m.createView(ctx, workspaceID, appID, trigger.UserID, payload, "", "", "", "view.pushed")
	}
	if err != nil {
		return domain.View{}, err
	}
	if depth, err := m.viewStackDepth(ctx, parent); err != nil {
		return domain.View{}, err
	} else if depth >= 3 {
		return domain.View{}, ErrInvalidView
	}
	return m.createView(ctx, workspaceID, appID, trigger.UserID, payload, parent.RootViewID, parent.ID, "", "view.pushed")
}

func (m Messages) UpdateView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, viewID, externalID, payload, expectedHash string) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	if (strings.TrimSpace(viewID) == "") == (strings.TrimSpace(externalID) == "") {
		return domain.View{}, ErrInvalidView
	}
	var current domain.View
	var err error
	if strings.TrimSpace(viewID) != "" {
		current, err = m.Store.GetView(ctx, workspaceID, domain.ViewID(strings.TrimSpace(viewID)))
	} else {
		current, err = m.Store.GetViewByExternalID(ctx, workspaceID, appID, strings.TrimSpace(externalID))
	}
	if err != nil {
		return domain.View{}, err
	}
	if current.AppID != appID {
		return domain.View{}, store.ErrNotFound
	}
	return m.updateView(ctx, workspaceID, actor, current, payload, expectedHash, "view.updated")
}

func (m Messages) CurrentModalView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.View{}, err
	}
	return m.Store.GetCurrentView(ctx, workspaceID, userID, "modal")
}

func (m Messages) createView(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, user domain.UserID, payload string, rootID, previousID domain.ViewID, externalID, topic string) (domain.View, error) {
	viewType, payloadExternalID, err := viewPayload(payload)
	if err != nil {
		return domain.View{}, err
	}
	if externalID == "" {
		externalID = payloadExternalID
	}
	id, err := domain.NewViewID()
	if err != nil {
		return domain.View{}, err
	}
	now := time.Now().UTC()
	payload, err = normalizeViewPayload(id, payload)
	if err != nil {
		return domain.View{}, err
	}
	value := domain.View{ID: id, AppID: appID, WorkspaceID: domain.WorkspaceID(workspaceID), UserID: domain.UserID(user), Type: viewType, ExternalID: externalID, Payload: payload, Hash: viewHash(id, payload, now), CreatedAt: now, UpdatedAt: now}
	if rootID == "" {
		value.RootViewID = id
	} else {
		value.RootViewID = rootID
	}
	value.PreviousViewID = previousID
	event, err := newEvent(value.WorkspaceID, user, events.NewPayload(topic, events.String("view_id", string(value.ID)), events.String("app_id", string(appID)), events.String("user_id", string(user))), now)
	if err != nil {
		return domain.View{}, err
	}
	if err := m.Store.CreateView(ctx, value, event); err != nil {
		return domain.View{}, err
	}
	return value, nil
}

func (m Messages) updateView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, current domain.View, payload, expectedHash, topic string) (domain.View, error) {
	viewType, externalID, err := viewPayload(payload)
	if err != nil {
		return domain.View{}, err
	}
	if externalID == "" {
		externalID = current.ExternalID
	}
	now := time.Now().UTC()
	payload, err = normalizeViewPayload(current.ID, payload)
	if err != nil {
		return domain.View{}, err
	}
	value := current
	value.Type = viewType
	value.ExternalID = externalID
	value.Payload = payload
	value.State = ""
	value.Errors = nil
	value.Hash = viewHash(value.ID, value.Payload, now)
	value.UpdatedAt = now
	value.UserID = current.UserID
	event, err := newEvent(workspaceID, actor, events.NewPayload(topic, events.String("view_id", string(value.ID)), events.String("app_id", string(value.AppID)), events.String("user_id", string(value.UserID))), now)
	if err != nil {
		return domain.View{}, err
	}
	return m.Store.UpdateView(ctx, value, expectedHash, event)
}

func (m Messages) deleteView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, current domain.View, clear bool, topic string) error {
	event, err := newEvent(workspaceID, actor, events.NewPayload(topic,
		events.String("view_id", string(current.ID)),
		events.String("app_id", string(current.AppID)),
		events.String("user_id", string(current.UserID)),
		events.Bool("is_cleared", clear),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteView(ctx, workspaceID, current.UserID, current.ID, clear, event)
}

func workflowJSON(raw string, allowEmpty bool, array bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && allowEmpty {
		if array {
			return "[]", nil
		}
		return "{}", nil
	}
	if raw == "" {
		return "", ErrInvalidWorkflowStep
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", ErrInvalidWorkflowStep
	}
	if array {
		if _, ok := value.([]any); !ok {
			return "", ErrInvalidWorkflowStep
		}
	} else {
		if _, ok := value.(map[string]any); !ok {
			return "", ErrInvalidWorkflowStep
		}
	}
	return raw, nil
}

func (m Messages) WorkflowStepCompleted(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, executeID, outputs string) error {
	return m.setWorkflowStep(ctx, workspaceID, actor, executeID, domain.WorkflowStepCompleted, outputs, "", "", "")
}

func (m Messages) WorkflowStepFailed(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, executeID, failure string) error {
	failure, err := workflowJSON(failure, false, false)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(failure), &fields); err != nil {
		return ErrInvalidWorkflowStep
	}
	var message string
	if raw, ok := fields["message"]; !ok || json.Unmarshal(raw, &message) != nil || strings.TrimSpace(message) == "" {
		return ErrInvalidWorkflowStep
	}
	return m.setWorkflowStep(ctx, workspaceID, actor, executeID, domain.WorkflowStepFailed, "", failure, "", "")
}

func (m Messages) WorkflowUpdateStep(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, editID, inputs, outputs, stepName, imageURL string) error {
	if strings.TrimSpace(editID) == "" {
		return ErrInvalidWorkflowStep
	}
	inputs, err := workflowJSON(inputs, true, false)
	if err != nil {
		return err
	}
	outputs, err = workflowJSON(outputs, true, true)
	if err != nil {
		return err
	}
	return m.setWorkflowStepWithValues(ctx, workspaceID, actor, editID, domain.WorkflowStep{ID: domain.WorkflowStepID(editID), EditID: editID, Status: domain.WorkflowStepConfigured, Inputs: inputs, Outputs: outputs, StepName: strings.TrimSpace(stepName), ImageURL: strings.TrimSpace(imageURL)})
}

func (m Messages) setWorkflowStep(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, executeID string, status domain.WorkflowStepStatus, outputs, failure, stepName, imageURL string) error {
	if strings.TrimSpace(executeID) == "" {
		return ErrInvalidWorkflowStep
	}
	outputsJSON := "{}"
	if status == domain.WorkflowStepCompleted {
		var err error
		outputsJSON, err = workflowJSON(outputs, true, false)
		if err != nil {
			return err
		}
	}
	return m.setWorkflowStepWithValues(ctx, workspaceID, actor, executeID, domain.WorkflowStep{ID: domain.WorkflowStepID(executeID), Status: status, Outputs: outputsJSON, Error: failure, StepName: stepName, ImageURL: imageURL})
}

func (m Messages) setWorkflowStepWithValues(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id string, value domain.WorkflowStep) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	now := time.Now().UTC()
	value.WorkspaceID = workspaceID
	value.UserID = actor
	value.UpdatedAt = now
	event, err := newEvent(workspaceID, actor, events.NewPayload("workflow.step_"+string(value.Status), events.String("workflow_step_id", id)), now)
	if err != nil {
		return err
	}
	return m.Store.SetWorkflowStep(ctx, value, event)
}

func (m Messages) OpenDialog(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, triggerID, payload string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	trigger, err := m.consumeAppTrigger(ctx, workspaceID, appID, triggerID)
	if err != nil {
		return err
	}
	payload = strings.TrimSpace(payload)
	var fields map[string]json.RawMessage
	if payload == "" || json.Unmarshal([]byte(payload), &fields) != nil || fields == nil {
		return ErrInvalidDialog
	}
	for _, name := range []string{"callback_id", "title", "elements"} {
		if _, ok := fields[name]; !ok {
			return ErrInvalidDialog
		}
	}
	var callbackID, title string
	if json.Unmarshal(fields["callback_id"], &callbackID) != nil || strings.TrimSpace(callbackID) == "" || json.Unmarshal(fields["title"], &title) != nil || strings.TrimSpace(title) == "" {
		return ErrInvalidDialog
	}
	var elements []json.RawMessage
	if json.Unmarshal(fields["elements"], &elements) != nil || len(elements) == 0 {
		return ErrInvalidDialog
	}
	id, err := domain.NewDialogID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, actor, events.NewPayload("dialog.opened", events.String("dialog_id", string(id)), events.String("callback_id", strings.TrimSpace(callbackID))), now)
	if err != nil {
		return err
	}
	return m.Store.CreateDialog(ctx, domain.Dialog{ID: id, WorkspaceID: workspaceID, UserID: trigger.UserID, Payload: payload, CreatedAt: now}, event)
}

func (m Messages) BotInfo(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, botID domain.BotID) (domain.Bot, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Bot{}, err
	}
	if strings.TrimSpace(string(botID)) == "" {
		return domain.Bot{}, ErrInvalidBot
	}
	return m.Store.GetBot(ctx, workspaceID, botID)
}

func (m Messages) MigrationExchange(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, ids []domain.UserID, toOld bool) (domain.MigrationExchange, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.MigrationExchange{}, err
	}
	seen := make(map[domain.UserID]struct{}, len(ids))
	values := make([]domain.UserID, 0, len(ids))
	for _, id := range ids {
		id = domain.UserID(strings.TrimSpace(string(id)))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		values = append(values, id)
	}
	if len(values) == 0 || len(values) > 400 {
		return domain.MigrationExchange{}, ErrInvalidMigration
	}
	result := domain.MigrationExchange{WorkspaceID: workspaceID, UserIDMap: make(map[domain.UserID]domain.UserID, len(values)), InvalidUserIDs: make([]domain.UserID, 0)}
	for _, id := range values {
		migration, err := m.Store.FindUserMigration(ctx, workspaceID, id)
		if errors.Is(err, store.ErrNotFound) {
			result.InvalidUserIDs = append(result.InvalidUserIDs, id)
			continue
		}
		if err != nil {
			return domain.MigrationExchange{}, err
		}
		if toOld {
			result.UserIDMap[id] = migration.OldID
		} else {
			result.UserIDMap[id] = migration.GlobalID
		}
	}
	return result, nil
}

func (m Messages) OAuthExchange(ctx context.Context, clientID, clientSecret, code, redirectURI string) (domain.OAuthToken, error) {
	return m.oauthExchange(ctx, clientID, clientSecret, code, redirectURI, "", "user", false)
}

func (m Messages) OAuthV2Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI string, userOnly bool) (domain.OAuthToken, error) {
	tokenType := domain.TokenBot
	if userOnly {
		tokenType = domain.TokenUser
	}
	return m.oauthExchange(ctx, clientID, clientSecret, code, redirectURI, "", tokenType, true)
}

const oauthRotatingTokenLifetime = 12 * time.Hour

func (m Messages) oauthExchange(ctx context.Context, clientID, clientSecret, code, redirectURI, codeVerifier string, tokenType domain.TokenType, rotationAllowed bool) (domain.OAuthToken, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	code = strings.TrimSpace(code)
	if clientID == "" || clientSecret == "" || code == "" {
		return domain.OAuthToken{}, ErrInvalidOAuth
	}
	client, err := m.Store.GetOAuthClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuthClient
		}
		return domain.OAuthToken{}, err
	}
	if !secretDigestsEqual(client.SecretHash, domain.HashToken(clientSecret)) {
		return domain.OAuthToken{}, ErrInvalidOAuthClient
	}
	rotating := false
	if rotationAllowed {
		app, _, appErr := m.Store.GetApp(ctx, client.AppID)
		if appErr == nil {
			rotating = app.TokenRotationEnabled
		} else if !errors.Is(appErr, store.ErrNotFound) {
			return domain.OAuthToken{}, appErr
		}
	}
	var accessToken string
	if tokenType == "bot" && rotating {
		accessToken, err = domain.NewRotatingBotToken()
	} else if tokenType == domain.TokenBot {
		accessToken, err = domain.NewBotToken()
	} else if rotating {
		accessToken, err = domain.NewRotatingUserToken()
	} else {
		accessToken, err = domain.NewUserToken()
	}
	if err != nil {
		return domain.OAuthToken{}, err
	}
	exchange := domain.OAuthToken{TokenType: tokenType, CodeVerifier: codeVerifier}
	if tokenType == domain.TokenBot {
		if rotating {
			exchange.AuthedUserAccessToken, err = domain.NewRotatingUserToken()
		} else {
			exchange.AuthedUserAccessToken, err = domain.NewUserToken()
		}
		if err != nil {
			return domain.OAuthToken{}, err
		}
	}
	if rotating {
		exchange.RefreshToken, err = domain.NewRefreshToken()
		if err != nil {
			return domain.OAuthToken{}, err
		}
		exchange.ExpiresAt = time.Now().UTC().Add(oauthRotatingTokenLifetime)
		if tokenType == domain.TokenBot {
			exchange.AuthedUserRefreshToken, err = domain.NewRefreshToken()
			if err != nil {
				return domain.OAuthToken{}, err
			}
			exchange.AuthedUserExpiresAt = exchange.ExpiresAt
		}
	}
	token, err := m.Store.ExchangeOAuthCode(ctx, clientID, clientSecret, code, redirectURI, accessToken, exchange)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	token.AppID = client.AppID
	token.TokenType = tokenType
	if tokenType == domain.TokenBot {
		if err := m.recordAppBotToken(ctx, token.AppID, token.WorkspaceID, accessToken, token.InstallerID); err != nil {
			return domain.OAuthToken{}, err
		}
	}
	return token, nil
}

// recordAppBotToken seals a freshly issued bot access token so a later
// function_executed dispatch can include it as bot_access_token, exactly as
// Slack sends the app's token with the callback. The plaintext lives only in
// memory here; the store keeps sealed ciphertext opened at delivery time.
func (m Messages) recordAppBotToken(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, plaintext string, installer domain.UserID) error {
	// The credential key is auto-generated at startup when absent, so a missing
	// key only happens in unit tests that never dispatch — there is nothing to
	// seal for them, and production can never reach this state.
	if appID == "" || workspaceID == "" || plaintext == "" || len(m.AppCredentialKey) != 32 {
		return nil
	}
	ciphertext, err := secretbox.Seal(m.AppCredentialKey, appBotTokenAssociatedData(appID, workspaceID), plaintext)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, installer, events.NewPayload("app.bot_token_issued",
		events.String("app_id", string(appID)),
	), now)
	if err != nil {
		return err
	}
	// app_installed commits with the issuance: Slack dispatches it to the
	// newly installed app itself, so it is target-routed and automatic (no
	// manifest subscription). A re-exchange re-announces the install, which
	// is the observable Slack behavior for a reinstall.
	installed, err := newEvent(workspaceID, installer, events.NewPayload("app.installed",
		events.String("app_id", string(appID)),
		events.String("target_app_id", string(appID)),
	), now)
	if err != nil {
		return err
	}
	return m.Store.SetAppBotToken(ctx, appID, workspaceID, ciphertext, event, installed)
}

func (m Messages) OAuthV2Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (domain.OAuthToken, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	refreshToken = strings.TrimSpace(refreshToken)
	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return domain.OAuthToken{}, ErrInvalidOAuth
	}
	client, err := m.Store.GetOAuthClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuthClient
		}
		return domain.OAuthToken{}, err
	}
	if !secretDigestsEqual(client.SecretHash, domain.HashToken(clientSecret)) {
		return domain.OAuthToken{}, ErrInvalidOAuthClient
	}
	app, _, err := m.Store.GetApp(ctx, client.AppID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	if !app.TokenRotationEnabled {
		return domain.OAuthToken{}, ErrInvalidOAuth
	}
	grant, err := m.Store.LookupOAuthRefreshToken(ctx, clientID, refreshToken)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	var nextAccessToken string
	if grant.TokenType.IsBot() {
		nextAccessToken, err = domain.NewRotatingBotToken()
	} else {
		nextAccessToken, err = domain.NewRotatingUserToken()
	}
	if err != nil {
		return domain.OAuthToken{}, err
	}
	nextRefreshToken, err := domain.NewRefreshToken()
	if err != nil {
		return domain.OAuthToken{}, err
	}
	expiresAt := time.Now().UTC().Add(oauthRotatingTokenLifetime)
	token, err := m.Store.ExchangeOAuthRefreshToken(ctx, clientID, clientSecret, refreshToken, nextAccessToken, nextRefreshToken, expiresAt)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	return token, nil
}

func (m Messages) OAuthV2ExchangeToken(ctx context.Context, clientID, clientSecret, accessToken string) (domain.OAuthToken, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	accessToken = strings.TrimSpace(accessToken)
	if clientID == "" || clientSecret == "" || accessToken == "" {
		return domain.OAuthToken{}, ErrInvalidOAuth
	}
	client, err := m.Store.GetOAuthClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuthClient
		}
		return domain.OAuthToken{}, err
	}
	if !secretDigestsEqual(client.SecretHash, domain.HashToken(clientSecret)) {
		return domain.OAuthToken{}, ErrInvalidOAuthClient
	}
	app, _, err := m.Store.GetApp(ctx, client.AppID)
	if err != nil || !app.TokenRotationEnabled {
		if errors.Is(err, store.ErrNotFound) || err == nil {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	record, err := m.Store.LookupToken(ctx, accessToken)
	if err != nil || record.Revoked || record.AppID != client.AppID || !record.ExpiresAt.IsZero() || !record.TokenType.Valid() {
		if errors.Is(err, store.ErrNotFound) || err == nil {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	var nextAccessToken string
	if record.TokenType == "bot" {
		nextAccessToken, err = domain.NewRotatingBotToken()
	} else {
		nextAccessToken, err = domain.NewRotatingUserToken()
	}
	if err != nil {
		return domain.OAuthToken{}, err
	}
	nextRefreshToken, err := domain.NewRefreshToken()
	if err != nil {
		return domain.OAuthToken{}, err
	}
	token, err := m.Store.ExchangeOAuthAccessToken(ctx, clientID, clientSecret, accessToken, nextAccessToken, nextRefreshToken, time.Now().UTC().Add(oauthRotatingTokenLifetime))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	return token, nil
}

func (m Messages) CreateRTMConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.RTMConnection, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.RTMConnection{}, err
	}
	id, err := domain.NewRTMConnectionID()
	if err != nil {
		return domain.RTMConnection{}, err
	}
	// The ticket carries the journal tail as it is at this moment, so the
	// stream opens on what happens next rather than replaying everything that
	// already happened. Taking it here — not when the socket opens — means the
	// dial is not a gap: events posted while the client connects are delivered.
	cursor, err := m.Store.LatestEventSequence(ctx, workspaceID)
	if err != nil {
		return domain.RTMConnection{}, err
	}
	value := domain.RTMConnection{ID: id, WorkspaceID: workspaceID, UserID: userID, ExpiresAt: time.Now().UTC().Add(30 * time.Second), Cursor: cursor}
	if err := m.Store.CreateRTMConnection(ctx, value); err != nil {
		return domain.RTMConnection{}, err
	}
	return value, nil
}

func (m Messages) ConsumeRTMConnection(ctx context.Context, id string) (domain.RTMConnection, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.RTMConnection{}, store.ErrNotFound
	}
	return m.Store.ConsumeRTMConnection(ctx, id)
}

func (m Messages) UserByEmail(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, email string) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.User{}, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > 320 {
		return domain.User{}, store.ErrNotFound
	}
	return m.Store.FindUserByEmail(ctx, workspaceID, email)
}

func (m Messages) SetUserProfile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, profile domain.UserProfile) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.User{}, err
	}
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	profile.StatusText = strings.TrimSpace(profile.StatusText)
	profile.StatusEmoji = strings.TrimSpace(profile.StatusEmoji)
	profile.Image24 = strings.TrimSpace(profile.Image24)
	profile.Image32 = strings.TrimSpace(profile.Image32)
	profile.Image48 = strings.TrimSpace(profile.Image48)
	profile.Image72 = strings.TrimSpace(profile.Image72)
	profile.Image192 = strings.TrimSpace(profile.Image192)
	profile.Image512 = strings.TrimSpace(profile.Image512)
	profile.Image1024 = strings.TrimSpace(profile.Image1024)
	if profile.StatusText == "" && profile.StatusEmoji == "" {
		// Slack clears a custom status only when both fields are empty. Keeping a
		// deadline attached to an already-cleared status would later publish a
		// second, invented profile change.
		profile.StatusExpiration = time.Time{}
	} else if !profile.StatusExpiration.IsZero() {
		profile.StatusExpiration = profile.StatusExpiration.UTC().Truncate(time.Second)
		if !profile.StatusExpiration.After(time.Now().UTC()) {
			return domain.User{}, ErrInvalidProfile
		}
	}
	if len(profile.DisplayName) > 80 || len(profile.StatusText) > 100 || len(profile.StatusEmoji) > 64 || len(profile.Image24) > 2048 || len(profile.Image32) > 2048 || len(profile.Image48) > 2048 || len(profile.Image72) > 2048 || len(profile.Image192) > 2048 || len(profile.Image512) > 2048 || len(profile.Image1024) > 2048 {
		return domain.User{}, ErrInvalidProfile
	}
	if err := m.validateStatusEmoji(ctx, workspaceID, profile.StatusEmoji, ErrInvalidProfile); err != nil {
		return domain.User{}, err
	}
	current, err := m.Store.GetUser(ctx, userID)
	if err != nil || current.WorkspaceID != workspaceID {
		return domain.User{}, store.ErrNotFound
	}
	statusChanged := current.Profile.StatusText != profile.StatusText ||
		current.Profile.StatusEmoji != profile.StatusEmoji ||
		!current.Profile.StatusExpiration.Equal(profile.StatusExpiration)
	updated := current
	updated.Profile = profile
	now := time.Now().UTC()
	payload, err := events.UserChangePayload("user.profile_changed", updated, false, statusChanged, now)
	if err != nil {
		return domain.User{}, err
	}
	event, err := newEvent(workspaceID, userID, payload, now)
	if err != nil {
		return domain.User{}, err
	}
	return m.Store.UpdateUserProfile(ctx, workspaceID, userID, profile, event)
}

func (m Messages) validateStatusEmoji(ctx context.Context, workspaceID domain.WorkspaceID, value string, invalid error) error {
	if value == "" {
		return nil
	}
	if !strings.HasPrefix(value, ":") || !strings.HasSuffix(value, ":") {
		return invalid
	}
	name := normalizeEmojiName(value)
	if !validEmojiName(name) {
		return invalid
	}
	if _, _, ok := slackemoji.ParseReactionName(name); ok {
		return nil
	}
	custom, err := m.Store.ListEmojis(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, candidate := range custom {
		if normalizeEmojiName(candidate.Name) == name {
			return nil
		}
	}
	return invalid
}

func normalizeScheduledStatus(statusText, statusEmoji string, startsAt, endsAt time.Time, now time.Time) (string, string, time.Time, time.Time, error) {
	statusText = strings.TrimSpace(statusText)
	statusEmoji = strings.TrimSpace(statusEmoji)
	startsAt = startsAt.UTC().Truncate(time.Second)
	endsAt = endsAt.UTC().Truncate(time.Second)
	if (statusText == "" && statusEmoji == "") || len(statusText) > 100 || len(statusEmoji) > 64 ||
		!startsAt.After(now.UTC()) || !endsAt.After(startsAt) {
		return "", "", time.Time{}, time.Time{}, ErrInvalidScheduledStatus
	}
	return statusText, statusEmoji, startsAt, endsAt, nil
}

func (m Messages) ScheduleUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, statusText, statusEmoji string, startsAt, endsAt time.Time) (domain.ScheduledStatus, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ScheduledStatus{}, err
	}
	now := time.Now().UTC()
	statusText, statusEmoji, startsAt, endsAt, err := normalizeScheduledStatus(statusText, statusEmoji, startsAt, endsAt, now)
	if err != nil {
		return domain.ScheduledStatus{}, err
	}
	if err := m.validateStatusEmoji(ctx, workspaceID, statusEmoji, ErrInvalidScheduledStatus); err != nil {
		return domain.ScheduledStatus{}, err
	}
	id, err := domain.NewScheduledStatusID()
	if err != nil {
		return domain.ScheduledStatus{}, err
	}
	value := domain.ScheduledStatus{ID: id, WorkspaceID: workspaceID, UserID: userID, StatusText: statusText, StatusEmoji: statusEmoji, StartsAt: startsAt, EndsAt: endsAt, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.CreateScheduledStatus(ctx, value); err != nil {
		if errors.Is(err, store.ErrScheduledStatusLimit) {
			return domain.ScheduledStatus{}, ErrScheduledStatusLimit
		}
		return domain.ScheduledStatus{}, err
	}
	return value, nil
}

func (m Messages) ScheduledUserStatuses(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.ScheduledStatus, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.ListScheduledStatuses(ctx, workspaceID, userID)
}

func (m Messages) UpdateScheduledUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID, statusText, statusEmoji string, startsAt, endsAt time.Time) (domain.ScheduledStatus, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ScheduledStatus{}, err
	}
	now := time.Now().UTC()
	statusText, statusEmoji, startsAt, endsAt, err := normalizeScheduledStatus(statusText, statusEmoji, startsAt, endsAt, now)
	if err != nil {
		return domain.ScheduledStatus{}, err
	}
	if err := m.validateStatusEmoji(ctx, workspaceID, statusEmoji, ErrInvalidScheduledStatus); err != nil {
		return domain.ScheduledStatus{}, err
	}
	current, err := m.Store.GetScheduledStatus(ctx, workspaceID, userID, id)
	if err != nil {
		return domain.ScheduledStatus{}, err
	}
	current.StatusText, current.StatusEmoji = statusText, statusEmoji
	current.StartsAt, current.EndsAt, current.UpdatedAt = startsAt, endsAt, now
	if err := m.Store.UpdateScheduledStatus(ctx, current); err != nil {
		return domain.ScheduledStatus{}, err
	}
	return current, nil
}

func (m Messages) DeleteScheduledUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	return m.Store.DeleteScheduledStatus(ctx, workspaceID, userID, id)
}

const maxUserPhotoBytes = 10 << 20

// userPhotoContentTypes is the closed set of types a profile photo may be. It is
// an allow-list rather than an "image/" prefix test because the prefix admits
// image/svg+xml, which is a script container, and because the DECLARED type was
// the only thing ever checked.
//
// A member could upload an HTML document declared as image/png; the public
// capability URL then served it on the application origin, the browser sniffed
// it as a document, and the script in it read the victim's CSRF token. That is a
// session takeover, and it was reproduced end to end. The serving side needs its
// own defences — nosniff, a pinned content type — but the bytes must never reach
// storage in the first place: a stored file that is a lie is a defect wherever
// it is later served from.
var userPhotoContentTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// userPhotoSniffLength is what http.DetectContentType reads. It is the whole
// algorithm's input, so nothing beyond it needs buffering.
const userPhotoSniffLength = 512

// normalizeImageContentType reduces a declared content type to the bare type,
// lower case, with the one alias clients actually send folded onto its
// registered name.
func normalizeImageContentType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	if value == "image/jpg" {
		return "image/jpeg"
	}
	return value
}

// scriptableContentTypes are the types a browser will execute or render as a
// document. A byte stream that IS one of these must never be recorded as
// anything else, whatever the uploader called it: the recorded type is what
// every API client is told and what a viewer acts on.
var scriptableContentTypes = map[string]bool{
	"text/html":       true,
	"image/svg+xml":   true,
	"text/xml":        true,
	"application/xml": true,
}

// resolveUploadContentType decides the type a stored file is RECORDED with, and
// returns a reader that still yields every byte.
//
// The declared type used to be recorded verbatim, so files.upload published
// whatever the client said. Two disagreements matter and both are resolved in
// favour of the bytes: a stream that is a scriptable document called something
// harmless, and a declared image that is not that image. Every other
// disagreement keeps the declared type, because http.DetectContentType knows a
// small closed set and reports application/octet-stream for everything else —
// overriding on that would replace good metadata with none.
func resolveUploadContentType(declared string, source io.Reader) (string, io.Reader, error) {
	head := make([]byte, userPhotoSniffLength)
	read, err := io.ReadFull(source, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", nil, err
	}
	head = head[:read]
	rest := io.MultiReader(bytes.NewReader(head), source)
	sniffed := normalizeImageContentType(http.DetectContentType(head))
	normalized := normalizeImageContentType(declared)
	switch {
	case scriptableContentTypes[sniffed] && sniffed != normalized:
		return sniffed, rest, nil
	case strings.HasPrefix(normalized, "image/") && sniffed != normalized:
		return sniffed, rest, nil
	default:
		return declared, rest, nil
	}
}

// sniffUserPhoto reads enough of the stream to identify it and returns a reader
// that still yields every byte, so the caller streams the original bytes to the
// blob store rather than a copy held in memory.
func sniffUserPhoto(declared string, source io.Reader) (io.Reader, error) {
	head := make([]byte, userPhotoSniffLength)
	read, err := io.ReadFull(source, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	head = head[:read]
	sniffed := normalizeImageContentType(http.DetectContentType(head))
	if !userPhotoContentTypes[sniffed] || sniffed != declared {
		return nil, ErrInvalidProfile
	}
	return io.MultiReader(bytes.NewReader(head), source), nil
}

func userPhotoURL(workspaceID domain.WorkspaceID, userID domain.UserID, token string) string {
	return "/users/" + string(workspaceID) + "/" + string(userID) + "/photo/" + token
}

func currentUserPhotoToken(workspaceID domain.WorkspaceID, user domain.User) string {
	prefix := "/users/" + string(workspaceID) + "/" + string(user.ID) + "/photo/"
	if strings.HasPrefix(user.Profile.Image24, prefix) {
		return strings.TrimPrefix(user.Profile.Image24, prefix)
	}
	return ""
}

func (m Messages) SetUserPhoto(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, mimeType string, size int64, source io.Reader) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.User{}, err
	}
	if m.Blob == nil {
		return domain.User{}, ErrBlobUnavailable
	}
	mimeType = normalizeImageContentType(mimeType)
	if !userPhotoContentTypes[mimeType] || size <= 0 || size > maxUserPhotoBytes || source == nil {
		return domain.User{}, ErrInvalidProfile
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.User{}, store.ErrNotFound
	}
	// The bytes decide, not the label on them.
	source, err = sniffUserPhoto(mimeType, source)
	if err != nil {
		return domain.User{}, err
	}
	token, err := domain.PublicID("photo_")
	if err != nil {
		return domain.User{}, err
	}
	key := string(workspaceID) + "/users/" + string(userID) + "/" + token
	if _, err := m.Blob.Put(ctx, key, size, source); err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return domain.User{}, ErrBlobUnavailable
		}
		return domain.User{}, err
	}
	oldToken := currentUserPhotoToken(workspaceID, user)
	photoURL := userPhotoURL(workspaceID, userID, token)
	user.Profile.Image24, user.Profile.Image32, user.Profile.Image48, user.Profile.Image72, user.Profile.Image192, user.Profile.Image512, user.Profile.Image1024 = photoURL, photoURL, photoURL, photoURL, photoURL, photoURL, photoURL
	photoPayload, err := events.UserChangePayload("user.profile_changed", user, false, false, time.Now().UTC())
	if err != nil {
		return domain.User{}, err
	}
	event, err := newEvent(workspaceID, userID, photoPayload, time.Now().UTC())
	if err != nil {
		if cleanupErr := m.Blob.Delete(context.Background(), key); cleanupErr != nil {
			return domain.User{}, errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		return domain.User{}, err
	}
	// The profile change and the instruction to reclaim the replaced photo commit
	// together. They used to be two transactions, so a crash between them left a
	// blob nothing referenced and nothing would ever delete, and a failure of the
	// second reported failure for a profile change that had already succeeded.
	written := []events.Event{event}
	if oldToken != "" {
		cleanup, cleanupErr := newEvent(workspaceID, userID, events.BlobKey(events.UserPhotoBlobDeleteTopic, string(workspaceID)+"/users/"+string(userID)+"/"+oldToken), time.Now().UTC())
		if cleanupErr != nil {
			return domain.User{}, cleanupErr
		}
		written = append(written, cleanup)
	}
	updated, err := m.Store.UpdateUserProfile(ctx, workspaceID, userID, user.Profile, written...)
	if err != nil {
		if cleanupErr := m.Blob.Delete(context.Background(), key); cleanupErr != nil {
			return domain.User{}, errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		return domain.User{}, err
	}
	return updated, nil
}

func (m Messages) OpenUserPhoto(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, token string) (domain.User, io.ReadCloser, error) {
	if m.Blob == nil {
		return domain.User{}, nil, ErrBlobUnavailable
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.User{}, nil, store.ErrNotFound
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted || currentUserPhotoToken(workspaceID, user) != token {
		return domain.User{}, nil, store.ErrNotFound
	}
	key := string(workspaceID) + "/users/" + string(userID) + "/" + token
	_, reader, err := m.Blob.Open(ctx, key)
	if errors.Is(err, blob.ErrUnavailable) {
		return domain.User{}, nil, ErrBlobUnavailable
	}
	return user, reader, err
}

func (m Messages) DeleteUserPhoto(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return store.ErrNotFound
	}
	oldToken := currentUserPhotoToken(workspaceID, user)
	user.Profile.Image24, user.Profile.Image32, user.Profile.Image48, user.Profile.Image72, user.Profile.Image192, user.Profile.Image512, user.Profile.Image1024 = "", "", "", "", "", "", ""
	photoPayload, err := events.UserChangePayload("user.profile_changed", user, false, false, time.Now().UTC())
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, photoPayload, time.Now().UTC())
	if err != nil {
		return err
	}
	// As in SetUserPhoto: the profile change and the reclamation of the removed
	// photo are one unit, so a crash cannot leave an unreferenced blob behind.
	written := []events.Event{event}
	if oldToken != "" {
		cleanup, cleanupErr := newEvent(workspaceID, userID, events.BlobKey(events.UserPhotoBlobDeleteTopic, string(workspaceID)+"/users/"+string(userID)+"/"+oldToken), time.Now().UTC())
		if cleanupErr != nil {
			return cleanupErr
		}
		written = append(written, cleanup)
	}
	_, err = m.Store.UpdateUserProfile(ctx, workspaceID, userID, user.Profile, written...)
	return err
}

func (m Messages) SetUserPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, presence domain.Presence) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.User{}, err
	}
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return domain.User{}, ErrInvalidPresence
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.presence_changed", events.String("user_id", string(userID)), events.String("presence", string(presence))), time.Now().UTC())
	if err != nil {
		return domain.User{}, err
	}
	return m.Store.SetUserPresence(ctx, workspaceID, userID, presence, event)
}

func (m Messages) DoNotDisturbInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, requestedID domain.UserID) (domain.DoNotDisturb, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.DoNotDisturb{}, err
	}
	if requestedID == "" {
		requestedID = userID
	}
	requested, err := m.Store.GetUser(ctx, requestedID)
	if err != nil || requested.WorkspaceID != workspaceID || requested.Deleted {
		return domain.DoNotDisturb{}, store.ErrNotFound
	}
	if _, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, requestedID); err != nil {
		return domain.DoNotDisturb{}, store.ErrNotFound
	}
	return m.Store.GetDoNotDisturb(ctx, workspaceID, requestedID)
}

func (m Messages) SetSnooze(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, minutes int64) (domain.DoNotDisturb, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.DoNotDisturb{}, err
	}
	if minutes < 1 || minutes > 1440 {
		return domain.DoNotDisturb{}, ErrInvalidSnooze
	}
	value, err := m.Store.GetDoNotDisturb(ctx, workspaceID, userID)
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	value.SnoozeUntil = time.Now().UTC().Truncate(time.Second).Add(time.Duration(minutes) * time.Minute)
	event, err := newEvent(workspaceID, userID, dndEventPayload("user.dnd_snoozed", userID, value, time.Now().UTC()), time.Now().UTC())
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	if err := m.Store.SetDoNotDisturb(ctx, value, event); err != nil {
		return domain.DoNotDisturb{}, err
	}
	return value, nil
}

func (m Messages) EndSnooze(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.DoNotDisturb, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.DoNotDisturb{}, err
	}
	value, err := m.Store.GetDoNotDisturb(ctx, workspaceID, userID)
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	value.SnoozeUntil = time.Time{}
	event, err := newEvent(workspaceID, userID, dndEventPayload("user.dnd_snooze_ended", userID, value, time.Now().UTC()), time.Now().UTC())
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	if err := m.Store.SetDoNotDisturb(ctx, value, event); err != nil {
		return domain.DoNotDisturb{}, err
	}
	return value, nil
}

func (m Messages) EndDND(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	value, err := m.Store.GetDoNotDisturb(ctx, workspaceID, userID)
	if err != nil {
		return err
	}
	value.Enabled = false
	value.NextStartAt = time.Time{}
	value.NextEndAt = time.Time{}
	event, err := newEvent(workspaceID, userID, dndEventPayload("user.dnd_ended", userID, value, time.Now().UTC()), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetDoNotDisturb(ctx, value, event)
}

func (m Messages) Users(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.UserPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.UserPage{}, err
	}
	return m.Store.ListUsers(ctx, workspaceID, request)
}

// SearchPeople answers the People search tab. The client used to load every
// member of the workspace and filter them in the browser, which is correct on a
// small workspace and unbounded work per request on any other; this asks the
// store the question instead, and returns a page rather than everything.
func (m Messages) SearchPeople(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.UserPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.UserPage{}, err
	}
	if strings.TrimSpace(query) == "" {
		return domain.UserPage{}, ErrInvalidSearch
	}
	return m.Store.SearchUsers(ctx, workspaceID, query, request)
}

// SearchChannels answers the Channels search tab. It is the member conversation
// listing with a query, so the visibility rule is the one the sidebar uses and a
// search cannot surface a private channel the reader is not in.
func (m Messages) SearchChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.ConversationPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPage{}, err
	}
	if strings.TrimSpace(query) == "" {
		return domain.ConversationPage{}, ErrInvalidSearch
	}
	return m.Store.ListConversations(ctx, workspaceID, userID, domain.ConversationListRequest{
		Limit: request.Limit, Cursor: request.Cursor, Query: query,
		Types: []domain.ConversationType{domain.ConversationTypePublic, domain.ConversationTypePrivate},
	})
}

func (m Messages) AdminListUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, request domain.PageRequest) (domain.AdminUserPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.AdminUserPage{}, err
	}
	return m.Store.ListAdminUsers(ctx, workspaceID, request)
}

func (m Messages) ConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) (domain.UserPage, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.UserPage{}, err
	}
	return m.Store.ListConversationMembers(ctx, conversationID, request)
}

// IsConversationMember reports the mutation authority the current user has in
// a conversation they may read. A public channel deliberately remains readable
// before joining it; callers need this separate fact so they do not advertise
// posting, reactions, pins, or unread state that the service will refuse.
func (m Messages) IsConversationMember(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (bool, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return false, err
	}
	return m.Store.IsConversationMember(ctx, conversationID, userID)
}

func (m Messages) WorkspaceInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.GetWorkspace(ctx, workspaceID)
}

func (m Messages) AuthorizedAppWorkspaces(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, request domain.PageRequest) (domain.WorkspacePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.WorkspacePage{}, err
	}
	if appID == "" || request.Limit < 1 || request.Limit > 1000 || request.Descending {
		return domain.WorkspacePage{}, store.ErrInvalidArgument
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.WorkspacePage{}, err
	}
	installations, err := m.Store.ListAppInstallations(ctx, appID)
	if err != nil {
		return domain.WorkspacePage{}, err
	}
	values := make([]domain.Workspace, 0, min(request.Limit+1, len(installations)))
	for _, installation := range installations {
		if string(installation.WorkspaceID) <= after {
			continue
		}
		value, getErr := m.Store.GetWorkspace(ctx, installation.WorkspaceID)
		if getErr != nil {
			return domain.WorkspacePage{}, getErr
		}
		values = append(values, value)
		if len(values) > request.Limit {
			break
		}
	}
	page := domain.WorkspacePage{Workspaces: values}
	if len(values) > request.Limit {
		page.HasMore = true
		page.Workspaces = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(page.Workspaces[len(page.Workspaces)-1].ID))
		if err != nil {
			return domain.WorkspacePage{}, err
		}
	}
	return page, nil
}

// SetAppIcon records what a client draws beside an app's messages. The owner
// sets it, because the icon is part of how the app presents itself.
func (m Messages) SetAppIcon(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, iconURL string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if app.OwnerID != actorID {
		if adminErr := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); adminErr != nil {
			return adminErr
		}
	}
	iconURL = strings.TrimSpace(iconURL)
	if _, err := url.ParseRequestURI(iconURL); err != nil {
		return ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("app.icon_set",
		events.String("app_id", string(appID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetAppIcon(ctx, workspaceID, appID, iconURL, event)
}

// ExternalAuthToken reports one of an app's external credentials without its
// secret. The ciphertext never leaves the store, in the same way an app's
// signing secret does not: a caller needs to know the credential exists and
// when it lapses, not what it is.
func (m Messages) ExternalAuthToken(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, id string) (domain.ExternalAuthToken, error) {
	if appID == "" || strings.TrimSpace(id) == "" {
		return domain.ExternalAuthToken{}, ErrInvalidWorkspace
	}
	value, err := m.Store.GetExternalAuthToken(ctx, workspaceID, appID, strings.TrimSpace(id))
	if err != nil {
		return domain.ExternalAuthToken{}, err
	}
	value.Ciphertext = ""
	return value, nil
}

// DeleteExternalAuthToken revokes one external credential, or every one the app
// holds when no identifier is named.
func (m Messages) DeleteExternalAuthToken(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, id string) error {
	if appID == "" {
		return ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("app.external_token_deleted",
		events.String("app_id", string(appID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteExternalAuthToken(ctx, workspaceID, appID, strings.TrimSpace(id), event)
}

// UpdateUserAppConnection records that a member has re-authorised an app. Slack
// answers ok and refreshes the connection rather than reporting one, so the
// membership check is the whole contract: a member who is not here cannot hold
// a connection to anything.
func (m Messages) UpdateUserAppConnection(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if appID == "" {
		return ErrInvalidWorkspace
	}
	installations, err := m.Store.ListAppInstallations(ctx, appID)
	if err != nil {
		return err
	}
	installed := false
	for _, installation := range installations {
		if installation.WorkspaceID == workspaceID && installation.Enabled {
			installed = true
			break
		}
	}
	if !installed {
		return store.ErrNotFound
	}
	event, eventErr := newEvent(workspaceID, actorID, events.NewPayload("app.user_connection_updated",
		events.String("app_id", string(appID))), time.Now().UTC())
	if eventErr != nil {
		return eventErr
	}
	return m.Store.AppendEvent(ctx, event)
}

// AssistantSearchAvailability reports what an assistant may search here.
func (m Messages) AssistantSearchAvailability(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (domain.AssistantSearchAvailability, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.AssistantSearchAvailability{}, err
	}
	// Messages are the one source this deployment indexes. Naming a source it
	// cannot search would promise a result it can never return.
	return domain.AssistantSearchAvailability{Enabled: true, SearchableSources: []string{"messages"}}, nil
}

// AssistantSearchContext answers the messages an assistant may quote. It is the
// member's own search, so it can never reach a conversation the member cannot
// read: an assistant answering on somebody's behalf must see what they see and
// no more.
func (m Messages) AssistantSearchContext(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	if strings.TrimSpace(query) == "" {
		return domain.MessagePage{}, ErrInvalidWorkspace
	}
	return m.SearchMessages(ctx, workspaceID, actorID, domain.MessageSearchRequest{Query: query, Page: request})
}

// AdminRequestExport records a request for a report the workspace will build
// and send. Slack acknowledges the request rather than answering the report, so
// the acknowledgement has to leave a trace: an export nobody can find later is
// the same as one that never ran.
func (m Messages) AdminRequestExport(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, kind string, bounds map[string]int64) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	fields := []events.Field{events.String("export", kind)}
	for _, name := range slices.Sorted(maps.Keys(bounds)) {
		fields = append(fields, events.Int(name, bounds[name]))
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("export.requested", fields...), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.AppendEvent(ctx, event)
}

// RequestWorkflowStepResponsesExport records a request for one step's collected
// responses. Only somebody who may manage the workflow may ask, because the
// responses are what its members submitted.
func (m Messages) RequestWorkflowStepResponsesExport(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, workflowID domain.WorkflowID, stepID string) error {
	if _, err := m.WorkflowStepResponses(ctx, workspaceID, actorID, workflowID, stepID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("export.requested",
		events.String("export", "workflow_step_responses"),
		events.String("workflow_id", string(workflowID)),
		events.String("step_id", stepID)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.AppendEvent(ctx, event)
}

// WorkflowStepResponses collects what members submitted to one step across
// every run of the workflow. The export used to acknowledge a request and
// produce nothing; this is the report it acknowledges.
func (m Messages) WorkflowStepResponses(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, workflowID domain.WorkflowID, stepID string) ([]domain.WorkflowStepResponse, error) {
	stepID = strings.TrimSpace(stepID)
	if workflowID == "" || stepID == "" {
		return nil, ErrInvalidWorkflowStep
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return nil, err
	}
	if err := m.requireWorkflowManager(ctx, workflow, actorID); err != nil {
		return nil, err
	}
	responses := make([]domain.WorkflowStepResponse, 0)
	cursor := domain.Cursor("")
	for {
		runs, more, next, err := m.Store.ListWorkflowRuns(ctx, workspaceID, workflowID, domain.PageRequest{Limit: 100, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			executions, err := m.Store.ListWorkflowRunSteps(ctx, workspaceID, run.ID)
			if err != nil {
				return nil, err
			}
			for _, execution := range executions {
				// EditID carries which step of the definition this execution
				// is, which is how one step's answers are picked out of every
				// run's executions.
				if execution.EditID != stepID {
					continue
				}
				responses = append(responses, domain.WorkflowStepResponse{
					RunID: run.ID, StepID: execution.ID, ActorID: execution.UserID,
					Status: execution.Status, Outputs: execution.Outputs, CompletedAt: execution.UpdatedAt,
				})
			}
		}
		if !more || next == "" {
			break
		}
		cursor = next
	}
	return responses, nil
}

// AdminAnomalyAllowList reports what audit is told not to flag.
func (m Messages) AdminAnomalyAllowList(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (domain.AnomalyAllowList, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AnomalyAllowList{}, err
	}
	return m.Store.GetAnomalyAllowList(ctx, workspaceID)
}

// AdminSetAnomalyAllowList replaces the allow list. An address without a reason
// is refused: an exclusion nobody explained is one nobody can review later.
func (m Messages) AdminSetAnomalyAllowList(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, addresses, reasons []string) (domain.AnomalyAllowList, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AnomalyAllowList{}, err
	}
	if len(addresses) > 0 && len(reasons) == 0 {
		return domain.AnomalyAllowList{}, ErrInvalidWorkspace
	}
	for _, address := range addresses {
		if net.ParseIP(strings.TrimSpace(address)) == nil {
			if _, _, err := net.ParseCIDR(strings.TrimSpace(address)); err != nil {
				return domain.AnomalyAllowList{}, ErrInvalidWorkspace
			}
		}
	}
	value := domain.AnomalyAllowList{
		WorkspaceID: workspaceID, IPAddresses: append([]string{}, addresses...),
		Reasons: append([]string{}, reasons...), UpdatedAt: time.Now().UTC(),
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("audit.anomaly_allow_list_set",
		events.Int("addresses", int64(len(value.IPAddresses)))), value.UpdatedAt)
	if err != nil {
		return domain.AnomalyAllowList{}, err
	}
	if err := m.Store.SetAnomalyAllowList(ctx, value, event); err != nil {
		return domain.AnomalyAllowList{}, err
	}
	return value, nil
}

// TeamBillingInfo reports which plan the workspace is on.
func (m Messages) TeamBillingInfo(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (domain.WorkspacePlan, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return "", err
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return workspace.Plan, nil
}

// AdminAnalytics reports one day of analytics. The rows are computed from the
// messages and reactions the day holds rather than read from a nightly
// aggregate: a stored aggregate is a second copy of the truth, and the two
// disagree the first time a message is deleted.
func (m Messages) AdminAnalytics(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, kind domain.AnalyticsKind, day time.Time) ([]domain.AnalyticsRow, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if !kind.Valid() || day.IsZero() {
		return nil, ErrInvalidWorkspace
	}
	return m.Store.AnalyticsRows(ctx, workspaceID, kind, day)
}

// AppActivities reports one app's activity log to that app. An app reads only
// its own entries, so the caller's identity decides the filter rather than an
// argument it could set to somebody else's.
func (m Messages) AppActivities(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, filter domain.AppActivityFilter, request domain.PageRequest) (domain.AppActivityPage, error) {
	if appID == "" {
		return domain.AppActivityPage{}, ErrInvalidWorkspace
	}
	filter.AppID = appID
	return m.Store.ListAppActivities(ctx, workspaceID, filter, request)
}

// AdminAppActivities reports any app's activity log to an administrator.
func (m Messages) AdminAppActivities(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, filter domain.AppActivityFilter, request domain.PageRequest) (domain.AppActivityPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AppActivityPage{}, err
	}
	return m.Store.ListAppActivities(ctx, workspaceID, filter, request)
}

// AdminLookupConversations finds channels an administrator is looking for. A
// filter left at its zero value is not applied, so a lookup that names nothing
// answers every channel rather than none.
func (m Messages) AdminLookupConversations(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, lookup domain.ConversationLookup, request domain.PageRequest) (domain.ConversationPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.ConversationPage{}, err
	}
	return m.Store.LookupConversations(ctx, workspaceID, lookup, request)
}

// AdminBulkMoveConversations reassigns channels to another workspace. Every
// channel is checked before one moves, so a request naming a channel that is
// not here moves nothing.
func (m Messages) AdminBulkMoveConversations(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID, target domain.WorkspaceID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(ids) == 0 || target == "" {
		return ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("channel.bulk_moved",
		events.String("target_team_id", string(target)), events.Int("channels", int64(len(ids)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.MoveConversations(ctx, workspaceID, ids, target, event)
}

// AdminSetConversationsExcludedFromAI keeps channels in or out of the
// workspace's generative features.
func (m Messages) AdminSetConversationsExcludedFromAI(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID, excluded bool) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("channel.ai_exclusion_set",
		events.Int("channels", int64(len(ids)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetConversationsExcludedFromAI(ctx, workspaceID, ids, excluded, event)
}

// AdminConversationsExcludedFromAI reports which of the named channels are out.
func (m Messages) AdminConversationsExcludedFromAI(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID) ([]domain.ConversationID, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrInvalidConversation
	}
	return m.Store.ConversationsExcludedFromAI(ctx, workspaceID, ids)
}

// AdminLinkConversationObjects links one channel to external records.
func (m Messages) AdminLinkConversationObjects(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.ConversationID, orgID string, recordIDs []string) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	orgID = strings.TrimSpace(orgID)
	if id == "" || orgID == "" || len(recordIDs) == 0 {
		return ErrInvalidConversation
	}
	now := time.Now().UTC()
	objects := make([]domain.LinkedObject, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		recordID = strings.TrimSpace(recordID)
		if recordID == "" {
			return ErrInvalidConversation
		}
		objects = append(objects, domain.LinkedObject{
			ConversationID: id, WorkspaceID: workspaceID, OrgID: orgID, RecordID: recordID, CreatedAt: now,
		})
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("channel.objects_linked",
		events.String("channel", string(id)), events.Int("records", int64(len(objects)))), now)
	if err != nil {
		return err
	}
	return m.Store.LinkConversationObjects(ctx, objects, event)
}

// AdminUnlinkConversationObjects removes every link the named channels hold.
func (m Messages) AdminUnlinkConversationObjects(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("channel.objects_unlinked",
		events.Int("channels", int64(len(ids)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.UnlinkConversationObjects(ctx, workspaceID, ids, event)
}

// AdminConversationObjects reports the records one channel is linked to.
func (m Messages) AdminConversationObjects(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.ConversationID) ([]domain.LinkedObject, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, ErrInvalidConversation
	}
	return m.Store.ListConversationObjects(ctx, workspaceID, id)
}

// AdminCreateConversationForObjects creates a channel and links it to an
// external record in one step. The link is written after the channel exists, so
// a link that cannot be stored leaves no channel behind either.
func (m Messages) AdminCreateConversationForObjects(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, name, orgID, recordID string, private bool) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	orgID, recordID = strings.TrimSpace(orgID), strings.TrimSpace(recordID)
	if orgID == "" || recordID == "" {
		return domain.Conversation{}, ErrInvalidConversation
	}
	conversation, err := m.CreateConversation(ctx, workspaceID, actorID, name, private)
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.AdminLinkConversationObjects(ctx, workspaceID, actorID, conversation.ID, orgID, []string{recordID}); err != nil {
		if deleteErr := m.Store.DeleteConversation(ctx, workspaceID, conversation.ID, events.Event{
			ID: domain.EventID("evt_unlinked_" + string(conversation.ID)), WorkspaceID: workspaceID, ActorID: actorID,
			Topic: "channel.deleted", Payload: string(conversation.ID), CreatedAt: time.Now().UTC(),
		}); deleteErr != nil {
			return domain.Conversation{}, deleteErr
		}
		return domain.Conversation{}, err
	}
	return conversation, nil
}

// AdminAppConfigs reports the administrative configuration of the named apps.
// An app nobody has configured answers Slack's defaults rather than being left
// out, so the caller learns the effective answer for every app it named.
func (m Messages) AdminAppConfigs(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appIDs []domain.AppID) ([]domain.AppConfig, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if len(appIDs) == 0 {
		return nil, ErrInvalidWorkspace
	}
	stored, err := m.Store.ListAppConfigs(ctx, workspaceID, appIDs)
	if err != nil {
		return nil, err
	}
	held := make(map[domain.AppID]domain.AppConfig, len(stored))
	for _, config := range stored {
		held[config.AppID] = config
	}
	configs := make([]domain.AppConfig, 0, len(appIDs))
	for _, appID := range appIDs {
		if config, exists := held[appID]; exists {
			configs = append(configs, config)
			continue
		}
		configs = append(configs, domain.AppConfig{
			AppID: appID, WorkspaceID: workspaceID,
			DomainURLs: []string{}, DomainEmails: []string{},
			WorkflowAuthStrategy: domain.WorkflowAuthBuilderChoice,
		})
	}
	return configs, nil
}

// AdminSetAppConfig writes one app's administrative configuration.
func (m Messages) AdminSetAppConfig(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, config domain.AppConfig) (domain.AppConfig, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AppConfig{}, err
	}
	if config.AppID == "" || !config.WorkflowAuthStrategy.Valid() {
		return domain.AppConfig{}, ErrInvalidWorkspace
	}
	if config.DomainURLs == nil {
		config.DomainURLs = []string{}
	}
	if config.DomainEmails == nil {
		config.DomainEmails = []string{}
	}
	config.WorkspaceID, config.UpdatedAt = workspaceID, time.Now().UTC()
	event, err := newEvent(workspaceID, actorID, events.NewPayload("app.config_set",
		events.String("app_id", string(config.AppID)),
		events.String("workflow_auth_strategy", string(config.WorkflowAuthStrategy))), config.UpdatedAt)
	if err != nil {
		return domain.AppConfig{}, err
	}
	if err := m.Store.SetAppConfig(ctx, config, event); err != nil {
		return domain.AppConfig{}, err
	}
	return config, nil
}

// AdminClearAppResolution undoes an approval decision, so the app is undecided
// again rather than approved or restricted.
func (m Messages) AdminClearAppResolution(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if appID == "" {
		return ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("app.resolution_cleared",
		events.String("app_id", string(appID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.ClearAppApproval(ctx, workspaceID, appID, event)
}

// AdminFunctionPermissions reports who may run each named function. A function
// with no stored permission answers Slack's default rather than being left out,
// so the caller learns the effective answer for every identifier it named.
func (m Messages) AdminFunctionPermissions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, functionIDs []string) ([]domain.AutomationPermission, error) {
	return m.adminAutomationPermissions(ctx, workspaceID, actorID, "function", functionIDs, domain.PermissionEveryone)
}

// AdminWorkflowPermissions reports who may run each named workflow.
func (m Messages) AdminWorkflowPermissions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, workflowIDs []domain.WorkflowID) ([]domain.AutomationPermission, error) {
	ids := make([]string, 0, len(workflowIDs))
	for _, id := range workflowIDs {
		ids = append(ids, string(id))
	}
	return m.adminAutomationPermissions(ctx, workspaceID, actorID, "workflow_use", ids, domain.PermissionEveryone)
}

// AdminTriggerTypePermission reports who may build triggers of one type.
func (m Messages) AdminTriggerTypePermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, kind domain.WorkflowTriggerType) (domain.AutomationPermission, error) {
	if !kind.Valid() {
		return domain.AutomationPermission{}, ErrInvalidTriggerConfig
	}
	values, err := m.adminAutomationPermissions(ctx, workspaceID, actorID, "trigger_type", []string{string(kind)}, domain.PermissionEveryone)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	return values[0], nil
}

// AdminSetFunctionPermission decides who may run one function.
func (m Messages) AdminSetFunctionPermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, functionID string, value domain.AutomationPermission) (domain.AutomationPermission, error) {
	return m.adminSetAutomationPermission(ctx, workspaceID, actorID, "function", strings.TrimSpace(functionID), value)
}

// AdminSetTriggerTypePermission decides who may build triggers of one type.
func (m Messages) AdminSetTriggerTypePermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, kind domain.WorkflowTriggerType, value domain.AutomationPermission) (domain.AutomationPermission, error) {
	if !kind.Valid() {
		return domain.AutomationPermission{}, ErrInvalidTriggerConfig
	}
	return m.adminSetAutomationPermission(ctx, workspaceID, actorID, "trigger_type", string(kind), value)
}

func (m Messages) adminAutomationPermissions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, resourceType string, ids []string, fallback domain.PermissionType) ([]domain.AutomationPermission, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrInvalidWorkflowStep
	}
	values := make([]domain.AutomationPermission, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, ErrInvalidWorkflowStep
		}
		value, err := m.Store.GetAutomationPermission(ctx, workspaceID, resourceType, id)
		if errors.Is(err, store.ErrNotFound) {
			value = domain.AutomationPermission{
				ResourceType: resourceType, ResourceID: id, WorkspaceID: workspaceID, PermissionType: fallback,
			}
		} else if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (m Messages) adminSetAutomationPermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, resourceType, resourceID string, value domain.AutomationPermission) (domain.AutomationPermission, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AutomationPermission{}, err
	}
	if resourceID == "" || !value.PermissionType.SettableBy() {
		return domain.AutomationPermission{}, ErrInvalidWorkflowStep
	}
	value.ResourceType, value.ResourceID, value.WorkspaceID = resourceType, resourceID, workspaceID
	value.UpdatedAt = time.Now().UTC()
	if err := m.validateAutomationEntities(ctx, &value); err != nil {
		return domain.AutomationPermission{}, err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("automation.permission_set",
		events.String("resource_type", resourceType), events.String("resource_id", resourceID),
		events.String("permission_type", string(value.PermissionType))), value.UpdatedAt)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if err := m.Store.SetAutomationPermission(ctx, value, event); err != nil {
		return domain.AutomationPermission{}, err
	}
	return value, nil
}

// AdminCreateBarrier builds an information barrier. Every named group is
// checked before the barrier is stored, so a barrier never names a group that
// does not exist and therefore stops nothing.
func (m Messages) AdminCreateBarrier(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, primary domain.UserGroupID, barrieredFrom []domain.UserGroupID, subjects []domain.BarrierSubject) (domain.InformationBarrier, error) {
	barrier, err := m.barrierValue(ctx, workspaceID, actorID, "", primary, barrieredFrom, subjects)
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	id, err := domain.NewBarrierID()
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	barrier.ID = id
	event, err := newEvent(workspaceID, actorID, events.NewPayload("barrier.created", events.String("barrier_id", string(id))), barrier.UpdatedAt)
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	if err := m.Store.CreateBarrier(ctx, barrier, event); err != nil {
		return domain.InformationBarrier{}, err
	}
	return barrier, nil
}

// AdminUpdateBarrier replaces the groups and subjects one barrier holds.
func (m Messages) AdminUpdateBarrier(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.BarrierID, primary domain.UserGroupID, barrieredFrom []domain.UserGroupID, subjects []domain.BarrierSubject) (domain.InformationBarrier, error) {
	barrier, err := m.barrierValue(ctx, workspaceID, actorID, id, primary, barrieredFrom, subjects)
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("barrier.updated", events.String("barrier_id", string(id))), barrier.UpdatedAt)
	if err != nil {
		return domain.InformationBarrier{}, err
	}
	if err := m.Store.UpdateBarrier(ctx, barrier, event); err != nil {
		return domain.InformationBarrier{}, err
	}
	return barrier, nil
}

// AdminDeleteBarrier removes one barrier.
func (m Messages) AdminDeleteBarrier(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.BarrierID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if strings.TrimSpace(string(id)) == "" {
		return ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("barrier.deleted", events.String("barrier_id", string(id))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteBarrier(ctx, workspaceID, id, event)
}

// AdminBarriers reports the workspace's barriers.
func (m Messages) AdminBarriers(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, request domain.PageRequest) (domain.InformationBarrierPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.InformationBarrierPage{}, err
	}
	return m.Store.ListBarriers(ctx, workspaceID, request)
}

func (m Messages) barrierValue(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.BarrierID, primary domain.UserGroupID, barrieredFrom []domain.UserGroupID, subjects []domain.BarrierSubject) (domain.InformationBarrier, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.InformationBarrier{}, err
	}
	if primary == "" || len(barrieredFrom) == 0 || !domain.ValidBarrierSubjects(subjects) {
		return domain.InformationBarrier{}, ErrInvalidUserGroup
	}
	if _, err := m.Store.GetUserGroup(ctx, workspaceID, primary); err != nil {
		return domain.InformationBarrier{}, err
	}
	seen := make(map[domain.UserGroupID]struct{}, len(barrieredFrom))
	groups := make([]domain.UserGroupID, 0, len(barrieredFrom))
	for _, group := range barrieredFrom {
		// A group barriered from itself would stop the group reaching itself,
		// which is not a barrier and which no administrator means.
		if group == primary {
			return domain.InformationBarrier{}, ErrInvalidUserGroup
		}
		if _, repeated := seen[group]; repeated {
			continue
		}
		if _, err := m.Store.GetUserGroup(ctx, workspaceID, group); err != nil {
			return domain.InformationBarrier{}, err
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return domain.InformationBarrier{}, ErrInvalidUserGroup
	}
	return domain.InformationBarrier{
		ID: id, WorkspaceID: workspaceID, PrimaryGroupID: primary,
		BarrieredFromIDs: groups, Subjects: domain.BarrierSubjects(), UpdatedAt: time.Now().UTC(),
	}, nil
}

// AdminSetSessionSettings writes session settings for the named members. A
// duration Slack refuses is refused here, so a caller never stores a setting
// that silently becomes something else.
func (m Messages) AdminSetSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targets []domain.UserID, settings domain.SessionSettings) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(targets) == 0 || !settings.Duration.Valid() {
		return ErrInvalidWorkspace
	}
	now := time.Now().UTC()
	values := make([]domain.SessionSettings, 0, len(targets))
	for _, target := range targets {
		if _, err := m.activeWorkspaceMembership(ctx, workspaceID, target); err != nil {
			return store.ErrNotFound
		}
		value := settings
		value.UserID, value.WorkspaceID, value.UpdatedAt = target, workspaceID, now
		values = append(values, value)
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.session_settings_set",
		events.Int("members", int64(len(values)))), now)
	if err != nil {
		return err
	}
	return m.Store.SetSessionSettings(ctx, values, event)
}

// AdminClearSessionSettings puts the named members back on the workspace
// default.
func (m Messages) AdminClearSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targets []domain.UserID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if len(targets) == 0 {
		return ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.session_settings_cleared",
		events.Int("members", int64(len(targets)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.ClearSessionSettings(ctx, workspaceID, targets, event)
}

// AdminSessionSettings reports the settings the named members hold.
func (m Messages) AdminSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targets []domain.UserID) ([]domain.SessionSettings, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, ErrInvalidWorkspace
	}
	return m.Store.ListSessionSettings(ctx, workspaceID, targets)
}

// MemberSessionSettings reports the settings that govern one member's own
// sessions.
//
// This exists because the sign-in paths need the policy and are not
// administrators: a member signing in reads their own setting to decide how
// long the session they are being given lives. AdminSessionSettings cannot
// serve them, so before this method the policy had no reader at all and the
// duration an administrator set decided nothing.
//
// The member reads their own and nobody else's. An administrator reading
// another member's already has AdminSessionSettings, and widening this one to
// take a target would put a second authority rule on the same data.
//
// A member with no setting is not an error: the absence is the workspace
// default, so an empty value is returned and domain.SessionSettings.Lifetime
// resolves it.
func (m Messages) MemberSessionSettings(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.SessionSettings, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.SessionSettings{}, err
	}
	settings, err := m.Store.ListSessionSettings(ctx, workspaceID, []domain.UserID{userID})
	if err != nil {
		return domain.SessionSettings{}, err
	}
	if len(settings) == 0 {
		return domain.SessionSettings{WorkspaceID: workspaceID, UserID: userID}, nil
	}
	return settings[0], nil
}

// AdminAssignAuthPolicy puts members under an authentication policy. Every
// named member is checked before one row is written, so a request that names
// somebody outside the workspace leaves nothing behind.
func (m Messages) AdminAssignAuthPolicy(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, entityIDs []string) error {
	entities, err := m.authPolicyEntities(ctx, workspaceID, actorID, policy, kind, entityIDs)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("auth.policy_entities_assigned",
		events.String("policy_name", string(policy)), events.Int("entities", int64(len(entities)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetAuthPolicyEntities(ctx, entities, event)
}

// AdminRemoveAuthPolicyEntities takes members back out of the policy.
func (m Messages) AdminRemoveAuthPolicyEntities(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, entityIDs []string) error {
	entities, err := m.authPolicyEntities(ctx, workspaceID, actorID, policy, kind, entityIDs)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("auth.policy_entities_removed",
		events.String("policy_name", string(policy)), events.Int("entities", int64(len(entities)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteAuthPolicyEntities(ctx, entities, event)
}

// AdminAuthPolicyEntities reports who is under one policy.
func (m Messages) AdminAuthPolicyEntities(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, request domain.PageRequest) (domain.AuthPolicyEntityPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AuthPolicyEntityPage{}, err
	}
	if !policy.Valid() || !kind.Valid() {
		return domain.AuthPolicyEntityPage{}, ErrInvalidWorkspace
	}
	return m.Store.ListAuthPolicyEntities(ctx, workspaceID, policy, kind, request)
}

func (m Messages) authPolicyEntities(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, policy domain.AuthPolicyName, kind domain.PolicyEntityType, entityIDs []string) ([]domain.AuthPolicyEntity, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if !policy.Valid() || !kind.Valid() || len(entityIDs) == 0 {
		return nil, ErrInvalidWorkspace
	}
	now := time.Now().UTC()
	entities := make([]domain.AuthPolicyEntity, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			return nil, ErrInvalidWorkspace
		}
		// The only entity type Slack defines is the member, and the storage
		// carries a foreign key to users, so an unknown identifier is refused
		// here rather than becoming a constraint violation at commit.
		if _, err := m.activeWorkspaceMembership(ctx, workspaceID, domain.UserID(entityID)); err != nil {
			return nil, store.ErrNotFound
		}
		entities = append(entities, domain.AuthPolicyEntity{
			Policy: policy, EntityType: kind, EntityID: entityID, WorkspaceID: workspaceID, CreatedAt: now,
		})
	}
	return entities, nil
}

// AdminAddRoleAssignments gives members a system role over entities. The
// service checks every member before it writes one row, so an administrator who
// names somebody outside the workspace learns it before the request lands.
func (m Messages) AdminAddRoleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, roleID string, entityIDs []string, userIDs []domain.UserID) error {
	assignments, err := m.roleAssignments(ctx, workspaceID, actorID, roleID, entityIDs, userIDs)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("role.assignments_added",
		events.String("role_id", roleID), events.Int("assignments", int64(len(assignments)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetRoleAssignments(ctx, assignments, event)
}

// AdminRemoveRoleAssignments takes the role away again.
func (m Messages) AdminRemoveRoleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, roleID string, entityIDs []string, userIDs []domain.UserID) error {
	assignments, err := m.roleAssignments(ctx, workspaceID, actorID, roleID, entityIDs, userIDs)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("role.assignments_removed",
		events.String("role_id", roleID), events.Int("assignments", int64(len(assignments)))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteRoleAssignments(ctx, assignments, event)
}

// AdminListRoleAssignments reports who holds one role.
func (m Messages) AdminListRoleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, roleID string, request domain.PageRequest) (domain.RoleAssignmentPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.RoleAssignmentPage{}, err
	}
	return m.Store.ListRoleAssignments(ctx, workspaceID, strings.TrimSpace(roleID), request)
}

func (m Messages) roleAssignments(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, roleID string, entityIDs []string, userIDs []domain.UserID) ([]domain.RoleAssignment, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	roleID = strings.TrimSpace(roleID)
	if roleID == "" || len(entityIDs) == 0 || len(userIDs) == 0 {
		return nil, ErrInvalidWorkspace
	}
	for _, userID := range userIDs {
		if _, err := m.activeWorkspaceMembership(ctx, workspaceID, userID); err != nil {
			return nil, store.ErrNotFound
		}
	}
	now := time.Now().UTC()
	assignments := make([]domain.RoleAssignment, 0, len(entityIDs)*len(userIDs))
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			return nil, ErrInvalidWorkspace
		}
		for _, userID := range userIDs {
			assignments = append(assignments, domain.RoleAssignment{
				RoleID: roleID, EntityID: entityID, UserID: userID, WorkspaceID: workspaceID, CreatedAt: now,
			})
		}
	}
	return assignments, nil
}

// DiscoverableContacts reports which of the named email addresses belong to a
// member this workspace lets others find. A workspace that is not discoverable
// answers no contacts, whatever the addresses match.
func (m Messages) DiscoverableContacts(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, emails []string) ([]domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if len(emails) == 0 {
		return nil, ErrInvalidWorkspace
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if workspace.Discoverability != domain.WorkspaceDiscoverabilityOpen {
		return []domain.User{}, nil
	}
	found := make([]domain.User, 0, len(emails))
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		user, err := m.Store.FindUserByEmail(ctx, workspaceID, email)
		if err != nil {
			continue
		}
		if user.Deleted {
			continue
		}
		found = append(found, user)
	}
	return found, nil
}

func (m Messages) AdminCreateWorkspace(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, domainName, name, description string, discoverability domain.WorkspaceDiscoverability) (domain.Workspace, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if domainName == "" || name == "" {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	if discoverability == "" {
		discoverability = domain.WorkspaceDiscoverabilityOpen
	}
	if !discoverability.Valid() {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	id, err := domain.NewWorkspaceID()
	if err != nil {
		return domain.Workspace{}, err
	}
	value := domain.Workspace{ID: id, Domain: domainName, Name: name, Description: description, Discoverability: discoverability}
	event, err := newEvent(id, actor, events.NewPayload("workspace.created", events.String("workspace_id", string(id)), events.String("domain", domainName)), time.Now().UTC())
	if err != nil {
		return domain.Workspace{}, err
	}
	if err := m.Store.CreateWorkspace(ctx, value, event); err != nil {
		return domain.Workspace{}, err
	}
	return value, nil
}

// TeamBillableInfo answers team.billableInfo, which discloses the full workspace
// roster and each member's billing-active state.
//
// Authority: requireWorkspaceAdmin. It gated on workspace membership, so any
// member could read the whole directory plus a billing fact about every other
// member, and the unbounded form walked the workspace with a membership read per
// user and accumulated the whole result in memory.
func (m Messages) TeamBillableInfo(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) (domain.BillableInfo, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.BillableInfo{}, err
	}
	if targetID != "" {
		user, err := m.Store.GetUser(ctx, targetID)
		if err != nil || user.WorkspaceID != workspaceID {
			return domain.BillableInfo{}, store.ErrNotFound
		}
		membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, targetID)
		if err != nil {
			return domain.BillableInfo{}, err
		}
		return domain.BillableInfo{Users: []domain.BillableUser{{UserID: targetID, BillingActive: membership.Active && !user.Deleted}}}, nil
	}
	result := domain.BillableInfo{Users: make([]domain.BillableUser, 0)}
	request := domain.PageRequest{Limit: 200}
	for {
		page, err := m.Store.ListUsers(ctx, workspaceID, request)
		if err != nil {
			return domain.BillableInfo{}, err
		}
		for _, user := range page.Users {
			membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, user.ID)
			if err != nil {
				return domain.BillableInfo{}, err
			}
			result.Users = append(result.Users, domain.BillableUser{UserID: user.ID, BillingActive: membership.Active && !user.Deleted})
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			return result, nil
		}
		request.Cursor = page.NextCursor
	}
}

func (m Messages) Conversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ConversationListRequest) (domain.ConversationPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPage{}, err
	}
	if err := domain.ValidateConversationTypes(request.Types); err != nil {
		return domain.ConversationPage{}, err
	}
	if request.MemberUserID != "" {
		member, err := m.Store.GetUser(ctx, request.MemberUserID)
		if err != nil || member.WorkspaceID != workspaceID {
			return domain.ConversationPage{}, store.ErrNotFound
		}
	}
	return m.Store.ListConversations(ctx, workspaceID, userID, request)
}

// barrierSeparates reports whether an information barrier stops any two of the
// named members from reaching each other. A barrier that is stored and never
// consulted is a setting an administrator believes in and the product ignores,
// which is worse than not offering it.
//
// The read is deliberately direct rather than cached: a workspace holds a
// handful of barriers, the groups are small, and a stale cache here would let
// through exactly the contact the barrier exists to stop.
func (m Messages) barrierSeparates(ctx context.Context, workspaceID domain.WorkspaceID, members []domain.UserID, subject domain.BarrierSubject) (bool, error) {
	if len(members) < 2 {
		return false, nil
	}
	page, err := m.Store.ListBarriers(ctx, workspaceID, domain.PageRequest{Limit: barrierReadCeiling})
	if err != nil {
		return false, err
	}
	present := make(map[domain.UserID]struct{}, len(members))
	for _, member := range members {
		present[member] = struct{}{}
	}
	for _, barrier := range page.Barriers {
		if !slices.Contains(barrier.Subjects, subject) {
			continue
		}
		primary, err := m.groupHolds(ctx, workspaceID, barrier.PrimaryGroupID, present)
		if err != nil {
			return false, err
		}
		if !primary {
			continue
		}
		for _, group := range barrier.BarrieredFromIDs {
			barriered, err := m.groupHolds(ctx, workspaceID, group, present)
			if err != nil {
				return false, err
			}
			if barriered {
				return true, nil
			}
		}
	}
	return false, nil
}

// barrierReadCeiling bounds the barriers one check reads. A workspace with more
// than this many is beyond what this check was designed for, and answering from
// a partial list would silently let contact through.
const barrierReadCeiling = 200

func (m Messages) groupHolds(ctx context.Context, workspaceID domain.WorkspaceID, groupID domain.UserGroupID, members map[domain.UserID]struct{}) (bool, error) {
	group, err := m.Store.GetUserGroup(ctx, workspaceID, groupID)
	if err != nil {
		// A barrier naming a group that has gone is not a reason to allow
		// contact the barrier was built to stop, but it is also not this
		// caller's failure to report. It holds nobody, so it separates nobody.
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, member := range group.Users {
		if _, present := members[member]; present {
			return true, nil
		}
	}
	return false, nil
}

func (m Messages) OpenConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, users []domain.UserID) (domain.Conversation, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	seen := map[domain.UserID]struct{}{userID: {}}
	for _, candidate := range users {
		candidate = domain.UserID(strings.TrimSpace(string(candidate)))
		if candidate == "" {
			return domain.Conversation{}, ErrInvalidConversation
		}
		seen[candidate] = struct{}{}
	}
	members := make([]domain.UserID, 0, len(seen))
	for candidate := range seen {
		member, err := m.Store.GetUser(ctx, candidate)
		if err != nil || member.WorkspaceID != workspaceID || member.Deleted {
			return domain.Conversation{}, store.ErrNotFound
		}
		if _, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, candidate); err != nil {
			return domain.Conversation{}, store.ErrNotFound
		}
		members = append(members, candidate)
	}
	sort.Slice(members, func(left, right int) bool { return members[left] < members[right] })
	// Slack's current product contract allows a DM to contain up to nine
	// people total, including the caller.
	if len(members) < 2 || len(members) > 9 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	// Two people or nine, the barrier subject is the kind of conversation this
	// would be.
	subject := domain.BarrierSubjectDirect
	if len(members) > 2 {
		subject = domain.BarrierSubjectGroupDirect
	}
	separated, err := m.barrierSeparates(ctx, workspaceID, members, subject)
	if err != nil {
		return domain.Conversation{}, err
	}
	if separated {
		return domain.Conversation{}, ErrBarrieredFromMember
	}
	if existing, err := m.Store.FindDirectConversation(ctx, workspaceID, members); err == nil {
		event, eventErr := newEvent(workspaceID, userID, events.NewPayload("conversation.direct_opened", events.String("channel_id", string(existing.ID))), time.Now().UTC())
		if eventErr != nil {
			return domain.Conversation{}, eventErr
		}
		if _, openErr := m.Store.SetDirectConversationOpen(ctx, workspaceID, userID, existing.ID, true, event); openErr != nil {
			return domain.Conversation{}, openErr
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Conversation{}, err
	}
	id, err := domain.NewConversationID()
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation := domain.Conversation{ID: id, WorkspaceID: workspaceID, Name: "direct", Kind: domain.ConversationKindFor(true, len(members) == 2, len(members) > 2)}
	event, err := conversationLifecycleEvent(workspaceID, "conversation.direct_created", conversation, userID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.Store.CreateDirectConversation(ctx, conversation, members, event); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return m.Store.FindDirectConversation(ctx, workspaceID, members)
		}
		return domain.Conversation{}, err
	}
	return conversation, nil
}

// AddPeopleToDirectConversation implements Slack's first-party DM transition.
// It is intentionally not exposed as a conversations.* Web API method: Slack
// documents this as a client journey, while conversations.open only opens the
// canonical exact participant set and has no history-selection argument.
func (m Messages) AddPeopleToDirectConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, sourceID domain.ConversationID, additions []domain.UserID, history domain.DirectHistorySelection) (domain.Conversation, error) {
	if !history.Valid() || len(additions) == 0 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, sourceID); err != nil {
		return domain.Conversation{}, err
	}
	source, err := m.Store.GetConversation(ctx, sourceID)
	if err != nil || source.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if !source.IsDirectOrGroup() {
		return domain.Conversation{}, ErrInvalidConversation
	}
	page, err := m.Store.ListConversationMembers(ctx, sourceID, domain.PageRequest{Limit: 20})
	if err != nil {
		return domain.Conversation{}, err
	}
	if page.HasMore {
		return domain.Conversation{}, ErrInvalidConversation
	}
	members := make(map[domain.UserID]struct{}, len(page.Users)+len(additions))
	for _, member := range page.Users {
		members[member.ID] = struct{}{}
	}
	added := make([]domain.UserID, 0, len(additions))
	for _, candidate := range additions {
		candidate = domain.UserID(strings.TrimSpace(string(candidate)))
		if candidate == "" {
			return domain.Conversation{}, ErrInvalidConversation
		}
		if _, exists := members[candidate]; exists {
			continue
		}
		user, err := m.Store.GetUser(ctx, candidate)
		if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
			return domain.Conversation{}, store.ErrNotFound
		}
		if _, err := m.activeWorkspaceMembership(ctx, workspaceID, candidate); err != nil {
			return domain.Conversation{}, store.ErrNotFound
		}
		members[candidate] = struct{}{}
		added = append(added, candidate)
	}
	if len(added) == 0 || len(members) > 9 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	memberIDs := make([]domain.UserID, 0, len(members))
	for member := range members {
		memberIDs = append(memberIDs, member)
	}
	sort.Slice(memberIDs, func(left, right int) bool { return memberIDs[left] < memberIDs[right] })
	sort.Slice(added, func(left, right int) bool { return added[left] < added[right] })
	if existing, err := m.Store.FindDirectConversation(ctx, workspaceID, memberIDs); err == nil {
		openEvent, eventErr := newEvent(workspaceID, userID, events.NewPayload("conversation.direct_opened", events.String("channel_id", string(existing.ID))), time.Now().UTC())
		if eventErr != nil {
			return domain.Conversation{}, eventErr
		}
		if _, eventErr = m.Store.SetDirectConversationOpen(ctx, workspaceID, userID, existing.ID, true, openEvent); eventErr != nil {
			return domain.Conversation{}, eventErr
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Conversation{}, err
	}
	targetID, err := domain.NewConversationID()
	if err != nil {
		return domain.Conversation{}, err
	}
	target := domain.Conversation{ID: targetID, WorkspaceID: workspaceID, Name: "direct", Kind: domain.ConversationTypeMPIM}
	now := time.Now().UTC()
	sourceNoticeID, err := domain.NewMessageID()
	if err != nil {
		return domain.Conversation{}, err
	}
	targetNoticeID, err := domain.NewMessageID()
	if err != nil {
		return domain.Conversation{}, err
	}
	addedMentions := make([]string, len(added))
	for index, member := range added {
		addedMentions[index] = "<@" + string(member) + ">"
	}
	names := strings.Join(addedMentions, ", ")
	sourceNotice := domain.Message{ID: sourceNoticeID, WorkspaceID: workspaceID, Conversation: sourceID, AuthorID: userID, Text: "<@" + string(userID) + "> added " + names + " to a new conversation.", CreatedAt: now}
	targetNotice := domain.Message{ID: targetNoticeID, WorkspaceID: workspaceID, Conversation: targetID, AuthorID: userID, Text: "<@" + string(userID) + "> added " + names + " to this conversation.", CreatedAt: now}
	createdEvent, err := newEvent(workspaceID, userID, events.NewPayload(
		"conversation.direct_members_added",
		events.String("channel_id", string(targetID)),
		events.String("source_channel_id", string(sourceID)),
	), now)
	if err != nil {
		return domain.Conversation{}, err
	}
	sourceEvent, err := messageEvent(workspaceID, "message.created", sourceNotice)
	if err != nil {
		return domain.Conversation{}, err
	}
	targetEvent, err := messageEvent(workspaceID, "message.created", targetNotice)
	if err != nil {
		return domain.Conversation{}, err
	}
	expansion := domain.DirectConversationExpansion{
		Source:       sourceID,
		Target:       target,
		Members:      memberIDs,
		History:      history,
		SourceNotice: sourceNotice,
		TargetNotice: targetNotice,
	}
	if err := m.Store.ExpandDirectConversation(ctx, expansion, []events.Event{createdEvent, sourceEvent, targetEvent}); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return m.Store.FindDirectConversation(ctx, workspaceID, memberIDs)
		}
		return domain.Conversation{}, err
	}
	return target, nil
}

func (m Messages) ConvertGroupDirectToPrivate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if !conversation.IsDirectOrGroup() {
		return domain.Conversation{}, ErrInvalidConversation
	}
	membership, err := m.activeWorkspaceMembership(ctx, workspaceID, userID)
	if err != nil {
		return domain.Conversation{}, err
	}
	// Slack allows members and multi-channel guests by default, but not
	// single-channel guests.
	if membership.UltraRestricted {
		return domain.Conversation{}, ErrNotWorkspaceAdmin
	}
	name = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), "-"))
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n") {
		return domain.Conversation{}, ErrInvalidConversation
	}
	now := time.Now().UTC()
	noticeID, err := domain.NewMessageID()
	if err != nil {
		return domain.Conversation{}, err
	}
	notice := domain.Message{
		ID:           noticeID,
		WorkspaceID:  workspaceID,
		Conversation: conversationID,
		AuthorID:     userID,
		Text:         "<@" + string(userID) + "> changed this group DM to the private channel #" + name + ".",
		CreatedAt:    now,
	}
	convertedEvent, err := newEvent(workspaceID, userID, events.NewPayload(
		"conversation.group_direct_converted",
		events.String("channel_id", string(conversationID)),
		events.String("name", name),
	), now)
	if err != nil {
		return domain.Conversation{}, err
	}
	noticeEvent, err := messageEvent(workspaceID, "message.created", notice)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.ConvertGroupDirectToPrivate(ctx, domain.GroupDirectConversion{Conversation: conversationID, Name: name, Notice: notice}, []events.Event{convertedEvent, noticeEvent})
}

func (m Messages) CreateConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string, private bool) (domain.Conversation, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	if err := m.refuseGuest(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	name = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), "-"))
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n") {
		return domain.Conversation{}, ErrInvalidConversation
	}
	id, err := domain.NewConversationID()
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation := domain.Conversation{ID: id, WorkspaceID: workspaceID, Name: name, Kind: domain.ConversationKindFor(private, false, false)}
	event, err := conversationLifecycleEvent(workspaceID, "conversation.created", conversation, userID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.Store.CreateConversation(ctx, conversation, userID, event); err != nil {
		return domain.Conversation{}, err
	}
	return conversation, nil
}

func (m Messages) RenameConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if conversation.Kind == domain.ConversationTypeIM {
		return domain.Conversation{}, ErrInvalidConversation
	}
	if conversation.Kind == domain.ConversationTypeMPIM {
		name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	} else {
		name = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), "-"))
	}
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n") {
		return domain.Conversation{}, ErrInvalidConversation
	}
	renamed := conversation
	renamed.Name = name
	event, err := conversationLifecycleEvent(workspaceID, "conversation.renamed", renamed, userID)
	if err != nil {
		return domain.Conversation{}, err
	}
	notice, err := conversationNotice(workspaceID, conversationID, userID, domain.MessageSubtypeChannelName, name)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.RenameConversation(ctx, conversationID, name, event, notice)
}

func (m Messages) SetConversationTopic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, topic string) (domain.Conversation, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	topic = strings.TrimSpace(topic)
	if len(topic) > 250 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	event, err := conversationEvent(workspaceID, "conversation.topic_changed", conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	notice, err := conversationNotice(workspaceID, conversationID, userID, domain.MessageSubtypeChannelTopic, topic)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationTopic(ctx, conversationID, topic, event, notice)
}

func (m Messages) SetConversationPurpose(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, purpose string) (domain.Conversation, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	purpose = strings.TrimSpace(purpose)
	if len(purpose) > 250 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	event, err := conversationEvent(workspaceID, "conversation.purpose_changed", conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	notice, err := conversationNotice(workspaceID, conversationID, userID, domain.MessageSubtypeChannelPurpose, purpose)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationPurpose(ctx, conversationID, purpose, event, notice)
}

func (m Messages) SetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if conversation.IsDirectOrGroup() {
		return domain.Conversation{}, ErrInvalidConversation
	}
	if archived == conversation.Archived {
		if archived {
			return domain.Conversation{}, ErrConversationAlreadyArchived
		}
		return domain.Conversation{}, ErrConversationNotArchived
	}
	if archived {
		required, err := m.isDefaultConversation(ctx, workspaceID, conversationID)
		if err != nil {
			return domain.Conversation{}, err
		}
		if required {
			return domain.Conversation{}, ErrCannotArchiveDefault
		}
	}
	topic := "conversation.unarchived"
	if archived {
		topic = "conversation.archived"
	}
	event, err := conversationLifecycleEvent(workspaceID, topic, conversation, userID)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationArchived(ctx, conversationID, archived, event)
}

func (m Messages) JoinConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.Archived {
		return domain.Conversation{}, ErrInvalidConversation
	}
	// Joining is self-service, which is exactly what a guest does not have: a
	// single-channel guest could otherwise walk into every public channel in
	// the workspace, one identifier at a time.
	if err := m.refuseGuest(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	// Joining is self-service, so it is only ever available for an open channel.
	// A private channel is joined by invitation and a direct conversation by
	// opening it, and neither may be entered by naming an identifier. The store
	// refuses this too, inside the write transaction, which is where the race-free
	// enforcement belongs — but every neighbouring method states its own
	// precondition, and this one silently depended on a backend detail.
	if conversation.Kind.OrPublic() != domain.ConversationTypePublic {
		return domain.Conversation{}, store.ErrNotFound
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"conversation.member_added",
		events.String("channel_id", string(conversationID)),
		events.String("user_id", string(userID)),
	), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	notice, err := conversationNotice(workspaceID, conversationID, userID, domain.MessageSubtypeChannelJoin, "")
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.Store.AddConversationMember(ctx, conversationID, userID, event, notice); err != nil {
		return domain.Conversation{}, err
	}
	return conversation, nil
}

func (m Messages) InviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	result, err := m.inviteConversationMembersWithOptions(ctx, workspaceID, userID, conversationID, users, false)
	if err != nil {
		return domain.Conversation{}, err
	}
	if len(result.Failures) == 0 {
		return result.Conversation, nil
	}
	switch result.Failures[0].Reason {
	case conversationInviteSelf:
		return domain.Conversation{}, ErrCannotInviteSelf
	case conversationInviteAlreadyInChannel:
		return domain.Conversation{}, store.ErrAlreadyExists
	default:
		return domain.Conversation{}, store.ErrNotFound
	}
}

// inviteConversationMembersWithOptions implements ordinary
// conversations.invite. By default every invitee is validated before the store
// is mutated, so one invalid user makes the whole request atomic. force permits
// the valid subset to proceed while retaining rejected invitees for the
// transport's per-user error response.
func (m Messages) inviteConversationMembersWithOptions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID, force bool) (conversationInviteResult, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return conversationInviteResult{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return conversationInviteResult{}, store.ErrNotFound
	}
	if conversation.IsDirectOrGroup() || conversation.Archived {
		return conversationInviteResult{}, ErrInvalidConversation
	}

	result := conversationInviteResult{Conversation: conversation}
	seen := make(map[domain.UserID]struct{}, len(users))
	valid := make([]domain.UserID, 0, len(users))
	for _, targetID := range users {
		targetID = domain.UserID(strings.TrimSpace(string(targetID)))
		if targetID == "" {
			result.Failures = append(result.Failures, conversationInviteFailure{UserID: targetID, Reason: conversationInviteUserNotFound})
			continue
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		if targetID == userID {
			result.Failures = append(result.Failures, conversationInviteFailure{UserID: targetID, Reason: conversationInviteSelf})
			continue
		}
		target, lookupErr := m.Store.GetUser(ctx, targetID)
		if lookupErr != nil || target.WorkspaceID != workspaceID || target.Deleted {
			result.Failures = append(result.Failures, conversationInviteFailure{UserID: targetID, Reason: conversationInviteUserNotFound})
			continue
		}
		member, membershipErr := m.Store.IsConversationMember(ctx, conversationID, targetID)
		if membershipErr != nil {
			return conversationInviteResult{}, membershipErr
		}
		if member {
			result.Failures = append(result.Failures, conversationInviteFailure{UserID: targetID, Reason: conversationInviteAlreadyInChannel})
			continue
		}
		valid = append(valid, targetID)
	}
	if len(valid) == 0 || (!force && len(result.Failures) != 0) {
		return result, nil
	}

	event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.members_invited", events.String("channel_id", string(conversationID)), events.Strings("user_ids", userIDStrings(valid))), time.Now().UTC())
	if err != nil {
		return conversationInviteResult{}, err
	}
	if err := m.Store.InviteConversationMembers(ctx, conversationID, valid, event); err != nil {
		return conversationInviteResult{}, err
	}
	result.InvitedCount = len(valid)
	return result, nil
}

func (m Messages) AdminInviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	return m.adminInviteConversationMembers(ctx, workspaceID, userID, conversationID, users)
}

func (m Messages) AdminConvertConversationToPrivate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.Kind.OrPublic() != domain.ConversationTypePublic {
		return domain.Conversation{}, ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, userID, conversationPayload("conversation.converted_to_private", conversationID), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationPrivate(ctx, conversationID, event)
}

// AdminConvertConversationToPublic is the reverse of
// AdminConvertConversationToPrivate, and is deliberately stricter.
//
// Making a channel private hides what was said from people who could already
// read it. Making one public shows what was said to people who never could, and
// nothing in the product can take that back — so the two are not the same
// operation with a flag flipped.
//
// A channel an external organization is in is refused. Those members joined a
// private conversation; making it public would expose it to a workspace they
// never agreed to be visible to, and no administrator of this workspace speaks
// for them.
func (m Messages) AdminConvertConversationToPublic(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.Kind != domain.ConversationTypePrivate {
		return domain.Conversation{}, ErrInvalidConversation
	}
	teams, _, err := m.Store.ListConversationTeams(ctx, workspaceID, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	for _, team := range teams {
		if team != workspaceID {
			return domain.Conversation{}, ErrInvalidConversation
		}
	}
	event, err := newEvent(workspaceID, userID, conversationPayload("conversation.converted_to_public", conversationID), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationPublic(ctx, conversationID, event)
}

func (m Messages) AdminGetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationPrefs, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPrefs{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID || conversation.IsDirectOrGroup() {
		return domain.ConversationPrefs{}, store.ErrNotFound
	}
	return m.Store.GetConversationPrefs(ctx, conversationID)
}

func (m Messages) AdminSetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, value domain.ConversationPrefs) (domain.ConversationPrefs, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPrefs{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID || conversation.IsDirectOrGroup() {
		return domain.ConversationPrefs{}, store.ErrNotFound
	}
	value, err = normalizeConversationPrefs(value)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	for _, target := range append(append([]domain.UserID{}, value.CanThread.Users...), value.WhoCanPost.Users...) {
		member, lookupErr := m.Store.GetUser(ctx, target)
		if lookupErr != nil || member.WorkspaceID != workspaceID || member.Deleted {
			return domain.ConversationPrefs{}, ErrInvalidConversationPrefs
		}
	}
	event, err := newEvent(workspaceID, userID, conversationPayload("conversation.preferences_changed", conversationID), time.Now().UTC())
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	return m.Store.SetConversationPrefs(ctx, conversationID, value, event)
}

func normalizeConversationPrefs(value domain.ConversationPrefs) (domain.ConversationPrefs, error) {
	var err error
	value.CanThread, err = normalizeConversationPreferenceList(value.CanThread)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	value.WhoCanPost, err = normalizeConversationPreferenceList(value.WhoCanPost)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	return value, nil
}

func normalizeConversationPreferenceList(value domain.ConversationPreferenceList) (domain.ConversationPreferenceList, error) {
	types := make([]domain.ConversationPreferenceType, 0, len(value.Types))
	typeSeen := make(map[domain.ConversationPreferenceType]struct{}, len(value.Types))
	for _, item := range value.Types {
		item = domain.ConversationPreferenceType(strings.TrimSpace(string(item)))
		if item == "" {
			return domain.ConversationPreferenceList{}, ErrInvalidConversationPrefs
		}
		if _, exists := typeSeen[item]; exists {
			continue
		}
		typeSeen[item] = struct{}{}
		types = append(types, item)
	}
	users := make([]domain.UserID, 0, len(value.Users))
	userSeen := make(map[domain.UserID]struct{}, len(value.Users))
	for _, item := range value.Users {
		item = domain.UserID(strings.TrimSpace(string(item)))
		if item == "" {
			return domain.ConversationPreferenceList{}, ErrInvalidConversationPrefs
		}
		if _, exists := userSeen[item]; exists {
			continue
		}
		userSeen[item] = struct{}{}
		users = append(users, item)
	}
	if len(types) > 20 || len(users) > 100 {
		return domain.ConversationPreferenceList{}, ErrInvalidConversationPrefs
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	return domain.ConversationPreferenceList{Types: types, Users: users}, nil
}

func (m Messages) AdminSearchConversations(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query string, request domain.PageRequest) (domain.ConversationPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPage{}, err
	}
	query = strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if query == "" || len(query) > 200 {
		return domain.ConversationPage{}, ErrInvalidConversation
	}
	return m.Store.SearchConversations(ctx, workspaceID, query, request)
}

func (m Messages) AdminConversationTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) ([]domain.WorkspaceID, bool, domain.Cursor, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return nil, false, "", err
	}
	if request.Limit <= 0 {
		return nil, false, "", ErrInvalidConversation
	}
	if _, err := domain.DecodeListCursor(request.Cursor); err != nil {
		return nil, false, "", err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return nil, false, "", store.ErrNotFound
	}
	teams, _, err := m.Store.ListConversationTeams(ctx, workspaceID, conversationID)
	if err != nil {
		return nil, false, "", err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, false, "", err
	}
	start := 0
	for start < len(teams) && string(teams[start]) <= after {
		start++
	}
	teams = teams[start:]
	hasMore := len(teams) > request.Limit
	if hasMore {
		teams = teams[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(string(teams[len(teams)-1]))
		if err != nil {
			return nil, false, "", err
		}
	}
	return teams, hasMore, next, nil
}

func (m Messages) AdminSetConversationTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, teams []domain.WorkspaceID, orgChannel bool) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	seen := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, teamID := range teams {
		teamID = domain.WorkspaceID(strings.TrimSpace(string(teamID)))
		// A conversation may only be associated with a workspace this actor's
		// authority covers. Testing existence instead accepted any workspace id on
		// the deployment, which wrote a cross-tenant row and — because a missing
		// workspace answered differently from a foreign one — turned the operation
		// into an oracle any workspace administrator could use to enumerate every
		// tenant. A foreign id and an absent id are now the same refusal.
		//
		// This process's topology places one workspace behind a conversation, the
		// same fact AdminAddUserGroupTeams asserts. A multi-workspace association
		// needs a persisted organization edge, which does not exist.
		if teamID != workspaceID {
			return ErrInvalidConversation
		}
		seen[teamID] = struct{}{}
	}
	if len(seen) == 0 && !orgChannel {
		return ErrInvalidConversation
	}
	normalized := make([]domain.WorkspaceID, 0, len(seen))
	for teamID := range seen {
		normalized = append(normalized, teamID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	event, err := newEvent(workspaceID, actor, events.NewPayload("conversation.teams_changed", events.String("channel_id", string(conversationID)), events.Strings("team_ids", workspaceIDStrings(normalized)), events.Bool("org_channel", orgChannel)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetConversationTeams(ctx, workspaceID, conversationID, normalized, orgChannel, event)
}

func (m Messages) AdminDisconnectSharedConversation(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, leaving []domain.WorkspaceID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	// Every sibling administrative conversation method proves the conversation
	// belongs to the actor's workspace before minting an event for it. This one
	// relied on the store's own predicate, so it emitted a journal record naming a
	// conversation that may not exist and refused in a different shape from the
	// rest.
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	for index, team := range leaving {
		leaving[index] = domain.WorkspaceID(strings.TrimSpace(string(team)))
		if leaving[index] == "" {
			return ErrInvalidConversation
		}
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("conversation.shared_disconnected", events.String("channel_id", string(conversationID)), events.Strings("team_ids", workspaceIDStrings(leaving))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DisconnectConversationTeams(ctx, workspaceID, conversationID, leaving, event)
}

func (m Messages) AdminConnectedChannelInfo(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, channels []domain.ConversationID, teams []domain.WorkspaceID, request domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return nil, false, "", err
	}
	if request.Limit <= 0 {
		return nil, false, "", ErrInvalidConversation
	}
	return m.Store.ListConnectedChannelInfo(ctx, workspaceID, channels, teams, request)
}

func normalizeEmojiName(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), ":"))
}

func validEmojiName(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '+' {
			continue
		}
		return false
	}
	return true
}

func (m Messages) Emojis(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.CustomEmoji, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.ListEmojis(ctx, workspaceID)
}

func (m Messages) AdminAddEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, imageURL string) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return err
	}
	name, imageURL = normalizeEmojiName(name), strings.TrimSpace(imageURL)
	parsedURL, urlErr := url.Parse(imageURL)
	if !validEmojiName(name) || imageURL == "" || len(imageURL) > 2048 || urlErr != nil ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return ErrInvalidEmoji
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("emoji.added", events.String("name", name), events.String("value", imageURL)), time.Now().UTC())
	if err != nil {
		return err
	}
	err = m.Store.AddEmoji(ctx, domain.CustomEmoji{WorkspaceID: workspaceID, Name: name, URL: imageURL}, event)
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrEmojiAlreadyExists
	}
	return err
}

func (m Messages) AdminAddEmojiAlias(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, target string) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return err
	}
	name, target = normalizeEmojiName(name), normalizeEmojiName(target)
	if !validEmojiName(name) || !validEmojiName(target) || name == target {
		return ErrInvalidEmoji
	}
	emojis, err := m.Store.ListEmojis(ctx, workspaceID)
	if err != nil {
		return err
	}
	found := false
	for _, value := range emojis {
		if value.Name == target {
			found = true
			break
		}
	}
	if !found {
		return store.ErrNotFound
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("emoji.alias_added", events.String("name", name), events.String("alias_for", target)), time.Now().UTC())
	if err != nil {
		return err
	}
	err = m.Store.AddEmoji(ctx, domain.CustomEmoji{WorkspaceID: workspaceID, Name: name, AliasFor: target}, event)
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrEmojiAlreadyExists
	}
	return err
}

func (m Messages) AdminRemoveEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return err
	}
	name = normalizeEmojiName(name)
	if name == "" {
		return ErrInvalidEmoji
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("emoji.removed", events.String("name", name)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.RemoveEmoji(ctx, workspaceID, name, event)
}

func (m Messages) AdminRenameEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, oldName, newName string) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return err
	}
	oldName, newName = normalizeEmojiName(oldName), normalizeEmojiName(newName)
	if oldName == "" || newName == "" || oldName == newName || len(oldName) > 255 || len(newName) > 255 {
		return ErrInvalidEmoji
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("emoji.renamed", events.String("old_name", oldName), events.String("new_name", newName)), time.Now().UTC())
	if err != nil {
		return err
	}
	err = m.Store.RenameEmoji(ctx, workspaceID, oldName, newName, event)
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrEmojiAlreadyExists
	}
	return err
}

func normalizeUserGroupChannels(values []domain.ConversationID) ([]domain.ConversationID, error) {
	seen := make(map[domain.ConversationID]struct{}, len(values))
	result := make([]domain.ConversationID, 0, len(values))
	for _, value := range values {
		value = domain.ConversationID(strings.TrimSpace(string(value)))
		if value == "" {
			return nil, ErrInvalidUserGroup
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (m Messages) UserGroupChannels(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID) ([]domain.ConversationID, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return nil, err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	return append([]domain.ConversationID(nil), value.Channels...), nil
}

func (m Messages) AddUserGroupChannels(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, channels []domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	add, err := normalizeUserGroupChannels(channels)
	if err != nil || len(add) == 0 {
		return ErrInvalidUserGroup
	}
	// A default channel list that names a conversation from another workspace, or
	// none at all, is stored and rendered as though it were real. Every sibling
	// that persists a conversation identifier proves it first.
	for _, channelID := range add {
		conversation, err := m.Store.GetConversation(ctx, channelID)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	combined := append(append([]domain.ConversationID(nil), value.Channels...), add...)
	combined, err = normalizeUserGroupChannels(combined)
	if err != nil {
		return err
	}
	snapshot := value
	snapshot.Channels = combined
	snapshot.UpdatedBy = actor
	snapshot.UpdatedAt = time.Now().UTC()
	payload, err := userGroupEventPayload("usergroup.channels_changed", snapshot, events.Strings("channel_ids", conversationIDStrings(combined)))
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actor, payload, snapshot.UpdatedAt)
	if err != nil {
		return err
	}
	return m.Store.SetUserGroupChannels(ctx, workspaceID, id, combined, actor, event)
}

// AdminAddUserGroupTeams validates the organization-level association against
// this process's single-workspace topology. The workspace is already implicit
// in UserGroup, so a valid association needs no additional persisted edge.
func (m Messages) AdminAddUserGroupTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, teams []domain.WorkspaceID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	if _, err := m.Store.GetUserGroup(ctx, workspaceID, id); err != nil {
		return err
	}
	if len(teams) == 0 {
		return ErrInvalidUserGroup
	}
	seen := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, team := range teams {
		team = domain.WorkspaceID(strings.TrimSpace(string(team)))
		if team == "" {
			return ErrInvalidUserGroup
		}
		if _, exists := seen[team]; exists {
			continue
		}
		seen[team] = struct{}{}
		if team != workspaceID {
			return ErrInvalidUserGroup
		}
	}
	return nil
}

func (m Messages) RemoveUserGroupChannels(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, channels []domain.ConversationID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	remove, err := normalizeUserGroupChannels(channels)
	if err != nil || len(remove) == 0 {
		return ErrInvalidUserGroup
	}
	removeSet := make(map[domain.ConversationID]struct{}, len(remove))
	for _, channel := range remove {
		removeSet[channel] = struct{}{}
	}
	remaining := make([]domain.ConversationID, 0, len(value.Channels))
	for _, channel := range value.Channels {
		if _, exists := removeSet[channel]; !exists {
			remaining = append(remaining, channel)
		}
	}
	snapshot := value
	snapshot.Channels = remaining
	snapshot.UpdatedBy = actor
	snapshot.UpdatedAt = time.Now().UTC()
	payload, err := userGroupEventPayload("usergroup.channels_changed", snapshot, events.Strings("channel_ids", conversationIDStrings(remaining)))
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actor, payload, snapshot.UpdatedAt)
	if err != nil {
		return err
	}
	return m.Store.SetUserGroupChannels(ctx, workspaceID, id, remaining, actor, event)
}

func (m Messages) AdminSetWorkspaceName(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, name string) (domain.Workspace, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if name == "" || len(name) > 255 {
		return domain.Workspace{}, ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("workspace.name_changed", events.String("name", name)), time.Now().UTC())
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceName(ctx, workspaceID, name, event)
}

func (m Messages) AdminSetWorkspaceDescription(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, description string) (domain.Workspace, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	description = strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
	if len(description) > 255 {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("workspace.description_changed", events.String("description", description)), time.Now().UTC())
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceDescription(ctx, workspaceID, description, event)
}

func (m Messages) AdminSetWorkspaceDiscoverability(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, discoverability domain.WorkspaceDiscoverability) (domain.Workspace, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	if !discoverability.Valid() {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("workspace.discoverability_changed", events.String("discoverability", string(discoverability))), time.Now().UTC())
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceDiscoverability(ctx, workspaceID, discoverability, event)
}

func (m Messages) AdminSetWorkspaceIcon(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, iconURL string) (domain.Workspace, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	iconURL = strings.TrimSpace(iconURL)
	parsed, err := url.ParseRequestURI(iconURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(iconURL) > 2048 {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("workspace.icon_changed", events.String("icon_url", iconURL)), time.Now().UTC())
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceIcon(ctx, workspaceID, iconURL, event)
}

func (m Messages) AdminSetWorkspaceDefaultChannels(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, channels []domain.ConversationID) (domain.Workspace, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	channels, err := normalizeWorkspaceDefaultChannels(channels)
	if err != nil {
		return domain.Workspace{}, err
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("workspace.default_channels_changed", events.Strings("channel_ids", conversationIDStrings(channels))), time.Now().UTC())
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceDefaultChannels(ctx, workspaceID, channels, event)
}

func normalizeWorkspaceDefaultChannels(values []domain.ConversationID) ([]domain.ConversationID, error) {
	seen := make(map[domain.ConversationID]struct{}, len(values))
	result := make([]domain.ConversationID, 0, len(values))
	for _, value := range values {
		value = domain.ConversationID(strings.TrimSpace(string(value)))
		if value == "" {
			return nil, ErrInvalidWorkspace
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) > 100 {
		return nil, ErrInvalidWorkspace
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func conversationIDStrings(values []domain.ConversationID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func workspaceIDStrings(values []domain.WorkspaceID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func userIDStrings(values []domain.UserID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func (m Messages) AdminTeamUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, role domain.WorkspaceRole, request domain.PageRequest) (domain.UserPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserPage{}, err
	}
	// Only the two authority roles are listable here, because the two routes
	// that reach this are admin.teams.admins.list and admin.teams.owners.list.
	// The refusal used to be ErrInvalidUserGroup — "user group name, handle,
	// and members are invalid" — which told a caller asking about workspace
	// roles to go and look at user groups.
	if role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return domain.UserPage{}, ErrInvalidWorkspace
	}
	return m.Store.ListUsersByRole(ctx, workspaceID, role, request)
}

// adminInviteConversationMembers reaches conversations the actor is not in, so
// it requires a workspace administrator instead of ordinary conversation
// membership. Workspace membership alone authorizes neither.
func (m Messages) adminInviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsDirectOrGroup() || conversation.Archived {
		return domain.Conversation{}, ErrInvalidConversation
	}
	seen := make(map[domain.UserID]struct{}, len(users))
	normalized := make([]domain.UserID, 0, len(users))
	for _, targetID := range users {
		targetID = domain.UserID(strings.TrimSpace(string(targetID)))
		if targetID == "" {
			return domain.Conversation{}, ErrInvalidConversation
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		target, lookupErr := m.Store.GetUser(ctx, targetID)
		if lookupErr != nil || target.WorkspaceID != workspaceID || target.Deleted {
			return domain.Conversation{}, store.ErrNotFound
		}
		seen[targetID] = struct{}{}
		normalized = append(normalized, targetID)
	}
	if len(normalized) == 0 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.members_invited", events.String("channel_id", string(conversationID)), events.Strings("user_ids", userIDStrings(normalized))), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.Store.InviteConversationMembers(ctx, conversationID, normalized, event); err != nil {
		// admin.conversations.invite historically treats an already-complete
		// membership set as an idempotent success in our pinned compatibility
		// surface.
		if errors.Is(err, store.ErrAlreadyExists) {
			return conversation, nil
		}
		return domain.Conversation{}, err
	}
	return conversation, nil
}

func (m Messages) LeaveConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if conversation.Archived {
		return ErrInvalidConversation
	}
	if conversation.IsDirectOrGroup() {
		event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.direct_closed", events.String("channel_id", string(conversationID))), time.Now().UTC())
		if err != nil {
			return err
		}
		changed, err := m.Store.SetDirectConversationOpen(ctx, workspaceID, userID, conversationID, false, event)
		if err != nil {
			return err
		}
		if !changed {
			return store.ErrAlreadyExists
		}
		return nil
	}
	if !conversation.IsDirectOrGroup() {
		required, err := m.isDefaultConversation(ctx, workspaceID, conversationID)
		if err != nil {
			return err
		}
		if required {
			return ErrCannotLeaveDefault
		}
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.member_left", events.String("channel_id", string(conversationID)), events.String("user_id", string(userID))), time.Now().UTC())
	if err != nil {
		return err
	}
	notice, err := conversationNotice(workspaceID, conversationID, userID, domain.MessageSubtypeChannelLeave, "")
	if err != nil {
		return err
	}
	return m.Store.RemoveConversationMember(ctx, conversationID, userID, event, notice)
}

func (m Messages) isDefaultConversation(ctx context.Context, workspaceID domain.WorkspaceID, conversationID domain.ConversationID) (bool, error) {
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return false, err
	}
	for _, required := range workspace.DefaultChannelIDs {
		if required == conversationID {
			return true, nil
		}
	}
	return false, nil
}

func (m Messages) KickConversationMember(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, targetID domain.UserID) error {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return store.ErrNotFound
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.member_kicked", events.String("channel_id", string(conversationID)), events.String("user_id", string(targetID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.RemoveConversationMember(ctx, conversationID, targetID, event)
}

// MessageAt resolves a message by its public timestamp, which is the
// identifier every Slack permalink, action and API argument names it by. The
// caller must be able to read the conversation; a message that does not exist
// and one in a conversation the caller may not read are the same answer, so
// the lookup cannot be used to probe for either.
func (m Messages) MessageAt(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp) (domain.Message, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.Message{}, err
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return domain.Message{}, ErrInvalidTimestamp
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, conversation, createdAt)
	if err != nil || message.WorkspaceID != workspaceID || message.Deleted {
		return domain.Message{}, store.ErrNotFound
	}
	return message, nil
}

// ReadCursor reports where a member has read to in a conversation. MarkRead
// has always been on this seam; the reader was not, so nothing above the
// store could render an unread divider or answer "which message is the first
// unread one" without writing a cursor to find out.
func (m Messages) ReadCursor(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ReadCursor, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.ReadCursor{}, err
	}
	cursor, err := m.Store.GetReadCursor(ctx, workspaceID, userID, conversationID)
	if errors.Is(err, store.ErrNotFound) {
		// A member who has never marked anything read has read nothing, which
		// is a cursor at the zero instant rather than a missing record: every
		// caller would otherwise have to translate the same absence.
		return domain.ReadCursor{WorkspaceID: workspaceID, UserID: userID, Conversation: conversationID}, nil
	}
	if err != nil {
		return domain.ReadCursor{}, err
	}
	return cursor, nil
}

// ThreadSummaries reports what each named thread root has accumulated. The
// caller must be able to read the conversation; the summary discloses reply
// counts and participants, which are conversation content.
func (m Messages) ThreadSummaries(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, roots []domain.MessageTimestamp) (map[domain.MessageTimestamp]domain.ThreadSummary, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return nil, err
	}
	return m.Store.ThreadSummaries(ctx, conversationID, roots)
}

func (m Messages) MarkRead(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.ReadCursor, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.ReadCursor{}, err
	}
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		return domain.ReadCursor{}, ErrInvalidTimestamp
	}
	now := time.Now().UTC()
	cursor := domain.ReadCursor{WorkspaceID: workspaceID, UserID: userID, Conversation: conversationID, LastRead: timestamp, UpdatedAt: now}
	event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.read", events.String("channel_id", string(conversationID)), events.String("user_id", string(userID)), events.String("ts", string(timestamp))), now)
	if err != nil {
		return domain.ReadCursor{}, err
	}
	if err := m.Store.SetReadCursor(ctx, cursor, event); err != nil {
		return domain.ReadCursor{}, err
	}
	return cursor, nil
}

// markAllReadPage is how many conversations one page of the mark-all-read walk
// reads. It matches the largest page the client already asks for elsewhere.
const markAllReadPage = 200

// MarkAllRead is Slack's "mark all messages as read": every conversation the
// member belongs to advances to its newest message, and the count of
// conversations that actually moved is returned.
//
// The read position is the newest message observed now, not "everything
// forever". A message that arrives while this runs stays unread, which is both
// what a member expects and the only answer that does not silently discard
// something they have not seen.
//
// Conversations with nothing unread are skipped rather than rewritten, so the
// journal records the conversations a member actually cleared instead of one
// event per conversation they have ever joined.
func (m Messages) MarkAllRead(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (int, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return 0, err
	}
	// Every conversation, not the first page of them: "mark all read" that
	// stopped at a page boundary would leave badges up with no way to tell
	// which ones it skipped.
	unread := make([]domain.ConversationID, 0, markAllReadPage)
	request := domain.ConversationListRequest{Limit: markAllReadPage, IncludeClosedDirects: true}
	for {
		page, err := m.Store.ListConversations(ctx, workspaceID, userID, request)
		if err != nil {
			return 0, err
		}
		for _, conversation := range page.Conversations {
			if conversation.UnreadCount > 0 {
				unread = append(unread, conversation.ID)
			}
		}
		if page.NextCursor == "" || page.NextCursor == request.Cursor {
			// A cursor that does not advance ends the walk. The alternative is
			// a loop whose termination depends on a value the store chose,
			// which is a hang rather than an error and would be reached first
			// by the workspace with the most conversations.
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(unread) == 0 {
		return 0, nil
	}
	latest, err := m.Store.LatestMessageTimestamps(ctx, workspaceID, unread)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	cursors := make([]store.ReadCursorUpdate, 0, len(unread))
	for _, conversation := range unread {
		timestamp, ok := latest[conversation]
		if !ok {
			// Unread without a message means the count and the messages
			// disagree; skipping is the only safe reading, and inventing a
			// cursor would be worse than leaving the badge up.
			continue
		}
		event, err := newEvent(workspaceID, userID, events.NewPayload("conversation.read",
			events.String("channel_id", string(conversation)), events.String("user_id", string(userID)), events.String("ts", string(timestamp))), now)
		if err != nil {
			return 0, err
		}
		cursors = append(cursors, store.ReadCursorUpdate{
			Cursor: domain.ReadCursor{WorkspaceID: workspaceID, UserID: userID, Conversation: conversation, LastRead: timestamp, UpdatedAt: now},
			Event:  event,
		})
	}
	if len(cursors) == 0 {
		return 0, nil
	}
	if err := m.Store.SetReadCursors(ctx, cursors); err != nil {
		return 0, err
	}
	return len(cursors), nil
}

// RecordActivity is the heartbeat behind automatic presence. It is
// authorization-checked like any other write, and it deliberately reports
// nothing: a client sends it and moves on.
func (m Messages) RecordActivity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	return m.Store.TouchUserActivity(ctx, workspaceID, userID, time.Now().UTC())
}

// FollowedThreads is Slack's Threads view. Authorization is the workspace, and
// the store answers only threads in conversations the member follows — a follow
// can only be created by a member of the conversation, so a follow is itself
// the membership proof.
func (m Messages) FollowedThreads(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.FollowedThreadPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.FollowedThreadPage{}, err
	}
	return m.Store.ListFollowedThreads(ctx, workspaceID, userID, request)
}

func (m Messages) WorkspaceNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceNotificationPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return m.Store.GetWorkspaceNotificationPreferences(ctx, workspaceID, userID)
}

// SetNotificationSchedule writes the window a member allows notifications in.
// It is a call of its own rather than four more arguments on the preferences
// setter, because it is a separate form answering a separate question, and
// because a setter that took everything would let either form silently undo the
// other.
func (m Messages) SetNotificationSchedule(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, schedule domain.NotificationSchedule) (domain.WorkspaceNotificationPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	if !schedule.Valid() {
		return domain.WorkspaceNotificationPreferences{}, store.InvalidArgument("notification schedule is invalid")
	}
	preferences, err := m.Store.GetWorkspaceNotificationPreferences(ctx, workspaceID, userID)
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	preferences.Schedule = schedule
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"notification.preferences_changed",
		events.String("user_id", string(userID)),
	), now)
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	if err := m.Store.SetWorkspaceNotificationPreferences(ctx, preferences, event); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return preferences, nil
}

func (m Messages) SetWorkspaceNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, level domain.NotificationLevel, keywords []string, activityChannels, activityReminders, browserNotifications bool) (domain.WorkspaceNotificationPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	// The schedule is carried forward rather than defaulted. This call builds a
	// whole record from its arguments, and the schedule is not one of them, so
	// saving the notification form would otherwise silently switch off a window
	// the member set on a different form — the same whole-record hazard the
	// assistant thread state was designed around.
	current, err := m.Store.GetWorkspaceNotificationPreferences(ctx, workspaceID, userID)
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	preferences := domain.WorkspaceNotificationPreferences{
		WorkspaceID: workspaceID, UserID: userID, Level: level,
		Keywords:         domain.NormalizeNotificationKeywords(keywords),
		ActivityChannels: activityChannels, ActivityReminders: activityReminders,
		BrowserNotifications: browserNotifications,
		Schedule:             current.Schedule,
	}
	if !preferences.Valid() {
		return domain.WorkspaceNotificationPreferences{}, store.InvalidArgument("workspace notification preferences are invalid")
	}
	now := time.Now().UTC()
	event, eventErr := newEvent(workspaceID, userID, events.NewPayload(
		"notification.preferences_changed",
		events.String("user_id", string(userID)),
	), now)
	if eventErr != nil {
		return domain.WorkspaceNotificationPreferences{}, eventErr
	}
	if err := m.Store.SetWorkspaceNotificationPreferences(ctx, preferences, event); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return preferences, nil
}

func (m Messages) ConversationNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationNotificationPreferences, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	return m.Store.GetConversationNotificationPreferences(ctx, workspaceID, userID, conversationID)
}

func (m Messages) SetConversationNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, level domain.NotificationLevel, followEveryThread bool) (domain.ConversationNotificationPreferences, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	preferences := domain.ConversationNotificationPreferences{
		WorkspaceID: workspaceID, UserID: userID, Conversation: conversationID,
		Level: level, FollowEveryThread: followEveryThread,
	}
	if !preferences.Valid() {
		return domain.ConversationNotificationPreferences{}, store.InvalidArgument("conversation notification preferences are invalid")
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"conversation.notification_preferences_changed",
		events.String("channel_id", string(conversationID)),
		events.String("user_id", string(userID)),
	), now)
	if err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	if err := m.Store.SetConversationNotificationPreferences(ctx, preferences, event); err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	return preferences, nil
}

func (m Messages) ThreadFollowed(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, root domain.MessageTimestamp) (bool, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return false, err
	}
	if err := m.requireThreadRoot(ctx, workspaceID, conversationID, root); err != nil {
		return false, err
	}
	return m.Store.IsThreadFollowed(ctx, workspaceID, userID, conversationID, root)
}

func (m Messages) requireThreadRoot(ctx context.Context, workspaceID domain.WorkspaceID, conversationID domain.ConversationID, root domain.MessageTimestamp) error {
	createdAt, err := domain.ParseMessageTimestamp(root)
	if err != nil {
		return ErrInvalidTimestamp
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, conversationID, createdAt)
	if err != nil {
		return err
	}
	if message.WorkspaceID != workspaceID || message.Deleted || message.ThreadTimestamp != "" {
		return store.ErrNotFound
	}
	return nil
}

func (m Messages) SetThreadFollowed(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, root domain.MessageTimestamp, followed bool) error {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	if err := m.requireThreadRoot(ctx, workspaceID, conversationID, root); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"thread.follow_changed",
		events.String("channel_id", string(conversationID)),
		events.String("user_id", string(userID)),
		events.String("thread_ts", string(root)),
	), now)
	if err != nil {
		return err
	}
	return m.Store.SetThreadFollowed(ctx, workspaceID, userID, conversationID, root, followed, event)
}

func (m Messages) AddReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) error {
	reaction, err := m.reactionFor(ctx, workspaceID, userID, conversationID, timestamp, name)
	if err != nil {
		return err
	}
	if _, _, standard := slackemoji.ParseReactionName(reaction.Name); !standard {
		custom, listErr := m.Store.ListEmojis(ctx, workspaceID)
		if listErr != nil {
			return listErr
		}
		found := false
		for _, value := range custom {
			if normalizeEmojiName(value.Name) == reaction.Name {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidReaction
		}
	}
	now := time.Now().UTC()
	reaction.CreatedAt = now
	event, err := newEvent(workspaceID, userID, reactionPayload("reaction.added", reaction, userID, conversationID, timestamp), now)
	if err != nil {
		return err
	}
	return m.Store.AddReaction(ctx, reaction, event)
}

func (m Messages) RemoveReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) error {
	reaction, err := m.reactionFor(ctx, workspaceID, userID, conversationID, timestamp, name)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, reactionPayload("reaction.removed", reaction, userID, conversationID, timestamp), now)
	if err != nil {
		return err
	}
	return m.Store.RemoveReaction(ctx, reaction, event)
}

func (m Messages) Reactions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error) {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return nil, "", false, err
	}
	return m.Store.ListReactions(ctx, message.ID, request)
}

func (m Messages) UserReactions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.UserReactionPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.UserReactionPage{}, err
	}
	return m.Store.ListUserReactions(ctx, workspaceID, userID, request)
}

func reactionPayload(topic string, reaction domain.Reaction, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) events.Payload {
	return events.NewPayload(topic,
		events.String("message_id", string(reaction.Message)),
		events.String("channel_id", string(conversationID)),
		events.String("ts", string(timestamp)),
		events.String("reaction", reaction.Name),
		events.String("user_id", string(userID)),
	)
}

func (m Messages) reactionFor(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) (domain.Reaction, error) {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return domain.Reaction{}, err
	}
	name = normalizeEmojiName(name)
	if _, _, standard := slackemoji.ParseReactionName(name); standard {
		return domain.Reaction{Message: message.ID, Name: name, UserID: userID}, nil
	}
	if !validEmojiName(name) {
		return domain.Reaction{}, ErrInvalidReaction
	}
	return domain.Reaction{Message: message.ID, Name: name, UserID: userID}, nil
}

func (m Messages) AddPin(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, messageItemPayload("pin.added", message.ID, conversationID, userID, timestamp), now)
	if err != nil {
		return err
	}
	return m.Store.AddPin(ctx, domain.Pin{Message: message.ID, UserID: userID, CreatedAt: now}, event)
}

func (m Messages) RemovePin(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, messageItemPayload("pin.removed", message.ID, conversationID, userID, timestamp), now)
	if err != nil {
		return err
	}
	return m.Store.RemovePin(ctx, domain.Pin{Message: message.ID, UserID: userID}, event)
}

func messageItemPayload(topic string, messageID domain.MessageID, conversationID domain.ConversationID, userID domain.UserID, timestamp domain.MessageTimestamp) events.Payload {
	return events.NewPayload(topic,
		events.String("message_id", string(messageID)),
		events.String("channel_id", string(conversationID)),
		events.String("ts", string(timestamp)),
		events.String("user_id", string(userID)),
	)
}

func (m Messages) Pins(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return nil, "", false, err
	}
	return m.Store.ListPins(ctx, conversationID, request)
}

func (m Messages) AddStar(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, messageItemPayload("star.added", message.ID, conversationID, userID, timestamp), now)
	if err != nil {
		return err
	}
	return m.Store.AddStar(ctx, domain.Star{Message: message, Conversation: conversationID, UserID: userID, CreatedAt: now}, event)
}

func (m Messages) RemoveStar(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, messageItemPayload("star.removed", message.ID, conversationID, userID, timestamp), now)
	if err != nil {
		return err
	}
	return m.Store.RemoveStar(ctx, domain.Star{Message: message, Conversation: conversationID, UserID: userID}, event)
}

func (m Messages) Stars(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, "", false, err
	}
	return m.Store.ListStars(ctx, workspaceID, userID, request)
}

func savedItemPayload(topic string, item domain.SavedItem) events.Payload {
	return events.NewPayload(topic,
		events.String("saved_item_id", string(item.ID)),
		events.String("message_id", string(item.MessageID)),
		events.String("channel_id", string(item.Conversation)),
		events.String("user_id", string(item.UserID)),
		events.String("state", string(item.State)),
	)
}

// SaveForLater implements Slack's current private Later state. It must not call
// AddStar: Slack retired that relationship in 2023 and current Later items are
// neither written nor returned through the deprecated stars.* methods.
func (m Messages) SaveForLater(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.SavedItem, error) {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return domain.SavedItem{}, err
	}
	id, err := domain.NewSavedItemID()
	if err != nil {
		return domain.SavedItem{}, err
	}
	now := time.Now().UTC()
	item := domain.SavedItem{
		ID: id, WorkspaceID: workspaceID, UserID: userID, MessageID: message.ID,
		Conversation: conversationID, State: domain.SavedItemInProgress,
		CreatedAt: now, UpdatedAt: now,
	}
	event, err := newEvent(workspaceID, userID, savedItemPayload("saved_item.created", item), now)
	if err != nil {
		return domain.SavedItem{}, err
	}
	item, _, err = m.Store.CreateSavedItem(ctx, item, event)
	if err != nil {
		return domain.SavedItem{}, err
	}
	item.Message = message
	item.SourceAvailable = !message.Deleted
	return item, nil
}

func (m Messages) SavedItemForMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, messageID domain.MessageID) (domain.SavedItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.SavedItem{}, err
	}
	item, err := m.Store.GetSavedItemByMessage(ctx, workspaceID, userID, messageID)
	if err != nil {
		return domain.SavedItem{}, err
	}
	return m.savedItemWithSource(ctx, item)
}

func (m Messages) SavedItemsForMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, messageIDs []domain.MessageID) ([]domain.SavedItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if len(messageIDs) > 200 {
		return nil, store.InvalidArgument("too many saved item message ids")
	}
	return m.Store.ListSavedItemsForMessages(ctx, workspaceID, userID, messageIDs)
}

func (m Messages) SavedItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, state domain.SavedItemState, request domain.PageRequest) (domain.SavedItemPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.SavedItemPage{}, err
	}
	if !state.Valid() {
		return domain.SavedItemPage{}, store.InvalidArgument("saved item state is invalid")
	}
	page, err := m.Store.ListSavedItems(ctx, workspaceID, userID, state, request)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	for index := range page.Items {
		page.Items[index], err = m.savedItemWithSource(ctx, page.Items[index])
		if err != nil {
			return domain.SavedItemPage{}, err
		}
	}
	return page, nil
}

func (m Messages) SetSavedItemState(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID, state domain.SavedItemState) (domain.SavedItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.SavedItem{}, err
	}
	if !state.Valid() {
		return domain.SavedItem{}, store.InvalidArgument("saved item state is invalid")
	}
	item, err := m.Store.GetSavedItem(ctx, workspaceID, userID, id)
	if err != nil {
		return domain.SavedItem{}, err
	}
	if item.State == state {
		return m.savedItemWithSource(ctx, item)
	}
	item.State = state
	item.UpdatedAt = time.Now().UTC()
	event, err := newEvent(workspaceID, userID, savedItemPayload("saved_item.changed", item), item.UpdatedAt)
	if err != nil {
		return domain.SavedItem{}, err
	}
	item, err = m.Store.UpdateSavedItem(ctx, item, event)
	if err != nil {
		return domain.SavedItem{}, err
	}
	return m.savedItemWithSource(ctx, item)
}

func (m Messages) RemoveSavedItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	item, err := m.Store.GetSavedItem(ctx, workspaceID, userID, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, savedItemPayload("saved_item.removed", item), now)
	if err != nil {
		return err
	}
	return m.Store.DeleteSavedItem(ctx, workspaceID, userID, id, event)
}

func (m Messages) savedItemWithSource(ctx context.Context, item domain.SavedItem) (domain.SavedItem, error) {
	item.Message = domain.Message{}
	item.SourceAvailable = false
	if err := m.authorizeConversation(ctx, item.WorkspaceID, item.UserID, item.Conversation); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return item, nil
		}
		return domain.SavedItem{}, err
	}
	message, err := m.Store.GetMessage(ctx, item.MessageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return item, nil
		}
		return domain.SavedItem{}, err
	}
	if message.WorkspaceID != item.WorkspaceID || message.Conversation != item.Conversation || message.Deleted {
		return item, nil
	}
	item.Message = message
	item.SourceAvailable = true
	return item, nil
}

func (m Messages) AddBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, title string, bookmarkType domain.BookmarkType, link, emoji, entityID, accessLevel, parentID string) (domain.Bookmark, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Bookmark{}, err
	}
	title = strings.TrimSpace(title)
	bookmarkType = domain.BookmarkType(strings.TrimSpace(string(bookmarkType)))
	link = strings.TrimSpace(link)
	accessLevel = strings.TrimSpace(accessLevel)
	if title == "" || len(title) > 255 || !bookmarkType.Valid() || link == "" || accessLevel != "" && accessLevel != "read" && accessLevel != "write" {
		return domain.Bookmark{}, ErrInvalidBookmark
	}
	id, err := domain.NewBookmarkID()
	if err != nil {
		return domain.Bookmark{}, err
	}
	now := time.Now().UTC()
	bookmark := domain.Bookmark{ID: id, WorkspaceID: workspaceID, Conversation: conversationID, Title: title, Type: bookmarkType, Link: link, Emoji: strings.TrimSpace(emoji), EntityID: strings.TrimSpace(entityID), AccessLevel: accessLevel, ParentID: domain.BookmarkID(strings.TrimSpace(parentID)), CreatedAt: now, UpdatedAt: now, UpdatedBy: userID}
	event, err := newEvent(workspaceID, userID, bookmarkPayload("bookmark.created", id, conversationID), now)
	if err != nil {
		return domain.Bookmark{}, err
	}
	if err := m.Store.CreateBookmark(ctx, bookmark, event); err != nil {
		return domain.Bookmark{}, err
	}
	return bookmark, nil
}

func (m Messages) EditBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, id domain.BookmarkID, update domain.BookmarkUpdate) (domain.Bookmark, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Bookmark{}, err
	}
	bookmark, err := m.Store.GetBookmark(ctx, workspaceID, conversationID, id)
	if err != nil {
		return domain.Bookmark{}, err
	}
	if update.SetTitle {
		bookmark.Title = strings.TrimSpace(update.Title)
	}
	if update.SetLink {
		bookmark.Link = strings.TrimSpace(update.Link)
	}
	if update.SetEmoji {
		bookmark.Emoji = strings.TrimSpace(update.Emoji)
	}
	if bookmark.Title == "" || len(bookmark.Title) > 255 || !bookmark.Type.Valid() || bookmark.Link == "" {
		return domain.Bookmark{}, ErrInvalidBookmark
	}
	bookmark.UpdatedAt = time.Now().UTC()
	bookmark.UpdatedBy = userID
	event, err := newEvent(workspaceID, userID, bookmarkPayload("bookmark.updated", id, conversationID), bookmark.UpdatedAt)
	if err != nil {
		return domain.Bookmark{}, err
	}
	return m.Store.UpdateBookmark(ctx, bookmark, event)
}

func (m Messages) Bookmarks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) ([]domain.Bookmark, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return nil, err
	}
	return m.Store.ListBookmarks(ctx, workspaceID, conversationID)
}

func (m Messages) RemoveBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, id domain.BookmarkID) error {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, bookmarkPayload("bookmark.removed", id, conversationID), now)
	if err != nil {
		return err
	}
	return m.Store.DeleteBookmark(ctx, workspaceID, conversationID, id, event)
}

func bookmarkPayload(topic string, id domain.BookmarkID, conversationID domain.ConversationID) events.Payload {
	return events.NewPayload(topic, events.String("bookmark_id", string(id)), events.String("channel_id", string(conversationID)))
}

func (m Messages) AddReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, targetID domain.UserID, text string, due time.Time) (domain.Reminder, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Reminder{}, err
	}
	if targetID == "" {
		targetID = userID
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return domain.Reminder{}, store.ErrNotFound
	}
	if _, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, targetID); err != nil {
		return domain.Reminder{}, store.ErrNotFound
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 3000 || due.IsZero() {
		return domain.Reminder{}, ErrInvalidReminder
	}
	id, err := domain.NewReminderID()
	if err != nil {
		return domain.Reminder{}, err
	}
	reminder := domain.Reminder{WorkspaceID: workspaceID, ID: id, Creator: userID, User: targetID, Text: text, Time: due.UTC()}
	event, err := newEvent(workspaceID, userID, events.NewPayload("reminder.created", events.String("reminder_id", string(id)), events.String("user_id", string(targetID))), time.Now().UTC())
	if err != nil {
		return domain.Reminder{}, err
	}
	if err := m.Store.CreateReminder(ctx, reminder, event); err != nil {
		return domain.Reminder{}, err
	}
	return reminder, nil
}

func (m Messages) ReminderInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) (domain.Reminder, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Reminder{}, err
	}
	return m.Store.GetReminder(ctx, workspaceID, userID, reminderID)
}

func (m Messages) Reminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ReminderPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ReminderPage{}, err
	}
	return m.Store.ListReminders(ctx, workspaceID, userID, request)
}

func (m Messages) CompleteReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("reminder.completed", events.String("reminder_id", string(reminderID)), events.String("user_id", string(userID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.CompleteReminder(ctx, workspaceID, userID, reminderID, time.Now().UTC(), event)
}

func (m Messages) DeleteReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("reminder.deleted", events.String("reminder_id", string(reminderID)), events.String("user_id", string(userID))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteReminder(ctx, workspaceID, userID, reminderID, event)
}

func (m Messages) CreateLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.LaterReminderRequest) (domain.LaterReminder, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.LaterReminder{}, err
	}
	normalized, err := m.normalizeLaterReminderRequest(ctx, workspaceID, userID, request)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	id, err := domain.NewLaterReminderID()
	if err != nil {
		return domain.LaterReminder{}, err
	}
	now := time.Now().UTC()
	reminder := domain.LaterReminder{
		ID: id, WorkspaceID: workspaceID, Creator: userID, Target: normalized.Target,
		Channel: normalized.Channel, Text: normalized.Text, DueAt: normalized.DueAt,
		TimeZone: normalized.TimeZone, Recurrence: normalized.Recurrence,
		CreatedAt: now, UpdatedAt: now,
	}
	if normalized.Target == domain.LaterReminderPersonal {
		reminder.UserID = userID
	}
	if normalized.SourceTimestamp != "" {
		message, messageErr := m.messageForTimestamp(ctx, workspaceID, userID, normalized.SourceChannel, normalized.SourceTimestamp)
		if messageErr != nil {
			return domain.LaterReminder{}, messageErr
		}
		reminder.SourceMessageID = message.ID
		reminder.SourceConversation = message.Conversation
		reminder.SourceTimestamp = normalized.SourceTimestamp
	}
	event, err := newEvent(workspaceID, userID, laterReminderPayload("later_reminder.created", reminder), now)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	if err := m.Store.CreateLaterReminder(ctx, reminder, event); err != nil {
		return domain.LaterReminder{}, err
	}
	return reminder, nil
}

func (m Messages) LaterReminderInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.LaterReminderID) (domain.LaterReminder, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.LaterReminder{}, err
	}
	return m.Store.GetLaterReminder(ctx, workspaceID, userID, id)
}

func (m Messages) LaterReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, target domain.LaterReminderTarget, request domain.PageRequest) (domain.LaterReminderPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.LaterReminderPage{}, err
	}
	if !target.Valid() {
		return domain.LaterReminderPage{}, ErrInvalidLaterReminder
	}
	return m.Store.ListLaterReminders(ctx, workspaceID, userID, target, request)
}

func (m Messages) UpdateLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.LaterReminderID, request domain.LaterReminderRequest) (domain.LaterReminder, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.LaterReminder{}, err
	}
	current, err := m.Store.GetLaterReminder(ctx, workspaceID, userID, id)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	if current.Target != domain.LaterReminderPersonal || request.Target != domain.LaterReminderPersonal {
		return domain.LaterReminder{}, ErrInvalidLaterReminder
	}
	normalized, err := m.normalizeLaterReminderRequest(ctx, workspaceID, userID, request)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	current.Text = normalized.Text
	current.DueAt = normalized.DueAt
	current.TimeZone = normalized.TimeZone
	current.Recurrence = normalized.Recurrence
	current.UpdatedAt = time.Now().UTC()
	event, err := newEvent(workspaceID, userID, laterReminderPayload("later_reminder.changed", current), current.UpdatedAt)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	return m.Store.UpdateLaterReminder(ctx, current, event)
}

func (m Messages) AcknowledgeLaterReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"later_reminder.acknowledged",
		events.String("user_id", string(userID)),
	), now)
	if err != nil {
		return err
	}
	return m.Store.AcknowledgeLaterReminders(ctx, workspaceID, userID, now, event)
}

func (m Messages) Activity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query domain.ActivityQuery) (domain.ActivityPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ActivityPage{}, err
	}
	if !query.Valid() {
		return domain.ActivityPage{}, store.InvalidArgument("activity filter is invalid")
	}
	return m.Store.ListActivity(ctx, workspaceID, userID, query)
}

func (m Messages) MutateActivity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ActivityID, mutation domain.ActivityMutation) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	return m.Store.MutateActivity(ctx, workspaceID, userID, ids, mutation, time.Now().UTC())
}

func (m Messages) ActivityPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.ActivityPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ActivityPreferences{}, err
	}
	return m.Store.GetActivityPreferences(ctx, workspaceID, userID)
}

func (m Messages) SetActivityPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, layout domain.ActivityLayout) (domain.ActivityPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ActivityPreferences{}, err
	}
	preferences := domain.ActivityPreferences{WorkspaceID: workspaceID, UserID: userID, Layout: layout}
	if err := m.Store.SetActivityPreferences(ctx, preferences); err != nil {
		return domain.ActivityPreferences{}, err
	}
	return preferences, nil
}

func (m Messages) CompleteLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.LaterReminderID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"later_reminder.completed",
		events.String("reminder_id", string(id)),
		events.String("user_id", string(userID)),
	), now)
	if err != nil {
		return err
	}
	return m.Store.CompleteLaterReminder(ctx, workspaceID, userID, id, now, event)
}

func (m Messages) DeleteLaterReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.LaterReminderID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	reminder, err := m.Store.GetLaterReminder(ctx, workspaceID, userID, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, laterReminderPayload("later_reminder.deleted", reminder), now)
	if err != nil {
		return err
	}
	return m.Store.DeleteLaterReminder(ctx, workspaceID, userID, id, event)
}

func (m Messages) normalizeLaterReminderRequest(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.LaterReminderRequest) (domain.LaterReminderRequest, error) {
	request.Text = strings.TrimSpace(request.Text)
	request.TimeZone = strings.TrimSpace(request.TimeZone)
	request.DueAt = request.DueAt.UTC()
	if !request.Target.Valid() || !request.Recurrence.Valid() || request.Text == "" || len(request.Text) > 3000 || request.DueAt.IsZero() {
		return domain.LaterReminderRequest{}, ErrInvalidLaterReminder
	}
	if !request.DueAt.After(time.Now().UTC()) {
		return domain.LaterReminderRequest{}, ErrReminderTimeInPast
	}
	if request.TimeZone == "" {
		request.TimeZone = "UTC"
	}
	if _, err := time.LoadLocation(request.TimeZone); err != nil {
		return domain.LaterReminderRequest{}, ErrInvalidLaterReminder
	}
	switch request.Target {
	case domain.LaterReminderPersonal:
		if request.Channel != "" {
			return domain.LaterReminderRequest{}, ErrInvalidLaterReminder
		}
		if (request.SourceChannel == "") != (request.SourceTimestamp == "") {
			return domain.LaterReminderRequest{}, ErrInvalidLaterReminder
		}
	case domain.LaterReminderChannel:
		if request.Channel == "" || request.SourceChannel != "" || request.SourceTimestamp != "" {
			return domain.LaterReminderRequest{}, ErrInvalidLaterReminder
		}
		membership, err := m.activeWorkspaceMembership(ctx, workspaceID, userID)
		if err != nil {
			return domain.LaterReminderRequest{}, err
		}
		if membership.Guest() {
			return domain.LaterReminderRequest{}, ErrInvalidLaterReminder
		}
		if err := m.requireConversationMembership(ctx, workspaceID, userID, request.Channel); err != nil {
			return domain.LaterReminderRequest{}, err
		}
	}
	return request, nil
}

func laterReminderPayload(topic string, reminder domain.LaterReminder) events.Payload {
	return events.NewPayload(
		topic,
		events.String("reminder_id", string(reminder.ID)),
		events.String("target", string(reminder.Target)),
		events.String("user_id", string(reminder.UserID)),
		events.String("channel_id", string(reminder.Channel)),
	)
}

func (m Messages) ScheduleMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text string, postAt time.Time) (domain.ScheduledMessage, error) {
	return m.ScheduleMessageWithBlocksAndAttachments(ctx, workspaceID, userID, channel, text, "", "", postAt)
}

func (m Messages) ScheduledMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	return m.ScheduledMessagesForCredential(ctx, workspaceID, userID, domain.ScheduledMessageQuery{
		CredentialHash: InternalScheduledCredential(workspaceID, userID),
		Channel:        channel,
		Page:           request,
	})
}

func (m Messages) ScheduledMessagesForCredential(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query domain.ScheduledMessageQuery) (domain.ScheduledMessagePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	if query.CredentialHash == "" || (!query.Oldest.IsZero() && !query.Latest.IsZero() && !query.Oldest.Before(query.Latest)) {
		return domain.ScheduledMessagePage{}, store.InvalidArgument("scheduled-message token and time range are invalid")
	}
	if query.Channel != "" {
		if err := m.authorizeConversation(ctx, workspaceID, userID, query.Channel); err != nil {
			return domain.ScheduledMessagePage{}, err
		}
	}
	return m.Store.ListScheduledMessagesForCredential(ctx, workspaceID, query)
}

func (m Messages) ScheduledMessageHistory(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, includeDelivered bool, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	return m.Store.ListScheduledMessageHistory(ctx, workspaceID, InternalScheduledCredential(workspaceID, userID), includeDelivered, request)
}

func (m Messages) UpdateScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledMessageID, channel domain.ConversationID, text string, postAt time.Time) (domain.ScheduledMessage, error) {
	if id == "" || channel == "" {
		return domain.ScheduledMessage{}, store.InvalidArgument("scheduled-message id and channel are required")
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, channel); err != nil {
		return domain.ScheduledMessage{}, err
	}
	text = strings.TrimSpace(text)
	if messageTextTooLong(text) || postAt.IsZero() {
		return domain.ScheduledMessage{}, ErrInvalidMessage
	}
	now := time.Now().UTC()
	postAt = time.Unix(postAt.UTC().Unix(), 0).UTC()
	if !postAt.After(now) {
		return domain.ScheduledMessage{}, ErrScheduledTimeInPast
	}
	if postAt.After(now.Add(120 * 24 * time.Hour)) {
		return domain.ScheduledMessage{}, ErrScheduledTimeTooFar
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"message.schedule_updated",
		events.String("scheduled_message_id", string(id)),
		events.String("channel_id", string(channel)),
		events.String("post_at", string(domain.NewMessageTimestamp(postAt))),
	), now)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	value, err := m.Store.UpdateScheduledMessageWithinLimit(ctx, domain.ScheduledMessageUpdate{
		WorkspaceID: workspaceID, ID: id, Channel: channel, Text: text, PostAt: postAt,
		CredentialHash: InternalScheduledCredential(workspaceID, userID),
	}, 5*time.Minute, 30, event)
	if errors.Is(err, store.ErrScheduledMessageLimit) {
		return domain.ScheduledMessage{}, ErrScheduledTooMany
	}
	return value, err
}

func (m Messages) SendScheduledMessageNow(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledMessageID) (domain.Message, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Message{}, err
	}
	if id == "" {
		return domain.Message{}, store.InvalidArgument("scheduled-message id is required")
	}
	owner := "send-now:" + string(userID) + ":" + string(id)
	item, err := m.Store.ClaimScheduledMessageForCredential(ctx, workspaceID, InternalScheduledCredential(workspaceID, userID), id, owner, time.Minute)
	if err != nil {
		return domain.Message{}, err
	}
	message, postErr := m.PostScheduledMessage(ctx, item.WorkspaceID, item.ID)
	if postErr != nil {
		releaseErr := m.Store.ReleaseScheduledMessage(ctx, owner, item.ID, item.PostAt)
		return domain.Message{}, errors.Join(postErr, releaseErr)
	}
	if err := m.Store.MarkScheduledMessageDelivered(ctx, owner, item.ID); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (m Messages) DeleteScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID) error {
	return m.DeleteScheduledMessageForCredential(ctx, workspaceID, userID, InternalScheduledCredential(workspaceID, userID), channel, id)
}

func (m Messages) DeleteScheduledMessageForCredential(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, credentialHash string, channel domain.ConversationID, id domain.ScheduledMessageID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if credentialHash == "" || channel == "" || id == "" {
		return store.InvalidArgument("scheduled-message credential, channel, and id are required")
	}
	// The exact credential coordinate stored with the item is its ownership
	// check. Requiring current conversation visibility here stranded scheduled
	// work after its owner left a private channel, even though the scheduler
	// could still attempt it. The atomic delete below discloses no content.
	event, err := newEvent(workspaceID, userID, events.NewPayload("message.schedule_deleted", events.String("scheduled_message_id", string(id)), events.String("channel_id", string(channel))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteScheduledMessageForCredential(ctx, workspaceID, credentialHash, channel, id, event)
}

func (m Messages) SaveDraft(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, text string) (domain.Draft, error) {
	return m.SaveDraftWithAttachments(ctx, workspaceID, userID, conversation, thread, text, nil)
}

func (m Messages) SaveDraftWithAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, text string, attachments []domain.DraftAttachment) (domain.Draft, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversation); err != nil {
		return domain.Draft{}, err
	}
	if (strings.TrimSpace(text) == "" && len(attachments) == 0) || messageTextTooLong(text) || len(attachments) > 10 {
		return domain.Draft{}, ErrInvalidMessage
	}
	normalizedAttachments, err := m.normalizeComposerAttachments(ctx, workspaceID, userID, attachments)
	if err != nil {
		return domain.Draft{}, err
	}
	if thread != "" {
		createdAt, err := domain.ParseMessageTimestamp(thread)
		if err != nil {
			return domain.Draft{}, ErrInvalidTimestamp
		}
		parent, err := m.Store.GetMessageByCreatedAt(ctx, conversation, createdAt)
		if err != nil || parent.WorkspaceID != workspaceID || parent.Deleted || parent.ThreadTimestamp != "" {
			return domain.Draft{}, store.ErrNotFound
		}
	}
	now := time.Now().UTC()
	value := domain.Draft{
		WorkspaceID: workspaceID, UserID: userID, ConversationID: conversation,
		ThreadTimestamp: thread, Text: text, Attachments: normalizedAttachments, UpdatedAt: now,
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"draft.saved",
		events.String("channel_id", string(conversation)),
		events.String("thread_ts", string(thread)),
	), now)
	if err != nil {
		return domain.Draft{}, err
	}
	return m.Store.UpsertDraft(ctx, value, event)
}

func (m Messages) normalizeComposerAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, attachments []domain.DraftAttachment) ([]domain.DraftAttachment, error) {
	if len(attachments) > 10 {
		return nil, ErrInvalidMessage
	}
	normalizedAttachments := make([]domain.DraftAttachment, 0, len(attachments))
	seenUploads := make(map[domain.ExternalUploadID]struct{}, len(attachments))
	for _, attachment := range attachments {
		attachment.UploadID = domain.ExternalUploadID(strings.TrimSpace(string(attachment.UploadID)))
		attachment.Title = strings.TrimSpace(attachment.Title)
		if attachment.UploadID == "" || len(attachment.Title) > 255 {
			return nil, ErrInvalidExternalUpload
		}
		if _, duplicate := seenUploads[attachment.UploadID]; duplicate {
			return nil, ErrInvalidExternalUpload
		}
		upload, err := m.Store.GetExternalUpload(ctx, attachment.UploadID)
		if err != nil || upload.WorkspaceID != workspaceID || upload.Uploader != userID ||
			upload.Status != domain.ExternalUploadUploaded {
			return nil, ErrInvalidExternalUpload
		}
		if !upload.ExpiresAt.After(time.Now().UTC()) {
			referenced, referenceErr := m.Store.PendingUploadReferenceExists(ctx, workspaceID, userID, attachment.UploadID)
			if referenceErr != nil {
				return nil, referenceErr
			}
			if !referenced {
				return nil, ErrInvalidExternalUpload
			}
		}
		if attachment.Title == "" {
			attachment.Title = upload.Title
		}
		attachment.Name = upload.Name
		attachment.MIMEType = upload.MIMEType
		attachment.Size = upload.Size
		normalizedAttachments = append(normalizedAttachments, attachment)
		seenUploads[attachment.UploadID] = struct{}{}
	}
	return normalizedAttachments, nil
}

func (m Messages) Draft(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.Draft, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.Draft{}, err
	}
	return m.Store.GetDraft(ctx, workspaceID, userID, conversation, thread)
}

func (m Messages) Drafts(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.DraftPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.DraftPage{}, err
	}
	return m.Store.ListDrafts(ctx, workspaceID, userID, request)
}

func (m Messages) DeleteDraft(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload(
		"draft.deleted",
		events.String("channel_id", string(conversation)),
		events.String("thread_ts", string(thread)),
	), now)
	if err != nil {
		return err
	}
	err = m.Store.DeleteDraft(ctx, workspaceID, userID, conversation, thread, event)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (m Messages) SentMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.MessagePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.MessagePage{}, err
	}
	return m.Store.ListAuthoredMessages(ctx, workspaceID, userID, request)
}

func normalizeUserGroupHandle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), "-")
	return value
}

func normalizeUserGroupUsers(values []domain.UserID) ([]domain.UserID, error) {
	seen := make(map[domain.UserID]struct{}, len(values))
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		value = domain.UserID(strings.TrimSpace(string(value)))
		if value == "" {
			return nil, ErrInvalidUserGroup
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result, nil
}

// User group membership is a private-channel key: a conversation restricted with
// admin.conversations.restrictAccess.addGroup admits exactly the members of the
// named groups, so whoever can rewrite a group's membership can admit themselves
// to every channel restricted to it. The mutations therefore require the same
// authority as the restriction that reads them.
//
// The reads below stay at member authority: a directory of groups and their
// members is ordinary workspace information, and @-mentioning a group needs it.
func (m Messages) CreateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, name, handle, description string) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	name = strings.TrimSpace(name)
	handle = normalizeUserGroupHandle(handle)
	description = strings.TrimSpace(description)
	if name == "" {
		return domain.UserGroup{}, ErrInvalidUserGroup
	}
	if handle == "" {
		handle = normalizeUserGroupHandle(name)
	}
	if handle == "" || len(name) > 255 || len(handle) > 255 || len(description) > 2000 {
		return domain.UserGroup{}, ErrInvalidUserGroup
	}
	id, err := domain.NewUserGroupID()
	if err != nil {
		return domain.UserGroup{}, err
	}
	now := time.Now().UTC()
	value := domain.UserGroup{WorkspaceID: workspaceID, ID: id, Name: name, Handle: handle, Description: description, Creator: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now, Enabled: true}
	payload, err := userGroupEventPayload("usergroup.created", value)
	if err != nil {
		return domain.UserGroup{}, err
	}
	event, err := newEvent(workspaceID, actor, payload, now)
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.CreateUserGroup(ctx, value, event); err != nil {
		return domain.UserGroup{}, err
	}
	return value, nil
}

func (m Messages) UpdateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, name, handle, description string) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return domain.UserGroup{}, err
	}
	if strings.TrimSpace(name) != "" {
		value.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(handle) != "" {
		value.Handle = normalizeUserGroupHandle(handle)
	}
	if description != "" {
		value.Description = strings.TrimSpace(description)
	}
	if value.Name == "" || value.Handle == "" || len(value.Name) > 255 || len(value.Handle) > 255 || len(value.Description) > 2000 {
		return domain.UserGroup{}, ErrInvalidUserGroup
	}
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	payload, err := userGroupEventPayload("usergroup.updated", value)
	if err != nil {
		return domain.UserGroup{}, err
	}
	event, err := newEvent(workspaceID, actor, payload, value.UpdatedAt)
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.UpdateUserGroup(ctx, value, event); err != nil {
		return domain.UserGroup{}, err
	}
	return value, nil
}

func (m Messages) SetUserGroupEnabled(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, enabled bool) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return domain.UserGroup{}, err
	}
	value.Enabled = enabled
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	payload, err := userGroupEventPayload("usergroup.enabled_changed", value, events.Bool("enabled", enabled))
	if err != nil {
		return domain.UserGroup{}, err
	}
	event, err := newEvent(workspaceID, actor, payload, value.UpdatedAt)
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.SetUserGroupEnabled(ctx, workspaceID, id, enabled, actor, event); err != nil {
		return domain.UserGroup{}, err
	}
	return m.Store.GetUserGroup(ctx, workspaceID, id)
}

func (m Messages) ListUserGroups(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, includeDisabled bool, request domain.PageRequest) (domain.UserGroupPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.UserGroupPage{}, err
	}
	return m.Store.ListUserGroups(ctx, workspaceID, includeDisabled, request)
}

func (m Messages) UserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID) ([]domain.UserID, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return nil, err
	}
	value, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	return append([]domain.UserID(nil), value.Users...), nil
}

func (m Messages) SetUserGroupUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, users []domain.UserID) (domain.UserGroup, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	normalized, err := normalizeUserGroupUsers(users)
	if err != nil {
		return domain.UserGroup{}, err
	}
	for _, userID := range normalized {
		user, getErr := m.Store.GetUser(ctx, userID)
		if getErr != nil || user.WorkspaceID != workspaceID || user.Deleted {
			return domain.UserGroup{}, store.ErrNotFound
		}
		if _, getErr = m.Store.GetWorkspaceMembership(ctx, workspaceID, userID); getErr != nil {
			return domain.UserGroup{}, store.ErrNotFound
		}
	}
	previous, err := m.Store.GetUserGroup(ctx, workspaceID, id)
	if err != nil {
		return domain.UserGroup{}, err
	}
	snapshot := previous
	snapshot.Users = normalized
	snapshot.UpdatedBy = actor
	snapshot.UpdatedAt = time.Now().UTC()
	added, removed := userIDDeltas(previous.Users, normalized)
	payload, err := userGroupEventPayload("usergroup.users_changed", snapshot,
		events.Strings("added_users", added),
		events.Strings("removed_users", removed),
	)
	if err != nil {
		return domain.UserGroup{}, err
	}
	event, err := newEvent(workspaceID, actor, payload, snapshot.UpdatedAt)
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.SetUserGroupUsers(ctx, workspaceID, id, normalized, actor, event); err != nil {
		return domain.UserGroup{}, err
	}
	return m.Store.GetUserGroup(ctx, workspaceID, id)
}

func normalizeCallUsers(values []domain.UserID) ([]domain.UserID, error) {
	seen := make(map[domain.UserID]struct{}, len(values))
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		value = domain.UserID(strings.TrimSpace(string(value)))
		if value == "" {
			return nil, ErrInvalidCall
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result, nil
}

func (m Messages) validateCallUsers(ctx context.Context, workspaceID domain.WorkspaceID, users []domain.UserID) error {
	for _, userID := range users {
		user, err := m.Store.GetUser(ctx, userID)
		if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
			return store.ErrNotFound
		}
		membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, userID)
		if err != nil || !membership.Active {
			return store.ErrNotFound
		}
	}
	return nil
}

// StartHuddle starts, or joins, the conversation's huddle.
//
// Membership of the conversation is the authority: a huddle happens inside a
// conversation and is visible to the people in it, so being able to read the
// conversation is exactly the right to be in its huddle. AddCall checks neither
// membership nor posting rights because an app-registered call has no
// conversation to check against — the two operations are deliberately not the
// same check.
func (m Messages) StartHuddle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, title string) (domain.Call, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversationID); err != nil {
		return domain.Call{}, err
	}
	id, err := domain.NewCallID()
	if err != nil {
		return domain.Call{}, err
	}
	now := time.Now().UTC()
	value := domain.Call{
		ID: id, WorkspaceID: workspaceID, Kind: domain.CallKindHuddle, ConversationID: conversationID,
		Title: strings.TrimSpace(title), CreatedBy: actor, StartedAt: now,
	}
	started, err := huddleEvent(workspaceID, actor, "huddle.started", id, conversationID, now)
	if err != nil {
		return domain.Call{}, err
	}
	joined, err := huddleEvent(workspaceID, actor, "huddle.joined", id, conversationID, now)
	if err != nil {
		return domain.Call{}, err
	}
	call, _, err := m.Store.StartHuddle(ctx, value, started, joined)
	return call, err
}

// JoinHuddle adds the actor to the conversation's running huddle. It is
// separate from StartHuddle so a client can express "join what is there"
// without the risk of starting a second one if it has just ended.
func (m Messages) JoinHuddle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	call, err := m.activeHuddleFor(ctx, workspaceID, actor, conversationID)
	if err != nil {
		return domain.Call{}, err
	}
	joined, err := huddleEvent(workspaceID, actor, "huddle.joined", call.ID, conversationID, time.Now().UTC())
	if err != nil {
		return domain.Call{}, err
	}
	return m.Store.JoinCall(ctx, workspaceID, call.ID, actor, joined)
}

// LeaveHuddle removes the actor. The store ends the huddle when the last person
// leaves, in the same transaction, so a conversation never shows a huddle
// nobody is in.
func (m Messages) LeaveHuddle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	call, err := m.activeHuddleFor(ctx, workspaceID, actor, conversationID)
	if err != nil {
		return domain.Call{}, err
	}
	now := time.Now().UTC()
	left, err := huddleEvent(workspaceID, actor, "huddle.left", call.ID, conversationID, now)
	if err != nil {
		return domain.Call{}, err
	}
	ended, err := huddleEvent(workspaceID, actor, "huddle.ended", call.ID, conversationID, now)
	if err != nil {
		return domain.Call{}, err
	}
	return m.Store.LeaveCall(ctx, workspaceID, call.ID, actor, left, ended)
}

// EndHuddle ends it for everyone. Only the person who started it or a workspace
// administrator may: ending a huddle removes everyone else from it, which is
// not a thing any participant should be able to do to the others.
func (m Messages) EndHuddle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	call, err := m.activeHuddleFor(ctx, workspaceID, actor, conversationID)
	if err != nil {
		return domain.Call{}, err
	}
	if call.CreatedBy != actor {
		if adminErr := m.requireWorkspaceAdmin(ctx, workspaceID, actor); adminErr != nil {
			return domain.Call{}, ErrHuddleNotOwned
		}
	}
	now := time.Now().UTC()
	ended, err := huddleEvent(workspaceID, actor, "huddle.ended", call.ID, conversationID, now)
	if err != nil {
		return domain.Call{}, err
	}
	duration := int64(now.Sub(call.StartedAt).Seconds())
	if duration < 0 {
		duration = 0
	}
	if err := m.Store.EndCall(ctx, workspaceID, call.ID, duration, ended); err != nil {
		return domain.Call{}, err
	}
	return m.Store.GetCall(ctx, workspaceID, call.ID)
}

// ActiveHuddle reports the conversation's running huddle, or ErrNotFound.
func (m Messages) ActiveHuddle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	return m.activeHuddleFor(ctx, workspaceID, actor, conversationID)
}

func (m Messages) activeHuddleFor(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID) (domain.Call, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversationID); err != nil {
		return domain.Call{}, err
	}
	return m.Store.ActiveHuddle(ctx, workspaceID, conversationID)
}

// SendCallSignal carries one peer's half of the WebRTC handshake to another.
// The server never reads the payload: it is an SDP description or an ICE
// candidate that means something to the two browsers and nothing here.
//
// What the server does decide is who may send one to whom. Both the sender and
// the recipient have to be in the call right now, which is what stops the
// signalling path being a way to reach somebody who is not in the huddle, or to
// keep reaching one who has left.
func (m Messages) SendCallSignal(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, callID domain.CallID, recipient domain.UserID, kind domain.CallSignalKind, payload string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	if !kind.Valid() || recipient == "" || recipient == actor {
		return ErrInvalidCall
	}
	if payload == "" || len(payload) > domain.CallSignalCeiling {
		return ErrInvalidCall
	}
	call, err := m.Store.GetCall(ctx, workspaceID, callID)
	if err != nil {
		return err
	}
	if !call.EndedAt.IsZero() {
		return ErrInvalidCall
	}
	if !slices.Contains(call.Participants, actor) || !slices.Contains(call.Participants, recipient) {
		return ErrInvalidCall
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("huddle.signal",
		events.String("call_id", string(callID)),
		// user_id is the recipient, because that is the field the event stream
		// filters a recipient-scoped topic on.
		events.String("user_id", string(recipient)),
		events.String("from_user_id", string(actor)),
		events.String("signal", string(kind)),
		events.String("payload", payload),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.AppendEvent(ctx, event)
}

func huddleEvent(workspaceID domain.WorkspaceID, actor domain.UserID, topic string, id domain.CallID, conversationID domain.ConversationID, at time.Time) (events.Event, error) {
	return newEvent(workspaceID, actor, events.NewPayload(topic,
		events.String("call_id", string(id)),
		events.String("channel_id", string(conversationID)),
		events.String("user_id", string(actor)),
	), at)
}

func (m Messages) AddCall(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, externalUniqueID, externalDisplayID, joinURL, desktopAppJoinURL, title string, startedAt time.Time, users []domain.UserID) (domain.Call, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Call{}, err
	}
	externalUniqueID, externalDisplayID, joinURL, desktopAppJoinURL, title = strings.TrimSpace(externalUniqueID), strings.TrimSpace(externalDisplayID), strings.TrimSpace(joinURL), strings.TrimSpace(desktopAppJoinURL), strings.TrimSpace(title)
	if externalUniqueID == "" || joinURL == "" {
		return domain.Call{}, ErrInvalidCall
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	} else {
		startedAt = startedAt.UTC()
	}
	normalized, err := normalizeCallUsers(users)
	if err != nil {
		return domain.Call{}, err
	}
	// The people a call would put together must not include two the barrier
	// separates. The caller is one of them: starting a call is joining it.
	separated, err := m.barrierSeparates(ctx, workspaceID, append([]domain.UserID{actor}, normalized...), domain.BarrierSubjectCall)
	if err != nil {
		return domain.Call{}, err
	}
	if separated {
		return domain.Call{}, ErrBarrieredFromMember
	}
	if err := m.validateCallUsers(ctx, workspaceID, normalized); err != nil {
		return domain.Call{}, err
	}
	id, err := domain.NewCallID()
	if err != nil {
		return domain.Call{}, err
	}
	value := domain.Call{ID: id, WorkspaceID: workspaceID, Kind: domain.CallKindExternal, ExternalUniqueID: externalUniqueID, ExternalDisplayID: externalDisplayID, JoinURL: joinURL, DesktopAppJoinURL: desktopAppJoinURL, Title: title, CreatedBy: actor, Participants: normalized, StartedAt: startedAt}
	event, err := newEvent(workspaceID, actor, events.NewPayload("call.created", events.String("call_id", string(id))), time.Now().UTC())
	if err != nil {
		return domain.Call{}, err
	}
	if err := m.Store.CreateCall(ctx, value, event); err != nil {
		return domain.Call{}, err
	}
	return value, nil
}

func (m Messages) GetCall(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.CallID) (domain.Call, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Call{}, err
	}
	return m.Store.GetCall(ctx, workspaceID, id)
}

func (m Messages) UpdateCall(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.CallID, title, joinURL, desktopAppJoinURL string) (domain.Call, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Call{}, err
	}
	value, err := m.Store.GetCall(ctx, workspaceID, id)
	if err != nil {
		return domain.Call{}, err
	}
	value.Title, value.JoinURL, value.DesktopAppJoinURL = strings.TrimSpace(title), strings.TrimSpace(joinURL), strings.TrimSpace(desktopAppJoinURL)
	if value.Title == "" && value.JoinURL == "" && value.DesktopAppJoinURL == "" {
		return domain.Call{}, ErrInvalidCall
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("call.updated", events.String("call_id", string(id))), time.Now().UTC())
	if err != nil {
		return domain.Call{}, err
	}
	if err := m.Store.UpdateCall(ctx, value, event); err != nil {
		return domain.Call{}, err
	}
	return m.Store.GetCall(ctx, workspaceID, id)
}

func (m Messages) EndCall(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.CallID, duration int64) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	if duration < 0 {
		return ErrInvalidCall
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("call.ended", events.String("call_id", string(id)), events.Int("duration", duration)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.EndCall(ctx, workspaceID, id, duration, event)
}

func (m Messages) changeCallParticipants(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.CallID, users []domain.UserID, add bool) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	value, err := m.Store.GetCall(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	changed, err := normalizeCallUsers(users)
	if err != nil {
		return err
	}
	if err := m.validateCallUsers(ctx, workspaceID, changed); err != nil {
		return err
	}
	if add {
		// A barrier stops a call as surely as it stops a direct message, and
		// the people who must not meet are everybody who would be on the call
		// together - the ones already there as well as the ones joining.
		together := append(append([]domain.UserID{}, value.Participants...), changed...)
		separated, err := m.barrierSeparates(ctx, workspaceID, together, domain.BarrierSubjectCall)
		if err != nil {
			return err
		}
		if separated {
			return ErrBarrieredFromMember
		}
	}
	set := make(map[domain.UserID]struct{}, len(value.Participants)+len(changed))
	if add {
		for _, userID := range value.Participants {
			set[userID] = struct{}{}
		}
		for _, userID := range changed {
			set[userID] = struct{}{}
		}
	} else {
		for _, userID := range value.Participants {
			set[userID] = struct{}{}
		}
		for _, userID := range changed {
			delete(set, userID)
		}
	}
	result := make([]domain.UserID, 0, len(set))
	for userID := range set {
		result = append(result, userID)
	}
	result, err = normalizeCallUsers(result)
	if err != nil && len(result) != 0 {
		return err
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("call.participants_changed", events.String("call_id", string(id)), events.Strings("user_ids", userIDStrings(result))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetCallParticipants(ctx, workspaceID, id, result, event)
}

func (m Messages) AddCallParticipants(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.CallID, users []domain.UserID) error {
	return m.changeCallParticipants(ctx, workspaceID, actor, id, users, true)
}
func (m Messages) RemoveCallParticipants(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.CallID, users []domain.UserID) error {
	return m.changeCallParticipants(ctx, workspaceID, actor, id, users, false)
}

func (m Messages) messageForTimestamp(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.Message, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Message{}, err
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return domain.Message{}, ErrInvalidTimestamp
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, conversationID, createdAt)
	if err != nil {
		return domain.Message{}, err
	}
	if message.WorkspaceID != workspaceID {
		return domain.Message{}, store.ErrNotFound
	}
	return message, nil
}

func (m Messages) Permalink(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp) (string, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return "", err
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return "", ErrInvalidTimestamp
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, conversation, createdAt)
	if err != nil || message.WorkspaceID != workspaceID {
		return "", store.ErrNotFound
	}
	canonical := domain.NewMessageTimestamp(message.CreatedAt)
	// Slack's shape is <origin>/archives/<channel>/p<ts without the dot>, and
	// internal/web serves exactly that path, redirecting into the window that
	// contains the message.
	//
	// The origin is deliberately omitted rather than guessed. This used to
	// return https://sameoldchat.local/..., a host that exists nowhere, so
	// every permalink the product handed out was unfollowable. The service
	// does not know what origin served the request — the web and API handlers
	// do — and a relative permalink resolves correctly against whichever one
	// did, which is the honest answer available here.
	return "/archives/" + url.PathEscape(string(conversation)) + "/p" + strings.ReplaceAll(string(canonical), ".", ""), nil
}

func (m Messages) PostEphemeral(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, recipientID domain.UserID, text string) (domain.EphemeralMessage, error) {
	return m.PostEphemeralWithBlocksAndAttachments(ctx, workspaceID, authorID, conversation, recipientID, text, "", "", "")
}

func (m Messages) RecordAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ip, userAgent string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return store.ErrNotFound
	}
	ip, userAgent = strings.TrimSpace(ip), strings.TrimSpace(userAgent)
	if len(ip) > 128 || len(userAgent) > 1024 {
		return ErrInvalidAccessLog
	}
	return m.Store.RecordAccess(ctx, domain.AccessLog{WorkspaceID: workspaceID, UserID: userID, Username: user.Name, CreatedAt: time.Now().UTC(), IP: ip, UserAgent: userAgent})
}

// AnalyticsBusiestChannels bounds the busiest-channel list the dashboard asks
// for. It is a product decision, not a store one, so it lives here.
const AnalyticsBusiestChannels = 10

// WorkspaceAnalytics reports what the workspace holds and what has happened in
// it since an instant the caller chooses. It is administrative: the counts span
// private conversations the actor is not in, which is why it requires the
// workspace administrator role rather than mere membership.
// UserWorkspaces lists the workspaces the actor may switch into. It resolves by
// the actor's own verified address, and it is deliberately readable only about
// oneself: which workspaces a given address belongs to is exactly the fact a
// directory of one deployment must not hand out about other people.
func (m Messages) UserWorkspaces(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) ([]domain.WorkspaceMembershipSummary, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	user, err := m.Store.GetUser(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if user.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	return m.Store.ListWorkspacesForEmail(ctx, user.Email)
}

func (m Messages) WorkspaceAnalytics(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, since time.Time) (domain.WorkspaceAnalytics, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.WorkspaceAnalytics{}, err
	}
	return m.Store.WorkspaceAnalytics(ctx, workspaceID, since.UTC(), AnalyticsBusiestChannels)
}

func (m Messages) ListAccessLogs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, before time.Time, limit, page int) ([]domain.AccessLog, bool, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, false, err
	}
	return m.Store.ListAccessLogs(ctx, workspaceID, before, limit, page)
}

func (m Messages) Post(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, text string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	return m.PostWithBlocksAndAttachments(ctx, workspaceID, authorID, conversation, text, "", "", threadTimestamp, idempotencyKey, "")
}

// ShareFile publishes an existing upload as durable conversation content.
// The file/share/message/outbox mutation is atomic in every storage profile:
// callers never observe an orphan share when creating the message fails.
func (m Messages) ShareFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID, conversationID domain.ConversationID, threadTimestamp domain.MessageTimestamp) (domain.Message, error) {
	if fileID == "" || conversationID == "" {
		return domain.Message{}, ErrInvalidFile
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Message{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Message{}, store.ErrNotFound
	}
	if conversation.Archived {
		return domain.Message{}, ErrConversationAlreadyArchived
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID || file.Uploader != userID || file.Deleted {
		return domain.Message{}, store.ErrNotFound
	}
	threadTimestampValue := domain.MessageTimestamp("")
	if threadTimestamp != "" {
		createdAt, parseErr := domain.ParseMessageTimestamp(threadTimestamp)
		if parseErr != nil {
			return domain.Message{}, ErrInvalidTimestamp
		}
		parent, lookupErr := m.Store.GetMessageByCreatedAt(ctx, conversationID, createdAt)
		if lookupErr != nil || parent.WorkspaceID != workspaceID {
			return domain.Message{}, store.ErrNotFound
		}
		threadTimestampValue = threadTimestamp
	}
	id, err := domain.NewMessageID()
	if err != nil {
		return domain.Message{}, err
	}
	message := domain.Message{
		ID: id, WorkspaceID: workspaceID, Conversation: conversationID, AuthorID: userID,
		ThreadTimestamp: threadTimestampValue, CreatedAt: domain.MessageInstant(time.Now()),
		Files: []domain.File{file},
	}
	for {
		messageEvent, eventErr := messageEventAt(workspaceID, "message.created", message, nil, message.CreatedAt)
		if eventErr != nil {
			return domain.Message{}, eventErr
		}
		sharedFile := file
		if !slices.Contains(sharedFile.SharedChannels, conversationID) {
			sharedFile.SharedChannels = append(sharedFile.SharedChannels, conversationID)
		}
		shareEvent, eventErr := fileEventAt(workspaceID, userID, "file.shared", sharedFile, conversationID, message.CreatedAt)
		if eventErr != nil {
			return domain.Message{}, eventErr
		}
		if err := m.Store.CreateFileShareMessage(ctx, []domain.FileID{fileID}, message, []events.Event{messageEvent, shareEvent}); errors.Is(err, store.ErrMessageTimestampTaken) {
			message.CreatedAt = message.CreatedAt.Add(time.Microsecond)
			continue
		} else if err != nil {
			return domain.Message{}, err
		}
		return m.Store.GetMessage(ctx, message.ID)
	}
}

func (m Messages) AdminCreateIncomingWebhook(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, conversationID domain.ConversationID, botUserID domain.UserID) (domain.IncomingWebhook, string, error) {
	if strings.TrimSpace(string(appID)) == "" {
		return domain.IncomingWebhook{}, "", ErrInvalidMessage
	}
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	if err := m.authorizeConversation(ctx, workspaceID, actorID, conversationID); err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	installations, err := m.Store.ListAppInstallations(ctx, appID)
	if err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	installed := false
	for _, installation := range installations {
		if installation.WorkspaceID == workspaceID && installation.Enabled {
			installed = true
			break
		}
	}
	if !installed {
		return domain.IncomingWebhook{}, "", store.ErrNotFound
	}
	bot, err := m.Store.GetUser(ctx, botUserID)
	if err != nil || bot.WorkspaceID != workspaceID || bot.Deleted {
		return domain.IncomingWebhook{}, "", store.ErrNotFound
	}
	appBot, err := m.Store.GetBotByApp(ctx, workspaceID, appID)
	if err != nil || appBot.UserID != botUserID || appBot.Deleted {
		return domain.IncomingWebhook{}, "", store.ErrNotFound
	}
	// The generated hook posts as the installed app's bot. Creating a hook that
	// only the administering human can reach produces a URL that can never
	// succeed, and accepting another app's bot user is cross-app impersonation.
	if err := m.requireConversationMembership(ctx, workspaceID, botUserID, conversationID); err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.IncomingWebhook{}, "", store.ErrNotFound
	}
	if conversation.Archived {
		return domain.IncomingWebhook{}, "", ErrConversationAlreadyArchived
	}
	secret, err := domain.PublicID("whsec_")
	if err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	id, err := domain.NewIncomingWebhookID()
	if err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	value := domain.IncomingWebhook{ID: id, WorkspaceID: workspaceID, AppID: appID, ConversationID: conversationID, UserID: botUserID, SecretHash: domain.HashToken(secret), Enabled: true, CreatedAt: time.Now().UTC()}
	if err := m.Store.CreateIncomingWebhook(ctx, value); err != nil {
		return domain.IncomingWebhook{}, "", err
	}
	return value, secret, nil
}

func (m Messages) AdminSetIncomingWebhookEnabled(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, webhookID domain.IncomingWebhookID, enabled bool) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("incoming_webhook.enabled", events.String("incoming_webhook_id", string(webhookID)), events.Bool("enabled", enabled)), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetIncomingWebhookEnabled(ctx, workspaceID, webhookID, enabled, event)
}

func (m Messages) PostIncomingWebhook(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret, text, blocks string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	return m.PostIncomingWebhookWithAttachments(ctx, workspaceID, appID, secret, text, blocks, "", threadTimestamp, idempotencyKey)
}

func (m Messages) Unfurl(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, unfurls map[string]string) (domain.Message, error) {
	message, err := m.messageForMutation(ctx, workspaceID, userID, conversation, timestamp)
	if err != nil {
		return domain.Message{}, err
	}
	if message.Deleted {
		return domain.Message{}, ErrMessageAlreadyDeleted
	}
	if messageUnfurlsTooLong(unfurls) {
		return domain.Message{}, ErrInvalidMessage
	}
	normalized, err := domain.NormalizeUnfurls(unfurls)
	if err != nil {
		return domain.Message{}, ErrInvalidMessage
	}
	message.Unfurls = normalized
	event, err := messageEvent(workspaceID, "message.unfurled", message)
	if err != nil {
		return domain.Message{}, err
	}
	if err := m.Store.UpdateMessage(ctx, message, event); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (m Messages) Update(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, text string) (domain.Message, error) {
	return m.UpdateWithBlocksAndAttachments(ctx, workspaceID, userID, conversation, timestamp, text, "", "")
}

func (m Messages) Delete(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp) (domain.Message, error) {
	message, err := m.messageForMutation(ctx, workspaceID, userID, conversation, timestamp)
	if err != nil {
		return domain.Message{}, err
	}
	if message.Deleted {
		return domain.Message{}, ErrMessageAlreadyDeleted
	}
	previous := message
	message.Deleted = true
	event, err := messageMutationEvent(workspaceID, "message.deleted", message, previous)
	if err != nil {
		return domain.Message{}, err
	}
	// Deleting the message retracts the shares it carried, so every file on it
	// is a candidate for file.unshared. The store journals only the candidates
	// whose share actually ended; see store.Store.DeleteMessage.
	unshares := make([]store.FileUnshare, 0, len(message.Files))
	for _, file := range message.Files {
		unshared := file
		unshared.SharedChannels = slices.DeleteFunc(append([]domain.ConversationID(nil), file.SharedChannels...),
			func(channel domain.ConversationID) bool { return channel == conversation })
		unshareEvent, eventErr := fileEventAt(workspaceID, userID, "file.unshared", unshared, conversation, time.Now().UTC())
		if eventErr != nil {
			return domain.Message{}, eventErr
		}
		unshares = append(unshares, store.FileUnshare{FileID: file.ID, Event: unshareEvent})
	}
	if err := m.Store.DeleteMessage(ctx, message, event, unshares); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (m Messages) messageForMutation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp) (domain.Message, error) {
	if strings.TrimSpace(string(conversation)) == "" {
		return domain.Message{}, ErrInvalidMessage
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return domain.Message{}, ErrInvalidTimestamp
	}
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.Message{}, err
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, conversation, createdAt)
	if err != nil || message.WorkspaceID != workspaceID {
		return domain.Message{}, store.ErrNotFound
	}
	if message.AuthorID != userID {
		return domain.Message{}, ErrMessageNotOwned
	}
	return message, nil
}

// newEvent mints an event identifier and builds a durable record from a typed
// payload. Every producer in this package goes through it, so no call site can
// hand a bare identifier to the journal: events.New only accepts an
// events.Payload, which cannot be built from a string.
func newEvent(workspaceID domain.WorkspaceID, actorID domain.UserID, payload events.Payload, createdAt time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, workspaceID, actorID, payload, createdAt)
}

func messageEvent(workspaceID domain.WorkspaceID, topic string, message domain.Message) (events.Event, error) {
	return messageEventAt(workspaceID, topic, message, nil, time.Now().UTC())
}

func messageMutationEvent(workspaceID domain.WorkspaceID, topic string, message, previous domain.Message) (events.Event, error) {
	return messageEventAt(workspaceID, topic, message, &previous, time.Now().UTC())
}

func messageEventAt(workspaceID domain.WorkspaceID, topic string, message domain.Message, previous *domain.Message, createdAt time.Time) (events.Event, error) {
	event, err := newEvent(workspaceID, message.AuthorID, messagePayload(topic, message), createdAt)
	if err != nil {
		return events.Event{}, err
	}
	event.PrivatePayload, err = encodeMessageEventSnapshot(message, previous)
	if err != nil {
		return events.Event{}, err
	}
	return event, nil
}

func fileEventAt(workspaceID domain.WorkspaceID, userID domain.UserID, topic string, file domain.File, channelID domain.ConversationID, createdAt time.Time) (events.Event, error) {
	fields := []events.Field{events.String("file_id", string(file.ID))}
	if channelID != "" {
		fields = append(fields, events.String("channel_id", string(channelID)), events.String("user_id", string(userID)))
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload(topic, fields...), createdAt)
	if err != nil {
		return events.Event{}, err
	}
	event.PrivatePayload, err = encodeFileEventSnapshot(file, channelID, userID)
	if err != nil {
		return events.Event{}, err
	}
	return event, nil
}

// messagePayload deliberately omits the message text. The durable journal is
// read by every workspace member's event stream and by every configured event
// consumer, so the payload carries the identifiers needed to fetch the message
// through an authorized read rather than the content itself.
func messagePayload(topic string, message domain.Message) events.Payload {
	fields := []events.Field{
		events.String("message_id", string(message.ID)),
		events.String("channel_id", string(message.Conversation)),
		events.String("user_id", string(message.AuthorID)),
		events.String("ts", string(domain.NewMessageTimestamp(message.CreatedAt))),
	}
	if message.ThreadTimestamp != "" {
		fields = append(fields, events.String("thread_ts", string(message.ThreadTimestamp)))
	}
	return events.NewPayload(topic, fields...)
}

// userIDDeltas reports which members a new set adds and removes relative to
// the previous one, in the order the sets carry them; Slack's
// subteam_members_changed speaks in deltas, not whole sets.
func userIDDeltas(previous, next []domain.UserID) (added, removed []string) {
	added, removed = []string{}, []string{}
	before := make(map[domain.UserID]struct{}, len(previous))
	for _, id := range previous {
		before[id] = struct{}{}
	}
	after := make(map[domain.UserID]struct{}, len(next))
	for _, id := range next {
		after[id] = struct{}{}
	}
	for _, id := range next {
		if _, existed := before[id]; !existed {
			added = append(added, string(id))
		}
	}
	for _, id := range previous {
		if _, remains := after[id]; !remains {
			removed = append(removed, string(id))
		}
	}
	return added, removed
}

func conversationEvent(workspaceID domain.WorkspaceID, topic string, conversationID domain.ConversationID) (events.Event, error) {
	return newEvent(workspaceID, "", conversationPayload(topic, conversationID), time.Now().UTC())
}

func conversationPayload(topic string, conversationID domain.ConversationID) events.Payload {
	return events.NewPayload(topic, events.String("channel_id", string(conversationID)))
}

// conversationLifecycleEvent snapshots the workspace-visible facts the Slack
// channel lifecycle events carry: the name, the privacy that selects between
// the channel_* and group_* vocabularies, and the acting user. `created` is
// recorded only where the event IS the creation, because the domain
// conversation carries no creation instant of its own.
// conversationNotice builds the message a conversation change posts into the
// conversation — Slack's channel_join, channel_leave, channel_topic,
// channel_purpose and channel_name messages. It is written by the same store
// call as the change it describes, so the two commit together.
//
// The text is the Slack phrasing, with the actor as a mention the client
// resolves like any other: the notice is an ordinary durable message that
// happens to carry a subtype, not a second rendering path.
func conversationNotice(workspaceID domain.WorkspaceID, conversationID domain.ConversationID, actorID domain.UserID, subtype domain.MessageSubtype, detail string) (domain.Message, error) {
	id, err := domain.NewMessageID()
	if err != nil {
		return domain.Message{}, err
	}
	text := ""
	switch subtype {
	case domain.MessageSubtypeChannelJoin:
		text = "<@" + string(actorID) + "> has joined the channel"
	case domain.MessageSubtypeChannelLeave:
		text = "<@" + string(actorID) + "> has left the channel"
	case domain.MessageSubtypeChannelTopic:
		text = "<@" + string(actorID) + "> set the channel topic: " + detail
		if strings.TrimSpace(detail) == "" {
			text = "<@" + string(actorID) + "> cleared the channel topic"
		}
	case domain.MessageSubtypeChannelPurpose:
		text = "<@" + string(actorID) + "> set the channel purpose: " + detail
		if strings.TrimSpace(detail) == "" {
			text = "<@" + string(actorID) + "> cleared the channel purpose"
		}
	case domain.MessageSubtypeChannelName:
		text = "<@" + string(actorID) + "> renamed the channel to " + detail
	default:
		return domain.Message{}, ErrInvalidMessage
	}
	return domain.Message{
		ID: id, WorkspaceID: workspaceID, Conversation: conversationID, AuthorID: actorID,
		Text: text, Subtype: subtype, CreatedAt: domain.MessageInstant(time.Now()),
		// Normalized exactly as a composed message is, so the two storage
		// profiles cannot disagree about what an empty attachment list is.
		Attachments: "[]",
	}, nil
}

func conversationLifecycleEvent(workspaceID domain.WorkspaceID, topic string, conversation domain.Conversation, actorID domain.UserID) (events.Event, error) {
	now := time.Now().UTC()
	fields := []events.Field{
		events.String("channel_id", string(conversation.ID)),
		events.String("name", conversation.Name),
		events.Bool("is_private", conversation.PrivateFlag()),
	}
	if conversation.Kind == domain.ConversationTypeMPIM {
		fields = append(fields, events.Bool("is_mpim", true))
	}
	if topic == "conversation.created" || topic == "conversation.direct_created" {
		fields = append(fields, events.Int("created", now.Unix()))
	}
	if actorID != "" {
		fields = append(fields, events.String("user_id", string(actorID)))
	}
	return newEvent(workspaceID, actorID, events.NewPayload(topic, fields...), now)
}

// subteamJSON renders the subteam object the subteam_* events carry, in the
// field vocabulary usergroups.* responses already use.
func subteamJSON(group domain.UserGroup) (string, error) {
	object := map[string]any{
		"id":           group.ID,
		"team_id":      group.WorkspaceID,
		"is_usergroup": true,
		"name":         group.Name,
		"handle":       group.Handle,
		"description":  group.Description,
		"date_create":  group.CreatedAt.Unix(),
		"date_update":  group.UpdatedAt.Unix(),
		"created_by":   group.Creator,
		"updated_by":   group.UpdatedBy,
		"user_count":   len(group.Users),
	}
	if !group.DeletedAt.IsZero() {
		object["date_delete"] = group.DeletedAt.Unix()
	}
	encoded, err := json.Marshal(object)
	return string(encoded), err
}

func userGroupEventPayload(topic string, group domain.UserGroup, extra ...events.Field) (events.Payload, error) {
	encoded, err := subteamJSON(group)
	if err != nil {
		return events.Payload{}, err
	}
	fields := append([]events.Field{
		events.String("usergroup_id", string(group.ID)),
		events.String("team_id", string(group.WorkspaceID)),
		events.String("handle", group.Handle),
		events.JSON("subteam", encoded),
	}, extra...)
	return events.NewPayload(topic, fields...), nil
}

// dndEventPayload snapshots the dnd_status fields the dnd_updated event
// carries, mirroring the dnd.info response vocabulary.
func dndEventPayload(topic string, userID domain.UserID, value domain.DoNotDisturb, now time.Time) events.Payload {
	fields := []events.Field{
		events.String("user_id", string(userID)),
		events.Bool("dnd_enabled", value.Enabled),
		events.Bool("snooze_enabled", value.SnoozeEnabled(now)),
	}
	if !value.NextStartAt.IsZero() {
		fields = append(fields, events.Int("next_dnd_start_ts", value.NextStartAt.Unix()))
	}
	if !value.NextEndAt.IsZero() {
		fields = append(fields, events.Int("next_dnd_end_ts", value.NextEndAt.Unix()))
	}
	if value.SnoozeEnabled(now) {
		fields = append(fields, events.Int("snooze_endtime", value.SnoozeUntil.Unix()))
	}
	return events.NewPayload(topic, fields...)
}

func (m Messages) authorizeConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return store.ErrNotFound
	}
	if conversation.PrivateFlag() {
		member, err := m.Store.IsConversationMember(ctx, conversationID, userID)
		if err != nil || !member {
			return store.ErrNotFound
		}
		if err := m.authorizeConversationAccessGroups(ctx, workspaceID, userID, conversationID); err != nil {
			return err
		}
	}
	return nil
}

// requireConversationMembership is authorizeConversation plus the membership a
// public channel does not require for a read.
//
// It is the check for an operation that acts on a conversation AS one of its
// members: posting into it, renaming it, changing its topic or purpose,
// inviting, kicking, leaving, marking it read. Those are exactly the operations
// whose pinned enums declare `not_in_channel`; see ErrNotInConversation.
func (m Messages) requireConversationMembership(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	member, err := m.Store.IsConversationMember(ctx, conversationID, userID)
	if err != nil {
		return err
	}
	if !member {
		return ErrNotInConversation
	}
	return nil
}

// authorizeConversationAccessGroups enforces the restriction
// admin.conversations.restrictAccess.addGroup writes.
//
// The grants had a writer and a lister and no reader: an operator could restrict
// a private channel to a user group, the API answered ok, the restriction read
// back — and nothing consulted it, so membership alone still decided. That is
// the same defect the list and canvas access grants had, left in place one file
// over, and it is worse than an absent feature because the operator believes a
// control exists.
//
// When a private conversation names access groups, a member must additionally
// belong to one of them. An empty set restricts nothing, which is the state
// every conversation starts in.
func (m Messages) authorizeConversationAccessGroups(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	groups, err := m.Store.ListConversationAccessGroups(ctx, workspaceID, conversationID)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}
	for _, groupID := range groups {
		group, err := m.Store.GetUserGroup(ctx, workspaceID, groupID)
		if err != nil {
			// A restriction naming a group that can no longer be read must not
			// resolve to "unrestricted": a deleted or unreadable group withholds
			// access rather than granting it.
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		if !group.Enabled {
			continue
		}
		for _, member := range group.Users {
			if member == userID {
				return nil
			}
		}
	}
	return store.ErrNotFound
}

// activeWorkspaceMembership resolves the durable membership an authority
// decision is made from. It is the single place membership is established, so
// authorizeWorkspace, requireWorkspaceAdmin and WorkspaceMembership cannot
// disagree about what "belongs to this workspace" means.
//
// A user who is absent, deleted, in another workspace, or whose membership is
// inactive is indistinguishable from a user who does not exist: the caller has
// proved nothing about this workspace, so it learns nothing about it.
// refuseGuest is the one place that answers "may a guest do this themselves?".
//
// Guests reach channels by being added to them, never by naming one: a
// single-channel guest belongs to the one channel they were invited to, and a
// multi-channel guest to the ones members put them in. Neither browses the
// workspace. Creating and joining both let a guest reach a channel nobody
// invited them to, so both ask here rather than each growing its own rule.
func (m Messages) refuseGuest(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	membership, err := m.activeWorkspaceMembership(ctx, workspaceID, userID)
	if err != nil {
		return err
	}
	switch {
	case membership.UltraRestricted:
		return ErrUserIsUltraRestricted
	case membership.Restricted:
		return ErrUserIsRestricted
	}
	return nil
}

func (m Messages) activeWorkspaceMembership(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceMembership, error) {
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return domain.WorkspaceMembership{}, err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.WorkspaceMembership{}, store.ErrNotFound
	}
	membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, userID)
	if err != nil || !membership.Active {
		return domain.WorkspaceMembership{}, store.ErrNotFound
	}
	return membership, nil
}

// authorizeWorkspace admits any active member of the workspace. It is the check
// for an operation whose authority is "you work here": reading the directory,
// posting, managing your own profile.
//
// It is NOT the check for an operation that acts on the workspace itself or on
// another member. Those use requireWorkspaceAdmin.
func (m Messages) authorizeWorkspace(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	_, err := m.activeWorkspaceMembership(ctx, workspaceID, userID)
	return err
}

// requireWorkspaceAdmin admits only an active administrator or owner.
//
// Every administrative method used to call authorizeWorkspace, which verifies
// workspace MEMBERSHIP and not ROLE. A plain member holding admin.users:write —
// which a browser session used to be minted with unconditionally — could
// therefore call SetUserRole on their own user and become an owner. The role, not
// the scope set on a token, is the authority: a scope list is a snapshot taken
// when a credential was minted, and the role is the current state of the
// workspace.
//
// The refusal is ErrNotWorkspaceAdmin for a member and store.ErrNotFound for a
// non-member, so a caller cannot use the distinction to probe a workspace it has
// no membership in.
func (m Messages) requireWorkspaceAdmin(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) error {
	membership, err := m.activeWorkspaceMembership(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}
	if membership.Role != domain.WorkspaceRoleAdmin && membership.Role != domain.WorkspaceRoleOwner {
		return ErrNotWorkspaceAdmin
	}
	return nil
}

// WorkspaceMembership reads one membership row: the target's role and whether it
// is active.
//
// Authority: an actor may always read their OWN membership, because it is a fact
// about themselves that every signed-in surface needs (the browser shell derives
// its own capability display from it, and the login path derives the scopes of
// the session it is about to mint). Reading SOMEONE ELSE's membership is an
// administrative read and requires requireWorkspaceAdmin.
//
// It replaces a loop that paged the entire workspace through AdminListUsers to
// find one row, which was both an administrative read performed on behalf of a
// plain member and O(workspace) work for an O(1) question.
func (m Messages) WorkspaceMembership(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) (domain.WorkspaceMembership, error) {
	if actorID != targetID {
		if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
			return domain.WorkspaceMembership{}, err
		}
	}
	return m.activeWorkspaceMembership(ctx, workspaceID, targetID)
}

// ProvisionExternalUser creates the local user an external identity provider has
// just asserted, during that user's first sign-in.
//
// This method carries NO end-user authority check BY DESIGN, and it must stay
// that way: there is no actor to check. The user being provisioned has no local
// account yet and the signing-in browser holds no session, so passing any actor
// would mean naming an unrelated identity and calling its authority the caller's
// own — which is exactly the defect that let a lookup identity seeded as a plain
// member drive AdminCreateUser.
//
// The authority behind a call is therefore the provider assertion, and the caller
// MUST have verified that assertion before calling: a signature, issuer,
// audience and nonce that all validated, and an email address the provider
// asserts as verified. internal/web/identity.go is the only caller and does both
// before reaching here.
//
// It is not exposed on the Slack HTTP surface and must never be; a caller able to
// name an email address and a role would otherwise mint an administrator. In the
// distributed composition it crosses the chat gRPC boundary, which requires a
// mutually authenticated TLS 1.3 connection (cmd/chatd/main.go configures
// tls.RequireAndVerifyClientCert), so the only reachable callers are peers
// holding a client certificate this deployment issued.
func (m Messages) ProvisionExternalUser(ctx context.Context, workspaceID domain.WorkspaceID, email, realName string, role domain.WorkspaceRole) (domain.User, error) {
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return domain.User{}, err
	}
	return m.createWorkspaceUser(ctx, workspaceID, "", email, realName, role)
}

// SynchronizeExternalUserRole writes through the workspace role an external
// identity provider asserts for a user, during that user's sign-in.
//
// This method carries NO end-user authority check BY DESIGN. The provider's role
// claim is authoritative for a federated workspace, and the user it describes is
// signing in with no session yet, so there is no actor whose authority could be
// checked. Running the write as the signing-in member instead would mean letting
// a member change a role, which is the defect ErrNotWorkspaceAdmin exists to
// remove; running it as a seeded "lookup" identity would mean making that
// identity an administrator, which re-opens the same hole through a different
// door.
//
// The caller MUST therefore have verified the provider assertion the role came
// from before calling. internal/web/identity.go is the only caller: it reaches
// here only after the OpenID Connect ID token verified and the userinfo subject
// matched the token subject.
//
// It is not exposed on the Slack HTTP surface and must never be. In the
// distributed composition it crosses the chat gRPC boundary, which requires a
// mutually authenticated TLS 1.3 connection (cmd/chatd/main.go configures
// tls.RequireAndVerifyClientCert).
func (m Messages) SynchronizeExternalUserRole(ctx context.Context, workspaceID domain.WorkspaceID, targetID domain.UserID, role domain.WorkspaceRole) error {
	// An identity provider may describe a member or an administrator. It may not
	// appoint an owner: ownership is the authority that decides who administers
	// the workspace, and it must not be assignable by an external claim or by
	// anyone who can reach the internal boundary. createWorkspaceUser already
	// refused Owner for the same reason; this path did not, so the two
	// provisioning routes disagreed about the same concept.
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin {
		return ErrInvalidWorkspace
	}
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return err
	}
	// Ownership is a local concept the provider cannot express: its role claim
	// distinguishes a member from an administrator and says nothing about who
	// owns the workspace. Writing the claim through unconditionally therefore
	// DEMOTED an owner every time they signed in, and because only an owner may
	// confer ownership, demoting the last one is unrecoverable — the workspace
	// would be left permanently unadministrable by its own owner logging in.
	//
	// An existing owner keeps their role; the claim still governs the member and
	// administrator distinction for everyone else.
	membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, targetID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil && membership.Role == domain.WorkspaceRoleOwner {
		return nil
	}
	return m.setWorkspaceRole(ctx, workspaceID, "", targetID, role)
}

func (m Messages) PostWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, text, blocks string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	return m.PostWithBlocksAndAttachments(ctx, workspaceID, authorID, conversation, text, blocks, "", threadTimestamp, idempotencyKey, "")
}

func (m Messages) UpdateWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, text, blocks string) (domain.Message, error) {
	return m.UpdateWithBlocksAndAttachments(ctx, workspaceID, userID, conversation, timestamp, text, blocks, "")
}

func (m Messages) ScheduleMessageWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text, blocks string, postAt time.Time) (domain.ScheduledMessage, error) {
	return m.ScheduleMessageWithBlocksAndAttachments(ctx, workspaceID, userID, channel, text, blocks, "", postAt)
}

func (m Messages) PostEphemeralWithBlocks(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, recipientID domain.UserID, text, blocks string) (domain.EphemeralMessage, error) {
	return m.PostEphemeralWithBlocksAndAttachments(ctx, workspaceID, authorID, conversation, recipientID, text, blocks, "", "")
}

func (m Messages) ScheduleMessageWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text, blocks, attachments string, postAt time.Time) (domain.ScheduledMessage, error) {
	return m.ScheduleMessageAs(ctx, workspaceID, userID, domain.ScheduledMessageRequest{
		Channel:        channel,
		Text:           text,
		Blocks:         blocks,
		Attachments:    attachments,
		PostAt:         postAt,
		CredentialHash: InternalScheduledCredential(workspaceID, userID),
	})
}

// InternalScheduledCredential identifies work created by SameOldChat's
// first-party client. Keeping this coordinate in one place lets the web client
// preserve thread context through ScheduleMessageAs without duplicating the
// ownership contract used by list and delete.
func InternalScheduledCredential(workspaceID domain.WorkspaceID, userID domain.UserID) string {
	return domain.HashToken("internal-scheduled\x00" + string(workspaceID) + "\x00" + string(userID))
}

func (m Messages) ScheduleMessageAs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ScheduledMessageRequest) (domain.ScheduledMessage, error) {
	channel := request.Channel
	if err := m.requireConversationMembership(ctx, workspaceID, userID, channel); err != nil {
		return domain.ScheduledMessage{}, err
	}
	text := strings.TrimSpace(request.Text)
	if request.CredentialHash == "" || messagePayloadTooLong(request.Blocks, request.Attachments) {
		return domain.ScheduledMessage{}, ErrInvalidMessage
	}
	fileAttachments, fileErr := m.normalizeComposerAttachments(ctx, workspaceID, userID, request.FileAttachments)
	if fileErr != nil {
		return domain.ScheduledMessage{}, fileErr
	}
	normalizedBlocks, err := domain.NormalizeBlocks([]byte(request.Blocks))
	normalizedAttachments, attachmentErr := domain.NormalizeAttachments([]byte(request.Attachments))
	if err != nil || attachmentErr != nil || (text == "" && normalizedBlocks == "" && normalizedAttachments == "" && len(fileAttachments) == 0) || messageTextTooLong(text) || request.PostAt.IsZero() {
		return domain.ScheduledMessage{}, ErrInvalidMessage
	}
	metadata := ""
	if strings.TrimSpace(request.Metadata) != "" {
		if request.AppID == "" {
			return domain.ScheduledMessage{}, ErrInvalidMessage
		}
		metadata, err = normalizeMessageMetadata(request.Metadata)
		if err != nil {
			return domain.ScheduledMessage{}, ErrInvalidMessage
		}
	}
	if len(fileAttachments) != 0 && (normalizedAttachments != "" || request.AppID != "" || request.BotID != "" || metadata != "" || strings.TrimSpace(request.StreamState) != "") {
		// Hosted composer files are a private first-party contract. Slack's
		// published schedule API uses "attachments" for structured message
		// attachments and exposes no hosted file-id parameter, so app-authored
		// payload extensions must not be silently combined with this seam.
		return domain.ScheduledMessage{}, ErrInvalidMessage
	}
	streamState, err := normalizeScheduledMessageState(request.StreamState, text, normalizedBlocks, request.ThreadTimestamp)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	now := time.Now().UTC()
	// Slack's post_at contract is whole Unix seconds, and the SQL schema stores
	// that precision. Normalize before quota bucketing and cursor construction so
	// local memory composition cannot order the same request differently.
	postAt := time.Unix(request.PostAt.UTC().Unix(), 0).UTC()
	if !postAt.After(now) {
		return domain.ScheduledMessage{}, ErrScheduledTimeInPast
	}
	if postAt.After(now.Add(120 * 24 * time.Hour)) {
		return domain.ScheduledMessage{}, ErrScheduledTimeTooFar
	}
	target, err := m.Store.GetConversation(ctx, channel)
	if err != nil || target.WorkspaceID != workspaceID {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	if target.Archived {
		return domain.ScheduledMessage{}, ErrConversationAlreadyArchived
	}
	if request.ThreadTimestamp != "" {
		createdAt, parseErr := domain.ParseMessageTimestamp(request.ThreadTimestamp)
		if parseErr != nil {
			return domain.ScheduledMessage{}, ErrInvalidTimestamp
		}
		parent, parentErr := m.Store.GetMessageByCreatedAt(ctx, channel, createdAt)
		if parentErr != nil || parent.WorkspaceID != workspaceID {
			return domain.ScheduledMessage{}, store.ErrNotFound
		}
	}
	id, err := domain.NewScheduledMessageID()
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	value := domain.ScheduledMessage{
		WorkspaceID: workspaceID, ID: id, Channel: channel, Author: userID,
		AppID: request.AppID, BotID: request.BotID, CredentialHash: request.CredentialHash,
		Text: text, Blocks: normalizedBlocks, Attachments: normalizedAttachments, Metadata: metadata, StreamState: streamState,
		ThreadTimestamp: request.ThreadTimestamp, PostAt: postAt, CreatedAt: now, FileAttachments: fileAttachments,
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("message.scheduled", events.String("scheduled_message_id", string(id)), events.String("channel_id", string(channel)), events.String("post_at", string(domain.NewMessageTimestamp(value.PostAt)))), now)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := m.Store.CreateScheduledMessageWithinLimit(ctx, value, 5*time.Minute, 30, event); err != nil {
		if errors.Is(err, store.ErrScheduledMessageLimit) {
			return domain.ScheduledMessage{}, ErrScheduledTooMany
		}
		return domain.ScheduledMessage{}, err
	}
	return value, nil
}

// ScheduledMessagePostRequest reconstructs the exact message mutation stored at
// scheduling time. The scheduler and the first-party "send now" path share this
// conversion so options cannot disappear merely because delivery is deferred.
func ScheduledMessagePostRequest(value domain.ScheduledMessage) (domain.MessagePostRequest, error) {
	var state domain.MessageStreamState
	if value.StreamState != "" {
		if err := json.Unmarshal([]byte(value.StreamState), &state); err != nil {
			return domain.MessagePostRequest{}, ErrInvalidMessage
		}
	}
	return domain.MessagePostRequest{
		Conversation: value.Channel, Text: value.Text, Blocks: value.Blocks, Attachments: value.Attachments,
		Metadata: value.Metadata, ThreadTimestamp: value.ThreadTimestamp, IdempotencyKey: string(value.ID),
		AppID: value.AppID, MarkdownText: state.MarkdownText, ReplyBroadcast: state.ReplyBroadcast,
		Parse: state.Parse, MrkdwnDisabled: state.MrkdwnDisabled, LinkNames: state.LinkNames,
		UnfurlLinks: state.UnfurlLinks, UnfurlMedia: state.UnfurlMedia,
		Username: state.Username, IconEmoji: state.IconEmoji, IconURL: state.IconURL,
	}, nil
}

// PostScheduledMessage is the one deferred-delivery path shared by the worker
// and first-party "Send now". The canonical pending record is loaded from the
// repository rather than trusted from an internal caller. For composer files,
// upload completion, every file share, the message, Activity/outbox effects,
// and the scheduled ID's idempotency record commit in one transaction.
func (m Messages) PostScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, id domain.ScheduledMessageID) (domain.Message, error) {
	value, err := m.Store.GetScheduledMessage(ctx, workspaceID, id)
	if err != nil {
		return domain.Message{}, err
	}
	request, err := ScheduledMessagePostRequest(value)
	if err != nil {
		return domain.Message{}, err
	}
	if len(value.FileAttachments) == 0 {
		return m.postMessageAs(ctx, value.WorkspaceID, value.Author, request, value.ID)
	}
	completions := make([]domain.ExternalUploadCompletion, 0, len(value.FileAttachments))
	for _, attachment := range value.FileAttachments {
		completions = append(completions, domain.ExternalUploadCompletion{ID: attachment.UploadID, Title: attachment.Title})
	}
	_, err = m.completeExternalUploads(ctx, value.WorkspaceID, value.Author, completions, []domain.ConversationID{value.Channel}, value.Text, value.Blocks, value.ThreadTimestamp, string(value.ID))
	if err != nil {
		return domain.Message{}, err
	}
	return m.Store.GetIdempotentMessage(ctx, value.WorkspaceID, value.Author, string(value.ID))
}

func normalizeScheduledMessageState(raw, text, blocks string, threadTimestamp domain.MessageTimestamp) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	var state domain.MessageStreamState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return "", ErrInvalidMessage
	}
	if state.Active || state.TaskDisplayMode != "" || state.BotID != "" || state.PlanTitle != "" ||
		len(state.Tasks) != 0 || len(state.ChunkBlocks) != 0 || len(state.Warnings) != 0 ||
		(state.MarkdownText && blocks != "") || (state.MarkdownText && utf8.RuneCountInString(text) > 12000) ||
		(state.ReplyBroadcast && threadTimestamp == "") ||
		(state.Parse != "" && state.Parse != "none" && state.Parse != "full") ||
		utf8.RuneCountInString(state.Username) > 80 ||
		(state.IconURL != "" && !validMessageIconURL(state.IconURL)) ||
		(state.IconEmoji != "" && (!strings.HasPrefix(state.IconEmoji, ":") || !strings.HasSuffix(state.IconEmoji, ":"))) {
		return "", ErrInvalidMessage
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if string(encoded) == "{}" {
		return "", nil
	}
	return string(encoded), nil
}

func (m Messages) PostEphemeralWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, recipientID domain.UserID, text, blocks, attachments string, appID domain.AppID) (domain.EphemeralMessage, error) {
	return m.postEphemeralWithBlocksAndAttachments(ctx, workspaceID, authorID, conversation, recipientID, text, blocks, attachments, appID, "")
}

func (m Messages) postEphemeralWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, recipientID domain.UserID, text, blocks, attachments string, appID domain.AppID, idempotencyKey string) (domain.EphemeralMessage, error) {
	if err := m.authorizeConversation(ctx, workspaceID, authorID, conversation); err != nil {
		return domain.EphemeralMessage{}, err
	}
	text = strings.TrimSpace(text)
	if messagePayloadTooLong(blocks, attachments) {
		return domain.EphemeralMessage{}, ErrInvalidEphemeral
	}
	normalizedBlocks, err := domain.NormalizeBlocks([]byte(blocks))
	normalizedAttachments, attachmentErr := domain.NormalizeAttachments([]byte(attachments))
	if conversation == "" || recipientID == "" || (text == "" && normalizedBlocks == "" && normalizedAttachments == "") || messageTextTooLong(text) || err != nil || attachmentErr != nil {
		return domain.EphemeralMessage{}, ErrInvalidEphemeral
	}
	recipient, err := m.Store.GetUser(ctx, recipientID)
	if err != nil || recipient.WorkspaceID != workspaceID || recipient.Deleted {
		return domain.EphemeralMessage{}, store.ErrNotFound
	}
	isMember, err := m.Store.IsConversationMember(ctx, conversation, recipientID)
	if err != nil || !isMember {
		return domain.EphemeralMessage{}, store.ErrNotFound
	}
	var id domain.MessageID
	if idempotencyKey == "" {
		id, err = domain.NewMessageID()
		if err != nil {
			return domain.EphemeralMessage{}, err
		}
	} else {
		// Ephemeral messages do not use the ordinary message idempotency table,
		// because they are recipient-scoped and never enter channel history.
		// A stable identifier gives a retried Socket Mode acknowledgement the
		// same atomic insert boundary in both stores.
		id = domain.MessageID("msg_ephemeral_" + domain.HashToken(
			string(workspaceID) + "\x00" + string(authorID) + "\x00" + string(conversation) + "\x00" +
				string(recipientID) + "\x00" + string(appID) + "\x00" + idempotencyKey,
		)[:40])
	}
	now := domain.MessageInstant(time.Now().UTC())
	value := domain.EphemeralMessage{ID: id, WorkspaceID: workspaceID, Conversation: conversation, AuthorID: authorID, AppID: appID, RecipientID: recipientID, Text: text, Blocks: normalizedBlocks, Attachments: normalizedAttachments, Timestamp: domain.NewMessageTimestamp(now), CreatedAt: now}
	// user_id names the single recipient. Every consumer that fans this record
	// out has to filter on it, which is why it is a first-class payload field.
	payload := events.NewPayload(events.EphemeralMessageTopic,
		events.String("workspace_id", string(value.WorkspaceID)),
		events.String("channel_id", string(value.Conversation)),
		events.String("author_id", string(value.AuthorID)),
		events.String("app_id", string(value.AppID)),
		events.String("user_id", string(value.RecipientID)),
		events.String("text", value.Text),
		events.String("blocks", value.Blocks),
		events.String("attachments", value.Attachments),
		events.String("ts", string(value.Timestamp)),
	)
	event, err := newEvent(workspaceID, authorID, payload, now)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if err := m.Store.CreateEphemeralMessage(ctx, value, event); err != nil {
		if idempotencyKey != "" && errors.Is(err, store.ErrAlreadyExists) {
			return value, nil
		}
		return domain.EphemeralMessage{}, err
	}
	return value, nil
}

func (m Messages) ListEphemeralMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, limit int) ([]domain.EphemeralMessage, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return nil, err
	}
	return m.Store.ListEphemeralMessages(ctx, workspaceID, userID, conversationID, limit)
}

func (m Messages) PostWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, conversation domain.ConversationID, text, blocks, attachments string, threadTimestamp domain.MessageTimestamp, idempotencyKey string, appID domain.AppID) (domain.Message, error) {
	return m.PostMessageAs(ctx, workspaceID, authorID, domain.MessagePostRequest{
		Conversation: conversation, Text: text, Blocks: blocks, Attachments: attachments,
		ThreadTimestamp: threadTimestamp, IdempotencyKey: idempotencyKey, AppID: appID,
	})
}

func (m Messages) PostMessageAs(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, request domain.MessagePostRequest) (domain.Message, error) {
	return m.postMessageAs(ctx, workspaceID, authorID, request, "")
}

func (m Messages) postMessageAs(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, request domain.MessagePostRequest, scheduledID domain.ScheduledMessageID) (domain.Message, error) {
	if scheduledID != "" && request.IdempotencyKey != string(scheduledID) {
		return domain.Message{}, ErrInvalidMessage
	}
	if request.IdempotencyKey != "" {
		cached, err := m.Store.GetIdempotentMessage(ctx, workspaceID, authorID, request.IdempotencyKey)
		if err == nil {
			return cached, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return domain.Message{}, err
		}
	}
	if messagePayloadTooLong(request.Blocks, request.Attachments) {
		return domain.Message{}, ErrInvalidMessage
	}
	normalizedBlocks, err := domain.NormalizeBlocks([]byte(request.Blocks))
	normalizedAttachments, attachmentErr := domain.NormalizeAttachments([]byte(request.Attachments))
	if err != nil || attachmentErr != nil || strings.TrimSpace(string(request.Conversation)) == "" ||
		(strings.TrimSpace(request.Text) == "" && normalizedBlocks == "" && normalizedAttachments == "") ||
		messageTextTooLong(request.Text) || (request.MarkdownText && utf8.RuneCountInString(request.Text) > 12000) ||
		(request.Parse != "" && request.Parse != "none" && request.Parse != "full") ||
		(request.ReplyBroadcast && request.ThreadTimestamp == "") ||
		!request.Subtype.Valid() {
		return domain.Message{}, ErrInvalidMessage
	}
	metadata := ""
	if strings.TrimSpace(request.Metadata) != "" {
		if request.AppID == "" {
			return domain.Message{}, ErrInvalidMessage
		}
		metadata, err = normalizeMessageMetadata(request.Metadata)
		if err != nil {
			return domain.Message{}, ErrInvalidMessage
		}
	}
	if utf8.RuneCountInString(request.Username) > 80 ||
		(request.IconURL != "" && !validMessageIconURL(request.IconURL)) ||
		(request.IconEmoji != "" && (!strings.HasPrefix(request.IconEmoji, ":") || !strings.HasSuffix(request.IconEmoji, ":"))) {
		return domain.Message{}, ErrInvalidMessage
	}
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return domain.Message{}, err
	}
	if err := m.requireConversationMembership(ctx, workspaceID, authorID, request.Conversation); err != nil {
		return domain.Message{}, err
	}
	target, err := m.Store.GetConversation(ctx, request.Conversation)
	if err != nil || target.WorkspaceID != workspaceID {
		return domain.Message{}, store.ErrNotFound
	}
	if target.Archived {
		return domain.Message{}, ErrConversationAlreadyArchived
	}
	threadTimestampValue := domain.MessageTimestamp("")
	if request.ThreadTimestamp != "" {
		createdAt, err := domain.ParseMessageTimestamp(request.ThreadTimestamp)
		if err != nil {
			return domain.Message{}, ErrInvalidTimestamp
		}
		parent, err := m.Store.GetMessageByCreatedAt(ctx, request.Conversation, createdAt)
		if err != nil || parent.WorkspaceID != workspaceID {
			return domain.Message{}, store.ErrNotFound
		}
		threadTimestampValue = request.ThreadTimestamp
	}
	id, err := domain.NewMessageID()
	if err != nil {
		return domain.Message{}, err
	}
	state := domain.MessageStreamState{
		Username: request.Username, IconEmoji: request.IconEmoji, IconURL: request.IconURL,
		MarkdownText: request.MarkdownText, ReplyBroadcast: request.ReplyBroadcast,
		Parse: request.Parse, MrkdwnDisabled: request.MrkdwnDisabled, LinkNames: request.LinkNames,
		UnfurlLinks: request.UnfurlLinks, UnfurlMedia: request.UnfurlMedia,
	}
	streamState := ""
	if encoded, encodeErr := json.Marshal(state); encodeErr != nil {
		return domain.Message{}, encodeErr
	} else if string(encoded) != "{}" {
		streamState = string(encoded)
	}
	message := domain.Message{
		ID: id, WorkspaceID: workspaceID, Conversation: request.Conversation, AuthorID: authorID,
		AppID: request.AppID, Text: request.Text, Blocks: normalizedBlocks, Attachments: normalizedAttachments,
		Metadata: metadata, StreamState: streamState, ThreadTimestamp: threadTimestampValue,
		CreatedAt: domain.MessageInstant(time.Now()), Subtype: request.Subtype,
	}
	// A message's ts is its public identifier and it carries microseconds, so two
	// messages in one conversation may not be created on the same microsecond.
	// The repository refuses the collision; the remedy is the next microsecond,
	// and the event and the response are rebuilt from the instant that was
	// actually taken so the client is told the identifier the row really has.
	// This is the same construction the real Slack timestamp uses, and it is why
	// the identifier cannot be merged no matter how coarse the host clock is.
	for {
		event, err := messageEventAt(workspaceID, "message.created", message, nil, message.CreatedAt)
		if err != nil {
			return domain.Message{}, err
		}
		if scheduledID == "" {
			err = m.Store.CreateMessage(ctx, message, event, request.IdempotencyKey)
		} else {
			err = m.Store.CreateScheduledMessagePost(ctx, scheduledID, message, event)
		}
		if errors.Is(err, store.ErrMessageTimestampTaken) {
			message.CreatedAt = message.CreatedAt.Add(time.Microsecond)
			continue
		}
		if err != nil {
			if errors.Is(err, store.ErrIdempotencyConflict) {
				return m.Store.GetIdempotentMessage(ctx, workspaceID, authorID, request.IdempotencyKey)
			}
			return domain.Message{}, err
		}
		return message, nil
	}
}

func (m Messages) PostIncomingWebhookWithAttachments(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret, text, blocks, attachments string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) (domain.Message, error) {
	value, err := m.Store.LookupIncomingWebhook(ctx, workspaceID, appID, secret)
	if err != nil {
		return domain.Message{}, err
	}
	return m.PostWithBlocksAndAttachments(ctx, workspaceID, value.UserID, value.ConversationID, text, blocks, attachments, threadTimestamp, idempotencyKey, value.AppID)
}

func (m Messages) UpdateWithBlocksAndAttachments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, text, blocks, attachments string) (domain.Message, error) {
	return m.UpdateMessage(ctx, workspaceID, userID, conversation, timestamp, domain.MessagePatch{
		Text:        &text,
		Blocks:      &blocks,
		Attachments: &attachments,
	})
}

// UpdateMessage applies Slack's presence-sensitive chat.update rules. Omitted
// blocks survive when text is also omitted, text without blocks removes the old
// blocks, omitted attachments always survive, and explicit empty arrays remove
// either collection.
func (m Messages) UpdateMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, patch domain.MessagePatch) (domain.Message, error) {
	if patch.Text == nil && patch.Blocks == nil && patch.Attachments == nil {
		return domain.Message{}, ErrInvalidMessage
	}
	message, err := m.messageForMutation(ctx, workspaceID, userID, conversation, timestamp)
	if err != nil {
		return domain.Message{}, err
	}
	if message.Deleted {
		return domain.Message{}, ErrMessageAlreadyDeleted
	}
	previous := message
	if patch.Text != nil {
		message.Text = *patch.Text
	}
	if patch.Blocks != nil {
		message.Blocks, err = domain.NormalizeBlocks([]byte(*patch.Blocks))
		if err != nil {
			return domain.Message{}, ErrInvalidMessage
		}
	} else if patch.Text != nil {
		message.Blocks = ""
	}
	if patch.Attachments != nil {
		message.Attachments, err = domain.NormalizeAttachments([]byte(*patch.Attachments))
		if err != nil {
			return domain.Message{}, ErrInvalidMessage
		}
	}
	if messagePayloadTooLong(message.Blocks, message.Attachments) ||
		messageTextTooLong(message.Text) ||
		(strings.TrimSpace(message.Text) == "" && message.Blocks == "" && message.Attachments == "") {
		return domain.Message{}, ErrInvalidMessage
	}
	// The edit is recorded on the message itself, not only on the event it
	// emits. Slack's message object carries an `edited` sub-object, and every
	// reader — history, replies, the first-party timeline — needs it; deriving
	// it from an outbox event meant a replayed event and the message could
	// disagree about when the edit happened.
	message.EditedAt = time.Now().UTC()
	message.EditedBy = userID
	event, err := messageMutationEvent(workspaceID, "message.changed", message, previous)
	if err != nil {
		return domain.Message{}, err
	}
	if err := m.Store.UpdateMessage(ctx, message, event); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func messageTextTooLong(text string) bool {
	return utf8.RuneCountInString(text) > MaxMessageTextRunes
}

func messagePayloadTooLong(blocks, attachments string) bool {
	return len(blocks) > MaxMessageBodyBytes || len(attachments) > MaxMessageBodyBytes-len(blocks)
}

func messageUnfurlsTooLong(unfurls map[string]string) bool {
	remaining := MaxMessageBodyBytes
	for sourceURL, value := range unfurls {
		if len(sourceURL) > remaining {
			return true
		}
		remaining -= len(sourceURL)
		if len(value) > remaining {
			return true
		}
		remaining -= len(value)
	}
	return false
}

func (m Messages) CreateExternalUpload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, mimeType string, size int64, ttl time.Duration) (domain.ExternalUpload, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ExternalUpload{}, err
	}
	name = strings.TrimSpace(name)
	mimeType = strings.TrimSpace(mimeType)
	if name == "" || mimeType == "" || size <= 0 || ttl <= 0 {
		return domain.ExternalUpload{}, ErrInvalidExternalUpload
	}
	id, err := domain.NewExternalUploadID()
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	now := time.Now().UTC()
	value := domain.ExternalUpload{ID: id, WorkspaceID: workspaceID, Uploader: userID, Name: name, Title: name, MIMEType: mimeType, BlobKey: string(workspaceID) + "/external/" + string(id), Size: size, Status: domain.ExternalUploadPending, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := m.Store.CreateExternalUpload(ctx, value); err != nil {
		return domain.ExternalUpload{}, err
	}
	return value, nil
}

func (m Messages) UploadExternalFile(ctx context.Context, id domain.ExternalUploadID, size int64, source io.Reader) error {
	// A negative size means the transport is a multipart part whose own length
	// is not exposed by net/http. The upload ticket remains authoritative and
	// the blob store still reads exactly Size+1 bytes, so short and oversized
	// parts fail closed just like raw bodies.
	if m.Blob == nil || source == nil || size < -1 {
		return ErrInvalidExternalUpload
	}
	value, err := m.Store.GetExternalUpload(ctx, id)
	if err != nil {
		return err
	}
	if size == -1 {
		size = value.Size
	}
	if value.Status != domain.ExternalUploadPending || !value.ExpiresAt.After(time.Now().UTC()) || value.Size != size {
		return ErrInvalidExternalUpload
	}
	if _, err := m.Blob.Put(ctx, value.BlobKey, value.Size, source); err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return ErrBlobUnavailable
		}
		return err
	}
	if err := m.Store.MarkExternalUploadUploaded(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

func (m Messages) CompleteExternalUpload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ExternalUploadID, title string, channels []domain.ConversationID, initialComment, blocks string, threadTimestamp domain.MessageTimestamp) (domain.File, error) {
	files, err := m.CompleteExternalUploads(ctx, workspaceID, userID, []domain.ExternalUploadCompletion{{ID: id, Title: title}}, channels, initialComment, blocks, threadTimestamp)
	if err != nil {
		return domain.File{}, err
	}
	if len(files) != 1 {
		return domain.File{}, errors.New("external upload completion returned an unexpected file count")
	}
	return files[0], nil
}

func normalizeFileShareChannels(values []domain.ConversationID) []domain.ConversationID {
	seen := make(map[domain.ConversationID]struct{}, len(values))
	result := make([]domain.ConversationID, 0, len(values))
	for _, value := range values {
		value = domain.ConversationID(strings.TrimSpace(string(value)))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (m Messages) CompleteExternalUploads(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, completions []domain.ExternalUploadCompletion, channels []domain.ConversationID, initialComment, blocks string, threadTimestamp domain.MessageTimestamp) ([]domain.File, error) {
	return m.completeExternalUploads(ctx, workspaceID, userID, completions, channels, initialComment, blocks, threadTimestamp, "")
}

func (m Messages) completeExternalUploads(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, completions []domain.ExternalUploadCompletion, channels []domain.ConversationID, initialComment, blocks string, threadTimestamp domain.MessageTimestamp, idempotencyKey string) ([]domain.File, error) {
	if idempotencyKey != "" {
		cached, err := m.Store.GetIdempotentMessage(ctx, workspaceID, userID, idempotencyKey)
		if err == nil {
			return append([]domain.File(nil), cached.Files...), nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	completions, err := normalizeExternalUploadCompletions(completions)
	if err != nil {
		return nil, err
	}
	channels = normalizeFileShareChannels(channels)
	if len(channels) > 100 || (threadTimestamp != "" && len(channels) != 1) {
		return nil, ErrInvalidExternalUpload
	}
	if strings.TrimSpace(initialComment) != "" {
		blocks = ""
	}
	if messagePayloadTooLong(blocks, "") {
		return nil, ErrInvalidExternalUpload
	}
	normalizedBlocks, err := domain.NormalizeBlocks([]byte(blocks))
	if err != nil {
		return nil, ErrInvalidExternalUpload
	}
	values := make([]domain.ExternalUpload, len(completions))
	for index, completion := range completions {
		value, err := m.Store.GetExternalUpload(ctx, completion.ID)
		if err != nil || value.WorkspaceID != workspaceID || value.Uploader != userID {
			return nil, store.ErrNotFound
		}
		if !value.ExpiresAt.After(time.Now().UTC()) {
			pendingOwned, pendingErr := m.Store.PendingUploadReferenceExists(ctx, workspaceID, userID, value.ID)
			if pendingErr != nil {
				return nil, pendingErr
			}
			if !pendingOwned {
				return nil, ErrInvalidExternalUpload
			}
		}
		values[index] = value
	}
	for _, channel := range channels {
		if err := m.requireConversationMembership(ctx, workspaceID, userID, channel); err != nil {
			return nil, err
		}
		conversation, err := m.Store.GetConversation(ctx, channel)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return nil, store.ErrNotFound
		}
		if conversation.Archived {
			return nil, ErrConversationAlreadyArchived
		}
	}
	threadTimestampValue := domain.MessageTimestamp("")
	if threadTimestamp != "" {
		createdAt, parseErr := domain.ParseMessageTimestamp(threadTimestamp)
		if parseErr != nil {
			return nil, ErrInvalidTimestamp
		}
		parent, lookupErr := m.Store.GetMessageByCreatedAt(ctx, channels[0], createdAt)
		if lookupErr != nil || parent.WorkspaceID != workspaceID {
			return nil, store.ErrNotFound
		}
		threadTimestampValue = threadTimestamp
	}
	completed, allCompleted, err := m.completedExternalUploadFiles(ctx, workspaceID, userID, completions)
	if err != nil {
		return nil, err
	}
	if allCompleted {
		// A completed ticket is retry evidence only when it was shared to this
		// exact destination set. Without this check, a client could complete a
		// draft-owned upload through files.completeUploadExternal in one
		// channel, then "send" the stale draft in another; the composer would
		// report success and clear the draft although no message was posted.
		if len(channels) != 0 && !sameFileShareChannels(completed, channels) {
			return nil, ErrInvalidExternalUpload
		}
		if idempotencyKey != "" {
			if _, err := m.Store.GetIdempotentMessage(ctx, workspaceID, userID, idempotencyKey); err != nil {
				return nil, ErrInvalidExternalUpload
			}
		}
		return completed, nil
	}
	for _, value := range values {
		if value.Status != domain.ExternalUploadUploaded {
			return nil, ErrInvalidExternalUpload
		}
	}
	files := make([]domain.File, len(values))
	eventsToEmit := make([]events.Event, 0, len(values)*(len(channels)+1))
	for index, value := range values {
		// The upload identifier was handed to the client as file_id when the
		// upload URL was issued. Minting a fresh one here would strand every
		// client that recorded it, so the completed file keeps it.
		fileID := domain.FileID(value.ID)
		title := completions[index].Title
		if title == "" {
			title = value.Title
		}
		createdAt := time.Now().UTC()
		files[index] = domain.File{ID: fileID, WorkspaceID: value.WorkspaceID, Uploader: value.Uploader, Name: value.Name, Title: title, MIMEType: value.MIMEType, BlobKey: value.BlobKey, Size: value.Size, CreatedAt: createdAt, SharedChannels: append([]domain.ConversationID(nil), channels...)}
		emitted, err := fileEventAt(workspaceID, userID, "file.created", files[index], "", createdAt)
		if err != nil {
			return nil, err
		}
		eventsToEmit = append(eventsToEmit, emitted)
		for _, channel := range channels {
			shared, err := fileEventAt(workspaceID, userID, "file.shared", files[index], channel, createdAt)
			if err != nil {
				return nil, err
			}
			eventsToEmit = append(eventsToEmit, shared)
		}
	}
	messages := make([]domain.Message, len(channels))
	messageEvents := make([]events.Event, len(channels))
	createdAt := domain.MessageInstant(time.Now())
	for index, channel := range channels {
		messageID, idErr := domain.NewMessageID()
		if idErr != nil {
			return nil, idErr
		}
		messages[index] = domain.Message{
			ID: messageID, WorkspaceID: workspaceID, Conversation: channel, AuthorID: userID,
			Text: initialComment, Blocks: normalizedBlocks, ThreadTimestamp: threadTimestampValue,
			CreatedAt: createdAt.Add(time.Duration(index) * time.Microsecond), Files: append([]domain.File(nil), files...),
		}
		emitted, eventErr := messageEventAt(workspaceID, "message.created", messages[index], nil, messages[index].CreatedAt)
		if eventErr != nil {
			return nil, eventErr
		}
		messageEvents[index] = emitted
	}
	// Paired at the call, so the store cannot be handed a completion without
	// its file or a message without its event.
	uploaded := make([]store.UploadedFile, 0, len(completions))
	for index, completion := range completions {
		uploaded = append(uploaded, store.UploadedFile{Completion: completion, File: files[index]})
	}
	for {
		posts := make([]store.PostedMessage, 0, len(messages))
		for index, message := range messages {
			posts = append(posts, store.PostedMessage{Message: message, Event: messageEvents[index]})
		}
		var err error
		if idempotencyKey == "" {
			err = m.Store.CompleteExternalUploads(ctx, uploaded, channels, eventsToEmit, posts)
		} else {
			err = m.Store.CompleteScheduledExternalUploads(ctx, domain.ScheduledMessageID(idempotencyKey), uploaded, channels, eventsToEmit, posts[0])
		}
		if errors.Is(err, store.ErrMessageTimestampTaken) {
			// Completion is transactional, so none of the tickets or shares
			// changed. Move the public message timestamps together and retry;
			// the upload must not fail merely because another message occupied
			// the host clock's current microsecond.
			for index := range messages {
				messages[index].CreatedAt = messages[index].CreatedAt.Add(time.Microsecond)
				messageEvents[index], err = messageEventAt(workspaceID, "message.created", messages[index], nil, messages[index].CreatedAt)
				if err != nil {
					return nil, err
				}
			}
			continue
		}
		if err == nil {
			break
		}
		// A concurrent caller may have completed these tickets between the read
		// above and this write. The store rejects the second writer rather than
		// completing twice, which is correct, but the caller's upload did
		// finish: reporting a conflict would make a retry or a duplicated
		// request look like a failure. Re-read and answer with what the winner
		// produced, exactly as a sequential retry is answered.
		if errors.Is(err, store.ErrIdempotencyConflict) {
			cached, cachedErr := m.Store.GetIdempotentMessage(ctx, workspaceID, userID, idempotencyKey)
			if cachedErr != nil {
				return nil, errors.Join(err, cachedErr)
			}
			return append([]domain.File(nil), cached.Files...), nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		settled, allSettled, settleErr := m.completedExternalUploadFiles(ctx, workspaceID, userID, completions)
		if settleErr != nil || !allSettled {
			return nil, err
		}
		files = settled
		if idempotencyKey != "" {
			cached, cachedErr := m.Store.GetIdempotentMessage(ctx, workspaceID, userID, idempotencyKey)
			if cachedErr != nil {
				return nil, errors.Join(err, cachedErr)
			}
			return append([]domain.File(nil), cached.Files...), nil
		}
		break
	}
	return files, nil
}

// completedExternalUploadFiles reports the files behind a set of tickets when
// every one of them has already been completed. Completion is answered from
// here both when the caller is retrying and when a concurrent caller won the
// write, so a retry and a race produce the same answer.
func (m Messages) completedExternalUploadFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, completions []domain.ExternalUploadCompletion) ([]domain.File, bool, error) {
	files := make([]domain.File, len(completions))
	for index, completion := range completions {
		value, err := m.Store.GetExternalUpload(ctx, completion.ID)
		if err != nil {
			return nil, false, err
		}
		if value.Status != domain.ExternalUploadCompleted {
			return nil, false, nil
		}
		if value.FileID == "" {
			return nil, false, errors.New("completed external upload has no file reference")
		}
		file, err := m.FileInfo(ctx, workspaceID, userID, value.FileID)
		if err != nil {
			return nil, false, err
		}
		files[index] = file
	}
	return files, true, nil
}

func normalizeExternalUploadCompletions(values []domain.ExternalUploadCompletion) ([]domain.ExternalUploadCompletion, error) {
	if len(values) == 0 {
		return nil, ErrInvalidExternalUpload
	}
	seen := make(map[domain.ExternalUploadID]struct{}, len(values))
	result := make([]domain.ExternalUploadCompletion, 0, len(values))
	for _, value := range values {
		value.ID = domain.ExternalUploadID(strings.TrimSpace(string(value.ID)))
		value.Title = strings.TrimSpace(value.Title)
		if value.ID == "" || len(value.Title) > 255 {
			return nil, ErrInvalidExternalUpload
		}
		if _, exists := seen[value.ID]; exists {
			return nil, ErrInvalidExternalUpload
		}
		seen[value.ID] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func sameFileShareChannels(files []domain.File, channels []domain.ConversationID) bool {
	for _, file := range files {
		if !reflect.DeepEqual(file.SharedChannels, channels) {
			return false
		}
	}
	return true
}
