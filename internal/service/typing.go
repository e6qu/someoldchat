package service

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Typing indicators: who is composing, right now, in a conversation you can
// see.
//
// The interesting decision here is what a typing signal is *not*. Every other
// mutation in this service commits an outbox event, because the fact it records
// is worth replaying to a client that reconnects and worth delivering to an app
// that subscribed. A typing signal is worth neither. Journalling it would
// replay "someone is typing" to a reconnecting client as though it were news,
// deliver it to apps that have no use for it, and write a durable record every
// few seconds for every member with a cursor in a composer.
//
// The audit that recorded this gap framed the remaining choice as a map in a
// single replica against a bus across several. It is neither, because the
// premise was wrong: live delivery in this product is not a push from the
// process that handled the write, it is each stream polling the store on its
// own timer. So the store is already the medium every replica shares, and a
// signal only has to be a short-lived row that the poll each stream already
// performs also reads. No bus, and no single-replica shortcut to regret later.

// SetTyping records that a member is composing in a conversation. Renewal is
// the common case rather than the exception — a client re-sends every few
// seconds while typing continues — so this replaces the member's signal instead
// of accumulating them, and there is no "stopped typing" call to make: the
// signal expires on its own, which is also what makes a client that vanishes
// mid-word stop appearing.
func (m Messages) SetTyping(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID) error {
	// Membership, not posting permission. Slack shows the indicator to members
	// of the conversation, and a member of a read-only channel who starts
	// typing has still started typing; refusing the signal there would make the
	// indicator quietly wrong rather than correctly absent.
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversationID); err != nil {
		return err
	}
	now := time.Now().UTC()
	return m.Store.RecordTyping(ctx, domain.TypingSignal{
		WorkspaceID:  workspaceID,
		Conversation: conversationID,
		UserID:       actor,
		ExpiresAt:    now.Add(domain.TypingSignalTTL),
	})
}

// TypingSignals reports who is composing in the conversations this reader
// belongs to. It takes no page request: the answer is bounded by how many
// people are typing at this instant, and a cursor over a set that is different
// by the time the second page is asked for would be a fiction.
func (m Messages) TypingSignals(ctx context.Context, workspaceID domain.WorkspaceID, reader domain.UserID) ([]domain.TypingSignal, error) {
	return m.Store.ListTypingSignals(ctx, workspaceID, reader, time.Now().UTC())
}

// TypingIn narrows the same read to one conversation, which is what a client
// showing a single open conversation needs. It filters in the service rather
// than issuing a second query shape, so both callers share one authorization
// rule and one visibility join.
func (m Messages) TypingIn(ctx context.Context, workspaceID domain.WorkspaceID, reader domain.UserID, conversationID domain.ConversationID) ([]domain.TypingSignal, error) {
	signals, err := m.TypingSignals(ctx, workspaceID, reader)
	if err != nil {
		return nil, err
	}
	return domain.TypingSignalsIn(signals, conversationID), nil
}
