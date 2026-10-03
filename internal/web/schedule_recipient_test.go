package web

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Scheduling in a one-to-one DM names the other person's time zone, so the
// composer can say what the chosen time is for them, as Slack does. A person
// whose client never reported a zone, a zone the server cannot load, and a
// channel all carry no such line.
func TestScheduleInADirectMessageKnowsTheRecipientsZone(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder", Profile: domain.UserProfile{Timezone: "America/Los_Angeles"}})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "cat", RealName: "Cat Unset"})
	s.SeedUser(domain.User{ID: "U4", WorkspaceID: "T1", Name: "dan", RealName: "Dan Nowhere", Profile: domain.UserProfile{Timezone: "Nowhere/Atlantis"}})
	for _, dm := range []struct {
		id   domain.ConversationID
		with domain.UserID
	}{{"Dbob", "U2"}, {"Dcat", "U3"}, {"Ddan", "U4"}} {
		s.SeedConversation(domain.Conversation{ID: dm.id, WorkspaceID: "T1", Name: "direct", Kind: domain.ConversationTypeIM})
		s.SeedConversationMember(dm.id, "U1")
		s.SeedConversationMember(dm.id, dm.with)
	}
	requireContains(t, "a DM with a reported zone", get(t, mux, "/app?channel=Dbob").Body.String(),
		`data-schedule-recipient data-recipient-name="Bob Builder" data-recipient-zone="America/Los_Angeles">Bob Builder is in America/Los_Angeles.</p>`,
		`id="composer-schedule-recipient" aria-live="polite" hidden>`)
	requireMissing(t, "a DM with no reported zone", get(t, mux, "/app?channel=Dcat").Body.String(), "data-schedule-recipient data-recipient-name")
	requireMissing(t, "a DM with an unknown zone", get(t, mux, "/app?channel=Ddan").Body.String(), "data-schedule-recipient data-recipient-name")
	requireMissing(t, "a channel", get(t, mux, "/app?channel=Cdev").Body.String(), "data-schedule-recipient data-recipient-name")
}
