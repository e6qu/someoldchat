package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// ScheduledStatusSource is a compare-and-set queue. A scheduled row's
// UpdatedAt is its revision: editing it after a worker reads the row prevents
// that worker from activating the obsolete version.
type ScheduledStatusSource interface {
	DueScheduledStatuses(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.ScheduledStatus, error)
	EarliestScheduledStatusStart(context.Context, domain.WorkspaceID) (time.Time, error)
	ActivateScheduledStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.ScheduledStatusID, time.Time, time.Time, events.Event) (bool, error)
}

type ScheduledStatusWorker struct {
	Source ScheduledStatusSource
	Limit  int
}

func NewScheduledStatusWorker(source ScheduledStatusSource, limit int) (ScheduledStatusWorker, error) {
	if source == nil || limit <= 0 {
		return ScheduledStatusWorker{}, errors.New("scheduled status worker requires a source and positive limit")
	}
	return ScheduledStatusWorker{Source: source, Limit: limit}, nil
}

func (w ScheduledStatusWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

func (w ScheduledStatusWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	now = now.UTC().Truncate(time.Second)
	values, err := w.Source.DueScheduledStatuses(ctx, workspaceID, now, w.Limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, value := range values {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		event, err := scheduledStatusEvent(value, now)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		changed, err := w.Source.ActivateScheduledStatus(ctx, value.WorkspaceID, value.UserID, value.ID, value.UpdatedAt, now, event)
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

func scheduledStatusEvent(value domain.ScheduledStatus, now time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	reason := "scheduled_status_started"
	if !value.EndsAt.After(now) {
		reason = "scheduled_status_missed"
	}
	return events.New(id, value.WorkspaceID, value.UserID, events.NewPayload(
		"user.profile_changed",
		events.String("user_id", string(value.UserID)),
		events.String("scheduled_status_id", string(value.ID)),
		events.String("reason", reason),
	), now)
}
