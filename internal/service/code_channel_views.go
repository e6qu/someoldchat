package service

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Code channel views: agents.conversations.setView, listViews and removeView,
// and the read a member's client renders the tabs from.

// SetCodeChannelView is agents.conversations.setView: the agent creates or
// updates the view its key names. A canvas view grants the channel the
// access it names to the canvas, which the agent must own.
func (m Messages) SetCodeChannelView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, request domain.CodeChannelViewRequest) (domain.CodeChannelView, error) {
	if _, err := m.agentCodeChannel(ctx, workspaceID, actor, app, conversation); err != nil {
		return domain.CodeChannelView{}, err
	}
	request, err := domain.NormalizeCodeChannelViewRequest(request)
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	switch request.Type {
	case domain.CodeChannelViewBlockKit:
		normalized, err := domain.NormalizeBlocks([]byte(request.Blocks))
		if err != nil || normalized == "" {
			return domain.CodeChannelView{}, domain.ErrInvalidBlocks
		}
		if err := validateMessageBlocks(normalized); err != nil {
			return domain.CodeChannelView{}, err
		}
		request.Blocks = normalized
	case domain.CodeChannelViewCanvas:
		if err := m.SetCanvasAccess(ctx, workspaceID, actor, request.CanvasID, request.AccessLevel.Grant(), []domain.ConversationID{conversation}, nil); errors.Is(err, store.ErrNotFound) {
			return domain.CodeChannelView{}, domain.ErrCodeChannelViewCanvasNotFound
		} else if err != nil {
			return domain.CodeChannelView{}, err
		}
	}
	id, err := domain.NewCodeChannelViewID()
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	file, err := domain.NewFileID()
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	now := time.Now().UTC()
	view := domain.CodeChannelView{
		WorkspaceID: workspaceID, Conversation: conversation, ID: id, FileID: file,
		Key: request.Key, Type: request.Type, Label: domain.CodeChannelViewLabel(request), AppID: app, BotUserID: actor,
		Content: request.Content, Blocks: request.Blocks, CanvasID: request.CanvasID, AccessLevel: request.AccessLevel,
		AgentContentHash: request.AgentContentHash, PRURL: request.PRURL, BaseBranch: request.BaseBranch, HeadBranch: request.HeadBranch,
		CSP: request.CSP, CreatedAt: now, UpdatedAt: now,
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload(events.CodeChannelViewSetTopic,
		events.String("channel_id", string(conversation)), events.String("view_key", request.Key), events.String("app_id", string(app))), now)
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	return m.Store.SetCodeChannelView(ctx, view, event)
}

// CodeChannelViews is a code channel's views, oldest first, for an agent's
// listViews or a member's client: anyone who can read the channel.
func (m Messages) CodeChannelViews(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID) ([]domain.CodeChannelView, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return nil, err
	}
	if _, err := m.Store.GetCodeChannel(ctx, workspaceID, conversation); errors.Is(err, store.ErrNotFound) {
		return nil, domain.ErrNotCodeChannel
	} else if err != nil {
		return nil, err
	}
	return m.Store.ListCodeChannelViews(ctx, workspaceID, conversation)
}

// RemoveCodeChannelView is agents.conversations.removeView: the view named by
// exactly one of its tab ID or the agent's key.
func (m Messages) RemoveCodeChannelView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CodeChannelViewID, key string) (domain.CodeChannelViewID, error) {
	if _, err := m.agentCodeChannel(ctx, workspaceID, actor, app, conversation); err != nil {
		return "", err
	}
	if (id == "") == (key == "") {
		return "", domain.ErrInvalidCodeChannelView
	}
	views, err := m.Store.ListCodeChannelViews(ctx, workspaceID, conversation)
	if err != nil {
		return "", err
	}
	for _, view := range views {
		if (id != "" && view.ID == id) || (key != "" && view.Key == key) {
			event, err := newEvent(workspaceID, actor, events.NewPayload(events.CodeChannelViewRemovedTopic,
				events.String("channel_id", string(conversation)), events.String("view_id", string(view.ID)), events.String("app_id", string(app))), time.Now().UTC())
			if err != nil {
				return "", err
			}
			if err := m.Store.RemoveCodeChannelView(ctx, workspaceID, conversation, view.ID, event); errors.Is(err, store.ErrNotFound) {
				return "", domain.ErrCodeChannelViewNotFound
			} else if err != nil {
				return "", err
			}
			return view.ID, nil
		}
	}
	return "", domain.ErrCodeChannelViewNotFound
}
