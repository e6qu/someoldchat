package grpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/service"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// This file is the differential harness for the seam.
//
// The architecture claims that "the same module interfaces support direct Go
// calls in monolith mode and generated gRPC adapters in distributed mode". No
// test asserted it: every existing transport test drives the Remote alone and
// compares it against absolute expectations, so a behaviour that the adapter
// changed — an error class the client could not restore, a field a decoder
// dropped, a parameter the client ignored — was invisible until it reached a
// deployment. Ten defects of exactly that shape accumulated behind the gap.
//
// A case here runs one operation twice: once against service.Messages called
// directly, and once against the same implementation behind a real gRPC server
// and client over a bufconn listener, with two independently seeded stores that
// start in the same state. Both outcomes must agree on
//
//   - success or failure,
//   - the classification of a failure against *every* sentinel in the error table
//     (not only the sentinel a case expects, which is what makes a case reject a
//     wrong-but-plausible sentinel such as reactions.add answering
//     service.ErrEmojiAlreadyExists instead of store.ErrAlreadyExists),
//   - the gRPC code the remote failure carries, which must be the code the local
//     failure maps to, and
//   - the value the operation projects.

// chatCaller is everything a caller holds of the chat module.
//
// The auth reads are a separate handle because that is how the composition roots
// wire them: in the monolith they resolve against the store, and in the split
// deployment against the Remote (cmd/server binds tokens, sessions and the
// session revoker to the same value). A case that exercises a token or session
// record has to go through the handle the deployment actually uses.
type chatCaller struct {
	chatapi.Service
	Tokens auth.TokenStore
}

// chatWorld is one composition of the chat module.
type chatWorld struct {
	name    string
	store   *memory.Store
	service chatCaller
}

// parity is the pair of compositions under comparison.
type parity struct {
	local  chatWorld
	remote chatWorld
}

// parityCase is one operation that must behave identically in both
// compositions.
type parityCase struct {
	name string

	// blobs provisions blob storage. A case that leaves it false exercises the
	// service.ErrBlobUnavailable path.
	blobs bool

	// seed prepares a store. It runs once per composition with an empty store, so
	// both compositions start from the same state.
	seed func(t *testing.T, target *memory.Store)

	// operate runs the case against one composition and returns the value to
	// compare. A failure case returns a nil value; a success case must project
	// only values that do not depend on generated identifiers or on wall-clock
	// time, because the two compositions generate their own.
	operate func(ctx context.Context, chat chatCaller) (any, error)

	// wantSentinel is the sentinel both compositions must fail with, or nil when
	// both must succeed.
	wantSentinel error

	// wantAgreedFailure marks a case where both compositions must fail and the
	// class is whatever the implementation gives, which the sweep below then
	// requires them to agree on.
	//
	// It replaces a wantUnclassifiedFailure flag that asserted the *absence* of
	// a domain class. That assertion ratified a defect: a non-positive page
	// bound is refused by a store guard that returns a bare errors.New
	// (internal/store/memory/memory.go "event limit must be positive",
	// internal/store/sqlstore/sqlstore.go "invalid Socket Mode response lease"),
	// so classifyErrors matches nothing and mapError falls through to
	// codes.Unavailable — HTTP 503, which asks a caller to retry a request that
	// can never succeed, for a request that is simply malformed. Nine RPCs
	// answer that way. Locking it in with a test meant giving those store guards
	// a sentinel would turn the suite red for doing the right thing. This flag
	// keeps the parity requirement, which is what the harness is for, and stops
	// asserting the class is missing; it passes before and after the store is
	// fixed. The fix itself belongs to internal/store and is reported.
	wantAgreedFailure bool
}

func seedBaseline(t *testing.T, target *memory.Store) {
	t.Helper()
	requireSeed(t, target.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}))
	requireSeed(t, target.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", RealName: "Alice Example", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}}))
	requireSeed(t, target.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", Email: "bob@example.com"}))
	// UA is the workspace administrator. Administrative operations are refused to
	// U1 and U2, who are plain members, so a case that drives one has to name the
	// authority it is claiming instead of relying on membership alone.
	requireSeed(t, target.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin", Email: "admin@example.com"}))
	requireSeed(t, seedWorkspaceRole(target, "T1", "UA", domain.WorkspaceRoleAdmin))
	requireSeed(t, target.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}))
	requireSeed(t, target.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "second"}))
	requireSeed(t, target.SeedConversationMember("C1", "U1"))
	requireSeed(t, target.SeedConversationMember("C1", "U2"))
	requireSeed(t, target.SeedConversationMember("C2", "U1"))
}

// seedWorkspaceRole promotes a seeded user. memory.Store.SeedUser creates a
// member membership, which is deliberately not enough for any administrative
// operation.
func seedWorkspaceRole(target *memory.Store, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole) error {
	event, err := events.New(domain.EventID("evt_seed_role_"+string(userID)), workspaceID, "", events.NewPayload("workspace.role_changed", events.String("user_id", string(userID)), events.String("role", string(role))), time.Now().UTC())
	if err != nil {
		return err
	}
	return target.SetWorkspaceRole(context.Background(), workspaceID, userID, role, event)
}

func requireSeed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func seedWorkflowParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	now := time.Unix(1_700_000_000, 0).UTC()
	manifest := `{"display_information":{"name":"Workflow parity"},"settings":{"function_runtime":"remote"},"functions":{"triage":{"title":"Triage","description":"Triage a request","input_parameters":{"properties":{"item":{"type":"string","title":"Item"}},"required":["item"]},"output_parameters":{"properties":{"result":{"type":"string","title":"Result"}},"required":["result"]}}}}`
	requireSeed(t, target.CreateApp(context.Background(), domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Workflow parity", ClientID: "workflow-client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "workflow-client", SecretHash: "client-hash", AppID: "A1"}))
	requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
	}))
	workflow := domain.WorkflowDefinition{
		ID: "WfParity", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", CallbackID: "triage-workflow",
		Title: "Triage workflow", Icon: "🚨", InputSchema: `{}`, Steps: `[{"function_id":"triage","title":"Triage request"}]`,
		Status: domain.WorkflowPublished, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.CreateWorkflow(context.Background(), workflow, events.Event{
		ID: "evt_workflow_parity", WorkspaceID: "T1", Topic: "workflow.created", CreatedAt: now,
	}))
	trigger := domain.WorkflowTrigger{
		ID: "FtParity", WorkflowID: workflow.ID, WorkspaceID: "T1", AppID: "A1", Title: "Run triage",
		Type: "link", Config: `{}`, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.SetWorkflowTrigger(context.Background(), trigger, 0, events.Event{
		ID: "evt_trigger_parity", WorkspaceID: "T1", Topic: "workflow.trigger_created", CreatedAt: now,
	}))
	run := domain.WorkflowRun{
		ID: "WxParity", WorkflowID: workflow.ID, WorkflowVersion: 1, TriggerID: trigger.ID,
		WorkspaceID: "T1", AppID: "A1", ActorID: "U1", ConversationID: "C1",
		Status: domain.WorkflowRunRunning, Inputs: `{"item":"request"}`, Outputs: `{}`,
		CreatedAt: now, UpdatedAt: now,
	}
	execution := domain.WorkflowStep{
		ID: "FxParity", WorkflowRunID: run.ID, WorkspaceID: "T1", AppID: "A1", UserID: "U1",
		FunctionID: "FnParity", EditID: "triage", Status: domain.WorkflowStepExecuting,
		Inputs: run.Inputs, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.CreateWorkflowRun(context.Background(), run, &execution, []events.Event{{
		ID: "evt_run_parity", WorkspaceID: "T1", Topic: "workflow.run_started", CreatedAt: now,
	}}))
}

func seedFormParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedWorkflowParity(t, target)
	now := time.Unix(1_700_000_200, 0).UTC()
	workflow := domain.WorkflowDefinition{
		ID: "WfForm", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", CallbackID: "form-workflow",
		Title: "Form workflow", InputSchema: `{}`,
		Steps: `[
			{"id":"intake","type":"form","form":{"title":"Intake","inputs":{"name":"Name"}}},
			{"id":"approve","type":"button","button":{"label":"Approve"}},
			{"id":"triage","type":"function","function_id":"triage","title":"Triage"}
		]`,
		Status: domain.WorkflowPublished, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.CreateWorkflow(context.Background(), workflow, events.Event{
		ID: "evt_form_workflow", WorkspaceID: "T1", Topic: "workflow.created", CreatedAt: now,
	}))
	trigger := domain.WorkflowTrigger{
		ID: "FtForm", WorkflowID: workflow.ID, WorkspaceID: "T1", AppID: "A1", Title: "Run form",
		Type: "link", Config: `{}`, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.SetWorkflowTrigger(context.Background(), trigger, 0, events.Event{
		ID: "evt_form_trigger", WorkspaceID: "T1", Topic: "workflow.trigger_created", CreatedAt: now,
	}))
	run := domain.WorkflowRun{
		ID: "WxForm", WorkflowID: workflow.ID, WorkflowVersion: 1, TriggerID: trigger.ID,
		WorkspaceID: "T1", AppID: "A1", ActorID: "U1", ConversationID: "C1",
		Status: domain.WorkflowRunRunning, Inputs: `{}`, Outputs: `{}`, CurrentStep: 0,
		CreatedAt: now, UpdatedAt: now,
	}
	waiting := domain.WorkflowStep{
		ID: "FxForm", WorkflowRunID: run.ID, WorkspaceID: "T1", AppID: "A1", UserID: "U1",
		EditID: "intake", Status: domain.WorkflowStepWaiting, Inputs: `{}`, Outputs: `{}`,
		StepName: "Intake", CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.CreateWorkflowRun(context.Background(), run, &waiting, []events.Event{{
		ID: "evt_form_run", WorkspaceID: "T1", Topic: "workflow.run_started", CreatedAt: now,
	}}))
}

func seedBranchParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedWorkflowParity(t, target)
	now := time.Unix(1_700_000_100, 0).UTC()
	workflow := domain.WorkflowDefinition{
		ID: "WfBranch", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", CallbackID: "branch-workflow",
		Title: "Branched workflow", InputSchema: `{}`,
		Steps: `[
			{"id":"triage","type":"function","function_id":"triage","title":"Classify"},
			{"id":"triage-2","type":"function","function_id":"triage","title":"Escalate","condition":{"source":"inputs.severity","operator":"equals","value":"high"}},
			{"id":"triage-3","type":"function","function_id":"triage","title":"Log","condition":{"source":"steps.triage.outputs.result","operator":"contains","value":"ok"}}
		]`,
		Status: domain.WorkflowPublished, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.CreateWorkflow(context.Background(), workflow, events.Event{
		ID: "evt_branch_workflow", WorkspaceID: "T1", Topic: "workflow.created", CreatedAt: now,
	}))
	trigger := domain.WorkflowTrigger{
		ID: "FtBranch", WorkflowID: workflow.ID, WorkspaceID: "T1", AppID: "A1", Title: "Run branches",
		Type: "link", Config: `{}`, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	requireSeed(t, target.SetWorkflowTrigger(context.Background(), trigger, 0, events.Event{
		ID: "evt_branch_trigger", WorkspaceID: "T1", Topic: "workflow.trigger_created", CreatedAt: now,
	}))
	seedRun := func(runID domain.WorkflowRunID, inputs string, currentStep int, executing domain.WorkflowStep, completed *domain.WorkflowStep) {
		run := domain.WorkflowRun{
			ID: runID, WorkflowID: workflow.ID, WorkflowVersion: 1, TriggerID: trigger.ID,
			WorkspaceID: "T1", AppID: "A1", ActorID: "U1", ConversationID: "C1",
			Status: domain.WorkflowRunRunning, Inputs: inputs, Outputs: `{}`, CurrentStep: currentStep,
			CreatedAt: now, UpdatedAt: now,
		}
		requireSeed(t, target.CreateWorkflowRun(context.Background(), run, &executing, []events.Event{{
			ID: domain.EventID("evt_" + strings.ToLower(string(runID)) + "_started"), WorkspaceID: "T1", Topic: "workflow.run_started", CreatedAt: now,
		}}))
		if completed != nil {
			requireSeed(t, target.SetWorkflowStep(context.Background(), *completed, events.Event{
				ID: domain.EventID("evt_" + strings.ToLower(string(runID)) + "_first"), WorkspaceID: "T1", Topic: "function_executed", CreatedAt: now,
			}))
		}
	}
	seedRun("WxBranchHigh", `{"severity":"high"}`, 1,
		domain.WorkflowStep{
			ID: "FxBranchHigh", WorkflowRunID: "WxBranchHigh", WorkspaceID: "T1", AppID: "A1", UserID: "U1",
			FunctionID: "FnBranch", EditID: "triage-2", Status: domain.WorkflowStepExecuting,
			Inputs: `{}`, Outputs: `{}`, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
		},
		&domain.WorkflowStep{
			ID: "FxBranchHighFirst", WorkflowRunID: "WxBranchHigh", WorkspaceID: "T1", AppID: "A1", UserID: "U1",
			FunctionID: "FnBranch", EditID: "triage", Status: domain.WorkflowStepCompleted,
			Inputs: `{}`, Outputs: `{"result":"ok"}`, CreatedAt: now, UpdatedAt: now,
		})
	seedRun("WxBranchLow", `{"severity":"low"}`, 0,
		domain.WorkflowStep{
			ID: "FxBranchLow", WorkflowRunID: "WxBranchLow", WorkspaceID: "T1", AppID: "A1", UserID: "U1",
			FunctionID: "FnBranch", EditID: "triage", Status: domain.WorkflowStepExecuting,
			Inputs: `{"severity":"low"}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
		}, nil)
}

func newParity(t *testing.T, testCase parityCase) parity {
	t.Helper()
	seed := testCase.seed
	if seed == nil {
		seed = seedBaseline
	}
	build := func(name string) chatWorld {
		target := memory.New()
		seed(t, target)
		implementation := service.Messages{Store: target, AppCredentialKey: bytes.Repeat([]byte("k"), 32)}
		if testCase.blobs {
			blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatalf("blob storage: %v", err)
			}
			implementation.Blob = blobs
		}
		return chatWorld{name: name, store: target, service: chatCaller{Service: implementation, Tokens: target}}
	}

	local := build("local")
	remote := build("remote")

	server, err := NewChatServer(remote.service, remote.store, remote.store, remote.store, Observer{})
	if err != nil {
		t.Fatalf("chat server: %v", err)
	}
	listener := bufconn.Listen(1 << 20)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-served
	})

	dial := append([]grpclib.DialOption{
		grpclib.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpclib.WithTransportCredentials(insecure.NewCredentials()),
	}, DialOptions(Observer{})...)
	connection, err := grpclib.NewClient("passthrough:///bufnet", dial...)
	if err != nil {
		t.Fatalf("chat client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	adapter, err := NewRemote(connection)
	if err != nil {
		t.Fatalf("chat remote: %v", err)
	}
	remote.service = chatCaller{Service: adapter, Tokens: adapter}
	return parity{local: local, remote: remote}
}

func TestCompositionsAgreeOnEveryErrorClassAndValue(t *testing.T) {
	for _, testCase := range parityCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pair := newParity(t, testCase)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			localValue, localErr := testCase.operate(ctx, pair.local.service)
			remoteValue, remoteErr := testCase.operate(ctx, pair.remote.service)

			if (localErr == nil) != (remoteErr == nil) {
				t.Fatalf("local error = %v, remote error = %v: one composition failed and the other did not", localErr, remoteErr)
			}
			switch {
			case testCase.wantAgreedFailure:
				if localErr == nil {
					t.Fatal("both compositions succeeded, want a failure")
				}
			case testCase.wantSentinel == nil:
				if localErr != nil {
					t.Fatalf("both compositions failed with %v, want success", localErr)
				}
			default:
				if localErr == nil {
					t.Fatalf("both compositions succeeded, want %v", testCase.wantSentinel)
				}
				if !errors.Is(localErr, testCase.wantSentinel) {
					t.Fatalf("local error %v is not %v; the case no longer provokes the class it documents", localErr, testCase.wantSentinel)
				}
				if !errors.Is(remoteErr, testCase.wantSentinel) {
					t.Fatalf("remote error %v is not %v", remoteErr, testCase.wantSentinel)
				}
			}

			// The sweep is the point of the harness: the two compositions must
			// agree about every sentinel, so restoring a plausible neighbour
			// (service.ErrEmojiAlreadyExists for store.ErrAlreadyExists) fails
			// here even though the case only names one sentinel.
			for _, class := range errorClasses {
				if errors.Is(localErr, class.sentinel) != errors.Is(remoteErr, class.sentinel) {
					t.Errorf("errors.Is(err, %s): local = %t, remote = %t (local %v, remote %v)",
						class.key, errors.Is(localErr, class.sentinel), errors.Is(remoteErr, class.sentinel), localErr, remoteErr)
				}
			}
			if localErr != nil {
				if class, classified := classifyError(localErr); classified {
					if got := status.Code(remoteErr); got != class.code {
						t.Errorf("remote status code = %s, want %s (the code the local error maps to)", got, class.code)
					}
				}
			}
			if !reflect.DeepEqual(localValue, remoteValue) {
				t.Fatalf("projected value differs:\n local  = %#v\n remote = %#v", localValue, remoteValue)
			}
		})
	}
}

func parityCases() []parityCase {
	timestampOf := func(message domain.Message) domain.MessageTimestamp {
		return domain.NewMessageTimestamp(message.CreatedAt)
	}
	return []parityCase{
		{
			name: "workflow managers manage across the composition seam",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// Before assignment a member cannot manage; the owner names them.
				before, err := chat.CanManageWorkflow(ctx, "T1", "U2", "WfParity")
				if err != nil {
					return nil, err
				}
				assigned, err := chat.SetWorkflowManagers(ctx, "T1", "U1", "WfParity", []domain.UserID{"U2"})
				if err != nil {
					return nil, err
				}
				after, err := chat.CanManageWorkflow(ctx, "T1", "U2", "WfParity")
				if err != nil {
					return nil, err
				}
				// The manager edits the workflow; a non-manager is refused.
				edited, err := chat.GetWorkflow(ctx, "T1", "U2", "WfParity")
				if err != nil {
					return nil, err
				}
				edited.Title = "Manager edit"
				if _, err := chat.UpdateWorkflow(ctx, "T1", "U2", edited, edited.Version, false); err != nil {
					return nil, err
				}
				stored, err := chat.GetWorkflow(ctx, "T1", "U1", "WfParity")
				if err != nil {
					return nil, err
				}
				if _, err := chat.SetWorkflowManagers(ctx, "T1", "U2", "WfParity", []domain.UserID{"U2"}); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("manager set-managers error=%v, want ErrNotFound", err)
				}
				return []any{
					before, after, len(assigned.ManagerIDs), stored.Title, len(stored.ManagerIDs),
				}, nil
			},
		},
		{
			name: "form and button steps resume across the composition seam",
			seed: seedFormParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// The run view reports the form the run is parked on, and a
				// member can open that run view to reach it (run views are
				// workspace-shareable, matching the interaction audience).
				before, err := chat.WorkflowRunInteraction(ctx, "T1", "U2", "WxForm")
				if err != nil {
					return nil, err
				}
				memberRun, err := chat.GetWorkflowRun(ctx, "T1", "U2", "WxForm")
				if err != nil {
					return nil, err
				}
				fieldNames := make([]string, 0, len(before.Fields))
				for _, field := range before.Fields {
					fieldNames = append(fieldNames, field.Name)
				}
				// A member submits it and the run advances to the approval
				// button, which the same member then clicks to reach triage.
				if err := chat.SubmitWorkflowForm(ctx, "T1", "U2", "WxForm", "FxForm", `{"name":"Ada"}`); err != nil {
					return nil, err
				}
				button, err := chat.WorkflowRunInteraction(ctx, "T1", "U1", "WxForm")
				if err != nil {
					return nil, err
				}
				if err := chat.CompleteWorkflowButton(ctx, "T1", "U2", "WxForm", button.StepID); err != nil {
					return nil, err
				}
				run, err := chat.GetWorkflowRun(ctx, "T1", "U1", "WxForm")
				if err != nil {
					return nil, err
				}
				// Once advanced past both interactive steps, the run no longer
				// reports an interaction.
				after, err := chat.WorkflowRunInteraction(ctx, "T1", "U1", "WxForm")
				if err != nil {
					return nil, err
				}
				// The owner exports the run history and the submitted form
				// fields; a member is refused.
				runExport, err := chat.WorkflowRunExport(ctx, "T1", "U1", "WfForm")
				if err != nil {
					return nil, err
				}
				formExport, err := chat.WorkflowFormResponseExport(ctx, "T1", "U1", "WfForm")
				if err != nil {
					return nil, err
				}
				if _, err := chat.WorkflowRunExport(ctx, "T1", "U2", "WfForm"); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("member run export error=%v, want ErrNotFound", err)
				}
				formValues := make([]string, 0, len(formExport))
				for _, response := range formExport {
					formValues = append(formValues, response.FormTitle+":"+response.Field+"="+response.Value)
				}
				return []any{
					memberRun.ID == "WxForm",
					before.Kind, before.Title, fieldNames, before.StepID,
					button.Kind, button.Label,
					run.Status, run.CurrentStep, after.Kind,
					len(runExport), formValues,
				}, nil
			},
		},
		{
			name: "workflow branches route completed runs across the composition seam",
			seed: seedBranchParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// The severity=high run is executing its escalate step; when it
				// completes, the log step's condition on the first step's
				// outputs holds, so the run advances to it.
				if err := chat.CompleteFunction(ctx, "T1", "U1", "A1", "FxBranchHigh", `{"result":"escalated"}`, ""); err != nil {
					return nil, err
				}
				highRun, err := chat.GetWorkflowRun(ctx, "T1", "U1", "WxBranchHigh")
				if err != nil {
					return nil, err
				}
				// The severity=low run is still on its first step; completing
				// it with outputs the log condition rejects skips both
				// remaining steps and finishes the run.
				if err := chat.CompleteFunction(ctx, "T1", "U1", "A1", "FxBranchLow", `{"result":"bad"}`, ""); err != nil {
					return nil, err
				}
				lowRun, err := chat.GetWorkflowRun(ctx, "T1", "U1", "WxBranchLow")
				if err != nil {
					return nil, err
				}
				return []any{
					highRun.Status, highRun.CurrentStep,
					lowRun.Status, lowRun.CurrentStep,
				}, nil
			},
		},
		{
			name: "workflow activity summarizes runs across the composition seam",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				before, err := chat.WorkflowActivity(ctx, "T1", "U1", "WfParity")
				if err != nil {
					return nil, err
				}
				if _, err := chat.WorkflowActivity(ctx, "T1", "U2", "WfParity"); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("non-owner activity error=%v, want ErrNotFound", err)
				}
				if err := chat.CompleteFunction(ctx, "T1", "U1", "A1", "FxParity", `{"result":"done"}`, ""); err != nil {
					return nil, err
				}
				after, err := chat.WorkflowActivity(ctx, "T1", "U1", "WfParity")
				if err != nil {
					return nil, err
				}
				recentIDs := func(activity domain.WorkflowActivity) []string {
					ids := make([]string, 0, len(activity.RecentRuns))
					for _, run := range activity.RecentRuns {
						ids = append(ids, string(run.ID)+":"+string(run.Status))
					}
					return ids
				}
				return []any{
					before.Queued, before.Running, before.Completed, before.Failed, before.Cancelled, recentIDs(before),
					after.Queued, after.Running, after.Completed, after.Failed, after.Cancelled, recentIDs(after),
				}, nil
			},
		},
		{
			name: "workflow duplication and deletion survive the composition seam",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				duplicate, err := chat.DuplicateWorkflow(ctx, "T1", "U1", "WfParity")
				if err != nil {
					return nil, err
				}
				if _, err := chat.DuplicateWorkflow(ctx, "T1", "U2", "WfParity"); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("non-owner duplicate error=%v, want ErrNotFound", err)
				}
				if err := chat.DeleteWorkflow(ctx, "T1", "U1", duplicate.ID, 99); !errors.Is(err, storepkg.ErrConflict) {
					return nil, fmt.Errorf("stale delete error=%v, want ErrConflict", err)
				}
				if err := chat.DeleteWorkflow(ctx, "T1", "U2", duplicate.ID, duplicate.Version); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("non-owner delete error=%v, want ErrNotFound", err)
				}
				if err := chat.DeleteWorkflow(ctx, "T1", "U1", duplicate.ID, duplicate.Version); err != nil {
					return nil, err
				}
				if _, err := chat.GetWorkflow(ctx, "T1", "U1", duplicate.ID); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("deleted duplicate error=%v, want ErrNotFound", err)
				}
				// Deleting a published workflow cancels its running run inside
				// the same transaction, then the workflow, its trigger, and the
				// run all stop existing.
				if err := chat.DeleteWorkflow(ctx, "T1", "U1", "WfParity", 1); err != nil {
					return nil, err
				}
				if _, err := chat.GetWorkflow(ctx, "T1", "U1", "WfParity"); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("deleted workflow error=%v, want ErrNotFound", err)
				}
				if _, err := chat.GetWorkflowRun(ctx, "T1", "U1", "WxParity"); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("deleted run error=%v, want ErrNotFound", err)
				}
				return []any{
					duplicate.Title, duplicate.CallbackID, string(duplicate.Status), duplicate.Icon,
					duplicate.Version, duplicate.PublishedVersion, duplicate.Steps,
				}, nil
			},
		},
		{
			name: "workflow permissions discovery and completion survive the composition seam",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				created, err := chat.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
					AppID: "A1", CallbackID: "created-workflow", Title: "Created workflow",
					Description: "Created through the shared seam", InputSchema: `{}`,
					Steps: `[{"function_id":"triage","title":"Triage request"}]`,
				})
				if err != nil {
					return nil, err
				}
				loaded, err := chat.GetWorkflow(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				loaded.Title = "Published workflow"
				published, err := chat.UpdateWorkflow(ctx, "T1", "U1", loaded, loaded.Version, true)
				if err != nil {
					return nil, err
				}
				// Stage an edit without publishing: the owner reads the staged head,
				// while the published revision the run pinned stays live.
				staged := published
				staged.Title = "Staged workflow"
				staged.Steps = `[{"function_id":"triage","title":"Triage request"},{"function_id":"triage","title":"Triage request"}]`
				staged, err = chat.UpdateWorkflow(ctx, "T1", "U1", staged, published.Version, false)
				if err != nil {
					return nil, err
				}
				ownerView, err := chat.GetWorkflow(ctx, "T1", "U1", staged.ID)
				if err != nil {
					return nil, err
				}
				// Per-step change tracking labels the appended step as added, and
				// hides the staged draft from anyone who is not the owner.
				changes, err := chat.WorkflowStepChanges(ctx, "T1", "U1", staged.ID)
				if err != nil {
					return nil, err
				}
				if _, err := chat.WorkflowStepChanges(ctx, "T1", "U2", staged.ID); !errors.Is(err, storepkg.ErrNotFound) {
					return nil, fmt.Errorf("non-owner step changes error=%v, want ErrNotFound", err)
				}
				if err := chat.DiscardWorkflowStagedChanges(ctx, "T1", "U1", staged.ID, staged.Version); err != nil {
					return nil, err
				}
				discarded, err := chat.GetWorkflow(ctx, "T1", "U1", staged.ID)
				if err != nil {
					return nil, err
				}
				changesAfterDiscard, err := chat.WorkflowStepChanges(ctx, "T1", "U1", staged.ID)
				if err != nil {
					return nil, err
				}
				workflows, more, _, err := chat.ListWorkflows(ctx, "T1", "U1", domain.PageRequest{Limit: 100})
				if err != nil {
					return nil, err
				}
				trigger, err := chat.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
					WorkflowID: published.ID, Title: "Run published workflow", Type: "link", Config: `{}`, Enabled: true,
				}, 0)
				if err != nil {
					return nil, err
				}
				triggers, err := chat.ListWorkflowTriggers(ctx, "T1", "U1", published.ID)
				if err != nil {
					return nil, err
				}
				// The workflow is published, so its trigger can be toggled but
				// not reconfigured: a title change is rejected with ErrConflict.
				if _, err := chat.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
					ID: trigger.ID, WorkflowID: published.ID, Title: "Renamed", Type: trigger.Type,
					Config: trigger.Config, Enabled: trigger.Enabled,
				}, trigger.Version); !errors.Is(err, storepkg.ErrConflict) {
					return nil, fmt.Errorf("published trigger reconfigure error=%v, want ErrConflict", err)
				}
				run, err := chat.RunWorkflow(ctx, "T1", "U1", trigger.ID, "C1", `{"item":"request"}`, "workflow-parity")
				if err != nil {
					return nil, err
				}
				storedRun, err := chat.GetWorkflowRun(ctx, "T1", "U1", run.ID)
				if err != nil {
					return nil, err
				}
				sum := sha256.Sum256([]byte("A1\x00triage"))
				functionID := fmt.Sprintf("Fn%X", sum[:8])
				initialFunction, err := chat.GetFunctionPermission(ctx, "T1", "U1", "A1", functionID, "")
				if err != nil {
					return nil, err
				}
				setFunction, err := chat.SetFunctionPermission(ctx, "T1", "U1", "A1", functionID, "", domain.AutomationPermission{
					PermissionType: "named_entities", UserIDs: []domain.UserID{"U1", "U2"},
				})
				if err != nil {
					return nil, err
				}
				storedFunction, err := chat.GetFunctionPermission(ctx, "T1", "U1", "A1", functionID, "")
				if err != nil {
					return nil, err
				}
				initialTrigger, err := chat.GetTriggerPermission(ctx, "T1", "U1", "A1", "FtParity")
				if err != nil {
					return nil, err
				}
				setTrigger, err := chat.SetTriggerPermission(ctx, "T1", "U1", "A1", "FtParity", domain.AutomationPermission{PermissionType: "everyone"})
				if err != nil {
					return nil, err
				}
				storedTrigger, err := chat.GetTriggerPermission(ctx, "T1", "U1", "A1", "FtParity")
				if err != nil {
					return nil, err
				}
				if err := chat.SetFeaturedWorkflows(ctx, "T1", "U1", "C1", []domain.WorkflowTriggerID{"FtParity"}); err != nil {
					return nil, err
				}
				featured, err := chat.ListFeaturedWorkflows(ctx, "T1", "U1", []domain.ConversationID{"C1"})
				if err != nil {
					return nil, err
				}
				steps, err := chat.ListFunctionWorkflowSteps(ctx, "T1", "U1", "A1", functionID, "WfParity", "", "")
				if err != nil {
					return nil, err
				}
				if err := chat.CompleteFunction(ctx, "T1", "U1", "A1", "FxParity", `{"result":"done"}`, ""); err != nil {
					return nil, err
				}
				return []any{
					created.Status, loaded.Title, published.Status, published.Version,
					staged.Status, staged.Version != staged.PublishedVersion, ownerView.Title,
					ownerView.Steps,
					len(changes), changes[0].Position, string(changes[0].Change),
					discarded.Title, discarded.Version == discarded.PublishedVersion,
					len(changesAfterDiscard),
					len(workflows), more, len(triggers), triggers[0].Type,
					run.Status, storedRun.Status, storedRun.WorkflowVersion,
					initialFunction.PermissionType,
					setFunction.PermissionType, len(setFunction.UserIDs),
					storedFunction.PermissionType, len(storedFunction.UserIDs),
					initialTrigger.PermissionType,
					setTrigger.PermissionType, storedTrigger.PermissionType,
					len(featured), featured[0].Title,
					len(steps), steps[0].Title, steps[0].StepID,
				}, nil
			},
		},
		{
			name: "automatic webhook scheduled and event workflow triggers survive the composition seam",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				webhook, err := chat.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
					WorkflowID: "WfParity", Title: "Hook", Type: "webhook", Config: `{}`, Enabled: true,
				}, 0)
				if err != nil {
					return nil, err
				}
				invokeURL, err := chat.WebhookTriggerURL(ctx, "T1", "U1", webhook.ID)
				if err != nil {
					return nil, err
				}
				_, deniedErr := chat.WebhookTriggerURL(ctx, "T1", "U2", webhook.ID)
				_, wrongSecretErr := chat.RunWebhookTrigger(ctx, "T1", webhook.ID, "wrong-secret", `{"item":"hook"}`)
				secret := invokeURL[strings.LastIndex(invokeURL, "/")+1:]
				hookRun, err := chat.RunWebhookTrigger(ctx, "T1", webhook.ID, secret, `{"item":"hook"}`)
				if err != nil {
					return nil, err
				}
				autoRun, err := chat.RunAutomaticWorkflow(ctx, "T1", "FtParity", "C1", `{"item":"auto"}`, "auto-parity")
				if err != nil {
					return nil, err
				}
				autoReplay, err := chat.RunAutomaticWorkflow(ctx, "T1", "FtParity", "C1", `{"item":"auto"}`, "auto-parity")
				if err != nil {
					return nil, err
				}
				scheduled, err := chat.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
					WorkflowID: "WfParity", Title: "Hourly", Type: "scheduled",
					Config:  `{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"hourly"}}`,
					Enabled: true,
				}, 0)
				if err != nil {
					return nil, err
				}
				weekdays, err := chat.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
					WorkflowID: "WfParity", Title: "Weekdays", Type: "scheduled",
					Config:  `{"start_time":"2026-01-05T09:00:00Z","timezone":"UTC","frequency":{"type":"weekly","weekdays":["mon","wed"]}}`,
					Enabled: true,
				}, 0)
				if err != nil {
					return nil, err
				}
				if _, err := chat.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
					WorkflowID: "WfParity", Title: "On message", Type: "message",
					Config: `{"channel_ids":["C1"]}`, Enabled: true,
				}, 0); err != nil {
					return nil, err
				}
				if _, err := chat.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "C1", Text: "fire the trigger"}); err != nil {
					return nil, err
				}
				started, err := chat.DispatchWorkflowEventTriggers(ctx, "T1", 100)
				if err != nil {
					return nil, err
				}
				again, err := chat.DispatchWorkflowEventTriggers(ctx, "T1", 100)
				if err != nil {
					return nil, err
				}
				return []any{
					webhook.Type, strings.HasPrefix(invokeURL, "/services/triggers/T1/"+string(webhook.ID)+"/"),
					errors.Is(deniedErr, storepkg.ErrNotFound), errors.Is(wrongSecretErr, service.ErrWebhookTriggerSecret),
					hookRun.Status, hookRun.ActorID,
					autoRun.Status, autoRun.ID == autoReplay.ID,
					!scheduled.NextRunAt.IsZero(), weekdays.Config, weekdays.NextRunAt.Format(time.RFC3339),
					started, again,
				}, nil
			},
		},
		{
			name: "typed posting channel canvases and app workspace paging survive the composition seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				requireSeed(t, target.SeedWorkspace(domain.Workspace{ID: "T2", Name: "second workspace"}))
				requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
					AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
				}))
				requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
					AppID: "A1", WorkspaceID: "T2", Enabled: true, CreatedAt: time.Unix(1_700_000_001, 0).UTC(),
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "C1", Text: "typed post"})
				if err != nil {
					return nil, err
				}
				canvas, err := chat.CreateConversationCanvas(ctx, "T1", "U1", "C1", "Channel canvas", `{"type":"markdown","markdown":"hello"}`)
				if err != nil {
					return nil, err
				}
				loaded, err := chat.ConversationCanvas(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				byID, err := chat.Canvas(ctx, "T1", "U1", canvas.ID)
				if err != nil {
					return nil, err
				}
				canvasAccess, err := chat.CanvasAccess(ctx, "T1", "U1", canvas.ID)
				if err != nil {
					return nil, err
				}
				canvasPage, err := chat.Canvases(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				list, err := chat.CreateList(ctx, "T1", "U1", "Work", "[]", "", "", false, true)
				if err != nil {
					return nil, err
				}
				listByID, err := chat.List(ctx, "T1", "U1", list.ID)
				if err != nil {
					return nil, err
				}
				listAccess, err := chat.ListAccess(ctx, "T1", "U1", list.ID)
				if err != nil {
					return nil, err
				}
				listPage, err := chat.Lists(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				workspaces, err := chat.AuthorizedAppWorkspaces(ctx, "T1", "U1", "A1", domain.PageRequest{Limit: 1})
				if err != nil {
					return nil, err
				}
				return []any{
					message.Text, message.Conversation,
					canvas.Title, loaded.ID == canvas.ID, byID.ID == canvas.ID, canvasAccess.Access, loaded.Version, len(canvasPage.Canvases),
					listByID.Name, listAccess.Access, len(listPage.Lists),
					len(workspaces.Workspaces), workspaces.Workspaces[0].ID, workspaces.HasMore, workspaces.NextCursor != "",
				}, nil
			},
		},
		{
			name: "direct history expansion and private conversion survive the composition seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				requireSeed(t, target.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol", Email: "carol@example.com"}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				source, err := chat.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
				if err != nil {
					return nil, err
				}
				if _, err := chat.Post(ctx, "T1", "U1", source.ID, "history to retain", "", ""); err != nil {
					return nil, err
				}
				expanded, err := chat.AddPeopleToDirectConversation(ctx, "T1", "U1", source.ID, []domain.UserID{"U3"}, domain.DirectHistoryAll)
				if err != nil {
					return nil, err
				}
				converted, err := chat.ConvertGroupDirectToPrivate(ctx, "T1", "U1", expanded.ID, "Project Room")
				if err != nil {
					return nil, err
				}
				history, err := chat.History(ctx, "T1", "U1", converted.ID, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				members, err := chat.ConversationMembers(ctx, "T1", "U1", converted.ID, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				texts := make([]string, len(history.Messages))
				for index, message := range history.Messages {
					texts[index] = message.Text
				}
				return []any{expanded.IsGroupDirect, converted.IsPrivate, converted.IsDirect, converted.IsGroupDirect, converted.Name, len(members.Users), texts}, nil
			},
		},
		// The privilege-escalation class. A member calling an administrative
		// mutation must be refused with the same sentinel and the same gRPC code in
		// both compositions; a refusal that arrives remotely as codes.Unavailable
		// reads as "retry", which is how an escalation attempt would look like a
		// transient failure instead of a denial.
		{
			name:         "a member cannot promote themselves",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.SetUserRole(ctx, "T1", "U1", "U1", domain.WorkspaceRoleOwner)
			},
		},
		{
			name:         "a member cannot list the workspace directory administratively",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.AdminListUsers(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				return nil, err
			},
		},
		{
			name:         "a member cannot rename the workspace",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.AdminSetWorkspaceName(ctx, "T1", "U1", "Taken Over")
				return nil, err
			},
		},
		{
			name: "an administrator promotes a member",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.SetUserRole(ctx, "T1", "UA", "U1", domain.WorkspaceRoleAdmin); err != nil {
					return nil, err
				}
				membership, err := chat.WorkspaceMembership(ctx, "T1", "U1", "U1")
				return membership, err
			},
		},
		{
			name: "a member reads their own membership",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return chat.WorkspaceMembership(ctx, "T1", "U1", "U1")
			},
		},
		{
			name:         "a member cannot read another member's membership",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.WorkspaceMembership(ctx, "T1", "U1", "U2")
				return nil, err
			},
		},
		{
			name: "an administrator reads another member's membership",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return chat.WorkspaceMembership(ctx, "T1", "UA", "U2")
			},
		},
		{
			name:         "membership of an unknown user is absent, not forbidden",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.WorkspaceMembership(ctx, "T1", "UA", "U-missing")
				return nil, err
			},
		},
		{
			// The sign-in operations take no actor at all, so they must succeed in
			// both compositions without any member being able to reach them through
			// the HTTP surface. The projection omits the generated identifier.
			name: "external provisioning creates a user and synchronises its role",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				user, err := chat.ProvisionExternalUser(ctx, "T1", " Carol@Example.COM ", "Carol Example", domain.WorkspaceRoleMember)
				if err != nil {
					return nil, err
				}
				if err := chat.SynchronizeExternalUserRole(ctx, "T1", user.ID, domain.WorkspaceRoleAdmin); err != nil {
					return nil, err
				}
				membership, err := chat.WorkspaceMembership(ctx, "T1", user.ID, user.ID)
				if err != nil {
					return nil, err
				}
				return []any{user.Email, user.RealName, membership.Role, membership.Active}, nil
			},
		},
		{
			name:         "external provisioning rejects a duplicate address",
			wantSentinel: storepkg.ErrAlreadyExists,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.ProvisionExternalUser(ctx, "T1", "alice@example.com", "Alice Again", domain.WorkspaceRoleMember)
				return nil, err
			},
		},
		{
			name:         "external role synchronisation rejects an unknown user",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.SynchronizeExternalUserRole(ctx, "T1", "U-missing", domain.WorkspaceRoleAdmin)
			},
		},
		{
			name:         "post rejects empty text",
			wantSentinel: service.ErrInvalidMessage,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.Post(ctx, "T1", "U1", "C1", "", "", "")
				return nil, err
			},
		},
		{
			name: "missing modal lifecycle state agrees across the composition seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, currentErr := chat.CurrentModalView(ctx, "T1", "U1")
				_, submitErr := chat.SubmitView(ctx, "T1", "U1", "C1", "V-missing", `{"values":{}}`, "https://chat.example.test")
				closeErr := chat.CloseView(ctx, "T1", "U1", "C1", "V-missing", false, "https://chat.example.test")
				return []bool{
					errors.Is(currentErr, storepkg.ErrNotFound),
					errors.Is(submitErr, storepkg.ErrNotFound),
					errors.Is(closeErr, storepkg.ErrNotFound),
				}, nil
			},
		},
		{
			name:         "post to an unknown conversation",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.Post(ctx, "T1", "U1", "C-missing", "hello", "", "")
				return nil, err
			},
		},
		{
			name: "recipient-scoped ephemeral messages cross the composition seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.PostEphemeral(ctx, "T1", "U1", "C1", "U2", "plain"); err != nil {
					return nil, err
				}
				if _, err := chat.PostEphemeralWithBlocks(ctx, "T1", "U1", "C1", "U2", "", `[{"type":"divider"}]`); err != nil {
					return nil, err
				}
				if _, err := chat.PostEphemeralWithBlocksAndAttachments(ctx, "T1", "U1", "C1", "U2", "", "", `[{"text":"attachment"}]`, "A1"); err != nil {
					return nil, err
				}
				values, err := chat.ListEphemeralMessages(ctx, "T1", "U2", "C1", 10)
				if err != nil {
					return nil, err
				}
				result := make([]any, 0, len(values))
				for _, value := range values {
					result = append(result, []any{value.Text, value.Blocks, value.Attachments, value.AppID, value.ID != "", !value.CreatedAt.IsZero()})
				}
				return result, nil
			},
		},
		{
			name:         "search rejects an empty query",
			wantSentinel: service.ErrInvalidSearch,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.Search(ctx, "T1", "U1", "   ", domain.PageRequest{Limit: 10})
				return nil, err
			},
		},
		{
			name: "recent searches remain private and ordered across the composition seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.RecordSearch(ctx, "T1", "U1", "first query"); err != nil {
					return nil, err
				}
				if err := chat.RecordSearch(ctx, "T1", "U1", "second query"); err != nil {
					return nil, err
				}
				if err := chat.RecordSearch(ctx, "T1", "U1", "first query"); err != nil {
					return nil, err
				}
				values, err := chat.RecentSearches(ctx, "T1", "U1", 10)
				if err != nil {
					return nil, err
				}
				queries := make([]string, len(values))
				for index, value := range values {
					queries[index] = value.Query
				}
				return queries, nil
			},
		},
		{
			name:  "typed message and file search preserve filters and totals",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "search parity message", "", ""); err != nil {
					return nil, err
				}
				messages, err := chat.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{
					Query: "parity in:C1", Sort: domain.SearchSortTimestamp, Direction: domain.SearchDirectionDescending,
					Page: domain.PageRequest{Limit: 10},
				})
				if err != nil {
					return nil, err
				}
				if _, err := chat.UploadFile(ctx, "T1", "U1", "parity.txt", "Parity notes", "text/plain", 5, bytes.NewReader([]byte("notes"))); err != nil {
					return nil, err
				}
				files, err := chat.SearchFiles(ctx, "T1", "U1", domain.FileSearchRequest{
					Query: "parity type:text", Sort: domain.SearchSortTimestamp, Direction: domain.SearchDirectionDescending,
					Count: 10, Page: 1,
				})
				if err != nil {
					return nil, err
				}
				return []any{len(messages.Messages), messages.Total, len(files.Files), files.Total, files.Files[0].Name}, nil
			},
		},
		{
			name:         "history rejects an undecodable cursor",
			wantSentinel: domain.ErrInvalidCursor,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10, Cursor: "not-a-cursor"})
				return nil, err
			},
		},
		{
			name:         "reaction rejects an invalid name",
			wantSentinel: service.ErrInvalidReaction,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "reactable", "", "")
				if err != nil {
					return nil, err
				}
				return nil, chat.AddReaction(ctx, "T1", "U1", "C1", timestampOf(message), "  ")
			},
		},
		{
			// The duplicate reaction is store.ErrAlreadyExists, which the Slack
			// API answers with already_reacted. Collapsing it onto the code alone
			// made the split deployment answer emoji_already_exists, a code that
			// method does not define.
			name:         "duplicate reaction",
			wantSentinel: storepkg.ErrAlreadyExists,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "reactable", "", "")
				if err != nil {
					return nil, err
				}
				if err := chat.AddReaction(ctx, "T1", "U1", "C1", timestampOf(message), "thumbsup"); err != nil {
					return nil, err
				}
				return nil, chat.AddReaction(ctx, "T1", "U1", "C1", timestampOf(message), "thumbsup")
			},
		},
		{
			name:         "duplicate custom emoji",
			wantSentinel: service.ErrEmojiAlreadyExists,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminAddEmoji(ctx, "T1", "UA", "party", "https://example.test/party.png"); err != nil {
					return nil, err
				}
				return nil, chat.AdminAddEmoji(ctx, "T1", "UA", "party", "https://example.test/party.png")
			},
		},
		{
			name:         "custom emoji rejects an empty name",
			wantSentinel: service.ErrInvalidEmoji,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.AdminAddEmoji(ctx, "T1", "UA", "  ", "https://example.test/party.png")
			},
		},
		{
			name:         "presence rejects an unknown value",
			wantSentinel: service.ErrInvalidPresence,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.SetUserPresence(ctx, "T1", "U1", domain.Presence("sleepy"))
				return nil, err
			},
		},
		{
			name:         "profile rejects an oversized display name",
			wantSentinel: service.ErrInvalidProfile,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.SetUserProfile(ctx, "T1", "U1", domain.UserProfile{DisplayName: string(bytes.Repeat([]byte("a"), 81))})
				return nil, err
			},
		},
		{
			name:         "conversation rejects an empty name",
			wantSentinel: service.ErrInvalidConversation,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.CreateConversation(ctx, "T1", "U1", "   ", false)
				return nil, err
			},
		},
		{
			name:         "bookmark rejects an unsupported type",
			wantSentinel: service.ErrInvalidBookmark,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.AddBookmark(ctx, "T1", "U1", "C1", "Title", "video", "https://example.test", ":link:", "", "", "")
				return nil, err
			},
		},
		{
			// store.ErrBookmarkLimit had no case in the server mapping at all, so
			// the limit answered 503 remotely and 400 in process.
			name:         "bookmark limit",
			wantSentinel: storepkg.ErrBookmarkLimit,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				for index := 0; index < domain.MaxBookmarksPerConversation; index++ {
					if _, err := chat.AddBookmark(ctx, "T1", "U1", "C1", fmt.Sprintf("Bookmark %d", index), "link", "https://example.test", "", "", "", ""); err != nil {
						return nil, err
					}
				}
				_, err := chat.AddBookmark(ctx, "T1", "U1", "C1", "One too many", "link", "https://example.test", "", "", "", "")
				return nil, err
			},
		},
		{
			name:         "update rejects a message owned by another user",
			wantSentinel: service.ErrMessageNotOwned,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U2", "C1", "bob wrote this", "", "")
				if err != nil {
					return nil, err
				}
				_, err = chat.Update(ctx, "T1", "U1", "C1", timestampOf(message), "alice rewrites it")
				return nil, err
			},
		},
		{
			name:         "delete rejects an already deleted message",
			wantSentinel: service.ErrMessageAlreadyDeleted,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "delete me", "", "")
				if err != nil {
					return nil, err
				}
				if _, err := chat.Delete(ctx, "T1", "U1", "C1", timestampOf(message)); err != nil {
					return nil, err
				}
				_, err = chat.Delete(ctx, "T1", "U1", "C1", timestampOf(message))
				return nil, err
			},
		},
		{
			// The reason commit 1d2ac46 exists: at the limit the ticket is valid
			// and the caller must release a connection, so the HTTP layer answers
			// 429. Without the sentinel it answered 401 and sent the client off to
			// re-authenticate.
			name:         "socket mode connection limit",
			wantSentinel: storepkg.ErrSocketModeConnectionLimit,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// A ticket is inactive until it is dialled, so consumption is what
				// the limit counts.
				for index := 0; index <= domain.SocketModeConnectionLimit; index++ {
					identifier := fmt.Sprintf("socket-%d", index)
					if err := chat.CreateSocketModeConnection(ctx, domain.SocketModeConnection{
						ID: identifier, AppID: "A1", ExpiresAt: time.Now().UTC().Add(time.Minute),
					}); err != nil {
						return nil, err
					}
					if _, err := chat.ConsumeSocketModeConnection(ctx, identifier); err != nil {
						return nil, err
					}
				}
				return nil, nil
			},
		},
		{
			name:         "upload without blob storage",
			wantSentinel: service.ErrBlobUnavailable,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", 5, bytes.NewReader([]byte("hello")))
				return nil, err
			},
		},
		// The malformed-field-plus-unauthorised-caller class. The transport used
		// to reject a missing required field before the implementation ran, so a
		// caller learned that its *field* was wrong from a request the
		// implementation would have refused for a reason it is not allowed to
		// know. files.upload with an empty title answered channel_not_found in
		// the monolith and invalid_arg_name in the split deployment; with blob
		// storage down it answered file_storage_unavailable locally and
		// invalid_arg_name remotely. Every seam guard that duplicated an
		// implementation check is deleted, and these cases keep them deleted.
		{
			name:         "upload with an empty title by a caller outside the workspace",
			blobs:        true,
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.UploadFile(ctx, "T1", "U-missing", "notes.txt", "", "text/plain", 5, bytes.NewReader([]byte("hello")))
				return nil, err
			},
		},
		{
			name:         "upload with an empty title while blob storage is down",
			wantSentinel: service.ErrBlobUnavailable,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.UploadFile(ctx, "T1", "U1", "notes.txt", "", "text/plain", 5, bytes.NewReader([]byte("hello")))
				return nil, err
			},
		},
		{
			name:         "canvas edit with empty changes by a caller outside the workspace",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.EditCanvas(ctx, "T1", "U-missing", "CV1", "")
			},
		},
		{
			name:         "an unknown workspace is absent, not malformed",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.WorkspaceInfo(ctx, "", "U1")
				return nil, err
			},
		},
		// The page-bound class. protoPageRequest rejected a limit outside 1..200
		// on the seam only, so a limit of 201 returned a page in the monolith and
		// invalid_arg_name in the split deployment, and a limit of 0 failed with
		// two different classes. No parity case varied a limit, which is why 24
		// call sites carried the divergence.
		{
			name: "a page limit above the old seam bound",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "paged", "", ""); err != nil {
					return nil, err
				}
				history, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 201})
				if err != nil {
					return nil, err
				}
				conversations, err := chat.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 500})
				if err != nil {
					return nil, err
				}
				users, err := chat.Users(ctx, "T1", "U1", domain.PageRequest{Limit: 1000})
				if err != nil {
					return nil, err
				}
				return []any{len(history.Messages), history.HasMore, len(conversations.Conversations), len(users.Users)}, nil
			},
		},
		{
			// Both compositions must refuse a non-positive limit the same way.
			// The store owns the rule, so the refusal is the store's and neither
			// composition invents one of its own.
			//
			// The refusal reaches a remote caller as codes.Unavailable today,
			// because the store guard carries no sentinel; that is a defect in
			// the guard, not a contract, and this case deliberately does not
			// assert it either way. See wantAgreedFailure.
			name:              "a page limit of zero",
			wantAgreedFailure: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 0})
				return nil, err
			},
		},
		{
			name:         "unknown external identity",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.GetExternalIdentity(ctx, "T1", "oidc", "missing-subject")
				return nil, err
			},
		},
		{
			name:         "replayed provider logout token",
			wantSentinel: storepkg.ErrConflict,
			seed: func(t *testing.T, target *memory.Store) {
				t.Helper()
				seedBaseline(t, target)
				requireSeed(t, target.SeedSession(context.Background(), "session-token", domain.SessionRecord{
					WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, ExpiresAt: time.Now().UTC().Add(time.Hour),
					OIDCProvider: "oidc", OIDCSubject: "subject", OIDCSID: "provider-session",
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				expiry := time.Now().UTC().Add(time.Minute)
				if err := chat.RevokeOIDCSessions(ctx, "T1", "oidc", "", "provider-session", "logout-token", expiry); err != nil {
					return nil, err
				}
				return nil, chat.RevokeOIDCSessions(ctx, "T1", "oidc", "", "provider-session", "logout-token", expiry)
			},
		},
		{
			// The decoders used to require presence to be exactly "auto" or "away"
			// and the profile to be present, invariants the local path does not
			// have: a stored record without them was a value in process and a hard
			// error across the seam.
			name: "user record without presence or profile",
			seed: func(t *testing.T, target *memory.Store) {
				t.Helper()
				seedBaseline(t, target)
				requireSeed(t, target.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				user, err := chat.UserInfo(ctx, "T1", "U1", "U3")
				if err != nil {
					return nil, err
				}
				return []any{user.ID, user.Name, user.Presence, user.Profile}, nil
			},
		},
		{
			// decodeProtoToken and decodeProtoSession rejected a record with no
			// scopes, which the store returns.
			name: "token and session records without scopes",
			seed: func(t *testing.T, target *memory.Store) {
				t.Helper()
				seedBaseline(t, target)
				requireSeed(t, target.SeedToken(context.Background(), "api-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1"}))
				requireSeed(t, target.SeedSession(context.Background(), "session-token", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Second)}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				token, err := chat.Tokens.LookupToken(ctx, "api-token")
				if err != nil {
					return nil, err
				}
				session, err := chat.LookupSession(ctx, "session-token")
				if err != nil {
					return nil, err
				}
				return []any{token.WorkspaceID, token.UserID, len(token.Scopes), session.UserID, len(session.Scopes), session.ExpiresAt.UTC()}, nil
			},
		},
		{
			name: "post then read history",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "first", "", ""); err != nil {
					return nil, err
				}
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "second", "", "key-1"); err != nil {
					return nil, err
				}
				page, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				texts := make([]string, 0, len(page.Messages))
				for _, message := range page.Messages {
					texts = append(texts, message.Text)
				}
				return []any{texts, page.HasMore}, nil
			},
		},
		{
			name: "streaming message lifecycle preserves state and metadata across the composition seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				parent, err := chat.Post(ctx, "T1", "U2", "C1", "question", "", "")
				if err != nil {
					return nil, err
				}
				stream, err := chat.StartMessageStream(ctx, "T1", "U1", domain.MessageStreamStart{
					Conversation: "C1", ThreadTimestamp: domain.NewMessageTimestamp(parent.CreatedAt), AppID: "A1", BotID: "B1",
					RecipientTeamID: "T1", RecipientUserID: "U2", MarkdownText: "**answer",
					TaskDisplayMode: "plan", Username: "Assistant", IconURL: "https://example.com/bot.png",
				})
				if err != nil {
					return nil, err
				}
				mutation := domain.MessageStreamMutation{
					Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A1",
					Chunks: `[{"type":"markdown_text","text":"**"},{"type":"task_update","id":"task","title":"Answer","status":"complete"}]`,
				}
				stream, err = chat.AppendMessageStream(ctx, "T1", "U1", mutation)
				if err != nil {
					return nil, err
				}
				mutation.Chunks = ""
				mutation.MarkdownText = " done"
				mutation.Blocks = `[{"type":"divider"}]`
				mutation.Metadata = `{"event_type":"answer","event_payload":{"id":"R1"}}`
				stream, err = chat.StopMessageStream(ctx, "T1", "U1", mutation)
				if err != nil {
					return nil, err
				}
				var state domain.MessageStreamState
				if err := json.Unmarshal([]byte(stream.StreamState), &state); err != nil {
					return nil, err
				}
				return []any{
					stream.Conversation, stream.AuthorID, stream.AppID, stream.Text, stream.Blocks,
					stream.Metadata, state.Active, len(state.Tasks), stream.ThreadTimestamp != "",
					state.BotID, state.TaskDisplayMode, state.Username, state.IconURL,
				}, nil
			},
		},
		{
			name: "user lookups return the stored record",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				byID, err := chat.UserInfo(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				byEmail, err := chat.UserByEmail(ctx, "T1", "U1", "ALICE@EXAMPLE.COM")
				if err != nil {
					return nil, err
				}
				return []any{byID, byEmail}, nil
			},
		},
		{
			// The client used to drop the conversationID parameter and let the
			// server re-derive the target from the payload, so a call whose
			// parameter and payload disagree mutated C1 in process and C2 across
			// the seam.
			name: "conversation preferences target the parameter, not the payload",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				prefs := domain.ConversationPrefs{
					ConversationID: "C2",
					CanThread:      domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{"admin"}},
				}
				if _, err := chat.AdminSetConversationPrefs(ctx, "T1", "UA", "C1", prefs); err != nil {
					return nil, err
				}
				first, err := chat.AdminGetConversationPrefs(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				second, err := chat.AdminGetConversationPrefs(ctx, "T1", "UA", "C2")
				if err != nil {
					return nil, err
				}
				return []any{first.CanThread.Types, second.CanThread.Types}, nil
			},
		},
		{
			// The server discarded the user the module resolved and the client
			// fabricated an identifier-only record, so every other field of the
			// returned user was empty across the seam.
			name:  "user photo download returns the stored user",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// The bytes have to sniff as the declared type: internal/service
				// now reads the leading bytes and refuses a photo whose content
				// does not match what the caller declared, so a repeated
				// four-byte stand-in is no longer a PNG to either composition.
				photo := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64)...)
				if _, err := chat.SetUserPhoto(ctx, "T1", "U1", "image/png", int64(len(photo)), bytes.NewReader(photo)); err != nil {
					return nil, err
				}
				user, err := chat.UserInfo(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				// The photo token is minted per composition, so it is an input
				// here and never part of the projection.
				opened, reader, err := chat.OpenUserPhoto(ctx, "T1", "U1", path.Base(user.Profile.Image512))
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				content, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				return []any{
					opened.ID, opened.WorkspaceID, opened.Email, opened.Name, opened.RealName,
					opened.Presence, opened.Deleted, opened.Profile.DisplayName,
					opened.Profile.StatusText, opened.Profile.StatusEmoji,
					bytes.Equal(content, photo),
				}, nil
			},
		},
		{
			name:  "external upload ticket",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				upload, err := chat.CreateExternalUpload(ctx, "T1", "U1", "external.txt", "text/plain", 5, time.Minute)
				if err != nil {
					return nil, err
				}
				if err := chat.UploadExternalFile(ctx, upload.ID, 5, bytes.NewReader([]byte("bytes"))); err != nil {
					return nil, err
				}
				file, err := chat.CompleteExternalUpload(ctx, "T1", "U1", upload.ID, "External", []domain.ConversationID{"C1"}, "shared", "", "")
				if err != nil {
					return nil, err
				}
				return []any{
					upload.Name, upload.MIMEType, upload.Size, upload.Status,
					upload.UploadedAt.IsZero(), upload.CompletedAt.IsZero(),
					file.Name, file.Title, file.Size, file.SharedChannels,
				}, nil
			},
		},
		{
			name:  "file upload, download and listing",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				content := bytes.Repeat([]byte("chunked-"), 20000)
				file, err := chat.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", int64(len(content)), bytes.NewReader(content))
				if err != nil {
					return nil, err
				}
				opened, reader, err := chat.OpenFile(ctx, "T1", "U1", file.ID)
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				readBack, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				page, err := chat.Files(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				shared, err := chat.ShareFile(ctx, "T1", "U1", file.ID, "C1", "")
				if err != nil {
					return nil, err
				}
				history, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{file.Name, file.Title, file.Size, opened.Name, bytes.Equal(readBack, content), len(page.Files), page.HasMore, len(shared.Files), len(history.Messages), len(history.Messages[0].Files), history.Messages[0].Files[0].Name}, nil
			},
		},
		{
			name: "bookmark lifecycle",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				bookmark, err := chat.AddBookmark(ctx, "T1", "U1", "C1", "Docs", "link", "https://example.test/docs", ":book:", "", "", "")
				if err != nil {
					return nil, err
				}
				edited, err := chat.EditBookmark(ctx, "T1", "U1", "C1", bookmark.ID, domain.BookmarkUpdate{Title: "Handbook", SetTitle: true})
				if err != nil {
					return nil, err
				}
				listed, err := chat.Bookmarks(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				titles := make([]string, 0, len(listed))
				for _, item := range listed {
					titles = append(titles, item.Title)
				}
				return []any{bookmark.Title, bookmark.Link, bookmark.Emoji, edited.Title, titles}, nil
			},
		},
		{
			name: "presence and profile mutations",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				away, err := chat.SetUserPresence(ctx, "T1", "U1", domain.PresenceAway)
				if err != nil {
					return nil, err
				}
				updated, err := chat.SetUserProfile(ctx, "T1", "U1", domain.UserProfile{DisplayName: "alice2", StatusText: "Focusing", StatusEmoji: ":dart:"})
				if err != nil {
					return nil, err
				}
				start := time.Unix(4102448400, 0).UTC()
				scheduled, err := chat.ScheduleUserStatus(ctx, "T1", "U1", "Lunch", ":sandwich:", start, start.Add(time.Hour))
				if err != nil {
					return nil, err
				}
				edited, err := chat.UpdateScheduledUserStatus(ctx, "T1", "U1", scheduled.ID, "Deep work", ":dart:", start.Add(time.Hour), start.Add(2*time.Hour))
				if err != nil {
					return nil, err
				}
				statuses, err := chat.ScheduledUserStatuses(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				if err := chat.DeleteScheduledUserStatus(ctx, "T1", "U1", scheduled.ID); err != nil {
					return nil, err
				}
				remaining, err := chat.ScheduledUserStatuses(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				return []any{away.Presence, updated.Profile, edited.StatusText, edited.StatusEmoji, edited.StartsAt.Unix(), len(statuses), len(remaining)}, nil
			},
		},
		{
			name: "conversation membership and cursor",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "read me", "", "")
				if err != nil {
					return nil, err
				}
				cursor, err := chat.MarkRead(ctx, "T1", "U1", "C1", timestampOf(message))
				if err != nil {
					return nil, err
				}
				members, err := chat.ConversationMembers(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				isMember, err := chat.IsConversationMember(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				identifiers := make([]domain.UserID, 0, len(members.Users))
				for _, member := range members.Users {
					identifiers = append(identifiers, member.ID)
				}
				return []any{cursor.Conversation, cursor.LastRead == timestampOf(message), identifiers, members.HasMore, isMember}, nil
			},
		},
		{
			name: "durable Activity filters triage and layout",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U2", "C1", "hello <@U1>", "", "")
				if err != nil {
					return nil, err
				}
				page, err := chat.Activity(ctx, "T1", "U1", domain.ActivityQuery{
					Kinds: []domain.ActivityKind{domain.ActivityMention},
					Page:  domain.PageRequest{Limit: 10},
				})
				if err != nil {
					return nil, err
				}
				if len(page.Items) != 1 {
					return nil, fmt.Errorf("Activity returned %d items after mention", len(page.Items))
				}
				if err := chat.MutateActivity(ctx, "T1", "U1", []domain.ActivityID{page.Items[0].ID}, domain.ActivityClear); err != nil {
					return nil, err
				}
				cleared, err := chat.Activity(ctx, "T1", "U1", domain.ActivityQuery{ClearedOnly: true, Page: domain.PageRequest{Limit: 10}})
				if err != nil {
					return nil, err
				}
				defaults, err := chat.ActivityPreferences(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				preferences, err := chat.SetActivityPreferences(ctx, "T1", "U1", domain.ActivityDense)
				if err != nil {
					return nil, err
				}
				return []any{
					page.Items[0].Message.ID == message.ID, page.Items[0].Kinds,
					len(cleared.Items), !cleared.Items[0].ReadAt.IsZero(),
					defaults.Layout, preferences.Layout,
				}, nil
			},
		},
		{
			name: "reminders and scheduled messages",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				reminder, err := chat.AddReminder(ctx, "T1", "U1", "", "water the plants", time.Now().UTC().Add(time.Hour))
				if err != nil {
					return nil, err
				}
				page, err := chat.Reminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				scheduled, err := chat.ScheduleMessage(ctx, "T1", "U1", "C1", "later", time.Now().UTC().Add(time.Hour))
				if err != nil {
					return nil, err
				}
				scheduledPage, err := chat.ScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{reminder.Text, len(page.Reminders), page.HasMore, scheduled.Text, len(scheduledPage.Items)}, nil
			},
		},
		{
			name: "durable drafts and first-party scheduled management",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				now := time.Now().UTC()
				requireSeed(t, target.CreateExternalUpload(context.Background(), domain.ExternalUpload{
					ID: "draft-upload", WorkspaceID: "T1", Uploader: "U1", Name: "draft.txt", Title: "draft.txt",
					MIMEType: "text/plain", BlobKey: "T1/external/draft-upload", Size: 5,
					Status: domain.ExternalUploadUploaded, CreatedAt: now, ExpiresAt: now.Add(time.Hour), UploadedAt: now,
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.SaveDraft(ctx, "T1", "U1", "C1", "", "unfinished thought"); err != nil {
					return nil, err
				}
				draft, err := chat.SaveDraftWithAttachments(ctx, "T1", "U1", "C1", "", "unfinished thought", []domain.DraftAttachment{{UploadID: "draft-upload", Title: "Evidence"}})
				if err != nil {
					return nil, err
				}
				loaded, err := chat.Draft(ctx, "T1", "U1", "C1", "")
				if err != nil {
					return nil, err
				}
				drafts, err := chat.Drafts(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
				if err != nil {
					return nil, err
				}
				scheduled, err := chat.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{
					Channel: "C1", Text: "old text", PostAt: time.Now().UTC().Add(2 * time.Hour),
					CredentialHash:  service.InternalScheduledCredential("T1", "U1"),
					FileAttachments: draft.Attachments,
				})
				if err != nil {
					return nil, err
				}
				if err := chat.DeleteDraft(ctx, "T1", "U1", "C1", ""); err != nil {
					return nil, err
				}
				updated, err := chat.UpdateScheduledMessage(ctx, "T1", "U1", scheduled.ID, "C1", "new text", time.Now().UTC().Add(3*time.Hour))
				if err != nil {
					return nil, err
				}
				history, err := chat.ScheduledMessageHistory(ctx, "T1", "U1", true, domain.PageRequest{Limit: 10, Descending: true})
				if err != nil {
					return nil, err
				}
				firstPost, err := chat.PostScheduledMessage(ctx, "T1", scheduled.ID)
				if err != nil {
					return nil, err
				}
				retriedPost, err := chat.PostScheduledMessage(ctx, "T1", scheduled.ID)
				if err != nil {
					return nil, err
				}
				sent, err := chat.SendScheduledMessageNow(ctx, "T1", "U1", scheduled.ID)
				if err != nil {
					return nil, err
				}
				sentPage, err := chat.SentMessages(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
				if err != nil {
					return nil, err
				}
				return []any{
					draft.Text, len(draft.Attachments), loaded.Text, len(loaded.Attachments), len(drafts.Items), len(drafts.Items[0].Attachments), updated.Text, len(history.Items),
					len(history.Items[0].FileAttachments), firstPost.ID == retriedPost.ID, firstPost.ID == sent.ID, sent.Text, len(sent.Files), len(sentPage.Messages), sentPage.Messages[0].Text, len(sentPage.Messages[0].Files),
				}, nil
			},
		},
		{
			name: "first-party Later reminder lifecycle",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				created, err := chat.CreateLaterReminder(ctx, "T1", "U1", domain.LaterReminderRequest{
					Target: domain.LaterReminderPersonal, Text: "water the plants",
					DueAt: time.Now().UTC().Add(time.Hour), TimeZone: "Europe/Bucharest",
				})
				if err != nil {
					return nil, err
				}
				info, err := chat.LaterReminderInfo(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				page, err := chat.LaterReminders(ctx, "T1", "U1", domain.LaterReminderPersonal, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				updated, err := chat.UpdateLaterReminder(ctx, "T1", "U1", created.ID, domain.LaterReminderRequest{
					Target: domain.LaterReminderPersonal, Text: "water every plant",
					DueAt: time.Now().UTC().Add(2 * time.Hour), TimeZone: "Europe/Bucharest",
					Recurrence: domain.ReminderWeekly,
				})
				if err != nil {
					return nil, err
				}
				if err := chat.AcknowledgeLaterReminders(ctx, "T1", "U1"); err != nil {
					return nil, err
				}
				if err := chat.CompleteLaterReminder(ctx, "T1", "U1", created.ID); err != nil {
					return nil, err
				}
				completed, err := chat.LaterReminderInfo(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				if err := chat.DeleteLaterReminder(ctx, "T1", "U1", created.ID); err != nil {
					return nil, err
				}
				return []any{
					info.Text, info.Target, info.TimeZone, len(page.Items), page.HasMore,
					updated.Text, updated.Recurrence, !completed.CompletedAt.IsZero(),
				}, nil
			},
		},
		{
			name: "lists",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				list, err := chat.CreateList(ctx, "T1", "U1", "Groceries", "", "[]", "", false, false)
				if err != nil {
					return nil, err
				}
				item, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"milk"}]`)
				if err != nil {
					return nil, err
				}
				items, err := chat.ListItems(ctx, "T1", "U1", list.ID, domain.PageRequest{Limit: 10}, false)
				if err != nil {
					return nil, err
				}
				fields := make([]string, 0, len(items.Items))
				for _, entry := range items.Items {
					fields = append(fields, entry.Fields)
				}
				return []any{list.Name, list.Schema, item.Fields, fields, items.HasMore}, nil
			},
		},
		{
			// The user-group mutations are administrative: creating one and
			// setting its membership is workspace administration, so they run as
			// UA. A member is refused, which the case below asserts in both
			// compositions.
			name: "user groups",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				group, err := chat.CreateUserGroup(ctx, "T1", "UA", "Engineers", "engineers", "builds things")
				if err != nil {
					return nil, err
				}
				withUsers, err := chat.SetUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1", "U2"})
				if err != nil {
					return nil, err
				}
				page, err := chat.ListUserGroups(ctx, "T1", "U1", false, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{group.Name, group.Handle, group.Description, withUsers.Users, len(page.Groups)}, nil
			},
		},
		{
			name:         "a member cannot create a user group",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.CreateUserGroup(ctx, "T1", "U1", "Engineers", "engineers", "builds things")
				return nil, err
			},
		},
		{
			name: "reactions, pins and stars",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "decorate me", "", "")
				if err != nil {
					return nil, err
				}
				timestamp := timestampOf(message)
				if err := chat.AddReaction(ctx, "T1", "U1", "C1", timestamp, "thumbsup"); err != nil {
					return nil, err
				}
				if err := chat.AddPin(ctx, "T1", "U1", "C1", timestamp); err != nil {
					return nil, err
				}
				if err := chat.AddStar(ctx, "T1", "U1", "C1", timestamp); err != nil {
					return nil, err
				}
				reactions, _, reactionsMore, err := chat.Reactions(ctx, "T1", "U1", "C1", timestamp, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				pins, _, pinsMore, err := chat.Pins(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				stars, _, starsMore, err := chat.Stars(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(reactions))
				for _, reaction := range reactions {
					names = append(names, reaction.Name)
				}
				return []any{names, reactionsMore, len(pins), pinsMore, len(stars), starsMore}, nil
			},
		},
		{
			name: "private Later saved-item lifecycle",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "save me for later", "", "")
				if err != nil {
					return nil, err
				}
				saved, err := chat.SaveForLater(ctx, "T1", "U1", "C1", timestampOf(message))
				if err != nil {
					return nil, err
				}
				byMessage, err := chat.SavedItemForMessage(ctx, "T1", "U1", message.ID)
				if err != nil {
					return nil, err
				}
				byMessages, err := chat.SavedItemsForMessages(ctx, "T1", "U1", []domain.MessageID{message.ID})
				if err != nil {
					return nil, err
				}
				page, err := chat.SavedItems(ctx, "T1", "U1", domain.SavedItemInProgress, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				completed, err := chat.SetSavedItemState(ctx, "T1", "U1", saved.ID, domain.SavedItemCompleted)
				if err != nil {
					return nil, err
				}
				if err := chat.RemoveSavedItem(ctx, "T1", "U1", saved.ID); err != nil {
					return nil, err
				}
				return []any{
					saved.State, saved.SourceAvailable, saved.Message.Text,
					byMessage.ID == saved.ID, len(byMessages), len(page.Items), page.HasMore,
					completed.State,
				}, nil
			},
		},
		{
			name: "workspace and conversation listings",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				workspace, err := chat.WorkspaceInfo(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				conversations, err := chat.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(conversations.Conversations))
				for _, conversation := range conversations.Conversations {
					names = append(names, conversation.Name)
				}
				users, err := chat.Users(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				identifiers := make([]domain.UserID, 0, len(users.Users))
				for _, user := range users.Users {
					identifiers = append(identifiers, user.ID)
				}
				return []any{workspace.ID, workspace.Name, names, identifiers, conversations.HasMore, users.HasMore}, nil
			},
		},
		{
			name: "do not disturb",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				initial, err := chat.DoNotDisturbInfo(ctx, "T1", "U1", "")
				if err != nil {
					return nil, err
				}
				snoozed, err := chat.SetSnooze(ctx, "T1", "U1", 5)
				if err != nil {
					return nil, err
				}
				ended, err := chat.EndSnooze(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				reference := time.Now().UTC()
				return []any{initial.Enabled, snoozed.SnoozeEnabled(reference), ended.SnoozeEnabled(reference)}, nil
			},
		},
		{
			name: "notification preferences and thread following",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				initialWorkspace, err := chat.WorkspaceNotificationPreferences(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				workspace, err := chat.SetWorkspaceNotificationPreferences(
					ctx, "T1", "U1", domain.NotificationAll, []string{"release", "customer escalation"}, false, true,
				)
				if err != nil {
					return nil, err
				}
				initialConversation, err := chat.ConversationNotificationPreferences(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				conversation, err := chat.SetConversationNotificationPreferences(ctx, "T1", "U1", "C1", domain.NotificationMentions, true)
				if err != nil {
					return nil, err
				}
				root, err := chat.Post(ctx, "T1", "U1", "C1", "follow this root", "", "")
				if err != nil {
					return nil, err
				}
				rootTimestamp := timestampOf(root)
				followedInitially, err := chat.ThreadFollowed(ctx, "T1", "U1", "C1", rootTimestamp)
				if err != nil {
					return nil, err
				}
				if err := chat.SetThreadFollowed(ctx, "T1", "U1", "C1", rootTimestamp, false); err != nil {
					return nil, err
				}
				followedFinally, err := chat.ThreadFollowed(ctx, "T1", "U1", "C1", rootTimestamp)
				if err != nil {
					return nil, err
				}
				return []any{
					initialWorkspace.Level, initialWorkspace.ActivityChannels, initialWorkspace.ActivityReminders,
					workspace.Level, workspace.Keywords, workspace.ActivityChannels, workspace.ActivityReminders,
					initialConversation.Level, initialConversation.FollowEveryThread,
					conversation.Level, conversation.FollowEveryThread,
					followedInitially, followedFinally,
				}, nil
			},
		},
		{
			name: "application event leases survive the composition seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				now := time.Unix(1700000000, 0).UTC()
				manifest := `{"display_information":{"name":"Events"},"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":["reaction_added"]}}}`
				requireSeed(t, target.CreateApp(context.Background(), domain.App{
					ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Events", ClientID: "event-client",
					SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
					VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
					ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
				}, domain.AppManifestRevision{
					AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
				}, domain.OAuthClient{ID: "event-client", SecretHash: "client-hash", AppID: "A1"}))
				requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
					AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
				}))
				requireSeed(t, target.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "events-bot"}))
				requireSeed(t, target.SeedConversationMember("C1", "UBOT"))
				requireSeed(t, target.CreateBot(context.Background(), domain.Bot{
					ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UBOT", Name: "events-bot", UpdatedAt: now,
				}))
				requireSeed(t, target.SeedToken(context.Background(), "xoxb-events", domain.TokenRecord{
					WorkspaceID: "T1", UserID: "UBOT", AppID: "A1", BotID: "B1",
					TokenType: "bot", Scopes: []string{"reactions:read"},
				}))
				event, err := events.New("EV1", "T1", "U1", events.NewPayload("reaction.added",
					events.String("message_id", "M1"),
					events.String("channel_id", "C1"),
					events.String("ts", "1700000000.000000"),
					events.String("reaction", "wave"),
					events.String("user_id", "U1"),
				), now)
				requireSeed(t, err)
				requireSeed(t, target.AppendEvent(context.Background(), event))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				first, firstAttempt, firstReason, found, err := chat.ClaimAppEvent(ctx, "A1", "socket", "connection-1", time.Minute)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, errors.New("first event lease was not found")
				}
				authorizations, err := chat.ListAppAuthorizations(ctx, "A1", "T1")
				if err != nil {
					return nil, err
				}
				if err := chat.ReleaseAppEvent(ctx, "A1", "socket", "connection-1", first.Sequence, "connection_closed", time.Now().UTC().Add(-time.Second)); err != nil {
					return nil, err
				}
				health, err := chat.GetDeveloperAppDeliveryHealth(ctx, "T1", "U1", "A1")
				if err != nil {
					return nil, err
				}
				second, secondAttempt, secondReason, found, err := chat.ClaimAppEvent(ctx, "A1", "socket", "connection-2", time.Minute)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, errors.New("released event lease was not found")
				}
				if err := chat.AckAppEvent(ctx, "A1", "socket", "connection-2", second.Sequence); err != nil {
					return nil, err
				}
				return []any{
					first.Sequence, first.Event.ID, first.Event.WorkspaceID, first.Event.ActorID, first.Event.Topic, first.Event.Payload,
					firstAttempt, firstReason,
					health.AppID, health.Surface, health.Endpoint, health.Configured, health.Installed,
					health.AcknowledgedSequence, health.InFlightSequence, health.RetryCount, health.RetryReason,
					health.PendingEvaluation, health.NextEventTopic, health.NextEventAt,
					second.Sequence, secondAttempt, secondReason,
					authorizations,
				}, nil
			},
		},
		{
			name: "Socket Mode interaction leases and response payloads survive the composition seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				now := time.Unix(1700000000, 0).UTC()
				requireSeed(t, target.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "socket-bot"}))
				requireSeed(t, target.SeedConversationMember("C1", "UBOT"))
				requireSeed(t, target.CreateBot(context.Background(), domain.Bot{
					ID: "B1", AppID: "A1", WorkspaceID: "T1", UserID: "UBOT", Name: "socket-bot", UpdatedAt: now,
				}))
				response := domain.AppResponseURL{
					TokenHash: "response-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
					ConversationID: "C1", CreatedAt: now, ExpiresAt: time.Now().UTC().Add(time.Hour), UsesRemaining: 5,
				}
				requireSeed(t, target.CreateAppInteractionCapabilities(context.Background(), domain.AppTrigger{
					TokenHash: "trigger-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
					CreatedAt: now, ExpiresAt: time.Now().UTC().Add(time.Hour),
				}, response))
				requireSeed(t, target.CreateSocketModeInteraction(context.Background(), domain.SocketModeInteraction{
					EnvelopeID: "EN1", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
					Type: "slash_commands", Payload: `{"command":"/deploy"}`, Response: response, CreatedAt: now,
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				first, found, err := chat.ClaimSocketModeInteraction(ctx, "A1", "connection-1", time.Minute)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, errors.New("first interaction lease was not found")
				}
				if err := chat.ReleaseSocketModeInteraction(ctx, "A1", first.EnvelopeID, "connection-1", "connection_closed", time.Now().UTC().Add(-time.Second)); err != nil {
					return nil, err
				}
				second, found, err := chat.ClaimSocketModeInteraction(ctx, "A1", "connection-2", time.Minute)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, errors.New("released interaction lease was not found")
				}
				if err := chat.HandleSocketModeResponse(ctx, "A1", second.EnvelopeID, []byte(`{"text":"deployment queued"}`)); err != nil {
					return nil, err
				}
				if err := chat.AckSocketModeInteraction(ctx, "A1", second.EnvelopeID, "connection-2"); err != nil {
					return nil, err
				}
				ephemeral, err := chat.ListEphemeralMessages(ctx, "T1", "U1", "C1", 10)
				if err != nil {
					return nil, err
				}
				return []any{
					first.EnvelopeID, first.Type, first.Payload, second.RetryCount, second.RetryReason,
					len(ephemeral), ephemeral[0].Text, ephemeral[0].AppID,
				}, nil
			},
		},
		{
			name: "application shortcut discovery and dispatch survive the composition seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				now := time.Unix(1700000000, 0).UTC()
				verification, err := secretbox.Seal(bytes.Repeat([]byte("k"), 32), "app:A1:verification-token", "verification-token")
				requireSeed(t, err)
				manifest := `{"display_information":{"name":"Shortcuts"},"features":{"shortcuts":[{"name":"Create ticket","callback_id":"create_ticket","description":"Create a ticket","type":"global"},{"name":"Attach ticket","callback_id":"attach_ticket","description":"Attach this message","type":"message"}]},"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
				requireSeed(t, target.CreateApp(context.Background(), domain.App{
					ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Shortcuts", ClientID: "shortcut-client",
					SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
					VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
					ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
				}, domain.AppManifestRevision{
					AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
				}, domain.OAuthClient{ID: "shortcut-client", SecretHash: "client-hash", AppID: "A1"}))
				requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
					AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				shortcuts, err := chat.ListAppShortcuts(ctx, "T1", "U1", "global")
				if err != nil {
					return nil, err
				}
				if err := chat.DispatchAppShortcut(ctx, "T1", "U1", "C1", "A1", "create_ticket", "", "https://chat.example.test"); err != nil {
					return nil, err
				}
				interaction, found, err := chat.ClaimSocketModeInteraction(ctx, "A1", "connection-1", time.Minute)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, errors.New("shortcut interaction was not found")
				}
				if err := chat.AckSocketModeInteraction(ctx, "A1", interaction.EnvelopeID, "connection-1"); err != nil {
					return nil, err
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
					return nil, err
				}
				return []any{
					len(shortcuts), shortcuts[0].AppID, shortcuts[0].AppName, shortcuts[0].Name,
					shortcuts[0].CallbackID, shortcuts[0].Description, shortcuts[0].Type,
					interaction.Type, payload["type"], payload["callback_id"], payload["api_app_id"],
				}, nil
			},
		},
		{
			name: "hosted app datastore CRUD survives the composition seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				now := time.Unix(1700000000, 0).UTC()
				manifest := `{"display_information":{"name":"Hosted"},"oauth_config":{"scopes":{"bot":["datastore:read","datastore:write"]}},"settings":{"is_hosted":true,"function_runtime":"slack"},"datastores":{"incidents":{"primary_key":"id","attributes":{"id":{"type":"string"},"title":{"type":"string"},"priority":{"type":"integer"}}}}}`
				requireSeed(t, target.CreateApp(context.Background(), domain.App{
					ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Hosted", ClientID: "hosted-client",
					SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
					VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
					ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
				}, domain.AppManifestRevision{
					AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
				}, domain.OAuthClient{ID: "hosted-client", SecretHash: "client-hash", AppID: "A1"}))
				requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
					AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				put, err := chat.PutAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{
					`{"id":"INC-1","title":"Investigate","priority":1}`,
					`{"id":"INC-2","title":"Mitigate","priority":2}`,
				}, false)
				if err != nil {
					return nil, err
				}
				updated, err := chat.PutAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{
					`{"id":"INC-1","priority":3}`,
				}, true)
				if err != nil {
					return nil, err
				}
				got, err := chat.GetAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"INC-2", "INC-1", "missing"})
				if err != nil {
					return nil, err
				}
				query, err := chat.QueryAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", domain.AppDatastoreQuery{
					Expression: "#priority >= :minimum", ExpressionAttributes: `{"#priority":"priority"}`,
					ExpressionValues: `{":minimum":3}`, Page: domain.PageRequest{Limit: 1},
				})
				if err != nil {
					return nil, err
				}
				count, err := chat.CountAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", domain.AppDatastoreQuery{
					Expression: "contains (#title, :term)", ExpressionAttributes: `{"#title":"title"}`, ExpressionValues: `{":term":"i"}`,
				})
				if err != nil {
					return nil, err
				}
				if err := chat.DeleteAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"INC-2"}); err != nil {
					return nil, err
				}
				remaining, err := chat.GetAppDatastoreItems(ctx, "T1", "U1", "A1", "incidents", []string{"INC-1", "INC-2"})
				if err != nil {
					return nil, err
				}
				return []any{put, updated, got, query, count, remaining}, nil
			},
		},
		{
			name: "developer app manifest lifecycle",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				configuration, err := chat.IssueAppConfigurationToken(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				manifest := `{"display_information":{"name":"Example"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true,"token_rotation_enabled":true}}`
				problems, err := chat.ValidateAppManifest(ctx, configuration.Token, "", manifest)
				if err != nil {
					return nil, err
				}
				app, credentials, err := chat.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
				if err != nil {
					return nil, err
				}
				exportedApp, exported, err := chat.ExportAppManifest(ctx, configuration.Token, app.ID)
				if err != nil {
					return nil, err
				}
				apps, err := chat.ListDeveloperApps(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				detail, detailManifest, err := chat.GetDeveloperApp(ctx, "T1", "U1", app.ID)
				if err != nil {
					return nil, err
				}
				appToken, err := chat.IssueDeveloperAppToken(ctx, "T1", "U1", app.ID, []string{"connections:write"})
				if err != nil {
					return nil, err
				}
				updatedManifest := `{"display_information":{"name":"Updated"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true,"token_rotation_enabled":true}}`
				updated, err := chat.UpdateAppFromManifest(ctx, configuration.Token, app.ID, updatedManifest)
				if err != nil {
					return nil, err
				}
				oauthRequest := domain.OAuthAuthorizationRequest{ClientID: credentials.ClientID, WorkspaceID: "T1", UserID: "U1", RedirectURI: "https://example.test/oauth", BotScopes: []string{"chat:write"}, State: "state"}
				inspected, err := chat.InspectOAuthAuthorization(ctx, oauthRequest)
				if err != nil {
					return nil, err
				}
				authorized, err := chat.AuthorizeOAuth(ctx, oauthRequest)
				if err != nil {
					return nil, err
				}
				oauthToken, err := chat.OAuthV2Exchange(ctx, credentials.ClientID, credentials.ClientSecret, authorized.Code, authorized.RedirectURI, false)
				if err != nil {
					return nil, err
				}
				refreshed, err := chat.OAuthV2Refresh(ctx, credentials.ClientID, credentials.ClientSecret, oauthToken.RefreshToken)
				if err != nil {
					return nil, err
				}
				if _, err := chat.OAuthV2ExchangeToken(ctx, credentials.ClientID, credentials.ClientSecret, oauthToken.AccessToken); !errors.Is(err, service.ErrInvalidOAuth) {
					return nil, fmt.Errorf("rotating token accepted by oauth.v2.exchange: %w", err)
				}
				rotated, err := chat.RotateAppConfigurationToken(ctx, configuration.RefreshToken)
				if err != nil {
					return nil, err
				}
				if err := chat.DeleteDeveloperApp(ctx, rotated.Token, app.ID); err != nil {
					return nil, err
				}
				return []any{len(problems), app.Name, credentials.ClientID == app.ClientID, exportedApp.ID == app.ID, exported == manifest, len(apps), detail.ID == app.ID, detailManifest == manifest, strings.HasPrefix(appToken.Token, "xapp-"), appToken.AppID == app.ID, strings.Join(appToken.Scopes, " "), updated.Name, updated.ManifestVersion, inspected.AppName, authorized.Code != "", authorized.BotID != "", authorized.BotUserID != "", strings.HasPrefix(oauthToken.AccessToken, "xoxe.xoxb-"), oauthToken.RefreshToken != "", strings.HasPrefix(refreshed.AccessToken, "xoxe.xoxb-"), refreshed.RefreshToken != ""}, nil
			},
		},
	}
}

// TestEveryChatMethodHasAParityCaseOrADocumentedGap derives the coverage of this
// harness instead of trusting the case list to be complete.
//
// parityCases is hand-written, and the same change that added it made the
// converter property derived (TestEveryConverterPairIsExercisedByTheProperty)
// precisely because a hand-written list "could not see" what was missing. The
// argument applies here verbatim and was not applied: 96 transport guards were
// deleted on the claim that "the implementation owns the answer, so both
// compositions agree", which is exactly what this harness proves, and not one
// deleted guard got a case. Nine RPCs turned a malformed request from HTTP 400
// into HTTP 503 and nothing here saw it.
//
// The method set is read by reflection, and the methods a case exercises are
// read from this file's own source, so a method added to chatapi.Service fails
// here until it is either exercised or listed with the others.
func TestEveryChatMethodHasAParityCaseOrADocumentedGap(t *testing.T) {
	exercised := methodsExercisedByParityCases(t)
	serviceType := reflect.TypeOf((*chatapi.Service)(nil)).Elem()
	for index := range serviceType.NumMethod() {
		name := serviceType.Method(index).Name
		_, isGap := parityGaps[name]
		switch {
		case exercised[name] && isGap:
			t.Errorf("chatapi.Service.%s is exercised by a parity case and also listed in parityGaps; remove the entry", name)
		case !exercised[name] && !isGap:
			t.Errorf("chatapi.Service.%s crosses the seam with no parity case. Add a case to parityCases, or add %q to parityGaps and say why it cannot have one.", name, name)
		}
	}
	for name := range parityGaps {
		if _, exists := serviceType.MethodByName(name); !exists {
			t.Errorf("parityGaps names %s, which chatapi.Service no longer declares", name)
		}
	}
}

// methodsExercisedByParityCases reads the calls parityCases makes on the caller
// handle. A case that calls chat.X or chat.Tokens.X exercises X.
func methodsExercisedByParityCases(t *testing.T) map[string]bool {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "differential_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cases *ast.FuncDecl
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "parityCases" {
			cases = function
		}
	}
	if cases == nil {
		t.Fatal("differential_test.go declares no parityCases")
	}
	exercised := map[string]bool{}
	ast.Inspect(cases, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch receiver := selector.X.(type) {
		case *ast.Ident:
			if receiver.Name == "chat" {
				exercised[selector.Sel.Name] = true
			}
		case *ast.SelectorExpr:
			if identifier, ok := receiver.X.(*ast.Ident); ok && identifier.Name == "chat" {
				exercised[selector.Sel.Name] = true
			}
		}
		return true
	})
	if len(exercised) == 0 {
		t.Fatal("no parity case calls the chat handle; the scan is reading the wrong thing")
	}
	return exercised
}

// parityGaps is the backlog this harness has not covered yet.
//
// It is not an exemption list: every entry is a method whose two compositions
// have never been compared, which is the state the whole seam was in before the
// harness existed. It exists so the gap is *enumerated and derived* rather than
// invisible — the set can only shrink, a method added to chatapi.Service cannot
// join it silently, and a method whose parity case is deleted reappears here as
// a failure.
//
// Priority for closing it, from the failures the deleted guards produced: the
// methods that take a page bound, a timestamp in nanoseconds, or an identifier
// the store treats as optional.
var parityGaps = map[string]struct{}{
	"AckSocketModeResponses":             {},
	"AcknowledgeEntityCommentAction":     {},
	"AddCall":                            {},
	"AddCallParticipants":                {},
	"AddRemoteFile":                      {},
	"AddUserGroupChannels":               {},
	"AdminAddConversationAccessGroup":    {},
	"AdminAddEmojiAlias":                 {},
	"AdminAddUserGroupTeams":             {},
	"AdminApproveApp":                    {},
	"AdminApproveInviteRequest":          {},
	"AdminAssignUser":                    {},
	"AdminConnectedChannelInfo":          {},
	"AdminConversationTeams":             {},
	"AdminConvertConversationToPrivate":  {},
	"AdminCreateIncomingWebhook":         {},
	"AdminCreateUser":                    {},
	"AdminCreateWorkspace":               {},
	"AdminDeleteConversation":            {},
	"AdminDenyInviteRequest":             {},
	"AdminDisconnectSharedConversation":  {},
	"AdminInviteConversationMembers":     {},
	"AdminInviteUser":                    {},
	"AdminListApps":                      {},
	"AdminListConversationAccessGroups":  {},
	"AdminListInviteRequests":            {},
	"AdminRemoveConversationAccessGroup": {},
	"AdminRemoveEmoji":                   {},
	"AdminRenameConversation":            {},
	"AdminRenameEmoji":                   {},
	"AdminRestrictApp":                   {},
	"AdminSearchConversations":           {},
	"AdminSetConversationArchived":       {},
	"AdminSetConversationTeams":          {},
	"AdminSetIncomingWebhookEnabled":     {},
	"AdminSetWorkspaceDefaultChannels":   {},
	"AdminSetWorkspaceDescription":       {},
	"AdminSetWorkspaceDiscoverability":   {},
	"AdminSetWorkspaceIcon":              {},
	"AdminTeamUsers":                     {},
	"BotInfo":                            {},
	"ClaimSocketModeResponses":           {},
	"CompleteExternalUploads":            {},
	"CompleteReminder":                   {},
	"ConsumeRTMConnection":               {},
	"ConversationInfo":                   {},
	"CountSocketModeConnections":         {},
	"CreateAppInstallation":              {},
	"CreateCanvas":                       {},
	"CreateExternalIdentity":             {},
	"CreateRTMConnection":                {},
	"CreateSession":                      {},
	"DeleteCanvas":                       {},
	"DeleteCanvasAccess":                 {},
	"DeleteFile":                         {},
	"DeleteFileComment":                  {},
	"DeleteListAccess":                   {},
	"DeleteListItems":                    {},
	"DeleteReminder":                     {},
	"DeleteScheduledMessage":             {},
	// These three credential-aware methods share the scheduled-message RPCs
	// exercised by the legacy wrappers above. Their token/range fields have
	// focused transport tests because parityCases seeds both compositions with
	// fresh independent stores and therefore cannot compare one token's durable
	// schedule across calls.
	"DeleteScheduledMessageForCredential":     {},
	"DeleteUserPhoto":                         {},
	"DispatchBlockAction":                     {},
	"DispatchViewBlockAction":                 {},
	"DispatchSlashCommand":                    {},
	"Emojis":                                  {},
	"EndCall":                                 {},
	"EndDND":                                  {},
	"FileInfo":                                {},
	"GetAuthMethod":                           {},
	"GetCall":                                 {},
	"GetListDownload":                         {},
	"GetListItem":                             {},
	"GetSocketModeCursor":                     {},
	"HandleAppResponse":                       {},
	"IntegrationLogs":                         {},
	"InviteConversationMembers":               {},
	"JoinConversation":                        {},
	"KickConversationMember":                  {},
	"LeaveConversation":                       {},
	"ListWorkspaceApps":                       {},
	"ListAccessLogs":                          {},
	"ListAppEventsAfter":                      {},
	"ListUserEventsAfter":                     {},
	"ListAppInstallations":                    {},
	"ListEventsAfter":                         {},
	"LookupAppToken":                          {},
	"LookupCanvasSections":                    {},
	"LoadAppOptions":                          {},
	"MigrationExchange":                       {},
	"OAuthExchange":                           {},
	"OpenDialog":                              {},
	"OpenAppHome":                             {},
	"OpenIDConnectToken":                      {},
	"OpenIDConnectUserInfo":                   {},
	"OpenPublicFile":                          {},
	"OpenView":                                {},
	"AppHome":                                 {},
	"Permalink":                               {},
	"PostIncomingWebhook":                     {},
	"PostIncomingWebhookWithAttachments":      {},
	"PostWithBlocks":                          {},
	"PostWithBlocksAndAttachments":            {},
	"PresentEntityComments":                   {},
	"PresentEntityDetails":                    {},
	"PublishView":                             {},
	"PushView":                                {},
	"RecordAccess":                            {},
	"RecordSocketModeResponse":                {},
	"ReleaseSocketModeConnection":             {},
	"ReleaseSocketModeResponses":              {},
	"ReminderInfo":                            {},
	"RemoteFileInfo":                          {},
	"RemoteFiles":                             {},
	"RemoveBookmark":                          {},
	"RemoveCallParticipants":                  {},
	"RemovePin":                               {},
	"RemoveReaction":                          {},
	"RemoveRemoteFile":                        {},
	"RemoveStar":                              {},
	"RemoveUser":                              {},
	"RemoveUserGroupChannels":                 {},
	"RenameConversation":                      {},
	"RenewSocketModeConnection":               {},
	"RenewSocketModeResponses":                {},
	"Replies":                                 {},
	"RequestAppPermissions":                   {},
	"ResetUserSessions":                       {},
	"RevokeFilePublic":                        {},
	"RevokeSession":                           {},
	"RevokeToken":                             {},
	"ScheduleMessageWithBlocks":               {},
	"ScheduleMessageWithBlocksAndAttachments": {},
	"ScheduledMessagesForCredential":          {},
	"SetAuthMethod":                           {},
	"SetCanvasAccess":                         {},
	"SetConversationArchived":                 {},
	"SetConversationPurpose":                  {},
	"SetConversationTopic":                    {},
	"SetListAccess":                           {},
	"SetSocketModeCursor":                     {},
	"SetUserExpiration":                       {},
	"SetUserGroupEnabled":                     {},
	"ShareFilePublic":                         {},
	"ShareRemoteFile":                         {},
	"StartListDownload":                       {},
	"TeamBillableInfo":                        {},
	"Unfurl":                                  {},
	"UninstallApp":                            {},
	"UpdateCall":                              {},
	"UpdateList":                              {},
	"UpdateListCells":                         {},
	"UpdateListItem":                          {},
	"UpdateRemoteFile":                        {},
	"UpdateUserGroup":                         {},
	"UpdateView":                              {},
	"UpdateWithBlocks":                        {},
	"UpdateWithBlocksAndAttachments":          {},
	"UpdateMessage":                           {},
	"UserGroupChannels":                       {},
	"UserGroupUsers":                          {},
	"UserReactions":                           {},
	"WorkflowStepCompleted":                   {},
	"WorkflowStepFailed":                      {},
	"WorkflowUpdateStep":                      {},
}
