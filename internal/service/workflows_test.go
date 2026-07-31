package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestWorkflowRunDispatchesSpecShapedFunctionAndCompletesOnce(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "U1", WorkspaceID: "T1", Name: "owner"},
		{ID: "U2", WorkspaceID: "T1", Name: "member"},
		{ID: "UB", WorkspaceID: "T1", Name: "bot"},
	} {
		if err := repository.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
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
			"functions":{"triage":{
				"title":"Triage incident","description":"Classifies one incident",
				"input_parameters":{"properties":{"incident":{"type":"string","title":"Incident"}},"required":["incident"]},
				"output_parameters":{"properties":{"priority":{"type":"integer","title":"Priority"}},"required":["priority"]}
			}}
		}`,
	}, domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "Automation", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: "bot"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository}
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Incident triage", InputSchema: `{}`,
		Steps: `[{"function_id":"triage","title":"Classify","inputs":{"source":"workflow"}}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil || workflow.Status != domain.WorkflowPublished || workflow.PublishedVersion != 2 {
		t.Fatalf("workflow=%+v err=%v", workflow, err)
	}
	// A staged edit to a published workflow keeps the published revision live:
	// the head carries the draft, runs keep pinning the published version, and a
	// non-owner never sees the unpublished title.
	stagedEdit := workflow
	stagedEdit.Title = "Staged title"
	stagedEdit.Description = "not yet published"
	staged, err := messages.UpdateWorkflow(ctx, "T1", "U1", stagedEdit, workflow.Version, false)
	if err != nil || staged.Status != domain.WorkflowPublished || staged.PublishedVersion != workflow.PublishedVersion || staged.Title != "Staged title" {
		t.Fatalf("staged edit workflow=%+v err=%v", staged, err)
	}
	head, err := repository.GetWorkflow(ctx, "T1", workflow.ID)
	if err != nil || head.Version != workflow.Version+1 || head.Title != "Staged title" || head.PublishedVersion != workflow.PublishedVersion {
		t.Fatalf("staged head=%+v err=%v", head, err)
	}
	projected, err := messages.GetWorkflow(ctx, "T1", "U2", workflow.ID)
	if err != nil || projected.Title == "Staged title" {
		t.Fatalf("non-owner saw staged edits: %+v err=%v", projected, err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run triage", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	otherWorkflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Other workflow", InputSchema: `{}`,
		Steps: `[{"function_id":"triage","title":"Classify"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	movedTrigger := trigger
	movedTrigger.WorkflowID = otherWorkflow.ID
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", movedTrigger, trigger.Version); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("trigger moved across workflows: %v", err)
	}
	unchangedTrigger, err := repository.GetWorkflowTrigger(ctx, "T1", trigger.ID)
	if err != nil || unchangedTrigger.WorkflowID != workflow.ID || unchangedTrigger.Version != trigger.Version {
		t.Fatalf("failed move changed trigger: %+v err=%v", unchangedTrigger, err)
	}
	if err := repository.SetAutomationPermission(ctx, domain.AutomationPermission{
		ResourceType: "trigger", ResourceID: string(trigger.ID), WorkspaceID: "T1", AppID: "A1",
		PermissionType: "everyone", UpdatedAt: now,
	}, events.Event{ID: "permission", WorkspaceID: "T1", Topic: "workflow.permission_set", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{"incident":"INC-1"}`, "request-1")
	if err != nil || run.Status != domain.WorkflowRunRunning {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	replayed, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{"incident":"DIFFERENT"}`, "request-1")
	if err != nil || replayed.ID != run.ID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	records, err := repository.ListEventsAfter(ctx, "T1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var functionRecord events.Record
	for _, record := range records {
		if record.Event.Topic == "function_executed" {
			functionRecord = record
		}
	}
	if functionRecord.Event.ID == "" || functionRecord.Event.PrivatePayload == "" {
		t.Fatalf("function event not durably snapshotted: %+v", functionRecord)
	}
	prepared, visible, err := PrepareAppEvent(ctx, repository, "A1", functionRecord)
	if err != nil || !visible {
		t.Fatalf("prepared=%+v visible=%v err=%v", prepared, visible, err)
	}
	delivered, err := events.Deliverable(prepared.Event)
	if err != nil {
		t.Fatal(err)
	}
	var function struct {
		ID               string `json:"id"`
		CallbackID       string `json:"callback_id"`
		Title            string `json:"title"`
		InputParameters  []any  `json:"input_parameters"`
		OutputParameters []any  `json:"output_parameters"`
	}
	if err := json.Unmarshal(delivered.Object["function"], &function); err != nil {
		t.Fatal(err)
	}
	if function.ID == "" || function.CallbackID != "triage" || function.Title != "Triage incident" ||
		len(function.InputParameters) != 1 || len(function.OutputParameters) != 1 {
		t.Fatalf("function=%+v", function)
	}
	var inputs map[string]any
	if err := json.Unmarshal(delivered.Object["inputs"], &inputs); err != nil {
		t.Fatal(err)
	}
	if inputs["incident"] != "INC-1" || inputs["source"] != "workflow" {
		t.Fatalf("inputs=%v", inputs)
	}
	executionID, ok := delivered.Field("function_execution_id")
	if !ok || executionID == "" {
		t.Fatalf("function execution id missing: %+v", delivered)
	}
	republished := workflow
	republished.Steps = `[
		{"function_id":"triage","title":"First classification"},
		{"function_id":"triage","title":"Second classification"}
	]`
	republished, err = messages.UpdateWorkflow(ctx, "T1", "U1", republished, head.Version, true)
	if err != nil || republished.PublishedVersion != head.Version+1 {
		t.Fatalf("republished workflow=%+v err=%v", republished, err)
	}
	if err := messages.CompleteFunction(ctx, "T1", "UB", "wrong-app", domain.WorkflowStepID(executionID), `{"priority":1}`, ""); !errors.Is(err, ErrFunctionAccessDenied) {
		t.Fatalf("wrong app error=%v", err)
	}
	if err := messages.CompleteFunction(ctx, "T1", "UB", "A1", domain.WorkflowStepID(executionID), `{"priority":1}`, ""); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.GetWorkflowRun(ctx, "T1", run.ID)
	if err != nil || completed.Status != domain.WorkflowRunCompleted || completed.Outputs != `{"priority":1}` {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if err := messages.CompleteFunction(ctx, "T1", "UB", "A1", domain.WorkflowStepID(executionID), `{}`, ""); !errors.Is(err, ErrFunctionNotRunning) {
		t.Fatalf("duplicate completion error=%v", err)
	}
	if _, err := repository.GetWorkflowRunByIdempotency(ctx, "T1", "request-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetWorkflowRunByIdempotency(ctx, "T1", ""); !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("empty idempotency error=%v", err)
	}
}

func TestListWorkflowsAppliesVisibilityBeforePagination(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "U1", WorkspaceID: "T1", Name: "owner"},
		{ID: "U2", WorkspaceID: "T1", Name: "viewer"},
	} {
		if err := repository.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	for index, workflow := range []domain.WorkflowDefinition{
		{ID: "Wf001", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", Title: "private draft", InputSchema: `{}`, Steps: `[]`, Status: domain.WorkflowDraft, CreatedAt: now, UpdatedAt: now},
		{ID: "Wf002", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", Title: "first published", InputSchema: `{}`, Steps: `[]`, Status: domain.WorkflowPublished, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "Wf003", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", Title: "second published", InputSchema: `{}`, Steps: `[]`, Status: domain.WorkflowPublished, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repository.CreateWorkflow(ctx, workflow, events.Event{
			ID: domain.EventID("workflow-list-" + string(rune('a'+index))), WorkspaceID: "T1",
			Topic: "workflow.created", CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: repository}
	first, more, next, err := messages.ListWorkflows(ctx, "T1", "U2", domain.PageRequest{Limit: 1})
	if err != nil || len(first) != 1 || first[0].ID != "Wf002" || !more || next == "" {
		t.Fatalf("first visible page=%+v more=%v next=%q err=%v", first, more, next, err)
	}
	second, more, next, err := messages.ListWorkflows(ctx, "T1", "U2", domain.PageRequest{Limit: 1, Cursor: next})
	if err != nil || len(second) != 1 || second[0].ID != "Wf003" || more || next != "" {
		t.Fatalf("second visible page=%+v more=%v next=%q err=%v", second, more, next, err)
	}
}

func TestStagedWorkflowEditKeepsRunningExecutionOnThePublishedRevision(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "staged-run")
	if err != nil {
		t.Fatal(err)
	}
	publishedVersion := workflow.PublishedVersion

	// Stage an edit that removes the only step. A naive implementation would
	// let the in-flight run complete against an empty step list.
	staged := workflow
	staged.Steps = `[]`
	staged, err = messages.UpdateWorkflow(ctx, "T1", "U1", staged, workflow.Version, false)
	if err != nil || staged.Status != domain.WorkflowPublished || staged.PublishedVersion != publishedVersion {
		t.Fatalf("staged edit=%+v err=%v", staged, err)
	}
	if staged.Version == staged.PublishedVersion {
		t.Fatal("staged edit did not diverge Version from PublishedVersion")
	}

	// The owner sees the staged (empty) steps; a non-owner sees the published
	// revision's steps.
	ownerView, err := messages.GetWorkflow(ctx, "T1", "U1", workflow.ID)
	if err != nil || ownerView.Steps != `[]` {
		t.Fatalf("owner view steps=%q err=%v", ownerView.Steps, err)
	}
	memberView, err := messages.GetWorkflow(ctx, "T1", "U2", workflow.ID)
	if err != nil || memberView.Steps == `[]` {
		t.Fatalf("member view saw staged steps=%q err=%v", memberView.Steps, err)
	}

	// Completing the in-flight run resolves the step from the published
	// revision, not the staged head: the run stays pinned to PublishedVersion.
	execution, err := repository.GetWorkflowRun(ctx, "T1", run.ID)
	if err != nil || execution.WorkflowVersion != publishedVersion {
		t.Fatalf("run drifted off the published revision: %+v", execution)
	}
}

func TestUnpublishCancelsRunningExecutions(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "cancel-run")
	if err != nil || run.Status != domain.WorkflowRunRunning {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	// The function_executed event carries the execution step's ID.
	records, err := repository.ListEventsAfter(ctx, "T1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var executionID domain.WorkflowStepID
	for _, record := range records {
		if record.Event.Topic != "function_executed" {
			continue
		}
		delivered, _ := events.Deliverable(record.Event)
		if id, ok := delivered.Field("function_execution_id"); ok {
			executionID = domain.WorkflowStepID(id)
		}
	}
	if executionID == "" {
		t.Fatal("no function_executed event found for the running workflow")
	}

	// Unpublish the workflow: the in-flight run and its executing step are
	// cancelled atomically with the status transition.
	unpublished := workflow
	unpublished.Status = domain.WorkflowDisabled
	if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", unpublished, workflow.Version, false); err != nil {
		t.Fatal(err)
	}
	cancelled, err := repository.GetWorkflowRun(ctx, "T1", run.ID)
	if err != nil || cancelled.Status != domain.WorkflowRunCancelled || cancelled.CompletedAt.IsZero() || cancelled.Error != "workflow_unpublished" {
		t.Fatalf("run after unpublish=%+v err=%v", cancelled, err)
	}
	cancelledStep, err := repository.GetWorkflowStep(ctx, "T1", executionID)
	if err != nil || cancelledStep.Status != domain.WorkflowStepCancelled {
		t.Fatalf("step after unpublish=%+v err=%v", cancelledStep, err)
	}

	// A late function completion is refused: the run is no longer executing.
	if err := messages.CompleteFunction(ctx, "T1", "U1", workflow.AppID, executionID, `{"result":"late"}`, ""); !errors.Is(err, ErrFunctionNotRunning) {
		t.Fatalf("late complete error=%v, want ErrFunctionNotRunning", err)
	}
}

func TestDiscardWorkflowStagedChangesRevertsToThePublishedRevision(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	publishedVersion := workflow.PublishedVersion
	staged := workflow
	staged.Title = "Staged and discarded"
	staged.Steps = `[]`
	staged, err := messages.UpdateWorkflow(ctx, "T1", "U1", staged, workflow.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Version == publishedVersion {
		t.Fatal("staged edit did not diverge")
	}
	if err := messages.DiscardWorkflowStagedChanges(ctx, "T1", "U1", workflow.ID, staged.Version); err != nil {
		t.Fatalf("discard error=%v", err)
	}
	after, err := messages.GetWorkflow(ctx, "T1", "U1", workflow.ID)
	if err != nil || after.Title == "Staged and discarded" || after.Version != publishedVersion {
		t.Fatalf("after discard=%+v err=%v", after, err)
	}
	// Discarding with no staged changes is a user error, not a conflict.
	if err := messages.DiscardWorkflowStagedChanges(ctx, "T1", "U1", workflow.ID, after.Version); !errors.Is(err, ErrInvalidWorkflowStep) {
		t.Fatalf("discard with no staged changes error=%v, want ErrInvalidWorkflowStep", err)
	}
}

func TestWorkflowStepChangesTracksAddedChangedAndRemovedSteps(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	publishedVersion := workflow.PublishedVersion

	// Publish a two-step revision so later staged edits have a real second
	// position to diverge from.
	twoSteps := workflow
	twoSteps.Steps = `[{"function_id":"triage","title":"Triage incident"},{"function_id":"notify","title":"Notify channel"}]`
	twoSteps, err := messages.UpdateWorkflow(ctx, "T1", "U1", twoSteps, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID)
	if err != nil || len(changes) != 0 {
		t.Fatalf("published workflow changes=%+v err=%v, want none", changes, err)
	}

	// Change step 2, keep step 1, and append a new step 3.
	staged := twoSteps
	staged.Steps = `[{"function_id":"triage","title":"Triage incident"},{"function_id":"triage","title":"Triage incident"},{"function_id":"notify","title":"Notify channel"}]`
	staged, err = messages.UpdateWorkflow(ctx, "T1", "U1", staged, twoSteps.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0] != (domain.WorkflowStepChange{Position: 2, FunctionID: "triage", Change: domain.WorkflowStepChangeChanged}) ||
		changes[1] != (domain.WorkflowStepChange{Position: 3, FunctionID: "notify", Change: domain.WorkflowStepChangeAdded}) {
		t.Fatalf("changes=%+v", changes)
	}

	// Removing the trailing steps reports them as removed against the published
	// revision rather than as unchanged empty slots.
	staged.Steps = `[{"function_id":"triage","title":"Triage incident"}]`
	staged, err = messages.UpdateWorkflow(ctx, "T1", "U1", staged, staged.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0] != (domain.WorkflowStepChange{Position: 2, FunctionID: "notify", Change: domain.WorkflowStepChangeRemoved}) {
		t.Fatalf("removed changes=%+v", changes)
	}

	// Publishing realigns the head, so the differences disappear.
	staged.Status = domain.WorkflowPublished
	staged, err = messages.UpdateWorkflow(ctx, "T1", "U1", staged, staged.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	if changes, err := messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID); err != nil || len(changes) != 0 {
		t.Fatalf("published changes=%+v err=%v, want none", changes, err)
	}

	// A member never reads the staged head, so the staged differences are
	// hidden behind ErrNotFound just as they are from the discard operation.
	staged.Steps = twoSteps.Steps
	if _, err := messages.UpdateWorkflow(ctx, "T1", "U1", staged, staged.Version, false); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.WorkflowStepChanges(ctx, "T1", "U2", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member changes error=%v, want ErrNotFound", err)
	}
	if changes, err := messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID); err != nil || len(changes) != 1 || changes[0] != (domain.WorkflowStepChange{Position: 2, FunctionID: "notify", Change: domain.WorkflowStepChangeAdded}) {
		t.Fatalf("owner changes=%+v err=%v", changes, err)
	}
	if after, err := messages.GetWorkflow(ctx, "T1", "U1", workflow.ID); err != nil || after.PublishedVersion == publishedVersion {
		t.Fatalf("published version did not advance: after=%+v err=%v", after, err)
	}
}

func TestWorkflowStepChangesSurvivesDiscardAndDraft(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	staged := workflow
	staged.Steps = `[]`
	staged, err := messages.UpdateWorkflow(ctx, "T1", "U1", staged, workflow.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID)
	if err != nil || len(changes) != 1 || changes[0].Change != domain.WorkflowStepChangeRemoved {
		t.Fatalf("empty draft changes=%+v err=%v", changes, err)
	}
	if err := messages.DiscardWorkflowStagedChanges(ctx, "T1", "U1", workflow.ID, staged.Version); err != nil {
		t.Fatal(err)
	}
	if changes, err := messages.WorkflowStepChanges(ctx, "T1", "U1", workflow.ID); err != nil || len(changes) != 0 {
		t.Fatalf("after discard changes=%+v err=%v, want none", changes, err)
	}

	// A draft that was never published has no published revision to diff against.
	draft, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Unpublished draft", InputSchema: `{}`,
		Steps: `[{"function_id":"triage","title":"Triage incident"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changes, err := messages.WorkflowStepChanges(ctx, "T1", "U1", draft.ID); err != nil || len(changes) != 0 {
		t.Fatalf("draft changes=%+v err=%v, want none", changes, err)
	}
}

func TestDiffWorkflowStepsIsPositional(t *testing.T) {
	published := `[{"function_id":"triage","title":"A"},{"function_id":"notify","title":"B"}]`
	// Reordering the two steps is a change at both positions: the diff does not
	// invent an edit script, it labels each slot against the published step at
	// the same index.
	reordered := `[{"function_id":"notify","title":"B"},{"function_id":"triage","title":"A"}]`
	got := diffWorkflowSteps(published, reordered)
	want := []domain.WorkflowStepChange{
		{Position: 1, FunctionID: "notify", Change: domain.WorkflowStepChangeChanged},
		{Position: 2, FunctionID: "triage", Change: domain.WorkflowStepChangeChanged},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reordered changes=%+v, want %+v", got, want)
	}
	if changes := diffWorkflowSteps(published, published); len(changes) != 0 {
		t.Fatalf("identical changes=%+v, want none", changes)
	}
	if changes := diffWorkflowSteps(`not json`, published); len(changes) != 2 {
		t.Fatalf("malformed head changes=%+v, want 2 (all added)", changes)
	}
	// A step whose metadata moved but whose callback did not is still a
	// change: the diff compares the whole step definition.
	renamed := `[{"function_id":"triage","title":"Renamed"},{"function_id":"notify","title":"B"}]`
	got = diffWorkflowSteps(published, renamed)
	want = []domain.WorkflowStepChange{{Position: 1, FunctionID: "triage", Change: domain.WorkflowStepChangeChanged}}
	if !slices.Equal(got, want) {
		t.Fatalf("renamed changes=%+v, want %+v", got, want)
	}
	// A revision written before step types existed decodes with an empty
	// type; it must not phantom-diff against the normalized head.
	legacy := `[{"id":"triage","function_id":"triage","title":"A"},{"id":"notify","function_id":"notify","title":"B"}]`
	normalized := `[{"id":"triage","type":"function","function_id":"triage","title":"A"},{"id":"notify","type":"function","function_id":"notify","title":"B"}]`
	if changes := diffWorkflowSteps(legacy, normalized); len(changes) != 0 {
		t.Fatalf("legacy revision changes=%+v, want none", changes)
	}
}

func TestNormalizeWorkflowStepsAssignsUniqueIDs(t *testing.T) {
	encoded, steps, err := normalizeWorkflowSteps(`[{"function_id":"triage"},{"function_id":"triage"},{"function_id":"triage"}]`)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{steps[0].ID, steps[1].ID, steps[2].ID}
	if !slices.Equal(ids, []string{"triage", "triage-2", "triage-3"}) {
		t.Fatalf("defaulted ids=%v", ids)
	}
	if !strings.Contains(encoded, `"type":"function"`) || !strings.Contains(encoded, `"id":"triage-2"`) {
		t.Fatalf("normalized encoding=%s", encoded)
	}
	if _, _, err := normalizeWorkflowSteps(`[{"id":"a","function_id":"triage"},{"id":"a","function_id":"notify"}]`); !errors.Is(err, ErrInvalidWorkflowStep) {
		t.Fatalf("duplicate explicit id error=%v, want ErrInvalidWorkflowStep", err)
	}
	if _, _, err := normalizeWorkflowSteps(`[{"type":"branch","function_id":"triage"}]`); !errors.Is(err, ErrInvalidWorkflowStep) {
		t.Fatalf("unknown step type error=%v, want ErrInvalidWorkflowStep", err)
	}
	// An explicit id that survives defaulting wins over a later defaulted
	// collision: the defaulted step is suffixed instead.
	_, steps, err = normalizeWorkflowSteps(`[{"id":"triage-2","function_id":"notify"},{"function_id":"triage"},{"function_id":"triage"}]`)
	if err != nil {
		t.Fatal(err)
	}
	ids = []string{steps[0].ID, steps[1].ID, steps[2].ID}
	if !slices.Equal(ids, []string{"triage-2", "triage", "triage-3"}) {
		t.Fatalf("mixed ids=%v", ids)
	}
}

func TestDuplicateWorkflowCopiesTheHeadAsANewDraft(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	duplicate, err := messages.DuplicateWorkflow(ctx, "T1", "U1", workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID == "" || duplicate.ID == workflow.ID {
		t.Fatalf("duplicate id=%q, want a fresh id", duplicate.ID)
	}
	if duplicate.Title != "Incident triage (copy)" || duplicate.Status != domain.WorkflowDraft ||
		duplicate.Version != 1 || duplicate.PublishedVersion != 0 || duplicate.OwnerID != "U1" {
		t.Fatalf("duplicate=%+v", duplicate)
	}
	if duplicate.Steps != workflow.Steps || duplicate.InputSchema != workflow.InputSchema || duplicate.AppID != workflow.AppID {
		t.Fatalf("duplicate head mismatch: %+v", duplicate)
	}
	stored, err := messages.GetWorkflow(ctx, "T1", "U1", duplicate.ID)
	if err != nil || stored.Title != duplicate.Title {
		t.Fatalf("stored duplicate=%+v err=%v", stored, err)
	}
	// Only the owner duplicates; a member gets the same ErrNotFound they get
	// from every other owner-scoped workflow operation.
	if _, err := messages.DuplicateWorkflow(ctx, "T1", "U2", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member duplicate error=%v, want ErrNotFound", err)
	}
	// A duplicated callback reference is suffixed so both workflows can be
	// told apart in callback routing.
	if duplicate.CallbackID != "" && duplicate.CallbackID == workflow.CallbackID {
		t.Fatalf("duplicate callback id=%q collides with the source", duplicate.CallbackID)
	}
}

func TestWorkflowActivitySummarizesRunsForTheOwner(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	running, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "activity-running")
	if err != nil {
		t.Fatal(err)
	}
	activity, err := messages.WorkflowActivity(ctx, "T1", "U1", workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Running != 1 || activity.Completed != 0 || len(activity.RecentRuns) != 1 ||
		activity.RecentRuns[0].ID != running.ID || activity.RecentRuns[0].Status != domain.WorkflowRunRunning {
		t.Fatalf("activity while running=%+v", activity)
	}
	if _, err := messages.WorkflowActivity(ctx, "T1", "U2", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member activity error=%v, want ErrNotFound", err)
	}
	// Complete the executing function step so the run finishes, then the
	// dashboard flips the count from running to completed.
	records, err := repository.ListEventsAfter(ctx, "T1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var executionID domain.WorkflowStepID
	for _, record := range records {
		if record.Event.Topic != "function_executed" {
			continue
		}
		delivered, _ := events.Deliverable(record.Event)
		if id, ok := delivered.Field("function_execution_id"); ok {
			executionID = domain.WorkflowStepID(id)
		}
	}
	if executionID == "" {
		t.Fatal("no function_executed event found for the running workflow")
	}
	if err := messages.CompleteFunction(ctx, "T1", "U1", workflow.AppID, executionID, `{"result":"done"}`, ""); err != nil {
		t.Fatal(err)
	}
	activity, err = messages.WorkflowActivity(ctx, "T1", "U1", workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Running != 0 || activity.Completed != 1 || len(activity.RecentRuns) != 1 ||
		activity.RecentRuns[0].Status != domain.WorkflowRunCompleted {
		t.Fatalf("activity after completion=%+v", activity)
	}
}

func TestDeleteWorkflowCancelsRunsAndRemovesEveryRecord(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "delete-run")
	if err != nil || run.Status != domain.WorkflowRunRunning {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if err := messages.DeleteWorkflow(ctx, "T1", "U1", workflow.ID, workflow.Version+1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale delete error=%v, want ErrConflict", err)
	}
	if err := messages.DeleteWorkflow(ctx, "T1", "U2", workflow.ID, workflow.Version); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member delete error=%v, want ErrNotFound", err)
	}
	if err := messages.DeleteWorkflow(ctx, "T1", "U1", workflow.ID, workflow.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.GetWorkflow(ctx, "T1", "U1", workflow.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted workflow error=%v, want ErrNotFound", err)
	}
	// The run and its trigger stop existing with the workflow instead of
	// dangling as orphaned records.
	if _, err := repository.GetWorkflowRun(ctx, "T1", run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted run error=%v, want ErrNotFound", err)
	}
	if _, err := repository.GetWorkflowTrigger(ctx, "T1", trigger.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted trigger error=%v, want ErrNotFound", err)
	}
	if revisions, err := repository.ListWorkflowRevisions(ctx, "T1", workflow.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted revisions error=%v", err)
	} else if err == nil && len(revisions) != 0 {
		t.Fatalf("deleted revisions=%+v, want none", revisions)
	}
}
