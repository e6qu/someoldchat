package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// WorkflowDelayInterval is how often a deployment should sweep for delay steps
// that have come due. It bounds how late a wait can resume, which is the only
// promise a delay step makes beyond "not before": Slack's own waits are not
// second-accurate either, and a shorter sweep buys precision nobody asked for
// at the cost of a query per replica per tick.
const WorkflowDelayInterval = 30 * time.Second

// WorkflowDelaySource is the half of the service a delay worker needs.
type WorkflowDelaySource interface {
	ResumeWorkflowDelays(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time, limit int) (int, error)
}

// WorkflowDelayWorker resumes runs parked on Slack's "wait for a set time"
// step.
//
// It carries no lease of its own. Advancing a step is a compare-and-set against
// the run's current position — AdvanceWorkflowRun refuses a step that is no
// longer current — so two replicas sweeping the same workspace cannot both
// resume the same run, and the loser sees a conflict rather than a duplicate.
// That is the same reason the scheduled-status worker holds no lease.
type WorkflowDelayWorker struct {
	Source WorkflowDelaySource
	Limit  int
}

func NewWorkflowDelayWorker(source WorkflowDelaySource, limit int) (WorkflowDelayWorker, error) {
	if source == nil || limit <= 0 {
		return WorkflowDelayWorker{}, errors.New("workflow delay worker requires a source and positive limit")
	}
	return WorkflowDelayWorker{Source: source, Limit: limit}, nil
}

func (w WorkflowDelayWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

// RunOnceAt resumes every delay that is due at the given instant, up to the
// worker's limit. A run is never resumed early: the store selects on the wake
// time it recorded, so a sweep that fires between two waits moves neither.
func (w WorkflowDelayWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	return w.Source.ResumeWorkflowDelays(ctx, workspaceID, now.UTC(), w.Limit)
}
