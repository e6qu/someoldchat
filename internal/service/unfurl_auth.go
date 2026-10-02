package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// PromptUnfurlAuthentication sends the member who shared a message's links an
// app's invitation to connect their account, chat.unfurl's user_auth_*
// arguments. It is ephemeral, shown to that member alone where the message is,
// and carries Slack's two buttons; a member who chose "Never ask me again" for
// this app is not asked again. The caller is the app, so the authority is the
// one chat.unfurl itself needs.
func (m Messages) PromptUnfurlAuthentication(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, conversation domain.ConversationID, timestamp domain.MessageTimestamp, prompt domain.UnfurlAuthPrompt) error {
	if appID == "" || strings.TrimSpace(string(conversation)) == "" {
		return domain.ErrCannotUnfurlURL
	}
	if _, err := m.unfurlAuthority(ctx, workspaceID, userID, appID, conversation); err != nil {
		return err
	}
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversation, timestamp)
	if err != nil {
		return err
	}
	if message.Deleted {
		return domain.ErrMessageAlreadyDeleted
	}
	// The app's own message has no member to invite.
	if message.AuthorID == userID || message.AppID == appID {
		return nil
	}
	declined, err := m.Store.UnfurlAuthDeclined(ctx, workspaceID, message.AuthorID, appID)
	if err != nil || declined {
		return err
	}
	member, err := m.Store.IsConversationMember(ctx, conversation, message.AuthorID)
	if err != nil || !member {
		return err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	blocks, text, err := unfurlAuthPromptBlocks(app.Name, prompt)
	if err != nil {
		return err
	}
	id, err := domain.NewMessageID()
	if err != nil {
		return err
	}
	now := domain.MessageInstant(time.Now().UTC())
	return m.createEphemeralMessage(ctx, domain.EphemeralMessage{
		ID: id, WorkspaceID: workspaceID, Conversation: conversation, AuthorID: userID, AppID: appID,
		RecipientID: message.AuthorID, Text: text, Blocks: blocks,
		Timestamp: domain.NewMessageTimestamp(now), ThreadTimestamp: message.ThreadTimestamp, CreatedAt: now,
	})
}

// unfurlAuthPromptBlocks is the prompt's content: the app's blocks, or its
// message (with the authentication link when one is named), and then Slack's
// two buttons.
func unfurlAuthPromptBlocks(appName string, prompt domain.UnfurlAuthPrompt) (string, string, error) {
	var blocks []any
	text := strings.TrimSpace(prompt.Message)
	if text == "" {
		text = "*" + appName + "* can show previews of your links once you connect your account."
	}
	if strings.TrimSpace(prompt.Blocks) != "" {
		if err := json.Unmarshal([]byte(prompt.Blocks), &blocks); err != nil {
			return "", "", domain.ErrInvalidMessage
		}
	} else {
		body := text
		if url := strings.TrimSpace(prompt.URL); url != "" {
			body += "\n<" + url + "|Connect your account>"
		}
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": body}})
	}
	button := func(label, actionID string) map[string]any {
		return map[string]any{"type": "button", "action_id": actionID, "text": map[string]any{"type": "plain_text", "text": label}}
	}
	blocks = append(blocks, map[string]any{"type": "actions", "block_id": domain.UnfurlAuthBlockID, "elements": []any{
		button("Not now", domain.UnfurlAuthNotNowAction), button("Never ask me again", domain.UnfurlAuthNeverAskAction),
	}})
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return "", "", err
	}
	normalized, err := domain.NormalizeBlocks(encoded)
	if err != nil {
		return "", "", domain.ErrInvalidMessage
	}
	return normalized, text, nil
}

// answerUnfurlAuthPrompt handles the two buttons Slack owns on an unfurl
// authentication prompt. Both dismiss the prompt; "Never ask me again" also
// records that this app may not ask the member again. It reports whether the
// action was one of them.
func (m Messages) answerUnfurlAuthPrompt(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, message domain.Message, action domain.AppBlockAction) (bool, error) {
	if action.BlockID != domain.UnfurlAuthBlockID || (action.ActionID != domain.UnfurlAuthNotNowAction && action.ActionID != domain.UnfurlAuthNeverAskAction) {
		return false, nil
	}
	prompt, err := m.Store.GetEphemeralMessage(ctx, workspaceID, userID, message.ID)
	if err != nil {
		return true, store.ErrNotFound
	}
	if action.ActionID == domain.UnfurlAuthNeverAskAction {
		if err := m.Store.DeclineUnfurlAuth(ctx, workspaceID, userID, prompt.AppID, time.Now().UTC()); err != nil {
			return true, err
		}
	}
	event, err := ephemeralMessageMutationEvent(prompt, true)
	if err != nil {
		return true, err
	}
	return true, m.Store.DeleteEphemeralMessage(ctx, workspaceID, userID, prompt.ID, event)
}
