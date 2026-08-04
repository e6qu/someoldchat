package service

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func publishAndRun(t *testing.T, messages Messages, workflow domain.WorkflowDefinition, steps, inputs, key string) domain.WorkflowRun {
	t.Helper()
	workflow.Steps = steps
	published, err := messages.UpdateWorkflow(t.Context(), "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatalf("publish %s: %v", key, err)
	}
	trigger, err := messages.SetWorkflowTrigger(t.Context(), "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(t.Context(), "T1", "U1", trigger.ID, "C1", inputs, key)
	if err != nil {
		t.Fatalf("run %s: %v", key, err)
	}
	return run
}

// Slack's "add people to a channel" step. Like the message step it dispatches
// to no app and waits for no one, and like it the action is taken as the member
// who started the run — so a workflow cannot add someone to a channel its owner
// cannot invite them to.
func TestAddPeopleStepAddsThemAndCompletesTheRun(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	run := publishAndRun(t, messages, workflow,
		`[{"id":"invite","type":"add_people","add_people":{"conversation":"C1","users":["U2"]}}]`, `{}`, "add-people")
	if run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	member, err := repository.IsConversationMember(ctx, "C1", "U2")
	if err != nil || !member {
		t.Fatalf("member = %v err = %v, want the step to have added them", member, err)
	}
}

// Running the same add-people step twice must not fail the second time: the
// step describes a desired end state, and a workflow on a schedule would fail
// forever otherwise.
func TestAddPeopleStepIsIdempotent(t *testing.T) {
	_, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	steps := `[{"id":"invite","type":"add_people","add_people":{"conversation":"C1","users":["U2"]}}]`
	if run := publishAndRun(t, messages, workflow, steps, `{}`, "idempotent-first"); run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("first run = %q", run.Status)
	}
	current, err := repository.GetWorkflow(t.Context(), "T1", workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run := publishAndRun(t, messages, current, steps, `{}`, "idempotent-second"); run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("second run = %q, want a step that is already satisfied to succeed", run.Status)
	}
}

// Slack's "create a canvas" step. The canvas belongs to the member who started
// the run, for the same reason a message step posts as them.
func TestCanvasStepCreatesOneOwnedByTheRunner(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	run := publishAndRun(t, messages, workflow,
		`[{"id":"doc","type":"create_canvas","create_canvas":{"title":"Incident notes","content":"What happened"}}]`, `{}`, "canvas")
	if run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	page, err := repository.ListCanvases(ctx, "T1", "U1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, canvas := range page.Canvases {
		if canvas.Title == "Incident notes" {
			if canvas.OwnerID != "U1" {
				t.Errorf("owner = %q, want the member who started the run", canvas.OwnerID)
			}
			return
		}
	}
	t.Fatalf("the canvas step created nothing: %+v", page.Canvases)
}

// A canvas title may quote the run's inputs, the same way a message may.
func TestCanvasStepQuotesRunInputs(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	workflow.InputSchema = `{"properties":{"service":{"type":"string"}}}`
	publishAndRun(t, messages, workflow,
		`[{"id":"doc","type":"create_canvas","create_canvas":{"title":"Runbook for {{inputs.service}}"}}]`,
		`{"service":"checkout"}`, "canvas-quoted")
	page, err := repository.ListCanvases(ctx, "T1", "U1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, canvas := range page.Canvases {
		if canvas.Title == "Runbook for checkout" {
			return
		}
	}
	t.Fatalf("the title reference was not substituted: %+v", page.Canvases)
}

// Built-in steps chain through each other: a run of three different built-in
// kinds must finish without anything external arriving.
func TestMixedBuiltInStepsAllRun(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	run := publishAndRun(t, messages, workflow, `[
		{"id":"invite","type":"add_people","add_people":{"conversation":"C1","users":["U2"]}},
		{"id":"doc","type":"create_canvas","create_canvas":{"title":"Chained canvas"}},
		{"id":"say","type":"message","message":{"conversation":"C1","text":"all three ran"}}
	]`, `{}`, "mixed")
	if run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed after three built-in steps", run.Status)
	}
	messagesPage, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messagesPage.Messages {
		if message.Text == "all three ran" {
			return
		}
	}
	t.Fatalf("the last step never ran: %+v", messagesPage.Messages)
}

// Definitions are refused at publish time rather than failing a run later.
func TestBuiltInStepDefinitionsAreValidated(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	for name, steps := range map[string]string{
		"add people with no conversation": `[{"id":"a","type":"add_people","add_people":{"users":["U2"]}}]`,
		"add people with nobody":          `[{"id":"a","type":"add_people","add_people":{"conversation":"C1"}}]`,
		"add people with a function":      `[{"id":"a","type":"add_people","function_id":"triage","add_people":{"conversation":"C1","users":["U2"]}}]`,
		"canvas with no title":            `[{"id":"a","type":"create_canvas","create_canvas":{"content":"body"}}]`,
		"canvas missing entirely":         `[{"id":"a","type":"create_canvas"}]`,
	} {
		candidate := workflow
		candidate.Steps = steps
		if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", candidate, workflow.Version, true); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
