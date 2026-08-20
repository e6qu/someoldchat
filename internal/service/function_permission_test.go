package service

import (
	"errors"
	"testing"

	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// TestFunctionPermissionGatesBuildingAWorkflow proves the permission an
// administrator sets with admin.functions.permissions.set actually governs who
// may build a workflow with that function. Before this it was stored and reported
// but never read, so a restriction changed nothing. A builder the restriction
// excludes is refused; naming them, or opening the function to everyone, restores
// the ability; and an unrestricted function is buildable by default.
func TestFunctionPermissionGatesBuildingAWorkflow(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)

	// Default (no stored permission): the owner rebuilds the workflow fine.
	updated, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, false)
	if err != nil {
		t.Fatalf("an unrestricted function should build without complaint: %v", err)
	}

	// Restrict the triage function to a member who is not the builder.
	if _, err := messages.SetFunctionPermission(ctx, "T1", "U1", "A1", "", "triage", domain.AutomationPermission{
		PermissionType: domain.PermissionNamedEntities, UserIDs: []domain.UserID{"U2"},
	}); err != nil {
		t.Fatalf("set function permission: %v", err)
	}

	// The excluded builder can no longer build a workflow with it.
	if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", updated, updated.Version, false); !errors.Is(err, ErrFunctionUseRestricted) {
		t.Fatalf("a builder the restriction excludes updated the workflow anyway: err=%v, want ErrFunctionUseRestricted", err)
	}

	// Naming the builder restores the ability.
	if _, err := messages.SetFunctionPermission(ctx, "T1", "U1", "A1", "", "triage", domain.AutomationPermission{
		PermissionType: domain.PermissionNamedEntities, UserIDs: []domain.UserID{"U1"},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err = messages.UpdateWorkflow(ctx, "T1", "U1", updated, updated.Version, false)
	if err != nil {
		t.Fatalf("the named builder should be allowed again: %v", err)
	}

	// Opening the function to everyone allows any builder.
	if _, err := messages.SetFunctionPermission(ctx, "T1", "U1", "A1", "", "triage", domain.AutomationPermission{
		PermissionType: domain.PermissionEveryone,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", updated, updated.Version, false); err != nil {
		t.Fatalf("everyone should build with an open function: %v", err)
	}
}

// TestTriggerTypePermissionGatesTriggerCreation proves the permission set with
// admin.workflows.triggers.types.permissions.set governs who may create a trigger
// of a type. Before this it was stored and reported but never read. A builder the
// restriction excludes is refused before the trigger is even configured; opening
// the type to everyone lets them create it.
func TestTriggerTypePermissionGatesTriggerCreation(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	schedule := `{"start_time":"2026-03-27T09:00:00Z","timezone":"UTC","frequency":{"type":"daily"}}`

	restrictTo := func(kind domain.PermissionType, users ...domain.UserID) {
		t.Helper()
		if err := repository.SetAutomationPermission(ctx, domain.AutomationPermission{
			ResourceType: "trigger_type", ResourceID: "scheduled", WorkspaceID: "T1",
			PermissionType: kind, UserIDs: users, UpdatedAt: time.Now().UTC(),
		}, events.Event{ID: domain.EventID("E-tt-" + string(kind)), WorkspaceID: "T1", Topic: "workflow.trigger_type_permission_set"}); err != nil {
			t.Fatal(err)
		}
	}

	// Restricted to someone other than the builder: refused.
	restrictTo(domain.PermissionNamedEntities, "U2")
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Daily", Type: "scheduled", Config: schedule, Enabled: true,
	}, 0); !errors.Is(err, ErrTriggerTypeRestricted) {
		t.Fatalf("a builder the restriction excludes created the trigger anyway: err=%v, want ErrTriggerTypeRestricted", err)
	}

	// Opened to everyone: the builder creates it.
	restrictTo(domain.PermissionEveryone)
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Daily", Type: "scheduled", Config: schedule, Enabled: true,
	}, 0); err != nil {
		t.Fatalf("everyone should build a scheduled trigger: %v", err)
	}
}
