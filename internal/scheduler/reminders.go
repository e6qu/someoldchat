package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/lease"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ReminderSource is the durable execution boundary for first-party Later
// reminders. It is separate from Source because reminder delivery and scheduled
// messages have independent state machines and must not silently acquire one
// another's storage semantics.
type ReminderSource interface {
	EarliestLaterReminder(context.Context, domain.WorkspaceID) (time.Time, error)
	ClaimDueLaterReminders(context.Context, domain.WorkspaceID, string, int, time.Duration, time.Time) ([]domain.LaterReminder, error)
	RenewLaterReminder(context.Context, string, domain.LaterReminderID, time.Duration, time.Time) error
	MarkLaterReminderDelivered(context.Context, string, domain.LaterReminderID, time.Time, time.Time, events.Event) error
	MarkLaterReminderFailed(context.Context, string, domain.LaterReminderID, string, time.Time, events.Event) error
	ReleaseLaterReminder(context.Context, string, domain.LaterReminderID, time.Time, time.Time) error
}

type ReminderWorker struct {
	Source ReminderSource
	Poster chatapi.Service
	Owner  string
	Limit  int
	Lease  time.Duration
	Clock  func() time.Time
}

func NewReminderWorker(source ReminderSource, poster chatapi.Service, owner string, limit int, leaseDuration time.Duration, clock func() time.Time) (ReminderWorker, error) {
	if source == nil || poster == nil || owner == "" || limit <= 0 || leaseDuration <= 0 {
		return ReminderWorker{}, errors.New("reminder worker requires source, poster, owner, positive limit, and lease")
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return ReminderWorker{Source: source, Poster: poster, Owner: owner, Limit: limit, Lease: leaseDuration, Clock: clock}, nil
}

// RunOnce delivers one bounded batch. A personal reminder is delivered as a
// private Activity/Later event; a channel reminder also posts one idempotent
// message to the target conversation.
func (w ReminderWorker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	now := w.now()
	items, err := w.Source.ClaimDueLaterReminders(ctx, workspace, w.Owner, w.Limit, w.Lease, now)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, reminder := range items {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		if reminder.Target == domain.LaterReminderChannel {
			if postErr := w.postChannelReminder(ctx, reminder); postErr != nil {
				failedAt := w.now()
				if failureCode := permanentFailureCode(postErr); failureCode != "" {
					event, eventErr := laterReminderEvent(reminder, "later_reminder.failed", failedAt, time.Time{}, failureCode)
					if eventErr != nil {
						failures = errors.Join(failures, eventErr)
					} else if markErr := w.Source.MarkLaterReminderFailed(ctx, w.Owner, reminder.ID, failureCode, failedAt, event); markErr != nil {
						failures = errors.Join(failures, markErr)
					} else {
						completed++
						continue
					}
				} else {
					failures = errors.Join(failures, postErr)
				}
				if releaseErr := w.Source.ReleaseLaterReminder(ctx, w.Owner, reminder.ID, failedAt.Add(w.Lease), failedAt); releaseErr != nil {
					failures = errors.Join(failures, releaseErr)
				}
				continue
			}
		}

		deliveredAt := w.now()
		nextDue, recurrenceErr := NextReminderDue(reminder, deliveredAt)
		if recurrenceErr != nil {
			event, eventErr := laterReminderEvent(reminder, "later_reminder.failed", deliveredAt, time.Time{}, "invalid_timezone")
			if eventErr != nil {
				failures = errors.Join(failures, recurrenceErr, eventErr)
			} else if markErr := w.Source.MarkLaterReminderFailed(ctx, w.Owner, reminder.ID, "invalid_timezone", deliveredAt, event); markErr != nil {
				failures = errors.Join(failures, recurrenceErr, markErr)
			} else {
				completed++
			}
			continue
		}
		event, eventErr := laterReminderEvent(reminder, "later_reminder.delivered", deliveredAt, nextDue, "")
		if eventErr != nil {
			failures = errors.Join(failures, eventErr)
			if releaseErr := w.Source.ReleaseLaterReminder(ctx, w.Owner, reminder.ID, deliveredAt.Add(w.Lease), deliveredAt); releaseErr != nil {
				failures = errors.Join(failures, releaseErr)
			}
			continue
		}
		if markErr := w.Source.MarkLaterReminderDelivered(ctx, w.Owner, reminder.ID, deliveredAt, nextDue, event); markErr != nil {
			failures = errors.Join(failures, markErr)
			continue
		}
		completed++
	}
	return completed, failures
}

func (w ReminderWorker) now() time.Time {
	return w.Clock().UTC()
}

func (w ReminderWorker) postChannelReminder(ctx context.Context, reminder domain.LaterReminder) error {
	return lease.While(ctx, w.Lease,
		func(renewContext context.Context) error {
			return w.Source.RenewLaterReminder(renewContext, w.Owner, reminder.ID, w.Lease, w.now())
		},
		func(postContext context.Context) error {
			idempotencyKey := fmt.Sprintf("later-reminder:%s:%d", reminder.ID, reminder.DueAt.UTC().Unix())
			_, err := w.Poster.PostWithBlocksAndAttachments(
				postContext, reminder.WorkspaceID, reminder.Creator, reminder.Channel,
				"Reminder: "+reminder.Text, "", "", "", idempotencyKey, "",
			)
			return err
		},
	)
}

// NextReminderDue returns the first recurrence after the delivery instant while
// preserving the reminder's local wall-clock time across daylight-saving
// changes. One-time reminders return the zero time.
func NextReminderDue(reminder domain.LaterReminder, after time.Time) (time.Time, error) {
	if reminder.Recurrence == domain.ReminderOnce {
		return time.Time{}, nil
	}
	location, err := time.LoadLocation(reminder.TimeZone)
	if err != nil {
		return time.Time{}, err
	}
	next := reminder.DueAt.In(location)
	advance := func(value time.Time) time.Time {
		switch reminder.Recurrence {
		case domain.ReminderDaily:
			return value.AddDate(0, 0, 1)
		case domain.ReminderWeekly:
			return value.AddDate(0, 0, 7)
		case domain.ReminderMonthly:
			return value.AddDate(0, 1, 0)
		case domain.ReminderYearly:
			return value.AddDate(1, 0, 0)
		default:
			return time.Time{}
		}
	}
	for !next.After(after.In(location)) {
		next = advance(next)
		if next.IsZero() {
			return time.Time{}, store.InvalidArgument("Later reminder recurrence is invalid")
		}
	}
	return next.UTC(), nil
}

func laterReminderEvent(reminder domain.LaterReminder, topic string, at, nextDue time.Time, failureCode string) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	fields := []events.Field{
		events.String("reminder_id", string(reminder.ID)),
		events.String("target", string(reminder.Target)),
		events.String("text", reminder.Text),
		events.String("due_at", reminder.DueAt.UTC().Format(time.RFC3339)),
	}
	if reminder.UserID != "" {
		fields = append(fields, events.String("user_id", string(reminder.UserID)))
	}
	if reminder.Channel != "" {
		fields = append(fields, events.String("channel_id", string(reminder.Channel)))
	}
	if !nextDue.IsZero() {
		fields = append(fields, events.String("next_due_at", nextDue.UTC().Format(time.RFC3339)))
	}
	if failureCode != "" {
		fields = append(fields, events.String("failure_code", failureCode))
	}
	return events.New(id, reminder.WorkspaceID, reminder.Creator, events.NewPayload(topic, fields...), at)
}
