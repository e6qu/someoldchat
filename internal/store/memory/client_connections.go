package memory

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// OpenClientConnection mirrors the SQL profile: lapsed leases of the member
// are removed, and the online event is journalled only for a member who had no
// connection at now. Whether the member may connect is the service's to
// decide.
func (s *Store) OpenClientConnection(_ context.Context, value domain.ClientConnection, now time.Time, online events.Event) error {
	if value.ID == "" || value.ExpiresAt.IsZero() {
		return store.InvalidArgument("a client connection requires an ID and a lease")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[value.UserID]
	if !ok || user.WorkspaceID != value.WorkspaceID {
		return store.ErrNotFound
	}
	if _, exists := s.clientConnections[value.ID]; exists {
		return store.ErrAlreadyExists
	}
	for id, connection := range s.clientConnections {
		if connection.UserID == value.UserID && !connection.ExpiresAt.After(now) {
			delete(s.clientConnections, id)
		}
	}
	value.ExpiresAt = value.ExpiresAt.UTC()
	s.clientConnections[value.ID] = value
	wasConnected := user.ConnectedAt(now)
	if value.ExpiresAt.After(user.ConnectedUntil) {
		user.ConnectedUntil = value.ExpiresAt
		s.users[value.UserID] = user
	}
	if online.ID != "" && !wasConnected {
		s.outbox = append(s.outbox, online)
	}
	return nil
}

func (s *Store) RenewClientConnection(_ context.Context, workspace domain.WorkspaceID, userID domain.UserID, id domain.ClientConnectionID, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	connection, ok := s.clientConnections[id]
	if !ok || connection.WorkspaceID != workspace || connection.UserID != userID {
		return store.ErrNotFound
	}
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(connection.ExpiresAt) {
		return nil
	}
	connection.ExpiresAt = expiresAt
	s.clientConnections[id] = connection
	if user, ok := s.users[userID]; ok && expiresAt.After(user.ConnectedUntil) {
		user.ConnectedUntil = expiresAt
		s.users[userID] = user
	}
	return nil
}

func (s *Store) CloseClientConnection(_ context.Context, workspace domain.WorkspaceID, userID domain.UserID, id domain.ClientConnectionID, now time.Time, offline events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	connection, ok := s.clientConnections[id]
	if !ok || connection.WorkspaceID != workspace || connection.UserID != userID {
		return store.ErrNotFound
	}
	delete(s.clientConnections, id)
	wasConnected := user.ConnectedAt(now)
	var remaining time.Time
	for _, other := range s.clientConnections {
		if other.UserID == userID && other.ExpiresAt.After(now) && other.ExpiresAt.After(remaining) {
			remaining = other.ExpiresAt
		}
	}
	user.ConnectedUntil = remaining
	s.users[userID] = user
	if offline.ID != "" && wasConnected && remaining.IsZero() {
		s.outbox = append(s.outbox, offline)
	}
	return nil
}

func (s *Store) CountClientConnections(_ context.Context, workspace domain.WorkspaceID, userID domain.UserID, now time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, connection := range s.clientConnections {
		if connection.WorkspaceID == workspace && connection.UserID == userID && connection.ExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}
