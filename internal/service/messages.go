package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

var (
	ErrInvalidMessage              = errors.New("message text and conversation are required")
	ErrInvalidTimestamp            = errors.New("message timestamp is invalid")
	ErrMessageNotOwned             = errors.New("message is not owned by user")
	ErrMessageAlreadyDeleted       = errors.New("message is already deleted")
	ErrInvalidConversation         = errors.New("conversation name is invalid")
	ErrInvalidWorkspace            = errors.New("workspace settings are invalid")
	ErrInvalidConversationPrefs    = errors.New("conversation preferences are invalid")
	ErrInvalidReaction             = errors.New("reaction name is invalid")
	ErrBlobUnavailable             = errors.New("blob storage is unavailable")
	ErrInvalidFile                 = errors.New("file metadata is invalid")
	ErrInvalidSearch               = errors.New("search query is invalid")
	ErrInvalidProfile              = errors.New("user profile is invalid")
	ErrInvalidPresence             = errors.New("user presence is invalid")
	ErrInvalidSnooze               = errors.New("snooze duration must be between 1 and 1440 minutes")
	ErrInvalidReminder             = errors.New("reminder text, user, and time are required")
	ErrInvalidLaterReminder        = errors.New("Later reminder arguments are invalid")
	ErrReminderTimeInPast          = errors.New("reminder time is in the past")
	ErrScheduledTimeInPast         = errors.New("scheduled message time is in the past")
	ErrScheduledTimeTooFar         = errors.New("scheduled message time is more than 120 days away")
	ErrScheduledTooMany            = errors.New("too many messages are scheduled in the channel window")
	ErrInvalidUserGroup            = errors.New("user group name, handle, and members are invalid")
	ErrInvalidCall                 = errors.New("call external id and join URL are required")
	ErrInvalidEphemeral            = errors.New("ephemeral message recipient, conversation, and text are required")
	ErrInvalidAccessLog            = errors.New("access log fields are invalid")
	ErrInvalidEmoji                = errors.New("custom emoji name or URL is invalid")
	ErrEmojiAlreadyExists          = errors.New("custom emoji already exists")
	ErrInvalidRemoteFile           = errors.New("remote file metadata is invalid")
	ErrInvalidInviteRequest        = errors.New("invite request is invalid")
	ErrInvalidAppApproval          = errors.New("app approval is invalid")
	ErrInvalidView                 = errors.New("view payload is invalid")
	ErrAppHomeNotEnabled           = errors.New("app home tab is not enabled")
	ErrInvalidList                 = errors.New("list payload is invalid")
	ErrInvalidEntity               = errors.New("entity payload is invalid")
	ErrInvalidWorkflowStep         = errors.New("workflow step payload is invalid")
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

	// ErrNotWorkspaceAdmin refuses an administrative operation to an actor whose
	// durable workspace membership is not an administrator or an owner.
	//
	// It is distinct from store.ErrNotFound, which every administrative method
	// already returned for an actor who is not a member of the workspace at all.
	// Collapsing the two would tell an authenticated member that the workspace
	// does not exist, and would hide the real reason from the operator reading the
	// audit trail.
	ErrNotWorkspaceAdmin = errors.New("actor is not a workspace administrator")

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
	return m.Store.UninstallApp(ctx, workspaceID, appID)
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
			prepared, visible, prepareErr := PrepareAppEvent(ctx, m.Store, appID, record)
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
		prepared, visible, err := PrepareAppEvent(ctx, m.Store, appID, record)
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
	event, err := newEvent(workspaceID, userID, events.NewPayload("file.created", events.String("file_id", string(file.ID))), file.CreatedAt)
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
		event, err := newEvent(workspaceID, userID, events.NewPayload("file.public_shared", events.String("file_id", string(fileID))), time.Now().UTC())
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
	return m.Store.ListFiles(ctx, workspaceID, request)
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
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.MessagePage{}, err
	}
	query = strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if query == "" || len(query) > 500 {
		return domain.MessagePage{}, ErrInvalidSearch
	}
	return m.Store.SearchMessages(ctx, workspaceID, userID, query, request)
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
	return m.Store.GetConversation(ctx, conversationID)
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
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.removed", events.String("user_id", string(targetID))), time.Now().UTC())
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

func (m Messages) AdminSetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsDirect || conversation.IsGroupDirect {
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
	if conversation.IsDirect || conversation.IsGroupDirect {
		return ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, actorID, conversationPayload("conversation.deleted", conversationID), time.Now().UTC())
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
	if !conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect || groupID == "" {
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
	if !conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect {
		return nil, ErrInvalidConversation
	}
	return m.Store.ListConversationAccessGroups(ctx, workspaceID, conversationID)
}

func (m Messages) AdminApproveInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.InviteRequestID) error {
	return m.changeInviteRequestStatus(ctx, workspaceID, actorID, id, domain.InviteRequestApproved)
}

func (m Messages) AdminDenyInviteRequest(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.InviteRequestID) error {
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
	topic := "invite_request.approved"
	if status == domain.InviteRequestDenied {
		topic = "invite_request.denied"
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload(topic, events.String("invite_request_id", string(id))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetInviteRequestStatus(ctx, workspaceID, id, status, time.Now().UTC(), event)
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
		if err != nil || conversation.WorkspaceID != workspaceID || conversation.IsDirect {
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
	value := domain.InviteRequest{ID: domain.InviteRequestID(id), WorkspaceID: workspaceID, Email: email, RequestedBy: actorID, ChannelIDs: normalizedChannels, CustomMessage: customMessage, RealName: realName, Resend: resend, Restricted: restricted, UltraRestricted: ultraRestricted, GuestExpirationAt: guestExpirationAt.UTC(), Status: domain.InviteRequestPending, CreatedAt: now}
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
	event, err := newEvent(workspaceID, actorID, events.NewPayload("user.created", events.String("user_id", string(id))), now)
	if err != nil {
		return domain.User{}, err
	}
	user := domain.User{ID: id, WorkspaceID: workspaceID, Email: email, Name: realName, RealName: realName, Presence: domain.PresenceAuto}
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

func (m Messages) AdminApproveApp(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	return m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalApproved)
}

func (m Messages) AdminRestrictApp(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	return m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalRestricted)
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
	if appID == "" {
		appID = domain.AppID("request:" + string(requestID))
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
	if status != domain.AppApprovalRequested && status != domain.AppApprovalApproved && status != domain.AppApprovalRestricted {
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
	tokenType := "bot"
	if userOnly {
		tokenType = "user"
	}
	return m.oauthExchange(ctx, clientID, clientSecret, code, redirectURI, "", tokenType, true)
}

const oauthRotatingTokenLifetime = 12 * time.Hour

func (m Messages) oauthExchange(ctx context.Context, clientID, clientSecret, code, redirectURI, codeVerifier, tokenType string, rotationAllowed bool) (domain.OAuthToken, error) {
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
	} else if tokenType == "bot" {
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
	if tokenType == "bot" {
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
		if tokenType == "bot" {
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
	return token, nil
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
	if grant.TokenType == "bot" {
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
	if err != nil || record.Revoked || record.AppID != client.AppID || !record.ExpiresAt.IsZero() || record.TokenType != "bot" && record.TokenType != "user" {
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
	value := domain.RTMConnection{ID: id, WorkspaceID: workspaceID, UserID: userID, ExpiresAt: time.Now().UTC().Add(30 * time.Second)}
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
	if len(profile.DisplayName) > 80 || len(profile.StatusText) > 100 || len(profile.StatusEmoji) > 64 || len(profile.Image24) > 2048 || len(profile.Image32) > 2048 || len(profile.Image48) > 2048 || len(profile.Image72) > 2048 || len(profile.Image192) > 2048 || len(profile.Image512) > 2048 || len(profile.Image1024) > 2048 {
		return domain.User{}, ErrInvalidProfile
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.profile_changed", events.String("user_id", string(userID))), time.Now().UTC())
	if err != nil {
		return domain.User{}, err
	}
	return m.Store.UpdateUserProfile(ctx, workspaceID, userID, profile, event)
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
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.profile_changed", events.String("user_id", string(userID))), time.Now().UTC())
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
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.profile_changed", events.String("user_id", string(userID))), time.Now().UTC())
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
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.dnd_snoozed", events.String("user_id", string(userID))), time.Now().UTC())
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
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.dnd_snooze_ended", events.String("user_id", string(userID))), time.Now().UTC())
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
	event, err := newEvent(workspaceID, userID, events.NewPayload("user.dnd_ended", events.String("user_id", string(userID))), time.Now().UTC())
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
		if !page.HasMore {
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
	conversation := domain.Conversation{ID: id, WorkspaceID: workspaceID, Name: "direct", IsPrivate: true, IsDirect: len(members) == 2, IsGroupDirect: len(members) > 2}
	event, err := conversationEvent(workspaceID, "conversation.direct_created", conversation.ID)
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

func (m Messages) CreateConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string, private bool) (domain.Conversation, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
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
	conversation := domain.Conversation{ID: id, WorkspaceID: workspaceID, Name: name, IsPrivate: private}
	event, err := conversationEvent(workspaceID, "conversation.created", conversation.ID)
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
	if conversation.IsDirect {
		return domain.Conversation{}, ErrInvalidConversation
	}
	if conversation.IsGroupDirect {
		name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	} else {
		name = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), "-"))
	}
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n") {
		return domain.Conversation{}, ErrInvalidConversation
	}
	event, err := conversationEvent(workspaceID, "conversation.renamed", conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.RenameConversation(ctx, conversationID, name, event)
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
	return m.Store.SetConversationTopic(ctx, conversationID, topic, event)
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
	return m.Store.SetConversationPurpose(ctx, conversationID, purpose, event)
}

func (m Messages) SetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if conversation.IsDirect || conversation.IsGroupDirect {
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
	event, err := conversationEvent(workspaceID, topic, conversationID)
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
	// Joining is self-service, so it is only ever available for an open channel.
	// A private channel is joined by invitation and a direct conversation by
	// opening it, and neither may be entered by naming an identifier. The store
	// refuses this too, inside the write transaction, which is where the race-free
	// enforcement belongs — but every neighbouring method states its own
	// precondition, and this one silently depended on a backend detail.
	if conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect {
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
	if err := m.Store.AddConversationMember(ctx, conversationID, userID, event); err != nil {
		return domain.Conversation{}, err
	}
	return conversation, nil
}

func (m Messages) InviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	return m.inviteConversationMembers(ctx, workspaceID, userID, conversationID, users, true)
}

func (m Messages) AdminInviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID) (domain.Conversation, error) {
	return m.inviteConversationMembers(ctx, workspaceID, userID, conversationID, users, false)
}

func (m Messages) AdminConvertConversationToPrivate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect {
		return domain.Conversation{}, ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, userID, conversationPayload("conversation.converted_to_private", conversationID), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationPrivate(ctx, conversationID, event)
}

func (m Messages) AdminGetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationPrefs, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPrefs{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID || conversation.IsDirect || conversation.IsGroupDirect {
		return domain.ConversationPrefs{}, store.ErrNotFound
	}
	return m.Store.GetConversationPrefs(ctx, conversationID)
}

func (m Messages) AdminSetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, value domain.ConversationPrefs) (domain.ConversationPrefs, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPrefs{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID || conversation.IsDirect || conversation.IsGroupDirect {
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
	return strings.ToLower(strings.TrimSpace(value))
}

func (m Messages) Emojis(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.CustomEmoji, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.ListEmojis(ctx, workspaceID)
}

func (m Messages) AdminAddEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, url string) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return err
	}
	name, url = normalizeEmojiName(name), strings.TrimSpace(url)
	if name == "" || len(name) > 255 || url == "" || len(url) > 2048 {
		return ErrInvalidEmoji
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("emoji.added", events.String("name", name)), time.Now().UTC())
	if err != nil {
		return err
	}
	err = m.Store.AddEmoji(ctx, domain.CustomEmoji{WorkspaceID: workspaceID, Name: name, URL: url}, event)
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
	if name == "" || target == "" || name == target || len(name) > 255 || len(target) > 255 {
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
	event, err := newEvent(workspaceID, actor, events.NewPayload("usergroup.channels_changed", events.String("usergroup_id", string(id)), events.Strings("channel_ids", conversationIDStrings(combined))), time.Now().UTC())
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
	event, err := newEvent(workspaceID, actor, events.NewPayload("usergroup.channels_changed", events.String("usergroup_id", string(id)), events.Strings("channel_ids", conversationIDStrings(remaining))), time.Now().UTC())
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
	if role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return domain.UserPage{}, ErrInvalidUserGroup
	}
	return m.Store.ListUsersByRole(ctx, workspaceID, role, request)
}

// inviteConversationMembers serves two callers with different authority.
// asConversationMember is the ordinary conversations.invite, where the inviter has
// to be in the conversation; the administrative admin.conversations.invite reaches
// conversations the actor is not in, so it requires a workspace administrator
// instead. Workspace membership alone authorizes neither.
func (m Messages) inviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID, asConversationMember bool) (domain.Conversation, error) {
	if asConversationMember {
		if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
			return domain.Conversation{}, err
		}
	} else if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsDirect || conversation.IsGroupDirect || conversation.Archived {
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
	if conversation.IsDirect || conversation.IsGroupDirect {
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
	if !conversation.IsDirect && !conversation.IsGroupDirect {
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
	return m.Store.RemoveConversationMember(ctx, conversationID, userID, event)
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

func (m Messages) WorkspaceNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceNotificationPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	return m.Store.GetWorkspaceNotificationPreferences(ctx, workspaceID, userID)
}

func (m Messages) SetWorkspaceNotificationPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, level domain.NotificationLevel, keywords []string, activityChannels, activityReminders bool) (domain.WorkspaceNotificationPreferences, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	preferences := domain.WorkspaceNotificationPreferences{
		WorkspaceID: workspaceID, UserID: userID, Level: level,
		Keywords:         domain.NormalizeNotificationKeywords(keywords),
		ActivityChannels: activityChannels, ActivityReminders: activityReminders,
	}
	if !preferences.Valid() {
		return domain.WorkspaceNotificationPreferences{}, store.InvalidArgument("workspace notification preferences are invalid")
	}
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
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "\r\n|\x00") {
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

func (m Messages) AddBookmark(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, title, bookmarkType, link, emoji, entityID, accessLevel, parentID string) (domain.Bookmark, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Bookmark{}, err
	}
	title = strings.TrimSpace(title)
	bookmarkType = strings.TrimSpace(bookmarkType)
	link = strings.TrimSpace(link)
	accessLevel = strings.TrimSpace(accessLevel)
	if title == "" || len(title) > 255 || bookmarkType != "link" || link == "" || accessLevel != "" && accessLevel != "read" && accessLevel != "write" {
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
	if bookmark.Title == "" || len(bookmark.Title) > 255 || bookmark.Type != "link" || bookmark.Link == "" {
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
	if text == "" || messageTextTooLong(text) || postAt.IsZero() {
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
	message, postErr := m.PostWithBlocksAndAttachments(ctx, item.WorkspaceID, item.Author, item.Channel, item.Text, item.Blocks, item.Attachments, item.ThreadTimestamp, string(item.ID), item.AppID)
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
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversation); err != nil {
		return domain.Draft{}, err
	}
	if strings.TrimSpace(text) == "" || messageTextTooLong(text) {
		return domain.Draft{}, ErrInvalidMessage
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
		ThreadTimestamp: thread, Text: text, UpdatedAt: now,
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
	event, err := newEvent(workspaceID, actor, events.NewPayload("usergroup.created", events.String("usergroup_id", string(id)), events.String("handle", handle)), now)
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
	event, err := newEvent(workspaceID, actor, events.NewPayload("usergroup.updated", events.String("usergroup_id", string(id)), events.String("handle", value.Handle)), value.UpdatedAt)
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
	event, err := newEvent(workspaceID, actor, events.NewPayload("usergroup.enabled_changed", events.String("usergroup_id", string(id)), events.Bool("enabled", enabled)), time.Now().UTC())
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
	event, err := newEvent(workspaceID, actor, events.NewPayload("usergroup.users_changed", events.String("usergroup_id", string(id)), events.Strings("user_ids", userIDStrings(normalized))), time.Now().UTC())
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
	if err := m.validateCallUsers(ctx, workspaceID, normalized); err != nil {
		return domain.Call{}, err
	}
	id, err := domain.NewCallID()
	if err != nil {
		return domain.Call{}, err
	}
	value := domain.Call{ID: id, WorkspaceID: workspaceID, ExternalUniqueID: externalUniqueID, ExternalDisplayID: externalDisplayID, JoinURL: joinURL, DesktopAppJoinURL: desktopAppJoinURL, Title: title, CreatedBy: actor, Participants: normalized, StartedAt: startedAt}
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
	return "https://sameoldchat.local/archives/" + url.PathEscape(string(conversation)) + "/p" + strings.ReplaceAll(string(canonical), ".", ""), nil
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
		event, eventErr := newEvent(workspaceID, userID, messagePayload("message.created", message), message.CreatedAt)
		if eventErr != nil {
			return domain.Message{}, eventErr
		}
		if err := m.Store.CreateFileShareMessage(ctx, []domain.FileID{fileID}, message, event); errors.Is(err, store.ErrMessageTimestampTaken) {
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
	message.Deleted = true
	event, err := messageEvent(workspaceID, "message.deleted", message)
	if err != nil {
		return domain.Message{}, err
	}
	if err := m.Store.UpdateMessage(ctx, message, event); err != nil {
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
	return newEvent(workspaceID, message.AuthorID, messagePayload(topic, message), time.Now().UTC())
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

func conversationEvent(workspaceID domain.WorkspaceID, topic string, conversationID domain.ConversationID) (events.Event, error) {
	return newEvent(workspaceID, "", conversationPayload(topic, conversationID), time.Now().UTC())
}

func conversationPayload(topic string, conversationID domain.ConversationID) events.Payload {
	return events.NewPayload(topic, events.String("channel_id", string(conversationID)))
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
	if conversation.IsPrivate {
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
	normalizedBlocks, err := domain.NormalizeBlocks([]byte(request.Blocks))
	normalizedAttachments, attachmentErr := domain.NormalizeAttachments([]byte(request.Attachments))
	if err != nil || attachmentErr != nil || (text == "" && normalizedBlocks == "" && normalizedAttachments == "") || messageTextTooLong(text) || request.PostAt.IsZero() {
		return domain.ScheduledMessage{}, ErrInvalidMessage
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
		Text: text, Blocks: normalizedBlocks, Attachments: normalizedAttachments,
		ThreadTimestamp: request.ThreadTimestamp, PostAt: postAt, CreatedAt: now,
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
	if idempotencyKey != "" {
		cached, err := m.Store.GetIdempotentMessage(ctx, workspaceID, authorID, idempotencyKey)
		if err == nil {
			return cached, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return domain.Message{}, err
		}
	}
	if messagePayloadTooLong(blocks, attachments) {
		return domain.Message{}, ErrInvalidMessage
	}
	normalizedBlocks, err := domain.NormalizeBlocks([]byte(blocks))
	normalizedAttachments, attachmentErr := domain.NormalizeAttachments([]byte(attachments))
	if err != nil || attachmentErr != nil || strings.TrimSpace(string(conversation)) == "" || (strings.TrimSpace(text) == "" && normalizedBlocks == "" && normalizedAttachments == "") || messageTextTooLong(text) {
		return domain.Message{}, ErrInvalidMessage
	}
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return domain.Message{}, err
	}
	if err := m.requireConversationMembership(ctx, workspaceID, authorID, conversation); err != nil {
		return domain.Message{}, err
	}
	target, err := m.Store.GetConversation(ctx, conversation)
	if err != nil || target.WorkspaceID != workspaceID {
		return domain.Message{}, store.ErrNotFound
	}
	if target.Archived {
		return domain.Message{}, ErrConversationAlreadyArchived
	}
	threadTimestampValue := domain.MessageTimestamp("")
	if threadTimestamp != "" {
		createdAt, err := domain.ParseMessageTimestamp(threadTimestamp)
		if err != nil {
			return domain.Message{}, ErrInvalidTimestamp
		}
		parent, err := m.Store.GetMessageByCreatedAt(ctx, conversation, createdAt)
		if err != nil || parent.WorkspaceID != workspaceID {
			return domain.Message{}, store.ErrNotFound
		}
		threadTimestampValue = threadTimestamp
	}
	id, err := domain.NewMessageID()
	if err != nil {
		return domain.Message{}, err
	}
	message := domain.Message{ID: id, WorkspaceID: workspaceID, Conversation: conversation, AuthorID: authorID, AppID: appID, Text: text, Blocks: normalizedBlocks, Attachments: normalizedAttachments, ThreadTimestamp: threadTimestampValue, CreatedAt: domain.MessageInstant(time.Now())}
	// A message's ts is its public identifier and it carries microseconds, so two
	// messages in one conversation may not be created on the same microsecond.
	// The repository refuses the collision; the remedy is the next microsecond,
	// and the event and the response are rebuilt from the instant that was
	// actually taken so the client is told the identifier the row really has.
	// This is the same construction the real Slack timestamp uses, and it is why
	// the identifier cannot be merged no matter how coarse the host clock is.
	for {
		event, err := newEvent(workspaceID, authorID, messagePayload("message.created", message), message.CreatedAt)
		if err != nil {
			return domain.Message{}, err
		}
		err = m.Store.CreateMessage(ctx, message, event, idempotencyKey)
		if errors.Is(err, store.ErrMessageTimestampTaken) {
			message.CreatedAt = message.CreatedAt.Add(time.Microsecond)
			continue
		}
		if err != nil {
			if errors.Is(err, store.ErrIdempotencyConflict) {
				return m.Store.GetIdempotentMessage(ctx, workspaceID, authorID, idempotencyKey)
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
	event, err := messageEvent(workspaceID, "message.changed", message)
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
			return nil, ErrInvalidExternalUpload
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
		return completed, nil
	}
	for _, value := range values {
		if value.Status != domain.ExternalUploadUploaded {
			return nil, ErrInvalidExternalUpload
		}
	}
	files := make([]domain.File, len(values))
	eventsToEmit := make([]events.Event, len(values))
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
		emitted, err := newEvent(workspaceID, userID, events.NewPayload("file.created", events.String("file_id", string(fileID)), events.Strings("channel_ids", conversationIDStrings(channels))), createdAt)
		if err != nil {
			return nil, err
		}
		eventsToEmit[index] = emitted
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
		emitted, eventErr := newEvent(workspaceID, userID, messagePayload("message.created", messages[index]), messages[index].CreatedAt)
		if eventErr != nil {
			return nil, eventErr
		}
		messageEvents[index] = emitted
	}
	for {
		err := m.Store.CompleteExternalUploads(ctx, completions, files, channels, eventsToEmit, messages, messageEvents)
		if errors.Is(err, store.ErrMessageTimestampTaken) {
			// Completion is transactional, so none of the tickets or shares
			// changed. Move the public message timestamps together and retry;
			// the upload must not fail merely because another message occupied
			// the host clock's current microsecond.
			for index := range messages {
				messages[index].CreatedAt = messages[index].CreatedAt.Add(time.Microsecond)
				messageEvents[index], err = newEvent(workspaceID, userID, messagePayload("message.created", messages[index]), messages[index].CreatedAt)
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
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		settled, allSettled, settleErr := m.completedExternalUploadFiles(ctx, workspaceID, userID, completions)
		if settleErr != nil || !allSettled {
			return nil, err
		}
		files = settled
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
		if value.ID == "" {
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
