package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// The assistant thread surface: an app may give a thread a title, show a
// transient status while it works, and offer prompts before anyone has typed.
//
// None of it is message content, which is why it is stored beside the thread
// rather than as messages: a status that read as a message would appear in
// history, in search and in unread counts, and would still be there after the
// assistant stopped thinking.

var (
	// ErrInvalidAssistantThread is the single sentinel for a malformed
	// assistant write — an empty title, an empty status, no prompts, or more
	// prompts than a pane can offer.
	ErrInvalidAssistantThread = errors.New("invalid assistant thread state")
	// ErrAssistantThreadNotFound distinguishes "this thread has no assistant
	// state" from "this thread does not exist", which the client needs in order
	// to render nothing rather than an error.
	ErrAssistantThreadNotFound = errors.New("assistant thread state not found")
)

// SetAssistantThreadTitle names a thread in the client. Slack's own assistant
// uses it to replace "New chat" with what the conversation turned out to be
// about.
func (m Messages) SetAssistantThreadTitle(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, thread domain.MessageTimestamp, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return ErrInvalidAssistantThread
	}
	return m.setAssistantThread(ctx, workspaceID, actor, conversationID, thread, domain.AssistantThreadTitle,
		func(value *domain.AssistantThread) { value.Title = title })
}

// SetAssistantThreadStatus shows what the assistant is doing. It is transient
// by design: an app clears it by setting an empty status, which is why the
// empty string is accepted here and refused for a title.
func (m Messages) SetAssistantThreadStatus(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, thread domain.MessageTimestamp, status string) error {
	return m.setAssistantThread(ctx, workspaceID, actor, conversationID, thread, domain.AssistantThreadStatus,
		func(value *domain.AssistantThread) { value.Status = strings.TrimSpace(status) })
}

// SetAssistantThreadSuggestedPrompts offers openings a member can click.
func (m Messages) SetAssistantThreadSuggestedPrompts(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, thread domain.MessageTimestamp, title string, prompts []domain.AssistantPrompt) error {
	if len(prompts) == 0 || len(prompts) > domain.AssistantPromptLimit {
		return ErrInvalidAssistantThread
	}
	cleaned := make([]domain.AssistantPrompt, 0, len(prompts))
	for _, prompt := range prompts {
		prompt.Title = strings.TrimSpace(prompt.Title)
		prompt.Message = strings.TrimSpace(prompt.Message)
		if prompt.Title == "" || prompt.Message == "" {
			return ErrInvalidAssistantThread
		}
		cleaned = append(cleaned, prompt)
	}
	return m.setAssistantThread(ctx, workspaceID, actor, conversationID, thread, domain.AssistantThreadPrompts,
		func(value *domain.AssistantThread) {
			value.PromptsTitle = strings.TrimSpace(title)
			value.Prompts = cleaned
		})
}

// AssistantThread reads the state a client renders beside a thread.
func (m Messages) AssistantThread(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, thread domain.MessageTimestamp) (domain.AssistantThread, error) {
	if err := m.authorizeConversation(ctx, workspaceID, actor, conversationID); err != nil {
		return domain.AssistantThread{}, err
	}
	return m.Store.GetAssistantThread(ctx, workspaceID, conversationID, thread)
}

// setAssistantThread is the shared authorization and write. The caller must be
// able to post in the conversation: assistant state is displayed to everyone
// who can read the thread, so writing it is a posting-shaped act even though it
// creates no message.
func (m Messages) setAssistantThread(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, thread domain.MessageTimestamp, field domain.AssistantThreadField, apply func(*domain.AssistantThread)) error {
	if _, err := domain.ParseMessageTimestamp(thread); err != nil {
		return ErrInvalidTimestamp
	}
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversationID); err != nil {
		return err
	}
	now := time.Now().UTC()
	value := domain.AssistantThread{WorkspaceID: workspaceID, Conversation: conversationID, ThreadTimestamp: thread, UpdatedAt: now}
	apply(&value)
	event, err := newEvent(workspaceID, actor, events.NewPayload("assistant.thread_updated",
		events.String("channel_id", string(conversationID)),
		events.String("thread_ts", string(thread)),
		events.String("field", string(field)),
	), now)
	if err != nil {
		return err
	}
	return m.Store.SetAssistantThread(ctx, value, field, event)
}
