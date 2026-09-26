package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type journalReadCounter struct {
	store.Store
	examined int
}

func (c *journalReadCounter) ListEventsAfter(ctx context.Context, workspace domain.WorkspaceID, after uint64, limit int) ([]events.Record, error) {
	records, err := c.Store.ListEventsAfter(ctx, workspace, after, limit)
	c.examined += len(records)
	return records, err
}

// A reader who is not in a busy private channel used to be handed
// back an empty page and no way to move past it, so every live stream re-read
// and re-authorized the channel's whole history on every poll. The page now
// says how far it examined, and a reader resuming from there reads nothing
// again.
func TestUserEventPageReportsHowFarItExamined(t *testing.T) {
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	state.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "eve"})
	key := []byte(strings.Repeat("k", 32))
	messages := Messages{Store: state, AppCredentialKey: key}
	busy, err := messages.CreateConversation(ctx, "T1", "U1", "busy-private", true)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 250; index++ {
		if _, err := messages.Post(ctx, "T1", "U1", busy.ID, fmt.Sprintf("m%d", index), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	head, err := state.LatestEventSequence(ctx, "T1")
	if err != nil {
		t.Fatal(err)
	}
	counter := &journalReadCounter{Store: state}
	counted := Messages{Store: counter, AppCredentialKey: key}

	first, err := counted.ListUserEventsAfter(ctx, "T1", "U3", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 0 {
		t.Fatalf("a non-member was shown %d records of a private channel", len(first.Records))
	}
	if first.Through != head {
		t.Fatalf("the page examined through %d, want the journal head %d", first.Through, head)
	}
	counter.examined = 0
	second, err := counted.ListUserEventsAfter(ctx, "T1", "U3", first.Through, 100)
	if err != nil {
		t.Fatal(err)
	}
	if counter.examined != 0 || second.Through != head {
		t.Fatalf("resuming from Through re-read %d records (through=%d)", counter.examined, second.Through)
	}

	// A member's page stops at the limit, and Through is exactly the last
	// record it was given, so nothing between pages is skipped.
	member, err := messages.ListUserEventsAfter(ctx, "T1", "U1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(member.Records) != 10 || member.Through != member.Records[9].Sequence {
		t.Fatalf("member page has %d records through %d", len(member.Records), member.Through)
	}
	// A cursor already past the head is never moved backwards.
	beyond, err := messages.ListUserEventsAfter(ctx, "T1", "U1", head+5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if beyond.Through != head+5 {
		t.Fatalf("a cursor past the head came back as %d", beyond.Through)
	}
}
