package sqlstore

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// TestConcurrentWritersAreServedFairly measures what a write queue is for.
//
// Throughput is the obvious number and the less interesting one: writes to one
// SQLite file are serialised by the engine whatever we do, so the total time is
// roughly the same with a queue and without. What differs is who waits and for
// how long. SQLite's busy_timeout is a sleep-and-retry handler with no ordering,
// so when the write lock frees every waiter races for it again and an unlucky
// writer can lose repeatedly; Go's own pool is no fairer, handing a freed
// connection to connRequests.TakeRandom() rather than to the longest waiter.
//
// So this reports the slowest single write beside the median. A queue should
// pull the tail in hard while leaving the middle where it was.
func TestConcurrentWritersAreServedFairly(t *testing.T) {
	if testing.Short() {
		t.Skip("writes several thousand rows")
	}
	const (
		writers    = 16
		eachWrites = 40
	)
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "fairness.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "fair"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "fair"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "fair"}, "U1", events.Event{
		ID: "evt_seed", WorkspaceID: "T1", Topic: "conversation.created", Payload: "{}", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	latencies := make([][]time.Duration, writers)
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(writers)
	began := time.Now()
	for writer := 0; writer < writers; writer++ {
		go func(writer int) {
			defer group.Done()
			latencies[writer] = make([]time.Duration, 0, eachWrites)
			<-start
			for index := 0; index < eachWrites; index++ {
				at := time.Now()
				// A distinct instant per write: a message timestamp is its
				// identifier and the store admits one per conversation, so
				// writers taking the clock would collide with each other rather
				// than queue behind each other, and the test would be measuring
				// the collision instead of the wait.
				created := time.Unix(1700000000, 0).Add(time.Duration(writer*eachWrites+index) * time.Microsecond).UTC()
				message := domain.Message{
					ID:          domain.MessageID(fmt.Sprintf("m-%02d-%03d", writer, index)),
					WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1",
					Text: "fairness", CreatedAt: created,
				}
				event := events.Event{
					ID: domain.EventID(fmt.Sprintf("e-%02d-%03d", writer, index)), WorkspaceID: "T1",
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

	// The tail is the assertion. Without ordering a loser can be passed over
	// again and again, and the worst write runs orders of magnitude behind the
	// median; with a queue its wait is bounded by the writers already ahead of
	// it. Sixteen writers cannot make one wait a hundred times the median
	// unless something is starving it.
	if ratio := float64(worst) / float64(median); ratio > 100 {
		t.Fatalf("the slowest write took %.0f times the median (%s against %s); writers are not being served fairly", ratio, worst.Round(time.Microsecond), median.Round(time.Microsecond))
	}
}
