package realtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// ConnectionTracker records each open stream as one of its member's client
// connections, which is what makes them online: Slack reports a member active
// only while at least one of their clients is connected.
type ConnectionTracker interface {
	OpenClientConnection(context.Context, domain.WorkspaceID, domain.UserID) (domain.ClientConnection, error)
	RenewClientConnection(context.Context, domain.WorkspaceID, domain.UserID, domain.ClientConnectionID) (domain.ClientConnection, error)
	CloseClientConnection(context.Context, domain.WorkspaceID, domain.UserID, domain.ClientConnectionID) error
}

// connectionCloseTimeout bounds the close a stream sends as it ends, after its
// request context is already done.
const connectionCloseTimeout = 5 * time.Second

// connectionLease is one stream's connection, renewed while the stream runs.
type connectionLease struct {
	tracker   ConnectionTracker
	logger    *slog.Logger
	value     domain.ClientConnection
	renewedAt time.Time
}

func openConnectionLease(ctx context.Context, tracker ConnectionTracker, logger *slog.Logger, workspace domain.WorkspaceID, user domain.UserID) (*connectionLease, error) {
	value, err := tracker.OpenClientConnection(ctx, workspace, user)
	if err != nil {
		return nil, err
	}
	return &connectionLease{tracker: tracker, logger: logger, value: value, renewedAt: time.Now()}, nil
}

// renewIfDue renews the lease once a renewal period has passed. A failed
// renewal is retried a period later; the lease outlasts two failures before
// the member appears to have gone.
func (l *connectionLease) renewIfDue(ctx context.Context) {
	if time.Since(l.renewedAt) < domain.ClientConnectionRenewal {
		return
	}
	l.renewedAt = time.Now()
	value, err := l.tracker.RenewClientConnection(ctx, l.value.WorkspaceID, l.value.UserID, l.value.ID)
	if err != nil {
		if ctx.Err() == nil {
			l.logger.Warn("stream could not renew its client connection", "workspace", l.value.WorkspaceID, "user", l.value.UserID, "connection", l.value.ID, "error", err)
		}
		return
	}
	l.value = value
}

// close ends the connection as its stream ends. The request context is done
// by then, so the close runs on its own bounded context.
func (l *connectionLease) close(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectionCloseTimeout)
	defer cancel()
	if err := l.tracker.CloseClientConnection(ctx, l.value.WorkspaceID, l.value.UserID, l.value.ID); err != nil {
		l.logger.Warn("stream could not close its client connection; it lapses with its lease", "workspace", l.value.WorkspaceID, "user", l.value.UserID, "connection", l.value.ID, "error", err)
	}
}
