package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
)

type Source interface {
	ClaimScheduledMessages(context.Context, domain.WorkspaceID, string, int, time.Duration) ([]domain.ScheduledMessage, error)
	EarliestScheduledMessage(context.Context, domain.WorkspaceID) (time.Time, error)
	RenewScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Duration) error
	MarkScheduledMessageDelivered(context.Context, string, domain.ScheduledMessageID) error
	ReleaseScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Time) error
}

type Worker struct {
	Source Source
	Poster chatapi.Service
	Owner  string
	Limit  int
	Lease  time.Duration
}

func NewWorker(source Source, poster chatapi.Service, owner string, limit int, lease time.Duration) (Worker, error) {
	if source == nil || poster == nil || owner == "" || limit <= 0 || lease <= 0 {
		return Worker{}, errors.New("scheduled worker requires source, poster, owner, positive limit, and lease")
	}
	return Worker{Source: source, Poster: poster, Owner: owner, Limit: limit, Lease: lease}, nil
}

func (w Worker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	items, err := w.Source.ClaimScheduledMessages(ctx, workspace, w.Owner, w.Limit, w.Lease)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, item := range items {
		if err := w.postWithLease(ctx, item); err != nil {
			if releaseErr := w.Source.ReleaseScheduledMessage(ctx, w.Owner, item.ID, time.Now().UTC().Add(w.Lease)); releaseErr != nil {
				return completed, releaseErr
			}
			return completed, err
		}
		if err := w.Source.MarkScheduledMessageDelivered(ctx, w.Owner, item.ID); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

func (w Worker) PublishWakeDeadline(ctx context.Context, publisher DeadlinePublisher, workspace domain.WorkspaceID, fence uint64) error {
	return PublishWakeDeadline(ctx, w.Source, publisher, workspace, fence)
}

func (w Worker) postWithLease(ctx context.Context, item domain.ScheduledMessage) error {
	postContext, cancel := context.WithCancel(ctx)
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
				if err := w.Source.RenewScheduledMessage(postContext, w.Owner, item.ID, w.Lease); err != nil {
					cancel()
					renewErrors <- err
					return
				}
			}
		}
	}()
	_, postErr := w.Poster.Post(postContext, item.WorkspaceID, item.Author, item.Channel, item.Text, "", string(item.ID))
	cancel()
	close(done)
	<-renewDone
	select {
	case renewErr := <-renewErrors:
		if !errors.Is(renewErr, context.Canceled) && (postErr == nil || errors.Is(postErr, context.Canceled)) {
			return renewErr
		}
	default:
	}
	return postErr
}
