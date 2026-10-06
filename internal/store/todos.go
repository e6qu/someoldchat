package store

import "github.com/sameoldchat/sameoldchat/internal/domain"

// TodoPageOf trims an ordered read of up to limit+1 to-dos to a page whose
// cursor names its last to-do under the query's sort. Every storage profile
// pages To-dos through it, so they agree on where a page ends.
func TodoPageOf(items []domain.Todo, query domain.TodoQuery) (domain.TodoPage, error) {
	page := domain.TodoPage{Items: items, HasMore: len(items) > query.Page.Limit}
	if !page.HasMore {
		return page, nil
	}
	page.Items = items[:query.Page.Limit]
	var err error
	page.NextCursor, err = domain.NewTodoCursor(page.Items[len(page.Items)-1], query.Sort)
	return page, err
}

// SameReminderTiming reports whether an edit leaves a to-do's reminder as it
// was, at the whole-second precision every profile stores. Only a changed
// reminder starts its delivery afresh.
func SameReminderTiming(left, right domain.ReminderTiming) bool {
	return left.DueAt.Unix() == right.DueAt.Unix() && left.DueAt.IsZero() == right.DueAt.IsZero() &&
		left.TimeZone == right.TimeZone && left.Recurrence == right.Recurrence &&
		left.RecurrenceAnchor.Unix() == right.RecurrenceAnchor.Unix() && left.RecurrenceAnchor.IsZero() == right.RecurrenceAnchor.IsZero()
}
