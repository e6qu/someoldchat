package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Code channels: Slack Code's agents.conversations.* create, archive and
// setProperties. Each is an agent's call, made with its bot user (actor) on
// behalf of its app.

// codeChannelNameAttempts bounds how many numbered names a new code channel
// tries when its name is taken, as a member creating several channels for one
// task would number them.
const codeChannelNameAttempts = 20

// CreateCodeChannel is agents.conversations.create. A session key the agent
// already used answers that session's channel rather than creating another.
// A channel started from a message is named after it when the agent gives no
// name, and the message's author is invited.
func (m Messages) CreateCodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, request domain.CodeChannelRequest) (domain.CodeChannel, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.CodeChannel{}, err
	}
	if app == "" {
		return domain.CodeChannel{}, domain.ErrAgentSessionNotAgent
	}
	// A private code channel is a private channel like any other, so the
	// workspace's "who can create private channels" policy governs it too.
	if request.Private {
		if err := m.requirePrivateChannelCreator(ctx, workspaceID, actor); err != nil {
			return domain.CodeChannel{}, err
		}
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	if !domain.ValidCodeChannelSessionID(request.SessionID) || (request.Origin.Channel == "") != (request.Origin.Timestamp == "") {
		return domain.CodeChannel{}, domain.ErrInvalidCodeChannel
	}
	if existing, err := m.Store.FindCodeChannelBySession(ctx, workspaceID, app, request.SessionID); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.CodeChannel{}, err
	}
	members := []domain.UserID{actor}
	originText := ""
	if !request.Origin.Empty() {
		// The origin must be a channel the agent can read, and not one
		// shared with another organization.
		if err := m.authorizeConversation(ctx, workspaceID, actor, request.Origin.Channel); err != nil {
			return domain.CodeChannel{}, err
		}
		origin, err := m.Store.GetConversation(ctx, request.Origin.Channel)
		if err != nil {
			return domain.CodeChannel{}, err
		}
		if origin.IsExtShared {
			return domain.CodeChannel{}, domain.ErrOriginExternallyShared
		}
		at, err := domain.ParseMessageTimestamp(request.Origin.Timestamp)
		if err != nil {
			return domain.CodeChannel{}, domain.ErrInvalidTimestamp
		}
		message, err := m.Store.GetMessageByCreatedAt(ctx, request.Origin.Channel, at)
		if errors.Is(err, store.ErrNotFound) || (err == nil && message.Deleted) {
			return domain.CodeChannel{}, domain.ErrCodeChannelMessageNotFound
		}
		if err != nil {
			return domain.CodeChannel{}, err
		}
		originText = message.Text
		if message.AuthorID != actor && message.AuthorID != "" {
			if _, err := m.activeWorkspaceMembership(ctx, workspaceID, message.AuthorID); err == nil {
				members = append(members, message.AuthorID)
			} else if !errors.Is(err, store.ErrNotFound) {
				return domain.CodeChannel{}, err
			}
		}
	} else if strings.TrimSpace(request.Name) == "" {
		return domain.CodeChannel{}, domain.ErrInvalidCodeChannel
	}
	base := domain.CodeChannelName(request.Name, originText)
	if base == "" {
		return domain.CodeChannel{}, domain.ErrInvalidCodeChannelName
	}
	now := time.Now().UTC()
	for attempt := 1; attempt <= codeChannelNameAttempts; attempt++ {
		name := base
		if attempt > 1 {
			suffix := fmt.Sprintf("-%d", attempt)
			name = strings.TrimRight(base[:min(len(base), domain.CodeChannelNameLimit-len(suffix))], "-_") + suffix
		}
		id, err := domain.NewConversationID()
		if err != nil {
			return domain.CodeChannel{}, err
		}
		conversation := domain.Conversation{ID: id, WorkspaceID: workspaceID, Name: name, Kind: domain.ConversationKindFor(request.Private, false, false),
			Created: conversationInstant(now), CreatorID: actor}
		created, err := conversationLifecycleEvent(workspaceID, "conversation.created", conversation, actor)
		if err != nil {
			return domain.CodeChannel{}, err
		}
		emitted := []events.Event{created}
		if len(members) > 1 {
			invited, err := newEvent(workspaceID, actor, events.NewPayload("conversation.members_invited",
				events.String("channel_id", string(id)), events.Strings("user_ids", userIDStrings(members[1:]))), now)
			if err != nil {
				return domain.CodeChannel{}, err
			}
			emitted = append(emitted, invited)
		}
		record := domain.CodeChannel{
			WorkspaceID: workspaceID, Conversation: id, AppID: app, BotUserID: actor, SessionID: request.SessionID,
			Origin: request.Origin, ContextBar: []domain.CodeChannelContextItem{}, CreatedAt: now, UpdatedAt: now,
		}
		err = m.Store.CreateCodeChannel(ctx, conversation, members, record, emitted)
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, store.ErrAlreadyExists) {
			return domain.CodeChannel{}, err
		}
		// Either the name is taken, or another call created this session's
		// channel first; that one is the answer.
		if existing, findErr := m.Store.FindCodeChannelBySession(ctx, workspaceID, app, request.SessionID); findErr == nil {
			return existing, nil
		}
	}
	return domain.CodeChannel{}, store.ErrAlreadyExists
}

// CodeChannel reads a code channel's record for a member who can see the
// channel; store.ErrNotFound when the channel is not a code channel.
func (m Messages) CodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID) (domain.CodeChannel, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.CodeChannel{}, err
	}
	return m.Store.GetCodeChannel(ctx, workspaceID, conversation)
}

// agentCodeChannel admits an agent that is a member of the code channel and
// answers the channel's record.
func (m Messages) agentCodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID) (domain.CodeChannel, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversation); err != nil {
		return domain.CodeChannel{}, err
	}
	if app == "" {
		return domain.CodeChannel{}, domain.ErrAgentSessionNotAgent
	}
	record, err := m.Store.GetCodeChannel(ctx, workspaceID, conversation)
	if errors.Is(err, store.ErrNotFound) {
		return domain.CodeChannel{}, domain.ErrNotCodeChannel
	}
	return record, err
}

// ArchiveCodeChannel is agents.conversations.archive. With a summary message,
// the agent shares it back as a reply on the message the work began from
// before the channel is archived.
func (m Messages) ArchiveCodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, summary domain.MessageTimestamp) error {
	record, err := m.agentCodeChannel(ctx, workspaceID, actor, app, conversation)
	if err != nil {
		return err
	}
	channel, err := m.Store.GetConversation(ctx, conversation)
	if err != nil {
		return err
	}
	if channel.Archived {
		return domain.ErrConversationAlreadyArchived
	}
	if summary != "" {
		if record.Origin.Empty() {
			return domain.ErrCodeChannelHasNoOrigin
		}
		at, err := domain.ParseMessageTimestamp(summary)
		if err != nil {
			return domain.ErrInvalidTimestamp
		}
		message, err := m.Store.GetMessageByCreatedAt(ctx, conversation, at)
		if errors.Is(err, store.ErrNotFound) || (err == nil && message.Deleted) {
			return domain.ErrCodeChannelMessageNotFound
		}
		if err != nil {
			return err
		}
		if _, err := m.PostMessageAs(ctx, workspaceID, actor, domain.MessagePostRequest{
			Conversation: record.Origin.Channel, ThreadTimestamp: record.Origin.Timestamp,
			Text: message.Text, Blocks: message.Blocks, AppID: app,
			IdempotencyKey: "code-channel-summary:" + string(conversation) + ":" + string(summary),
		}); err != nil {
			return err
		}
	}
	_, err = m.SetConversationArchived(ctx, workspaceID, actor, conversation, true)
	return err
}

// SetCodeChannelProperties is agents.conversations.setProperties. The calling
// agent's context bar items replace its own; the summary message must be a
// message of the channel; each agent resource field given is written.
func (m Messages) SetCodeChannelProperties(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, properties domain.CodeChannelProperties) error {
	for attempt := 0; attempt < agentSessionWriteAttempts; attempt++ {
		record, err := m.agentCodeChannel(ctx, workspaceID, actor, app, conversation)
		if err != nil {
			return err
		}
		updated := record
		if properties.ContextBar != nil {
			items, err := domain.NormalizeContextBar(*properties.ContextBar, actor)
			if err != nil {
				return err
			}
			if updated.ContextBar, err = record.WithAgentContextBar(actor, items); err != nil {
				return err
			}
		}
		if properties.Summary != nil {
			if err := m.validateCodeChannelSummary(ctx, conversation, *properties.Summary); err != nil {
				return err
			}
			updated.Summary = *properties.Summary
		}
		if !properties.AgentResource.Empty() {
			updated.AgentResource = properties.AgentResource.Apply(record.AgentResource)
			if !domain.ValidAgentResource(updated.AgentResource) {
				return domain.ErrInvalidCodeChannel
			}
		}
		updated.UpdatedAt = time.Now().UTC()
		if !updated.UpdatedAt.After(record.UpdatedAt) {
			updated.UpdatedAt = record.UpdatedAt.Add(time.Nanosecond)
		}
		event, err := newEvent(workspaceID, actor, events.NewPayload(events.CodeChannelPropertiesSetTopic,
			events.String("channel_id", string(conversation)), events.String("app_id", string(app))), updated.UpdatedAt)
		if err != nil {
			return err
		}
		err = m.Store.UpdateCodeChannel(ctx, updated, record.UpdatedAt, event)
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return store.ErrConflict
}

// validateCodeChannelSummary requires the summary to name a live message of
// the channel, in the thread it says it is in.
func (m Messages) validateCodeChannelSummary(ctx context.Context, conversation domain.ConversationID, summary domain.CodeChannelSummary) error {
	at, err := domain.ParseMessageTimestamp(summary.MessageTimestamp)
	if err != nil {
		return domain.ErrInvalidTimestamp
	}
	message, err := m.Store.GetMessageByCreatedAt(ctx, conversation, at)
	if errors.Is(err, store.ErrNotFound) || (err == nil && message.Deleted) {
		return domain.ErrCodeChannelMessageNotFound
	}
	if err != nil {
		return err
	}
	if summary.ThreadTimestamp != "" && message.ThreadTimestamp != summary.ThreadTimestamp {
		return domain.ErrInvalidCodeChannel
	}
	return nil
}
