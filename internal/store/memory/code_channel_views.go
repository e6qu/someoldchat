package memory

import (
	"context"
	"sort"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The code channel view repository mirrors the SQL profile: a view is unique
// by its channel and key, keeps its IDs and creation instant across writes,
// and advances its version on each.

func cloneCodeChannelView(value domain.CodeChannelView) domain.CodeChannelView {
	value.CSP.ConnectDomains = append([]string(nil), value.CSP.ConnectDomains...)
	value.CSP.ResourceDomains = append([]string(nil), value.CSP.ResourceDomains...)
	if len(value.CSP.ConnectDomains) == 0 {
		value.CSP.ConnectDomains = nil
	}
	if len(value.CSP.ResourceDomains) == 0 {
		value.CSP.ResourceDomains = nil
	}
	return value
}

func (s *Store) SetCodeChannelView(_ context.Context, view domain.CodeChannelView, event events.Event) (domain.CodeChannelView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[view.Conversation]; !ok {
		return domain.CodeChannelView{}, store.ErrNotFound
	}
	view.Version = 1
	for id, existing := range s.codeChannelViews {
		if existing.WorkspaceID == view.WorkspaceID && existing.Conversation == view.Conversation && existing.Key == view.Key {
			view.ID, view.FileID, view.CreatedAt, view.Version = existing.ID, existing.FileID, existing.CreatedAt, existing.Version+1
			delete(s.codeChannelViews, id)
			break
		}
	}
	if _, taken := s.codeChannelViews[view.ID]; taken {
		return domain.CodeChannelView{}, store.ErrAlreadyExists
	}
	s.codeChannelViews[view.ID] = cloneCodeChannelView(view)
	s.outbox = append(s.outbox, event)
	return cloneCodeChannelView(view), nil
}

func (s *Store) ListCodeChannelViews(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.CodeChannelView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := []domain.CodeChannelView{}
	for _, view := range s.codeChannelViews {
		if view.WorkspaceID == workspace && view.Conversation == conversation {
			views = append(views, cloneCodeChannelView(view))
		}
	}
	sort.Slice(views, func(left, right int) bool {
		if !views[left].CreatedAt.Equal(views[right].CreatedAt) {
			return views[left].CreatedAt.Before(views[right].CreatedAt)
		}
		return views[left].ID < views[right].ID
	})
	return views, nil
}

func (s *Store) RemoveCodeChannelView(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.CodeChannelViewID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	view, ok := s.codeChannelViews[id]
	if !ok || view.WorkspaceID != workspace || view.Conversation != conversation {
		return store.ErrNotFound
	}
	delete(s.codeChannelViews, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) deleteConversationCodeChannelViewsLocked(conversation domain.ConversationID) {
	for id, view := range s.codeChannelViews {
		if view.Conversation == conversation {
			delete(s.codeChannelViews, id)
		}
	}
}
