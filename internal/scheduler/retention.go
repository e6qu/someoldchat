package scheduler

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// RetentionInterval is how often one conversation may be swept. Slack runs its
// deletions once a day and says so, which is also why content past the horizon
// stays readable until the sweep reaches it: this worker is the only thing that
// enforces retention, and it is deliberately not instantaneous.
const RetentionInterval = 24 * time.Hour

// RetentionSource is a compare-and-set queue, like ScheduledStatusSource. A
// conversation's sweep watermark is its claim: advancing it is the same
// statement that selects it, so two workers cannot both take the same
// conversation. Losing the race is harmless because deleting content that is
// already gone is a no-op — which is why this needs no lease.
type RetentionSource interface {
	ClaimRetentionSweep(context.Context, domain.WorkspaceID, time.Time, time.Time, int) ([]domain.ConversationID, error)
	GetConversation(context.Context, domain.ConversationID) (domain.Conversation, error)
	GetRetentionPolicy(context.Context, domain.WorkspaceID) (domain.RetentionPolicy, error)
	GetConversationRetention(context.Context, domain.WorkspaceID, domain.ConversationID) (domain.ConversationRetention, error)
	SweepRetention(context.Context, domain.RetentionSweepRequest) (domain.RetentionSweep, error)
	// AppendRetentionEvents journals the sweep's announcements after the
	// deletion has committed. It is a second transaction on purpose: the
	// counts an event carries are only known once the rows are gone, and
	// holding the deletion open to build them would be worse than the window
	// this leaves. If it fails, the content is still correctly deleted and the
	// orphaned bytes are still reclaimed — cmd/blobgc walks live references
	// rather than trusting these events — so the loss is telemetry, not data.
	AppendRetentionEvents(context.Context, domain.WorkspaceID, []events.Event) error
}

type RetentionWorker struct {
	Source RetentionSource
	// Limit bounds both how many conversations one pass claims and how much
	// content it deletes from each, so a workspace with years of backlog drains
	// over many cycles rather than holding one transaction open across all of
	// it.
	Limit int
}

func NewRetentionWorker(source RetentionSource, limit int) (RetentionWorker, error) {
	if source == nil || limit <= 0 {
		return RetentionWorker{}, errors.New("retention worker requires a source and positive limit")
	}
	return RetentionWorker{Source: source, Limit: limit}, nil
}

func (w RetentionWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

func (w RetentionWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	now = now.UTC().Truncate(time.Second)
	claimed, err := w.Source.ClaimRetentionSweep(ctx, workspaceID, now.Add(-RetentionInterval), now, w.Limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, conversationID := range claimed {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		swept, err := w.sweep(ctx, conversationID, now)
		if err != nil {
			// One conversation that cannot be swept must not stop the batch:
			// the others' policies are unrelated to it, and a workspace whose
			// retention silently stopped for everyone because one channel is
			// broken is worse than a workspace missing one channel's sweep.
			failures = errors.Join(failures, err)
			continue
		}
		completed++
		_ = swept
	}
	return completed, failures
}

func (w RetentionWorker) sweep(ctx context.Context, conversationID domain.ConversationID, now time.Time) (domain.RetentionSweep, error) {
	conversation, err := w.Source.GetConversation(ctx, conversationID)
	if err != nil {
		return domain.RetentionSweep{}, err
	}
	policy, err := w.Source.GetRetentionPolicy(ctx, conversation.WorkspaceID)
	if err != nil {
		return domain.RetentionSweep{}, err
	}
	override, err := w.Source.GetConversationRetention(ctx, conversation.WorkspaceID, conversationID)
	if err != nil {
		return domain.RetentionSweep{}, err
	}
	request := domain.RetentionSweepRequest{
		WorkspaceID:    conversation.WorkspaceID,
		ConversationID: conversationID,
		MessageHorizon: domain.RetentionHorizon(override.Effective(policy), now),
		// Files have no per-channel override: Slack's per-channel API takes one
		// duration and it governs messages. A file's fate follows the
		// workspace policy wherever it was shared.
		FileHorizon: domain.RetentionHorizon(policy.FileDays, now),
		SweptAt:     now,
		Limit:       w.Limit,
	}
	if request.MessageHorizon.IsZero() && request.FileHorizon.IsZero() {
		// Nothing is governed here, so there is nothing to delete and nothing
		// worth announcing. The watermark has already been advanced by the
		// claim, which is what keeps an unconfigured workspace cheap.
		return domain.RetentionSweep{ConversationID: conversationID, SweptAt: now, Complete: true}, nil
	}
	swept, err := w.Source.SweepRetention(ctx, request)
	if err != nil {
		return domain.RetentionSweep{}, err
	}
	if swept.Messages == 0 && swept.Files == 0 {
		return swept, nil
	}
	emitted, err := retentionEvents(conversation.WorkspaceID, swept, now)
	if err != nil {
		return domain.RetentionSweep{}, err
	}
	if err := w.Source.AppendRetentionEvents(ctx, conversation.WorkspaceID, emitted); err != nil {
		return domain.RetentionSweep{}, err
	}
	return swept, nil
}

// retentionEvents builds one summary event per conversation plus one
// blob-delete event per orphaned file.
//
// Deliberately not one event per deleted message. Slack emits no event for
// retention deletion at all, and a workspace draining years of backlog would
// otherwise write millions of records into the journal that this product's own
// retention does not remove.
func retentionEvents(workspaceID domain.WorkspaceID, swept domain.RetentionSweep, now time.Time) ([]events.Event, error) {
	emitted := make([]events.Event, 0, len(swept.ExpiredBlobs)+1)
	summary, err := newRetentionEvent(workspaceID, events.NewPayload("retention.swept",
		events.String("channel_id", string(swept.ConversationID)),
		events.String("messages", strconv.Itoa(swept.Messages)),
		events.String("files", strconv.Itoa(swept.Files)),
	), now)
	if err != nil {
		return nil, err
	}
	emitted = append(emitted, summary)
	for _, blob := range swept.ExpiredBlobs {
		// The same topic an ordinary file deletion emits, so the existing blob
		// cleanup worker reclaims these bytes through the path it already has.
		event, err := newRetentionEvent(workspaceID, events.BlobKey(events.FileBlobDeleteTopic, blob.BlobKey), now)
		if err != nil {
			return nil, err
		}
		emitted = append(emitted, event)
	}
	return emitted, nil
}

// newRetentionEvent carries no actor. A sweep is the workspace's own policy
// applying itself on a schedule, not something a person did, and naming the
// administrator who last edited the policy as the actor would misattribute
// every deletion for as long as the policy stands.
func newRetentionEvent(workspaceID domain.WorkspaceID, payload events.Payload, now time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, workspaceID, "", payload, now)
}
