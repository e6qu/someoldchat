package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// AppEventProjectionStore is the minimum state required to filter every
// channel-scoped record by each installed bot/user authorization and to turn
// identifier-only journal records into Slack event bodies. Keeping content out
// of the workspace journal avoids leaking private messages to unrelated users
// and apps; projecting it here avoids shipping the content-free fake events
// that forced message callbacks to be withheld entirely.
type AppEventProjectionStore interface {
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	GetFile(context.Context, domain.FileID) (domain.File, error)
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
	GetConversation(context.Context, domain.ConversationID) (domain.Conversation, error)
	ListAppAuthorizations(context.Context, domain.AppID, domain.WorkspaceID) ([]domain.AppAuthorization, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
	// GetAppBotTokenCiphertext returns the sealed bot access token a
	// function_executed dispatch includes for the receiving app.
	GetAppBotTokenCiphertext(context.Context, domain.AppID, domain.WorkspaceID) (string, error)
}

type UserEventProjectionStore interface {
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
}

// messageEventSnapshot freezes the state an app callback describes. App
// delivery is intentionally asynchronous: re-reading the message when a worker
// eventually handles message.created would otherwise publish a later edit, and
// a message.changed/message.deleted callback could never recover the required
// previous_message after the repository committed the mutation.
//
// The snapshot lives in events.Event.PrivatePayload. It therefore persists and
// crosses the internal gRPC seam, but Event's JSON encoder excludes it from
// browser SSE and generic record delivery.
type messageEventSnapshot struct {
	Current  domain.Message  `json:"current"`
	Previous *domain.Message `json:"previous,omitempty"`
}

type fileEventSnapshot struct {
	File      domain.File           `json:"file"`
	ChannelID domain.ConversationID `json:"channel_id,omitempty"`
	UserID    domain.UserID         `json:"user_id,omitempty"`
}

func encodeFileEventSnapshot(file domain.File, channelID domain.ConversationID, userID domain.UserID) (string, error) {
	file.BlobKey = ""
	file.PublicToken = ""
	file.SharedChannels = append([]domain.ConversationID(nil), file.SharedChannels...)
	encoded, err := json.Marshal(fileEventSnapshot{File: file, ChannelID: channelID, UserID: userID})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeFileEventSnapshot(event events.Event) (fileEventSnapshot, bool, error) {
	if strings.TrimSpace(event.PrivatePayload) == "" {
		return fileEventSnapshot{}, false, nil
	}
	var value fileEventSnapshot
	if err := json.Unmarshal([]byte(event.PrivatePayload), &value); err != nil {
		return fileEventSnapshot{}, false, err
	}
	if value.File.ID == "" || value.File.WorkspaceID == "" || value.File.CreatedAt.IsZero() {
		return fileEventSnapshot{}, false, events.ErrPayloadFieldInvalid
	}
	return value, true, nil
}

func encodeMessageEventSnapshot(current domain.Message, previous *domain.Message) (string, error) {
	current = eventSafeMessage(current)
	value := messageEventSnapshot{Current: current}
	if previous != nil {
		copy := eventSafeMessage(*previous)
		value.Previous = &copy
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeMessageEventSnapshot(event events.Event) (messageEventSnapshot, bool, error) {
	if strings.TrimSpace(event.PrivatePayload) == "" {
		return messageEventSnapshot{}, false, nil
	}
	var value messageEventSnapshot
	if err := json.Unmarshal([]byte(event.PrivatePayload), &value); err != nil {
		return messageEventSnapshot{}, false, err
	}
	if value.Current.ID == "" || value.Current.WorkspaceID == "" || value.Current.Conversation == "" || value.Current.CreatedAt.IsZero() {
		return messageEventSnapshot{}, false, events.ErrPayloadFieldInvalid
	}
	return value, true, nil
}

func eventSafeMessage(value domain.Message) domain.Message {
	value.Unfurls = cloneStringMap(value.Unfurls)
	value.Files = append([]domain.File(nil), value.Files...)
	for index := range value.Files {
		// Object-storage keys and public bearer URLs are not part of a Slack
		// event and must not become delivery-worker diagnostics.
		value.Files[index].BlobKey = ""
		value.Files[index].PublicToken = ""
		value.Files[index].SharedChannels = append([]domain.ConversationID(nil), value.Files[index].SharedChannels...)
	}
	return value
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	copy := make(map[string]string, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}

// PrepareUserEvent hydrates content-bearing RTM records only after proving the
// connected user belongs to the conversation. The journal itself deliberately
// carries identifiers, not private message text.
func PrepareUserEvent(ctx context.Context, state UserEventProjectionStore, workspaceID domain.WorkspaceID, userID domain.UserID, record events.Record) (events.Record, bool, error) {
	if state == nil || workspaceID == "" || userID == "" {
		return events.Record{}, false, store.InvalidArgument("user event projection requires a store, workspace, and user")
	}
	if record.Event.WorkspaceID != workspaceID {
		return record, false, nil
	}
	if record.Event.Topic != "message.created" && record.Event.Topic != "message.changed" && record.Event.Topic != "message.deleted" {
		visible, err := userCanSeeChannelEvent(ctx, state, userID, record.Event)
		return record, visible, err
	}
	snapshot, snapshotted, err := decodeMessageEventSnapshot(record.Event)
	if err != nil {
		return events.Record{}, false, err
	}
	if snapshotted {
		if snapshot.Current.WorkspaceID != workspaceID {
			return record, false, nil
		}
		member, err := state.IsConversationMember(ctx, snapshot.Current.Conversation, userID)
		if errors.Is(err, store.ErrNotFound) {
			return record, false, nil
		}
		if err != nil || !member {
			return record, member, err
		}
		return projectMessageSnapshot(ctx, state, record, snapshot, "")
	}
	delivered, err := events.Deliverable(record.Event)
	if err != nil {
		return record, true, nil
	}
	messageID, err := deliveredString(delivered, "message_id")
	if err != nil {
		return record, true, nil
	}
	message, err := state.GetMessage(ctx, domain.MessageID(messageID))
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return events.Record{}, false, err
	}
	if message.WorkspaceID != workspaceID {
		return record, false, nil
	}
	member, err := state.IsConversationMember(ctx, message.Conversation, userID)
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil || !member {
		return record, member, err
	}
	return projectMessageEvent(ctx, state, record, message, "")
}

// PrepareAppEvent returns a Slack-shaped copy of a content-bearing app event
// and whether this app may receive it. Identifier-only records for other topics
// pass through unchanged to the central events translator. The credential key
// opens the sealed bot access token a function_executed dispatch includes, so
// the token is decrypted only at delivery time, never persisted in plaintext.
func PrepareAppEvent(ctx context.Context, state AppEventProjectionStore, credentialKey []byte, appID domain.AppID, record events.Record) (events.Record, bool, error) {
	if state == nil || appID == "" {
		return events.Record{}, false, store.InvalidArgument("app event projection requires a store and app")
	}
	authorizations, err := state.ListAppAuthorizations(ctx, appID, record.Event.WorkspaceID)
	if err != nil {
		return events.Record{}, false, err
	}
	authorizations, err = scopedAppAuthorizations(ctx, state, record.Event, authorizations)
	if err != nil {
		return events.Record{}, false, err
	}
	if events.RecipientScoped(record.Event.Topic) {
		delivered, err := events.Deliverable(record.Event)
		if err != nil {
			return events.Record{}, false, err
		}
		recipient, err := deliveredString(delivered, "user_id")
		if err != nil {
			return events.Record{}, false, err
		}
		authorizations = filterAppAuthorizations(authorizations, func(value domain.AppAuthorization) bool {
			return value.TokenType == "user" && value.UserID == domain.UserID(recipient)
		})
		if channelID, scoped := eventChannelID(record.Event); scoped {
			authorizations, err = visibleAppAuthorizations(ctx, state, authorizations, channelID)
			if err != nil {
				return events.Record{}, false, err
			}
		}
		return withEventAuthorizations(record, authorizations)
	}
	switch record.Event.Topic {
	case "app.uninstalled":
		// The uninstall revoked every authorization the app had, and Slack
		// still delivers the announcement: zero authorizations are the fact
		// the event reports, not a reason to withhold it. Target routing in
		// the payload keeps it addressed to the uninstalled app alone.
		return record, true, nil
	case "message.created", "message.changed", "message.deleted":
		return prepareAppMessageEvent(ctx, state, authorizations, record)
	case "file.created", "file.shared", "file.unshared":
		return prepareAppFileEvent(ctx, state, authorizations, record)
	case "function_executed":
		return prepareFunctionExecutedEvent(ctx, state, credentialKey, appID, authorizations, record)
	default:
		if channelID, scoped := eventChannelID(record.Event); scoped && !publicConversationLifecycleEvent(record.Event) {
			authorizations, err = visibleAppAuthorizations(ctx, state, authorizations, channelID)
			if err != nil {
				return events.Record{}, false, err
			}
		}
		return withEventAuthorizations(record, authorizations)
	}
}

// publicConversationLifecycleEvent reports a lifecycle record about a public
// channel. Slack addresses channel_created, channel_rename, channel_archive
// and their kin to the workspace — a freshly created channel has no members
// at all — so the membership filter that guards content-adjacent events must
// not run for them. Private lifecycle records keep the filter: group_rename
// to a non-member would leak the room's existence and name.
func publicConversationLifecycleEvent(event events.Event) bool {
	switch event.Topic {
	case "conversation.created", "conversation.renamed", "conversation.archived", "conversation.unarchived", "conversation.deleted":
	default:
		return false
	}
	delivered, err := events.Deliverable(event)
	if err != nil {
		return false
	}
	isPrivate, ok := delivered.Bool("is_private")
	return ok && !isPrivate
}

func prepareFunctionExecutedEvent(ctx context.Context, state AppEventProjectionStore, credentialKey []byte, appID domain.AppID, authorizations []domain.AppAuthorization, record events.Record) (events.Record, bool, error) {
	var snapshot functionExecutionSnapshot
	if strings.TrimSpace(record.Event.PrivatePayload) == "" ||
		json.Unmarshal([]byte(record.Event.PrivatePayload), &snapshot) != nil ||
		snapshot.AppID == "" || snapshot.FunctionExecutionID == "" || snapshot.WorkflowRunID == "" ||
		len(snapshot.Function) == 0 || len(snapshot.Inputs) == 0 {
		return events.Record{}, false, events.ErrPayloadFieldInvalid
	}
	if snapshot.AppID != appID {
		return record, false, nil
	}
	fields := []events.Field{
		events.String("target_app_id", string(snapshot.AppID)),
		events.JSON("function", string(snapshot.Function)),
		events.JSON("inputs", string(snapshot.Inputs)),
		events.String("function_execution_id", string(snapshot.FunctionExecutionID)),
		events.String("workflow_execution_id", string(snapshot.WorkflowRunID)),
	}
	// Slack sends the receiving app's bot access token with every
	// function_executed callback so the app can call back. The token is sealed
	// at issuance and opened only here, at delivery time; an app that issued no
	// bot token (older hash-only installations) simply omits the field.
	if ciphertext, err := state.GetAppBotTokenCiphertext(ctx, snapshot.AppID, record.Event.WorkspaceID); err == nil && ciphertext != "" {
		if token, err := secretbox.Open(credentialKey, appBotTokenAssociatedData(snapshot.AppID, record.Event.WorkspaceID), ciphertext); err == nil && token != "" {
			fields = append(fields, events.String("bot_access_token", token))
		}
	}
	projected, err := events.New(record.Event.ID, record.Event.WorkspaceID, record.Event.ActorID,
		events.NewPayload("function_executed", fields...), record.Event.CreatedAt)
	if err != nil {
		return events.Record{}, false, err
	}
	projected.Authorizations = record.Event.Authorizations
	record.Event = projected
	return withEventAuthorizations(record, authorizations)
}

// appBotTokenAssociatedData binds an app's sealed bot access token to the
// app/workspace pair it was issued for.
func appBotTokenAssociatedData(appID domain.AppID, workspaceID domain.WorkspaceID) string {
	return "app_bot_token:" + string(appID) + ":" + string(workspaceID)
}

func scopedAppAuthorizations(ctx context.Context, state AppEventProjectionStore, event events.Event, authorizations []domain.AppAuthorization) ([]domain.AppAuthorization, error) {
	required, err := appEventRequiredScopes(ctx, state, event)
	if err != nil {
		return nil, err
	}
	if len(required) == 0 {
		return authorizations, nil
	}
	return filterAppAuthorizations(authorizations, func(value domain.AppAuthorization) bool {
		for _, granted := range value.Scopes {
			for _, wanted := range required {
				if granted == wanted {
					return true
				}
			}
		}
		return false
	}), nil
}

// appEventRequiredScopes is grounded in the pinned Events AsyncAPI
// x-scopes-required entries. Conversation-dependent events select the one
// history/read scope for the actual channel kind rather than accepting a token
// that can read a different class of conversation.
func appEventRequiredScopes(ctx context.Context, state AppEventProjectionStore, event events.Event) ([]string, error) {
	switch {
	case strings.HasPrefix(event.Topic, "file."):
		return []string{"files:read"}, nil
	case strings.HasPrefix(event.Topic, "reaction."):
		return []string{"reactions:read"}, nil
	case strings.HasPrefix(event.Topic, "pin."):
		return []string{"pins:read"}, nil
	case strings.HasPrefix(event.Topic, "star."):
		return []string{"stars:read"}, nil
	case strings.HasPrefix(event.Topic, "emoji."):
		return []string{"emoji:read"}, nil
	case strings.HasPrefix(event.Topic, "usergroup."):
		return []string{"usergroups:read"}, nil
	// The DND check precedes the generic user. prefix: the durable topics are
	// user.dnd_snoozed and friends, and a "dnd." case here matched nothing, so
	// dnd_updated was deliverable on a users:read grant the AsyncAPI does not
	// accept for it.
	case strings.HasPrefix(event.Topic, "user.dnd_"):
		return []string{"dnd:read"}, nil
	case strings.HasPrefix(event.Topic, "user."):
		return []string{"users:read"}, nil
	// team_rename's durable topic is workspace.name_changed; a "team." case
	// here matched nothing, so the event would have required no scope at all.
	case strings.HasPrefix(event.Topic, "workspace."):
		return []string{"team:read"}, nil
	case strings.HasPrefix(event.Topic, "link."):
		return []string{"links:read"}, nil
	}
	channelID, scoped := eventChannelID(event)
	if !scoped {
		return nil, nil
	}
	conversation, err := state.GetConversation(ctx, channelID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(event.Topic, "message."):
		switch {
		case conversation.Kind == domain.ConversationTypeIM:
			return []string{"im:history"}, nil
		case conversation.Kind == domain.ConversationTypeMPIM:
			return []string{"mpim:history"}, nil
		case conversation.PrivateFlag():
			return []string{"groups:history"}, nil
		default:
			return []string{"channels:history"}, nil
		}
	case strings.HasPrefix(event.Topic, "conversation."):
		if conversation.PrivateFlag() || conversation.Kind == domain.ConversationTypeMPIM {
			return []string{"groups:read"}, nil
		}
		return []string{"channels:read"}, nil
	default:
		return nil, nil
	}
}

func userCanSeeChannelEvent(ctx context.Context, state UserEventProjectionStore, userID domain.UserID, event events.Event) (bool, error) {
	channelID, scoped := eventChannelID(event)
	if !scoped {
		return true, nil
	}
	if publicConversationLifecycleEvent(event) {
		return true, nil
	}
	member, err := state.IsConversationMember(ctx, channelID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return member, err
}

// eventChannelID recognizes the common durable channel_id routing field. A
// malformed or legacy payload is left to the established transport policy,
// which logs and consumes it; projection must not turn that diagnostic path
// into a transient storage failure.
func eventChannelID(event events.Event) (domain.ConversationID, bool) {
	delivered, err := events.Deliverable(event)
	if err != nil {
		return "", false
	}
	value, ok := delivered.Field("channel_id")
	value = strings.TrimSpace(value)
	return domain.ConversationID(value), ok && value != ""
}

func prepareAppMessageEvent(ctx context.Context, state AppEventProjectionStore, authorizations []domain.AppAuthorization, record events.Record) (events.Record, bool, error) {
	snapshot, snapshotted, err := decodeMessageEventSnapshot(record.Event)
	if err != nil {
		return events.Record{}, false, err
	}
	if snapshotted {
		if snapshot.Current.WorkspaceID != record.Event.WorkspaceID {
			return record, false, nil
		}
		authorizations, err = visibleAppAuthorizations(ctx, state, authorizations, snapshot.Current.Conversation)
		if err != nil || len(authorizations) == 0 {
			return record, false, err
		}
		record, _, _ = withEventAuthorizations(record, authorizations)
		return projectMessageSnapshot(ctx, state, record, snapshot, appBotUserID(authorizations))
	}
	delivered, err := events.Deliverable(record.Event)
	if err != nil {
		return record, true, nil
	}
	messageID, err := deliveredString(delivered, "message_id")
	if err != nil {
		return record, true, nil
	}
	message, err := state.GetMessage(ctx, domain.MessageID(messageID))
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return events.Record{}, false, err
	}
	if message.WorkspaceID != record.Event.WorkspaceID {
		return record, false, nil
	}
	authorizations, err = visibleAppAuthorizations(ctx, state, authorizations, message.Conversation)
	if err != nil || len(authorizations) == 0 {
		return record, false, err
	}
	record, _, _ = withEventAuthorizations(record, authorizations)
	return projectMessageEvent(ctx, state, record, message, appBotUserID(authorizations))
}

type messageEventProjectionStore interface {
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
}

func projectMessageEvent(ctx context.Context, state any, record events.Record, message domain.Message, botUserID domain.UserID) (events.Record, bool, error) {
	return projectMessageSnapshot(ctx, state, record, messageEventSnapshot{Current: message}, botUserID)
}

// appBotUserID names the receiving app's bot member, when the app has one:
// it is the user the message text must mention for the app_mention companion
// to be derived. Per-user delivery passes none — app_mention is an
// application event.
func appBotUserID(authorizations []domain.AppAuthorization) domain.UserID {
	for _, authorization := range authorizations {
		if authorization.TokenType == "bot" {
			return authorization.UserID
		}
	}
	return ""
}

func projectMessageSnapshot(ctx context.Context, state any, record events.Record, snapshot messageEventSnapshot, botUserID domain.UserID) (events.Record, bool, error) {
	switch record.Event.Topic {
	case "message.changed":
		if snapshot.Previous == nil {
			return events.Record{}, false, events.ErrPayloadFieldInvalid
		}
		current, err := appEventMessage(ctx, state, snapshot.Current)
		if err != nil {
			return events.Record{}, false, err
		}
		previous, err := appEventMessage(ctx, state, *snapshot.Previous)
		if err != nil {
			return events.Record{}, false, err
		}
		delete(current, "event_ts")
		delete(previous, "event_ts")
		// event_ts is when the change was journalled; the `edited` sub-object
		// is what the message itself records. They used to be the same
		// derived value, so a replayed event reported its replay instant as
		// the edit time. appEventMessage now carries `edited` from the
		// message; this only supplies the envelope's own timestamp.
		editedAt := string(domain.NewMessageTimestamp(record.Event.CreatedAt))
		if _, recorded := current["edited"]; !recorded {
			current["edited"] = map[string]any{"user": record.Event.ActorID, "ts": editedAt}
		}
		body := map[string]any{
			"type": "message", "subtype": "message_changed", "hidden": true,
			"channel": snapshot.Current.Conversation, "ts": string(domain.NewMessageTimestamp(snapshot.Current.CreatedAt)),
			"event_ts": editedAt, "message": current, "previous_message": previous,
		}
		return encodeProjectedEvent(record, body)
	case "message.deleted":
		if snapshot.Previous == nil {
			return events.Record{}, false, events.ErrPayloadFieldInvalid
		}
		previous, err := appEventMessage(ctx, state, *snapshot.Previous)
		if err != nil {
			return events.Record{}, false, err
		}
		delete(previous, "event_ts")
		deletedAt := string(domain.NewMessageTimestamp(record.Event.CreatedAt))
		body := map[string]any{
			"type": "message", "subtype": "message_deleted", "hidden": true,
			"channel":    snapshot.Previous.Conversation,
			"deleted_ts": string(domain.NewMessageTimestamp(snapshot.Previous.CreatedAt)),
			"event_ts":   deletedAt, "previous_message": previous,
		}
		return encodeProjectedEvent(record, body)
	default:
		body, err := appEventMessage(ctx, state, snapshot.Current)
		if err != nil {
			return events.Record{}, false, err
		}
		// The marker is routing metadata for the events builder, which strips
		// it and emits the app_mention companion; it never reaches a consumer.
		if botUserID != "" && strings.Contains(snapshot.Current.Text, "<@"+string(botUserID)+">") {
			body["app_mentioned"] = true
		}
		return encodeProjectedEvent(record, body)
	}
}

func appEventMessage(ctx context.Context, state any, message domain.Message) (map[string]any, error) {
	timestamp := string(domain.NewMessageTimestamp(message.CreatedAt))
	body := map[string]any{
		"type": "message", "channel": message.Conversation, "user": message.AuthorID,
		"text": message.Text, "ts": timestamp, "event_ts": timestamp,
	}
	if !message.EditedAt.IsZero() {
		body["edited"] = map[string]any{"user": message.EditedBy, "ts": string(domain.NewMessageTimestamp(message.EditedAt))}
	}
	if message.Subtype != "" {
		body["subtype"] = string(message.Subtype)
	}
	if message.ThreadTimestamp != "" {
		body["thread_ts"] = message.ThreadTimestamp
	}
	if message.AppID != "" {
		body["app_id"] = message.AppID
		if bots, ok := state.(messageEventProjectionStore); ok {
			if bot, botErr := bots.GetBotByApp(ctx, message.WorkspaceID, message.AppID); botErr == nil {
				body["bot_id"] = bot.ID
			} else if !errors.Is(botErr, store.ErrNotFound) {
				return nil, botErr
			}
		}
	}
	if message.Blocks != "" {
		body["blocks"] = json.RawMessage(message.Blocks)
	}
	if message.Attachments != "" && message.Attachments != "[]" {
		body["attachments"] = json.RawMessage(message.Attachments)
	}
	if message.Metadata != "" {
		body["metadata"] = json.RawMessage(message.Metadata)
	}
	if len(message.Files) > 0 {
		files := make([]map[string]any, 0, len(message.Files))
		for _, file := range message.Files {
			files = append(files, appEventFile(file))
		}
		body["subtype"] = "file_share"
		body["upload"] = true
		body["files"] = files
	}
	return body, nil
}

func encodeProjectedEvent(record events.Record, body map[string]any) (events.Record, bool, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return events.Record{}, false, err
	}
	record.Event.Payload = string(encoded)
	return record, true, nil
}

func prepareAppFileEvent(ctx context.Context, state AppEventProjectionStore, authorizations []domain.AppAuthorization, record events.Record) (events.Record, bool, error) {
	snapshot, snapshotted, err := decodeFileEventSnapshot(record.Event)
	if err != nil {
		return events.Record{}, false, err
	}
	if snapshotted {
		if snapshot.File.WorkspaceID != record.Event.WorkspaceID {
			return record, false, nil
		}
		authorizations, err = visibleFileAuthorizations(ctx, state, authorizations, snapshot)
		if err != nil || len(authorizations) == 0 {
			return record, false, err
		}
		record, _, _ = withEventAuthorizations(record, authorizations)
		body := map[string]any{"file_id": snapshot.File.ID, "file": map[string]any{"id": snapshot.File.ID}}
		switch record.Event.Topic {
		case "file.shared":
			body["type"] = "file_shared"
			body["channel_id"] = snapshot.ChannelID
			body["user_id"] = snapshot.UserID
			body["event_ts"] = string(domain.NewMessageTimestamp(record.Event.CreatedAt))
		case "file.unshared":
			body["type"] = "file_unshared"
		default:
			body = map[string]any{"type": "file_created", "file": appEventFile(snapshot.File), "event_ts": string(domain.NewMessageTimestamp(record.Event.CreatedAt))}
		}
		return encodeProjectedEvent(record, body)
	}
	delivered, err := events.Deliverable(record.Event)
	if err != nil {
		return record, true, nil
	}
	fileID, err := deliveredString(delivered, "file_id")
	if err != nil {
		return record, true, nil
	}
	file, err := state.GetFile(ctx, domain.FileID(fileID))
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return events.Record{}, false, err
	}
	if file.WorkspaceID != record.Event.WorkspaceID {
		return record, false, nil
	}
	authorizations, err = visibleFileAuthorizations(ctx, state, authorizations, fileEventSnapshot{File: file})
	if err != nil {
		return events.Record{}, false, err
	}
	if len(authorizations) == 0 {
		return record, false, nil
	}
	record, _, _ = withEventAuthorizations(record, authorizations)
	body := map[string]any{"type": "file_created", "file": appEventFile(file), "event_ts": string(domain.NewMessageTimestamp(record.Event.CreatedAt))}
	encoded, err := json.Marshal(body)
	if err != nil {
		return events.Record{}, false, err
	}
	record.Event.Payload = string(encoded)
	return record, true, nil
}

func visibleFileAuthorizations(ctx context.Context, state AppEventProjectionStore, authorizations []domain.AppAuthorization, snapshot fileEventSnapshot) ([]domain.AppAuthorization, error) {
	channels := snapshot.File.SharedChannels
	if snapshot.ChannelID != "" {
		channels = append(append([]domain.ConversationID(nil), channels...), snapshot.ChannelID)
	}
	result := make([]domain.AppAuthorization, 0, len(authorizations))
	for _, authorization := range authorizations {
		if authorization.UserID == "" {
			continue
		}
		if snapshot.File.Uploader == authorization.UserID {
			result = append(result, authorization)
			continue
		}
		for _, conversationID := range channels {
			member, err := state.IsConversationMember(ctx, conversationID, authorization.UserID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if member {
				result = append(result, authorization)
				break
			}
		}
	}
	return result, nil
}

func visibleAppAuthorizations(ctx context.Context, state AppEventProjectionStore, authorizations []domain.AppAuthorization, conversationID domain.ConversationID) ([]domain.AppAuthorization, error) {
	result := make([]domain.AppAuthorization, 0, len(authorizations))
	for _, authorization := range authorizations {
		if authorization.UserID == "" {
			continue
		}
		member, err := state.IsConversationMember(ctx, conversationID, authorization.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if member {
			result = append(result, authorization)
		}
	}
	return result, nil
}

func filterAppAuthorizations(authorizations []domain.AppAuthorization, keep func(domain.AppAuthorization) bool) []domain.AppAuthorization {
	result := make([]domain.AppAuthorization, 0, len(authorizations))
	for _, authorization := range authorizations {
		if keep(authorization) {
			result = append(result, authorization)
		}
	}
	return result
}

func withEventAuthorizations(record events.Record, authorizations []domain.AppAuthorization) (events.Record, bool, error) {
	if len(authorizations) == 0 {
		return record, false, nil
	}
	record.Event.Authorizations = make([]events.Authorization, 0, len(authorizations))
	for _, authorization := range authorizations {
		record.Event.Authorizations = append(record.Event.Authorizations, events.Authorization{
			TeamID: authorization.WorkspaceID,
			UserID: authorization.UserID,
			IsBot:  authorization.TokenType == "bot",
		})
	}
	return record, true, nil
}

func deliveredString(delivered events.Delivered, name string) (string, error) {
	var value string
	if err := json.Unmarshal(delivered.Object[name], &value); err != nil || strings.TrimSpace(value) == "" {
		return "", events.ErrPayloadFieldInvalid
	}
	return value, nil
}

func appEventFile(file domain.File) map[string]any {
	fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Name)), ".")
	value := map[string]any{
		"id": file.ID, "created": file.CreatedAt.Unix(), "timestamp": file.CreatedAt.Unix(),
		"name": file.Name, "title": file.Title, "mimetype": file.MIMEType,
		"filetype": fileType, "pretty_type": strings.ToUpper(fileType), "user": file.Uploader,
		"editable": false, "size": file.Size, "mode": "hosted", "is_external": false,
		"external_type": "", "is_public": file.PublicToken != "", "public_url_shared": file.PublicToken != "",
		"display_as_bot": false,
	}
	if !file.Deleted {
		value["url_private"] = "/api/files/" + url.PathEscape(string(file.ID))
		value["url_private_download"] = "/api/files/" + url.PathEscape(string(file.ID))
	}
	return value
}
