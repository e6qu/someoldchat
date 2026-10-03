package api

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// CodeChannels is Slack Code's code channel surface of the chat boundary: the
// agents.conversations.* methods an agent calls with its bot user and app,
// and the read a member's client uses. Service embeds it.
type CodeChannels interface {
	// CreateCodeChannel is agents.conversations.create.
	CreateCodeChannel(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.CodeChannelRequest) (domain.CodeChannel, error)
	// ArchiveCodeChannel is agents.conversations.archive, with the summary
	// message to share back, if any.
	ArchiveCodeChannel(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.MessageTimestamp) error
	// SetCodeChannelProperties is agents.conversations.setProperties.
	SetCodeChannelProperties(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CodeChannelProperties) error
	// CodeChannel reads a code channel's record for a member who can see the
	// channel.
	CodeChannel(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.CodeChannel, error)
	// SetCodeChannelView is agents.conversations.setView.
	SetCodeChannelView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CodeChannelViewRequest) (domain.CodeChannelView, error)
	// CodeChannelViews is a code channel's views, for an agent's listViews
	// or a member's client.
	CodeChannelViews(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) ([]domain.CodeChannelView, error)
	// RemoveCodeChannelView is agents.conversations.removeView, naming the
	// view by exactly one of its tab ID or key; it answers the removed tab.
	RemoveCodeChannelView(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CodeChannelViewID, string) (domain.CodeChannelViewID, error)
	// SetCodeChannelCommands is agents.conversations.setCommands; it answers
	// how many commands the channel holds.
	SetCodeChannelCommands(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, []domain.CodeChannelCommand) (int, error)
	// CodeChannelCanvas is agents.conversations.getCanvas: the canvas and its
	// comments.
	CodeChannelCanvas(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CanvasID) (domain.Canvas, domain.CanvasCommentPage, error)
	// SetCodeChannelCanvasContent is agents.conversations.setCanvasContent;
	// it answers how many sections changed.
	SetCodeChannelCanvasContent(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CanvasID, string) (int, error)
}
