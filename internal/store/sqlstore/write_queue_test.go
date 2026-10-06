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
// So this asks how often a write was overtaken: how many writes that began
// after it had finished before it did. A queue serves waiters in order, so a
// write is passed over only by the few that raced it to the queue; without one
// a loser is passed over again and again. The slowest write against the median
// is reported beside it but not asserted: a stall that holds every writer at
// once, a slow fsync on a loaded runner, stretches the tail of a fair queue as
// much as an unfair one, and a ratio cannot tell them apart.
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

	type span struct{ began, ended time.Time }
	latencies := make([][]time.Duration, writers)
	spans := make([][]span, writers)
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(writers)
	began := time.Now()
	for writer := 0; writer < writers; writer++ {
		go func(writer int) {
			defer group.Done()
			latencies[writer] = make([]time.Duration, 0, eachWrites)
			spans[writer] = make([]span, 0, eachWrites)
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
				done := time.Now()
				latencies[writer] = append(latencies[writer], done.Sub(at))
				spans[writer] = append(spans[writer], span{began: at, ended: done})
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

	// Overtaking is the assertion. A write that joins the queue behind another
	// cannot finish first, so a write is overtaken only by those that took the
	// clock after it but reached the queue before it, which is a handful at
	// most; one writer per other writer is a generous bound. Without ordering a
	// loser is overtaken by dozens.
	var every []span
	for _, batch := range spans {
		every = append(every, batch...)
	}
	most := 0
	for _, write := range every {
		overtaken := 0
		for _, other := range every {
			if other.began.After(write.began) && other.ended.Before(write.ended) {
				overtaken++
			}
		}
		most = max(most, overtaken)
	}
	t.Logf("the most-overtaken write was passed by %d writes begun after it", most)
	if most > writers {
		t.Fatalf("a write was overtaken by %d writes that began after it, more than the %d writers could account for; writers are not being served in order", most, writers)
	}
}
