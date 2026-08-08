package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const MaxStreamMarkdownRunes = 12_000

var (
	ErrInvalidMessageStream       = errors.New("message stream arguments are invalid")
	ErrInvalidStreamChunks        = errors.New("message stream chunks are invalid")
	ErrMessageNotStreaming        = errors.New("message is not in streaming state")
	ErrMessageNotOwnedByApp       = errors.New("message is not owned by app")
	ErrMissingStreamRecipientTeam = errors.New("message stream recipient team is required")
	ErrMissingStreamRecipientUser = errors.New("message stream recipient user is required")
)

func (m Messages) StartMessageStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamStart) (domain.Message, error) {
	if request.AppID == "" || request.Conversation == "" || request.ThreadTimestamp == "" {
		return domain.Message{}, ErrInvalidMessageStream
	}
	mode := strings.TrimSpace(request.TaskDisplayMode)
	if mode == "" {
		mode = "timeline"
	}
	username := strings.TrimSpace(request.Username)
	iconEmoji := strings.TrimSpace(request.IconEmoji)
	iconURL := strings.TrimSpace(request.IconURL)
	if !oneOf(mode, "timeline", "plan", "dense") ||
		utf8.RuneCountInString(username) > 80 || utf8.RuneCountInString(iconEmoji) > 255 ||
		(iconURL != "" && !validMessageIconURL(iconURL)) {
		return domain.Message{}, ErrInvalidMessageStream
	}
	if iconEmoji != "" {
		iconURL = ""
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, request.Conversation); err != nil {
		return domain.Message{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, request.Conversation)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Message{}, store.ErrNotFound
	}
	if !conversation.IsDirectOrGroup() {
		if request.RecipientTeamID == "" {
			return domain.Message{}, ErrMissingStreamRecipientTeam
		}
		if request.RecipientUserID == "" {
			return domain.Message{}, ErrMissingStreamRecipientUser
		}
		if request.RecipientTeamID != workspaceID {
			return domain.Message{}, store.ErrNotFound
		}
		recipient, err := m.Store.GetUser(ctx, request.RecipientUserID)
		if err != nil || recipient.WorkspaceID != workspaceID || recipient.Deleted {
			return domain.Message{}, store.ErrNotFound
		}
		member, err := m.Store.IsConversationMember(ctx, request.Conversation, request.RecipientUserID)
		if err != nil || !member {
			return domain.Message{}, store.ErrNotFound
		}
	}
	threadAt, err := domain.ParseMessageTimestamp(request.ThreadTimestamp)
	if err != nil {
		return domain.Message{}, ErrInvalidTimestamp
	}
	parent, err := m.Store.GetMessageByCreatedAt(ctx, request.Conversation, threadAt)
	if err != nil || parent.WorkspaceID != workspaceID || parent.Deleted {
		return domain.Message{}, store.ErrNotFound
	}
	state := domain.MessageStreamState{
		Active: true, TaskDisplayMode: mode, BotID: request.BotID,
		Username: username, IconEmoji: iconEmoji, IconURL: iconURL,
	}
	text := ""
	if request.MarkdownText != "" || strings.TrimSpace(request.Chunks) != "" {
		text, err = applyMessageStreamContent(&state, "", request.MarkdownText, request.Chunks)
		if err != nil {
			return domain.Message{}, err
		}
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		return domain.Message{}, err
	}
	id, err := domain.NewMessageID()
	if err != nil {
		return domain.Message{}, err
	}
	message := domain.Message{
		ID: id, WorkspaceID: workspaceID, Conversation: request.Conversation,
		AuthorID: userID, AppID: request.AppID, Text: text, StreamState: string(encodedState),
		ThreadTimestamp: request.ThreadTimestamp, CreatedAt: domain.MessageInstant(time.Now()),
	}
	for {
		event, err := messageEventAt(workspaceID, "message.created", message, nil, message.CreatedAt)
		if err != nil {
			return domain.Message{}, err
		}
		err = m.Store.CreateMessage(ctx, message, event, "")
		if errors.Is(err, store.ErrMessageTimestampTaken) {
			message.CreatedAt = message.CreatedAt.Add(time.Microsecond)
			continue
		}
		if err != nil {
			return domain.Message{}, err
		}
		return message, nil
	}
}

func (m Messages) AppendMessageStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamMutation) (domain.Message, error) {
	message, state, err := m.messageStreamForMutation(ctx, workspaceID, userID, request)
	if err != nil {
		return domain.Message{}, err
	}
	message.Text, err = applyMessageStreamContent(&state, message.Text, request.MarkdownText, request.Chunks)
	if err != nil {
		return domain.Message{}, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return domain.Message{}, err
	}
	message.StreamState = string(encoded)
	event, err := messageEvent(workspaceID, "message.changed", message)
	if err != nil {
		return domain.Message{}, err
	}
	if err := m.Store.UpdateMessage(ctx, message, event); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (m Messages) StopMessageStream(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamMutation) (domain.Message, error) {
	message, state, err := m.messageStreamForMutation(ctx, workspaceID, userID, request)
	if err != nil {
		return domain.Message{}, err
	}
	message.Text, err = applyMessageStreamContent(&state, message.Text, request.MarkdownText, request.Chunks)
	if err != nil {
		return domain.Message{}, err
	}
	if strings.TrimSpace(request.Blocks) != "" {
		message.Blocks, err = domain.NormalizeBlocks([]byte(request.Blocks))
		if err != nil {
			return domain.Message{}, ErrInvalidMessageStream
		}
	}
	if strings.TrimSpace(request.Metadata) != "" {
		message.Metadata, err = normalizeMessageMetadata(request.Metadata)
		if err != nil {
			return domain.Message{}, err
		}
	}
	state.Active = false
	encoded, err := json.Marshal(state)
	if err != nil {
		return domain.Message{}, err
	}
	message.StreamState = string(encoded)
	if messagePayloadTooLong(message.Blocks, message.Attachments) || messageTextTooLong(message.Text) {
		return domain.Message{}, ErrInvalidMessageStream
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

func (m Messages) messageStreamForMutation(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.MessageStreamMutation) (domain.Message, domain.MessageStreamState, error) {
	if request.AppID == "" || request.Conversation == "" || request.Timestamp == "" {
		return domain.Message{}, domain.MessageStreamState{}, ErrInvalidMessageStream
	}
	if err := m.authorizeConversation(ctx, workspaceID, userID, request.Conversation); err != nil {
		return domain.Message{}, domain.MessageStreamState{}, err
	}
	createdAt, err := domain.ParseMessageTimestamp(request.Timestamp)
	if err != nil {
		return domain.Message{}, domain.MessageStreamState{}, ErrInvalidTimestamp
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, request.Conversation, createdAt)
	if err != nil || message.WorkspaceID != workspaceID {
		return domain.Message{}, domain.MessageStreamState{}, store.ErrNotFound
	}
	if message.AppID != request.AppID {
		return domain.Message{}, domain.MessageStreamState{}, ErrMessageNotOwnedByApp
	}
	var state domain.MessageStreamState
	if json.Unmarshal([]byte(message.StreamState), &state) != nil || !state.Active {
		return domain.Message{}, domain.MessageStreamState{}, ErrMessageNotStreaming
	}
	return message, state, nil
}

func applyMessageStreamContent(state *domain.MessageStreamState, existingText, markdownText, rawChunks string) (string, error) {
	if utf8.RuneCountInString(markdownText) > MaxStreamMarkdownRunes {
		return "", ErrInvalidMessageStream
	}
	text := existingText + markdownText
	if strings.TrimSpace(rawChunks) == "" {
		if markdownText == "" {
			return "", ErrInvalidMessageStream
		}
		if messageTextTooLong(text) {
			return "", ErrInvalidMessageStream
		}
		return text, nil
	}
	var chunks []map[string]any
	if err := json.Unmarshal([]byte(rawChunks), &chunks); err != nil || chunks == nil || len(chunks) == 0 {
		return "", ErrInvalidStreamChunks
	}
	taskIndex := make(map[string]int, len(state.Tasks))
	for index, raw := range state.Tasks {
		var task map[string]any
		if json.Unmarshal(raw, &task) == nil {
			taskIndex[strings.TrimSpace(stringMapValue(task, "id"))] = index
		}
	}
	for _, chunk := range chunks {
		switch strings.TrimSpace(stringMapValue(chunk, "type")) {
		case "markdown_text":
			value := stringMapValue(chunk, "text")
			if value == "" || utf8.RuneCountInString(value) > MaxStreamMarkdownRunes {
				return "", ErrInvalidStreamChunks
			}
			text += value
		case "plan_update":
			title := strings.TrimSpace(stringMapValue(chunk, "title"))
			if title == "" || utf8.RuneCountInString(title) > 256 {
				return "", ErrInvalidStreamChunks
			}
			state.PlanTitle = title
		case "task_update":
			id := strings.TrimSpace(stringMapValue(chunk, "id"))
			title := strings.TrimSpace(stringMapValue(chunk, "title"))
			status := strings.TrimSpace(stringMapValue(chunk, "status"))
			if id == "" || title == "" || !oneOf(status, "pending", "in_progress", "complete", "error") ||
				utf8.RuneCountInString(id) > 256 || utf8.RuneCountInString(title) > 256 ||
				utf8.RuneCountInString(stringMapValue(chunk, "details")) > 256 ||
				utf8.RuneCountInString(stringMapValue(chunk, "output")) > 256 {
				return "", ErrInvalidStreamChunks
			}
			encoded, err := json.Marshal(chunk)
			if err != nil {
				return "", ErrInvalidStreamChunks
			}
			if index, exists := taskIndex[id]; exists {
				state.Tasks[index] = encoded
			} else {
				taskIndex[id] = len(state.Tasks)
				state.Tasks = append(state.Tasks, encoded)
			}
		case "blocks":
			encoded, err := json.Marshal(chunk["blocks"])
			if err != nil {
				return "", ErrInvalidStreamChunks
			}
			normalized, err := domain.NormalizeBlocks(encoded)
			if err != nil {
				return "", ErrInvalidStreamChunks
			}
			var blocks []json.RawMessage
			if json.Unmarshal([]byte(normalized), &blocks) != nil {
				return "", ErrInvalidStreamChunks
			}
			remaining := 50 - len(state.ChunkBlocks)
			if remaining < 0 {
				remaining = 0
			}
			if len(blocks) > remaining {
				blocks = blocks[:remaining]
				appendStreamWarning(state, "too_many_blocks")
			}
			state.ChunkBlocks = append(state.ChunkBlocks, blocks...)
		default:
			return "", ErrInvalidStreamChunks
		}
	}
	if messageTextTooLong(text) {
		return "", ErrInvalidMessageStream
	}
	return text, nil
}

func appendStreamWarning(state *domain.MessageStreamState, warning string) {
	for _, existing := range state.Warnings {
		if existing == warning {
			return
		}
	}
	state.Warnings = append(state.Warnings, warning)
}

func validMessageIconURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func normalizeMessageMetadata(raw string) (string, error) {
	if len(raw) > MaxMessageBodyBytes {
		return "", ErrInvalidMessageStream
	}
	var value struct {
		EventType    string         `json:"event_type"`
		EventPayload map[string]any `json:"event_payload"`
	}
	if json.Unmarshal([]byte(raw), &value) != nil || strings.TrimSpace(value.EventType) == "" ||
		value.EventPayload == nil || utf8.RuneCountInString(value.EventType) > 255 {
		return "", ErrInvalidMessageStream
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidMessageStream
	}
	return string(encoded), nil
}

func stringMapValue(value map[string]any, field string) string {
	result, _ := value[field].(string)
	return result
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}
