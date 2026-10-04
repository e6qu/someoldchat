package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Channel sections sort "By most recent activity" as Slack's do, so a channel
// row carries its newest message's time, the section offers the order, and a
// muted channel is marked from the same batched read.
func TestChannelRowsCarryRecentActivityAndMute(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedConversation(domain.Conversation{ID: "Cbusy", WorkspaceID: "T1", Name: "busy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cbusy", "U1"); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000600, 0).UTC()
	message := domain.Message{ID: "Mbusy", WorkspaceID: "T1", Conversation: "Cbusy", AuthorID: "U1", Text: "latest", CreatedAt: at}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Ebusy", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created"}`, CreatedAt: at}, ""); err != nil {
		t.Fatal(err)
	}
	muted := domain.ConversationNotificationPreferences{WorkspaceID: "T1", UserID: "U1", Conversation: "Cbusy", Level: domain.NotificationMute}
	if err := s.SetConversationNotificationPreferences(context.Background(), muted, events.Event{ID: "Emute", WorkspaceID: "T1", Topic: "conversation.notification_preferences_changed", Payload: `{}`, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}

	body := sidebar(t, mux)
	start := strings.Index(body, `data-conversation="Cbusy"`)
	if start < 0 {
		t.Fatal("the busy channel has no sidebar row")
	}
	row := body[start:]
	row = row[:strings.Index(row, "</a>")]
	if want := fmt.Sprintf(`data-recent="%d"`, at.Unix()); !strings.Contains(row, want) {
		t.Errorf("the channel row does not carry its newest message's time %s:\n%s", want, row)
	}
	if !strings.Contains(row, "is-muted") {
		t.Errorf("the muted channel's row is not marked muted:\n%s", row)
	}
	if !strings.Contains(body, `data-section-sort="recent"`) {
		t.Error(`the Channels section does not offer "By most recent activity"`)
	}
}
