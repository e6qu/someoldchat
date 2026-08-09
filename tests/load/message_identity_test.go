package load

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A message's timestamp is its public identifier. Read cursors, thread roots,
// pagination and permalinks all key on it, so two messages in one conversation
// sharing one is not a cosmetic collision: marking one read marks the other,
// a reply addresses whichever the store returns first, and a page boundary can
// swallow or repeat a message.
//
// The suite beside this one posts with timestamps spaced a microsecond apart on
// purpose — its own comment records that closer spacing "collapsed onto one
// value" — so distinctness was assumed by construction and never asked of the
// store that has to enforce it.
//
// This asks the store directly, with every writer racing for the SAME instant.
// Driving the service instead looked more realistic and proved less: the service
// takes its instant from the clock, the posts landed in different microseconds
// on their own, and the collision path was never reached. Removing the retry
// that exists to handle a collision left that version green, which is the whole
// definition of a test with no teeth.
//
// Exactly one writer may win. The losers must be told with
// store.ErrMessageTimestampTaken specifically, because that sentinel is what the
// service retries on: any other error and the post fails outright instead of
// advancing a microsecond and trying again.
func TestOneWriterWinsAContestedMessageTimestamp(t *testing.T) {
	const (
		writers   = 32
		workspace = domain.WorkspaceID("T-identity")
		channel   = domain.ConversationID("C-identity")
	)
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "identity"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateConversation(ctx, domain.Conversation{
		ID: channel, WorkspaceID: workspace, Name: "identity",
	}, "U-0", events.Event{ID: "E-conversation", WorkspaceID: workspace, Topic: "conversation.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// One instant, contested by everybody.
	contested := time.Unix(1700000000, 123*1000).UTC()
	var winners, taken, other atomic.Int64
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(writers)
	for writer := 0; writer < writers; writer++ {
		go func(writer int) {
			defer group.Done()
			message := domain.Message{
				ID:           domain.MessageID(fmt.Sprintf("M-%02d", writer)),
				WorkspaceID:  workspace,
				Conversation: channel,
				AuthorID:     "U-0",
				Text:         "contested",
				CreatedAt:    contested,
			}
			event := events.Event{
				ID: domain.EventID(fmt.Sprintf("E-%02d", writer)), WorkspaceID: workspace,
				Topic: "message.created", CreatedAt: contested,
			}
			<-start
			switch err := repository.CreateMessage(ctx, message, event, ""); {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, store.ErrMessageTimestampTaken):
				taken.Add(1)
			default:
				other.Add(1)
				t.Errorf("writer %d was refused with %v, and the service only retries on ErrMessageTimestampTaken: any other error loses the message", writer, err)
			}
		}(writer)
	}
	close(start)
	group.Wait()

	if winners.Load() != 1 {
		t.Fatalf("%d writers took the same timestamp; a message timestamp is the identifier a read cursor, a thread root and a permalink all use, so two messages holding one is two messages nothing can tell apart", winners.Load())
	}
	if want := int64(writers - 1); taken.Load() != want {
		t.Fatalf("%d writers were told the timestamp was taken, want %d (%d were refused some other way)", taken.Load(), want, other.Load())
	}
}
