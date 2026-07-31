package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// WorkflowScheduleSource is the scheduled-trigger queue. A trigger's
// NextRunAt is both its next fire time and the compare-and-set fence for
// completing a fire, so an owner edit or disable that raced the worker makes
// the stale fire a no-op.
type WorkflowScheduleSource interface {
	DueScheduledWorkflowTriggers(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.WorkflowTrigger, error)
	EarliestScheduledWorkflowTrigger(context.Context, domain.WorkspaceID) (time.Time, error)
	CompleteScheduledWorkflowTrigger(context.Context, domain.WorkspaceID, domain.WorkflowTriggerID, time.Time, time.Time, events.Event) (bool, error)
}

// WorkflowTriggerRunner starts a run for a fired trigger. It is the chat
// module seam, so the worker executes the same path in local and distributed
// composition.
type WorkflowTriggerRunner interface {
	RunAutomaticWorkflow(context.Context, domain.WorkspaceID, domain.WorkflowTriggerID, domain.ConversationID, string, string) (domain.WorkflowRun, error)
	DispatchWorkflowEventTriggers(context.Context, domain.WorkspaceID, int) (int, error)
}

type WorkflowScheduleWorker struct {
	Source WorkflowScheduleSource
	Runner WorkflowTriggerRunner
	Limit  int
}

func NewWorkflowScheduleWorker(source WorkflowScheduleSource, runner WorkflowTriggerRunner, limit int) (WorkflowScheduleWorker, error) {
	if source == nil || runner == nil || limit <= 0 {
		return WorkflowScheduleWorker{}, errors.New("workflow schedule worker requires a source, runner, and positive limit")
	}
	return WorkflowScheduleWorker{Source: source, Runner: runner, Limit: limit}, nil
}

func (w WorkflowScheduleWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

// RunOnceAt fires every scheduled trigger whose occurrence is due at `now`.
//
// A run is started before its schedule advances, and the fire's idempotency
// key is derived from the trigger and the occurrence, so a crash between the
// two, or two workers racing one occurrence, produces one run. A run refused
// because the workflow was unpublished or the trigger disabled after the due
// read still advances the schedule: the occurrence was due, the refusal is
// the recorded outcome, and leaving the trigger due would fire it into the
// same refusal every poll. Any other failure leaves the schedule untouched so
// the next cycle retries.
func (w WorkflowScheduleWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	now = now.UTC().Truncate(time.Second)
	triggers, err := w.Source.DueScheduledWorkflowTriggers(ctx, workspaceID, now, w.Limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures error
	for _, trigger := range triggers {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		workspace := trigger.WorkspaceID
		occurrence := trigger.NextRunAt
		idempotency := "scheduled:" + string(trigger.ID) + ":" + occurrence.UTC().Format(time.RFC3339)
		_, runErr := w.Runner.RunAutomaticWorkflow(ctx, workspace, trigger.ID, "", "{}", idempotency)
		if runErr != nil && !errors.Is(runErr, store.ErrConflict) {
			failures = errors.Join(failures, runErr)
			continue
		}
		next, nextErr := service.NextWorkflowScheduledRun(trigger.Config, occurrence, false)
		if nextErr != nil {
			failures = errors.Join(failures, nextErr)
			continue
		}
		event, eventErr := workflowTriggerFiredEvent(trigger, occurrence, now)
		if eventErr != nil {
			failures = errors.Join(failures, eventErr)
			continue
		}
		advanced, completeErr := w.Source.CompleteScheduledWorkflowTrigger(ctx, workspace, trigger.ID, occurrence, next, event)
		if completeErr != nil {
			failures = errors.Join(failures, completeErr)
			continue
		}
		if advanced {
			completed++
		}
	}
	return completed, failures
}

func workflowTriggerFiredEvent(trigger domain.WorkflowTrigger, occurrence, firedAt time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, trigger.WorkspaceID, "", events.NewPayload(
		"workflow.trigger_fired",
		events.String("workflow_id", string(trigger.WorkflowID)),
		events.String("trigger_id", string(trigger.ID)),
		events.String("trigger_type", trigger.Type),
		events.String("occurrence", occurrence.UTC().Format(time.RFC3339)),
	), firedAt)
}

// WorkflowEventWorker tails the durable event journal and fires message,
// reaction, join, and list triggers. All matching state lives in the store
// behind the runner, so the worker itself carries no cursor.
type WorkflowEventWorker struct {
	Runner WorkflowTriggerRunner
	Limit  int
}

func NewWorkflowEventWorker(runner WorkflowTriggerRunner, limit int) (WorkflowEventWorker, error) {
	if runner == nil || limit <= 0 {
		return WorkflowEventWorker{}, errors.New("workflow event worker requires a runner and positive limit")
	}
	return WorkflowEventWorker{Runner: runner, Limit: limit}, nil
}

func (w WorkflowEventWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.Runner.DispatchWorkflowEventTriggers(ctx, workspaceID, w.Limit)
}
