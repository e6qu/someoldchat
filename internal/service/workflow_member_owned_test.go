package service

import (
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// WORKFLOW-02: a member builds a workflow of their own with no developer app,
// as Workflow Builder allows. It is built from the steps that run without an
// app, published, started from a trigger and run to completion; the run and
// its steps carry no app either.
func TestAMemberBuildsAndRunsAWorkflowWithNoApp(t *testing.T) {
	ctx, repository, messages, _ := seedWorkflowTriggerWorld(t)
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U2", domain.WorkflowDefinition{Title: "Welcome new people"})
	if err != nil {
		t.Fatalf("create a workflow with no app: %v", err)
	}
	if workflow.AppID != "" || workflow.OwnerID != "U2" || workflow.Status != domain.WorkflowDraft {
		t.Fatalf("workflow = %+v, want a draft U2 owns with no app", workflow)
	}
	workflow.Steps = `[
		{"id":"doc","type":"create_canvas","create_canvas":{"title":"Welcome notes"}},
		{"id":"say","type":"message","message":{"conversation":"C1","text":"welcome aboard"}}
	]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U2", workflow, workflow.Version, true)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U2", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Start", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatalf("add a trigger: %v", err)
	}
	if trigger.AppID != "" {
		t.Fatalf("trigger app = %q, want none", trigger.AppID)
	}
	// The trigger's default permission, app collaborators, admits the
	// workflow's owner: a member's own workflow has no app to have any.
	permission, err := messages.GetTriggerPermission(ctx, "T1", "U2", "", trigger.ID)
	if err != nil || permission.PermissionType != domain.PermissionAppCollaborators || len(permission.UserIDs) != 1 || permission.UserIDs[0] != "U2" {
		t.Fatalf("permission=%+v err=%v, want app collaborators naming the owner", permission, err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U2", trigger.ID, "C1", `{}`, "member-owned")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != domain.WorkflowRunCompleted || run.AppID != "" {
		t.Fatalf("run = %+v, want completed with no app", run)
	}
	history, err := repository.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	posted := false
	for _, message := range history.Messages {
		posted = posted || message.Text == "welcome aboard" && message.AuthorID == "U2"
	}
	if !posted {
		t.Fatalf("the message step did not post as the member who ran it: %+v", history.Messages)
	}
}

// A function belongs to an app, so a workflow with no app cannot call one —
// neither an app's function named by its own app ID nor a bare callback.
// The refusal comes at publish rather than as a failed run later.
func TestAWorkflowWithNoAppCannotCallAFunction(t *testing.T) {
	ctx, _, messages, _ := seedWorkflowTriggerWorld(t)
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	for _, steps := range []string{
		`[{"id":"fn","function_id":"triage","title":"Triage"}]`,
		`[{"id":"fn","app_id":"A1","function_id":"triage","title":"Triage"}]`,
	} {
		workflow.Steps = steps
		if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true); !errors.Is(err, domain.ErrInvalidWorkflowStep) {
			t.Fatalf("steps %s: err=%v, want ErrInvalidWorkflowStep", steps, err)
		}
	}
	if _, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{Title: "Calls an app", Steps: `[{"id":"fn","function_id":"triage"}]`}); !errors.Is(err, domain.ErrInvalidWorkflowStep) {
		t.Fatalf("create with a function step: err=%v, want ErrInvalidWorkflowStep", err)
	}
}

// An administrator can restrict a trigger type to an app's collaborators. A
// workflow with no app has none, so the restriction admits nobody building
// one by hand — it is refused like any other restriction, not answered with
// a lookup failure for an app that does not exist.
func TestAppCollaboratorRestrictionsAdmitNobodyToAWorkflowWithNoApp(t *testing.T) {
	ctx, repository, messages, _ := seedWorkflowTriggerWorld(t)
	now := time.Now().UTC()
	if err := repository.SetAutomationPermission(ctx, domain.AutomationPermission{
		ResourceType: "trigger_type", ResourceID: "link", WorkspaceID: "T1",
		PermissionType: domain.PermissionAppCollaborators, UpdatedAt: now,
	}, events.Event{ID: "E-permission", WorkspaceID: "T1", Topic: "automation.permission_set", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	workflow.Steps = `[{"id":"say","type":"message","message":{"conversation":"C1","text":"hi"}}]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{WorkflowID: published.ID, Title: "Start", Type: "link", Config: `{}`, Enabled: true}, 0)
	if !errors.Is(err, domain.ErrTriggerTypeRestricted) {
		t.Fatalf("err=%v, want ErrTriggerTypeRestricted", err)
	}
}
