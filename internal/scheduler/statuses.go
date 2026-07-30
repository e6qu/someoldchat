package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// StatusSource is the durable compare-and-set queue behind custom-status
// expiration. It deliberately has no lease: two workers may read the same due
// status, while ExpireUserStatus lets exactly one clear the matching deadline
// and append its profile-change event.
type StatusSource interface {
	DueUserStatuses(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.User, error)
	EarliestUserStatusExpiration(context.Context, domain.WorkspaceID) (time.Time, error)
	ExpireUserStatus(context.Context, domain.WorkspaceID, domain.UserID, time.Time, time.Time, events.Event) (bool, error)
}

type StatusWorker struct {
	Source StatusSource
	Limit  int
}

func NewStatusWorker(source StatusSource, limit int) (StatusWorker, error) {
	if source == nil || limit <= 0 {
		return StatusWorker{}, errors.New("status worker requires a source and positive limit")
	}
	return StatusWorker{Source: source, Limit: limit}, nil
}

func (w StatusWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

func (w StatusWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	now = now.UTC().Truncate(time.Second)
	users, err := w.Source.DueUserStatuses(ctx, workspaceID, now, w.Limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, user := range users {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		event, err := statusExpiredEvent(user, now)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		changed, err := w.Source.ExpireUserStatus(ctx, user.WorkspaceID, user.ID, user.Profile.StatusExpiration, now, event)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if changed {
			completed++
		}
	}
	return completed, failures
}

func statusExpiredEvent(user domain.User, now time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, user.WorkspaceID, user.ID, events.NewPayload(
		"user.profile_changed",
		events.String("user_id", string(user.ID)),
		events.String("reason", "status_expired"),
	), now)
}
