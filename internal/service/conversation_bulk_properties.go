package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// AdminBulkSetConversationProperties sets one property on up to
// domain.ConversationPropertyBulkLimit channels
// (admin.conversations.bulkSetProperties).
//
// Unlike admin.conversations.bulkSetExcludeFromSlackAi, which refuses the whole
// request when one channel is not here, this method's reference reserves its
// refusal, no_valid_channels, for a request in which all the channels are
// invalid. A channel of this workspace is therefore set and one that is not is
// skipped, in one transaction, and only a request naming none of them fails.
func (m Messages) AdminBulkSetConversationProperties(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, ids []domain.ConversationID, property domain.ConversationProperty) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	seen := make(map[domain.ConversationID]struct{}, len(ids))
	named := make([]domain.ConversationID, 0, len(ids))
	for _, id := range ids {
		id = domain.ConversationID(strings.TrimSpace(string(id)))
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		named = append(named, id)
	}
	if len(named) == 0 || len(named) > domain.ConversationPropertyBulkLimit {
		return domain.ErrInvalidConversation
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("channel.ai_exclusion_set",
		events.Int("channels", int64(len(named)))), time.Now().UTC())
	if err != nil {
		return err
	}
	if _, err := m.Store.SetExistingConversationsExcludedFromAI(ctx, workspaceID, named, property.ExcludeFromSlackAI, event); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.ErrNoValidChannels
		}
		return err
	}
	return nil
}
