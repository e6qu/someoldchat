package service

import (
	"context"
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
		Text: post.Text, CreatedAt: domain.MessageInstant(time.Now()),
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
