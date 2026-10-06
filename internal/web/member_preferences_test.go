package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// A preference the page's script sends is kept for the member, and every
// page carries what the member keeps, so a new browser takes it on load. A
// malformed one is refused without being kept.
func TestPreferencesAreKeptForTheMember(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	for _, preference := range []url.Values{
		{"_csrf": {csrf}, "name": {"theme"}, "value": {"dark"}},
		{"_csrf": {csrf}, "name": {"section-sort:starred"}, "value": {"recent"}},
	} {
		if kept := postForm(t, mux, "/app/preferences", preference.Encode(), false); kept.Code != http.StatusNoContent {
			t.Fatalf("keep %v=%d: %s", preference, kept.Code, kept.Body)
		}
	}
	if refused := postForm(t, mux, "/app/preferences", url.Values{"_csrf": {csrf}, "name": {"Not A Name"}, "value": {"x"}}.Encode(), false); refused.Code != http.StatusBadRequest {
		t.Fatalf("a malformed preference=%d", refused.Code)
	}
	kept, err := s.MemberPreferences(context.Background(), "T1", "U1")
	if err != nil || len(kept) != 2 || kept["theme"] != "dark" {
		t.Fatalf("kept=%v err=%v", kept, err)
	}
	requireContains(t, "the page", get(t, mux, "/app?channel=Cdev").Body.String(),
		`data-preferences="{&#34;section-sort:starred&#34;:&#34;recent&#34;,&#34;theme&#34;:&#34;dark&#34;}" data-preferences-csrf="`+csrf+`"`)
}

// REMIND-02: Preferences' "Set a default time for reminder notifications"
// moves where a reminder set for a day lands, in /remind and in To-dos'
// suggested times; a value that is not a time of day is refused, not kept.
func TestDefaultReminderTimeMovesRemindersSetForADay(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	requireContains(t, "Preferences", get(t, mux, "/app/preferences?channel=Cdev").Body.String(),
		"Set a default time for reminder notifications", `data-preference="reminder-default-time" data-default="09:00"`, `<option value="07:30">7:30 AM</option>`, `<option value="00:00">Midnight</option>`)
	if refused := postForm(t, mux, "/app/preferences", url.Values{"_csrf": {csrf}, "name": {"reminder-default-time"}, "value": {"9am"}}.Encode(), false); refused.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable reminder time=%d", refused.Code)
	}
	if kept := postForm(t, mux, "/app/preferences", url.Values{"_csrf": {csrf}, "name": {"reminder-default-time"}, "value": {"07:30"}}.Encode(), false); kept.Code != http.StatusNoContent {
		t.Fatalf("keep the reminder time=%d: %s", kept.Code, kept.Body)
	}
	chat := service.Messages{Store: s}

	remind := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {csrf}, "text": {"/remind #general deploy tomorrow"}, "timezone": {"Europe/Bucharest"},
	}.Encode(), false)
	if remind.Code != http.StatusSeeOther {
		t.Fatalf("/remind status=%d body=%s", remind.Code, remind.Body)
	}
	bucharest, _ := time.LoadLocation("Europe/Bucharest")
	page, err := chat.ChannelReminders(context.Background(), "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("channel reminders=%+v err=%v", page, err)
	}
	if due := page.Items[0].Reminder.DueAt.In(bucharest); due.Hour() != 7 || due.Minute() != 30 {
		t.Fatalf("/remind tomorrow is due %s, want 7:30 in the member's zone", due)
	}

	created := postForm(t, mux, "/app/todos/create?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {csrf}, "title": {"stretch"}, "preset": {"tomorrow"}, "timezone": {"UTC"},
	}.Encode(), false)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("to-do status=%d body=%s", created.Code, created.Body)
	}
	todos, err := chat.Todos(context.Background(), "T1", "U1", domain.TodoQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(todos.Items) != 1 {
		t.Fatalf("to-dos=%+v err=%v", todos, err)
	}
	if due := todos.Items[0].Reminder.DueAt.UTC(); due.Hour() != 7 || due.Minute() != 30 {
		t.Fatalf("a to-do reminded tomorrow is due %s, want 7:30", due)
	}
}
