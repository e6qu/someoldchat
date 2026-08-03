package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Slack's call block carries only `{"type":"call","call_id":"R…"}`. Everything a
// member reads about the call belongs to the object the app registered through
// calls.add, and nothing resolved it: an app could register a call, post a
// message referring to it, and the member would see an empty message.
func TestCallBlockRendersTheRegisteredCall(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: s}
	call, err := messages.AddCall(context.Background(), "T1", "U1", "external-1", "EXT-1",
		"https://calls.example/join/1", "", "Release sync", time.Unix(1700000400, 0).UTC(), []domain.UserID{"U1", "U2"})
	if err != nil {
		t.Fatal(err)
	}
	postCallMessage(t, s, "Mcall", string(call.ID))

	body := renderWorkspacePage(t, mux)
	for _, want := range []string{"Release sync", "In progress", "Join call", "https://calls.example/join/1", "Bob Builder"} {
		if !strings.Contains(body, want) {
			t.Errorf("the call block did not render %q", want)
		}
	}
}

// An identifier that resolves to nothing must say so. Rendering an empty card,
// or a Join button pointing nowhere, would be worse than saying the call is
// gone.
func TestCallBlockSaysSoWhenTheCallIsUnknown(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	postCallMessage(t, s, "Munknown", "R-does-not-exist")

	body := renderWorkspacePage(t, mux)
	if !strings.Contains(body, "This call is no longer available.") {
		t.Error("an unresolvable call block did not report itself as unavailable")
	}
	if strings.Contains(body, "Join call") {
		t.Error("an unresolvable call block offered a Join control")
	}
}

// An ended call keeps its card and loses its join control: the message stays
// meaningful as history, and the link is not offered for something nobody can
// join.
func TestEndedCallKeepsItsCardAndLosesItsJoinControl(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	ctx := context.Background()
	call, err := messages.AddCall(ctx, "T1", "U1", "external-2", "EXT-2", "https://calls.example/join/2", "", "Retro", time.Unix(1700000500, 0).UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.EndCall(ctx, "T1", "U1", call.ID, 600); err != nil {
		t.Fatal(err)
	}
	postCallMessage(t, s, "Mended", string(call.ID))

	body := renderWorkspacePage(t, mux)
	if !strings.Contains(body, "Retro") || !strings.Contains(body, "Ended") {
		t.Error("an ended call lost its card")
	}
	if strings.Contains(body, ">Join call<") {
		t.Error("an ended call still offered a Join control")
	}
}

func postCallMessage(t *testing.T, s *memory.Store, id domain.MessageID, callID string) {
	t.Helper()
	at := time.Unix(1700000600, 0).UTC()
	message := domain.Message{
		ID: id, WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1",
		Text: "call", Blocks: `[{"type":"call","call_id":"` + callID + `"}]`, CreatedAt: at,
	}
	event := events.Event{ID: domain.EventID("E" + id), WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created"}`, CreatedAt: at}
	if err := s.CreateMessage(context.Background(), message, event, ""); err != nil {
		t.Fatal(err)
	}
}
