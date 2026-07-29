package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/lease"
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

// RunOnce posts every message in one claimed batch.
//
// A failing item no longer abandons the rest of the batch. Returning on the first
// error left every later item claimed by this owner, so nothing could touch them
// until the lease expired: one message that could not be posted stalled the whole
// workspace's schedule for a lease period on every cycle, and the batch was
// re-claimed and stalled again. Each item is now released or acknowledged in its
// own right and the failures are reported together.
func (w Worker) RunOnce(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	items, err := w.Source.ClaimScheduledMessages(ctx, workspace, w.Owner, w.Limit, w.Lease)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		if postErr := w.postWithLease(ctx, item); postErr != nil {
			failures = errors.Join(failures, postErr)
			if releaseErr := w.Source.ReleaseScheduledMessage(ctx, w.Owner, item.ID, time.Now().UTC().Add(w.Lease)); releaseErr != nil {
				failures = errors.Join(failures, releaseErr)
			}
			continue
		}
		if err := w.Source.MarkScheduledMessageDelivered(ctx, w.Owner, item.ID); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		completed++
	}
	return completed, failures
}

func (w Worker) PublishWakeDeadline(ctx context.Context, publisher FencedDeadlinePublisher, workspaces ...domain.WorkspaceID) error {
	return PublishEarliestWakeDeadline(ctx, w.Source, publisher, workspaces...)
}

// postWithLease posts one scheduled message while holding its lease.
//
// A lost lease is always reported, joined with the post's own error rather than
// dropped in favour of it. It used to be suppressed whenever the post had also
// failed, which is the one combination that matters most: the caller then
// released the item for another replica believing only the post had failed,
// while a second owner had in fact been running the same post the whole time.
//
// Duplicate posting is prevented by the idempotency key, which is the scheduled
// message's own identifier: two owners that both post the same item produce one
// message. That contract is stated here because it is what makes releasing a
// failed item safe, and it was previously implicit.
func (w Worker) postWithLease(ctx context.Context, item domain.ScheduledMessage) error {
	return lease.While(ctx, w.Lease,
		func(renewContext context.Context) error {
			return w.Source.RenewScheduledMessage(renewContext, w.Owner, item.ID, w.Lease)
		},
		func(postContext context.Context) error {
			_, err := w.Poster.PostWithBlocksAndAttachments(postContext, item.WorkspaceID, item.Author, item.Channel, item.Text, item.Blocks, item.Attachments, "", string(item.ID), "")
			return err
		},
	)
}
