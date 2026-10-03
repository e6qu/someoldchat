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
}
