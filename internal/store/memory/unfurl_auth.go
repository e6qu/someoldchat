package memory

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func unfurlAuthKey(workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) string {
	return string(workspaceID) + "\x00" + string(userID) + "\x00" + string(appID)
}

func (s *Store) DeclineUnfurlAuth(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, _ time.Time) error {
	if workspaceID == "" || userID == "" || appID == "" {
		return store.InvalidArgument("an unfurl authentication decline names a workspace, a member and an app")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unfurlAuthDeclines[unfurlAuthKey(workspaceID, userID, appID)] = struct{}{}
	return nil
}

func (s *Store) UnfurlAuthDeclined(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, declined := s.unfurlAuthDeclines[unfurlAuthKey(workspaceID, userID, appID)]
	return declined, nil
}
