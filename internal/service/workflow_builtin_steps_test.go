package service

import (
	"strconv"
	"testing"
	"time"

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

// Slack's "wait for a set time" step. It is the first kind that suspends a run
// on the clock rather than on a person, an app, or nothing at all, so the run
// must stay parked until the wake time and then continue on its own.
func TestDelayStepParksTheRunUntilItIsDue(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	run := publishAndRun(t, messages, workflow, `[
		{"id":"wait","type":"delay","delay":{"seconds":3600}},
		{"id":"say","type":"message","message":{"conversation":"C1","text":"after the wait"}}
	]`, `{}`, "delayed")
	if run.Status != domain.WorkflowRunRunning {
		t.Fatalf("run status = %q, want it still running while parked", run.Status)
	}

	// Nothing is due yet, so a sweep moves nothing and the later step has not
	// run: a delay that resumed early would be no delay at all.
	resumed, err := messages.ResumeWorkflowDelays(ctx, "T1", time.Now().UTC(), 10)
	if err != nil || resumed != 0 {
		t.Fatalf("resumed = %d err = %v, want nothing due yet", resumed, err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if message.Text == "after the wait" {
			t.Fatal("the step after the delay ran before the delay was due")
		}
	}

	// Once due, the run continues on its own and drains the built-in step
	// behind it.
	resumed, err = messages.ResumeWorkflowDelays(ctx, "T1", time.Now().UTC().Add(2*time.Hour), 10)
	if err != nil || resumed != 1 {
		t.Fatalf("resumed = %d err = %v, want the due delay to have moved", resumed, err)
	}
	after, err := messages.GetWorkflowRun(ctx, "T1", "U1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed once the wait finished", after.Status)
	}
	page, err = repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if message.Text == "after the wait" {
			return
		}
	}
	t.Fatalf("the step after the delay never ran: %+v", page.Messages)
}

// Two sweeps must not resume the same wait twice. Advancing is a compare-and-set
// against the run's current position, so the second sweep finds nothing rather
// than running the following step again.
func TestDueDelayResumesExactlyOnce(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	publishAndRun(t, messages, workflow, `[
		{"id":"wait","type":"delay","delay":{"seconds":60}},
		{"id":"say","type":"message","message":{"conversation":"C1","text":"exactly once"}}
	]`, `{}`, "once")

	due := time.Now().UTC().Add(time.Hour)
	if resumed, err := messages.ResumeWorkflowDelays(ctx, "T1", due, 10); err != nil || resumed != 1 {
		t.Fatalf("first sweep resumed = %d err = %v", resumed, err)
	}
	if resumed, err := messages.ResumeWorkflowDelays(ctx, "T1", due, 10); err != nil || resumed != 0 {
		t.Fatalf("second sweep resumed = %d err = %v, want the wait already spent", resumed, err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range page.Messages {
		if message.Text == "exactly once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the step after the delay ran %d times, want once", count)
	}
}

// A delay is bounded and must be positive: a mistyped wait should be refused at
// publish time rather than parking a run past any horizon anyone would check.
func TestDelayStepDurationIsBounded(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	for name, steps := range map[string]string{
		"zero":     `[{"id":"a","type":"delay","delay":{"seconds":0}}]`,
		"negative": `[{"id":"a","type":"delay","delay":{"seconds":-60}}]`,
		"too long": `[{"id":"a","type":"delay","delay":{"seconds":5184000}}]`,
		"missing":  `[{"id":"a","type":"delay"}]`,
	} {
		candidate := workflow
		candidate.Steps = steps
		if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", candidate, workflow.Version, true); err == nil {
			t.Errorf("a %s delay was accepted", name)
		}
	}
}

// Slack's "wait until a date" step. It reuses the delay machinery — the run
// parks on a durable wake instant and a worker resumes it — and differs only in
// how the instant is arrived at: a fixed moment rather than one measured from
// when the run reached the step.
func TestWaitUntilStepParksUntilTheNamedInstant(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	at := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	steps := `[
		{"id":"hold","type":"wait_until","wait_until":{"unix_seconds":` + strconv.FormatInt(at.Unix(), 10) + `}},
		{"id":"say","type":"message","message":{"conversation":"C1","text":"the day arrived"}}
	]`
	run := publishAndRun(t, messages, workflow, steps, `{}`, "wait-until")
	if run.Status != domain.WorkflowRunRunning {
		t.Fatalf("run status = %q, want it parked until the named instant", run.Status)
	}
	if resumed, err := messages.ResumeWorkflowDelays(ctx, "T1", at.Add(-time.Minute), 10); err != nil || resumed != 0 {
		t.Fatalf("resumed = %d err = %v, want nothing due a minute early", resumed, err)
	}
	if resumed, err := messages.ResumeWorkflowDelays(ctx, "T1", at, 10); err != nil || resumed != 1 {
		t.Fatalf("resumed = %d err = %v, want the wait due at the instant it names", resumed, err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if message.Text == "the day arrived" {
			return
		}
	}
	t.Fatalf("the step after the wait never ran: %+v", page.Messages)
}

// A date already past is not an error and must not park a run indefinitely: the
// wait it describes has been satisfied, so the next sweep resumes it.
func TestWaitUntilAPastInstantIsAlreadyDue(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	past := time.Now().UTC().Add(-48 * time.Hour).Unix()
	steps := `[{"id":"hold","type":"wait_until","wait_until":{"unix_seconds":` + strconv.FormatInt(past, 10) + `}}]`
	run := publishAndRun(t, messages, workflow, steps, `{}`, "wait-past")
	if resumed, err := messages.ResumeWorkflowDelays(ctx, "T1", time.Now().UTC(), 10); err != nil || resumed != 1 {
		t.Fatalf("resumed = %d err = %v, want a past instant to be due at once", resumed, err)
	}
	after, err := messages.GetWorkflowRun(ctx, "T1", "U1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.WorkflowRunCompleted {
		t.Fatalf("run status = %q, want completed", after.Status)
	}
}

func TestWaitUntilStepDefinitionIsValidated(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	for name, steps := range map[string]string{
		"no instant":     `[{"id":"a","type":"wait_until"}]`,
		"zero":           `[{"id":"a","type":"wait_until","wait_until":{"unix_seconds":0}}]`,
		"negative":       `[{"id":"a","type":"wait_until","wait_until":{"unix_seconds":-5}}]`,
		"a function too": `[{"id":"a","type":"wait_until","function_id":"triage","wait_until":{"unix_seconds":1800000000}}]`,
	} {
		candidate := workflow
		candidate.Steps = steps
		if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", candidate, workflow.Version, true); err == nil {
			t.Errorf("%s was accepted as a wait-until step", name)
		}
	}
}
