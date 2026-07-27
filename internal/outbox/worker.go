package outbox

import (
	"context"
	"errors"
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

// RunOnce claims a batch and delivers it record by record.
//
// Each record is acknowledged as soon as its own delivery succeeds, so a later
// failure in the same batch cannot re-deliver work that already reached the
// destination. A retryable failure releases the failed record together with the
// records still claimed behind it, so another worker can take them
// immediately instead of waiting out the lease.
func (w Worker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	records, err := w.Source.ClaimEvents(ctx, workspace, w.Owner, w.Limit, w.Lease)
	if err != nil {
		return 0, err
	}
	outstanding := make([]uint64, 0, len(records))
	for _, record := range records {
		outstanding = append(outstanding, record.Sequence)
	}
	delivered := 0
	var permanent []error
	for index, record := range records {
		// Renewal covers every sequence this worker still owns rather than the
		// prefix it has already processed. Renewing only the prefix lets the
		// tail's lease expire mid-batch, which is exactly how two workers end
		// up delivering the same event.
		deliveryErr := w.deliverWithLease(ctx, outstanding[index:], record)
		if deliveryErr != nil && !errors.Is(deliveryErr, ErrPermanent) {
			retryAt := time.Now().UTC().Add(w.Lease)
			if releaseErr := w.Source.ReleaseEvents(ctx, w.Owner, outstanding[index:], retryAt); releaseErr != nil {
				return delivered, errors.Join(deliveryErr, releaseErr)
			}
			return delivered, deliveryErr
		}
		if err := w.Source.AckEvents(ctx, w.Owner, []uint64{record.Sequence}); err != nil {
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

func (w Worker) deliverWithLease(ctx context.Context, sequences []uint64, record events.Record) error {
	deliveryContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErrors := make(chan error, 1)
	done := make(chan struct{})
	renewDone := make(chan struct{})
	interval := w.Lease / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := w.Source.RenewEvents(deliveryContext, w.Owner, sequences, w.Lease); err != nil {
					cancel()
					renewErrors <- err
					return
				}
			}
		}
	}()
	deliveryError := w.Deliver(deliveryContext, record)
	cancel()
	close(done)
	<-renewDone
	select {
	case renewalError := <-renewErrors:
		if !errors.Is(renewalError, context.Canceled) && (deliveryError == nil || errors.Is(deliveryError, context.Canceled)) {
			return renewalError
		}
	default:
	}
	return deliveryError
}
