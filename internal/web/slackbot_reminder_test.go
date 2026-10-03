package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// A reminder in the member's Slackbot DM shows Slack's controls, and choosing
// Mark as complete completes the reminder and replaces the controls.
func TestSlackbotReminderControlsWorkFromTheSlackbotDM(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateReminder(ctx, domain.Reminder{WorkspaceID: "T1", ID: "Rm1", Creator: "U1", User: "U1", Text: "file the report", Time: now},
		events.Event{ID: "E-Rm1", WorkspaceID: "T1", ActorID: "U1", Topic: "reminder.created", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	blocks, err := domain.SlackbotReminderBlocks("Reminder: file the report.", "Rm1", false)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := (service.Messages{Store: s}).PostAsSlackbot(ctx, "T1", "U1", domain.SlackbotPost{Text: "Reminder: file the report.", Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}
	channel := string(posted.Conversation)
	page := get(t, mux, "/app?channel="+channel).Body.String()
	requireContains(t, "reminder controls", page, "Mark as complete", "Remind me about this", "In 20 minutes", `action="/app/interaction"`)

	response := postForm(t, mux, "/app/interaction", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "message_id": {string(posted.ID)}, "block_id": {"slackbot_reminder:Rm1"},
		"action_id": {domain.SlackbotReminderCompleteAction}, "action_type": {"button"}, "channel": {channel}, "value": {"Rm1"},
	}.Encode(), false)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("complete status=%d body=%s", response.Code, response.Body)
	}
	reminder, err := s.GetReminder(ctx, "T1", "U1", "Rm1")
	if err != nil || reminder.CompleteAt.IsZero() {
		t.Fatalf("reminder=%+v err=%v", reminder, err)
	}
	after := get(t, mux, "/app?channel="+channel).Body.String()
	requireContains(t, "answered reminder", after, "You marked this reminder as complete.")
	requireMissing(t, "answered reminder", after, "Mark as complete")
}
