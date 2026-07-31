package service

import (
	"context"
	"encoding/json"
	"errors"
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
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run triage", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
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
