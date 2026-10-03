package memory

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The code channel repository mirrors the SQL profile: a record beside each
// code channel's conversation, an agent's session key naming at most one
// channel, and every mutation's events appended under the same lock.

func cloneCodeChannel(value domain.CodeChannel) domain.CodeChannel {
	value.ContextBar = append([]domain.CodeChannelContextItem{}, value.ContextBar...)
	return value
}

func (s *Store) CreateCodeChannel(_ context.Context, conversation domain.Conversation, members []domain.UserID, value domain.CodeChannel, emitted []events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.conversations[conversation.ID]; exists {
		return store.ErrAlreadyExists
	}
	for _, existing := range s.conversations {
		if existing.WorkspaceID == conversation.WorkspaceID && !existing.IsDirectOrGroup() && existing.Name == conversation.Name {
			return store.ErrAlreadyExists
		}
	}
	if value.SessionID != "" {
		for _, existing := range s.codeChannels {
			if existing.WorkspaceID == value.WorkspaceID && existing.AppID == value.AppID && existing.SessionID == value.SessionID {
				return store.ErrAlreadyExists
			}
		}
	}
	for _, member := range members {
		if _, ok := s.users[member]; !ok {
			return store.ErrNotFound
		}
	}
	s.conversations[conversation.ID] = conversation
	s.conversationTeams[conversation.ID] = map[domain.WorkspaceID]struct{}{conversation.WorkspaceID: {}}
	s.conversationOrg[conversation.ID] = false
	s.memberships[conversation.ID] = make(map[domain.UserID]struct{}, len(members))
	for _, member := range members {
		s.memberships[conversation.ID][member] = struct{}{}
	}
	s.codeChannels[conversation.ID] = cloneCodeChannel(value)
	s.outbox = append(s.outbox, emitted...)
	return nil
}

func (s *Store) GetCodeChannel(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) (domain.CodeChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.codeChannels[conversation]
	if !ok || value.WorkspaceID != workspace {
		return domain.CodeChannel{}, store.ErrNotFound
	}
	return cloneCodeChannel(value), nil
}

func (s *Store) FindCodeChannelBySession(_ context.Context, workspace domain.WorkspaceID, app domain.AppID, session string) (domain.CodeChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if session == "" {
		return domain.CodeChannel{}, store.ErrNotFound
	}
	for _, value := range s.codeChannels {
		if value.WorkspaceID == workspace && value.AppID == app && value.SessionID == session {
			return cloneCodeChannel(value), nil
		}
	}
	return domain.CodeChannel{}, store.ErrNotFound
}

func (s *Store) UpdateCodeChannel(_ context.Context, value domain.CodeChannel, expected time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.codeChannels[value.Conversation]
	if !ok || current.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if !current.UpdatedAt.Equal(expected) {
		return store.ErrConflict
	}
	current.ContextBar = append([]domain.CodeChannelContextItem{}, value.ContextBar...)
	current.Summary = value.Summary
	current.AgentResource = value.AgentResource
	current.UpdatedAt = value.UpdatedAt
	s.codeChannels[value.Conversation] = current
	s.outbox = append(s.outbox, event)
	return nil
}
