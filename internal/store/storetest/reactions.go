// Package storetest holds behavioral checks every repository implementation
// must pass identically. Each implementation's own tests seed the fixture its
// seeding API allows and hand the repository to the check, so the memory and
// SQL profiles are held to one contract rather than two copies of it.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// UserReactionRepository is the part of store.Store the reactions.list check
// drives.
type UserReactionRepository interface {
	CreateMessage(context.Context, domain.Message, events.Event, string, ...events.Event) error
	AddReaction(context.Context, domain.Reaction, events.Event) error
	ListUserReactions(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.UserReactionPage, error)
}

// CheckUserReactionsPageByMessage requires reactions.list to page by reacted
// message: a message U1 reacted to with several emoji is one item whose rows
// all arrive on the same page, never split across two. The repository must
// hold workspace T1, member U1 and public conversation C1 that U1 belongs to.
func CheckUserReactionsPageByMessage(t *testing.T, repository UserReactionRepository) {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	names := map[domain.MessageID][]string{"M1": {"eyes", "tada", "thumbsup"}, "M2": {"wave"}, "M3": {"fire", "rocket"}}
	for index, id := range []domain.MessageID{"M1", "M2", "M3"} {
		created := base.Add(time.Duration(index) * time.Second)
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: string(id), CreatedAt: created}
		if err := repository.CreateMessage(ctx, message, events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: created}, ""); err != nil {
			t.Fatal(err)
		}
		for offset, name := range names[id] {
			at := created.Add(time.Duration(offset+1) * time.Millisecond)
			if err := repository.AddReaction(ctx, domain.Reaction{Message: id, Name: name, UserID: "U1", CreatedAt: at}, events.Event{ID: domain.EventID("E" + string(id) + name), WorkspaceID: "T1", Topic: "reaction.added", Payload: string(id), CreatedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := make(map[domain.MessageID]int)
	var order []domain.MessageID
	request := domain.PageRequest{Limit: 1}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("reactions.list did not terminate")
		}
		page, err := repository.ListUserReactions(ctx, "T1", "U1", request)
		if err != nil {
			t.Fatal(err)
		}
		messages := make(map[domain.MessageID]bool)
		for _, item := range page.Items {
			messages[item.Message.ID] = true
			seen[item.Message.ID]++
		}
		if len(messages) != 1 {
			t.Fatalf("a page of limit 1 carried %d messages: %+v", len(messages), page.Items)
		}
		for id := range messages {
			order = append(order, id)
			if seen[id] != len(names[id]) {
				t.Fatalf("message %s arrived with %d of its %d reactions on one page", id, seen[id], len(names[id]))
			}
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatalf("last page carried cursor %q", page.NextCursor)
			}
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(order) != 3 || order[0] != "M1" || order[1] != "M2" || order[2] != "M3" {
		t.Fatalf("messages paged as %v, want M1 M2 M3 once each", order)
	}
}
