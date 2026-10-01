package sqlstore

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) SetExistingConversationsExcludedFromAI(ctx context.Context, workspace domain.WorkspaceID, ids []domain.ConversationID, excluded bool, event events.Event) (int, error) {
	if len(ids) == 0 {
		return 0, store.ErrInvalidArgument
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixNano()
	applied := 0
	for _, id := range ids {
		if err := checkConversationOwner(ctx, tx, workspace, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return 0, err
		}
		if excluded {
			if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_ai_exclusions(conversation_id, workspace_id, updated_at)
				VALUES (?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET updated_at = excluded.updated_at`, id, workspace, now); err != nil {
				return 0, classify(err)
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_ai_exclusions WHERE conversation_id = ? AND workspace_id = ?`, id, workspace); err != nil {
			return 0, classify(err)
		}
		applied++
	}
	if applied == 0 {
		return 0, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return 0, err
	}
	return applied, tx.Commit()
}
