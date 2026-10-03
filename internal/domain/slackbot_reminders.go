package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// A reminder Slackbot delivers to a member carries Slack's controls: Mark as
// complete, and Remind me about this, which sets the reminder again for a
// moment the member picks. The controls are Block Kit on Slackbot's message,
// and Slackbot itself answers them; no app is involved.

// The action IDs and block ID prefix of a reminder message's controls.
const (
	SlackbotReminderCompleteAction = "slackbot_reminder_complete"
	SlackbotReminderSnoozeAction   = "slackbot_reminder_snooze"
	slackbotReminderBlockPrefix    = "slackbot_reminder:"
)

// ReminderSnoozeOption is one choice of "Remind me about this".
type ReminderSnoozeOption struct {
	Value string
	Text  string
}

// ReminderSnoozeOptions are Slack's choices, in its order.
var ReminderSnoozeOptions = []ReminderSnoozeOption{
	{Value: "20m", Text: "In 20 minutes"},
	{Value: "1h", Text: "In 1 hour"},
	{Value: "3h", Text: "In 3 hours"},
	{Value: "tomorrow", Text: "Tomorrow"},
	{Value: "next_week", Text: "Next week"},
}

// ReminderSnoozeDue is when a snoozed reminder comes due again: after a span,
// or at 9:00 in the member's zone tomorrow or next Monday.
func ReminderSnoozeDue(choice string, now time.Time, zone *time.Location) (time.Time, bool) {
	if zone == nil {
		zone = time.UTC
	}
	local := now.In(zone)
	morning := func(days int) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day()+days, 9, 0, 0, 0, zone).UTC()
	}
	switch choice {
	case "20m":
		return now.Add(20 * time.Minute).UTC().Truncate(time.Second), true
	case "1h":
		return now.Add(time.Hour).UTC().Truncate(time.Second), true
	case "3h":
		return now.Add(3 * time.Hour).UTC().Truncate(time.Second), true
	case "tomorrow":
		return morning(1), true
	case "next_week":
		days := (int(time.Monday) - int(local.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
		return morning(days), true
	}
	return time.Time{}, false
}

// SlackbotReminderBlocks is the Block Kit of a delivered reminder: its text,
// then its controls. A recurring reminder is not completed from one
// occurrence, so it offers only "Remind me about this".
func SlackbotReminderBlocks(text string, reminder ReminderID, recurring bool) (string, error) {
	plain := func(value string) map[string]any { return map[string]any{"type": "plain_text", "text": value} }
	options := make([]any, 0, len(ReminderSnoozeOptions))
	for _, option := range ReminderSnoozeOptions {
		options = append(options, map[string]any{"text": plain(option.Text), "value": option.Value})
	}
	elements := []any{}
	if !recurring {
		elements = append(elements, map[string]any{"type": "button", "action_id": SlackbotReminderCompleteAction, "text": plain("Mark as complete"), "value": string(reminder)})
	}
	elements = append(elements, map[string]any{"type": "static_select", "action_id": SlackbotReminderSnoozeAction, "placeholder": plain("Remind me about this"), "options": options})
	encoded, err := json.Marshal([]any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		map[string]any{"type": "actions", "block_id": slackbotReminderBlockPrefix + string(reminder), "elements": elements},
	})
	return string(encoded), err
}

// SlackbotReminderOfBlock is the reminder a reminder message's control block
// names, if the block is one.
func SlackbotReminderOfBlock(blockID string) (ReminderID, bool) {
	id, found := strings.CutPrefix(blockID, slackbotReminderBlockPrefix)
	return ReminderID(id), found && id != ""
}
