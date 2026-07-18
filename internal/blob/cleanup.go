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
	records, err := w.Source.ClaimEventsForTopic(ctx, workspace, topic, w.Owner, w.Limit, w.Lease)
	if err != nil {
		return 0, err
	}
	sequences := make([]uint64, 0, len(records))
	for _, record := range records {
		sequences = append(sequences, record.Sequence)
		if err := w.Store.Delete(ctx, record.Event.Payload); err != nil && !errors.Is(err, ErrNotFound) {
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
