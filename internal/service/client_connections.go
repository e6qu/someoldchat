package service

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// A member's open clients — the web client's event stream and RTM sockets —
// are what make them online, as on Slack: presence is active only while at
// least one is connected. Each stream opens a lease, renews it while it is
// open and closes it when it ends.

// OpenClientConnection records a client the member opened. If it brings them
// online, the journal announces the change of presence.
func (m Messages) OpenClientConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.ClientConnection, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ClientConnection{}, err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return domain.ClientConnection{}, err
	}
	id, err := domain.NewClientConnectionID()
	if err != nil {
		return domain.ClientConnection{}, err
	}
	now := time.Now().UTC()
	value := domain.ClientConnection{ID: id, WorkspaceID: workspaceID, UserID: userID, ExpiresAt: now.Add(domain.ClientConnectionLease)}
	connected := user
	connected.ConnectedUntil = value.ExpiresAt
	online, err := m.presenceChange(workspaceID, user, connected, now)
	if err != nil {
		return domain.ClientConnection{}, err
	}
	if err := m.Store.OpenClientConnection(ctx, value, now, online); err != nil {
		return domain.ClientConnection{}, err
	}
	return value, nil
}

// RenewClientConnection extends the lease of a client that is still open.
func (m Messages) RenewClientConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ClientConnectionID) (domain.ClientConnection, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ClientConnection{}, err
	}
	value := domain.ClientConnection{ID: id, WorkspaceID: workspaceID, UserID: userID, ExpiresAt: time.Now().UTC().Add(domain.ClientConnectionLease)}
	if err := m.Store.RenewClientConnection(ctx, workspaceID, userID, id, value.ExpiresAt); err != nil {
		return domain.ClientConnection{}, err
	}
	return value, nil
}

// CloseClientConnection records that a client ended. If it was the member's
// last, they go offline and the journal announces it.
func (m Messages) CloseClientConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ClientConnectionID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	disconnected := user
	disconnected.ConnectedUntil = time.Time{}
	offline, err := m.presenceChange(workspaceID, user, disconnected, now)
	if err != nil {
		return err
	}
	return m.Store.CloseClientConnection(ctx, workspaceID, userID, id, now, offline)
}

// ClientConnectionCount is how many of the caller's own clients are open,
// users.getPresence's connection_count.
func (m Messages) ClientConnectionCount(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (int, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return 0, err
	}
	return m.Store.CountClientConnections(ctx, workspaceID, userID, time.Now().UTC())
}

// presenceChange is the presence_change a change to the member makes, or no
// event when their presence stays as it was.
func (m Messages) presenceChange(workspaceID domain.WorkspaceID, before, after domain.User, now time.Time) (events.Event, error) {
	presence := after.PresenceAt(now)
	if presence == before.PresenceAt(now) {
		return events.Event{}, nil
	}
	return newEvent(workspaceID, after.ID, events.NewPayload("user.presence_changed",
		events.String("user_id", string(after.ID)), events.String("presence", presence)), now)
}
