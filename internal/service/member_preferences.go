package service

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// MemberPreferences is the caller's own preferences, which follow them to
// every client.
func (m Messages) MemberPreferences(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (map[string]string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.MemberPreferences(ctx, workspaceID, userID)
}

// SetMemberPreference keeps one of the caller's preferences; an empty value
// removes it.
func (m Messages) SetMemberPreference(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, value string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	name, value, err := domain.NormalizeMemberPreference(name, value)
	if err != nil {
		return err
	}
	return m.Store.SetMemberPreference(ctx, workspaceID, userID, name, value, time.Now().UTC())
}
