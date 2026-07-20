package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

var (
	ErrInvalidMessage           = errors.New("message text and conversation are required")
	ErrInvalidTimestamp         = errors.New("message timestamp is invalid")
	ErrMessageNotOwned          = errors.New("message is not owned by user")
	ErrMessageAlreadyDeleted    = errors.New("message is already deleted")
	ErrInvalidConversation      = errors.New("conversation name is invalid")
	ErrInvalidWorkspace         = errors.New("workspace settings are invalid")
	ErrInvalidConversationPrefs = errors.New("conversation preferences are invalid")
	ErrInvalidReaction          = errors.New("reaction name is invalid")
	ErrBlobUnavailable          = errors.New("blob storage is unavailable")
	ErrInvalidFile              = errors.New("file metadata is invalid")
	ErrInvalidSearch            = errors.New("search query is invalid")
	ErrInvalidProfile           = errors.New("user profile is invalid")
	ErrInvalidPresence          = errors.New("user presence is invalid")
	ErrInvalidSnooze            = errors.New("snooze duration must be between 1 and 1440 minutes")
	ErrInvalidReminder          = errors.New("reminder text, user, and time are required")
	ErrInvalidUserGroup         = errors.New("user group name, handle, and members are invalid")
	ErrInvalidCall              = errors.New("call external id and join URL are required")
	ErrInvalidEphemeral         = errors.New("ephemeral message recipient, conversation, and text are required")
	ErrInvalidAccessLog         = errors.New("access log fields are invalid")
	ErrInvalidEmoji             = errors.New("custom emoji name or URL is invalid")
	ErrEmojiAlreadyExists       = errors.New("custom emoji already exists")
	ErrInvalidRemoteFile        = errors.New("remote file metadata is invalid")
	ErrInvalidInviteRequest     = errors.New("invite request is invalid")
	ErrInvalidAppApproval       = errors.New("app approval is invalid")
	ErrInvalidView              = errors.New("view payload is invalid")
	ErrInvalidWorkflowStep      = errors.New("workflow step payload is invalid")
	ErrInvalidDialog            = errors.New("dialog payload is invalid")
	ErrInvalidBot               = errors.New("bot identifier is required")
	ErrInvalidMigration         = errors.New("migration user identifiers are invalid")
	ErrInvalidOAuth             = errors.New("oauth authorization is invalid")
	ErrInvalidOAuthClient       = errors.New("oauth client is invalid")
	ErrInvalidIntegrationLogs   = errors.New("integration log arguments are invalid")
	ErrInvalidBookmark          = errors.New("bookmark title, type, and link are invalid")
)

type Messages struct {
	Store store.Store
	Blob  blob.Store
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

func (m Messages) ListAppEventsAfter(ctx context.Context, appID domain.AppID, after uint64, limit int) ([]events.Record, error) {
	return m.Store.ListAppEventsAfter(ctx, appID, after, limit)
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.RevokeUserSessions(ctx, workspaceID, targetID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.sessions_reset", Payload: string(targetID), CreatedAt: time.Now().UTC()})
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
		return errors.New("auth method is incomplete")
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
		return errors.New("external identity is incomplete")
	}
	return m.Store.CreateExternalIdentity(ctx, identity)
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
	file := domain.File{ID: id, WorkspaceID: workspaceID, Uploader: userID, Name: name, Title: title, MIMEType: mimeType, BlobKey: string(workspaceID) + "/" + string(id), Size: size, CreatedAt: time.Now().UTC()}
	if _, err := m.Blob.Put(ctx, file.BlobKey, size, source); err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return domain.File{}, ErrBlobUnavailable
		}
		return domain.File{}, err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		cleanupErr := m.Blob.Delete(context.Background(), file.BlobKey)
		if cleanupErr != nil {
			return domain.File{}, errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		return domain.File{}, err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "file.created", Payload: string(file.ID), CreatedAt: file.CreatedAt}
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

func (m Messages) DeleteFile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, fileID domain.FileID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	file, err := m.Store.GetFile(ctx, fileID)
	if err != nil || file.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	deletedAt := time.Now().UTC()
	if err := m.Store.DeleteFile(ctx, fileID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: events.FileBlobDeleteTopic, Payload: file.BlobKey, CreatedAt: deletedAt}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.DeleteFileComment(ctx, workspaceID, fileID, commentID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "file.comment_deleted", Payload: string(fileID) + "|" + string(commentID), CreatedAt: time.Now().UTC()})
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
		eventID, err := domain.NewEventID()
		if err != nil {
			return domain.File{}, err
		}
		if err := m.Store.ShareFilePublic(ctx, workspaceID, fileID, token, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "file.public_shared", Payload: string(fileID), CreatedAt: time.Now().UTC()}); err != nil {
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
		eventID, err := domain.NewEventID()
		if err != nil {
			return domain.File{}, err
		}
		if err := m.Store.RevokeFilePublic(ctx, workspaceID, fileID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "file.public_revoked", Payload: string(fileID), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.RemoteFile{}, err
	}
	if err := m.Store.AddRemoteFile(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "remote_file.created", Payload: string(value.ID), CreatedAt: value.CreatedAt}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.RemoveRemoteFile(ctx, workspaceID, lookup, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "remote_file.removed", Payload: string(lookup.ID) + lookup.ExternalID, CreatedAt: time.Now().UTC()})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return m.Store.SetRemoteFileShares(ctx, workspaceID, lookup, channels, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "remote_file.shared", Payload: string(lookup.ID) + lookup.ExternalID, CreatedAt: time.Now().UTC()})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return m.Store.UpdateRemoteFile(ctx, workspaceID, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "remote_file.updated", Payload: string(value.ID), CreatedAt: time.Now().UTC()})
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

func (m Messages) IntegrationLogs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID, changeType, serviceID, userFilter string, count, page int) (domain.IntegrationLogPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
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
			value := domain.IntegrationLog{AppID: domain.AppID(strings.TrimSpace(record.Event.Payload)), AppType: "app", ChangeType: change, Date: record.Event.CreatedAt, Scope: "", UserID: record.Event.ActorID}
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetUserDeleted(ctx, workspaceID, targetID, true, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.removed", Payload: string(targetID), CreatedAt: time.Now().UTC()})
}

func (m Messages) SetUserRole(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID, role domain.WorkspaceRole) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return errors.New("invalid workspace role")
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetWorkspaceRole(ctx, workspaceID, targetID, role, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workspace.role_changed", Payload: string(targetID), CreatedAt: time.Now().UTC()})
}

func (m Messages) SetUserExpiration(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID, expiration time.Time) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if targetID == "" || (!expiration.IsZero() && expiration.Before(time.Unix(0, 0).UTC())) {
		return ErrInvalidWorkspace
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetUserExpiration(ctx, workspaceID, targetID, expiration.UTC(), events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.expiration_changed", Payload: string(targetID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminRenameConversation(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, name string) (domain.Conversation, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.RenameConversation(ctx, conversationID, name, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.renamed_by_admin", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminSetConversationArchived(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, archived bool) (domain.Conversation, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationArchived(ctx, conversationID, archived, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.archive_changed_by_admin", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminDeleteConversation(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if conversation.IsDirect || conversation.IsGroupDirect {
		return ErrInvalidConversation
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.DeleteConversation(ctx, workspaceID, conversationID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.deleted", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminAddConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID) error {
	return m.changeConversationAccessGroup(ctx, workspaceID, actorID, conversationID, groupID, true)
}

func (m Messages) AdminRemoveConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID) error {
	return m.changeConversationAccessGroup(ctx, workspaceID, actorID, conversationID, groupID, false)
}

func (m Messages) changeConversationAccessGroup(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, groupID domain.UserGroupID, add bool) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	topic := "conversation.access_group_added"
	if !add {
		topic = "conversation.access_group_removed"
	}
	return m.Store.SetConversationAccessGroups(ctx, workspaceID, conversationID, groups, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: topic, Payload: string(conversationID) + "\x00" + string(groupID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminListConversationAccessGroups(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) ([]domain.UserGroupID, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	topic := "invite_request.approved"
	if status == domain.InviteRequestDenied {
		topic = "invite_request.denied"
	}
	return m.Store.SetInviteRequestStatus(ctx, workspaceID, id, status, time.Now().UTC(), events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: topic, Payload: string(id), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminListInviteRequests(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, status domain.InviteRequestStatus, request domain.PageRequest) (domain.InviteRequestPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.InviteRequestPage{}, err
	}
	if status != domain.InviteRequestPending && status != domain.InviteRequestApproved && status != domain.InviteRequestDenied {
		return domain.InviteRequestPage{}, ErrInvalidInviteRequest
	}
	return m.Store.ListInviteRequests(ctx, workspaceID, status, request)
}

func (m Messages) AdminInviteUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, email string, channels []domain.ConversationID, customMessage, realName string, resend, restricted, ultraRestricted bool, guestExpirationAt time.Time) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	value := domain.InviteRequest{ID: domain.InviteRequestID(id), WorkspaceID: workspaceID, Email: email, RequestedBy: actorID, ChannelIDs: normalizedChannels, CustomMessage: customMessage, RealName: realName, Resend: resend, Restricted: restricted, UltraRestricted: ultraRestricted, GuestExpirationAt: guestExpirationAt.UTC(), Status: domain.InviteRequestPending, CreatedAt: now}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "invite_request.created", Payload: string(value.ID), CreatedAt: now}
	return m.Store.CreateInviteRequest(ctx, value, event)
}

func (m Messages) AdminCreateUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, email, realName string, role domain.WorkspaceRole) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.User{}, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	realName = strings.TrimSpace(realName)
	if workspaceID == "" || email == "" || !strings.Contains(email, "@") || len(email) > 320 || realName == "" || len(realName) > 200 {
		return domain.User{}, ErrInvalidInviteRequest
	}
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin {
		return domain.User{}, ErrInvalidInviteRequest
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.User{}, err
	}
	now := time.Now().UTC()
	user := domain.User{ID: id, WorkspaceID: workspaceID, Email: email, Name: realName, RealName: realName, Presence: domain.PresenceAuto}
	membership := domain.WorkspaceMembership{WorkspaceID: workspaceID, UserID: id, Role: role, Active: true}
	if err := m.Store.CreateUser(ctx, user, membership, events.Event{ID: eventID, WorkspaceID: workspaceID, ActorID: actorID, Topic: "user.created", Payload: string(id), CreatedAt: now}); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (m Messages) AdminAssignUser(ctx context.Context, workspaceID domain.WorkspaceID, actorID, targetID domain.UserID, channels []domain.ConversationID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.assigned", Payload: string(targetID) + "|" + strings.Join(conversationIDStrings(normalized), ","), CreatedAt: time.Now().UTC()}
	return m.Store.AssignUser(ctx, workspaceID, targetID, normalized, event)
}

func (m Messages) AdminApproveApp(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	return m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalApproved)
}

func (m Messages) AdminRestrictApp(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID) error {
	return m.changeAppApproval(ctx, workspaceID, actorID, appID, requestID, domain.AppApprovalRestricted)
}

func (m Messages) changeAppApproval(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID, requestID domain.AppRequestID, status domain.AppApprovalStatus) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return m.Store.SetAppApproval(ctx, workspaceID, appID, requestID, status, now, events.Event{ID: eventID, WorkspaceID: workspaceID, ActorID: actorID, Topic: "app." + string(status), Payload: string(appID), CreatedAt: now})
}

func (m Messages) AdminListApps(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, status domain.AppApprovalStatus, request domain.PageRequest) (domain.AppApprovalPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	value := domain.AppPermissionRequest{ID: id, WorkspaceID: workspaceID, RequesterID: actor, TargetUserID: target, Scopes: scopes, TriggerID: triggerID, CreatedAt: time.Now().UTC()}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, ActorID: actor, Topic: "app.permissions_requested", Payload: string(id), CreatedAt: value.CreatedAt}
	return m.Store.CreateAppPermissionRequest(ctx, value, event)
}

func viewPayload(payload string) (string, string, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", "", ErrInvalidView
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &fields); err != nil || fields == nil {
		return "", "", ErrInvalidView
	}
	var viewType string
	if raw, ok := fields["type"]; !ok || json.Unmarshal(raw, &viewType) != nil || strings.TrimSpace(viewType) == "" {
		return "", "", ErrInvalidView
	}
	var externalID string
	if raw, ok := fields["external_id"]; ok && json.Unmarshal(raw, &externalID) != nil {
		return "", "", ErrInvalidView
	}
	return viewType, strings.TrimSpace(externalID), nil
}

func viewHash(id domain.ViewID, payload string, now time.Time) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(string(id)+"\x00"+payload+"\x00"+now.UTC().Format(time.RFC3339Nano))))
}

func (m Messages) OpenView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, triggerID, payload string) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	if strings.TrimSpace(triggerID) == "" {
		return domain.View{}, ErrInvalidView
	}
	return m.createView(ctx, workspaceID, actor, payload, "", "", "", "view.opened")
}

func (m Messages) PublishView(ctx context.Context, workspaceID domain.WorkspaceID, actor, target domain.UserID, payload, expectedHash string) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	user, err := m.Store.GetUser(ctx, target)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.View{}, store.ErrNotFound
	}
	viewType, _, err := viewPayload(payload)
	if err != nil || viewType != "home" {
		return domain.View{}, ErrInvalidView
	}
	current, err := m.Store.GetPublishedView(ctx, workspaceID, target)
	if errors.Is(err, store.ErrNotFound) {
		return m.createView(ctx, workspaceID, target, payload, "", "", "", "view.published")
	}
	if err != nil {
		return domain.View{}, err
	}
	return m.updateView(ctx, workspaceID, actor, current, payload, expectedHash, "view.published")
}

func (m Messages) PushView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, triggerID, payload string) (domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.View{}, err
	}
	if strings.TrimSpace(triggerID) == "" {
		return domain.View{}, ErrInvalidView
	}
	parent, err := m.Store.GetLatestView(ctx, workspaceID, actor, "modal")
	if errors.Is(err, store.ErrNotFound) {
		return m.createView(ctx, workspaceID, actor, payload, "", "", "", "view.pushed")
	}
	if err != nil {
		return domain.View{}, err
	}
	return m.createView(ctx, workspaceID, actor, payload, parent.RootViewID, parent.ID, "", "view.pushed")
}

func (m Messages) UpdateView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, viewID, externalID, payload, expectedHash string) (domain.View, error) {
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
		current, err = m.Store.GetViewByExternalID(ctx, workspaceID, strings.TrimSpace(externalID))
	}
	if err != nil {
		return domain.View{}, err
	}
	return m.updateView(ctx, workspaceID, actor, current, payload, expectedHash, "view.updated")
}

func (m Messages) createView(ctx context.Context, workspaceID domain.WorkspaceID, user domain.UserID, payload string, rootID, previousID domain.ViewID, externalID, topic string) (domain.View, error) {
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
	value := domain.View{ID: id, WorkspaceID: domain.WorkspaceID(workspaceID), UserID: domain.UserID(user), Type: viewType, ExternalID: externalID, Payload: strings.TrimSpace(payload), Hash: viewHash(id, payload, now), CreatedAt: now, UpdatedAt: now}
	if rootID == "" {
		value.RootViewID = id
	} else {
		value.RootViewID = rootID
	}
	value.PreviousViewID = previousID
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.View{}, err
	}
	if err := m.Store.CreateView(ctx, value, events.Event{ID: eventID, WorkspaceID: value.WorkspaceID, Topic: topic, Payload: string(value.ID), CreatedAt: now}); err != nil {
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
	value := current
	value.Type = viewType
	value.ExternalID = externalID
	value.Payload = strings.TrimSpace(payload)
	value.Hash = viewHash(value.ID, value.Payload, now)
	value.UpdatedAt = now
	value.UserID = current.UserID
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.View{}, err
	}
	return m.Store.UpdateView(ctx, value, expectedHash, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: topic, Payload: string(value.ID), CreatedAt: now})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetWorkflowStep(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workflow.step_" + string(value.Status), Payload: id, CreatedAt: now})
}

func (m Messages) OpenDialog(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, triggerID, payload string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	if strings.TrimSpace(triggerID) == "" {
		return ErrInvalidDialog
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.CreateDialog(ctx, domain.Dialog{ID: id, WorkspaceID: workspaceID, UserID: actor, Payload: payload, CreatedAt: now}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "dialog.opened", Payload: string(id), CreatedAt: now})
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
	if client.SecretHash != domain.HashToken(clientSecret) {
		return domain.OAuthToken{}, ErrInvalidOAuthClient
	}
	accessToken, err := domain.NewOAuthToken()
	if err != nil {
		return domain.OAuthToken{}, err
	}
	token, err := m.Store.ExchangeOAuthCode(ctx, clientID, clientSecret, code, redirectURI, accessToken, domain.OAuthToken{TokenType: "user"})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthToken{}, ErrInvalidOAuth
		}
		return domain.OAuthToken{}, err
	}
	token.AppID = client.AppID
	token.TokenType = "user"
	if err := m.Store.CreateAppInstallation(ctx, domain.AppInstallation{AppID: client.AppID, WorkspaceID: token.WorkspaceID, Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.User{}, err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.profile_changed", Payload: string(userID), CreatedAt: time.Now().UTC()}
	return m.Store.UpdateUserProfile(ctx, workspaceID, userID, profile, event)
}

const maxUserPhotoBytes = 10 << 20

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
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	if !strings.HasPrefix(mimeType, "image/") || size <= 0 || size > maxUserPhotoBytes || source == nil {
		return domain.User{}, ErrInvalidProfile
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return domain.User{}, store.ErrNotFound
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
	eventID, err := domain.NewEventID()
	if err != nil {
		if cleanupErr := m.Blob.Delete(context.Background(), key); cleanupErr != nil {
			return domain.User{}, errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		return domain.User{}, err
	}
	updated, err := m.Store.UpdateUserProfile(ctx, workspaceID, userID, user.Profile, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.profile_changed", Payload: string(userID), CreatedAt: time.Now().UTC()})
	if err != nil {
		if cleanupErr := m.Blob.Delete(context.Background(), key); cleanupErr != nil {
			return domain.User{}, errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		return domain.User{}, err
	}
	if oldToken != "" {
		cleanupID, cleanupErr := domain.NewEventID()
		if cleanupErr != nil {
			return domain.User{}, cleanupErr
		}
		oldKey := string(workspaceID) + "/users/" + string(userID) + "/" + oldToken
		if cleanupErr := m.Store.AppendEvent(ctx, events.Event{ID: cleanupID, WorkspaceID: workspaceID, Topic: events.UserPhotoBlobDeleteTopic, Payload: oldKey, CreatedAt: time.Now().UTC()}); cleanupErr != nil {
			return domain.User{}, cleanupErr
		}
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	if _, err := m.Store.UpdateUserProfile(ctx, workspaceID, userID, user.Profile, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.profile_changed", Payload: string(userID), CreatedAt: time.Now().UTC()}); err != nil {
		return err
	}
	if oldToken != "" {
		cleanupID, err := domain.NewEventID()
		if err != nil {
			return err
		}
		oldKey := string(workspaceID) + "/users/" + string(userID) + "/" + oldToken
		return m.Store.AppendEvent(ctx, events.Event{ID: cleanupID, WorkspaceID: workspaceID, Topic: events.UserPhotoBlobDeleteTopic, Payload: oldKey, CreatedAt: time.Now().UTC()})
	}
	return nil
}

func (m Messages) SetUserPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, presence domain.Presence) (domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.User{}, err
	}
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return domain.User{}, ErrInvalidPresence
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.User{}, err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.presence_changed", Payload: string(userID), CreatedAt: time.Now().UTC()}
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	if err := m.Store.SetDoNotDisturb(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.dnd_snoozed", Payload: string(userID), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	if err := m.Store.SetDoNotDisturb(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.dnd_snooze_ended", Payload: string(userID), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetDoNotDisturb(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "user.dnd_ended", Payload: string(userID), CreatedAt: time.Now().UTC()})
}

func (m Messages) Users(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.UserPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.UserPage{}, err
	}
	return m.Store.ListUsers(ctx, workspaceID, request)
}

func (m Messages) AdminListUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, request domain.PageRequest) (domain.AdminUserPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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

func (m Messages) WorkspaceInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.GetWorkspace(ctx, workspaceID)
}

func (m Messages) AdminCreateWorkspace(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, domainName, name, description string, discoverability domain.WorkspaceDiscoverability) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Workspace{}, err
	}
	value := domain.Workspace{ID: id, Domain: domainName, Name: name, Description: description, Discoverability: discoverability}
	event := events.Event{ID: eventID, WorkspaceID: id, Topic: "workspace.created", Payload: string(id), CreatedAt: time.Now().UTC()}
	if err := m.Store.CreateWorkspace(ctx, value, event); err != nil {
		return domain.Workspace{}, err
	}
	return value, nil
}

func (m Messages) TeamBillableInfo(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) (domain.BillableInfo, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
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
	if len(members) < 2 || len(members) > 8 {
		return domain.Conversation{}, ErrInvalidConversation
	}
	if existing, err := m.Store.FindDirectConversation(ctx, workspaceID, members); err == nil {
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
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
	}
	name = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), "-"))
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
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
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
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
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
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.Conversation{}, err
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
	event, err := conversationEvent(workspaceID, "conversation.member_added", conversationID)
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
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsPrivate || conversation.IsDirect || conversation.IsGroupDirect {
		return domain.Conversation{}, ErrInvalidConversation
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Conversation{}, err
	}
	return m.Store.SetConversationPrivate(ctx, conversationID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.converted_to_private", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminGetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (domain.ConversationPrefs, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPrefs{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID || conversation.IsDirect || conversation.IsGroupDirect {
		return domain.ConversationPrefs{}, store.ErrNotFound
	}
	return m.Store.GetConversationPrefs(ctx, conversationID)
}

func (m Messages) AdminSetConversationPrefs(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, value domain.ConversationPrefs) (domain.ConversationPrefs, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	return m.Store.SetConversationPrefs(ctx, conversationID, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.preferences_changed", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
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
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ConversationPage{}, err
	}
	query = strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if query == "" || len(query) > 200 {
		return domain.ConversationPage{}, ErrInvalidConversation
	}
	return m.Store.SearchConversations(ctx, workspaceID, query, request)
}

func (m Messages) AdminConversationTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, request domain.PageRequest) ([]domain.WorkspaceID, bool, domain.Cursor, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	seen := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, teamID := range teams {
		teamID = domain.WorkspaceID(strings.TrimSpace(string(teamID)))
		if teamID == "" {
			return ErrInvalidConversation
		}
		if _, err := m.Store.GetWorkspace(ctx, teamID); err != nil {
			return ErrInvalidConversation
		}
		seen[teamID] = struct{}{}
	}
	if len(seen) == 0 && !orgChannel {
		return ErrInvalidConversation
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	normalized := make([]domain.WorkspaceID, 0, len(seen))
	for teamID := range seen {
		normalized = append(normalized, teamID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return m.Store.SetConversationTeams(ctx, workspaceID, conversationID, normalized, orgChannel, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.teams_changed", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminDisconnectSharedConversation(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, leaving []domain.WorkspaceID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	for index, team := range leaving {
		leaving[index] = domain.WorkspaceID(strings.TrimSpace(string(team)))
		if leaving[index] == "" {
			return ErrInvalidConversation
		}
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.DisconnectConversationTeams(ctx, workspaceID, conversationID, leaving, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.shared_disconnected", Payload: string(conversationID), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminConnectedChannelInfo(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, channels []domain.ConversationID, teams []domain.WorkspaceID, request domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	name, url = normalizeEmojiName(name), strings.TrimSpace(url)
	if name == "" || len(name) > 255 || url == "" || len(url) > 2048 {
		return ErrInvalidEmoji
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	err = m.Store.AddEmoji(ctx, domain.CustomEmoji{WorkspaceID: workspaceID, Name: name, URL: url}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "emoji.added", Payload: name, CreatedAt: time.Now().UTC()})
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrEmojiAlreadyExists
	}
	return err
}

func (m Messages) AdminAddEmojiAlias(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, target string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	err = m.Store.AddEmoji(ctx, domain.CustomEmoji{WorkspaceID: workspaceID, Name: name, AliasFor: target}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "emoji.alias_added", Payload: name + "|" + target, CreatedAt: time.Now().UTC()})
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrEmojiAlreadyExists
	}
	return err
}

func (m Messages) AdminRemoveEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	name = normalizeEmojiName(name)
	if name == "" {
		return ErrInvalidEmoji
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.RemoveEmoji(ctx, workspaceID, name, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "emoji.removed", Payload: name, CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminRenameEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, oldName, newName string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	oldName, newName = normalizeEmojiName(oldName), normalizeEmojiName(newName)
	if oldName == "" || newName == "" || oldName == newName || len(oldName) > 255 || len(newName) > 255 {
		return ErrInvalidEmoji
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	err = m.Store.RenameEmoji(ctx, workspaceID, oldName, newName, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "emoji.renamed", Payload: oldName + "|" + newName, CreatedAt: time.Now().UTC()})
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	combined := append(append([]domain.ConversationID(nil), value.Channels...), add...)
	combined, err = normalizeUserGroupChannels(combined)
	if err != nil {
		return err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetUserGroupChannels(ctx, workspaceID, id, combined, actor, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "usergroup.channels_changed", Payload: string(id), CreatedAt: time.Now().UTC()})
}

// AdminAddUserGroupTeams validates the organization-level association against
// this process's single-workspace topology. The workspace is already implicit
// in UserGroup, so a valid association needs no additional persisted edge.
func (m Messages) AdminAddUserGroupTeams(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, teams []domain.WorkspaceID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetUserGroupChannels(ctx, workspaceID, id, remaining, actor, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "usergroup.channels_changed", Payload: string(id), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminSetWorkspaceName(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, name string) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if name == "" || len(name) > 255 {
		return domain.Workspace{}, ErrInvalidConversation
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceName(ctx, workspaceID, name, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workspace.name_changed", Payload: name, CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminSetWorkspaceDescription(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, description string) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	description = strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
	if len(description) > 255 {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceDescription(ctx, workspaceID, description, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workspace.description_changed", Payload: description, CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminSetWorkspaceDiscoverability(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, discoverability domain.WorkspaceDiscoverability) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	if !discoverability.Valid() {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceDiscoverability(ctx, workspaceID, discoverability, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workspace.discoverability_changed", Payload: string(discoverability), CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminSetWorkspaceIcon(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, iconURL string) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	iconURL = strings.TrimSpace(iconURL)
	parsed, err := url.ParseRequestURI(iconURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(iconURL) > 2048 {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceIcon(ctx, workspaceID, iconURL, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workspace.icon_changed", Payload: iconURL, CreatedAt: time.Now().UTC()})
}

func (m Messages) AdminSetWorkspaceDefaultChannels(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, channels []domain.ConversationID) (domain.Workspace, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.Workspace{}, err
	}
	channels, err := normalizeWorkspaceDefaultChannels(channels)
	if err != nil {
		return domain.Workspace{}, err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Workspace{}, err
	}
	return m.Store.SetWorkspaceDefaultChannels(ctx, workspaceID, channels, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "workspace.default_channels_changed", Payload: strings.Join(conversationIDStrings(channels), ","), CreatedAt: time.Now().UTC()})
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

func (m Messages) AdminTeamUsers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, role domain.WorkspaceRole, request domain.PageRequest) (domain.UserPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.UserPage{}, err
	}
	if role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return domain.UserPage{}, ErrInvalidUserGroup
	}
	return m.Store.ListUsersByRole(ctx, workspaceID, role, request)
}

func (m Messages) inviteConversationMembers(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, users []domain.UserID, requireMembership bool) (domain.Conversation, error) {
	if requireMembership {
		if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
			return domain.Conversation{}, err
		}
	} else if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
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
	event, err := conversationEvent(workspaceID, "conversation.members_invited", conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	payloadUsers := make([]string, 0, len(normalized))
	for _, targetID := range normalized {
		payloadUsers = append(payloadUsers, string(targetID))
	}
	event.Payload = string(conversationID) + "|" + strings.Join(payloadUsers, ",")
	if err := m.Store.InviteConversationMembers(ctx, conversationID, normalized, event); err != nil {
		return domain.Conversation{}, err
	}
	return conversation, nil
}

func (m Messages) LeaveConversation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) error {
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.member_left", Payload: string(conversationID) + "|" + string(userID), CreatedAt: time.Now().UTC()}
	return m.Store.RemoveConversationMember(ctx, conversationID, userID, event)
}

func (m Messages) KickConversationMember(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, targetID domain.UserID) error {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	target, err := m.Store.GetUser(ctx, targetID)
	if err != nil || target.WorkspaceID != workspaceID || target.Deleted {
		return store.ErrNotFound
	}
	event, err := conversationEvent(workspaceID, "conversation.member_kicked", conversationID)
	if err != nil {
		return err
	}
	event.Payload = string(conversationID) + "|" + string(targetID)
	return m.Store.RemoveConversationMember(ctx, conversationID, targetID, event)
}

func (m Messages) MarkRead(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.ReadCursor, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.ReadCursor{}, err
	}
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		return domain.ReadCursor{}, ErrInvalidTimestamp
	}
	now := time.Now().UTC()
	cursor := domain.ReadCursor{WorkspaceID: workspaceID, UserID: userID, Conversation: conversationID, LastRead: timestamp, UpdatedAt: now}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.ReadCursor{}, err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "conversation.read", Payload: string(conversationID) + "|" + string(timestamp), CreatedAt: now}
	if err := m.Store.SetReadCursor(ctx, cursor, event); err != nil {
		return domain.ReadCursor{}, err
	}
	return cursor, nil
}

func (m Messages) AddReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) error {
	reaction, err := m.reactionFor(ctx, workspaceID, userID, conversationID, timestamp, name)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	reaction.CreatedAt = now
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "reaction.added", Payload: string(reaction.Message) + "|" + reaction.Name + "|" + string(userID), CreatedAt: now}
	return m.Store.AddReaction(ctx, reaction, event)
}

func (m Messages) RemoveReaction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp, name string) error {
	reaction, err := m.reactionFor(ctx, workspaceID, userID, conversationID, timestamp, name)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "reaction.removed", Payload: string(reaction.Message) + "|" + reaction.Name + "|" + string(userID), CreatedAt: now}
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.AddPin(ctx, domain.Pin{Message: message.ID, UserID: userID, CreatedAt: now}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "pin.added", Payload: string(message.ID) + "|" + string(userID), CreatedAt: now})
}

func (m Messages) RemovePin(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.RemovePin(ctx, domain.Pin{Message: message.ID, UserID: userID}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "pin.removed", Payload: string(message.ID) + "|" + string(userID), CreatedAt: now})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.AddStar(ctx, domain.Star{Message: message, Conversation: conversationID, UserID: userID, CreatedAt: now}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "star.added", Payload: string(message.ID) + "|" + string(userID), CreatedAt: now})
}

func (m Messages) RemoveStar(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) error {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.RemoveStar(ctx, domain.Star{Message: message, Conversation: conversationID, UserID: userID}, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "star.removed", Payload: string(message.ID) + "|" + string(userID), CreatedAt: now})
}

func (m Messages) Stars(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, "", false, err
	}
	return m.Store.ListStars(ctx, workspaceID, userID, request)
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Bookmark{}, err
	}
	if err := m.Store.CreateBookmark(ctx, bookmark, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "bookmark.created", Payload: string(id), CreatedAt: now}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Bookmark{}, err
	}
	return m.Store.UpdateBookmark(ctx, bookmark, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "bookmark.updated", Payload: string(id), CreatedAt: bookmark.UpdatedAt})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.DeleteBookmark(ctx, workspaceID, conversationID, id, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "bookmark.removed", Payload: string(id), CreatedAt: now})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Reminder{}, err
	}
	if err := m.Store.CreateReminder(ctx, reminder, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "reminder.created", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.CompleteReminder(ctx, workspaceID, userID, reminderID, time.Now().UTC(), events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "reminder.completed", Payload: string(reminderID), CreatedAt: time.Now().UTC()})
}

func (m Messages) DeleteReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, reminderID domain.ReminderID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.DeleteReminder(ctx, workspaceID, userID, reminderID, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "reminder.deleted", Payload: string(reminderID), CreatedAt: time.Now().UTC()})
}

func (m Messages) ScheduleMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, text string, postAt time.Time) (domain.ScheduledMessage, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, channel); err != nil {
		return domain.ScheduledMessage{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 40000 || postAt.IsZero() || !postAt.After(time.Now().UTC()) {
		return domain.ScheduledMessage{}, ErrInvalidMessage
	}
	id, err := domain.NewScheduledMessageID()
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	now := time.Now().UTC()
	value := domain.ScheduledMessage{WorkspaceID: workspaceID, ID: id, Channel: channel, Author: userID, Text: text, PostAt: postAt.UTC(), CreatedAt: now}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := m.Store.CreateScheduledMessage(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "message.scheduled", Payload: string(id), CreatedAt: now}); err != nil {
		return domain.ScheduledMessage{}, err
	}
	return value, nil
}

func (m Messages) ScheduledMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	if channel != "" {
		if err := m.authorizeConversation(ctx, workspaceID, userID, channel); err != nil {
			return domain.ScheduledMessagePage{}, err
		}
	}
	return m.Store.ListScheduledMessages(ctx, workspaceID, userID, channel, request)
}

func (m Messages) DeleteScheduledMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if err := m.authorizeConversation(ctx, workspaceID, userID, channel); err != nil {
		return err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.DeleteScheduledMessage(ctx, workspaceID, userID, channel, id, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "message.schedule_deleted", Payload: string(id), CreatedAt: time.Now().UTC()})
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

func (m Messages) CreateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, name, handle, description string) (domain.UserGroup, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.CreateUserGroup(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "usergroup.created", Payload: string(id), CreatedAt: now}); err != nil {
		return domain.UserGroup{}, err
	}
	return value, nil
}

func (m Messages) UpdateUserGroup(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, name, handle, description string) (domain.UserGroup, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.UpdateUserGroup(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "usergroup.updated", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		return domain.UserGroup{}, err
	}
	return value, nil
}

func (m Messages) SetUserGroupEnabled(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, id domain.UserGroupID, enabled bool) (domain.UserGroup, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.UserGroup{}, err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.SetUserGroupEnabled(ctx, workspaceID, id, enabled, actor, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "usergroup.enabled_changed", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
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
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.UserGroup{}, err
	}
	if err := m.Store.SetUserGroupUsers(ctx, workspaceID, id, normalized, actor, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "usergroup.users_changed", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Call{}, err
	}
	if err := m.Store.CreateCall(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "call.created", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Call{}, err
	}
	if err := m.Store.UpdateCall(ctx, value, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "call.updated", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.EndCall(ctx, workspaceID, id, duration, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "call.ended", Payload: string(id), CreatedAt: time.Now().UTC()})
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
	eventID, err := domain.NewEventID()
	if err != nil {
		return err
	}
	return m.Store.SetCallParticipants(ctx, workspaceID, id, result, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "call.participants_changed", Payload: string(id), CreatedAt: time.Now().UTC()})
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
	if err := m.authorizeConversation(ctx, workspaceID, authorID, conversation); err != nil {
		return domain.EphemeralMessage{}, err
	}
	text = strings.TrimSpace(text)
	if conversation == "" || recipientID == "" || text == "" || len(text) > 40000 {
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
	now := time.Now().UTC()
	value := domain.EphemeralMessage{WorkspaceID: workspaceID, Conversation: conversation, AuthorID: authorID, RecipientID: recipientID, Text: text, Timestamp: domain.NewMessageTimestamp(now)}
	payload, err := json.Marshal(map[string]string{"workspace_id": string(value.WorkspaceID), "channel_id": string(value.Conversation), "author_id": string(value.AuthorID), "user_id": string(value.RecipientID), "text": value.Text, "ts": string(value.Timestamp)})
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if err := m.Store.AppendEvent(ctx, events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: events.EphemeralMessageTopic, Payload: string(payload), CreatedAt: now}); err != nil {
		return domain.EphemeralMessage{}, err
	}
	return value, nil
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
	if idempotencyKey != "" {
		cached, err := m.Store.GetIdempotentMessage(ctx, workspaceID, authorID, idempotencyKey)
		if err == nil {
			return cached, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return domain.Message{}, err
		}
	}
	if strings.TrimSpace(string(conversation)) == "" || strings.TrimSpace(text) == "" {
		return domain.Message{}, ErrInvalidMessage
	}
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return domain.Message{}, err
	}
	if err := m.authorizeConversation(ctx, workspaceID, authorID, conversation); err != nil {
		return domain.Message{}, err
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
	message := domain.Message{ID: id, WorkspaceID: workspaceID, Conversation: conversation, AuthorID: authorID, Text: text, ThreadTimestamp: threadTimestampValue, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	eventID, err := domain.NewEventID()
	if err != nil {
		return domain.Message{}, err
	}
	event := events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: "message.created", Payload: string(message.ID), CreatedAt: message.CreatedAt}
	if err := m.Store.CreateMessage(ctx, message, event, idempotencyKey); err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			return m.Store.GetIdempotentMessage(ctx, workspaceID, authorID, idempotencyKey)
		}
		return domain.Message{}, err
	}
	return message, nil
}

func (m Messages) Unfurl(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, unfurls map[string]string) (domain.Message, error) {
	message, err := m.messageForMutation(ctx, workspaceID, userID, conversation, timestamp)
	if err != nil {
		return domain.Message{}, err
	}
	if message.Deleted {
		return domain.Message{}, ErrMessageAlreadyDeleted
	}
	normalized, err := domain.NormalizeUnfurls(unfurls)
	if err != nil {
		return domain.Message{}, ErrInvalidMessage
	}
	message.Unfurls = normalized
	event, err := mutationEvent(workspaceID, "message.unfurled", message.ID)
	if err != nil {
		return domain.Message{}, err
	}
	if err := m.Store.UpdateMessage(ctx, message, event); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (m Messages) Update(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, text string) (domain.Message, error) {
	if strings.TrimSpace(text) == "" {
		return domain.Message{}, ErrInvalidMessage
	}
	message, err := m.messageForMutation(ctx, workspaceID, userID, conversation, timestamp)
	if err != nil {
		return domain.Message{}, err
	}
	if message.Deleted {
		return domain.Message{}, ErrMessageAlreadyDeleted
	}
	message.Text = text
	event, err := mutationEvent(workspaceID, "message.changed", message.ID)
	if err != nil {
		return domain.Message{}, err
	}
	if err := m.Store.UpdateMessage(ctx, message, event); err != nil {
		return domain.Message{}, err
	}
	return message, nil
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
	event, err := mutationEvent(workspaceID, "message.deleted", message.ID)
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

func mutationEvent(workspaceID domain.WorkspaceID, topic string, messageID domain.MessageID) (events.Event, error) {
	eventID, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	now := time.Now().UTC()
	return events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: topic, Payload: string(messageID), CreatedAt: now}, nil
}

func conversationEvent(workspaceID domain.WorkspaceID, topic string, conversationID domain.ConversationID) (events.Event, error) {
	eventID, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	now := time.Now().UTC()
	return events.Event{ID: eventID, WorkspaceID: workspaceID, Topic: topic, Payload: string(conversationID), CreatedAt: now}, nil
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
	}
	return nil
}

func (m Messages) authorizeWorkspace(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if _, err := m.Store.GetWorkspace(ctx, workspaceID); err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil || user.WorkspaceID != workspaceID || user.Deleted {
		return store.ErrNotFound
	}
	membership, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, userID)
	if err != nil || !membership.Active {
		return store.ErrNotFound
	}
	return nil
}
