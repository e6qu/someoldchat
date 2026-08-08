package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// TestWorkflowFindPermissionGatesDirectoryAndReads: a published workflow is
// findable by every member until the find permission narrows it; a member the
// permission excludes can neither list nor read the workflow, and gets the
// same answer a missing workflow gives, while managers stay unaffected.
func TestWorkflowFindPermissionGatesDirectoryAndReads(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	if err := repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "third"}); err != nil {
		t.Fatal(err)
	}
	listed := func(actor domain.UserID) bool {
		t.Helper()
		values, _, _, err := messages.ListWorkflows(ctx, "T1", actor, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if value.ID == workflow.ID {
				return true
			}
		}
		return false
	}
	if !listed("U2") {
		t.Fatal("a published workflow is not in the directory before any permission is set")
	}
	if _, err := messages.GetWorkflow(ctx, "T1", "U2", workflow.ID); err != nil {
		t.Fatalf("default find blocked a member read: %v", err)
	}

	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "find", domain.AutomationPermission{
		PermissionType: "named_entities", UserIDs: []domain.UserID{"U3"},
	}); err != nil {
		t.Fatal(err)
	}
	if listed("U2") {
		t.Fatal("a member the find permission excludes still sees the workflow in the directory")
	}
	if _, err := messages.GetWorkflow(ctx, "T1", "U2", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("excluded member read error=%v, want the not-found a missing workflow gives", err)
	}
	if !listed("U3") {
		t.Fatal("the named member lost the workflow the permission grants them")
	}
	if _, err := messages.GetWorkflow(ctx, "T1", "U3", workflow.ID); err != nil {
		t.Fatalf("named member read: %v", err)
	}
	if !listed("U1") {
		t.Fatal("the owner lost their own workflow to a find permission")
	}

	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "find", domain.AutomationPermission{
		PermissionType: "everyone",
	}); err != nil {
		t.Fatal(err)
	}
	if !listed("U2") {
		t.Fatal("reopening find to everyone did not restore the directory entry")
	}
}

// TestWorkflowUsePermissionGatesRunsBeyondTheTriggerGrant: the workflow-level
// use permission narrows who may run a workflow even when the trigger's own
// permission allows them, and an automatic fire — which runs as the owner —
// stays unaffected.
func TestWorkflowUsePermissionGatesRunsBeyondTheTriggerGrant(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	if err := repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "third"}); err != nil {
		t.Fatal(err)
	}
	link, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetTriggerPermission(ctx, "T1", "U1", "A1", link.ID, domain.AutomationPermission{
		PermissionType: "everyone",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U2", link.ID, "C1", `{}`, "use-open"); err != nil {
		t.Fatalf("open use blocked a run the trigger allows: %v", err)
	}

	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "use", domain.AutomationPermission{
		PermissionType: "named_entities", UserIDs: []domain.UserID{"U3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U2", link.ID, "C1", `{}`, "use-denied"); !errors.Is(err, ErrWorkflowPermissionDenied) {
		t.Fatalf("excluded member run error=%v, want ErrWorkflowPermissionDenied", err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U3", link.ID, "C1", `{}`, "use-named"); err != nil {
		t.Fatalf("named member run: %v", err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U1", link.ID, "C1", `{}`, "use-owner"); err != nil {
		t.Fatalf("owner run: %v", err)
	}

	automatic, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "On message", Type: "message", Config: `{"channel_ids":["C1"]}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunAutomaticWorkflow(ctx, "T1", automatic.ID, "C1", `{}`, "use-auto"); err != nil {
		t.Fatalf("automatic fire under a narrowed use permission: %v", err)
	}
}

// TestWorkflowCopyPermissionOpensDuplication: copy defaults to managers only;
// opening it lets a member duplicate the workflow — but only its published
// revision, never the staged head, and never an unpublished or unfindable
// workflow.
func TestWorkflowCopyPermissionOpensDuplication(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	if err := repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "third"}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.DuplicateWorkflow(ctx, "T1", "U2", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("default copy error=%v, want managers-only not-found", err)
	}

	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "copy", domain.AutomationPermission{
		PermissionType: "everyone",
	}); err != nil {
		t.Fatal(err)
	}
	// Stage an edit after publication: the head now differs from the published
	// revision, and the member's copy must be built from what the member can
	// see, not from the staged head.
	staged := workflow
	staged.Title = "Incident triage (staged secret)"
	staged, err := messages.UpdateWorkflow(ctx, "T1", "U1", staged, workflow.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := messages.DuplicateWorkflow(ctx, "T1", "U2", workflow.ID)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	if copied.OwnerID != "U2" || copied.Status != domain.WorkflowDraft {
		t.Fatalf("copy=%+v, want a draft owned by the copier", copied)
	}
	if strings.Contains(copied.Title, "staged secret") {
		t.Fatalf("the copy %q leaked the staged head a non-manager cannot read", copied.Title)
	}

	// Narrowing find below the copier closes copy with it: a workflow you
	// cannot see is not one you can duplicate.
	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "find", domain.AutomationPermission{
		PermissionType: "named_entities", UserIDs: []domain.UserID{"U3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.DuplicateWorkflow(ctx, "T1", "U2", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unfindable copy error=%v, want not-found", err)
	}

	// An unpublished workflow is invisible to members whatever copy says.
	draft, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Draft only", InputSchema: `{}`, Steps: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", draft.ID, "copy", domain.AutomationPermission{
		PermissionType: "everyone",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.DuplicateWorkflow(ctx, "T1", "U2", draft.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unpublished copy error=%v, want not-found", err)
	}
	if _, err := messages.DuplicateWorkflow(ctx, "T1", "U1", staged.ID); err != nil {
		t.Fatalf("the owner still copies their own workflow: %v", err)
	}
}

// TestWorkflowPermissionReadAndWriteContracts: defaults, validation, and the
// visibility rule — reading a scope answers exactly the actors GetWorkflow
// answers, so permission probes cannot reveal hidden workflows.
func TestWorkflowPermissionReadAndWriteContracts(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	if err := repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "third"}); err != nil {
		t.Fatal(err)
	}
	for scope, want := range map[string]domain.PermissionType{"find": domain.PermissionEveryone, "use": domain.PermissionEveryone, "copy": domain.PermissionAppCollaborators} {
		value, err := messages.GetWorkflowPermission(ctx, "T1", "U1", workflow.ID, scope)
		if err != nil {
			t.Fatal(err)
		}
		if value.PermissionType != want || value.ResourceType != "workflow_"+scope || value.ResourceID != string(workflow.ID) {
			t.Fatalf("default %s permission=%+v, want %s", scope, value, want)
		}
	}
	// A member reads the scopes of a workflow they can find.
	if _, err := messages.GetWorkflowPermission(ctx, "T1", "U2", workflow.ID, "copy"); err != nil {
		t.Fatalf("member read of a visible workflow's scope: %v", err)
	}

	if _, err := messages.GetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "run"); !errors.Is(err, ErrInvalidWorkflowStep) {
		t.Fatalf("unknown scope error=%v, want ErrInvalidWorkflowStep", err)
	}
	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "find", domain.AutomationPermission{
		PermissionType: "owner_only",
	}); !errors.Is(err, ErrInvalidWorkflowStep) {
		t.Fatalf("unknown permission type error=%v, want ErrInvalidWorkflowStep", err)
	}
	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U2", workflow.ID, "find", domain.AutomationPermission{
		PermissionType: "everyone",
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-manager set error=%v, want the not-found a missing workflow gives", err)
	}
	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "use", domain.AutomationPermission{
		PermissionType: "named_entities", UserIDs: []domain.UserID{"Unknown"},
	}); !errors.Is(err, ErrAutomationUserNotFound) {
		t.Fatalf("unknown named user error=%v, want ErrAutomationUserNotFound", err)
	}

	// An unpublished workflow answers permission reads only for its managers.
	draft, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Hidden draft", InputSchema: `{}`, Steps: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.GetWorkflowPermission(ctx, "T1", "U2", draft.ID, "find"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member probe of a draft's scope error=%v, want not-found", err)
	}
	if _, err := messages.GetWorkflowPermission(ctx, "T1", "U1", draft.ID, "find"); err != nil {
		t.Fatalf("manager read of a draft's scope: %v", err)
	}
	// And a member the find permission excludes cannot read any scope either.
	if _, err := messages.SetWorkflowPermission(ctx, "T1", "U1", workflow.ID, "find", domain.AutomationPermission{
		PermissionType: "named_entities", UserIDs: []domain.UserID{"U3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.GetWorkflowPermission(ctx, "T1", "U2", workflow.ID, "copy"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("excluded member scope read error=%v, want not-found", err)
	}
}
