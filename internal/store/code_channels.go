package store

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// CodeChannelStore is the persistence of Slack Code's code channels. Every
// mutation commits its events in the same transaction as its rows.
type CodeChannelStore interface {
	// CreateCodeChannel creates the channel's conversation, with the given
	// members, and its code channel record in one transaction. It answers
	// ErrAlreadyExists when the name is taken or the agent's session already
	// has a channel.
	CreateCodeChannel(context.Context, domain.Conversation, []domain.UserID, domain.CodeChannel, []events.Event) error
	// GetCodeChannel reads a conversation's code channel record, or
	// ErrNotFound when the conversation is not a code channel.
	GetCodeChannel(context.Context, domain.WorkspaceID, domain.ConversationID) (domain.CodeChannel, error)
	// FindCodeChannelBySession reads the channel an agent created for its
	// session, or ErrNotFound.
	FindCodeChannelBySession(context.Context, domain.WorkspaceID, domain.AppID, string) (domain.CodeChannel, error)
	// UpdateCodeChannel writes the record's properties when it was last
	// updated at the given instant, answering ErrConflict when another write
	// came first and ErrNotFound when there is no record.
	UpdateCodeChannel(context.Context, domain.CodeChannel, time.Time, events.Event) error
}

// CodeChannelViewStore is the persistence of code channel views. Every
// mutation commits its events in the same transaction as its rows.
type CodeChannelViewStore interface {
	// SetCodeChannelView writes the view named by its key. A new key gets
	// the given ID, file ID and creation instant at version 1; a key the
	// channel already has keeps its ID, file ID and creation instant and
	// advances its version. It answers the view as stored.
	SetCodeChannelView(context.Context, domain.CodeChannelView, events.Event) (domain.CodeChannelView, error)
	// ListCodeChannelViews answers a channel's views, oldest first.
	ListCodeChannelViews(context.Context, domain.WorkspaceID, domain.ConversationID) ([]domain.CodeChannelView, error)
	// RemoveCodeChannelView deletes a view by its ID, answering ErrNotFound
	// when the channel has no such view.
	RemoveCodeChannelView(context.Context, domain.WorkspaceID, domain.ConversationID, domain.CodeChannelViewID, events.Event) error
}
