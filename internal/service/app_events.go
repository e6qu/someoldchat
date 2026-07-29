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
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// AppEventProjectionStore is the minimum state required to filter every
// channel-scoped record by installed-bot membership and to turn
// identifier-only journal records into Slack event bodies. Keeping content out
// of the workspace journal avoids leaking private messages to unrelated users
// and apps; projecting it here avoids shipping the content-free fake events
// that forced message callbacks to be withheld entirely.
type AppEventProjectionStore interface {
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	GetFile(context.Context, domain.FileID) (domain.File, error)
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
}

type UserEventProjectionStore interface {
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
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
	if record.Event.Topic != "message.created" {
		visible, err := userCanSeeChannelEvent(ctx, state, userID, record.Event)
		return record, visible, err
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
	return projectMessageEvent(ctx, state, record, message)
}

// PrepareAppEvent returns a Slack-shaped copy of a content-bearing app event
// and whether this app may receive it. Identifier-only records for other topics
// pass through unchanged to the central events translator.
func PrepareAppEvent(ctx context.Context, state AppEventProjectionStore, appID domain.AppID, record events.Record) (events.Record, bool, error) {
	if state == nil || appID == "" {
		return events.Record{}, false, store.InvalidArgument("app event projection requires a store and app")
	}
	if events.RecipientScoped(record.Event.Topic) {
		return record, false, nil
	}
	switch record.Event.Topic {
	case "message.created":
		return prepareAppMessageEvent(ctx, state, appID, record)
	case "file.created":
		return prepareAppFileEvent(ctx, state, appID, record)
	default:
		visible, err := appCanSeeChannelEvent(ctx, state, appID, record.Event)
		return record, visible, err
	}
}

func userCanSeeChannelEvent(ctx context.Context, state UserEventProjectionStore, userID domain.UserID, event events.Event) (bool, error) {
	channelID, scoped := eventChannelID(event)
	if !scoped {
		return true, nil
	}
	member, err := state.IsConversationMember(ctx, channelID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return member, err
}

func appCanSeeChannelEvent(ctx context.Context, state AppEventProjectionStore, appID domain.AppID, event events.Event) (bool, error) {
	channelID, scoped := eventChannelID(event)
	if !scoped {
		return true, nil
	}
	return appBotCanSeeConversation(ctx, state, appID, event.WorkspaceID, channelID)
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

func prepareAppMessageEvent(ctx context.Context, state AppEventProjectionStore, appID domain.AppID, record events.Record) (events.Record, bool, error) {
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
	visible, err := appBotCanSeeConversation(ctx, state, appID, message.WorkspaceID, message.Conversation)
	if err != nil || !visible {
		return record, visible, err
	}
	return projectMessageEvent(ctx, state, record, message)
}

type messageEventProjectionStore interface {
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
}

func projectMessageEvent(ctx context.Context, state any, record events.Record, message domain.Message) (events.Record, bool, error) {
	timestamp := string(domain.NewMessageTimestamp(message.CreatedAt))
	body := map[string]any{
		"type": "message", "channel": message.Conversation, "user": message.AuthorID,
		"text": message.Text, "ts": timestamp, "event_ts": timestamp,
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
				return events.Record{}, false, botErr
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
	encoded, err := json.Marshal(body)
	if err != nil {
		return events.Record{}, false, err
	}
	record.Event.Payload = string(encoded)
	return record, true, nil
}

func prepareAppFileEvent(ctx context.Context, state AppEventProjectionStore, appID domain.AppID, record events.Record) (events.Record, bool, error) {
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
	bot, err := state.GetBotByApp(ctx, file.WorkspaceID, appID)
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return events.Record{}, false, err
	}
	visible := file.Uploader == bot.UserID
	for _, conversationID := range file.SharedChannels {
		member, memberErr := state.IsConversationMember(ctx, conversationID, bot.UserID)
		if memberErr != nil && !errors.Is(memberErr, store.ErrNotFound) {
			return events.Record{}, false, memberErr
		}
		visible = visible || member
	}
	if !visible {
		return record, false, nil
	}
	body := map[string]any{"type": "file_created", "file": appEventFile(file), "event_ts": string(domain.NewMessageTimestamp(record.Event.CreatedAt))}
	encoded, err := json.Marshal(body)
	if err != nil {
		return events.Record{}, false, err
	}
	record.Event.Payload = string(encoded)
	return record, true, nil
}

func appBotCanSeeConversation(ctx context.Context, state AppEventProjectionStore, appID domain.AppID, workspaceID domain.WorkspaceID, conversationID domain.ConversationID) (bool, error) {
	bot, err := state.GetBotByApp(ctx, workspaceID, appID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	member, err := state.IsConversationMember(ctx, conversationID, bot.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return member, err
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
