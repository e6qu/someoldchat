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
	// GetUser exists because the profile-change event this worker emits
	// carries the user object, and only the store holds it.
	GetUser(context.Context, domain.UserID) (domain.User, error)
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
		user, err := w.Source.GetUser(ctx, value.UserID)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		event, err := scheduledStatusEvent(value, user, now)
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

func scheduledStatusEvent(value domain.ScheduledStatus, user domain.User, now time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	// A started status snapshots the state activation produces; a missed one
	// changed nothing, so the current user rides unmodified and the event
	// records only that the schedule came and went.
	reason := "scheduled_status_started"
	statusChanged := true
	if !value.EndsAt.After(now) {
		reason = "scheduled_status_missed"
		statusChanged = false
	} else {
		user.Profile.StatusText = value.StatusText
		user.Profile.StatusEmoji = value.StatusEmoji
		user.Profile.StatusExpiration = value.EndsAt
		user.Profile.ActiveScheduledStatusID = value.ID
	}
	payload, err := events.UserChangePayload("user.profile_changed", user, false, statusChanged, now,
		events.String("scheduled_status_id", string(value.ID)),
		events.String("reason", reason))
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, value.WorkspaceID, value.UserID, payload, now)
}
