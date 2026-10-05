package service

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// agents.conversations.setCommands, getCanvas and setCanvasContent.

// SetCodeChannelCommands is agents.conversations.setCommands: the calling
// agent's commands replace its own set in the channel. It answers how many
// commands the channel holds.
func (m Messages) SetCodeChannelCommands(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, commands []domain.CodeChannelCommand) (int, error) {
	for attempt := 0; attempt < agentSessionWriteAttempts; attempt++ {
		record, err := m.agentCodeChannel(ctx, workspaceID, actor, app, conversation)
		if err != nil {
			return 0, err
		}
		normalized, err := domain.NormalizeCodeChannelCommands(commands, app, actor)
		if err != nil {
			return 0, err
		}
		updated := record
		if updated.Commands, err = record.WithAgentCommands(actor, normalized); err != nil {
			return 0, err
		}
		updated.UpdatedAt = time.Now().UTC()
		if !updated.UpdatedAt.After(record.UpdatedAt) {
			updated.UpdatedAt = record.UpdatedAt.Add(time.Nanosecond)
		}
		event, err := newEvent(workspaceID, actor, events.NewPayload(events.CodeChannelCommandsSetTopic,
			events.String("channel_id", string(conversation)), events.String("app_id", string(app))), updated.UpdatedAt)
		if err != nil {
			return 0, err
		}
		err = m.Store.UpdateCodeChannel(ctx, updated, record.UpdatedAt, event)
		if err == nil {
			return len(updated.Commands), nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return 0, err
		}
	}
	return 0, store.ErrConflict
}

// codeChannelCanvas finds a canvas an agent works on in its code channel: one
// a canvas view of the channel shows, or the channel's own canvas, which the
// agent must be able to access as required. Any other canvas, or one the
// agent cannot access, is ErrCodeChannelViewCanvasNotFound.
func (m Messages) codeChannelCanvas(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CanvasID, required domain.AccessLevel) (domain.Canvas, error) {
	if _, err := m.agentCodeChannel(ctx, workspaceID, actor, app, conversation); err != nil {
		return domain.Canvas{}, err
	}
	attached := false
	views, err := m.Store.ListCodeChannelViews(ctx, workspaceID, conversation)
	if err != nil {
		return domain.Canvas{}, err
	}
	for _, view := range views {
		if view.Type == domain.CodeChannelViewCanvas && view.CanvasID == id {
			attached = true
		}
	}
	if !attached {
		own, err := m.Store.GetChannelCanvas(ctx, workspaceID, conversation)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return domain.Canvas{}, err
		}
		attached = err == nil && own.ID == id
	}
	if !attached {
		return domain.Canvas{}, domain.ErrCodeChannelViewCanvasNotFound
	}
	if err := m.requireCanvasAccess(ctx, workspaceID, actor, id, required); errors.Is(err, store.ErrNotFound) {
		return domain.Canvas{}, domain.ErrCodeChannelViewCanvasNotFound
	} else if err != nil {
		return domain.Canvas{}, err
	}
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Canvas{}, domain.ErrCodeChannelViewCanvasNotFound
	}
	return canvas, err
}

// CodeChannelCanvas is agents.conversations.getCanvas: the canvas and its
// comments, oldest first, as the agent reads them.
func (m Messages) CodeChannelCanvas(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CanvasID) (domain.Canvas, domain.CanvasCommentPage, error) {
	canvas, err := m.codeChannelCanvas(ctx, workspaceID, actor, app, conversation, id, domain.AccessRead)
	if err != nil {
		return domain.Canvas{}, domain.CanvasCommentPage{}, err
	}
	comments, err := m.Store.ListCanvasComments(ctx, workspaceID, actor, id, domain.PageRequest{Limit: domain.CodeChannelCanvasCommentLimit})
	if err != nil {
		return domain.Canvas{}, domain.CanvasCommentPage{}, err
	}
	return canvas, comments, nil
}

// SetCodeChannelCanvasContent is agents.conversations.setCanvasContent: the
// canvas becomes the markdown given. Only the sections that differ are
// rewritten, so an unchanged section keeps its comments; it answers how many
// sections changed, and writes nothing when none did.
func (m Messages) SetCodeChannelCanvasContent(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CanvasID, markdown string) (int, error) {
	canvas, err := m.codeChannelCanvas(ctx, workspaceID, actor, app, conversation, id, domain.AccessWrite)
	if err != nil {
		return 0, err
	}
	return m.rewriteCanvasMarkdown(ctx, workspaceID, actor, canvas, markdown)
}
