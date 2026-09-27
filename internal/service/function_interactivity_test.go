package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedFunctionApp installs a Socket Mode app A1 with a remote function
// "triage" and a bot UB, and publishes a workflow running that function.
func seedFunctionApp(t *testing.T) (context.Context, *memory.Store, Messages, domain.WorkflowTrigger) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test", Domain: "test"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}),
		repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "triage-bot"}),
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		repository.SeedConversationMember("C1", "U1"),
		repository.SeedConversationMember("C1", "UB"),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	key := []byte(strings.Repeat("k", 32))
	signing, err := secretbox.Seal(key, appSigningSecretAssociatedData("A1"), "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	verification, err := secretbox.Seal(key, appVerificationTokenAssociatedData("A1"), "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Triage"},
		"oauth_config":{"scopes":{"bot":["chat:write"]}},
		"settings":{"socket_mode_enabled":true,"function_runtime":"remote","interactivity":{"is_enabled":true}},
		"functions":{"triage":{"title":"Triage","description":"Asks for a decision",
			"input_parameters":{"properties":{"ticket":{"type":"string"}},"required":[]},
			"output_parameters":{"properties":{"decision":{"type":"string"}},"required":[]}}}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Triage", ClientID: "client",
		SigningSecretHash: domain.HashToken("signing-secret"), SigningSecretCiphertext: signing,
		VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", AppID: "A1", WorkspaceID: "T1", UserID: "UB", Name: "triage-bot", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-triage", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: domain.TokenBot, Scopes: []string{"chat:write"},
	}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: key}
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Incident triage", InputSchema: `{}`,
		Steps: `[{"function_id":"triage","title":"Triage","input_mapping":{"ticket":"literal"}}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workflow, err = messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true); err != nil {
		t.Fatal(err)
	}
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Run", Type: "link", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repository, messages, trigger
}

// deliveredFunctionExecuted runs the workflow and returns function_executed as
// the app receives it.
func deliveredFunctionExecuted(t *testing.T, ctx context.Context, repository *memory.Store, messages Messages, record events.Record) events.Delivered {
	t.Helper()
	prepared, visible, err := PrepareAppEvent(ctx, repository, messages.AppCredentialKey, "", "A1", record)
	if err != nil || !visible {
		t.Fatalf("prepared=%+v visible=%v err=%v", prepared, visible, err)
	}
	delivered, err := events.Deliverable(prepared.Event)
	if err != nil {
		t.Fatal(err)
	}
	return delivered
}

func functionExecutedRecord(t *testing.T, ctx context.Context, repository *memory.Store) events.Record {
	t.Helper()
	records, err := repository.ListEventsAfter(ctx, "T1", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Event.Topic == "function_executed" {
			return record
		}
	}
	t.Fatal("no function_executed event")
	return events.Record{}
}

type functionScopedPayload struct {
	Type           string `json:"type"`
	TriggerID      string `json:"trigger_id"`
	BotAccessToken string `json:"bot_access_token"`
	FunctionData   *struct {
		ExecutionID string `json:"execution_id"`
		Function    struct {
			CallbackID string `json:"callback_id"`
		} `json:"function"`
		Inputs map[string]any `json:"inputs"`
	} `json:"function_data"`
	Interactivity *struct {
		Interactor struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		} `json:"interactor"`
		InteractivityPointer string `json:"interactivity_pointer"`
	} `json:"interactivity"`
}

func claimFunctionScopedPayload(t *testing.T, ctx context.Context, repository *memory.Store, owner string) (functionScopedPayload, string) {
	t.Helper()
	claimed, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", owner, time.Minute)
	if err != nil || !found {
		t.Fatalf("interaction=%+v found=%v err=%v", claimed, found, err)
	}
	var payload functionScopedPayload
	if err := json.Unmarshal([]byte(claimed.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	return payload, claimed.Payload
}

// TestFunctionScopedInteractivityRoundTrip follows Slack's custom-step flow:
// function_executed hands the app an execution-scoped token; a message posted
// and a modal opened with it route their interactions back with
// function_data and that token; completing the execution with it expires it,
// and later interactions are ordinary again.
func TestFunctionScopedInteractivityRoundTrip(t *testing.T) {
	ctx, repository, messages, trigger := seedFunctionApp(t)
	if _, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "function-scoped"); err != nil {
		t.Fatal(err)
	}
	record := functionExecutedRecord(t, ctx, repository)
	delivered := deliveredFunctionExecuted(t, ctx, repository, messages, record)
	token, _ := delivered.Field("bot_access_token")
	executionID, _ := delivered.Field("function_execution_id")
	if !strings.HasPrefix(token, "xwfp-") || executionID == "" {
		t.Fatalf("bot_access_token=%q function_execution_id=%q, want an execution-scoped xwfp- token", token, executionID)
	}
	// A redelivery hands the app the same token, never a second one.
	if again, _ := deliveredFunctionExecuted(t, ctx, repository, messages, record).Field("bot_access_token"); again != token {
		t.Fatalf("redelivered token=%q, want %q", again, token)
	}
	credential, err := repository.LookupToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AppID != "A1" || credential.UserID != "UB" || credential.BotID != "B1" || !credential.TokenType.IsBot() ||
		credential.FunctionExecutionID != domain.WorkflowStepID(executionID) || credential.Revoked || !credential.ExpiresAt.IsZero() {
		t.Fatalf("execution token resolves to %+v", credential)
	}

	// A message posted with the execution token belongs to the execution.
	message, err := messages.PostMessageAs(ctx, "T1", "UB", domain.MessagePostRequest{
		Conversation: "C1", Text: "Approve?", AppID: "A1", BotID: "B1", FunctionExecutionID: credential.FunctionExecutionID,
		Blocks: `[{"type":"actions","block_id":"decide","elements":[{"type":"button","action_id":"approve","text":{"type":"plain_text","text":"Approve"},"value":"yes"}]}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: message.ID, BlockID: "decide", ActionID: "approve", Type: "button", Value: "yes",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	action, raw := claimFunctionScopedPayload(t, ctx, repository, "socket-action")
	if action.Type != "block_actions" || action.BotAccessToken != token || action.FunctionData == nil ||
		action.FunctionData.ExecutionID != executionID || action.FunctionData.Function.CallbackID != "triage" ||
		action.FunctionData.Inputs["ticket"] != "literal" || action.Interactivity == nil ||
		action.Interactivity.Interactor.ID != "U1" || action.Interactivity.Interactor.Secret == "" ||
		action.Interactivity.InteractivityPointer != action.TriggerID {
		t.Fatalf("function-scoped block_actions=%s", raw)
	}

	// The interactivity pointer opens a modal, which belongs to the execution
	// because the execution token opened it.
	view, err := messages.OpenView(ctx, "T1", "UB", "A1", action.Interactivity.InteractivityPointer,
		`{"type":"modal","callback_id":"reason","title":{"type":"plain_text","text":"Reason"},"submit":{"type":"plain_text","text":"Save"},"notify_on_close":true,"blocks":[{"type":"input","block_id":"why","label":{"type":"plain_text","text":"Why"},"element":{"type":"plain_text_input","action_id":"why_input"}}]}`,
		credential.FunctionExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if view.FunctionExecutionID != credential.FunctionExecutionID {
		t.Fatalf("view execution=%q, want %q", view.FunctionExecutionID, executionID)
	}
	if _, err := messages.SubmitView(ctx, "T1", "U1", "C1", view.ID, `{"values":{"why":{"why_input":{"type":"plain_text_input","value":"urgent"}}}}`, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	submission, raw := claimFunctionScopedPayload(t, ctx, repository, "socket-submission")
	if submission.Type != "view_submission" || submission.BotAccessToken != token || submission.FunctionData == nil ||
		submission.FunctionData.ExecutionID != executionID || submission.Interactivity == nil {
		t.Fatalf("function-scoped view_submission=%s", raw)
	}

	// Bolt's complete(): functions.completeSuccess with the execution token.
	if err := messages.CompleteFunction(ctx, "T1", credential.UserID, credential.AppID, credential.FunctionExecutionID, `{"decision":"yes"}`, ""); err != nil {
		t.Fatal(err)
	}
	credential, err = repository.LookupToken(ctx, token)
	if err != nil || credential.ExpiresAt.IsZero() || credential.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("token after completion=%+v err=%v, want it expired", credential, err)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: message.ID, BlockID: "decide", ActionID: "approve", Type: "button", Value: "yes",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	after, raw := claimFunctionScopedPayload(t, ctx, repository, "socket-after")
	if after.FunctionData != nil || after.BotAccessToken != "" || after.Interactivity != nil {
		t.Fatalf("interaction after the execution ended=%s, want an ordinary block_actions", raw)
	}
}

// TestFunctionExecutedOmitsTheTokenForAnAppWithoutABot: the execution token
// acts as the app's bot, so an app with no bot installation is handed none.
func TestFunctionExecutedOmitsTheTokenForAnAppWithoutABot(t *testing.T) {
	ctx, repository, messages, trigger := seedFunctionApp(t)
	if err := repository.RevokeToken(ctx, "xoxb-triage"); err != nil {
		t.Fatal(err)
	}
	// A user authorization keeps the dispatch visible to the app.
	if err := repository.SeedToken(ctx, "xoxp-triage", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: domain.TokenUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{}`, "no-bot"); err != nil {
		t.Fatal(err)
	}
	delivered := deliveredFunctionExecuted(t, ctx, repository, messages, functionExecutedRecord(t, ctx, repository))
	if token, exists := delivered.Field("bot_access_token"); exists {
		t.Fatalf("bot_access_token=%q for an app without a bot", token)
	}
}
