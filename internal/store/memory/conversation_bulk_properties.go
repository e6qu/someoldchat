package memory

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) SetExistingConversationsExcludedFromAI(_ context.Context, workspace domain.WorkspaceID, ids []domain.ConversationID, excluded bool, event events.Event) (int, error) {
	if len(ids) == 0 {
		return 0, store.ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := make([]domain.ConversationID, 0, len(ids))
	for _, id := range ids {
		if s.checkConversationOwnerLocked(workspace, id) == nil {
			existing = append(existing, id)
		}
	}
	if len(existing) == 0 {
		return 0, store.ErrNotFound
	}
	for _, id := range existing {
		if excluded {
			s.aiExcludedConversations[id] = struct{}{}
			continue
		}
		delete(s.aiExcludedConversations, id)
	}
	s.outbox = append(s.outbox, event)
	return len(existing), nil
}
