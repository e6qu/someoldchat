package web

import (
	"context"
	"fmt"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// reminderClock is the time of day the member's reminders set for a day are
// delivered at: Preferences' "Set a default time for reminder notifications",
// 9:00 AM unless they changed it. A store that cannot answer is an error, not
// a silent 9:00 AM the member did not choose.
func (h Handler) reminderClock(ctx context.Context, principal auth.Principal) (domain.ReminderClock, error) {
	preferences, err := h.Messages.MemberPreferences(ctx, principal.WorkspaceID, principal.UserID)
	if err != nil {
		return domain.ReminderClock{}, err
	}
	return domain.ReminderDefaultClock(preferences), nil
}

type reminderTimeOption struct {
	Value string
	Label string
}

// reminderTimeOptions offers every half hour of the day. Slack's help names
// the drop-down but not its entries, so the half-hour step is our choice.
func reminderTimeOptions() []reminderTimeOption {
	options := make([]reminderTimeOption, 0, 48)
	for minutes := 0; minutes < 24*60; minutes += 30 {
		clock := domain.ReminderClock{Hour: minutes / 60, Minute: minutes % 60}
		hour := clock.Hour % 12
		if hour == 0 {
			hour = 12
		}
		meridiem := "AM"
		if clock.Hour >= 12 {
			meridiem = "PM"
		}
		label := fmt.Sprintf("%d:%02d %s", hour, clock.Minute, meridiem)
		switch minutes {
		case 0:
			label = "Midnight"
		case 12 * 60:
			label = "Noon"
		}
		options = append(options, reminderTimeOption{Value: clock.String(), Label: label})
	}
	return options
}
