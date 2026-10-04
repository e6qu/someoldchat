package service

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// sidebarActivityLimit bounds one SidebarActivity call, as a sidebar's own
// conversation listing is bounded.
const sidebarActivityLimit = 1000

// SidebarActivity reports, for each named conversation the member belongs to,
// when its newest message was posted and the member's notification
// preferences for it: what the sidebar needs to sort by recent activity and
// mark muted rows, in one call rather than one read per conversation. A name
// the member does not belong to is left out rather than refused, so a stale
// row does not fail the whole sidebar and the answer never describes a
// conversation the member cannot read.
func (m Messages) SidebarActivity(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversations []domain.ConversationID) (map[domain.ConversationID]domain.ConversationActivity, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if len(conversations) > sidebarActivityLimit {
		return nil, store.InvalidArgument("too many conversations for one sidebar")
	}
	readable := make([]domain.ConversationID, 0, len(conversations))
	seen := make(map[domain.ConversationID]bool, len(conversations))
	for _, conversation := range conversations {
		if seen[conversation] {
			continue
		}
		seen[conversation] = true
		if err := m.requireConversationMembership(ctx, workspaceID, userID, conversation); err != nil {
			if unreadableSidebarRow(err) {
				continue
			}
			return nil, err
		}
		readable = append(readable, conversation)
	}
	activity := make(map[domain.ConversationID]domain.ConversationActivity, len(readable))
	if len(readable) == 0 {
		return activity, nil
	}
	latest, err := m.Store.LatestMessageTimestamps(ctx, workspaceID, readable)
	if err != nil {
		return nil, err
	}
	overrides, err := m.Store.ConversationNotificationOverrides(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	for _, conversation := range readable {
		entry := domain.ConversationActivity{Notifications: domain.DefaultConversationNotificationPreferences(workspaceID, userID, conversation)}
		if preferences, ok := overrides[conversation]; ok {
			entry.Notifications = preferences
		}
		if timestamp, ok := latest[conversation]; ok {
			if at, parseErr := domain.ParseMessageTimestamp(timestamp); parseErr == nil {
				entry.LatestAt = at
			}
		}
		activity[conversation] = entry
	}
	return activity, nil
}

// unreadableSidebarRow reports whether a membership check failed because the
// row names a conversation that is gone or that the member left, which the
// sidebar skips, rather than because the read itself failed.
func unreadableSidebarRow(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, domain.ErrNotInConversation)
}
