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

func (w Worker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	records, err := w.Source.ClaimEvents(ctx, workspace, w.Owner, w.Limit, w.Lease)
	if err != nil {
		return 0, err
	}
	sequences := make([]uint64, 0, len(records))
	for _, record := range records {
		sequences = append(sequences, record.Sequence)
		if err := w.deliverWithLease(ctx, sequences, record); err != nil {
			retryAt := time.Now().UTC().Add(w.Lease)
			if releaseErr := w.Source.ReleaseEvents(ctx, w.Owner, sequences, retryAt); releaseErr != nil {
				return len(sequences), releaseErr
			}
			return len(sequences) - 1, err
		}
	}
	if len(sequences) == 0 {
		return 0, nil
	}
	if err := w.Source.AckEvents(ctx, w.Owner, sequences); err != nil {
		return len(sequences), err
	}
	return len(sequences), nil
}

func (w Worker) deliverWithLease(ctx context.Context, sequences []uint64, record events.Record) error {
	deliveryContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErrors := make(chan error, 1)
	done := make(chan struct{})
	interval := w.Lease / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := w.Source.RenewEvents(ctx, w.Owner, sequences, w.Lease); err != nil {
					cancel()
					renewErrors <- err
					return
				}
			}
		}
	}()
	deliveryError := w.Deliver(deliveryContext, record)
	close(done)
	select {
	case renewalError := <-renewErrors:
		if deliveryError == nil {
			return renewalError
		}
	default:
	}
	return deliveryError
}
