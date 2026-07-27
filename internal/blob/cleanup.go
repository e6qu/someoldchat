package blob

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/lease"
)

type CleanupSource interface {
	ClaimEventsForTopic(context.Context, domain.WorkspaceID, string, string, int, time.Duration) ([]events.Record, error)
	RenewEvents(context.Context, string, []uint64, time.Duration) error
	AckEvents(context.Context, string, []uint64) error
	ReleaseEvents(context.Context, string, []uint64, time.Time) error
}

type CleanupWorker struct {
	Source CleanupSource
	Store  Store
	Owner  string
	Limit  int
	Lease  time.Duration
}

func NewCleanupWorker(source CleanupSource, objects Store, owner string, limit int, lease time.Duration) (CleanupWorker, error) {
	if source == nil || objects == nil || owner == "" || limit <= 0 || lease <= 0 {
		return CleanupWorker{}, errors.New("blob cleanup requires source, store, owner, positive limit, and positive lease")
	}
	return CleanupWorker{Source: source, Store: objects, Owner: owner, Limit: limit, Lease: lease}, nil
}

func (w CleanupWorker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	count, err := w.runTopic(ctx, workspace, events.FileBlobDeleteTopic)
	if err != nil {
		return count, err
	}
	photoCount, err := w.runTopic(ctx, workspace, events.UserPhotoBlobDeleteTopic)
	return count + photoCount, err
}

func (w CleanupWorker) runTopic(ctx context.Context, workspace domain.WorkspaceID, topic string) (int, error) {
	completed := 0
	for completed < w.Limit {
		records, err := w.Source.ClaimEventsForTopic(ctx, workspace, topic, w.Owner, 1, w.Lease)
		if err != nil {
			return completed, err
		}
		if len(records) > 1 {
			return completed, errors.New("blob cleanup source returned more records than requested")
		}
		if len(records) == 0 {
			return completed, nil
		}
		record := records[0]
		sequence := []uint64{record.Sequence}
		if err := w.deleteWithLease(ctx, record); err != nil {
			retryAt := time.Now().UTC().Add(w.Lease)
			if releaseErr := w.Source.ReleaseEvents(ctx, w.Owner, sequence, retryAt); releaseErr != nil {
				return completed, releaseErr
			}
			return completed, err
		}
		if err := w.Source.AckEvents(ctx, w.Owner, sequence); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

// deleteWithLease deletes one object while holding its lease. A lost lease means
// another worker is deleting the same object, which is the condition behind
// duplicate deletion, so lease.While always surfaces it joined with the delete's
// own error rather than dropping it in favour of one or the other.
func (w CleanupWorker) deleteWithLease(ctx context.Context, record events.Record) error {
	return lease.While(ctx, w.Lease,
		func(renewContext context.Context) error {
			return w.Source.RenewEvents(renewContext, w.Owner, []uint64{record.Sequence}, w.Lease)
		},
		func(deleteContext context.Context) error {
			// An object that is already gone is a completed deletion, not a
			// failure: the topic exists to make deletion idempotent.
			if err := w.Store.Delete(deleteContext, record.Event.Payload); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			return nil
		},
	)
}
