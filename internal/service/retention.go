package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Retention. The workspace default governs everything; a channel may override
// the message duration, which is the only part Slack's API configures.
//
// Recorded deviation: Slack restricts these three methods to Enterprise plans.
// This product has no plans, so it applies the same authority rule it applies
// to every other administrative operation — the workspace administrator role —
// rather than inventing a plan tier to refuse against.

var (
	// ErrInvalidRetentionDuration refuses a duration outside Slack's documented
	// range: an integer greater than zero and below 36500 days.
	ErrInvalidRetentionDuration = errors.New("retention duration is invalid")
	// ErrRetentionNotSupported refuses a conversation type Slack will not apply
	// a custom retention policy to.
	ErrRetentionNotSupported = errors.New("conversation type does not support a retention policy")
)

// WorkspaceRetention reads the workspace default. It is an administrative read
// because a retention policy tells an attacker how long evidence survives.
func (m Messages) WorkspaceRetention(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (domain.RetentionPolicy, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.RetentionPolicy{}, err
	}
	return m.Store.GetRetentionPolicy(ctx, workspaceID)
}

// SetWorkspaceRetention writes the workspace default. Zero is accepted here and
// means keep forever, which is how an administrator turns retention off again —
// the per-channel API has no way to say it, but the workspace policy does.
func (m Messages) SetWorkspaceRetention(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, policy domain.RetentionPolicy) (domain.RetentionPolicy, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.RetentionPolicy{}, err
	}
	if !policy.Valid() {
		return domain.RetentionPolicy{}, ErrInvalidRetentionDuration
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("retention.policy_changed",
		events.String("message_days", retentionDays(policy.MessageDays)),
		events.String("file_days", retentionDays(policy.FileDays)),
	), time.Now().UTC())
	if err != nil {
		return domain.RetentionPolicy{}, err
	}
	if err := m.Store.SetRetentionPolicy(ctx, workspaceID, policy, event); err != nil {
		return domain.RetentionPolicy{}, err
	}
	return policy, nil
}

// LastRetentionSweep reports when the sweep last ran, so an administration
// page can show that the policy is actually being applied. Without it a
// workspace could believe retention was working for weeks after the worker
// stopped, which is precisely the failure a scheduled deletion hides.
func (m Messages) LastRetentionSweep(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) (time.Time, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return time.Time{}, err
	}
	return m.Store.LastRetentionSweep(ctx, workspaceID)
}

// ConversationRetention reports a channel's override and the duration that
// actually governs it, so a caller never has to resolve the two itself and
// cannot resolve them differently from the sweep.
func (m Messages) ConversationRetention(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) (domain.ConversationRetention, int, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.ConversationRetention{}, 0, err
	}
	override, err := m.Store.GetConversationRetention(ctx, workspaceID, conversationID)
	if err != nil {
		return domain.ConversationRetention{}, 0, err
	}
	policy, err := m.Store.GetRetentionPolicy(ctx, workspaceID)
	if err != nil {
		return domain.ConversationRetention{}, 0, err
	}
	return override, override.Effective(policy), nil
}

// SetConversationRetention applies a custom duration to one conversation.
func (m Messages) SetConversationRetention(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, days int) error {
	conversation, err := m.retentionTarget(ctx, workspaceID, actorID, conversationID)
	if err != nil {
		return err
	}
	// Slack's own bound: greater than zero and below 36500. Zero is refused
	// rather than treated as forever, because removing the override is how a
	// channel returns to the workspace default and two ways of saying it would
	// leave the caller guessing which one they got.
	if days <= 0 || !domain.ValidRetentionDays(days) {
		return ErrInvalidRetentionDuration
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("retention.policy_changed",
		events.String("channel_id", string(conversation.ID)),
		events.String("message_days", retentionDays(days)),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.SetConversationRetention(ctx, workspaceID, conversationID, days, time.Now().UTC(), event)
}

// RemoveConversationRetention returns a conversation to the workspace default.
func (m Messages) RemoveConversationRetention(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) error {
	conversation, err := m.retentionTarget(ctx, workspaceID, actorID, conversationID)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("retention.policy_changed",
		events.String("channel_id", string(conversation.ID)),
		events.String("message_days", "0"),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.RemoveConversationRetention(ctx, workspaceID, conversationID, event)
}

// retentionTarget authorizes the actor and refuses the conversation types Slack
// refuses. A group direct message has no administrator to govern it, and the
// workspace's default channel is the one nobody may quietly start deleting —
// both are Slack's rules, not ours.
func (m Messages) retentionTarget(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.Conversation{}, store.ErrNotFound
	}
	if conversation.IsGroupDirect {
		return domain.Conversation{}, ErrRetentionNotSupported
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return domain.Conversation{}, err
	}
	for _, required := range workspace.DefaultChannelIDs {
		if required == conversationID {
			return domain.Conversation{}, ErrRetentionNotSupported
		}
	}
	return conversation, nil
}

func retentionDays(days int) string {
	return strconv.Itoa(days)
}
