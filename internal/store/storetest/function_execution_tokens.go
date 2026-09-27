package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// FunctionExecutionTokenRepository is the part of store.Store the
// execution-scoped token check drives.
type FunctionExecutionTokenRepository interface {
	SeedToken(context.Context, string, domain.TokenRecord) error
	RevokeToken(context.Context, string) error
	CreateWorkflow(context.Context, domain.WorkflowDefinition, events.Event) error
	CreateWorkflowRun(context.Context, domain.WorkflowRun, *domain.WorkflowStep, []events.Event) error
	AdvanceWorkflowRun(context.Context, domain.WorkflowStep, *domain.WorkflowStep, domain.WorkflowRun, int, []events.Event) error
	LookupToken(context.Context, string) (domain.TokenRecord, error)
	IssueFunctionExecutionToken(context.Context, domain.FunctionExecutionToken, string) (domain.FunctionExecutionToken, error)
	GetFunctionExecutionToken(context.Context, domain.WorkspaceID, domain.WorkflowStepID) (domain.FunctionExecutionToken, error)
	DeleteWorkflow(context.Context, domain.WorkspaceID, domain.WorkflowID, uint64, events.Event) (bool, error)
}

// CheckFunctionExecutionTokens requires one execution-scoped token per
// function execution that authenticates as the app's bot while the execution
// runs, expires the instant it ends, and is revoked once the app loses its
// bot token. The repository must hold workspace T1 with members U1 and UB.
func CheckFunctionExecutionTokens(t *testing.T, repository FunctionExecutionTokenRepository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repository.SeedToken(ctx, "xoxb-app-bot", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: domain.TokenBot, Scopes: []string{"chat:write"},
	}); err != nil {
		t.Fatal(err)
	}
	workflowEvent := mustEvent(t, "Ev-wf", "workflow.created", now)
	if err := repository.CreateWorkflow(ctx, domain.WorkflowDefinition{
		ID: "Wf1", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", Title: "Triage", Status: "published",
		Steps: `[]`, InputSchema: `{}`, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}, workflowEvent); err != nil {
		t.Fatal(err)
	}
	run := domain.WorkflowRun{
		ID: "Wx1", WorkflowID: "Wf1", WorkspaceID: "T1", AppID: "A1", ActorID: "U1", Status: domain.WorkflowRunRunning,
		WorkflowVersion: 1, Inputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	step := domain.WorkflowStep{
		ID: "Fx1", WorkflowRunID: "Wx1", WorkspaceID: "T1", AppID: "A1", UserID: "U1", FunctionID: "Fn1", EditID: "s1",
		Status: domain.WorkflowStepExecuting, Inputs: `{"ticket":"42"}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflowRun(ctx, run, &step, []events.Event{mustEvent(t, "Ev-run", "workflow.run_started", now)}); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.GetFunctionExecutionToken(ctx, "T1", "Fx1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token before issue err=%v, want ErrNotFound", err)
	}
	issued := domain.FunctionExecutionToken{
		WorkspaceID: "T1", ExecutionID: "Fx1", AppID: "A1", CallbackID: "triage", UserID: "UB", BotID: "B1",
		Scopes: []string{"chat:write"}, Ciphertext: "sealed-first", CreatedAt: now,
	}
	first, err := repository.IssueFunctionExecutionToken(ctx, issued, domain.HashToken("xwfp-first"))
	if err != nil {
		t.Fatal(err)
	}
	// A second issue for the same execution returns the first token.
	issued.Ciphertext = "sealed-second"
	second, err := repository.IssueFunctionExecutionToken(ctx, issued, domain.HashToken("xwfp-second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Ciphertext != "sealed-first" || second.Ciphertext != "sealed-first" || second.CallbackID != "triage" ||
		second.UserID != "UB" || second.BotID != "B1" || len(second.Scopes) != 1 || !second.CreatedAt.Equal(now) {
		t.Fatalf("issued first=%+v second=%+v, want the first token recorded once", first, second)
	}
	if _, err := repository.LookupToken(ctx, "xwfp-second"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("discarded token lookup err=%v, want ErrNotFound", err)
	}
	record, err := repository.LookupToken(ctx, "xwfp-first")
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkspaceID != "T1" || record.UserID != "UB" || record.AppID != "A1" || record.BotID != "B1" ||
		record.TokenType != domain.TokenBot || record.FunctionExecutionID != "Fx1" || record.Revoked ||
		!record.ExpiresAt.IsZero() || len(record.Scopes) != 1 || record.Scopes[0] != "chat:write" {
		t.Fatalf("running execution token=%+v", record)
	}

	ended := now.Add(time.Second)
	completed := step
	completed.Status, completed.Outputs, completed.UpdatedAt = domain.WorkflowStepCompleted, `{}`, ended
	run.Status, run.UpdatedAt, run.CompletedAt = domain.WorkflowRunCompleted, ended, ended
	if err := repository.AdvanceWorkflowRun(ctx, completed, nil, run, 0, []events.Event{mustEvent(t, "Ev-done", "workflow.run_completed", ended)}); err != nil {
		t.Fatal(err)
	}
	record, err = repository.LookupToken(ctx, "xwfp-first")
	if err != nil {
		t.Fatal(err)
	}
	if !record.ExpiresAt.Equal(ended) || record.Revoked {
		t.Fatalf("completed execution token expires=%v revoked=%v, want expiry at %v", record.ExpiresAt, record.Revoked, ended)
	}

	if err := repository.RevokeToken(ctx, "xoxb-app-bot"); err != nil {
		t.Fatal(err)
	}
	record, err = repository.LookupToken(ctx, "xwfp-first")
	if err != nil || !record.Revoked {
		t.Fatalf("token of an app without a bot token=%+v err=%v, want revoked", record, err)
	}

	// Deleting the workflow deletes its executions and their tokens.
	if deleted, err := repository.DeleteWorkflow(ctx, "T1", "Wf1", 1, mustEvent(t, "Ev-deleted", "workflow.deleted", ended)); err != nil || !deleted {
		t.Fatalf("delete workflow=%v err=%v", deleted, err)
	}
	if _, err := repository.GetFunctionExecutionToken(ctx, "T1", "Fx1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token of a deleted execution err=%v, want ErrNotFound", err)
	}
	if _, err := repository.LookupToken(ctx, "xwfp-first"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup of a deleted execution's token err=%v, want ErrNotFound", err)
	}
}

func mustEvent(t *testing.T, id domain.EventID, topic string, at time.Time) events.Event {
	t.Helper()
	event, err := events.New(id, "T1", "U1", events.NewPayload(topic, events.String("workflow_id", "Wf1")), at)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
