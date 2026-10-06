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

// ReminderSource is the durable execution boundary for first-party reminders:
// a to-do's reminder and a /remind channel reminder. It is separate from
// Source because reminder delivery and scheduled messages have independent
// state machines and must not silently acquire one another's storage
// semantics. The two kinds share the lease protocol and nothing else: a to-do
// is never posted anywhere, and a channel reminder never reaches To-dos or
// Activity.
type ReminderSource interface {
	EarliestTodoReminder(context.Context, domain.WorkspaceID) (time.Time, error)
	ClaimDueTodoReminders(context.Context, domain.WorkspaceID, string, int, time.Duration, time.Time) ([]domain.Todo, error)
	RenewTodoReminder(context.Context, string, domain.TodoID, time.Duration, time.Time) error
	MarkTodoReminderDelivered(context.Context, string, domain.TodoID, time.Time, time.Time, events.Event) error
	MarkTodoReminderFailed(context.Context, string, domain.TodoID, string, time.Time, events.Event) error
	ReleaseTodoReminder(context.Context, string, domain.TodoID, time.Time, time.Time) error
	EarliestChannelReminder(context.Context, domain.WorkspaceID) (time.Time, error)
	ClaimDueChannelReminders(context.Context, domain.WorkspaceID, string, int, time.Duration, time.Time) ([]domain.ChannelReminder, error)
	RenewChannelReminder(context.Context, string, domain.ChannelReminderID, time.Duration, time.Time) error
	MarkChannelReminderDelivered(context.Context, string, domain.ChannelReminderID, time.Time, time.Time, events.Event) error
	MarkChannelReminderFailed(context.Context, string, domain.ChannelReminderID, string, time.Time, events.Event) error
	ReleaseChannelReminder(context.Context, string, domain.ChannelReminderID, time.Time, time.Time) error
}

// earliestFirstPartyReminder is when the next to-do or channel reminder comes
// due, for the lifecycle wake hint.
func earliestFirstPartyReminder(ctx context.Context, source ReminderSource, workspace domain.WorkspaceID) (time.Time, error) {
	todoAt, err := source.EarliestTodoReminder(ctx, workspace)
	if err != nil {
		return time.Time{}, err
	}
	channelAt, err := source.EarliestChannelReminder(ctx, workspace)
	if err != nil {
		return time.Time{}, err
	}
	if todoAt.IsZero() || (!channelAt.IsZero() && channelAt.Before(todoAt)) {
		return channelAt, nil
	}
	return todoAt, nil
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

// RunOnce delivers one bounded batch of each kind. A channel reminder has
// Slackbot post one idempotent message to the target conversation; a to-do's
// reminder is recorded as delivered, which files its Activity row and lights
// the To-dos and Activity badges ("you'll receive a notification and see a
// badge on the To-dos and Activity tabs when the date and time arrives").
// Neither marks anything done: a to-do whose reminder fired is overdue.
func (w ReminderWorker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	channels, channelErr := w.runChannelReminders(ctx, workspace)
	todos, todoErr := w.runTodoReminders(ctx, workspace)
	return channels + todos, errors.Join(channelErr, todoErr)
}

func (w ReminderWorker) runChannelReminders(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	now := w.now()
	items, err := w.Source.ClaimDueChannelReminders(ctx, workspace, w.Owner, w.Limit, w.Lease, now)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, reminder := range items {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		if postErr := w.postChannelReminder(ctx, reminder); postErr != nil {
			failedAt := w.now()
			if failureCode := permanentFailureCode(postErr); failureCode != "" {
				event, eventErr := channelReminderEvent(reminder, "channel_reminder.failed", failedAt, time.Time{}, failureCode)
				if eventErr != nil {
					failures = errors.Join(failures, eventErr)
				} else if markErr := w.Source.MarkChannelReminderFailed(ctx, w.Owner, reminder.ID, failureCode, failedAt, event); markErr != nil {
					failures = errors.Join(failures, markErr)
				} else {
					completed++
					continue
				}
			} else {
				failures = errors.Join(failures, postErr)
			}
			if releaseErr := w.Source.ReleaseChannelReminder(ctx, w.Owner, reminder.ID, failedAt.Add(w.Lease), failedAt); releaseErr != nil {
				failures = errors.Join(failures, releaseErr)
			}
			continue
		}
		deliveredAt := w.now()
		nextDue, recurrenceErr := NextReminderDue(reminder.Reminder, deliveredAt)
		if recurrenceErr != nil {
			event, eventErr := channelReminderEvent(reminder, "channel_reminder.failed", deliveredAt, time.Time{}, "invalid_timezone")
			if eventErr != nil {
				failures = errors.Join(failures, recurrenceErr, eventErr)
			} else if markErr := w.Source.MarkChannelReminderFailed(ctx, w.Owner, reminder.ID, "invalid_timezone", deliveredAt, event); markErr != nil {
				failures = errors.Join(failures, recurrenceErr, markErr)
			} else {
				completed++
			}
			continue
		}
		event, eventErr := channelReminderEvent(reminder, "channel_reminder.delivered", deliveredAt, nextDue, "")
		if eventErr != nil {
			failures = errors.Join(failures, eventErr)
			if releaseErr := w.Source.ReleaseChannelReminder(ctx, w.Owner, reminder.ID, deliveredAt.Add(w.Lease), deliveredAt); releaseErr != nil {
				failures = errors.Join(failures, releaseErr)
			}
			continue
		}
		if markErr := w.Source.MarkChannelReminderDelivered(ctx, w.Owner, reminder.ID, deliveredAt, nextDue, event); markErr != nil {
			failures = errors.Join(failures, markErr)
			continue
		}
		completed++
	}
	return completed, failures
}

func (w ReminderWorker) runTodoReminders(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	now := w.now()
	items, err := w.Source.ClaimDueTodoReminders(ctx, workspace, w.Owner, w.Limit, w.Lease, now)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, todo := range items {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		deliveredAt := w.now()
		nextDue, recurrenceErr := NextReminderDue(todo.Reminder, deliveredAt)
		if recurrenceErr != nil {
			event, eventErr := todoReminderEvent(todo, "todo.reminder_failed", deliveredAt, time.Time{}, "invalid_timezone")
			if eventErr != nil {
				failures = errors.Join(failures, recurrenceErr, eventErr)
			} else if markErr := w.Source.MarkTodoReminderFailed(ctx, w.Owner, todo.ID, "invalid_timezone", deliveredAt, event); markErr != nil {
				failures = errors.Join(failures, recurrenceErr, markErr)
			} else {
				completed++
			}
			continue
		}
		event, eventErr := todoReminderEvent(todo, "todo.reminder_delivered", deliveredAt, nextDue, "")
		if eventErr != nil {
			failures = errors.Join(failures, eventErr)
			if releaseErr := w.Source.ReleaseTodoReminder(ctx, w.Owner, todo.ID, deliveredAt.Add(w.Lease), deliveredAt); releaseErr != nil {
				failures = errors.Join(failures, releaseErr)
			}
			continue
		}
		if markErr := w.Source.MarkTodoReminderDelivered(ctx, w.Owner, todo.ID, deliveredAt, nextDue, event); markErr != nil {
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

func (w ReminderWorker) postChannelReminder(ctx context.Context, reminder domain.ChannelReminder) error {
	return lease.While(ctx, w.Lease,
		func(renewContext context.Context) error {
			return w.Source.RenewChannelReminder(renewContext, w.Owner, reminder.ID, w.Lease, w.now())
		},
		func(postContext context.Context) error {
			// The key keeps the identifier schema 218 carried over from Later,
			// so a channel reminder posted just before the upgrade is not
			// posted again just after it.
			idempotencyKey := fmt.Sprintf("later-reminder:%s:%d", reminder.ID, reminder.Reminder.DueAt.UTC().Unix())
			// Slackbot posts a channel reminder, as on Slack, for the member who
			// set it; the member must still be able to post there.
			_, err := w.Poster.PostAsSlackbot(postContext, reminder.WorkspaceID, reminder.Creator, domain.SlackbotPost{
				Conversation: reminder.Channel,
				Text:         ReminderText(reminder.Text), IdempotencyKey: idempotencyKey,
			})
			return err
		},
	)
}

// NextReminderDue returns the first recurrence after the delivery instant while
// preserving the reminder's local wall-clock time across daylight-saving
// changes. One-time reminders return the zero time.
//
// Monthly and yearly recurrences are computed from the anchor's calendar
// position — its day-of-month, and for yearly its month — rather than by adding
// to the last DueAt. time.AddDate normalises an impossible date forward, so
// adding a month to January 31st yields March 3rd, skipping February and then
// permanently landing on the 3rd. Clamping the anchor day to each period's
// length instead keeps a "monthly on the 31st" reminder on the last day of the
// short months and back on the 31st when the month has one, which is how every
// calendar recurrence behaves. Daily and weekly have no month-length to clamp,
// so they still advance by a fixed span.
func NextReminderDue(timing domain.ReminderTiming, after time.Time) (time.Time, error) {
	return nextRecurrence(timing.Recurrence, nil, timing.TimeZone, timing.RecurrenceAnchor, timing.DueAt, after)
}

// nextRecurrence is NextReminderDue for any reminder: first-party reminders and the
// Web API's recurring reminders.add reminders recur the same way. weekdays are
// the days a weekly recurrence falls on; none means the anchor's day.
func nextRecurrence(recurrence domain.ReminderRecurrence, weekdays []time.Weekday, timeZone string, anchor, due, after time.Time) (time.Time, error) {
	if recurrence == domain.ReminderOnce {
		return time.Time{}, nil
	}
	if timeZone == "" {
		timeZone = "UTC"
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return time.Time{}, err
	}
	if anchor.IsZero() {
		// A reminder stored before the anchor existed carries none; its current
		// due instant is the best anchor available and is at least self-consistent.
		anchor = due
	}
	anchorLocal := anchor.In(location)
	afterLocal := after.In(location)
	next := due.In(location)
	for !next.After(afterLocal) {
		next = domain.AdvanceReminder(recurrence, weekdays, anchorLocal, next, location)
		if next.IsZero() {
			return time.Time{}, store.InvalidArgument("reminder recurrence is invalid")
		}
	}
	return next.UTC(), nil
}

func todoReminderEvent(todo domain.Todo, topic string, at, nextDue time.Time, failureCode string) (events.Event, error) {
	fields := []events.Field{
		events.String("todo_id", string(todo.ID)),
		events.String("user_id", string(todo.UserID)),
		events.String("text", todo.Title),
		events.String("due_at", todo.Reminder.DueAt.UTC().Format(time.RFC3339)),
	}
	return reminderEvent(todo.WorkspaceID, todo.UserID, topic, fields, at, nextDue, failureCode)
}

func channelReminderEvent(reminder domain.ChannelReminder, topic string, at, nextDue time.Time, failureCode string) (events.Event, error) {
	fields := []events.Field{
		events.String("reminder_id", string(reminder.ID)),
		events.String("user_id", string(reminder.Creator)),
		events.String("channel_id", string(reminder.Channel)),
		events.String("text", reminder.Text),
		events.String("due_at", reminder.Reminder.DueAt.UTC().Format(time.RFC3339)),
	}
	return reminderEvent(reminder.WorkspaceID, reminder.Creator, topic, fields, at, nextDue, failureCode)
}

func reminderEvent(workspace domain.WorkspaceID, actor domain.UserID, topic string, fields []events.Field, at, nextDue time.Time, failureCode string) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	if !nextDue.IsZero() {
		fields = append(fields, events.String("next_due_at", nextDue.UTC().Format(time.RFC3339)))
	}
	if failureCode != "" {
		fields = append(fields, events.String("failure_code", failureCode))
	}
	return events.New(id, workspace, actor, events.NewPayload(topic, fields...), at)
}
