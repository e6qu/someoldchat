package outbox

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

type Source interface {
	ClaimEvents(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]events.Record, error)
	RenewEvents(context.Context, string, []uint64, time.Duration) error
	AckEvents(context.Context, string, []uint64) error
	ReleaseEvents(context.Context, string, []uint64, time.Time) error
}

type Delivery func(context.Context, events.Record) error

type Worker struct {
	Source  Source
	Owner   string
	Limit   int
	Lease   time.Duration
	Deliver Delivery

	// Now and NewTicker exist so the renewal schedule is a contract a test can
	// drive rather than a wall-clock race it approximates with sleeps: the
	// defect this worker had was in the schedule itself, and a sleeping test
	// cannot distinguish "renewed on time" from "never renewed and finished
	// first". Both are nil in production, which selects the real clock.
	Now       func() time.Time
	NewTicker func(time.Duration) (<-chan time.Time, func())
}

func NewWorker(source Source, owner string, limit int, lease time.Duration, deliver Delivery) (Worker, error) {
	if source == nil || owner == "" || limit <= 0 || lease <= 0 || deliver == nil {
		return Worker{}, errors.New("outbox worker requires source, owner, positive limit and lease, and delivery function")
	}
	return Worker{Source: source, Owner: owner, Limit: limit, Lease: lease, Deliver: deliver}, nil
}

// ErrPermanent marks a delivery failure that retrying cannot fix, such as a
// record whose payload can never be encoded for the destination. A delivery
// function wraps it to say "do not hand me this record again"; the worker
// acknowledges the record so the outbox drains, and reports the error so the
// operator sees the record that was dropped.
var ErrPermanent = errors.New("outbox delivery failed permanently")

func (w Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w Worker) newTicker(interval time.Duration) (<-chan time.Time, func()) {
	if w.NewTicker != nil {
		return w.NewTicker(interval)
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// RunOnce claims a batch and delivers it record by record.
//
// Each record is acknowledged as soon as its own delivery succeeds, so a later
// failure in the same batch cannot re-deliver work that already reached the
// destination. A retryable failure releases the failed record together with the
// records still claimed behind it, so another worker can take them
// immediately instead of waiting out the lease.
//
// The lease is renewed for the life of the whole batch, not for the life of one
// delivery. ClaimEvents sets one expiry for every claimed sequence, so the
// deadline the batch has to beat is "the batch finishes", not "a record
// finishes": with the shipped defaults a hundred records at 400 ms each outlive
// a thirty-second lease by half a batch, and every record after the expiry is
// delivered to the destination and then fails its acknowledgement with a lease
// conflict — a duplicate delivery for that record and an abandoned tail for the
// rest. Renewing per record cannot see that, because a delivery shorter than
// Lease/3 never reaches its own first tick.
func (w Worker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	records, err := w.Source.ClaimEvents(ctx, workspace, w.Owner, w.Limit, w.Lease)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	outstanding := make([]uint64, 0, len(records))
	for _, record := range records {
		outstanding = append(outstanding, record.Sequence)
	}
	batchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	// Renewal covers every sequence this worker still owns rather than the
	// prefix it has already processed. Renewing only the prefix lets the tail's
	// lease expire mid-batch, which is exactly how two workers end up delivering
	// the same event.
	hold := w.holdLease(batchContext, cancel, outstanding)
	delivered, err := w.deliverBatch(ctx, batchContext, records, outstanding, hold)
	// A renewal that fails while the last delivery is returning is still a lost
	// lease. Stopping the hold before the result is read is what makes the
	// report total rather than dependent on which goroutine finished first.
	hold.stop()
	return delivered, hold.precedence(err)
}

func (w Worker) deliverBatch(ctx, batchContext context.Context, records []events.Record, outstanding []uint64, hold *leaseHold) (int, error) {
	delivered := 0
	var permanent []error
	for index, record := range records {
		deliveryErr := hold.precedence(w.Deliver(batchContext, record))
		if deliveryErr != nil && !errors.Is(deliveryErr, ErrPermanent) {
			retryAt := w.now().Add(w.Lease)
			if releaseErr := w.Source.ReleaseEvents(ctx, w.Owner, outstanding[index:], retryAt); releaseErr != nil {
				return delivered, errors.Join(deliveryErr, releaseErr)
			}
			return delivered, deliveryErr
		}
		if err := hold.acknowledge(outstanding[index+1:], func() error {
			return w.Source.AckEvents(ctx, w.Owner, []uint64{record.Sequence})
		}); err != nil {
			// The record itself reached the destination, so it must not be
			// released. The tail behind it has not, and leaving it leased by an
			// owner that is about to return makes it unavailable to every worker
			// until the lease lapses — a full lease period of stalled delivery
			// caused by one transient store error.
			if tail := outstanding[index+1:]; len(tail) != 0 {
				if releaseErr := w.Source.ReleaseEvents(ctx, w.Owner, tail, w.now().Add(w.Lease)); releaseErr != nil {
					return delivered, errors.Join(deliveryErr, err, releaseErr)
				}
			}
			return delivered, errors.Join(deliveryErr, err)
		}
		if deliveryErr != nil {
			permanent = append(permanent, deliveryErr)
			continue
		}
		delivered++
	}
	return delivered, errors.Join(permanent...)
}

// leaseHold renews the lease on the sequences a worker still owns, for as long
// as the worker owns them.
type leaseHold struct {
	mu        sync.Mutex
	sequences []uint64
	failure   error

	// renewal serialises a renewal against the acknowledgement that removes a
	// sequence from the set. Without it a renewal that read the set just before
	// an acknowledgement names a sequence the worker no longer owns, and the
	// store rejects the worker's own lease as a conflict.
	renewal sync.Mutex

	cancel   context.CancelFunc
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (w Worker) holdLease(ctx context.Context, cancel context.CancelFunc, sequences []uint64) *leaseHold {
	hold := &leaseHold{sequences: append([]uint64(nil), sequences...), cancel: cancel, done: make(chan struct{}), finished: make(chan struct{})}
	interval := w.Lease / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticks, stopTicker := w.newTicker(interval)
	go func() {
		defer close(hold.finished)
		defer stopTicker()
		for {
			select {
			case <-hold.done:
				return
			case <-ticks:
				hold.renewal.Lock()
				covered := hold.covered()
				var renewErr error
				if len(covered) != 0 {
					renewErr = w.Source.RenewEvents(ctx, w.Owner, covered, w.Lease)
				}
				hold.renewal.Unlock()
				if renewErr != nil {
					hold.fail(renewErr)
					cancel()
					return
				}
			}
		}
	}()
	return hold
}

// acknowledge acknowledges a record and narrows the hold to what is left, under
// the same lock a renewal takes. An acknowledged sequence must leave the
// renewal set: renewing a sequence this worker no longer owns is a lease
// conflict against itself, and the worker would report its own bookkeeping as a
// lost lease.
func (hold *leaseHold) acknowledge(remaining []uint64, ack func() error) error {
	hold.renewal.Lock()
	defer hold.renewal.Unlock()
	if err := ack(); err != nil {
		return err
	}
	hold.mu.Lock()
	hold.sequences = append(hold.sequences[:0], remaining...)
	hold.mu.Unlock()
	return nil
}

func (hold *leaseHold) covered() []uint64 {
	hold.mu.Lock()
	defer hold.mu.Unlock()
	return append([]uint64(nil), hold.sequences...)
}

func (hold *leaseHold) fail(err error) {
	hold.mu.Lock()
	defer hold.mu.Unlock()
	if hold.failure == nil {
		hold.failure = err
	}
}

// stop ends renewal and waits for an in-flight renewal to return. It cancels
// the batch first so a renewal blocked on an unresponsive store is interrupted
// rather than holding the worker open for the store's own timeout.
func (hold *leaseHold) stop() {
	hold.once.Do(func() {
		hold.cancel()
		close(hold.done)
	})
	<-hold.finished
}

// precedence decides which of a lost lease and a failed delivery describes what
// went wrong. A renewal failure cancels the batch, so the delivery it
// interrupted reports a cancellation that would otherwise hide the real cause.
func (hold *leaseHold) precedence(deliveryErr error) error {
	hold.mu.Lock()
	renewalErr := hold.failure
	hold.mu.Unlock()
	if renewalErr == nil || errors.Is(renewalErr, context.Canceled) {
		return deliveryErr
	}
	if deliveryErr == nil || errors.Is(deliveryErr, context.Canceled) {
		return renewalErr
	}
	return deliveryErr
}
