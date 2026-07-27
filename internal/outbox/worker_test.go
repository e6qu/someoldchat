package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestWorkerClaimsDeliversAndAcknowledges(t *testing.T) {
	ctx := context.Background()
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "T1"})
	selected.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	selected.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Now().UTC()}
	if err := selected.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: message.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	var delivered []uint64
	worker, err := NewWorker(selected, "worker-1", 10, time.Minute, func(_ context.Context, record events.Record) error {
		delivered = append(delivered, record.Sequence)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(ctx, "T1")
	if err != nil || count != 1 || len(delivered) != 1 {
		t.Fatalf("count=%d delivered=%v err=%v", count, delivered, err)
	}
	count, err = worker.RunOnce(ctx, "T1")
	if err != nil || count != 0 {
		t.Fatalf("second count=%d err=%v", count, err)
	}
}

func TestWorkerLeavesLeaseUnacknowledgedOnDeliveryError(t *testing.T) {
	ctx := context.Background()
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "T1"})
	selected.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	selected.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Now().UTC()}
	if err := selected.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: message.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(selected, "worker-1", 10, time.Millisecond, func(context.Context, events.Record) error { return errors.New("delivery failed") })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(ctx, "T1"); err == nil {
		t.Fatal("delivery failure was hidden")
	}
	time.Sleep(2 * time.Millisecond)
	claimed, err := selected.ClaimEvents(ctx, "T1", "worker-2", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
}

func TestWorkerRenewsLeaseDuringLongDelivery(t *testing.T) {
	ctx := context.Background()
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "T1"})
	selected.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	selected.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Now().UTC()}
	if err := selected.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: message.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first, err := NewWorker(selected, "worker-1", 1, 30*time.Millisecond, func(context.Context, events.Record) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := first.RunOnce(ctx, "T1")
		result <- err
	}()
	<-started
	time.Sleep(80 * time.Millisecond)
	second, err := NewWorker(selected, "worker-2", 1, time.Minute, func(context.Context, events.Record) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := second.RunOnce(ctx, "T1"); err != nil || count != 0 {
		t.Fatalf("lease was lost during delivery: count=%d err=%v", count, err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerReportsRenewalFailureThatArrivesAfterDelivery(t *testing.T) {
	source := &lateRenewalFailureSource{renewStarted: make(chan struct{}), deliveryReturned: make(chan struct{}), releaseRenewal: make(chan struct{})}
	worker, err := NewWorker(source, "worker-1", 1, 3*time.Millisecond, func(context.Context, events.Record) error {
		<-source.renewStarted
		close(source.deliveryReturned)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, runErr := worker.RunOnce(context.Background(), "T1")
		result <- runErr
	}()
	<-source.deliveryReturned
	close(source.releaseRenewal)
	if err := <-result; !errors.Is(err, errLeaseLost) {
		t.Fatalf("worker error=%v, want %v", err, errLeaseLost)
	}
}

// batchSource records exactly which sequences were renewed, acknowledged and
// released, which is the only way to see the difference between "the batch was
// handled" and "the batch was handled once".
type batchSource struct {
	records  []events.Record
	claimed  bool
	renewed  [][]uint64
	acked    [][]uint64
	released [][]uint64

	// The worker renews a lease from its own goroutine while a test observes the
	// result from the test goroutine, so every field above is shared state and
	// must be guarded. onRenew lets a test wait for a renewal instead of polling
	// on a sleep.
	mu      sync.Mutex
	onRenew func()
}

func (s *batchSource) ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return s.records, nil
}

func (s *batchSource) RenewEvents(_ context.Context, _ string, sequences []uint64, _ time.Duration) error {
	s.mu.Lock()
	s.renewed = append(s.renewed, append([]uint64(nil), sequences...))
	notify := s.onRenew
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
	return nil
}

func (s *batchSource) AckEvents(_ context.Context, _ string, sequences []uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, append([]uint64(nil), sequences...))
	return nil
}

func (s *batchSource) ReleaseEvents(_ context.Context, _ string, sequences []uint64, retryAt time.Time) error {
	if !retryAt.After(time.Now().UTC()) {
		return errors.New("release retry time must be in the future")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, append([]uint64(nil), sequences...))
	return nil
}

// renewals, acknowledgements and releases copy the recorded batches under the
// lock so a test can assert on them without racing the worker.
func (s *batchSource) renewals() [][]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]uint64(nil), s.renewed...)
}

func (s *batchSource) acknowledgements() [][]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]uint64(nil), s.acked...)
}

func (s *batchSource) releases() [][]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]uint64(nil), s.released...)
}

func batchRecords(sequences ...uint64) []events.Record {
	result := make([]events.Record, 0, len(sequences))
	for _, sequence := range sequences {
		event, err := events.New(domain.EventID(fmt.Sprintf("event-%d", sequence)), "T1", "U1", events.NewPayload("message.created", events.String("message_id", fmt.Sprintf("M%d", sequence))), time.Unix(1700000000, 0))
		if err != nil {
			panic(err)
		}
		result = append(result, events.Record{Sequence: sequence, Event: event})
	}
	return result
}

func flatten(batches [][]uint64) []uint64 {
	result := make([]uint64, 0, len(batches))
	for _, batch := range batches {
		result = append(result, batch...)
	}
	return result
}

// A failure late in a batch must not put already-delivered records back on the
// queue: with a hundred-record batch that multiplies real webhook traffic by
// the batch size on every retry.
func TestOneFailureDoesNotReleaseAlreadyDeliveredRecords(t *testing.T) {
	source := &batchSource{records: batchRecords(1, 2, 3)}
	var attempted []uint64
	worker, err := NewWorker(source, "worker-1", 10, time.Minute, func(_ context.Context, record events.Record) error {
		attempted = append(attempted, record.Sequence)
		if record.Sequence == 3 {
			return errors.New("delivery failed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(context.Background(), "T1")
	if err == nil {
		t.Fatal("the delivery failure was hidden")
	}
	if count != 2 {
		t.Fatalf("delivered count=%d, want the two records that succeeded", count)
	}
	if got := flatten(source.acknowledgements()); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("acknowledged=%v, want each delivered record acknowledged once", source.acknowledgements())
	}
	if got := flatten(source.releases()); len(got) != 1 || got[0] != 3 {
		t.Fatalf("released=%v, want only the failed record", source.releases())
	}
	if len(attempted) != 3 {
		t.Fatalf("attempted=%v", attempted)
	}
}

// The lease has to cover every claimed record for as long as the worker holds
// it. Renewing only the processed prefix lets the tail expire mid-batch, and a
// second worker then delivers the tail a second time.
func TestLeaseRenewalCoversEveryClaimedSequence(t *testing.T) {
	source := &batchSource{records: batchRecords(1, 2, 3)}
	renewed := make(chan struct{})
	worker, err := NewWorker(source, "worker-1", 10, 3*time.Millisecond, func(_ context.Context, record events.Record) error {
		if record.Sequence != 1 {
			return nil
		}
		<-renewed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	source.onRenew = func() { once.Do(func() { close(renewed) }) }
	if _, err := worker.RunOnce(context.Background(), "T1"); err != nil {
		t.Fatal(err)
	}
	if len(source.renewals()) == 0 {
		t.Fatal("the lease was never renewed during a slow delivery")
	}
	first := source.renewals()[0]
	if len(first) != 3 || first[0] != 1 || first[1] != 2 || first[2] != 3 {
		t.Fatalf("renewed=%v while delivering the first record, want every claimed sequence", first)
	}
}

// virtualClock drives both the passage of time and the renewal ticker from the
// test goroutine. The defect being covered is in the *schedule*: a renewal that
// only runs while one delivery is in flight never fires for deliveries shorter
// than its interval, and no amount of sleeping distinguishes "renewed on time"
// from "the batch finished before the first tick". Advancing time explicitly,
// and handing the resulting tick to the worker synchronously, makes the
// schedule the thing under test.
type virtualClock struct {
	mu       sync.Mutex
	now      time.Time
	lastTick time.Time
	interval time.Duration

	ticks   chan time.Time
	renewed chan struct{}
}

func newVirtualClock(start time.Time) *virtualClock {
	return &virtualClock{now: start, lastTick: start, ticks: make(chan time.Time), renewed: make(chan struct{})}
}

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) newTicker(interval time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	c.interval = interval
	c.mu.Unlock()
	return c.ticks, func() {}
}

// advance moves the clock forward, delivering every tick the interval requires
// and waiting for the renewal it triggers to complete before time moves on.
func (c *virtualClock) advance(step time.Duration) {
	c.mu.Lock()
	target := c.now.Add(step)
	interval := c.interval
	c.mu.Unlock()
	for {
		c.mu.Lock()
		next := c.lastTick.Add(interval)
		if next.After(target) {
			c.mu.Unlock()
			break
		}
		c.lastTick = next
		c.now = next
		c.mu.Unlock()
		c.ticks <- next
		<-c.renewed
	}
	c.mu.Lock()
	c.now = target
	c.mu.Unlock()
}

// leasedSource enforces the precondition the SQL store enforces: a renewal or an
// acknowledgement is rejected unless the caller still holds a live lease. That
// is the whole point — a worker that delivers under an expired lease has
// already sent the record and cannot record that it did.
type leasedSource struct {
	clock *virtualClock

	mu         sync.Mutex
	records    []events.Record
	claimed    bool
	expiry     map[uint64]time.Time
	acked      []uint64
	renewCalls int
	renewedSet [][]uint64
}

var errLeaseExpired = errors.New("lease expired before acknowledgement")

func (s *leasedSource) ClaimEvents(_ context.Context, _ domain.WorkspaceID, _ string, _ int, lease time.Duration) ([]events.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	s.expiry = make(map[uint64]time.Time, len(s.records))
	expires := s.clock.Now().Add(lease)
	for _, record := range s.records {
		s.expiry[record.Sequence] = expires
	}
	return s.records, nil
}

func (s *leasedSource) RenewEvents(_ context.Context, _ string, sequences []uint64, lease time.Duration) error {
	now := s.clock.Now()
	s.mu.Lock()
	s.renewCalls++
	s.renewedSet = append(s.renewedSet, append([]uint64(nil), sequences...))
	for _, sequence := range sequences {
		if expiry, ok := s.expiry[sequence]; !ok || !expiry.After(now) {
			s.mu.Unlock()
			s.clock.renewed <- struct{}{}
			return fmt.Errorf("%w: sequence %d", errLeaseExpired, sequence)
		}
	}
	for _, sequence := range sequences {
		s.expiry[sequence] = now.Add(lease)
	}
	s.mu.Unlock()
	s.clock.renewed <- struct{}{}
	return nil
}

func (s *leasedSource) AckEvents(_ context.Context, _ string, sequences []uint64) error {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sequence := range sequences {
		if expiry, ok := s.expiry[sequence]; !ok || !expiry.After(now) {
			return fmt.Errorf("%w: sequence %d", errLeaseExpired, sequence)
		}
	}
	for _, sequence := range sequences {
		delete(s.expiry, sequence)
		s.acked = append(s.acked, sequence)
	}
	return nil
}

func (s *leasedSource) ReleaseEvents(_ context.Context, _ string, sequences []uint64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sequence := range sequences {
		delete(s.expiry, sequence)
	}
	return nil
}

func (s *leasedSource) counters() (int, []uint64, [][]uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewCalls, append([]uint64(nil), s.acked...), append([][]uint64(nil), s.renewedSet...)
}

// ClaimEvents sets one expiry for the whole batch, so the deadline the worker
// has to beat is "the batch finishes", not "a record finishes". With the shipped
// defaults — a hundred records, a thirty-second lease — a receiver taking 400 ms
// per record blows through the lease three quarters of the way in, and every
// record after that is delivered to the destination and then fails its
// acknowledgement: a duplicate delivery for that record and an abandoned tail
// for the rest. No delivery is ever long enough to reach a per-delivery
// renewal's first tick, which is why the schedule has to belong to the batch.
func TestLeaseIsRenewedBetweenRecordsSoAFastBatchOutlivesItsLease(t *testing.T) {
	const (
		batch       = 100
		perRecord   = 400 * time.Millisecond
		leaseWindow = 30 * time.Second
	)
	sequences := make([]uint64, 0, batch)
	for sequence := uint64(1); sequence <= batch; sequence++ {
		sequences = append(sequences, sequence)
	}
	clock := newVirtualClock(time.Unix(1700000000, 0).UTC())
	source := &leasedSource{clock: clock, records: batchRecords(sequences...)}
	var reached []uint64
	worker, err := NewWorker(source, "worker-1", batch, leaseWindow, func(_ context.Context, record events.Record) error {
		reached = append(reached, record.Sequence)
		clock.advance(perRecord)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.Now = clock.Now
	worker.NewTicker = clock.newTicker
	count, err := worker.RunOnce(context.Background(), "T1")
	renewals, acked, sets := source.counters()
	if err != nil {
		t.Fatalf("count=%d err=%v after %d renewals: the batch outlived its lease", count, err, renewals)
	}
	if renewals == 0 {
		t.Fatalf("RenewEvents was never called across a %s batch on a %s lease", time.Duration(batch)*perRecord, leaseWindow)
	}
	if count != batch || len(acked) != batch {
		t.Fatalf("delivered=%d acknowledged=%d, want %d", count, len(acked), batch)
	}
	if len(reached) != len(acked) {
		t.Fatalf("%d records reached the destination but %d were acknowledged: the difference is delivered twice", len(reached), len(acked))
	}
	// Every renewal covers exactly the outstanding tail: never a sequence this
	// worker has already acknowledged, which is a lease conflict against itself,
	// and never less than the tail, which is how the tail expires mid-batch.
	for _, set := range sets {
		if len(set) == 0 || set[len(set)-1] != batch || set[0]+uint64(len(set))-1 != batch {
			t.Fatalf("renewal covered %v, want the contiguous outstanding tail ending at %d", set, batch)
		}
	}
}

// A record that can never be delivered must leave the queue instead of being
// claimed and failed forever with no attempt counter and no dead letter.
func TestPermanentFailureIsAcknowledgedAndReported(t *testing.T) {
	source := &batchSource{records: batchRecords(1, 2)}
	worker, err := NewWorker(source, "worker-1", 10, time.Minute, func(_ context.Context, record events.Record) error {
		if record.Sequence == 1 {
			return fmt.Errorf("%w: payload cannot be encoded", ErrPermanent)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(context.Background(), "T1")
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("error=%v, want %v", err, ErrPermanent)
	}
	if count != 1 {
		t.Fatalf("delivered count=%d, want only the deliverable record", count)
	}
	if got := flatten(source.acknowledgements()); len(got) != 2 {
		t.Fatalf("acknowledged=%v, want the poisoned record dropped and the good record delivered", source.acknowledgements())
	}
	if len(source.releases()) != 0 {
		t.Fatalf("released=%v, want a permanently undeliverable record not to be retried", source.releases())
	}
}

var errLeaseLost = errors.New("lease lost")

type lateRenewalFailureSource struct {
	renewStarted     chan struct{}
	deliveryReturned chan struct{}
	releaseRenewal   chan struct{}
}

func (s *lateRenewalFailureSource) ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error) {
	return []events.Record{{Sequence: 1, Event: events.Event{ID: "event-1", WorkspaceID: "T1", Topic: "message.created"}}}, nil
}

func (s *lateRenewalFailureSource) RenewEvents(context.Context, string, []uint64, time.Duration) error {
	select {
	case <-s.renewStarted:
	default:
		close(s.renewStarted)
	}
	<-s.releaseRenewal
	return errLeaseLost
}

func (s *lateRenewalFailureSource) AckEvents(context.Context, string, []uint64) error { return nil }

func (s *lateRenewalFailureSource) ReleaseEvents(context.Context, string, []uint64, time.Time) error {
	return nil
}

// releaseRaceSource forces the interleaving the release paths have to survive:
// a renewal tick that fires while the worker is handing records back.
//
// The store rejects a renewal for a sequence the caller no longer holds, which
// is what makes an un-narrowed hold observable: the worker renews records it
// has just released and the store answers with a lease conflict against the
// worker's own bookkeeping.
type releaseRaceSource struct {
	mu       sync.Mutex
	renewed  [][]uint64
	released map[uint64]bool

	ticks chan time.Time
}

func (s *releaseRaceSource) ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error) {
	return batchRecords(1, 2, 3), nil
}

func (s *releaseRaceSource) RenewEvents(_ context.Context, _ string, sequences []uint64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewed = append(s.renewed, append([]uint64(nil), sequences...))
	for _, sequence := range sequences {
		if s.released[sequence] {
			return store.ErrLeaseConflict
		}
	}
	return nil
}

func (s *releaseRaceSource) AckEvents(context.Context, string, []uint64) error { return nil }

func (s *releaseRaceSource) ReleaseEvents(_ context.Context, _ string, sequences []uint64, _ time.Time) error {
	s.mu.Lock()
	if s.released == nil {
		s.released = map[uint64]bool{}
	}
	for _, sequence := range sequences {
		s.released[sequence] = true
	}
	s.mu.Unlock()
	// The tick arrives while the release is in flight. A release that narrows
	// the hold under the renewal lock makes the renewal wait and then find
	// nothing left to renew; one that does not lets the renewal read a set the
	// worker has already given up.
	s.ticks <- time.Now()
	return nil
}

func (s *releaseRaceSource) renewals() [][]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]uint64(nil), s.renewed...)
}

// A record handed back to the queue is not this worker's any more. Renewing it
// asks the store to extend a lease the worker has just dropped, which the store
// refuses — so the worker records a lost lease it inflicted on itself, and the
// only reason that does not surface today is that a delivery error happens to
// take precedence on this path.
func TestReleasingRecordsNarrowsTheLeaseHold(t *testing.T) {
	source := &releaseRaceSource{ticks: make(chan time.Time)}
	deliveryErr := errors.New("destination refused the record")
	worker, err := NewWorker(source, "worker-1", 10, 30*time.Millisecond, func(_ context.Context, record events.Record) error {
		if record.Sequence == 2 {
			return deliveryErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return source.ticks, func() {} }
	delivered, err := worker.RunOnce(context.Background(), "T1")
	if !errors.Is(err, deliveryErr) {
		t.Fatalf("RunOnce error=%v, want the delivery failure", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered=%d, want 1", delivered)
	}
	for _, renewal := range source.renewals() {
		for _, sequence := range renewal {
			if sequence == 2 || sequence == 3 {
				t.Fatalf("the worker renewed %d after releasing it: renewals=%v", sequence, source.renewals())
			}
		}
	}
}

// blockingRenewalSource answers a renewal only when the renewal's own context
// ends, which is what a degraded database does.
type blockingRenewalSource struct {
	ticks chan time.Time
	done  chan struct{}
}

func (s *blockingRenewalSource) ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error) {
	return batchRecords(1), nil
}

func (s *blockingRenewalSource) RenewEvents(ctx context.Context, _ string, _ []uint64, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingRenewalSource) AckEvents(context.Context, string, []uint64) error { return nil }

func (s *blockingRenewalSource) ReleaseEvents(context.Context, string, []uint64, time.Time) error {
	return nil
}

// A renewal that cannot complete within its own interval has already failed:
// the lease it is renewing expires three intervals in, and every record the
// worker delivers after that is delivered without a lease and claimable by
// another worker at the same time. The renewal has to be bounded, so the
// cancellation machinery runs before the lease lapses rather than after.
func TestASlowRenewalIsBoundedByItsOwnInterval(t *testing.T) {
	source := &blockingRenewalSource{ticks: make(chan time.Time), done: make(chan struct{})}
	const lease = 30 * time.Millisecond
	worker, err := NewWorker(source, "worker-1", 10, lease, func(ctx context.Context, _ events.Record) error {
		// The delivery outlives the lease, which is the case the renewal exists
		// for.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * lease):
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return source.ticks, func() {} }
	go func() { source.ticks <- time.Now() }()
	started := time.Now()
	finished := make(chan error, 1)
	go func() {
		_, err := worker.RunOnce(context.Background(), "T1")
		finished <- err
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RunOnce error=%v, want the renewal to fail with %v once it outlives its interval", err, context.DeadlineExceeded)
		}
		if elapsed := time.Since(started); elapsed >= lease {
			t.Fatalf("the renewal took %v to fail, which is past the %v lease it was renewing", elapsed, lease)
		}
	case <-time.After(10 * lease):
		t.Fatalf("an unbounded renewal outlived the %v lease it was renewing and the batch is still running", lease)
	}
}
