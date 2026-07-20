package load

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Search pages with a cursor over (created_at, id) while the corpus keeps
// growing. Two things have to hold through that, and neither is visible from a
// single-threaded test:
//
//   - a reader walking pages sees every message that already existed when it
//     started, exactly once, however many arrive mid-walk;
//   - a private conversation never appears to somebody who is not a member,
//     even while memberships are being changed underneath the search.

func seedSearchWorkspace(t *testing.T) *memory.Store {
	t.Helper()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "load"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	return repository
}

func searchMessage(index int, conversation domain.ConversationID, at time.Time) (domain.Message, events.Event) {
	message := domain.Message{
		ID:           domain.MessageID(fmt.Sprintf("msg_%s_%06d", conversation, index)),
		WorkspaceID:  "T1",
		Conversation: conversation,
		AuthorID:     "U1",
		Text:         fmt.Sprintf("needle haystack entry %d", index),
		CreatedAt:    at,
	}
	event := events.Event{
		ID:          domain.EventID(fmt.Sprintf("evt_%s_%06d", conversation, index)),
		WorkspaceID: "T1",
		Topic:       "message.created",
		Payload:     "{}",
		CreatedAt:   at,
	}
	return message, event
}

func TestSearchPaginationIsCompleteWhileMessagesArrive(t *testing.T) {
	const (
		seeded   = 240
		arriving = 240
		pageSize = 25
	)
	repository := seedSearchWorkspace(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	// Messages are seeded in groups sharing one timestamp. Cursor pagination
	// over (created_at, id) is only hard when timestamps collide: with unique
	// timestamps the identifier tiebreak is never consulted, and a broken
	// tiebreak would silently drop or repeat rows.
	const perTimestamp = 8
	for index := 0; index < seeded; index++ {
		at := base.Add(time.Duration(index/perTimestamp) * time.Millisecond)
		message, event := searchMessage(index, "C1", at)
		if err := repository.CreateMessage(ctx, message, event, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Writers append newer messages for the whole duration of the walk.
	var writing sync.WaitGroup
	stop := make(chan struct{})
	writing.Add(1)
	go func() {
		defer writing.Done()
		later := time.Now().UTC()
		for index := 0; index < arriving; index++ {
			select {
			case <-stop:
				return
			default:
			}
			message, event := searchMessage(seeded+index, "C1", later.Add(time.Duration(index/perTimestamp)*time.Millisecond))
			if err := repository.CreateMessage(ctx, message, event, ""); err != nil {
				return
			}
		}
	}()

	seen := make(map[domain.MessageID]int)
	cursor := domain.Cursor("")
	for pages := 0; pages < (seeded+arriving)/pageSize+8; pages++ {
		page, err := repository.SearchMessages(ctx, "T1", "U1", "needle", domain.PageRequest{Limit: pageSize, Cursor: cursor})
		if err != nil {
			close(stop)
			writing.Wait()
			t.Fatalf("search page %d: %v", pages, err)
		}
		for _, message := range page.Messages {
			seen[message.ID]++
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	close(stop)
	writing.Wait()

	for id, count := range seen {
		if count != 1 {
			t.Fatalf("search returned %q %d times across pages, want once", id, count)
		}
	}
	// Everything that existed before the walk began must have been returned.
	missing := make([]domain.MessageID, 0)
	for index := 0; index < seeded; index++ {
		id := domain.MessageID(fmt.Sprintf("msg_C1_%06d", index))
		if _, ok := seen[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d message(s) present before the walk were never returned, first %q", len(missing), missing[0])
	}
}

// A non-member must never see a private conversation's messages, including
// while their membership is being added and removed concurrently. A single
// leaked page is a disclosure, so the check is on every page of every search.
func TestSearchNeverLeaksPrivateConversationsToNonMembers(t *testing.T) {
	const (
		searchers = 16
		churns    = 200
	)
	repository := seedSearchWorkspace(t)
	repository.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true})
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	for index := 0; index < 60; index++ {
		message, event := searchMessage(index, "C2", base.Add(time.Duration(index)*time.Millisecond))
		if err := repository.CreateMessage(ctx, message, event, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Positive control: a member must actually find these messages, otherwise
	// the leak assertion below would hold for a search that returns nothing.
	repository.SeedConversationMember("C2", "U1")
	control, err := repository.SearchMessages(ctx, "T1", "U1", "needle", domain.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, message := range control.Messages {
		if message.Conversation == "C2" {
			found++
		}
	}
	if found == 0 {
		t.Fatal("a member of the private conversation found none of its messages; the leak check below would be vacuous")
	}
	if err := repository.RemoveConversationMember(ctx, "C2", "U1", events.Event{
		ID: "evt_control_leave", WorkspaceID: "T1", Topic: "member.left", Payload: "{}", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var leaked int64
	var group sync.WaitGroup
	stop := make(chan struct{})

	// U1 joins and leaves the private conversation continuously.
	group.Add(1)
	go func() {
		defer group.Done()
		for index := 0; index < churns; index++ {
			select {
			case <-stop:
				return
			default:
			}
			repository.SeedConversationMember("C2", "U1")
			_ = repository.RemoveConversationMember(ctx, "C2", "U1", events.Event{
				ID: domain.EventID(fmt.Sprintf("evt_leave_%d", index)), WorkspaceID: "T1", Topic: "member.left", Payload: "{}", CreatedAt: time.Now().UTC(),
			})
		}
	}()

	// U2 is never a member and must never see C2, whatever U1 is doing.
	for searcher := 0; searcher < searchers; searcher++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				page, err := repository.SearchMessages(ctx, "T1", "U2", "needle", domain.PageRequest{Limit: 50})
				if err != nil {
					return
				}
				for _, message := range page.Messages {
					if message.Conversation == "C2" {
						atomic.AddInt64(&leaked, 1)
					}
				}
			}
		}()
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	group.Wait()

	if leaked > 0 {
		t.Fatalf("private conversation messages were returned to a non-member %d times", leaked)
	}
}
