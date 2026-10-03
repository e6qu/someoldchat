package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// PostAsSlackbot posts a message from Slackbot for a member, as Slack delivers
// a reminder; see domain.SlackbotPost. The member must be an active member of
// the workspace, and of the conversation when one is named.
func (m Messages) PostAsSlackbot(ctx context.Context, workspaceID domain.WorkspaceID, memberID domain.UserID, post domain.SlackbotPost) (domain.Message, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, memberID); err != nil {
		return domain.Message{}, err
	}
	if strings.TrimSpace(post.Text) == "" || messageTextTooLong(post.Text) {
		return domain.Message{}, domain.ErrInvalidMessage
	}
	blocks := ""
	if strings.TrimSpace(post.Blocks) != "" {
		normalized, err := domain.NormalizeBlocks([]byte(post.Blocks))
		if err != nil || normalized == "" {
			return domain.Message{}, domain.ErrInvalidBlocks
		}
		if err := validateMessageBlocks(normalized); err != nil {
			return domain.Message{}, err
		}
		blocks = normalized
	}
	conversation := post.Conversation
	if conversation == "" {
		var err error
		if conversation, err = m.slackbotConversation(ctx, workspaceID, memberID); err != nil {
			return domain.Message{}, err
		}
	} else {
		if err := m.requireConversationMembership(ctx, workspaceID, memberID, conversation); err != nil {
			return domain.Message{}, err
		}
		target, err := m.Store.GetConversation(ctx, conversation)
		if err != nil {
			return domain.Message{}, err
		}
		if target.Archived {
			return domain.Message{}, domain.ErrConversationAlreadyArchived
		}
	}
	id, err := domain.NewMessageID()
	if err != nil {
		return domain.Message{}, err
	}
	return m.createMessage(ctx, domain.Message{
		ID: id, WorkspaceID: workspaceID, Conversation: conversation, AuthorID: domain.SlackbotUserID,
		Text: post.Text, Blocks: blocks, CreatedAt: domain.MessageInstant(time.Now()),
	}, post.IdempotencyKey, "")
}

// slackbotConversation is the member's DM with Slackbot, opened the first time
// Slackbot has something to say.
func (m Messages) slackbotConversation(ctx context.Context, workspaceID domain.WorkspaceID, member domain.UserID) (domain.ConversationID, error) {
	members := []domain.UserID{domain.SlackbotUserID, member}
	sort.Slice(members, func(left, right int) bool { return members[left] < members[right] })
	if existing, err := m.Store.FindDirectConversation(ctx, workspaceID, members); err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	kind := domain.DirectKindFor(len(members))
	id, err := domain.NewDirectConversationID(kind)
	if err != nil {
		return "", err
	}
	conversation := domain.Conversation{ID: id, WorkspaceID: workspaceID, Name: "direct", Kind: kind, Created: conversationInstant(time.Now()), CreatorID: domain.SlackbotUserID}
	event, err := conversationLifecycleEvent(workspaceID, "conversation.direct_created", conversation, domain.SlackbotUserID)
	if err != nil {
		return "", err
	}
	if err := m.Store.CreateDirectConversation(ctx, conversation, members, event); err != nil {
		if !errors.Is(err, store.ErrAlreadyExists) {
			return "", err
		}
		// Another delivery opened it first.
		existing, err := m.Store.FindDirectConversation(ctx, workspaceID, members)
		if err != nil {
			return "", err
		}
		return existing.ID, nil
	}
	return id, nil
}

// answerSlackbotReminder answers a member's choice on a reminder Slackbot
// delivered: Mark as complete completes it, and Remind me about this sets it
// again for the moment chosen. The controls are then replaced by what was
// done, as Slack's message is.
func (m Messages) answerSlackbotReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, message domain.Message, action domain.AppBlockAction) error {
	if !blocksContainDispatchableAction(message.Blocks, action.BlockID, action.ActionID, action.Type) {
		return store.ErrNotFound
	}
	id, found := domain.SlackbotReminderOfBlock(action.BlockID)
	if !found {
		return store.ErrNotFound
	}
	reminder, err := m.ReminderInfo(ctx, workspaceID, userID, id)
	if err != nil {
		return err
	}
	var outcome string
	switch action.ActionID {
	case domain.SlackbotReminderCompleteAction:
		if err := m.CompleteReminder(ctx, workspaceID, userID, id); err != nil {
			return err
		}
		outcome = "You marked this reminder as complete."
	case domain.SlackbotReminderSnoozeAction:
		location := m.MemberLocation(ctx, workspaceID, userID)
		due, known := domain.ReminderSnoozeDue(action.Value, time.Now(), location)
		if !known {
			return domain.ErrInvalidReminder
		}
		if _, err := m.AddReminder(ctx, workspaceID, userID, userID, reminder.Text, domain.ReminderSchedule{Due: due}); err != nil {
			return err
		}
		outcome = "I'll remind you " + slackbotReminderWhen(action.Value, due.In(location)) + "."
	default:
		return store.ErrNotFound
	}
	blocks, err := slackbotReminderAnswered(message.Blocks, action.BlockID, outcome)
	if err != nil {
		return err
	}
	previous := message
	message.Blocks = blocks
	event, err := messageMutationEvent(workspaceID, "message.changed", message, previous)
	if err != nil {
		return err
	}
	return m.Store.UpdateMessage(ctx, message, event)
}

// slackbotReminderWhen says when a snoozed reminder comes back.
func slackbotReminderWhen(choice string, due time.Time) string {
	for _, option := range domain.ReminderSnoozeOptions {
		if option.Value == choice && (choice == "20m" || choice == "1h" || choice == "3h") {
			return strings.ToLower(option.Text)
		}
	}
	return due.Format("Monday, January 2") + " at " + due.Format("3:04 PM")
}

// slackbotReminderAnswered is the reminder message's blocks with its controls
// replaced by what the member chose.
func slackbotReminderAnswered(raw, controls, outcome string) (string, error) {
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		return "", err
	}
	for index, block := range blocks {
		if block["block_id"] == controls {
			blocks[index] = map[string]any{"type": "context", "block_id": controls, "elements": []any{map[string]any{"type": "mrkdwn", "text": outcome}}}
		}
	}
	encoded, err := json.Marshal(blocks)
	return string(encoded), err
}
