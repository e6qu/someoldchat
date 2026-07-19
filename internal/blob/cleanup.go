package blob

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
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

func (w CleanupWorker) deleteWithLease(ctx context.Context, record events.Record) error {
	deleteContext, cancel := context.WithCancel(ctx)
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
				if err := w.Source.RenewEvents(deleteContext, w.Owner, []uint64{record.Sequence}, w.Lease); err != nil {
					cancel()
					renewErrors <- err
					return
				}
			}
		}
	}()
	deleteErr := w.Store.Delete(deleteContext, record.Event.Payload)
	if errors.Is(deleteErr, ErrNotFound) {
		deleteErr = nil
	}
	cancel()
	close(done)
	<-renewDone
	select {
	case err := <-renewErrors:
		if !errors.Is(err, context.Canceled) && (deleteErr == nil || errors.Is(deleteErr, context.Canceled)) {
			return err
		}
	default:
	}
	return deleteErr
}
