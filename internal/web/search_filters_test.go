package web

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Search's "Only my channels" and "Exclude automations" chips narrow the
// message results, stay checked on the page they produced, and are offered
// only where they mean something: messages, outside a one-conversation scope.
func TestSearchChipsKeepToMyChannelsAndToPeople(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedConversation(domain.Conversation{ID: "Cother", WorkspaceID: "T1", Name: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0).UTC()
	post := func(id domain.MessageID, conversation domain.ConversationID, appID domain.AppID) {
		t.Helper()
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: conversation, AuthorID: "U1", AppID: appID, Text: "deploy " + string(id), CreatedAt: at}
		at = at.Add(time.Second)
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: at}, ""); err != nil {
			t.Fatal(err)
		}
	}
	post("Mmine", "Cdev", "")
	post("Melsewhere", "Cother", "")
	post("Mapp", "Cdev", "A1")

	all := get(t, mux, "/app/search?q=deploy&channel=Cdev").Body.String()
	requireContains(t, "unfiltered", all, "#message-Mmine\"", "#message-Melsewhere\"", "#message-Mapp\"",
		`<input type="checkbox" name="mine" value="1">Only my channels`, `<input type="checkbox" name="automations" value="exclude">Exclude automations`)
	mine := get(t, mux, "/app/search?q=deploy&channel=Cdev&mine=1").Body.String()
	requireContains(t, "only my channels", mine, "#message-Mmine\"", "#message-Mapp\"", `name="mine" value="1" checked>`)
	requireMissing(t, "only my channels", mine, "#message-Melsewhere\"")
	people := get(t, mux, "/app/search?q=deploy&channel=Cdev&automations=exclude").Body.String()
	requireContains(t, "exclude automations", people, "#message-Mmine\"", "#message-Melsewhere\"", `name="automations" value="exclude" checked>`)
	requireMissing(t, "exclude automations", people, "#message-Mapp\"")

	scoped := get(t, mux, "/app/search?q=deploy&channel=Cdev&scope=channel").Body.String()
	requireMissing(t, "a one-conversation scope", scoped, "Only my channels")
	files := get(t, mux, "/app/search?q=deploy&channel=Cdev&type=files").Body.String()
	requireMissing(t, "the files tab", files, "Only my channels", "Exclude automations")
}
