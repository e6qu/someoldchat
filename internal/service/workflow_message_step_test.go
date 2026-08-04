package service

import (
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Slack's most-used Workflow Builder step sends a message to a conversation.
// It is the first built-in step that neither dispatches to an app nor waits for
// a person: the run performs it and carries straight on. Before this, a
// workflow could only call an app function or park on a form or a button, so
// the commonest workflow in Slack could not be expressed here at all.
func TestMessageStepPostsAndCarriesTheRunOn(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	workflow.Steps = `[{"id":"announce","type":"message","title":"Announce","message":{"conversation":"C1","text":"Deploy started"}}]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "message-step")
	if err != nil {
		t.Fatal(err)
	}
	// The run is finished when it returns: nothing else is coming to move it.
	if run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed — a built-in step has nothing left to wait for", run.Status)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range page.Messages {
		if message.Text == "Deploy started" {
			found = true
			if message.AuthorID != "U1" {
				t.Errorf("author = %q, want the member who started the run", message.AuthorID)
			}
		}
	}
	if !found {
		t.Fatalf("the message step posted nothing: %+v", page.Messages)
	}
}

// Two message steps in a row must both run. The first completes inside the
// call that starts it, so unless the run is carried past it the second never
// begins — a workflow that announces a start and an end would post only the
// start and sit there forever.
func TestConsecutiveMessageStepsBothRun(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	workflow.Steps = `[{"id":"one","type":"message","message":{"conversation":"C1","text":"first"}},{"id":"two","type":"message","message":{"conversation":"C1","text":"second"}}]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "chained")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed once both built-in steps ran", run.Status)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, message := range page.Messages {
		seen[message.Text] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("both message steps should have posted: %+v", page.Messages)
	}
}

// A step that cannot post fails the run rather than the request: the run
// records why, and an operator reads it afterwards.
func TestMessageStepToAnUnreachableConversationFailsTheRun(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	workflow.Steps = `[{"id":"one","type":"message","message":{"conversation":"C-nonexistent","text":"nobody"}}]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "unreachable")
	if err != nil {
		t.Fatalf("an undeliverable step failed the request instead of the run: %v", err)
	}
	if run.Status != domain.WorkflowRunFailed || run.Error == "" {
		t.Fatalf("run = %+v, want a failed run carrying the reason", run)
	}
}

// Text may quote earlier work the same way input mapping does, so a form's
// answer can be announced by the step after it.
func TestMessageStepQuotesRunInputs(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	workflow.InputSchema = `{"properties":{"service":{"type":"string"}}}`
	workflow.Steps = `[{"id":"announce","type":"message","message":{"conversation":"C1","text":"Deploying {{inputs.service}} now"}}]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{"service":"checkout"}`, "interpolated"); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if message.Text == "Deploying checkout now" {
			return
		}
	}
	t.Fatalf("the reference was not substituted: %+v", page.Messages)
}

// A reference that resolves to nothing is left as written. A blank gap tells an
// author nothing; the reference they typed tells them exactly what is wrong.
func TestMessageStepKeepsAnUnresolvableReferenceVisible(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	workflow.Steps = `[{"id":"announce","type":"message","message":{"conversation":"C1","text":"Owner is {{inputs.missing}}"}}]`
	published, err := messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: published.ID, Title: "Run it", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "unresolved"); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if strings.Contains(message.Text, "{{inputs.missing}}") {
			return
		}
	}
	t.Fatalf("an unresolvable reference was silently blanked: %+v", page.Messages)
}

// A step and its definition are refused together: a message step with no
// conversation or no text is a definition error, not a run that fails later.
func TestMessageStepDefinitionIsValidated(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	for name, steps := range map[string]string{
		"no conversation": `[{"id":"a","type":"message","message":{"text":"hi"}}]`,
		"no text":         `[{"id":"a","type":"message","message":{"conversation":"C1"}}]`,
		"no message":      `[{"id":"a","type":"message"}]`,
		"function too":    `[{"id":"a","type":"message","function_id":"triage","message":{"conversation":"C1","text":"hi"}}]`,
	} {
		candidate := workflow
		candidate.Steps = steps
		if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", candidate, workflow.Version, true); err == nil {
			t.Errorf("%s was accepted as a message step", name)
		}
	}
}
