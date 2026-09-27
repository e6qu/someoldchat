//go:build postgres

package qualification

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/postgres"
)

// TestPostgresConcurrentWritersAreServedFairly measures what a write queue is for.
//
// Throughput is the obvious number and the less interesting one: writes to one
// SQLite file are serialised by the engine whatever we do, so the total time is
// roughly the same with a queue and without. What differs is who waits and for
// how long. SQLite's busy_timeout is a sleep-and-retry handler with no ordering,
// so when the write lock frees every waiter races for it again and an unlucky
// writer can lose repeatedly; Go's own pool is no fairer, handing a freed
// connection to connRequests.TakeRandom() rather than to the longest waiter.
//
// So this reports the slowest single write beside the median, and asserts that
// no writer is left behind the others: each writer's finishing time against
// the first to finish, and no single write waiting out most of the run.
func TestPostgresConcurrentWritersAreServedFairly(t *testing.T) {
	if testing.Short() {
		t.Skip("writes several thousand rows")
	}
	const (
		writers    = 16
		eachWrites = 40
	)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	workspaceID := domain.WorkspaceID(fmt.Sprintf("T-fair-%d", stamp))
	userID := domain.UserID(fmt.Sprintf("U-fair-%d", stamp))
	conversationID := domain.ConversationID(fmt.Sprintf("C-fair-%d", stamp))
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if strings.TrimSpace(dsn) == "" {
		t.Skip("SAMEOLDCHAT_POSTGRES_DSN is required")
	}
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "fair"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, domain.User{ID: userID, WorkspaceID: workspaceID, Name: "fair"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateConversation(ctx, domain.Conversation{ID: conversationID, WorkspaceID: workspaceID, Name: "fair"}, userID, events.Event{
		ID: domain.EventID(fmt.Sprintf("evt-seed-%d", stamp)), WorkspaceID: workspaceID, Topic: "conversation.created", Payload: "{}", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	latencies := make([][]time.Duration, writers)
	finished := make([]time.Duration, writers)
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(writers)
	began := time.Now()
	for writer := 0; writer < writers; writer++ {
		go func(writer int) {
			defer group.Done()
			latencies[writer] = make([]time.Duration, 0, eachWrites)
			<-start
			defer func() { finished[writer] = time.Since(began) }()
			for index := 0; index < eachWrites; index++ {
				at := time.Now()
				// A distinct instant per write: a message timestamp is its
				// identifier and the store admits one per conversation, so
				// writers taking the clock would collide with each other rather
				// than queue behind each other, and the test would be measuring
				// the collision instead of the wait.
				created := time.Unix(1700000000, 0).Add(time.Duration(writer*eachWrites+index) * time.Microsecond).UTC()
				message := domain.Message{
					ID:          domain.MessageID(fmt.Sprintf("m-%d-%02d-%03d", stamp, writer, index)),
					WorkspaceID: workspaceID, Conversation: conversationID, AuthorID: userID,
					Text: "fairness", CreatedAt: created,
				}
				event := events.Event{
					ID: domain.EventID(fmt.Sprintf("e-%d-%02d-%03d", stamp, writer, index)), WorkspaceID: workspaceID,
					Topic: "message.created", Payload: "{}", CreatedAt: created,
				}
				if err := store.CreateMessage(ctx, message, event, ""); err != nil {
					t.Errorf("writer %d write %d: %v", writer, index, err)
					return
				}
				latencies[writer] = append(latencies[writer], time.Since(at))
			}
		}(writer)
	}
	close(start)
	group.Wait()
	total := time.Since(began)
	if t.Failed() {
		return
	}

	all := make([]time.Duration, 0, writers*eachWrites)
	for _, batch := range latencies {
		all = append(all, batch...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	median, worst := all[len(all)/2], all[len(all)-1]
	t.Logf("%d writes by %d writers in %s (%.0f writes/s); median %s, p99 %s, worst %s",
		len(all), writers, total.Round(time.Millisecond), float64(len(all))/total.Seconds(),
		median.Round(time.Microsecond), all[len(all)*99/100].Round(time.Microsecond), worst.Round(time.Microsecond))

	// Fairness is about who waits, so the assertion compares writers with one
	// another rather than one write with the median. Event-producing commits on
	// PostgreSQL are serialized in commit order (see outboxCommitOrderStatements),
	// so a single slow WAL flush holds every queued writer behind it: the worst
	// write can sit far above the median while every writer is served in turn.
	// Starvation looks different — one writer keeps losing and finishes long
	// after the rest. With a first-in, first-out wait each writer finishes its
	// share at about the same time.
	sort.Slice(finished, func(i, j int) bool { return finished[i] < finished[j] })
	first, last := finished[0], finished[len(finished)-1]
	t.Logf("writers finished between %s and %s", first.Round(time.Millisecond), last.Round(time.Millisecond))
	if last > 3*first && last-first > time.Second {
		t.Fatalf("the last writer finished %s after starting, the first %s: writers are not being served fairly", last.Round(time.Millisecond), first.Round(time.Millisecond))
	}
	// A write that waits out most of the run is starved however the others
	// fared.
	if worst > total*3/4 && worst > time.Second {
		t.Fatalf("one write waited %s of a %s run; writers are not being served fairly", worst.Round(time.Microsecond), total.Round(time.Millisecond))
	}
}
