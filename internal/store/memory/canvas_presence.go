package memory

import (
	"context"
	"sort"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func canvasPresenceKey(canvas domain.CanvasID, user domain.UserID, session string) string {
	return string(canvas) + "\x00" + string(user) + "\x00" + session
}

// RecordCanvasPresence mirrors the SQL profile: one row per page, replaced,
// with lapsed rows dropped on write.
func (s *Store) RecordCanvasPresence(_ context.Context, presence domain.CanvasPresence, now time.Time) error {
	if !presence.Valid() {
		return store.InvalidArgument("canvas presence requires a workspace, canvas, member, session and expiry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, existing := range s.canvasPresence {
		if !existing.ExpiresAt.After(now) {
			delete(s.canvasPresence, key)
		}
	}
	presence.Name = ""
	s.canvasPresence[canvasPresenceKey(presence.CanvasID, presence.UserID, presence.Session)] = presence
	return nil
}

func (s *Store) ClearCanvasPresence(_ context.Context, workspace domain.WorkspaceID, canvas domain.CanvasID, user domain.UserID, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := canvasPresenceKey(canvas, user, session)
	if existing, ok := s.canvasPresence[key]; ok && existing.WorkspaceID == workspace {
		delete(s.canvasPresence, key)
	}
	return nil
}

func (s *Store) ListCanvasPresence(_ context.Context, workspace domain.WorkspaceID, canvas domain.CanvasID, now time.Time) ([]domain.CanvasPresence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	present := make([]domain.CanvasPresence, 0, 4)
	for _, value := range s.canvasPresence {
		if value.WorkspaceID == workspace && value.CanvasID == canvas && value.ExpiresAt.After(now) {
			present = append(present, value)
		}
	}
	sort.Slice(present, func(i, j int) bool {
		if present[i].UserID != present[j].UserID {
			return present[i].UserID < present[j].UserID
		}
		return present[i].Session < present[j].Session
	})
	return present, nil
}
