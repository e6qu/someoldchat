package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func seedWorkflowScheduleWorld(t *testing.T) (context.Context, *memory.Store, service.Messages, domain.WorkflowTrigger) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC().Truncate(time.Second)
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"}),
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		repository.SeedConversationMember("C1", "U1"),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Automation", ClientID: "client",
		SigningSecretHash: "signing", SigningSecretCiphertext: "cipher", VerificationTokenHash: "verify",
		VerificationTokenCiphertext: "cipher", ManifestVersion: 1, Distribution: "private",
		CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, CreatedBy: "U1", CreatedAt: now,
		Manifest: `{
			"display_information":{"name":"Automation"},
			"settings":{"function_runtime":"remote"},
			"functions":{"triage":{"title":"Triage","description":"Triage",
				"input_parameters":{"properties":{},"required":[]},
				"output_parameters":{"properties":{},"required":[]}}}
		}`,
	}, domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository}
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Triage", InputSchema: `{}`, Steps: `[{"function_id":"triage"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workflow, err = messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true); err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Hourly", Type: "scheduled",
		Config:  `{"start_time":"2020-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"hourly"}}`,
		Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repository, messages, trigger
}

func TestWorkflowScheduleWorkerFiresOnceAndAdvances(t *testing.T) {
	ctx, repository, messages, trigger := seedWorkflowScheduleWorld(t)
	worker, err := NewWorkflowScheduleWorker(repository, messages, 10)
	if err != nil {
		t.Fatal(err)
	}
	due := trigger.NextRunAt
	fired, err := worker.RunOnceAt(ctx, "T1", due)
	if err != nil || fired != 1 {
		t.Fatalf("fired=%d err=%v, want 1", fired, err)
	}
	runs, _, _, err := repository.ListWorkflowRuns(ctx, "T1", trigger.WorkflowID, domain.PageRequest{Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if runs[0].TriggerID != trigger.ID || runs[0].ActorID != "U1" {
		t.Fatalf("scheduled run=%+v", runs[0])
	}
	stored, err := repository.GetWorkflowTrigger(ctx, "T1", trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.NextRunAt.Equal(due.Add(time.Hour)) {
		t.Fatalf("next run=%s, want one hour after %s", stored.NextRunAt, due)
	}
	// Replaying the same poll instant starts nothing: the occurrence advanced
	// and its idempotency key already has a run.
	fired, err = worker.RunOnceAt(ctx, "T1", due)
	if err != nil || fired != 0 {
		t.Fatalf("replay fired=%d err=%v, want 0", fired, err)
	}
	fired, err = worker.RunOnceAt(ctx, "T1", due.Add(time.Hour))
	if err != nil || fired != 1 {
		t.Fatalf("next occurrence fired=%d err=%v, want 1", fired, err)
	}
	runs, _, _, err = repository.ListWorkflowRuns(ctx, "T1", trigger.WorkflowID, domain.PageRequest{Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after second occurrence=%d err=%v", len(runs), err)
	}
}

func TestWorkflowScheduleWorkerAdvancesPastAnUnpublishableFire(t *testing.T) {
	ctx, repository, messages, trigger := seedWorkflowScheduleWorld(t)
	worker, err := NewWorkflowScheduleWorker(repository, messages, 10)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := repository.GetWorkflow(ctx, "T1", trigger.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		ID: workflow.ID, Title: workflow.Title, Steps: workflow.Steps, Status: domain.WorkflowDisabled,
	}, workflow.Version, false); err != nil {
		t.Fatal(err)
	}
	due := trigger.NextRunAt
	fired, err := worker.RunOnceAt(ctx, "T1", due)
	if err != nil || fired != 1 {
		t.Fatalf("refused fire fired=%d err=%v, want the schedule advanced", fired, err)
	}
	runs, _, _, err := repository.ListWorkflowRuns(ctx, "T1", trigger.WorkflowID, domain.PageRequest{Limit: 10})
	if err != nil || len(runs) != 0 {
		t.Fatalf("unpublished workflow produced runs=%+v", runs)
	}
	stored, err := repository.GetWorkflowTrigger(ctx, "T1", trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.NextRunAt.Equal(due.Add(time.Hour)) {
		t.Fatalf("refused fire left next run=%s, want the schedule advanced", stored.NextRunAt)
	}
}

func TestWorkflowEventWorkerDelegatesToTheRunner(t *testing.T) {
	_, _, messages, _ := seedWorkflowScheduleWorld(t)
	worker, err := NewWorkflowEventWorker(messages, 25)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background(), "T1"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkflowScheduleWorker(nil, messages, 10); err == nil {
		t.Fatal("schedule worker accepted a nil source")
	}
	if _, err := NewWorkflowEventWorker(nil, 25); err == nil {
		t.Fatal("event worker accepted a nil runner")
	}
}
