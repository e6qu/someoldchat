package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
)

// ReminderDeliverySource is the compare-and-set queue behind reminders.add.
//
// A reminder was a durable row that nothing ever read: reminders.add wrote it,
// reminders.list showed it, and the member was never reminded of anything. The
// claim is the mark, so two workers reading the same batch deliver each
// reminder once between them and neither needs a lease.
type ReminderDeliverySource interface {
	DueReminders(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.Reminder, error)
	EarliestReminder(context.Context, domain.WorkspaceID) (time.Time, error)
	MarkReminderDelivered(context.Context, domain.WorkspaceID, domain.ReminderID, time.Time, time.Time, events.Event) (bool, error)
}

// ReminderDeliveryWorker delivers reminders.add reminders as Slack does: Slackbot
// posts the reminder into the member's Slackbot DM.
type ReminderDeliveryWorker struct {
	Source ReminderDeliverySource
	Poster chatapi.Service
	Limit  int
}

func NewReminderDeliveryWorker(source ReminderDeliverySource, poster chatapi.Service, limit int) (ReminderDeliveryWorker, error) {
	if source == nil || poster == nil || limit <= 0 {
		return ReminderDeliveryWorker{}, errors.New("reminder delivery worker requires a source, poster and positive limit")
	}
	return ReminderDeliveryWorker{Source: source, Poster: poster, Limit: limit}, nil
}

func (w ReminderDeliveryWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

func (w ReminderDeliveryWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	now = now.UTC().Truncate(time.Second)
	values, err := w.Source.DueReminders(ctx, workspaceID, now, w.Limit)
	if err != nil {
		return 0, err
	}
	delivered := 0
	var failures error
	for _, reminder := range values {
		if err := ctx.Err(); err != nil {
			return delivered, errors.Join(failures, err)
		}
		event, err := reminderDeliveredEvent(reminder, now)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		// A recurring reminder moves to its next occurrence rather than being
		// retired; it used to be delivered once and never again.
		next, err := nextRecurrence(reminder.Recurrence, reminder.Weekdays, reminder.TimeZone, reminder.RecurrenceAnchor, reminder.Time, now)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		// The message is posted before the claim, under a key naming this
		// occurrence, so a worker that dies between the two posts it once
		// when the reminder is delivered again. A member who can no longer
		// receive it - deactivated since - is past reminding: the occurrence
		// is still claimed rather than retried forever.
		if _, err := w.Poster.PostAsSlackbot(ctx, reminder.WorkspaceID, reminder.User, domain.SlackbotPost{
			Text:           ReminderText(reminder.Text),
			IdempotencyKey: fmt.Sprintf("reminder:%s:%d", reminder.ID, reminder.Time.UTC().Unix()),
		}); err != nil && permanentFailureCode(err) == "" {
			failures = errors.Join(failures, err)
			continue
		}
		claimed, err := w.Source.MarkReminderDelivered(ctx, reminder.WorkspaceID, reminder.ID, now, next, event)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if claimed {
			delivered++
		}
	}
	return delivered, failures
}

// reminderDeliveredEvent carries who is being reminded and of what. The member
// the reminder is for is the audience, not whoever set it: Slack lets one
// member remind another, and the notice belongs to the person reminded.
func reminderDeliveredEvent(reminder domain.Reminder, now time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, reminder.WorkspaceID, reminder.Creator, events.NewPayload("reminder.delivered",
		events.String("reminder_id", string(reminder.ID)),
		events.String("user_id", string(reminder.User)),
		events.String("text", reminder.Text),
		events.String("due_at", reminder.Time.UTC().Format(time.RFC3339)),
	), now)
}

// ReminderText is the message Slackbot posts for a reminder: "Reminder: " and
// the reminder's text, ending as a sentence.
func ReminderText(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return "Reminder: " + text
}
