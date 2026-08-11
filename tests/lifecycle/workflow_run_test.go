package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A workflow run and its steps are one lifecycle wearing two status fields: a
// step is what moves the run, and neither can be driven without the other. They
// share this fixture for that reason, and it is a fixture rather than a seed —
// an app whose manifest declares the step's function, an installation, a
// published workflow, a trigger, and then a real run started from it. A seeded
// row would have skipped exactly the operations under test.
const (
	runWorkspace = domain.WorkspaceID("T1")
	runOwner     = domain.UserID("U-owner")
	runApp       = domain.AppID("A1")
	runChannel   = domain.ConversationID("C1")
	runFunction  = "triage"
)

type execution struct {
	messages service.Messages
	workflow domain.WorkflowID
	trigger  domain.WorkflowTriggerID
	run      domain.WorkflowRunID
	step     domain.WorkflowStepID
}

// startWorkflowRun builds the workspace, publishes a workflow with one step, and
// starts a run from its trigger.
func startWorkflowRun(t *testing.T) execution {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	if err := repository.SeedWorkspace(domain.Workspace{ID: runWorkspace, Name: "test"}); err != nil {
		t.Fatal(err)
	}
	membership := domain.WorkspaceMembership{WorkspaceID: runWorkspace, UserID: runOwner, Role: domain.WorkspaceRoleMember, Active: true}
	if err := repository.CreateUser(ctx, domain.User{ID: runOwner, WorkspaceID: runWorkspace, Name: "owner", Email: "owner@example.test"}, membership, events.Event{
		ID: "E-user", WorkspaceID: runWorkspace, Topic: "user.created", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateConversation(ctx, domain.Conversation{ID: runChannel, WorkspaceID: runWorkspace, Name: "general"}, runOwner, events.Event{
		ID: "E-conversation", WorkspaceID: runWorkspace, Topic: "conversation.created", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: runApp, DevelopmentWorkspaceID: runWorkspace, OwnerID: runOwner, Name: "Automation",
		ClientID: "client", SigningSecretHash: "signing-hash", SigningSecretCiphertext: "signing-ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "verification-ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: runApp, Version: 1, CreatedBy: runOwner, CreatedAt: now,
		Manifest: `{
			"display_information":{"name":"Automation"},
			"settings":{"function_runtime":"remote"},
			"functions":{"triage":{
				"title":"Triage incident","description":"Classifies one incident",
				"input_parameters":{"properties":{"incident":{"type":"string","title":"Incident"}},"required":["incident"]},
				"output_parameters":{"properties":{"priority":{"type":"integer","title":"Priority"}},"required":["priority"]}
			}}
		}`,
	}, domain.OAuthClient{ID: "client", AppID: runApp, SecretHash: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{
		AppID: runApp, WorkspaceID: runWorkspace, Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository}
	created, err := messages.CreateWorkflow(ctx, runWorkspace, runOwner, domain.WorkflowDefinition{
		WorkspaceID: runWorkspace, AppID: runApp, OwnerID: runOwner, CallbackID: "fixture",
		Title: "fixture workflow", InputSchema: `{}`,
		Steps: `[{"function_id":"` + runFunction + `","title":"Classify","inputs":{"incident":"one"}}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := messages.UpdateWorkflow(ctx, runWorkspace, runOwner, created, created.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, runWorkspace, runOwner, domain.WorkflowTrigger{
		WorkflowID: published.ID, WorkspaceID: runWorkspace, AppID: runApp, Title: "fixture trigger",
		// A shortcut rather than a webhook: a webhook trigger carries a minted
		// secret, and the fixture is about running the workflow rather than
		// about how it is invoked.
		Type: domain.WorkflowTriggerShortcut, Config: "{}", Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, runWorkspace, runOwner, trigger.ID, runChannel, `{"incident":"one"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := repository.ListWorkflowRunSteps(ctx, runWorkspace, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("a started run has no step, so there is nothing to advance it")
	}
	return execution{messages: messages, workflow: published.ID, trigger: trigger.ID, run: run.ID, step: steps[0].ID}
}

// TestTheWorkflowRunFixtureReachesEveryStateItClaims proves the fixture before
// two machines are judged against it. A driver whose setup silently lands in the
// wrong state reports the product's rules as broken when it is the fixture that
// is.
func TestTheWorkflowRunFixtureReachesEveryStateItClaims(t *testing.T) {
	started := startWorkflowRun(t)
	ctx := context.Background()
	run, err := started.messages.Store.GetWorkflowRun(ctx, runWorkspace, started.run)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.WorkflowRunRunning {
		t.Fatalf("a started run is %q, want running", run.Status)
	}
	step, err := started.messages.Store.GetWorkflowStep(ctx, runWorkspace, started.step)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a started run is %q and its first step is %q", run.Status, step.Status)

	// Every state the fixture offers, checked to be the state it lands in. This
	// began by asserting only the running case, and that was not enough: the
	// route to "completed" left the run running, so the driver judged the
	// machine against a run that was never in the state it claimed, and
	// reported the product breaking a rule it had not broken.
	for _, state := range []domain.WorkflowRunStatus{
		domain.WorkflowRunRunning, domain.WorkflowRunCompleted,
		domain.WorkflowRunFailed, domain.WorkflowRunCancelled,
	} {
		reached, ok := reachRunState(t, string(state))
		if !ok {
			t.Fatalf("the fixture cannot reach %q at all", state)
		}
		current, err := reached.messages.Store.GetWorkflowRun(ctx, runWorkspace, reached.run)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != state {
			t.Errorf("the route to %q leaves the run in %q", state, current.Status)
		}
	}
}

// reachRunState drives a fresh run to the requested state through the product's
// own operations, and reports the execution it left there.
func reachRunState(t *testing.T, state string) (execution, bool) {
	t.Helper()
	ctx := context.Background()
	started := startWorkflowRun(t)
	switch domain.WorkflowRunStatus(state) {
	case domain.WorkflowRunRunning:
		return started, true
	case domain.WorkflowRunCompleted:
		if err := started.messages.CompleteFunction(ctx, runWorkspace, runOwner, runApp, started.step, `{"priority":1}`, ""); err != nil {
			t.Fatalf("reaching %s: %v", state, err)
		}
		return started, true
	case domain.WorkflowRunFailed:
		if err := started.messages.CompleteFunction(ctx, runWorkspace, runOwner, runApp, started.step, "", "the step failed"); err != nil {
			t.Fatalf("reaching %s: %v", state, err)
		}
		return started, true
	case domain.WorkflowRunCancelled:
		// Nothing cancels a run directly. Taking its workflow offline cancels
		// the runs still in flight, and that is the door to use: DELETING the
		// workflow also marks them cancelled and then removes them in the same
		// pass, so nothing is left to observe.
		if err := unpublishWorkflow(ctx, started); err != nil {
			t.Fatalf("reaching %s: %v", state, err)
		}
		return started, true
	}
	// queued is unreachable: runWorkflow creates a run already running and
	// nothing else sets the state. Reported by the machine rather than skipped
	// silently.
	return execution{}, false
}

func workflowRunDriver() driver {
	var current execution
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			started, ok := reachRunState(t, state)
			if !ok {
				return service.Messages{}, false
			}
			current = started
			return started.messages, true
		},
		attempt: func(messages service.Messages, from, to string) error {
			ctx := context.Background()
			switch domain.WorkflowRunStatus(to) {
			case domain.WorkflowRunCompleted:
				return messages.CompleteFunction(ctx, runWorkspace, runOwner, runApp, current.step, `{"priority":1}`, "")
			case domain.WorkflowRunFailed:
				return messages.CompleteFunction(ctx, runWorkspace, runOwner, runApp, current.step, "", "the step failed")
			case domain.WorkflowRunCancelled:
				return unpublishWorkflow(ctx, current)
			}
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			run, err := messages.Store.GetWorkflowRun(context.Background(), runWorkspace, current.run)
			if err != nil {
				t.Fatal(err)
			}
			return string(run.Status)
		},
	}
}

func workflowStepDriver() driver {
	var current execution
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			// A step's states map onto the run's: its first step is executing
			// when the run starts, completed or failed when it is told so, and
			// cancelled when the workflow is deleted underneath it. "configured"
			// and "waiting" belong to a step that is not the one in flight.
			var runState string
			switch domain.WorkflowStepStatus(state) {
			case domain.WorkflowStepExecuting:
				runState = string(domain.WorkflowRunRunning)
			case domain.WorkflowStepCompleted:
				runState = string(domain.WorkflowRunCompleted)
			case domain.WorkflowStepFailed:
				runState = string(domain.WorkflowRunFailed)
			case domain.WorkflowStepCancelled:
				runState = string(domain.WorkflowRunCancelled)
			default:
				return service.Messages{}, false
			}
			started, ok := reachRunState(t, runState)
			if !ok {
				return service.Messages{}, false
			}
			current = started
			return started.messages, true
		},
		attempt: func(messages service.Messages, from, to string) error {
			ctx := context.Background()
			switch domain.WorkflowStepStatus(to) {
			case domain.WorkflowStepCompleted:
				return messages.CompleteFunction(ctx, runWorkspace, runOwner, runApp, current.step, `{"priority":1}`, "")
			case domain.WorkflowStepFailed:
				return messages.CompleteFunction(ctx, runWorkspace, runOwner, runApp, current.step, "", "the step failed")
			case domain.WorkflowStepCancelled:
				return unpublishWorkflow(ctx, current)
			}
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			step, err := messages.Store.GetWorkflowStep(context.Background(), runWorkspace, current.step)
			if err != nil {
				t.Fatal(err)
			}
			return string(step.Status)
		},
	}
}

// unpublishWorkflow takes the workflow offline, which cancels the runs still in
// flight and the steps they are executing.
func unpublishWorkflow(ctx context.Context, current execution) error {
	workflow, err := current.messages.Store.GetWorkflow(ctx, runWorkspace, current.workflow)
	if err != nil {
		return err
	}
	workflow.Status = domain.WorkflowDisabled
	_, err = current.messages.UpdateWorkflow(ctx, runWorkspace, runOwner, workflow, workflow.Version, false)
	return err
}

// TestAFinishedStepCannotBeAnsweredAgain is the regression for a step that had
// no final state.
//
// WorkflowStepCompleted and WorkflowStepFailed are the app-facing answers to a
// step, and neither asked what the step already said. An app could report
// success for a step that had already failed, failure for one that had
// succeeded, or answer one that was cancelled when its workflow went offline —
// and because a run's status is derived from its steps, that rewrote the outcome
// of a run that was already over.
//
// It has its own test rather than leaning on the lifecycle driver: the driver
// advances runs through CompleteFunction, which is a different path, so the
// driver stopped covering this the moment it was made to drive runs properly.
func TestAFinishedStepCannotBeAnsweredAgain(t *testing.T) {
	ctx := context.Background()
	for _, answered := range []struct {
		name  string
		first func(execution) error
	}{
		{"completed", func(e execution) error {
			return e.messages.WorkflowStepCompleted(ctx, runWorkspace, runOwner, string(e.step), `{"priority":1}`)
		}},
		{"failed", func(e execution) error {
			return e.messages.WorkflowStepFailed(ctx, runWorkspace, runOwner, string(e.step), `{"message":"no"}`)
		}},
	} {
		t.Run(answered.name, func(t *testing.T) {
			started := startWorkflowRun(t)
			if err := answered.first(started); err != nil {
				t.Fatal(err)
			}
			before, err := started.messages.Store.GetWorkflowStep(ctx, runWorkspace, started.step)
			if err != nil {
				t.Fatal(err)
			}
			if !before.Status.Terminal() {
				t.Fatalf("the step is %q after being answered, want a final state", before.Status)
			}
			completeErr := started.messages.WorkflowStepCompleted(ctx, runWorkspace, runOwner, string(started.step), `{"priority":2}`)
			failErr := started.messages.WorkflowStepFailed(ctx, runWorkspace, runOwner, string(started.step), `{"message":"again"}`)
			after, err := started.messages.Store.GetWorkflowStep(ctx, runWorkspace, started.step)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != before.Status {
				t.Fatalf("a %s step became %q when it was answered again", before.Status, after.Status)
			}
			if completeErr == nil && before.Status != domain.WorkflowStepCompleted {
				t.Errorf("completing a %s step was accepted", before.Status)
			}
			if failErr == nil && before.Status != domain.WorkflowStepFailed {
				t.Errorf("failing a %s step was accepted", before.Status)
			}
		})
	}
}
