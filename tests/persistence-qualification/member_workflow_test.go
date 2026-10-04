package qualification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// A member's own workflow has no app, as Workflow Builder's do not: every
// profile stores it, its trigger, its run and the run's step with no app, and
// every profile refuses a step whose app is not its run's — the invariant
// that keeps an app's step out of a member's run and a member's step out of
// an app's.
func memberOwnedWorkflowsAreStoredOnEveryProfile(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()
	now := time.Now().UTC().Truncate(time.Microsecond)
	workflowID := domain.WorkflowID("Wf-member-" + f.suffix)
	workflow := domain.WorkflowDefinition{
		ID: workflowID, WorkspaceID: f.workspaceID, OwnerID: f.userID, Title: "Welcome",
		InputSchema: `{}`, Steps: `[{"id":"say","type":"message","message":{"conversation":"C","text":"hi"}}]`,
		Status: domain.WorkflowPublished, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.repository.CreateWorkflow(ctx, workflow, f.event("member-workflow", "workflow.created", string(workflowID))); err != nil {
		t.Fatalf("create a workflow with no app: %v", err)
	}
	stored, err := f.repository.GetWorkflow(ctx, f.workspaceID, workflowID)
	if err != nil || stored.AppID != "" || stored.OwnerID != f.userID {
		t.Fatalf("stored=%+v err=%v, want the member's workflow with no app", stored, err)
	}
	// Workflows with no app and no reference do not collide with each other.
	second := workflow
	second.ID = domain.WorkflowID("Wf-member-second-" + f.suffix)
	if err := f.repository.CreateWorkflow(ctx, second, f.event("member-workflow-second", "workflow.created", string(second.ID))); err != nil {
		t.Fatalf("a second workflow with no app: %v", err)
	}
	trigger := domain.WorkflowTrigger{
		ID: domain.WorkflowTriggerID("Ft-member-" + f.suffix), WorkflowID: workflowID, WorkspaceID: f.workspaceID,
		Title: "Start", Type: "link", Config: `{}`, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.repository.SetWorkflowTrigger(ctx, trigger, f.event("member-trigger", "workflow.trigger_created", string(trigger.ID))); err != nil {
		t.Fatalf("a trigger with no app: %v", err)
	}
	run := domain.WorkflowRun{
		ID: domain.WorkflowRunID("Wx-member-" + f.suffix), WorkflowID: workflowID, WorkflowVersion: 1, TriggerID: trigger.ID,
		WorkspaceID: f.workspaceID, ActorID: f.userID, ConversationID: f.channelID, Status: domain.WorkflowRunRunning,
		Inputs: `{}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	step := func(id string, app domain.AppID) *domain.WorkflowStep {
		return &domain.WorkflowStep{
			ID: domain.WorkflowStepID(id + f.suffix), WorkflowRunID: run.ID, WorkspaceID: f.workspaceID, AppID: app,
			UserID: f.userID, Status: domain.WorkflowStepWaiting, Inputs: `{}`, Outputs: `{}`, StepName: "form",
			CreatedAt: now, UpdatedAt: now,
		}
	}
	started := []events.Event{f.event("member-run", "workflow.run_started", string(run.ID))}
	if err := f.repository.CreateWorkflowRun(ctx, run, step("Fx-app-", "A-elsewhere"), started); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("a step carrying an app in a run with none: err=%v, want ErrInvalidArgument", err)
	}
	if err := f.repository.CreateWorkflowRun(ctx, run, step("Fx-member-", ""), started); err != nil {
		t.Fatalf("a run and step with no app: %v", err)
	}
	if got, err := f.repository.GetWorkflowRun(ctx, f.workspaceID, run.ID); err != nil || got.AppID != "" || got.Status != domain.WorkflowRunRunning {
		t.Fatalf("run=%+v err=%v", got, err)
	}
}
