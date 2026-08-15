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
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
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
// The architecture claims the same module interfaces support direct Go calls in
// monolith mode and generated gRPC adapters in distributed mode. Nothing
// asserted it: every other transport test drives the Remote alone against
// absolute expectations, so an error class the client could not restore, a
// field a decoder dropped or a parameter the client ignored stayed invisible
// until it reached a deployment. Ten defects of that shape accumulated.
//
// A case runs one operation twice — against service.Messages directly, and
// against the same implementation behind a real gRPC server over bufconn — with
// two independently seeded stores starting in the same state. Both outcomes
// must agree on success or failure, on the classification of a failure against
// every sentinel in the error table (not only the one the case expects, which
// is what rejects a wrong-but-plausible sentinel), on the gRPC code the remote
// failure carries, and on the value the operation projects.

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

// assignExpectingFailure reduces a refusal to a string both compositions can
// agree on. The error values differ by transport — one is a sentinel, the other
// its mapped status — so comparing them directly would report a difference that
// is not one.
func (c chatCaller) assignExpectingFailure(ctx context.Context, listID domain.ListID, itemID domain.ListItemID) string {
	if _, err := c.AssignListItem(ctx, "T1", "U1", listID, itemID, "U3", time.Time{}); err != nil {
		return "refused"
	}
	return "accepted"
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

	// seedWithApp is seed for a case whose methods call an app over HTTP. The
	// harness starts one receiver per composition and hands its URL to the
	// seed, because the manifest has to name the endpoint and the manifest is
	// written by the seed. The two receivers are distinct servers answering
	// identically, so what the compositions compare is what each stored, not
	// which port it dialled.
	seedWithApp func(t *testing.T, target *memory.Store, appURL string)

	// operate runs the case against one composition and returns the value to
	// compare. A failure case returns a nil value; a success case must project
	// only values that do not depend on generated identifiers or on wall-clock
	// time, because the two compositions generate their own.
	operate func(ctx context.Context, chat chatCaller) (any, error)

	// wantSentinel is the sentinel both compositions must fail with, or nil when
	// both must succeed.
	wantSentinel error
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

// seedFileParity puts an image row in place without blob storage. The parity
// fixture has none, and a description is state on the row rather than anything
// to do with the bytes.
// seedListAssignmentParity gives a list its owner can write, a member who can
// read it, and a member who cannot.
func seedListAssignmentParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	now := time.Unix(1_700_000_000, 0).UTC()
	ctx := context.Background()
	requireSeed(t, target.CreateList(ctx, domain.List{
		ID: "Lx-assign", WorkspaceID: "T1", OwnerID: "U1", Name: "Launch tasks",
		Schema: "[]", Version: 1, CreatedAt: now, UpdatedAt: now,
	}, events.Event{ID: "ELx", WorkspaceID: "T1", Topic: "list.created", CreatedAt: now}))
	requireSeed(t, target.SetListAccess(ctx, domain.ListAccess{
		ListID: "Lx-assign", EntityType: domain.GrantUser, EntityID: "U2", Access: domain.AccessRead,
	}, events.Event{ID: "ELa", WorkspaceID: "T1", Topic: "list.access_changed", CreatedAt: now}))
	requireSeed(t, target.CreateListItem(ctx, domain.ListItem{
		ID: "Li-assign", ListID: "Lx-assign", WorkspaceID: "T1", Fields: `[{"column_id":"title","value":"ship it"}]`,
		CreatedBy: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now, Version: 1,
	}, events.Event{ID: "ELi", WorkspaceID: "T1", Topic: "list.item.created", CreatedAt: now}))
}

func seedFileParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	requireSeed(t, target.CreateFile(context.Background(), domain.File{
		ID: "Fparity-description", WorkspaceID: "T1", Uploader: "U1", Name: "diagram.png",
		Title: "Architecture", MIMEType: "image/png", BlobKey: "parity-description",
		Size: 16, CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		SharedChannels: []domain.ConversationID{"C1"},
	}, events.Event{ID: "EFparity", WorkspaceID: "T1", Topic: "file.created", CreatedAt: time.Unix(1_700_000_000, 0).UTC()}))
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
	requireSeed(t, target.SetWorkflowTrigger(context.Background(), trigger, events.Event{
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

// seedUserGroupParity gives the fixture two user groups, so a barrier has
// something real to name on both compositions.
// seedRequestedAppParity files one request that is still open and one that has
// been approved, so the cancel rule has both states to answer for.
func seedRequestedAppParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	now := time.Unix(1_700_000_400, 0).UTC()
	for _, approval := range []struct {
		app    domain.AppID
		id     domain.AppRequestID
		status domain.AppApprovalStatus
	}{
		{"A-cancel", "R-cancel", domain.AppApprovalRequested},
		{"A-approved", "R-approved", domain.AppApprovalApproved},
	} {
		requireSeed(t, target.SetAppApproval(context.Background(), "T1", approval.app, approval.id, approval.status, now, events.Event{
			ID: domain.EventID("evt_approval_" + string(approval.app)), WorkspaceID: "T1", Topic: "app.requested",
			Payload: string(approval.app), CreatedAt: now,
		}))
	}
}

// seedWebhookParity installs the app's bot into the channel a hook will post
// to. A hook posts as that bot, so a hook for a bot the channel does not hold
// would produce a URL that can never succeed.
func seedWebhookParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedWorkflowParity(t, target)
	now := time.Unix(1_700_000_600, 0).UTC()
	requireSeed(t, target.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "hook-bot"}))
	requireSeed(t, target.SeedConversationMember("C1", "UBOT"))
	requireSeed(t, target.CreateBot(context.Background(), domain.Bot{
		ID: "Bhook", WorkspaceID: "T1", AppID: "A1", UserID: "UBOT", Name: "hook-bot", UpdatedAt: now,
	}))
}

// seedViewParity installs an app that can own modals and a Home tab, and mints
// the trigger identifiers its modal methods consume.
//
// A trigger is normally minted inside a dispatch, which needs an app endpoint
// answering over HTTP. Seeding the rows directly is what lets the seam be
// compared without one: the methods under test read a trigger, they do not care
// which dispatch wrote it. Each is single use, so there is one per call.
func seedViewParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	// Approving and restricting an app are administrative decisions, so the
	// actor holds the authority for them as well as ordinary membership.
	requireSeed(t, seedWorkspaceRole(target, "T1", "U1", domain.WorkspaceRoleAdmin))
	now := time.Unix(1_700_000_700, 0).UTC()
	manifest := `{"display_information":{"name":"View parity"},"features":{"app_home":{"home_tab_enabled":true,"messages_tab_enabled":true}},"oauth_config":{"scopes":{"bot":["chat:write"]}},"settings":{"interactivity":{"is_enabled":true,"request_url":"https://apps.example.test/interactions"}}}`
	requireSeed(t, target.CreateApp(context.Background(), domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "View parity", ClientID: "view-client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "view-client", SecretHash: "client-hash", AppID: "A1"}))
	requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
	}))
	requireSeed(t, target.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "view-bot"}))
	requireSeed(t, target.CreateBot(context.Background(), domain.Bot{
		ID: "Bview", WorkspaceID: "T1", AppID: "A1", UserID: "UBOT", Name: "view-bot", UpdatedAt: now,
	}))
	// The triggers are dated against the wall clock rather than the fixture
	// instant, which is in the past: a trigger is refused once it expires, and
	// the case is about what the methods do with a live one.
	minted := time.Now().UTC()
	for _, id := range viewParityTriggers {
		requireSeed(t, target.CreateAppTrigger(context.Background(), domain.AppTrigger{
			TokenHash: domain.HashToken(id), AppID: "A1", WorkspaceID: "T1", UserID: "U1",
			CreatedAt: minted, ExpiresAt: minted.Add(time.Hour),
		}))
	}
}

// normalizeViewPayload replaces the block identifiers the product mints when a
// view is stored. They are derived from the view's own generated id, so the two
// compositions produce different ones for identical input and comparing the raw
// payload would report a disagreement that is not one. Everything else in the
// payload is still compared exactly, which is where a real divergence would be.
func normalizeViewPayload(payload string) string {
	return viewBlockIDPattern.ReplaceAllString(payload, `"block_id":"<generated>"`)
}

var viewBlockIDPattern = regexp.MustCompile(`"block_id":"[^"]*"`)

// viewParityTriggers are consumed in order by the case below. They are named
// rather than generated because both compositions must present the same ones.
var viewParityTriggers = []string{"trigger_open", "trigger_push", "trigger_dialog", "trigger_replay"}

// seedConnectParity gives the fixture a second organization with an
// administrator of its own. A Slack Connect invitation is a decision taken by
// two organizations, so one workspace cannot exercise it.
func seedConnectParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	requireSeed(t, seedWorkspaceRole(target, "T1", "U1", domain.WorkspaceRoleAdmin))
	requireSeed(t, target.SeedWorkspace(domain.Workspace{ID: "T2", Name: "second"}))
	requireSeed(t, target.SeedUser(domain.User{ID: "U2-second", WorkspaceID: "T2", Name: "guest-admin", Email: "admin@second.example"}))
	requireSeed(t, seedWorkspaceRole(target, "T2", "U2-second", domain.WorkspaceRoleAdmin))
	// Three channels, because an invitation is settled per channel and the case
	// needs one it can approve, one it can deny and one it can revoke without
	// the outcomes interfering.
	for _, channel := range []struct {
		id   domain.ConversationID
		name string
	}{{"C-accept", "connect-accept"}, {"C-deny", "connect-deny"}, {"C-revoke", "connect-revoke"}} {
		requireSeed(t, target.SeedConversation(domain.Conversation{ID: channel.id, WorkspaceID: "T1", Name: channel.name}))
		requireSeed(t, target.SeedConversationMember(channel.id, "U1"))
	}
}

// parityAppEndpoint is the app both compositions dial. It answers the shapes
// the interaction contract defines and nothing else, so a difference between
// the compositions cannot come from the app: each has its own server running
// this same handler.
// seedDispatchParity installs an app that answers over HTTP: a slash command,
// an interactivity endpoint and a message-menu options endpoint, all pointing
// at the receiver the harness started for this composition.
func seedDispatchParity(t *testing.T, target *memory.Store, appURL string) {
	t.Helper()
	seedBaseline(t, target)
	now := time.Unix(1_700_000_900, 0).UTC()
	manifest := `{"display_information":{"name":"Dispatch parity"},"features":{"slash_commands":[{"command":"/deploy","url":"` + appURL + `","description":"Deploy","usage_hint":"environment","should_escape":true}]},"oauth_config":{"scopes":{"bot":["commands","chat:write"]}},"settings":{"interactivity":{"is_enabled":true,"request_url":"` + appURL + `","message_menu_options_url":"` + appURL + `"}}}`
	// The app's credentials are sealed for real, because a dispatch opens the
	// verification token to sign the request it sends. The associated data
	// mirrors the service's own private helpers; if that format ever changes,
	// every dispatch in this case stops working, which is the tripwire.
	key := bytes.Repeat([]byte("k"), 32)
	const signingSecret, verificationToken = "dispatch-signing-secret", "dispatch-verification-token"
	signingCiphertext, err := secretbox.Seal(key, "app:A1:signing-secret", signingSecret)
	if err != nil {
		t.Fatal(err)
	}
	verificationCiphertext, err := secretbox.Seal(key, "app:A1:verification-token", verificationToken)
	if err != nil {
		t.Fatal(err)
	}
	requireSeed(t, target.CreateApp(context.Background(), domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Dispatch parity", ClientID: "dispatch-client",
		SigningSecretHash: domain.HashToken(signingSecret), SigningSecretCiphertext: signingCiphertext,
		VerificationTokenHash: domain.HashToken(verificationToken), VerificationTokenCiphertext: verificationCiphertext,
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "dispatch-client", SecretHash: "client-hash", AppID: "A1"}))
	requireSeed(t, target.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
	}))
	requireSeed(t, target.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "dispatch-bot"}))
	requireSeed(t, target.SeedConversationMember("C1", "UBOT"))
	requireSeed(t, target.CreateBot(context.Background(), domain.Bot{
		ID: "Bdispatch", WorkspaceID: "T1", AppID: "A1", UserID: "UBOT", Name: "dispatch-bot", UpdatedAt: now,
	}))
	// A response URL is normally minted by a dispatch and handed to the app
	// over HTTP, where this case cannot reach it. Seeding one with a known
	// token is what lets the response path be exercised at all.
	minted := time.Now().UTC()
	requireSeed(t, target.CreateAppInteractionCapabilities(context.Background(),
		domain.AppTrigger{
			TokenHash: domain.HashToken("trigger_dispatch"), AppID: "A1", WorkspaceID: "T1", UserID: "U1",
			CreatedAt: minted, ExpiresAt: minted.Add(time.Hour),
		},
		domain.AppResponseURL{
			TokenHash: domain.HashToken("response_dispatch"), AppID: "A1", WorkspaceID: "T1", UserID: "U1",
			ConversationID: "C1", CreatedAt: minted, ExpiresAt: minted.Add(time.Hour), UsesRemaining: 5,
		}))
}

func parityAppEndpoint(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// A slash command arrives form-encoded and an interaction arrives as a
	// JSON body, so the payload is whichever of the two this request carries.
	payload := string(body)
	if values, parseErr := url.ParseQuery(payload); parseErr == nil && values.Get("payload") != "" {
		payload = values.Get("payload")
	}
	switch {
	case strings.Contains(payload, `"type":"block_suggestion"`):
		// The typed value is echoed into the answer. An app that ignored it
		// would make a dropped value invisible, and this endpoint exists to
		// make the seam's fields observable, not to be realistic.
		var suggestion struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal([]byte(payload), &suggestion)
		_, _ = io.WriteString(w, `{"options":[{"text":{"type":"plain_text","text":"Production `+suggestion.Value+`"},"value":"prod"}]}`)
	case strings.Contains(payload, `"type":"view_submission"`):
		_, _ = io.WriteString(w, `{}`)
	default:
		_, _ = io.WriteString(w, `{"text":"acknowledged"}`)
	}
}

func seedUserGroupParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	// A private channel, because restricting a channel to a user group is a
	// thing only a private channel can have: a public one is already open to
	// the workspace, so there is nothing for the group to narrow.
	requireSeed(t, target.SeedConversation(domain.Conversation{
		ID: "C-private", WorkspaceID: "T1", Name: "restricted", Kind: domain.ConversationTypePrivate,
	}))
	requireSeed(t, target.SeedConversationMember("C-private", "U1"))
	now := time.Unix(1_700_000_300, 0).UTC()
	for _, group := range []domain.UserGroup{
		{ID: "S1", WorkspaceID: "T1", Name: "Traders", Handle: "traders", Creator: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now},
		{ID: "S2", WorkspaceID: "T1", Name: "Analysts", Handle: "analysts", Creator: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now},
	} {
		requireSeed(t, target.CreateUserGroup(context.Background(), group, events.Event{
			ID: domain.EventID("evt_group_" + string(group.ID)), WorkspaceID: "T1", Topic: "subteam.created", CreatedAt: now,
		}))
	}
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
	requireSeed(t, target.SetWorkflowTrigger(context.Background(), trigger, events.Event{
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
	requireSeed(t, target.SetWorkflowTrigger(context.Background(), trigger, events.Event{
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
	if seed == nil && testCase.seedWithApp == nil {
		seed = seedBaseline
	}
	build := func(name string) chatWorld {
		target := memory.New()
		implementation := service.Messages{Store: target, AppCredentialKey: bytes.Repeat([]byte("k"), 32)}
		if testCase.seedWithApp != nil {
			// TLS, because the manifest validator requires HTTPS endpoints: a
			// plain-HTTP receiver makes every manifest invalid and every
			// dispatch fail identically, which a differential case would report
			// as agreement.
			receiver := httptest.NewTLSServer(http.HandlerFunc(parityAppEndpoint))
			t.Cleanup(receiver.Close)
			implementation.AppHTTPClient = receiver.Client()
			testCase.seedWithApp(t, target, receiver.URL)
		}
		if seed != nil {
			seed(t, target)
		}
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
					before.Running, before.Completed, before.Failed, before.Cancelled, recentIDs(before),
					after.Running, after.Completed, after.Failed, after.Cancelled, recentIDs(after),
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
			// Handing a workflow over is a change to the list as it stands, not
			// a replacement of it, and both compositions have to agree on that
			// and on the order the list keeps.
			name: "an administrator adds and removes workflow collaborators one name at a time",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				created, err := chat.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
					AppID: "A1", CallbackID: "handover", Title: "Handover", InputSchema: `{}`,
					Steps: `[{"function_id":"triage","title":"Triage request"}]`,
				})
				if err != nil {
					return nil, err
				}
				if err := chat.AddWorkflowCollaborators(ctx, "T1", "UA", []domain.WorkflowID{created.ID}, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				// Adding somebody who already manages it changes nothing rather
				// than duplicating them.
				if err := chat.AddWorkflowCollaborators(ctx, "T1", "UA", []domain.WorkflowID{created.ID}, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				afterAdd, err := chat.GetWorkflow(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				// Somebody who is not a member cannot manage it, and a member
				// cannot hand out management at all.
				stranger := chat.AddWorkflowCollaborators(ctx, "T1", "UA", []domain.WorkflowID{created.ID}, []domain.UserID{"U-not-here"}) != nil
				member := chat.AddWorkflowCollaborators(ctx, "T1", "U2", []domain.WorkflowID{created.ID}, []domain.UserID{"U3"}) != nil
				empty := chat.AddWorkflowCollaborators(ctx, "T1", "UA", []domain.WorkflowID{created.ID}, nil) != nil
				// Removing somebody who never managed it is the state asked for.
				if err := chat.RemoveWorkflowCollaborators(ctx, "T1", "UA", []domain.WorkflowID{created.ID}, []domain.UserID{"U3"}); err != nil {
					return nil, err
				}
				if err := chat.RemoveWorkflowCollaborators(ctx, "T1", "UA", []domain.WorkflowID{created.ID}, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				afterRemove, err := chat.GetWorkflow(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				return []any{afterAdd.ManagerIDs, stranger, member, empty, afterRemove.ManagerIDs}, nil
			},
		},
		{
			// An administrator holds no app's client secret, so removing an app
			// is a different operation from the app removing itself. Both
			// compositions have to agree on who may do it and on what an app
			// that is not installed means.
			name: "an administrator uninstalls an app the workspace installed",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// A member cannot, and neither request may name nothing.
				member := chat.AdminUninstallApps(ctx, "T1", "U1", []domain.AppID{"A1"}) != nil
				empty := chat.AdminUninstallApps(ctx, "T1", "UA", nil) != nil
				// An app nobody has heard of stops the whole request.
				absent := chat.AdminUninstallApps(ctx, "T1", "UA", []domain.AppID{"A1", "A-not-here"}) != nil
				before, err := chat.ListAppInstallations(ctx, "A1")
				if err != nil {
					return nil, err
				}
				if err := chat.AdminUninstallApps(ctx, "T1", "UA", []domain.AppID{"A1"}); err != nil {
					return nil, err
				}
				after, err := chat.ListAppInstallations(ctx, "A1")
				if err != nil {
					return nil, err
				}
				// Uninstalling an app that is already gone is the state that
				// was asked for rather than a failure.
				again := chat.AdminUninstallApps(ctx, "T1", "UA", []domain.AppID{"A1"}) == nil
				enabled := func(values []domain.AppInstallation) []bool {
					states := make([]bool, 0, len(values))
					for _, value := range values {
						states = append(states, value.Enabled)
					}
					return states
				}
				return []any{member, empty, absent, enabled(before), enabled(after), again}, nil
			},
		},
		{
			// Four paged reads from the backlog, chosen by the priority the
			// backlog itself records: methods that take a page bound. A limit
			// the two compositions disagree about is the failure this harness
			// exists to catch, and none of these had ever been compared.
			name: "paged administrative reads agree on their bounds and their refusals",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				invites, err := chat.ListSharedInvites(ctx, "T1", "UA", domain.SharedInvitePending, domain.PageRequest{Limit: 5})
				if err != nil {
					return nil, err
				}
				requests, err := chat.AdminListInviteRequests(ctx, "T1", "UA", domain.InviteRequestPending, domain.PageRequest{Limit: 5})
				if err != nil {
					return nil, err
				}
				logs, more, err := chat.ListAccessLogs(ctx, "T1", "UA", time.Time{}, 5, 1)
				if err != nil {
					return nil, err
				}
				integration, err := chat.IntegrationLogs(ctx, "T1", "UA", "", "", "", "", 5, 1)
				if err != nil {
					return nil, err
				}
				// A limit neither composition may accept: the refusal has to be
				// the same class on both, not a 400 on one and a 503 on the
				// other, which is the divergence the backlog note names.
				_, badPage := chat.ListSharedInvites(ctx, "T1", "UA", domain.SharedInvitePending, domain.PageRequest{Limit: -1})
				_, badRequests := chat.AdminListInviteRequests(ctx, "T1", "UA", domain.InviteRequestPending, domain.PageRequest{Limit: -1})
				// A status nobody declares is refused rather than treated as
				// "any".
				_, badStatus := chat.ListSharedInvites(ctx, "T1", "UA", domain.SharedInviteStatus("nonsense"), domain.PageRequest{Limit: 5})
				// A member cannot read any of them.
				_, memberInvites := chat.ListSharedInvites(ctx, "T1", "U1", domain.SharedInvitePending, domain.PageRequest{Limit: 5})
				_, memberRequests := chat.AdminListInviteRequests(ctx, "T1", "U1", domain.InviteRequestPending, domain.PageRequest{Limit: 5})
				_, _, memberLogs := chat.ListAccessLogs(ctx, "T1", "U1", time.Time{}, 5, 1)
				return []any{
					len(invites.Invites), invites.HasMore, len(requests.Requests), requests.HasMore,
					len(logs), more, len(integration.Logs),
					badPage != nil, badRequests != nil, badStatus != nil,
					memberInvites != nil, memberRequests != nil, memberLogs != nil,
				}, nil
			},
		},
		{
			// An external credential's secret belongs to the store, so neither
			// composition may hand it back, and an assistant's context is the
			// member's own search rather than a wider one.
			name: "app icons, external credentials, and assistant search agree",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.SetAppIcon(ctx, "T1", "U1", "A1", "https://example.invalid/icon.png"); err != nil {
					return nil, err
				}
				notAURL := chat.SetAppIcon(ctx, "T1", "U1", "A1", "an icon")
				unknownApp := chat.SetAppIcon(ctx, "T1", "U1", "A-nobody", "https://example.invalid/icon.png")
				_, missingToken := chat.ExternalAuthToken(ctx, "T1", "A1", "Et-nobody")
				_, unnamedToken := chat.ExternalAuthToken(ctx, "T1", "A1", "")
				missingRevocation := chat.DeleteExternalAuthToken(ctx, "T1", "U1", "A1", "Et-nobody")
				connection := chat.UpdateUserAppConnection(ctx, "T1", "U1", "A1")
				unknownConnection := chat.UpdateUserAppConnection(ctx, "T1", "U1", "A-nobody")
				availability, err := chat.AssistantSearchAvailability(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				found, err := chat.AssistantSearchContext(ctx, "T1", "U1", "hello", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				_, emptyQuery := chat.AssistantSearchContext(ctx, "T1", "U1", "  ", domain.PageRequest{Limit: 10})
				return []any{
					availability.Enabled, availability.SearchableSources, len(found.Messages),
					notAURL != nil, unknownApp != nil, missingToken != nil, unnamedToken != nil,
					missingRevocation != nil, connection != nil, unknownConnection != nil, emptyQuery != nil,
				}, nil
			},
		},
		{
			// A scheduled message belongs to the credential that made it, not
			// only to the member holding it. Slack scopes listing and deletion
			// that way, so a second credential must not see or delete what the
			// first scheduled - and both compositions have to draw that line in
			// the same place.
			name: "a scheduled message stays with the credential that made it",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				at := time.Now().UTC().Add(48 * time.Hour)
				scheduled, err := chat.ScheduleMessage(ctx, "T1", "U1", "C1", "later", at)
				if err != nil {
					return nil, err
				}
				_, past := chat.ScheduleMessage(ctx, "T1", "U1", "C1", "too late", time.Unix(1_000_000, 0).UTC())
				listed, err := chat.ScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				// The credential-scoped read answers the same message when it
				// names the hash that made it, and nothing when it names another.
				mine, err := chat.ScheduledMessagesForCredential(ctx, "T1", "U1", domain.ScheduledMessageQuery{
					CredentialHash: scheduled.CredentialHash, Channel: "C1", Page: domain.PageRequest{Limit: 10},
				})
				if err != nil {
					return nil, err
				}
				theirs, err := chat.ScheduledMessagesForCredential(ctx, "T1", "U1", domain.ScheduledMessageQuery{
					CredentialHash: "another-credential", Channel: "C1", Page: domain.PageRequest{Limit: 10},
				})
				if err != nil {
					return nil, err
				}
				// Deleting through another credential is refused; through the
				// right one it works, and only once.
				strangerDeletes := chat.DeleteScheduledMessageForCredential(ctx, "T1", "U1", "another-credential", "C1", scheduled.ID)
				owned := chat.DeleteScheduledMessageForCredential(ctx, "T1", "U1", scheduled.CredentialHash, "C1", scheduled.ID)
				deletedTwice := chat.DeleteScheduledMessage(ctx, "T1", "U1", "C1", scheduled.ID)
				after, err := chat.ScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{
					len(listed.Items), len(mine.Items), len(theirs.Items), len(after.Items),
					past != nil, strangerDeletes != nil, owned != nil, deletedTwice != nil,
				}, nil
			},
		},
		{
			// An incoming webhook is a secret that posts to one channel. The
			// secret is returned once and never again, and posting with one
			// that was not issued has to be refused the same way on both
			// compositions.
			name: "an incoming webhook posts only with the secret it was issued",
			seed: seedWebhookParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// The administrator has to be in the channel the hook posts to:
				// issuing a secret that posts somewhere they cannot see would
				// be a way around the channel's own membership.
				if _, err := chat.AdminInviteConversationMembers(ctx, "T1", "UA", "C1", []domain.UserID{"UA"}); err != nil {
					return nil, err
				}
				hook, secret, err := chat.AdminCreateIncomingWebhook(ctx, "T1", "UA", "A1", "C1", "UBOT")
				if err != nil {
					return nil, err
				}
				posted, err := chat.PostIncomingWebhook(ctx, "T1", "A1", secret, "from the hook", "", "", "")
				if err != nil {
					return nil, err
				}
				withAttachments, err := chat.PostIncomingWebhookWithAttachments(ctx, "T1", "A1", secret, "with attachments", "", `[{"text":"detail"}]`, "", "")
				if err != nil {
					return nil, err
				}
				_, wrongSecret := chat.PostIncomingWebhook(ctx, "T1", "A1", "not-the-secret", "should not post", "", "", "")
				_, wrongApp := chat.PostIncomingWebhook(ctx, "T1", "A-nobody", secret, "should not post", "", "", "")

				// Disabling it stops the secret working without destroying it,
				// which is the difference between disabling and revoking.
				if err := chat.AdminSetIncomingWebhookEnabled(ctx, "T1", "UA", hook.ID, false); err != nil {
					return nil, err
				}
				_, disabled := chat.PostIncomingWebhook(ctx, "T1", "A1", secret, "while disabled", "", "", "")
				if err := chat.AdminSetIncomingWebhookEnabled(ctx, "T1", "UA", hook.ID, true); err != nil {
					return nil, err
				}
				reenabled, err := chat.PostIncomingWebhook(ctx, "T1", "A1", secret, "after enabling", "", "", "")
				if err != nil {
					return nil, err
				}
				missingHook := chat.AdminSetIncomingWebhookEnabled(ctx, "T1", "UA", "hook-nobody", false)
				member := chat.AdminSetIncomingWebhookEnabled(ctx, "T1", "U1", hook.ID, false)
				return []any{
					hook.ConversationID, secret != "", posted.Text, withAttachments.Attachments != "", reenabled.Text,
					wrongSecret != nil, wrongApp != nil, disabled != nil, missingHook != nil, member != nil,
				}, nil
			},
		},
		{
			// The administrative half of a channel. It reaches channels the
			// administrator is not in, which is exactly why each refusal has to
			// answer the same on both compositions.
			name: "an administrator renames, staffs, restricts, and deletes a channel identically",
			seed: seedUserGroupParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				renamed, err := chat.AdminRenameConversation(ctx, "T1", "UA", "C2", "admin-renamed")
				if err != nil {
					return nil, err
				}
				_, renameMissing := chat.AdminRenameConversation(ctx, "T1", "UA", "C-nobody", "nothing")
				invited, err := chat.AdminInviteConversationMembers(ctx, "T1", "UA", "C2", []domain.UserID{"U2"})
				if err != nil {
					return nil, err
				}
				_, invitedNobody := chat.AdminInviteConversationMembers(ctx, "T1", "UA", "C2", []domain.UserID{"U-nobody"})

				// Restricting a channel to a user group is a different authority
				// from membership, and it lists back in one stable order.
				if err := chat.AdminAddConversationAccessGroup(ctx, "T1", "UA", "C-private", "S1"); err != nil {
					return nil, err
				}
				if err := chat.AdminAddConversationAccessGroup(ctx, "T1", "UA", "C-private", "S2"); err != nil {
					return nil, err
				}
				groupMissing := chat.AdminAddConversationAccessGroup(ctx, "T1", "UA", "C-private", "S-nobody")
				// A public channel is already open to the workspace, so there
				// is nothing for a group to narrow.
				publicChannel := chat.AdminAddConversationAccessGroup(ctx, "T1", "UA", "C2", "S1")
				groups, err := chat.AdminListConversationAccessGroups(ctx, "T1", "UA", "C-private")
				if err != nil {
					return nil, err
				}
				sort.Slice(groups, func(left, right int) bool { return groups[left] < groups[right] })
				if err := chat.AdminRemoveConversationAccessGroup(ctx, "T1", "UA", "C-private", "S2"); err != nil {
					return nil, err
				}
				removedTwice := chat.AdminRemoveConversationAccessGroup(ctx, "T1", "UA", "C-private", "S2")
				remaining, err := chat.AdminListConversationAccessGroups(ctx, "T1", "UA", "C-private")
				if err != nil {
					return nil, err
				}

				found, err := chat.AdminSearchConversations(ctx, "T1", "UA", "admin-renamed", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				archived, err := chat.AdminSetConversationArchived(ctx, "T1", "UA", "C2", true)
				if err != nil {
					return nil, err
				}
				if err := chat.AdminDeleteConversation(ctx, "T1", "UA", "C2"); err != nil {
					return nil, err
				}
				deletedTwice := chat.AdminDeleteConversation(ctx, "T1", "UA", "C2")
				member := chat.AdminDeleteConversation(ctx, "T1", "U1", "C1")
				return []any{
					renamed.Name, invited.ID, groups, remaining, len(found.Conversations), archived.Archived,
					renameMissing != nil, invitedNobody != nil, groupMissing != nil, publicChannel != nil,
					removedTwice != nil, deletedTwice != nil, member != nil,
				}, nil
			},
		},
		{
			// What a workspace says about itself. Each setter answers the whole
			// workspace, so a setter that dropped a neighbouring field would
			// show up here rather than in whatever read it next.
			name: "workspace description, discoverability, icon, and defaults are set identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				described, err := chat.AdminSetWorkspaceDescription(ctx, "T1", "UA", "  Where the work happens  ")
				if err != nil {
					return nil, err
				}
				discoverable, err := chat.AdminSetWorkspaceDiscoverability(ctx, "T1", "UA", domain.WorkspaceDiscoverabilityInviteOnly)
				if err != nil {
					return nil, err
				}
				_, unknownDiscoverability := chat.AdminSetWorkspaceDiscoverability(ctx, "T1", "UA", "whenever")
				icon, err := chat.AdminSetWorkspaceIcon(ctx, "T1", "UA", "https://example.invalid/icon.png")
				if err != nil {
					return nil, err
				}
				_, notAnIcon := chat.AdminSetWorkspaceIcon(ctx, "T1", "UA", "an icon")
				defaults, err := chat.AdminSetWorkspaceDefaultChannels(ctx, "T1", "UA", []domain.ConversationID{"C1"})
				if err != nil {
					return nil, err
				}
				_, defaultMissing := chat.AdminSetWorkspaceDefaultChannels(ctx, "T1", "UA", []domain.ConversationID{"C-nobody"})
				member := func() bool {
					_, err := chat.AdminSetWorkspaceDescription(ctx, "T1", "U1", "not mine to set")
					return err != nil
				}()
				return []any{
					described.Description, discoverable.Discoverability, icon.IconURL, defaults.DefaultChannelIDs,
					// The description survives the three settings that followed
					// it, which is what says each setter left its neighbours be.
					defaults.Description, defaults.Discoverability, defaults.IconURL,
					unknownDiscoverability != nil, notAnIcon != nil, defaultMissing != nil, member,
				}, nil
			},
		},
		{
			// Socket Mode's durable delivery: a response is recorded, claimed
			// under a lease, renewed, and acknowledged. This is the machinery
			// most likely to disagree between compositions, because every step
			// is about who holds what and until when.
			name: "Socket Mode responses are claimed, renewed, and acknowledged identically",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				received := time.Unix(1_700_000_500, 0).UTC()
				first := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{"ok":true}`, ReceivedAt: received}
				second := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-2", Payload: `{"ok":true}`, ReceivedAt: received.Add(time.Second)}
				for _, response := range []domain.SocketModeResponse{first, second} {
					if err := chat.RecordSocketModeResponse(ctx, response); err != nil {
						return nil, err
					}
				}
				// The same envelope twice is one response, not two: an app that
				// retries must not have its answer delivered again.
				repeat := chat.RecordSocketModeResponse(ctx, first)

				claimed, err := chat.ClaimSocketModeResponses(ctx, "A1", "owner-one", 10, time.Minute)
				if err != nil {
					return nil, err
				}
				envelopes := make([]string, 0, len(claimed))
				for _, response := range claimed {
					envelopes = append(envelopes, response.EnvelopeID)
				}
				sort.Strings(envelopes)
				// A second owner finds nothing, because the first holds them.
				contended, err := chat.ClaimSocketModeResponses(ctx, "A1", "owner-two", 10, time.Minute)
				if err != nil {
					return nil, err
				}
				renewed := chat.RenewSocketModeResponses(ctx, "owner-one", claimed, 2*time.Minute)
				strangerRenews := chat.RenewSocketModeResponses(ctx, "owner-two", claimed, time.Minute)
				if err := chat.AckSocketModeResponses(ctx, "owner-one", claimed[:1]); err != nil {
					return nil, err
				}
				strangerAcks := chat.AckSocketModeResponses(ctx, "owner-two", claimed[1:])
				released := chat.ReleaseSocketModeResponses(ctx, "owner-one", claimed[1:], received)
				// What is released comes back to whoever claims next; what was
				// acknowledged does not.
				afterRelease, err := chat.ClaimSocketModeResponses(ctx, "A1", "owner-three", 10, time.Minute)
				if err != nil {
					return nil, err
				}
				returned := make([]string, 0, len(afterRelease))
				for _, response := range afterRelease {
					returned = append(returned, response.EnvelopeID)
				}
				sort.Strings(returned)

				cursor, err := chat.GetSocketModeCursor(ctx, "A1")
				if err != nil {
					return nil, err
				}
				if err := chat.SetSocketModeCursor(ctx, "A1", 42); err != nil {
					return nil, err
				}
				moved, err := chat.GetSocketModeCursor(ctx, "A1")
				if err != nil {
					return nil, err
				}
				connections, err := chat.CountSocketModeConnections(ctx, "A1")
				if err != nil {
					return nil, err
				}
				return []any{
					envelopes, len(contended), returned, cursor, moved, connections,
					repeat != nil, renewed != nil, strangerRenews != nil, strangerAcks != nil, released != nil,
				}, nil
			},
		},
		{
			// A list, its rows, and the export of them. The cells update names
			// the rows it changes, so the order it answers in is part of the
			// contract: the two compositions have to agree on it, and a map
			// iteration would make them disagree at random.
			name: "a list is updated, its cells written, and its rows exported identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				list, err := chat.CreateList(ctx, "T1", "U1", "Incidents", "[]",
					`[{"key":"title","name":"Title","type":"text"}]`, "", false, false)
				if err != nil {
					return nil, err
				}
				renamed, err := chat.UpdateList(ctx, "T1", "U1", list.ID, "Outages", "[]", false, false)
				if err != nil {
					return nil, err
				}
				_, missingList := chat.UpdateList(ctx, "T1", "U1", "F-nobody", "Nothing", "[]", false, false)

				first, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"one"}]`)
				if err != nil {
					return nil, err
				}
				second, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"two"}]`)
				if err != nil {
					return nil, err
				}
				// Named in the order second then first, so the answer proves
				// the request's order is kept rather than the store's.
				cells := `[{"row_id":"` + string(second.ID) + `","column_id":"title","text":"second"},` +
					`{"row_id":"` + string(first.ID) + `","column_id":"title","text":"first"}]`
				updated, err := chat.UpdateListCells(ctx, "T1", "U1", list.ID, cells)
				if err != nil {
					return nil, err
				}
				order := make([]string, 0, len(updated))
				for _, item := range updated {
					order = append(order, map[bool]string{true: "second", false: "first"}[item.ID == second.ID])
				}
				_, malformed := chat.UpdateListCells(ctx, "T1", "U1", list.ID, "not json")
				_, empty := chat.UpdateListCells(ctx, "T1", "U1", list.ID, "[]")

				started, err := chat.StartListDownload(ctx, "T1", "U1", list.ID, true)
				if err != nil {
					return nil, err
				}
				fetched, err := chat.GetListDownload(ctx, "T1", "U1", started.ID)
				if err != nil {
					return nil, err
				}
				_, missingJob := chat.GetListDownload(ctx, "T1", "U1", "export-nobody")

				if err := chat.DeleteListItems(ctx, "T1", "U1", list.ID, []domain.ListItemID{first.ID, second.ID}); err != nil {
					return nil, err
				}
				deletedTwice := chat.DeleteListItems(ctx, "T1", "U1", list.ID, []domain.ListItemID{first.ID})
				return []any{
					renamed.Name, order, fetched.Status, fetched.IncludeArchived, started.ListID == list.ID,
					missingList != nil, malformed != nil, empty != nil, missingJob != nil, deletedTwice != nil,
				}, nil
			},
		},
		{
			// A remote file is a pointer to something this deployment does not
			// hold. It is added, updated field by field, shared, listed and
			// removed, and every one of those is addressed either by its own
			// identifier or by the external one, which is the pair most likely
			// to drift between compositions.
			name: "a remote file is added, updated, shared, and removed identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				added, err := chat.AddRemoteFile(ctx, "T1", "U1", domain.RemoteFile{
					ExternalID: "ext-1", Title: "Design", FileType: "sketch",
					ExternalURL: "https://example.invalid/design", IndexableContents: "wireframes",
				})
				if err != nil {
					return nil, err
				}
				duplicate := func() bool {
					_, err := chat.AddRemoteFile(ctx, "T1", "U1", domain.RemoteFile{
						ExternalID: "ext-1", Title: "Again", ExternalURL: "https://example.invalid/again",
					})
					return err != nil
				}()
				// Reading by the external identifier has to find the same file
				// as reading by the one this deployment minted.
				byExternal, err := chat.RemoteFileInfo(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "ext-1"})
				if err != nil {
					return nil, err
				}
				byID, err := chat.RemoteFileInfo(ctx, "T1", "U1", domain.RemoteFileLookup{ID: added.ID})
				if err != nil {
					return nil, err
				}
				_, missing := chat.RemoteFileInfo(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "ext-nobody"})
				_, unaddressed := chat.RemoteFileInfo(ctx, "T1", "U1", domain.RemoteFileLookup{})

				// An update names which fields it sets, so a field it does not
				// name has to survive untouched.
				updated, err := chat.UpdateRemoteFile(ctx, "T1", "U1", domain.RemoteFileUpdate{
					Lookup: domain.RemoteFileLookup{ExternalID: "ext-1"}, SetTitle: true, Title: "Design v2",
				})
				if err != nil {
					return nil, err
				}
				shared, err := chat.ShareRemoteFile(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "ext-1"}, []domain.ConversationID{"C1"})
				if err != nil {
					return nil, err
				}
				_, sharedNowhere := chat.ShareRemoteFile(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "ext-1"}, []domain.ConversationID{"C-nobody"})
				listed, err := chat.RemoteFiles(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				if err := chat.RemoveRemoteFile(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "ext-1"}); err != nil {
					return nil, err
				}
				removedTwice := chat.RemoveRemoteFile(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "ext-1"})
				after, err := chat.RemoteFiles(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{
					added.Title, byExternal.ID == added.ID, byID.ExternalID,
					updated.Title, updated.FileType, updated.ExternalURL,
					shared.SharedChannels, len(listed.Files), len(after.Files),
					duplicate, missing != nil, unaddressed != nil, sharedNowhere != nil, removedTwice != nil,
				}, nil
			},
		},
		{
			// A hosted file: shared to a public link, revoked again, and
			// deleted. A public link that outlived its revocation would be the
			// worst kind of disagreement between the two compositions.
			name: "a hosted file is published, revoked, and deleted identically",
			seed: seedFileParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				published, err := chat.ShareFilePublic(ctx, "T1", "U1", "Fparity-description")
				if err != nil {
					return nil, err
				}
				publishedTwice, err := chat.ShareFilePublic(ctx, "T1", "U1", "Fparity-description")
				if err != nil {
					return nil, err
				}
				revoked, err := chat.RevokeFilePublic(ctx, "T1", "U1", "Fparity-description")
				if err != nil {
					return nil, err
				}
				revokedTwice, err := chat.RevokeFilePublic(ctx, "T1", "U1", "Fparity-description")
				if err != nil {
					return nil, err
				}
				_, missing := chat.ShareFilePublic(ctx, "T1", "U1", "F-nobody")
				comment := chat.DeleteFileComment(ctx, "T1", "U1", "Fparity-description", "FC-nobody")
				commentTwice := chat.DeleteFileComment(ctx, "T1", "U1", "Fparity-description", "FC-nobody")
				deleted := chat.DeleteFile(ctx, "T1", "U1", "Fparity-description")
				deletedTwice := chat.DeleteFile(ctx, "T1", "U1", "Fparity-description")
				return []any{
					published.PublicToken != "", publishedTwice.PublicToken == published.PublicToken,
					revoked.PublicToken == "", revokedTwice.PublicToken == "",
					missing != nil, comment != nil, commentTwice != nil, deleted != nil, deletedTwice != nil,
				}, nil
			},
		},
		{
			// A channel's own lifecycle: renaming it, saying what it is for,
			// who is in it, and archiving it. Archiving is the interesting one,
			// because an archived channel refuses the writes an open one takes.
			name: "a channel is renamed, described, staffed, and archived identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				renamed, err := chat.RenameConversation(ctx, "T1", "U1", "C1", " New Room ")
				if err != nil {
					return nil, err
				}
				topic, err := chat.SetConversationTopic(ctx, "T1", "U1", "C1", "what we are doing")
				if err != nil {
					return nil, err
				}
				purpose, err := chat.SetConversationPurpose(ctx, "T1", "U1", "C1", "why we are here")
				if err != nil {
					return nil, err
				}
				// C2 has one member, so joining, inviting and kicking have
				// somewhere to happen that does not disturb C1.
				joined, err := chat.JoinConversation(ctx, "T1", "U2", "C2")
				if err != nil {
					return nil, err
				}
				invited, err := chat.InviteConversationMembers(ctx, "T1", "U1", "C2", []domain.UserID{"UA"})
				if err != nil {
					return nil, err
				}
				_, invitedNobody := chat.InviteConversationMembers(ctx, "T1", "U1", "C2", []domain.UserID{"U-nobody"})
				kicked := chat.KickConversationMember(ctx, "T1", "U1", "C2", "UA")
				kickedTwice := chat.KickConversationMember(ctx, "T1", "U1", "C2", "UA")
				left := chat.LeaveConversation(ctx, "T1", "U2", "C2")
				leftTwice := chat.LeaveConversation(ctx, "T1", "U2", "C2")

				archived, err := chat.SetConversationArchived(ctx, "T1", "U1", "C1", true)
				if err != nil {
					return nil, err
				}
				// An archived channel takes no more writes, which is the whole
				// point of archiving rather than hiding.
				_, renameArchived := chat.RenameConversation(ctx, "T1", "U1", "C1", "later")
				_, archivedTwice := chat.SetConversationArchived(ctx, "T1", "U1", "C1", true)
				reopened, err := chat.SetConversationArchived(ctx, "T1", "U1", "C1", false)
				if err != nil {
					return nil, err
				}
				return []any{
					renamed.Name, topic.Topic, purpose.Purpose,
					joined.ID, invited.ID, archived.Archived, reopened.Archived,
					invitedNobody != nil, kicked != nil, kickedTwice != nil, left != nil, leftTwice != nil,
					renameArchived != nil, archivedTwice != nil,
				}, nil
			},
		},
		{
			// Custom emoji: add, alias, rename, remove, and what the workspace
			// lists afterwards. An alias points at another emoji, so removing
			// the target and renaming it both have to answer the same way on
			// each composition.
			name: "custom emoji are added, aliased, renamed, and removed identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminAddEmoji(ctx, "T1", "UA", "party", "https://example.invalid/party.png"); err != nil {
					return nil, err
				}
				duplicate := chat.AdminAddEmoji(ctx, "T1", "UA", "party", "https://example.invalid/other.png")
				if err := chat.AdminAddEmojiAlias(ctx, "T1", "UA", "celebrate", "party"); err != nil {
					return nil, err
				}
				aliasOfNothing := chat.AdminAddEmojiAlias(ctx, "T1", "UA", "phantomcat", "nobody")
				if err := chat.AdminRenameEmoji(ctx, "T1", "UA", "party", "partyparrot"); err != nil {
					return nil, err
				}
				renameMissing := chat.AdminRenameEmoji(ctx, "T1", "UA", "nobody", "somebody")
				member := chat.AdminAddEmoji(ctx, "T1", "U1", "members-only", "https://example.invalid/m.png")
				listed, err := chat.Emojis(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(listed))
				for _, emoji := range listed {
					names = append(names, emoji.Name+"="+emoji.AliasFor)
				}
				sort.Strings(names)
				if err := chat.AdminRemoveEmoji(ctx, "T1", "UA", "partyparrot"); err != nil {
					return nil, err
				}
				removeMissing := chat.AdminRemoveEmoji(ctx, "T1", "UA", "partyparrot")
				after, err := chat.Emojis(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				return []any{names, len(after), duplicate != nil, aliasOfNothing != nil,
					renameMissing != nil, member != nil, removeMissing != nil}, nil
			},
		},
		{
			// The three things a member attaches to one message: a reaction, a
			// pin, and a star. Each is added, listed, and taken away, and each
			// refuses the same second attempt.
			name: "reactions, pins, and stars attach to one message identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "something to mark", "", "")
				if err != nil {
					return nil, err
				}
				at := timestampOf(message)
				if err := chat.AddReaction(ctx, "T1", "U1", "C1", at, "eyes"); err != nil {
					return nil, err
				}
				twice := chat.AddReaction(ctx, "T1", "U1", "C1", at, "eyes")
				if err := chat.AddPin(ctx, "T1", "U1", "C1", at); err != nil {
					return nil, err
				}
				pinnedTwice := chat.AddPin(ctx, "T1", "U1", "C1", at)
				if err := chat.AddStar(ctx, "T1", "U1", "C1", at); err != nil {
					return nil, err
				}
				starredTwice := chat.AddStar(ctx, "T1", "U1", "C1", at)

				reactions, err := chat.UserReactions(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				marked := make([]string, 0, len(reactions.Items))
				for _, item := range reactions.Items {
					marked = append(marked, item.Reaction.Name)
				}

				if err := chat.RemoveReaction(ctx, "T1", "U1", "C1", at, "eyes"); err != nil {
					return nil, err
				}
				if err := chat.RemovePin(ctx, "T1", "U1", "C1", at); err != nil {
					return nil, err
				}
				if err := chat.RemoveStar(ctx, "T1", "U1", "C1", at); err != nil {
					return nil, err
				}
				// Taking away what is no longer there is the same answer on
				// both compositions, whatever that answer is.
				reactionGone := chat.RemoveReaction(ctx, "T1", "U1", "C1", at, "eyes")
				pinGone := chat.RemovePin(ctx, "T1", "U1", "C1", at)
				starGone := chat.RemoveStar(ctx, "T1", "U1", "C1", at)
				left, err := chat.UserReactions(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{marked, len(left.Items), twice != nil, pinnedTwice != nil, starredTwice != nil,
					reactionGone != nil, pinGone != nil, starGone != nil}, nil
			},
		},
		{
			// A user group's whole shape: renaming it, the channels it reaches,
			// the workspaces it spans, and disabling it without deleting it.
			name: "a user group is updated, scoped, and disabled identically",
			seed: seedUserGroupParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				updated, err := chat.UpdateUserGroup(ctx, "T1", "UA", "S1", "Traders desk", "traders-desk", "Front office")
				if err != nil {
					return nil, err
				}
				if err := chat.AddUserGroupChannels(ctx, "T1", "UA", "S1", []domain.ConversationID{"C1", "C2"}); err != nil {
					return nil, err
				}
				missingChannel := chat.AddUserGroupChannels(ctx, "T1", "UA", "S1", []domain.ConversationID{"C-nobody"})
				channels, err := chat.UserGroupChannels(ctx, "T1", "UA", "S1")
				if err != nil {
					return nil, err
				}
				sort.Slice(channels, func(left, right int) bool { return channels[left] < channels[right] })
				if err := chat.RemoveUserGroupChannels(ctx, "T1", "UA", "S1", []domain.ConversationID{"C2"}); err != nil {
					return nil, err
				}
				remaining, err := chat.UserGroupChannels(ctx, "T1", "UA", "S1")
				if err != nil {
					return nil, err
				}
				teams := chat.AdminAddUserGroupTeams(ctx, "T1", "UA", "S1", []domain.WorkspaceID{"T1"})
				members, err := chat.UserGroupUsers(ctx, "T1", "UA", "S1")
				if err != nil {
					return nil, err
				}
				disabled, err := chat.SetUserGroupEnabled(ctx, "T1", "UA", "S1", false)
				if err != nil {
					return nil, err
				}
				enabled, err := chat.SetUserGroupEnabled(ctx, "T1", "UA", "S1", true)
				if err != nil {
					return nil, err
				}
				_, missingGroup := chat.SetUserGroupEnabled(ctx, "T1", "UA", "S-nobody", false)
				return []any{
					updated.Name, updated.Handle, updated.Description,
					channels, remaining, len(members),
					disabled.Enabled, enabled.Enabled,
					missingChannel != nil, teams != nil, missingGroup != nil,
				}, nil
			},
		},
		{
			// A reminder is read, completed, and deleted. Completing one that
			// is already complete and deleting one that is gone are the two
			// answers most likely to drift between compositions.
			name: "a reminder is read, completed, and deleted identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				due := time.Unix(1_900_000_000, 0).UTC()
				created, err := chat.AddReminder(ctx, "T1", "U1", "U1", "water the plants", due)
				if err != nil {
					return nil, err
				}
				read, err := chat.ReminderInfo(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				_, somebodyElse := chat.ReminderInfo(ctx, "T1", "U2", created.ID)
				_, missing := chat.ReminderInfo(ctx, "T1", "U1", "Rm-nobody")
				if err := chat.CompleteReminder(ctx, "T1", "U1", created.ID); err != nil {
					return nil, err
				}
				completedTwice := chat.CompleteReminder(ctx, "T1", "U1", created.ID)
				listed, err := chat.Reminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				if err := chat.DeleteReminder(ctx, "T1", "U1", created.ID); err != nil {
					return nil, err
				}
				deletedTwice := chat.DeleteReminder(ctx, "T1", "U1", created.ID)
				after, err := chat.Reminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{read.Text, read.Time.Equal(due), len(listed.Reminders), len(after.Reminders),
					somebodyElse != nil, missing != nil, completedTwice != nil, deletedTwice != nil}, nil
			},
		},
		{
			// The whole huddle lifecycle, including the signalling that carries
			// WebRTC between two browsers. The refusals matter most: a signal
			// is how one member reaches another's machine, so both compositions
			// must refuse the same senders and recipients.
			name: "a huddle starts, signals, invites, empties, and ends identically",
			// The baseline plus U3, a C1 member who is the huddle invitation's
			// legitimate target. Setting seed replaces the baseline rather than
			// adding to it, so it is called explicitly.
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				requireSeed(t, target.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol", Email: "carol@example.com"}))
				requireSeed(t, target.SeedConversationMember("C1", "U3"))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				started, err := chat.StartHuddle(ctx, "T1", "U1", "C1", "Standup")
				if err != nil {
					return nil, err
				}
				// Nobody else is in it yet, so there is nobody to signal.
				alone := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalOffer, "v=0")
				joined, err := chat.JoinHuddle(ctx, "T1", "U2", "C1")
				if err != nil {
					return nil, err
				}
				offered := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalOffer, "v=0\r\no=- 0 0 IN IP4 0.0.0.0")
				answered := chat.SendCallSignal(ctx, "T1", "U2", started.ID, "U1", domain.CallSignalAnswer, "v=0")
				candidate := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalCandidate, "candidate:0 1 UDP 1 127.0.0.1 1 typ host")
				// Every refusal a signal has to make.
				self := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U1", domain.CallSignalOffer, "v=0")
				outsider := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "UA", domain.CallSignalOffer, "v=0")
				stranger := chat.SendCallSignal(ctx, "T1", "UA", started.ID, "U2", domain.CallSignalOffer, "v=0")
				unknownKind := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", "hangup", "v=0")
				empty := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalOffer, "")
				oversized := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalOffer, strings.Repeat("s", domain.CallSignalCeiling+1))
				missingCall := chat.SendCallSignal(ctx, "T1", "U1", "call-nobody", "U2", domain.CallSignalOffer, "v=0")

				// Inviting a specific member into the live huddle. U1 and U2 are
				// in it; U3 is a workspace member who is not, and inviting them
				// is the one that must succeed. Every refusal beside it: self,
				// somebody already in the huddle, an outsider to the
				// conversation (UA is a workspace admin but not a C1 member),
				// and an invite from a non-participant.
				inviteMember := chat.InviteToHuddle(ctx, "T1", "U1", "U3", "C1")
				inviteSelf := chat.InviteToHuddle(ctx, "T1", "U1", "U1", "C1")
				inviteJoined := chat.InviteToHuddle(ctx, "T1", "U1", "U2", "C1")
				inviteOutsider := chat.InviteToHuddle(ctx, "T1", "U1", "UA", "C1")
				inviteFromNonParticipant := chat.InviteToHuddle(ctx, "T1", "UA", "U3", "C1")

				active, err := chat.ActiveHuddle(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				if _, err := chat.LeaveHuddle(ctx, "T1", "U2", "C1"); err != nil {
					return nil, err
				}
				// U2 has gone, so U1 can no longer reach them through the call.
				afterLeaving := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalOffer, "v=0")
				ended, err := chat.EndHuddle(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				afterEnding := chat.SendCallSignal(ctx, "T1", "U1", started.ID, "U2", domain.CallSignalOffer, "v=0")
				_, gone := chat.ActiveHuddle(ctx, "T1", "U1", "C1")
				return []any{
					started.Title, len(started.Participants), len(joined.Participants), len(active.Participants),
					ended.EndedAt.IsZero(), gone != nil,
					alone != nil, offered != nil, answered != nil, candidate != nil,
					self != nil, outsider != nil, stranger != nil, unknownKind != nil,
					empty != nil, oversized != nil, missingCall != nil,
					afterLeaving != nil, afterEnding != nil,
					inviteMember == nil, inviteSelf != nil, inviteJoined != nil,
					inviteOutsider != nil, inviteFromNonParticipant != nil,
				}, nil
			},
		},
		{
			// The step responses report is what the export acknowledges, so
			// both compositions must collect the same answers and refuse the
			// same callers.
			name: "workflow step responses are collected identically",
			seed: seedFormParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				collected, err := chat.WorkflowStepResponses(ctx, "T1", "U1", "WfForm", "intake")
				if err != nil {
					return nil, err
				}
				described := make([]string, 0, len(collected))
				for _, response := range collected {
					described = append(described, string(response.ActorID)+"/"+string(response.Status))
				}
				_, unnamedStep := chat.WorkflowStepResponses(ctx, "T1", "U1", "WfForm", "")
				_, missingWorkflow := chat.WorkflowStepResponses(ctx, "T1", "U1", "Wf-nobody", "intake")
				_, stranger := chat.WorkflowStepResponses(ctx, "T1", "U2", "WfForm", "intake")
				return []any{described, unnamedStep != nil, missingWorkflow != nil, stranger != nil}, nil
			},
		},
		{
			// An empty allow list is the state a workspace starts in, not a
			// missing one, and an address without a reason is refused. Both
			// compositions must answer the same on each.
			name: "audit allow list, billing plan, and export requests agree",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				empty, err := chat.AdminAnomalyAllowList(ctx, "T1", "UA")
				if err != nil {
					return nil, err
				}
				written, err := chat.AdminSetAnomalyAllowList(ctx, "T1", "UA", []string{"198.51.100.7", "203.0.113.0/24"}, []string{"office"})
				if err != nil {
					return nil, err
				}
				_, unexplained := chat.AdminSetAnomalyAllowList(ctx, "T1", "UA", []string{"198.51.100.7"}, nil)
				_, notAnAddress := chat.AdminSetAnomalyAllowList(ctx, "T1", "UA", []string{"the office"}, []string{"office"})
				read, err := chat.AdminAnomalyAllowList(ctx, "T1", "UA")
				if err != nil {
					return nil, err
				}
				plan, err := chat.TeamBillingInfo(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				exported := chat.AdminRequestExport(ctx, "T1", "UA", "unsupported_versions", map[string]int64{"date_end_of_support": 1700000000})
				strangerExport := chat.AdminRequestExport(ctx, "T1", "U-nobody", "unsupported_versions", nil)
				stepExport := chat.RequestWorkflowStepResponsesExport(ctx, "T1", "U1", "Wf1", "intake")
				missingWorkflow := chat.RequestWorkflowStepResponsesExport(ctx, "T1", "U1", "Wf-nobody", "intake")
				return []any{
					empty.IPAddresses, empty.Reasons, written.IPAddresses, written.Reasons,
					read.IPAddresses, read.Reasons, plan,
					unexplained != nil, notAnAddress != nil, exported != nil, strangerExport != nil,
					stepExport != nil, missingWorkflow != nil,
				}, nil
			},
		},
		{
			// Analytics are computed from the day's own messages, so both
			// compositions must count the same rows for the same day and refuse
			// the same kinds.
			name: "analytics count the same rows on both compositions",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				day := time.Unix(1_700_000_000, 0).UTC().Truncate(24 * time.Hour)
				members, err := chat.AdminAnalytics(ctx, "T1", "UA", domain.AnalyticsMember, day)
				if err != nil {
					return nil, err
				}
				channels, err := chat.AdminAnalytics(ctx, "T1", "UA", domain.AnalyticsPublicChannel, day)
				if err != nil {
					return nil, err
				}
				everything, err := chat.AdminAnalytics(ctx, "T1", "UA", domain.AnalyticsConversations, day)
				if err != nil {
					return nil, err
				}
				_, badKind := chat.AdminAnalytics(ctx, "T1", "UA", "hourly", day)
				_, noDay := chat.AdminAnalytics(ctx, "T1", "UA", domain.AnalyticsMember, time.Time{})
				described := func(rows []domain.AnalyticsRow) []string {
					out := make([]string, 0, len(rows))
					for _, row := range rows {
						out = append(out, fmt.Sprintf("%s/%s/%s/%d/%d/%d", row.Kind, row.Date, row.EntityID, row.MessagesPosted, row.ReactionsAdded, row.MemberCount))
					}
					return out
				}
				return []any{described(members), described(channels), len(everything) >= len(channels), badKind != nil, noDay != nil}, nil
			},
		},
		{
			// An app's activity log is written when the app answers a function,
			// and a level filter is a rank comparison rather than a name match.
			// Both compositions must order and filter identically.
			name: "app activity is recorded and filtered by rank identically",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				empty, err := chat.AppActivities(ctx, "T1", "A1", domain.AppActivityFilter{}, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				_, badLevel := chat.AdminAppActivities(ctx, "T1", "U-nobody", domain.AppActivityFilter{}, domain.PageRequest{Limit: 10})
				admin, err := chat.AdminAppActivities(ctx, "T1", "UA", domain.AppActivityFilter{MinLevel: domain.ActivityWarn}, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				unnamedApp, unnamedErr := chat.AppActivities(ctx, "T1", "", domain.AppActivityFilter{}, domain.PageRequest{Limit: 10})
				return []any{len(empty.Activities), empty.HasMore, len(admin.Activities), len(unnamedApp.Activities), badLevel != nil, unnamedErr != nil}, nil
			},
		},
		{
			// A lookup that names no filter answers every channel, and a batch
			// that names a channel the workspace does not hold changes nothing.
			// Both compositions must agree on each.
			name: "administrative channel lookup, move, exclusion, and linking agree",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				everything, err := chat.AdminLookupConversations(ctx, "T1", "UA", domain.ConversationLookup{}, domain.PageRequest{Limit: 50})
				if err != nil {
					return nil, err
				}
				quiet, err := chat.AdminLookupConversations(ctx, "T1", "UA", domain.ConversationLookup{MaxMemberCount: 1}, domain.PageRequest{Limit: 50})
				if err != nil {
					return nil, err
				}
				if err := chat.AdminSetConversationsExcludedFromAI(ctx, "T1", "UA", []domain.ConversationID{"C1"}, true); err != nil {
					return nil, err
				}
				excluded, err := chat.AdminConversationsExcludedFromAI(ctx, "T1", "UA", []domain.ConversationID{"C1", "C2"})
				if err != nil {
					return nil, err
				}
				missingChannel := chat.AdminSetConversationsExcludedFromAI(ctx, "T1", "UA", []domain.ConversationID{"C1", "C-nobody"}, true)
				stillExcluded, err := chat.AdminConversationsExcludedFromAI(ctx, "T1", "UA", []domain.ConversationID{"C1"})
				if err != nil {
					return nil, err
				}
				if err := chat.AdminLinkConversationObjects(ctx, "T1", "UA", "C1", "00D000", []string{"a02", "a01"}); err != nil {
					return nil, err
				}
				linked, err := chat.AdminConversationObjects(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				records := make([]string, 0, len(linked))
				for _, object := range linked {
					records = append(records, object.OrgID+"/"+object.RecordID)
				}
				if err := chat.AdminUnlinkConversationObjects(ctx, "T1", "UA", []domain.ConversationID{"C1"}); err != nil {
					return nil, err
				}
				after, err := chat.AdminConversationObjects(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				missingTarget := chat.AdminBulkMoveConversations(ctx, "T1", "UA", []domain.ConversationID{"C1"}, "T-nobody")
				// A channel made for a record carries the link from the start.
				made, err := chat.AdminCreateConversationForObjects(ctx, "T1", "UA", "record-channel", "00D000", "a03", false)
				if err != nil {
					return nil, err
				}
				madeObjects, err := chat.AdminConversationObjects(ctx, "T1", "UA", made.ID)
				if err != nil {
					return nil, err
				}
				madeRecords := make([]string, 0, len(madeObjects))
				for _, object := range madeObjects {
					madeRecords = append(madeRecords, object.OrgID+"/"+object.RecordID)
				}
				_, unnamedRecord := chat.AdminCreateConversationForObjects(ctx, "T1", "UA", "no-record-channel", "00D000", "", false)
				return []any{
					made.Name, madeRecords, unnamedRecord != nil,
					len(everything.Conversations) > 0, len(quiet.Conversations) <= len(everything.Conversations),
					excluded, len(stillExcluded), records, len(after),
					missingChannel != nil, missingTarget != nil,
				}, nil
			},
		},
		{
			// An app nobody has configured answers the defaults, so both
			// compositions must report the same effective configuration.
			name: "app configuration defaults and resolution clearance agree",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				defaults, err := chat.AdminAppConfigs(ctx, "T1", "UA", []domain.AppID{"A1"})
				if err != nil {
					return nil, err
				}
				written, err := chat.AdminSetAppConfig(ctx, "T1", "UA", domain.AppConfig{
					AppID: "A1", DomainURLs: []string{"https://example.invalid"},
					WorkflowAuthStrategy: domain.WorkflowAuthEndUserOnly,
				})
				if err != nil {
					return nil, err
				}
				_, badStrategy := chat.AdminSetAppConfig(ctx, "T1", "UA", domain.AppConfig{AppID: "A1", WorkflowAuthStrategy: "whoever"})
				_, unknownApp := chat.AdminSetAppConfig(ctx, "T1", "UA", domain.AppConfig{AppID: "A-nobody", WorkflowAuthStrategy: domain.WorkflowAuthBuilderChoice})
				after, err := chat.AdminAppConfigs(ctx, "T1", "UA", []domain.AppID{"A1"})
				if err != nil {
					return nil, err
				}
				undecided := chat.AdminClearAppResolution(ctx, "T1", "UA", "A1")
				return []any{
					defaults[0].WorkflowAuthStrategy, defaults[0].DomainURLs, defaults[0].DomainEmails,
					written.WorkflowAuthStrategy, after[0].DomainURLs, after[0].WorkflowAuthStrategy,
					badStrategy != nil, unknownApp != nil, undecided != nil,
				}, nil
			},
		},
		{
			// A resource nobody has set a permission on answers the default, so
			// both compositions must report the same effective answer rather
			// than one answering nothing.
			name: "administrative automation permissions default and page identically",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				functions, err := chat.AdminFunctionPermissions(ctx, "T1", "UA", []string{"Fn1", "Fn2"})
				if err != nil {
					return nil, err
				}
				set, err := chat.AdminSetFunctionPermission(ctx, "T1", "UA", "Fn1", domain.AutomationPermission{
					PermissionType: domain.PermissionNamedEntities, UserIDs: []domain.UserID{"U1"},
				})
				if err != nil {
					return nil, err
				}
				_, system := chat.AdminSetFunctionPermission(ctx, "T1", "UA", "Fn1", domain.AutomationPermission{PermissionType: domain.PermissionSystem})
				_, unnamed := chat.AdminSetFunctionPermission(ctx, "T1", "UA", "", domain.AutomationPermission{PermissionType: domain.PermissionEveryone})
				after, err := chat.AdminFunctionPermissions(ctx, "T1", "UA", []string{"Fn1", "Fn2"})
				if err != nil {
					return nil, err
				}
				trigger, err := chat.AdminSetTriggerTypePermission(ctx, "T1", "UA", domain.WorkflowTriggerScheduled, domain.AutomationPermission{PermissionType: domain.PermissionEveryone})
				if err != nil {
					return nil, err
				}
				_, badType := chat.AdminSetTriggerTypePermission(ctx, "T1", "UA", "sundial", domain.AutomationPermission{PermissionType: domain.PermissionEveryone})
				readTrigger, err := chat.AdminTriggerTypePermission(ctx, "T1", "UA", domain.WorkflowTriggerScheduled)
				if err != nil {
					return nil, err
				}
				workflows, err := chat.AdminWorkflowPermissions(ctx, "T1", "UA", []domain.WorkflowID{"Wf1"})
				if err != nil {
					return nil, err
				}
				described := func(values []domain.AutomationPermission) []string {
					out := make([]string, 0, len(values))
					for _, value := range values {
						out = append(out, value.ResourceID+"/"+string(value.PermissionType))
					}
					return out
				}
				return []any{
					described(functions), described(after), described(workflows),
					set.PermissionType, set.UserIDs, trigger.ResourceID, readTrigger.PermissionType,
					system != nil, unnamed != nil, badType != nil,
				}, nil
			},
		},
		{
			// A barrier that restricts a subset of the subjects is not a
			// barrier, so both compositions must refuse one the same way.
			name: "information barriers are built, listed, and refused identically",
			seed: seedUserGroupParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				every := domain.BarrierSubjects()
				created, err := chat.AdminCreateBarrier(ctx, "T1", "UA", "S1", []domain.UserGroupID{"S2"}, every)
				if err != nil {
					return nil, err
				}
				partial := chat.AdminCreateBarrier
				_, someSubjects := partial(ctx, "T1", "UA", "S1", []domain.UserGroupID{"S2"}, []domain.BarrierSubject{domain.BarrierSubjectDirect})
				_, itself := partial(ctx, "T1", "UA", "S1", []domain.UserGroupID{"S1"}, every)
				_, unknownGroup := partial(ctx, "T1", "UA", "S1", []domain.UserGroupID{"S-nobody"}, every)
				updated, err := chat.AdminUpdateBarrier(ctx, "T1", "UA", created.ID, "S2", []domain.UserGroupID{"S1"}, every)
				if err != nil {
					return nil, err
				}
				_, unknownBarrier := chat.AdminUpdateBarrier(ctx, "T1", "UA", "B-nobody", "S1", []domain.UserGroupID{"S2"}, every)
				page, err := chat.AdminBarriers(ctx, "T1", "UA", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				if err := chat.AdminDeleteBarrier(ctx, "T1", "UA", created.ID); err != nil {
					return nil, err
				}
				missing := chat.AdminDeleteBarrier(ctx, "T1", "UA", created.ID)
				left, err := chat.AdminBarriers(ctx, "T1", "UA", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{
					created.PrimaryGroupID, created.BarrieredFromIDs, created.Subjects,
					updated.PrimaryGroupID, updated.BarrieredFromIDs, len(page.Barriers), len(left.Barriers),
					someSubjects != nil, itself != nil, unknownGroup != nil, unknownBarrier != nil, missing != nil,
				}, nil
			},
		},
		{
			// A member on the workspace default carries no settings row, so
			// both compositions must leave that member out of the answer rather
			// than reporting zeros.
			name: "session settings are written, read back, and cleared identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				settings := domain.SessionSettings{Duration: 12 * 60 * 60, DesktopAppBrowserQuit: true}
				if err := chat.AdminSetSessionSettings(ctx, "T1", "UA", []domain.UserID{"U1"}, settings); err != nil {
					return nil, err
				}
				tooShort := chat.AdminSetSessionSettings(ctx, "T1", "UA", []domain.UserID{"U1"}, domain.SessionSettings{Duration: 60 * 60})
				stranger := chat.AdminSetSessionSettings(ctx, "T1", "UA", []domain.UserID{"U-nobody"}, settings)
				read, err := chat.AdminSessionSettings(ctx, "T1", "UA", []domain.UserID{"U1", "U2"})
				if err != nil {
					return nil, err
				}
				described := make([]string, 0, len(read))
				for _, value := range read {
					described = append(described, fmt.Sprintf("%s/%d/%t/%t", value.UserID, value.Duration, value.DesktopAppBrowserQuit, value.MobileDeviceCheck))
				}
				if err := chat.AdminClearSessionSettings(ctx, "T1", "UA", []domain.UserID{"U1"}); err != nil {
					return nil, err
				}
				cleared, err := chat.AdminSessionSettings(ctx, "T1", "UA", []domain.UserID{"U1"})
				if err != nil {
					return nil, err
				}
				return []any{described, len(cleared), tooShort != nil, stranger != nil}, nil
			},
		},
		{
			// The member's own read is what the sign-in paths use to decide how
			// long a session lives, so the two compositions must agree on the
			// duration, on the absence that means the workspace default, and on
			// refusing somebody who is not a member. A distributed composition
			// that answered zero where the local one answered twelve hours would
			// hand out sessions three times too long.
			name: "a member reads their own session policy identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminSetSessionSettings(ctx, "T1", "UA", []domain.UserID{"U1"},
					domain.SessionSettings{Duration: 12 * 60 * 60, DesktopAppBrowserQuit: true}); err != nil {
					return nil, err
				}
				configured, err := chat.MemberSessionSettings(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				// U2 has no row: the absence is the workspace default, and
				// Lifetime resolves it rather than the caller.
				def, err := chat.MemberSessionSettings(ctx, "T1", "U2")
				if err != nil {
					return nil, err
				}
				_, strangerErr := chat.MemberSessionSettings(ctx, "T1", "U-nobody")
				return []any{
					fmt.Sprintf("%s/%d/%t/%s", configured.UserID, configured.Duration, configured.DesktopAppBrowserQuit, configured.Lifetime()),
					fmt.Sprintf("%s/%d/%s", def.UserID, def.Duration, def.Lifetime()),
					strangerErr != nil,
				}, nil
			},
		},
		{
			// An authentication policy this deployment does not hold cannot be
			// assigned, and both compositions must refuse the same names.
			name: "authentication policy entities are assigned, paged, and refused identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminAssignAuthPolicy(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, []string{"U2", "U1"}); err != nil {
					return nil, err
				}
				unknownPolicy := chat.AdminAssignAuthPolicy(ctx, "T1", "UA", "sso_only", domain.PolicyEntityUser, []string{"U1"})
				unknownKind := chat.AdminAssignAuthPolicy(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, "CHANNEL", []string{"U1"})
				stranger := chat.AdminAssignAuthPolicy(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, []string{"U-nobody"})
				page, err := chat.AdminAuthPolicyEntities(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, domain.PageRequest{Limit: 1})
				if err != nil {
					return nil, err
				}
				first := make([]string, 0, len(page.Entities))
				for _, entity := range page.Entities {
					first = append(first, entity.EntityID)
				}
				rest, err := chat.AdminAuthPolicyEntities(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, domain.PageRequest{Limit: 10, Cursor: page.NextCursor})
				if err != nil {
					return nil, err
				}
				second := make([]string, 0, len(rest.Entities))
				for _, entity := range rest.Entities {
					second = append(second, entity.EntityID)
				}
				if err := chat.AdminRemoveAuthPolicyEntities(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, []string{"U1"}); err != nil {
					return nil, err
				}
				left, err := chat.AdminAuthPolicyEntities(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{first, page.TotalCount, page.HasMore, second, len(left.Entities), left.TotalCount,
					unknownPolicy != nil, unknownKind != nil, stranger != nil}, nil
			},
		},
		{
			// A role assignment is a triple, so the two compositions must agree
			// on the order it pages in and on what a repeat write does.
			name: "role assignments are written, paged, and removed identically",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminAddRoleAssignments(ctx, "T1", "UA", "Rl0A", []string{"C2", "C1"}, []domain.UserID{"U1"}); err != nil {
					return nil, err
				}
				repeat := chat.AdminAddRoleAssignments(ctx, "T1", "UA", "Rl0A", []string{"C1"}, []domain.UserID{"U1"})
				stranger := chat.AdminAddRoleAssignments(ctx, "T1", "UA", "Rl0A", []string{"C1"}, []domain.UserID{"U-nobody"})
				page, err := chat.AdminListRoleAssignments(ctx, "T1", "UA", "Rl0A", domain.PageRequest{Limit: 1})
				if err != nil {
					return nil, err
				}
				first := make([]string, 0, len(page.Assignments))
				for _, assignment := range page.Assignments {
					first = append(first, string(assignment.UserID)+"/"+assignment.EntityID)
				}
				rest, err := chat.AdminListRoleAssignments(ctx, "T1", "UA", "Rl0A", domain.PageRequest{Limit: 10, Cursor: page.NextCursor})
				if err != nil {
					return nil, err
				}
				second := make([]string, 0, len(rest.Assignments))
				for _, assignment := range rest.Assignments {
					second = append(second, string(assignment.UserID)+"/"+assignment.EntityID)
				}
				if err := chat.AdminRemoveRoleAssignments(ctx, "T1", "UA", "Rl0A", []string{"C1"}, []domain.UserID{"U1"}); err != nil {
					return nil, err
				}
				left, err := chat.AdminListRoleAssignments(ctx, "T1", "UA", "Rl0A", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{first, page.HasMore, second, rest.HasMore, len(left.Assignments), repeat != nil, stranger != nil}, nil
			},
		},
		{
			// A workspace that hides itself answers no contacts, whatever the
			// addresses match. Both compositions must agree on that, because
			// the answer tells the caller who works here.
			name: "discoverable contacts follow the workspace setting",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				open, err := chat.DiscoverableContacts(ctx, "T1", "U1", []string{"u1@example.com", "nobody@example.com"})
				if err != nil {
					return nil, err
				}
				found := make([]string, 0, len(open))
				for _, user := range open {
					found = append(found, string(user.ID))
				}
				_, empty := chat.DiscoverableContacts(ctx, "T1", "U1", nil)
				return []any{found, empty != nil}, nil
			},
		},
		{
			// admin.functions.list reads the manifests of the installed apps,
			// because a function exists in a manifest and nowhere else.
			name: "an administrator lists the functions the installed apps declare",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				functions, err := chat.AdminFunctions(ctx, "T1", "UA")
				if err != nil {
					return nil, err
				}
				listed := make([]string, 0, len(functions))
				for _, function := range functions {
					listed = append(listed, string(function.AppID)+":"+function.CallbackID+":"+function.Title)
				}
				_, memberErr := chat.AdminFunctions(ctx, "T1", "U1")
				return []any{listed, memberErr != nil}, nil
			},
		},
		{
			// A member withdraws an app request. Cancelling records that nobody
			// decided it, which is a third state beside approved and
			// restricted.
			name: "an app request can be cancelled and lists under its own status",
			seed: seedRequestedAppParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminCancelAppRequest(ctx, "T1", "UA", "A-cancel", "R-cancel"); err != nil {
					return nil, err
				}
				// Cancelling is not a decision an administrator may take back:
				// a request that was already decided stays decided, and one
				// that was never filed cannot be withdrawn.
				repeat := chat.AdminCancelAppRequest(ctx, "T1", "UA", "A-cancel", "R-cancel") != nil
				decided := chat.AdminCancelAppRequest(ctx, "T1", "UA", "A-approved", "R-approved") != nil
				unfiled := chat.AdminCancelAppRequest(ctx, "T1", "UA", "A-nobody", "R-nobody") != nil
				cancelled, err := chat.AdminListApps(ctx, "T1", "UA", domain.AppApprovalCancelled, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				listed := make([]string, 0, len(cancelled.Apps))
				for _, approval := range cancelled.Apps {
					listed = append(listed, string(approval.ID)+":"+string(approval.Status))
				}
				member := chat.AdminCancelAppRequest(ctx, "T1", "U1", "A-cancel", "R-cancel") != nil
				empty := chat.AdminCancelAppRequest(ctx, "T1", "UA", "", "") != nil
				return []any{listed, member, empty, repeat, decided, unfiled}, nil
			},
		},
		{
			// A guest account lapses at a stored instant. A member who never
			// received one reads the zero time, which must not decode as an
			// instant in 1754.
			name: "an administrator reads when a guest account lapses",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				none, err := chat.UserExpiration(ctx, "T1", "UA", "U1")
				if err != nil {
					return nil, err
				}
				lapses := time.Unix(1_900_000_000, 0).UTC()
				if err := chat.SetUserExpiration(ctx, "T1", "UA", "U1", lapses); err != nil {
					return nil, err
				}
				stored, err := chat.UserExpiration(ctx, "T1", "UA", "U1")
				if err != nil {
					return nil, err
				}
				_, memberErr := chat.UserExpiration(ctx, "T1", "U1", "U1")
				_, missing := chat.UserExpiration(ctx, "T1", "UA", "U-not-here")
				return []any{none.IsZero(), stored.Equal(lapses), memberErr != nil, missing != nil}, nil
			},
		},
		{
			// An administrator archives or deletes several channels in one
			// request. The service checks every channel first, so a name that
			// is not here stops the request before it changes anything.
			name: "an administrator archives and deletes channels in bulk",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				member := chat.AdminBulkArchiveConversations(ctx, "T1", "U1", []domain.ConversationID{"C1"}) != nil
				empty := chat.AdminBulkArchiveConversations(ctx, "T1", "UA", nil) != nil
				absent := chat.AdminBulkArchiveConversations(ctx, "T1", "UA", []domain.ConversationID{"C1", "C-not-here"}) != nil
				if err := chat.AdminBulkArchiveConversations(ctx, "T1", "UA", []domain.ConversationID{"C1"}); err != nil {
					return nil, err
				}
				archived, err := chat.ConversationInfo(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				// A channel that is already archived is the state the request
				// asked for.
				again := chat.AdminBulkArchiveConversations(ctx, "T1", "UA", []domain.ConversationID{"C1"}) == nil
				if err := chat.AdminBulkDeleteConversations(ctx, "T1", "UA", []domain.ConversationID{"C1"}); err != nil {
					return nil, err
				}
				_, missing := chat.ConversationInfo(ctx, "T1", "UA", "C1")
				return []any{member, empty, absent, archived.Archived, again, missing != nil}, nil
			},
		},
		{
			// The two conversions are not one operation with a flag. Making a
			// channel private hides what was said from people who could read
			// it; making one public shows it to people who never could, and
			// nothing takes that back. Both compositions have to agree on the
			// asymmetry and on refusing a channel an external organization is
			// in.
			name: "a channel converts between public and private, and a connected channel does not",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				privately, err := chat.AdminConvertConversationToPrivate(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				// Converting again is the state that was asked for on neither
				// profile: it is the wrong kind of conversation now.
				repeated := func() bool {
					_, err := chat.AdminConvertConversationToPrivate(ctx, "T1", "UA", "C1")
					return err != nil
				}()
				publicly, err := chat.AdminConvertConversationToPublic(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				// A member cannot convert anything.
				_, memberErr := chat.AdminConvertConversationToPrivate(ctx, "T1", "U1", "C1")
				_, memberPublic := chat.AdminConvertConversationToPublic(ctx, "T1", "U1", "C1")
				missing := func() bool {
					_, err := chat.AdminConvertConversationToPublic(ctx, "T1", "UA", "C-not-here")
					return err != nil
				}()
				return []any{privately.PrivateFlag(), repeated, publicly.PrivateFlag(), memberErr != nil, memberPublic != nil, missing}, nil
			},
		},
		{
			// An administrator has to be able to find a workflow they do not own
			// and take it out of service. Both compositions must agree, because
			// an administrator told a workflow is stopped while it still runs
			// has been told something false about what their workspace does.
			name: "an administrator finds every workflow and takes one out of service",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				created, err := chat.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
					AppID: "A1", CallbackID: "admin-workflow", Title: "Nightly triage",
					Description: "Owned by somebody else", InputSchema: `{}`,
					Steps: `[{"function_id":"triage","title":"Triage request"}]`,
				})
				if err != nil {
					return nil, err
				}
				published, err := chat.UpdateWorkflow(ctx, "T1", "U1", created, created.Version, true)
				if err != nil {
					return nil, err
				}
				// The administrator does not own it and is not a manager.
				found, _, _, err := chat.AdminWorkflows(ctx, "T1", "UA", "nightly", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				titles := make([]string, 0, len(found))
				for _, value := range found {
					titles = append(titles, value.Title+":"+string(value.Status))
				}
				// A query nobody matches is an empty answer rather than everything.
				missing, _, _, err := chat.AdminWorkflows(ctx, "T1", "UA", "nothing-is-called-this", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				// A member cannot search the workspace or stop anything.
				_, _, _, memberErr := chat.AdminWorkflows(ctx, "T1", "U2", "", domain.PageRequest{Limit: 10})
				memberStop := chat.AdminUnpublishWorkflows(ctx, "T1", "U2", []domain.WorkflowID{published.ID}) != nil
				// A workflow that is not here stops the whole request.
				strangerStop := chat.AdminUnpublishWorkflows(ctx, "T1", "UA", []domain.WorkflowID{published.ID, "Wf-not-here"}) != nil
				beforeStop, err := chat.GetWorkflow(ctx, "T1", "U1", published.ID)
				if err != nil {
					return nil, err
				}
				if err := chat.AdminUnpublishWorkflows(ctx, "T1", "UA", []domain.WorkflowID{published.ID}); err != nil {
					return nil, err
				}
				stopped, err := chat.GetWorkflow(ctx, "T1", "U1", published.ID)
				if err != nil {
					return nil, err
				}
				// Stopping twice is the state that was asked for, not an error.
				again := chat.AdminUnpublishWorkflows(ctx, "T1", "UA", []domain.WorkflowID{published.ID}) == nil
				return []any{titles, len(missing), memberErr != nil, memberStop, strangerStop,
					string(beforeStop.Status), string(stopped.Status), stopped.Version, stopped.Title, again}, nil
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
				// The step payload was written by running the workflow and is
				// read back here: its inputs and status are compared as values,
				// so a composition that dropped or garbled the payload diverges
				// rather than passing on the call being accepted. A stranger is
				// refused the read.
				runSteps, err := chat.WorkflowRunSteps(ctx, "T1", "U1", run.ID)
				if err != nil {
					return nil, err
				}
				_, stepStranger := chat.WorkflowRunSteps(ctx, "T1", "U-absent", run.ID)
				if stepStranger == nil {
					return nil, fmt.Errorf("a stranger read a run's steps")
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
				initialCopy, err := chat.GetWorkflowPermission(ctx, "T1", "U1", "WfParity", "copy")
				if err != nil {
					return nil, err
				}
				setCopy, err := chat.SetWorkflowPermission(ctx, "T1", "U1", "WfParity", "copy", domain.AutomationPermission{
					PermissionType: "named_entities", UserIDs: []domain.UserID{"U2"},
				})
				if err != nil {
					return nil, err
				}
				storedCopy, err := chat.GetWorkflowPermission(ctx, "T1", "U1", "WfParity", "copy")
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
					initialCopy.PermissionType, initialCopy.ResourceType,
					setCopy.PermissionType, len(setCopy.UserIDs),
					storedCopy.PermissionType, len(storedCopy.UserIDs), storedCopy.ResourceType,
					len(featured), featured[0].Title,
					len(steps), steps[0].Title, steps[0].StepID,
					len(runSteps), runSteps[0].Inputs, string(runSteps[0].Status), runSteps[0].FunctionID,
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
				return []any{expanded.Kind == domain.ConversationTypeMPIM, converted.PrivateFlag(), converted.Kind == domain.ConversationTypeIM, converted.Kind == domain.ConversationTypeMPIM, converted.Name, len(members.Users), texts}, nil
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
			// Both connection families hand out a single-use ticket and then hold
			// a slot against a limit. What matters, and what a single read cannot
			// show, is that the slot is actually given back: if release did not
			// free one, an app that reconnected normally would be locked out
			// after its quota of reconnections, which is a failure that only
			// appears in production and only after a while.
			name: "connection tickets are single use and their slots are returned",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				ticket, err := chat.CreateRTMConnection(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				consumed, err := chat.ConsumeRTMConnection(ctx, ticket.ID)
				if err != nil {
					return nil, err
				}
				// A ticket is a one-time credential: dialling it twice must not
				// open two streams from one grant.
				_, reuseErr := chat.ConsumeRTMConnection(ctx, ticket.ID)
				expiry := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
				// Fill the socket mode quota exactly, then give one slot back and
				// take it again.
				for index := range domain.SocketModeConnectionLimit {
					identifier := fmt.Sprintf("release-socket-%d", index)
					if err := chat.CreateSocketModeConnection(ctx, domain.SocketModeConnection{
						ID: identifier, AppID: "A1", ExpiresAt: expiry,
					}); err != nil {
						return nil, err
					}
					if _, err := chat.ConsumeSocketModeConnection(ctx, identifier); err != nil {
						return nil, err
					}
				}
				atLimit, err := chat.CountSocketModeConnections(ctx, "A1")
				if err != nil {
					return nil, err
				}
				if err := chat.ReleaseSocketModeConnection(ctx, "release-socket-0"); err != nil {
					return nil, err
				}
				afterRelease, err := chat.CountSocketModeConnections(ctx, "A1")
				if err != nil {
					return nil, err
				}
				if err := chat.CreateSocketModeConnection(ctx, domain.SocketModeConnection{
					ID: "release-socket-replacement", AppID: "A1", ExpiresAt: expiry,
				}); err != nil {
					return nil, err
				}
				_, replacementErr := chat.ConsumeSocketModeConnection(ctx, "release-socket-replacement")
				// Renewal extends a live connection rather than opening another,
				// so the count must not move.
				renewErr := chat.RenewSocketModeConnection(ctx, "release-socket-replacement", expiry.Add(time.Hour))
				afterRenew, err := chat.CountSocketModeConnections(ctx, "A1")
				if err != nil {
					return nil, err
				}
				return []any{
					ticket.WorkspaceID, ticket.UserID, ticket.ExpiresAt.After(time.Now().UTC()),
					consumed.ID == ticket.ID, consumed.UserID, consumed.Cursor == ticket.Cursor,
					reuseErr != nil, errors.Is(reuseErr, storepkg.ErrNotFound),
					atLimit, afterRelease, atLimit - afterRelease,
					replacementErr == nil, renewErr == nil, afterRenew,
				}, nil
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
			// The refusal is an invalid argument, and the case asserts it. A
			// wantAgreedFailure flag used to hold this slot open while the store
			// guards carried bare errors and a remote caller was answered
			// codes.Unavailable — HTTP 503, a retry hint for a request that can
			// never succeed. The guards carry store.ErrInvalidArgument now, so
			// the contract is pinned like every other sentinel case; weakening
			// it back to "any agreed failure" would let that regression return
			// unseen.
			name:         "a page limit of zero",
			wantSentinel: storepkg.ErrInvalidArgument,
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
			// Blocks and attachments are the richest strings the seam carries, and
			// the writers that take them are the ones a converter is most likely to
			// drop a field from: each has a slightly different arity and the
			// difference between them is exactly which content column is written.
			// Reading the thread back afterwards is what proves the content was
			// stored rather than merely echoed by the writer's own response.
			name: "blocks and attachments survive every message writer across the seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				blocks := `[{"type":"section","text":{"type":"mrkdwn","text":"root"}}]`
				attachments := `[{"color":"#36a64f","text":"attached"}]`
				root, err := chat.PostWithBlocks(ctx, "T1", "U1", "C1", "root text", blocks, "", "")
				if err != nil {
					return nil, err
				}
				rootTimestamp := domain.NewMessageTimestamp(root.CreatedAt)
				reply, err := chat.PostWithBlocksAndAttachments(ctx, "T1", "U1", "C1", "reply text",
					`[{"type":"divider"}]`, attachments, rootTimestamp, "", "A1")
				if err != nil {
					return nil, err
				}
				replyTimestamp := domain.NewMessageTimestamp(reply.CreatedAt)
				editedBlocks, err := chat.UpdateWithBlocks(ctx, "T1", "U1", "C1", rootTimestamp, "root edited",
					`[{"type":"section","text":{"type":"mrkdwn","text":"root edited"}}]`)
				if err != nil {
					return nil, err
				}
				editedBoth, err := chat.UpdateWithBlocksAndAttachments(ctx, "T1", "U1", "C1", replyTimestamp,
					"reply edited", `[{"type":"divider"}]`, `[{"color":"#ff0000","text":"re-attached"}]`)
				if err != nil {
					return nil, err
				}
				// A patch names some fields and not others, so it takes two to
				// exercise the shape: one that must leave the unnamed fields as
				// they were, and one that must carry a field the first omitted. A
				// converter that dropped a pointer, or that turned an absent one
				// into an empty string, fails exactly one of the two.
				patchedText := "reply patched"
				patched, err := chat.UpdateMessage(ctx, "T1", "U1", "C1", replyTimestamp,
					domain.MessagePatch{Text: &patchedText})
				if err != nil {
					return nil, err
				}
				patchedAttachments := `[{"color":"#0000ff","text":"patched attachment"}]`
				patchedBlocks := `[{"type":"section","text":{"type":"mrkdwn","text":"patched block"}}]`
				repatched, err := chat.UpdateMessage(ctx, "T1", "U1", "C1", replyTimestamp,
					domain.MessagePatch{Blocks: &patchedBlocks, Attachments: &patchedAttachments})
				if err != nil {
					return nil, err
				}
				permalink, err := chat.Permalink(ctx, "T1", "U1", "C1", rootTimestamp)
				if err != nil {
					return nil, err
				}
				thread, err := chat.Replies(ctx, "T1", "U1", "C1", rootTimestamp, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				stored := make([]string, 0, len(thread.Messages))
				for _, message := range thread.Messages {
					stored = append(stored, strings.Join([]string{
						message.Text, message.Blocks, message.Attachments, string(message.AppID),
					}, "|"))
				}
				// The permalink carries the message's own generated timestamp, so
				// the two compositions cannot produce the same string. What they
				// must agree on is the shape, which is what the comparison asserts.
				wantPermalink := "/archives/C1/p" + strings.ReplaceAll(string(rootTimestamp), ".", "")
				return []any{
					root.Blocks, root.Text, reply.Blocks, reply.Attachments, string(reply.AppID),
					reply.ThreadTimestamp == rootTimestamp,
					editedBlocks.Text, editedBlocks.Blocks, editedBlocks.EditedBy,
					editedBoth.Text, editedBoth.Blocks, editedBoth.Attachments,
					patched.Text, patched.Blocks, patched.Attachments,
					repatched.Text, repatched.Blocks, repatched.Attachments,
					permalink == wantPermalink, stored, thread.HasMore,
				}, nil
			},
		},
		{
			// The scheduled writers store the same content a posted message
			// carries, one send in the future. They are separate seam methods from
			// the posting ones and drop fields independently of them.
			name: "scheduled message blocks and attachments survive the seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// Scheduling is capped at 120 days out, so the instant has to be
				// near. The two compositions run this body separately and each
				// reads its own clock, so nothing derived from it is compared
				// directly: the projections below are all relative to postAt.
				postAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Hour)
				withBlocks, err := chat.ScheduleMessageWithBlocks(ctx, "T1", "U1", "C1", "later text",
					`[{"type":"section","text":{"type":"mrkdwn","text":"later"}}]`, postAt)
				if err != nil {
					return nil, err
				}
				withBoth, err := chat.ScheduleMessageWithBlocksAndAttachments(ctx, "T1", "U1", "C1", "later both",
					`[{"type":"divider"}]`, `[{"color":"#36a64f","text":"attached later"}]`, postAt.Add(time.Hour))
				if err != nil {
					return nil, err
				}
				page, err := chat.ScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				stored := make([]string, 0, len(page.Items))
				for _, message := range page.Items {
					stored = append(stored, strings.Join([]string{
						message.Text, message.Blocks, message.Attachments, message.PostAt.UTC().Sub(postAt).String(),
					}, "|"))
				}
				return []any{
					withBlocks.Text, withBlocks.Blocks, withBlocks.Attachments, withBlocks.PostAt.UTC().Equal(postAt),
					withBoth.Text, withBoth.Blocks, withBoth.Attachments, withBoth.PostAt.UTC().Sub(postAt).String(),
					stored, page.HasMore,
				}, nil
			},
		},
		{
			// Retention is the one policy in the product whose mistakes are
			// irreversible: the sweep deletes, it does not tombstone. The seam
			// therefore has to agree not only on what was stored but on which of
			// the two policies actually governs a conversation, because that is
			// the number the sweep uses.
			name: "retention policy and its resolution agree across the seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				requireSeed(t, seedWorkspaceRole(target, "T1", "U1", domain.WorkspaceRoleOwner))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				workspace, err := chat.SetWorkspaceRetention(ctx, "T1", "U1", domain.RetentionPolicy{MessageDays: 90, FileDays: 30})
				if err != nil {
					return nil, err
				}
				readBack, err := chat.WorkspaceRetention(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				// With no override the workspace default governs.
				noOverride, defaultGoverning, err := chat.ConversationRetention(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				if err := chat.SetConversationRetention(ctx, "T1", "U1", "C1", 7); err != nil {
					return nil, err
				}
				override, overrideGoverning, err := chat.ConversationRetention(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				if err := chat.RemoveConversationRetention(ctx, "T1", "U1", "C1"); err != nil {
					return nil, err
				}
				reverted, revertedGoverning, err := chat.ConversationRetention(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				// A workspace that has never been swept reports the zero instant
				// rather than an error, so the admin surface can say "never".
				swept, err := chat.LastRetentionSweep(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				return []any{
					workspace.MessageDays, workspace.FileDays, readBack.MessageDays, readBack.FileDays,
					noOverride.DurationDays, defaultGoverning,
					override.DurationDays, string(override.ConversationID), overrideGoverning,
					reverted.DurationDays, revertedGoverning, swept.IsZero(),
				}, nil
			},
		},
		{
			// A call is the one object whose participant list is edited by two
			// methods that are not each other's inverse at the seam: adding takes
			// a set and removing takes a set, and either can be handed someone who
			// is not there. The projection reads the list back after each edit
			// rather than trusting the writers to report it.
			name: "external call lifecycle and participants agree across the seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				started := time.Now().UTC().Truncate(time.Second)
				call, err := chat.AddCall(ctx, "T1", "U1", "ext-call-1", "EXT-1",
					"https://example.com/join", "https://example.com/desktop", "Design review", started,
					[]domain.UserID{"U1", "U2"})
				if err != nil {
					return nil, err
				}
				if err := chat.AddCallParticipants(ctx, "T1", "U1", call.ID, []domain.UserID{"UA"}); err != nil {
					return nil, err
				}
				added, err := chat.GetCall(ctx, "T1", "U1", call.ID)
				if err != nil {
					return nil, err
				}
				if err := chat.RemoveCallParticipants(ctx, "T1", "U1", call.ID, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				removed, err := chat.GetCall(ctx, "T1", "U1", call.ID)
				if err != nil {
					return nil, err
				}
				updated, err := chat.UpdateCall(ctx, "T1", "U1", call.ID, "Design review (final)",
					"https://example.com/join2", "https://example.com/desktop2")
				if err != nil {
					return nil, err
				}
				if err := chat.EndCall(ctx, "T1", "U1", call.ID, 1800); err != nil {
					return nil, err
				}
				ended, err := chat.GetCall(ctx, "T1", "U1", call.ID)
				if err != nil {
					return nil, err
				}
				participants := func(call domain.Call) []string {
					names := make([]string, 0, len(call.Participants))
					for _, participant := range call.Participants {
						names = append(names, string(participant))
					}
					sort.Strings(names)
					return names
				}
				return []any{
					call.ExternalUniqueID, call.ExternalDisplayID, call.Title, string(call.Kind),
					call.StartedAt.UTC().Equal(started), participants(call),
					participants(added), participants(removed),
					updated.Title, updated.JoinURL, updated.DesktopAppJoinURL,
					ended.DurationSeconds, ended.EndedAt.IsZero(), participants(ended),
				}, nil
			},
		},
		{
			// The event cursor is how a disconnected client catches up: SSE
			// resumption and the workflow trigger poller both read it. A cursor
			// that skipped or repeated a record would lose or duplicate a
			// notification, and neither is visible from a single read, so the
			// case walks the log twice and compares the halves against the whole.
			name: "the event cursor pages the same log across the seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "first", "", ""); err != nil {
					return nil, err
				}
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "second", "", ""); err != nil {
					return nil, err
				}
				topics := func(records []events.Record) []string {
					names := make([]string, 0, len(records))
					for _, record := range records {
						names = append(names, record.Event.Topic)
					}
					return names
				}
				whole, err := chat.ListEventsAfter(ctx, "T1", 0, 100)
				if err != nil {
					return nil, err
				}
				ascending := true
				for index := 1; index < len(whole); index++ {
					if whole[index].Sequence <= whole[index-1].Sequence {
						ascending = false
					}
				}
				// Reading from the first record's own sequence must return the
				// rest and not repeat it: "after" is exclusive.
				var rest []events.Record
				if len(whole) > 0 {
					rest, err = chat.ListEventsAfter(ctx, "T1", whole[0].Sequence, 100)
					if err != nil {
						return nil, err
					}
				}
				limited, err := chat.ListEventsAfter(ctx, "T1", 0, 1)
				if err != nil {
					return nil, err
				}
				userScoped, err := chat.ListUserEventsAfter(ctx, "T1", "U1", 0, 100)
				if err != nil {
					return nil, err
				}
				// No app is installed in the fixture, so an app's view of the log
				// is empty. That is the answer both compositions must give: an
				// empty page, not a failure and not somebody else's events.
				appScoped, err := chat.ListAppEventsAfter(ctx, "A-absent", 0, 100)
				if err != nil {
					return nil, err
				}
				return []any{
					topics(whole), ascending, topics(rest), len(whole) == len(rest)+1,
					topics(limited), topics(userScoped), topics(appScoped),
				}, nil
			},
		},
		{
			// The modal stack is the richest state an app owns: a view carries a
			// payload, a hash guarding concurrent updates, a parent, and a root,
			// and every one of those is a field the seam can drop. The case
			// walks a real stack — open, push onto it, update by id — and reads
			// the Home tab back, because a surface that reports the write and
			// stores something else is the failure worth catching.
			name: "the modal stack and the Home tab agree across the seam",
			seed: seedViewParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				modal := `{"type":"modal","callback_id":"deploy","external_id":"deploy-1","title":{"type":"plain_text","text":"Deploy"},"submit":{"type":"plain_text","text":"Go"},"blocks":[{"type":"input","block_id":"env","label":{"type":"plain_text","text":"Environment"},"element":{"type":"plain_text_input","action_id":"env_input"}}]}`
				opened, err := chat.OpenView(ctx, "T1", "UBOT", "A1", "trigger_open", modal)
				if err != nil {
					return nil, err
				}
				pushed, err := chat.PushView(ctx, "T1", "UBOT", "A1", "trigger_push",
					`{"type":"modal","callback_id":"confirm","title":{"type":"plain_text","text":"Confirm"},"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Sure?"}}]}`)
				if err != nil {
					return nil, err
				}
				// The hash is the concurrency guard: an update carrying a stale
				// one must be refused, and both compositions must refuse it the
				// same way.
				staleErr := func() error {
					_, err := chat.UpdateView(ctx, "T1", "UBOT", "A1", string(pushed.ID), "",
						`{"type":"modal","title":{"type":"plain_text","text":"Stale"},"blocks":[]}`, "not-the-hash")
					return err
				}()
				updated, err := chat.UpdateView(ctx, "T1", "UBOT", "A1", string(pushed.ID), "",
					`{"type":"modal","title":{"type":"plain_text","text":"Confirmed"},"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Yes"}}]}`,
					pushed.Hash)
				if err != nil {
					return nil, err
				}
				// A trigger is single use, so the one already spent on the open
				// must not open a second modal.
				_, replayErr := chat.OpenView(ctx, "T1", "UBOT", "A1", "trigger_open", modal)

				published, err := chat.PublishView(ctx, "T1", "UBOT", "A1", "U1",
					`{"type":"home","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Welcome"}}]}`, "")
				if err != nil {
					return nil, err
				}
				installed, home, err := chat.AppHome(ctx, "T1", "U1", "A1")
				if err != nil {
					return nil, err
				}
				openedApp, openedHome, err := chat.OpenAppHome(ctx, "T1", "U1", "A1")
				if err != nil {
					return nil, err
				}
				dialogErr := chat.OpenDialog(ctx, "T1", "UBOT", "A1", "trigger_dialog",
					`{"callback_id":"ticket","title":"File a ticket","elements":[{"type":"text","name":"summary","label":"Summary"}]}`)
				// A dialog that is not a dialog is refused rather than stored,
				// and the spent trigger is what makes the refusal observable:
				// both compositions must reject the payload, not the trigger.
				invalidDialogErr := chat.OpenDialog(ctx, "T1", "UBOT", "A1", "trigger_replay", `{"callback_id":"ticket"}`)
				return []any{
					opened.Type, opened.ExternalID, opened.AppID, opened.UserID,
					opened.Hash != "", opened.RootViewID == "", opened.PreviousViewID == "",
					pushed.Type, pushed.RootViewID == opened.ID, pushed.PreviousViewID == opened.ID,
					staleErr != nil, errors.Is(staleErr, storepkg.ErrConflict),
					updated.ID == pushed.ID, updated.Hash != pushed.Hash, normalizeViewPayload(updated.Payload),
					replayErr != nil, errors.Is(replayErr, service.ErrInvalidTrigger),
					published.Type, published.UserID, normalizeViewPayload(published.Payload),
					installed.ID, installed.Name, installed.HomeTabEnabled, normalizeViewPayload(home.Payload),
					openedApp.ID, openedHome.Payload == home.Payload,
					dialogErr == nil, invalidDialogErr != nil,
					errors.Is(invalidDialogErr, service.ErrInvalidDialog),
				}, nil
			},
		},
		{
			// A Slack Connect invitation is settled by two organizations taking
			// different decisions, and the seam has to agree on which decision
			// each method records: denying and revoking both end an invitation
			// but are not the same fact, and declining belongs to the invited
			// side rather than the host.
			name: "the Slack Connect invitation lifecycle agrees across the seam",
			seed: seedConnectParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				accepted, err := chat.InviteShared(ctx, "T1", "U1", "C-accept", "T2", "")
				if err != nil {
					return nil, err
				}
				approved, err := chat.ApproveSharedInvite(ctx, "T1", "U1", accepted.ID)
				if err != nil {
					return nil, err
				}
				// Approving twice is refused: the invitation is no longer
				// pending, and both compositions must say so the same way.
				_, settledErr := chat.ApproveSharedInvite(ctx, "T1", "U1", accepted.ID)
				conversation, err := chat.AcceptSharedInvite(ctx, "T2", "U2-second", accepted.ID)
				if err != nil {
					return nil, err
				}

				denied, err := chat.InviteShared(ctx, "T1", "U1", "C-deny", "T2", "")
				if err != nil {
					return nil, err
				}
				refused, err := chat.DenySharedInvite(ctx, "T1", "U1", denied.ID)
				if err != nil {
					return nil, err
				}

				revoked, err := chat.InviteShared(ctx, "T1", "U1", "C-revoke", "T2", "")
				if err != nil {
					return nil, err
				}
				if _, err := chat.ApproveSharedInvite(ctx, "T1", "U1", revoked.ID); err != nil {
					return nil, err
				}
				withdrawn, err := chat.RevokeSharedInvite(ctx, "T1", "U1", revoked.ID)
				if err != nil {
					return nil, err
				}
				// Declining is the invited organization's answer, so it is
				// taken by the other workspace's administrator.
				declinable, err := chat.InviteShared(ctx, "T1", "U1", "C-revoke", "T2", "")
				if err != nil {
					return nil, err
				}
				if _, err := chat.ApproveSharedInvite(ctx, "T1", "U1", declinable.ID); err != nil {
					return nil, err
				}
				declined, err := chat.DeclineSharedInvite(ctx, "T2", "U2-second", declinable.ID)
				if err != nil {
					return nil, err
				}
				// The host decides whether the organization it let in may
				// invite others. That decision is durable state now, read back
				// through the seam and compared as a value, not only announced
				// in an event.
				permitted, err := chat.SetExternalInvitePermissions(ctx, "T1", "U1", "C-accept", "T2", true)
				if err != nil {
					return nil, err
				}
				permittedRead, err := chat.ExternalInvitePermission(ctx, "T1", "U1", "C-accept", "T2")
				if err != nil {
					return nil, err
				}
				if _, err := chat.SetExternalInvitePermissions(ctx, "T1", "U1", "C-accept", "T2", false); err != nil {
					return nil, err
				}
				restrictedRead, err := chat.ExternalInvitePermission(ctx, "T1", "U1", "C-accept", "T2")
				if err != nil {
					return nil, err
				}
				// The permission is carried by the event and nowhere else: the
				// conversation the call returns is unchanged by it. Reading the
				// journal is therefore the only way to see whether the flag
				// survived the seam, and a case that skipped it would pass with
				// the flag dropped.
				records, err := chat.ListEventsAfter(ctx, "T1", 0, 200)
				if err != nil {
					return nil, err
				}
				permissionEvents := make([]string, 0, 1)
				for _, record := range records {
					if record.Event.Topic != "conversation.external_invite_permissions_set" {
						continue
					}
					var payload struct {
						Channel   string `json:"channel_id"`
						Team      string `json:"team_id"`
						CanInvite string `json:"can_invite"`
					}
					if err := json.Unmarshal([]byte(record.Event.Payload), &payload); err != nil {
						return nil, err
					}
					permissionEvents = append(permissionEvents, strings.Join([]string{payload.Channel, payload.Team, payload.CanInvite}, "|"))
				}
				sort.Strings(permissionEvents)
				listed, err := chat.ListSharedInvites(ctx, "T1", "U1", domain.SharedInviteRevoked, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				revokedChannels := make([]string, 0, len(listed.Invites))
				for _, invite := range listed.Invites {
					revokedChannels = append(revokedChannels, string(invite.ConversationID))
				}
				sort.Strings(revokedChannels)
				return []any{
					string(accepted.ConversationID), string(accepted.TargetWorkspaceID),
					string(accepted.Status), accepted.InvitedBy,
					string(approved.Status), settledErr != nil,
					errors.Is(settledErr, service.ErrSharedInviteSettled),
					string(conversation.ID), conversation.WorkspaceID,
					// Denying and revoking both end an invitation and are both
					// recorded as revoked, which is only correct if the reason
					// is carried elsewhere; the projection pins the status so a
					// composition that invented a distinct one is caught.
					string(refused.Status), string(withdrawn.Status), string(declined.Status),
					string(permitted.ID), permissionEvents, revokedChannels, listed.HasMore,
					permittedRead, restrictedRead,
				}, nil
			},
		},
		{
			// Workspace administration is one long sequence rather than a set of
			// independent calls: a workspace is created, people are put in it by
			// three different routes — created outright, invited and approved,
			// invited and denied — and then counted, billed, reassigned and
			// removed. Splitting it into a case per method would lose the part
			// that matters, which is that each step sees what the last one did.
			name: "workspace administration agrees across the seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				requireSeed(t, seedWorkspaceRole(target, "T1", "U1", domain.WorkspaceRoleOwner))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				created, err := chat.AdminCreateWorkspace(ctx, "T1", "U1", "T-new", "New team", "A second workspace", domain.WorkspaceDiscoverabilityInviteOnly)
				if err != nil {
					return nil, err
				}
				member, err := chat.AdminCreateUser(ctx, "T1", "U1", "created@example.test", "Created Person", domain.WorkspaceRoleMember)
				if err != nil {
					return nil, err
				}
				// Two invitations: one that will be approved and accepted, one
				// that will be denied, so both terminal states are compared.
				for _, email := range []string{"approved@example.test", "denied@example.test"} {
					if err := chat.AdminInviteUser(ctx, "T1", "U1", email, []domain.ConversationID{"C1"}, "Join us", "Invited Person", false, false, false, time.Time{}); err != nil {
						return nil, err
					}
				}
				pending, err := chat.AdminListInviteRequests(ctx, "T1", "U1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				byEmail := make(map[string]domain.InviteRequestID, len(pending.Requests))
				for _, request := range pending.Requests {
					byEmail[request.Email] = request.ID
				}
				// The preview is what a signed-out invitee sees, so it takes no
				// actor at all and must still answer.
				preview, err := chat.InvitationPreview(ctx, "T1", byEmail["approved@example.test"])
				if err != nil {
					return nil, err
				}
				if err := chat.AdminApproveInviteRequest(ctx, "T1", "U1", byEmail["approved@example.test"]); err != nil {
					return nil, err
				}
				if err := chat.AdminDenyInviteRequest(ctx, "T1", "U1", byEmail["denied@example.test"]); err != nil {
					return nil, err
				}
				accepted, err := chat.AcceptInvitationForEmail(ctx, "T1", "approved@example.test", "Accepted Person")
				if err != nil {
					return nil, err
				}
				if err := chat.AdminAssignUser(ctx, "T1", "U1", accepted.ID, []domain.ConversationID{"C2"}); err != nil {
					return nil, err
				}
				// Only the two authority roles are listable: the routes that
				// reach this are admin.teams.admins.list and .owners.list.
				members, err := chat.AdminTeamUsers(ctx, "T1", "U1", domain.WorkspaceRoleAdmin, domain.PageRequest{Limit: 50})
				if err != nil {
					return nil, err
				}
				emails := make([]string, 0, len(members.Users))
				for _, user := range members.Users {
					emails = append(emails, user.Email)
				}
				sort.Strings(emails)
				billable, err := chat.TeamBillableInfo(ctx, "T1", "U1", "")
				if err != nil {
					return nil, err
				}
				// The accounts this case creates carry generated identifiers, so
				// only the seeded ones are named; the rest are counted, which
				// is what says everybody created along the way is billable.
				seeded := map[domain.UserID]bool{"U1": true, "U2": true, "UA": true}
				billing := make([]string, 0, len(billable.Users))
				billingCount := 0
				for _, user := range billable.Users {
					billingCount++
					if seeded[user.UserID] {
						billing = append(billing, string(user.UserID)+":"+strconv.FormatBool(user.BillingActive))
					}
				}
				sort.Strings(billing)
				// Access is recorded before the analytics window opens so the
				// count it feeds is deterministic.
				if err := chat.RecordAccess(ctx, "T1", "U1", "198.51.100.7", "qualification-agent"); err != nil {
					return nil, err
				}
				// Reading the access back is what gives the record teeth: the
				// write reports nothing, so a dropped field is invisible until
				// somebody asks for the log.
				logs, _, err := chat.ListAccessLogs(ctx, "T1", "U1", time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC), 10, 1)
				if err != nil {
					return nil, err
				}
				// The Unix epoch is a real instant, not an absent one: asking
				// for accesses before it must answer with none. It used to
				// answer none locally and everything remotely, because the
				// seam encoded the instant as a bare int64 whose zero also
				// meant "no filter".
				beforeEpoch, _, err := chat.ListAccessLogs(ctx, "T1", "U1", time.Unix(0, 0).UTC(), 10, 1)
				if err != nil {
					return nil, err
				}
				accesses := make([]string, 0, len(logs))
				for _, entry := range logs {
					accesses = append(accesses, strings.Join([]string{string(entry.UserID), entry.IP, entry.UserAgent}, "|"))
				}
				sort.Strings(accesses)
				analytics, err := chat.WorkspaceAnalytics(ctx, "T1", "U1", time.Unix(0, 0).UTC())
				if err != nil {
					return nil, err
				}
				workspaces, err := chat.UserWorkspaces(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				held := make([]string, 0, len(workspaces))
				for _, summary := range workspaces {
					held = append(held, string(summary.Workspace.ID)+":"+string(summary.Role))
				}
				sort.Strings(held)
				// Resetting sessions and removing the account are the two ways
				// an administrator ends someone's access, and they are not the
				// same: the first leaves the member in the workspace.
				if err := chat.ResetUserSessions(ctx, "T1", "U1", member.ID); err != nil {
					return nil, err
				}
				stillHere, err := chat.UserInfo(ctx, "T1", "U1", member.ID)
				if err != nil {
					return nil, err
				}
				if err := chat.RemoveUser(ctx, "T1", "U1", member.ID); err != nil {
					return nil, err
				}
				// A removed account is gone from the directory rather than
				// returned as a deleted row, so the projection holds the
				// refusal instead of a user.
				_, removedErr := chat.UserInfo(ctx, "T1", "U1", member.ID)
				denied, err := chat.AdminListInviteRequests(ctx, "T1", "U1", domain.InviteRequestDenied, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				deniedEmails := make([]string, 0, len(denied.Requests))
				for _, request := range denied.Requests {
					deniedEmails = append(deniedEmails, request.Email)
				}
				return []any{
					created.Domain, created.Name, created.Description, string(created.Discoverability),
					member.Email, member.RealName,
					preview.Email, preview.RealName, string(preview.Status),
					accepted.Email, accepted.Name != "",
					emails, billing, billingCount, deniedEmails, accesses, len(beforeEpoch),
					analytics.Members, analytics.Admins, analytics.PublicChannels,
					analytics.ArchivedChannels, analytics.Messages >= 0,
					held, stillHere.Deleted,
					removedErr != nil, errors.Is(removedErr, storepkg.ErrNotFound),
				}, nil
			},
		},
		{
			// Connecting organizations to a channel and taking them off it are
			// the administrative half of Slack Connect, separate from the
			// invitation lifecycle: an administrator can attach a team without
			// an invitation ever existing, and disconnecting is not the same as
			// revoking one.
			name: "connected channel administration agrees across the seam",
			seed: seedConnectParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// A conversation may only be associated with the acting
				// workspace: this deployment holds no organization edge, so a
				// foreign team is refused rather than written.
				foreignErr := chat.AdminSetConversationTeams(ctx, "T1", "U1", "C-accept", []domain.WorkspaceID{"T1", "T2"}, false)
				if err := chat.AdminSetConversationTeams(ctx, "T1", "U1", "C-accept", []domain.WorkspaceID{"T1"}, false); err != nil {
					return nil, err
				}
				// The second organization arrives the way Slack Connect puts it
				// there, by accepting an invitation, which is the only route
				// that attaches one.
				invite, err := chat.InviteShared(ctx, "T1", "U1", "C-accept", "T2", "")
				if err != nil {
					return nil, err
				}
				if _, err := chat.ApproveSharedInvite(ctx, "T1", "U1", invite.ID); err != nil {
					return nil, err
				}
				if _, err := chat.AcceptSharedInvite(ctx, "T2", "U2-second", invite.ID); err != nil {
					return nil, err
				}
				teams, hasMore, _, err := chat.AdminConversationTeams(ctx, "T1", "U1", "C-accept", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				attached := make([]string, 0, len(teams))
				for _, team := range teams {
					attached = append(attached, string(team))
				}
				sort.Strings(attached)
				infos, infoMore, _, err := chat.AdminConnectedChannelInfo(ctx, "T1", "U1", []domain.ConversationID{"C-accept"}, nil, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				connected := make([]string, 0, len(infos))
				for _, info := range infos {
					internal := make([]string, 0, len(info.InternalTeamIDs))
					for _, team := range info.InternalTeamIDs {
						internal = append(internal, string(team))
					}
					sort.Strings(internal)
					connected = append(connected, string(info.ChannelID)+"="+strings.Join(internal, ","))
				}
				sort.Strings(connected)
				if err := chat.AdminDisconnectSharedConversation(ctx, "T1", "U1", "C-accept", []domain.WorkspaceID{"T2"}); err != nil {
					return nil, err
				}
				remaining, _, _, err := chat.AdminConversationTeams(ctx, "T1", "U1", "C-accept", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				left := make([]string, 0, len(remaining))
				for _, team := range remaining {
					left = append(left, string(team))
				}
				sort.Strings(left)
				return []any{
					foreignErr != nil, errors.Is(foreignErr, service.ErrInvalidConversation),
					attached, hasMore, connected, infoMore, left,
				}, nil
			},
		},
		{
			// An app's presence in a workspace is three separate facts —
			// installed, approved, and holding a credential — and the methods
			// that report them are different. The case walks the whole arc so a
			// composition that lost one of the three is caught rather than one
			// that merely answered each call.
			name: "app installation and approval agree across the seam",
			seed: seedViewParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				installed, err := chat.ListWorkspaceApps(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(installed))
				for _, app := range installed {
					names = append(names, string(app.ID)+":"+app.Name+":"+strconv.FormatBool(app.HomeTabEnabled))
				}
				sort.Strings(names)
				// A second installation is a different workspace's decision,
				// so it must not appear in this one's listing.
				if err := chat.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T2-absent", Enabled: true, CreatedAt: time.Unix(1_700_000_800, 0).UTC()}); err != nil {
					return nil, err
				}
				installations, err := chat.ListAppInstallations(ctx, "A1")
				if err != nil {
					return nil, err
				}
				workspaces := make([]string, 0, len(installations))
				for _, installation := range installations {
					workspaces = append(workspaces, string(installation.WorkspaceID)+":"+strconv.FormatBool(installation.Enabled))
				}
				sort.Strings(workspaces)
				if err := chat.AdminApproveApp(ctx, "T1", "U1", "A1", "R-1"); err != nil {
					return nil, err
				}
				approved, err := chat.AdminListApps(ctx, "T1", "U1", domain.AppApprovalApproved, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				// The approval is named, not just counted: a request that lost
				// its app id is stored under a synthesised key, which a count
				// alone cannot tell from the real thing.
				approvedIDs := make([]string, 0, len(approved.Apps))
				for _, approval := range approved.Apps {
					approvedIDs = append(approvedIDs, string(approval.ID)+"/"+string(approval.RequestID))
				}
				sort.Strings(approvedIDs)
				// Requesting scopes is a member asking an administrator for
				// something, which is recorded rather than granted.
				permissionErr := chat.RequestAppPermissions(ctx, "T1", "U1", "U2", []string{"channels:read"}, "trigger_replay")
				// Restricting uninstalls the app, so what it did is read back
				// from the installation rather than from the approval alone.
				if err := chat.AdminRestrictApp(ctx, "T1", "U1", "A1", "R-1"); err != nil {
					return nil, err
				}
				afterRestriction, err := chat.ListWorkspaceApps(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				restricted, err := chat.AdminListApps(ctx, "T1", "U1", domain.AppApprovalRestricted, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				restrictedIDs := make([]string, 0, len(restricted.Apps))
				for _, approval := range restricted.Apps {
					restrictedIDs = append(restrictedIDs, string(approval.ID)+":"+string(approval.Status))
				}
				sort.Strings(restrictedIDs)
				// Uninstalling an app that restriction already removed is
				// refused, and both compositions must refuse it alike.
				uninstallErr := chat.UninstallApp(ctx, "view-client", "client-hash", "T1", "A1")
				_, tokenErr := chat.LookupAppToken(ctx, "xapp-absent")
				return []any{
					names, workspaces, approvedIDs, restrictedIDs,
					len(afterRestriction), permissionErr == nil,
					uninstallErr != nil, tokenErr != nil,
					errors.Is(tokenErr, storepkg.ErrNotFound),
				}, nil
			},
		},
		{
			// A workflow step is finished by the app that ran it, and the three
			// ways it can end — configured, completed, failed — are separate
			// methods writing separate durable states. seedWorkflowParity
			// leaves one step executing, which is the state all three act on.
			name: "workflow step completion agrees across the seam",
			seed: seedWorkflowParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// Configuring is keyed by the edit identifier the builder holds,
				// not by the execution identifier the runtime holds; they are
				// different names for different moments and a case that used one
				// for both would pass while the seam confused them.
				if err := chat.WorkflowUpdateStep(ctx, "T1", "U1", "triage", `{"item":{"value":"request"}}`, `[{"name":"result","type":"text"}]`, "Triage request", "https://example.test/icon.png"); err != nil {
					return nil, err
				}
				// No seam method returns a stored workflow step, so what these
				// three write is observable only through the run they move and
				// through whether they were accepted at all. That bounds this
				// case honestly: dropping an identifier is caught, dropping a
				// payload field is not, because nothing can read the payload
				// back. The product gap audit records that separately — an app
				// completes a step with outputs and no method reports them.
				configured, err := chat.GetWorkflowRun(ctx, "T1", "U1", "WxParity")
				if err != nil {
					return nil, err
				}
				completeErr := chat.WorkflowStepCompleted(ctx, "T1", "U1", "FxParity", `{"result":"done"}`)
				completed, err := chat.GetWorkflowRun(ctx, "T1", "U1", "WxParity")
				if err != nil {
					return nil, err
				}
				// A step that has already ended cannot end again, and a failure
				// payload that is not an object is refused rather than stored.
				repeatErr := chat.WorkflowStepCompleted(ctx, "T1", "U1", "FxParity", `{"result":"again"}`)
				malformedErr := chat.WorkflowStepFailed(ctx, "T1", "U1", "FxParity", `not-json`)
				return []any{
					string(configured.Status), configured.Inputs,
					completeErr == nil, string(completed.Status), completed.Outputs,
					repeatErr != nil, malformedErr != nil,
					errors.Is(malformedErr, service.ErrInvalidWorkflowStep),
				}, nil
			},
		},
		{
			// The three entity surfaces store nothing: they validate what an app
			// hands back and answer yes or no. What the seam has to agree on is
			// therefore the decision itself, so the case drives one accepted
			// shape and every rejected one each method declares.
			name: "entity presentation validates identically across the seam",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				detailsOK := chat.PresentEntityDetails(ctx, "T1", "U1", "trigger", `{"title":"Item"}`, false, "", "")
				detailsNoTrigger := chat.PresentEntityDetails(ctx, "T1", "U1", "  ", `{"title":"Item"}`, false, "", "")
				detailsBadMetadata := chat.PresentEntityDetails(ctx, "T1", "U1", "trigger", `["not","an","object"]`, false, "", "")
				// Declaring that the member must authenticate without saying
				// where is a promise with nowhere to send them.
				detailsNoAuthURL := chat.PresentEntityDetails(ctx, "T1", "U1", "trigger", `{"title":"Item"}`, true, "", "")
				commentsOK := chat.PresentEntityComments(ctx, "T1", "U1", "trigger", `[{"id":"c1","text":"hello"}]`, "", true, "delete", false, "", "")
				commentsEmpty := chat.PresentEntityComments(ctx, "T1", "U1", "trigger", "", "", true, "delete", false, "", "")
				commentsNotArray := chat.PresentEntityComments(ctx, "T1", "U1", "trigger", `{"id":"c1"}`, "", true, "delete", false, "", "")
				acknowledgeOK := chat.AcknowledgeEntityCommentAction(ctx, "T1", "U1", "trigger", `{"id":"c1"}`, "")
				acknowledgeBad := chat.AcknowledgeEntityCommentAction(ctx, "T1", "U1", "trigger", `[1,2,3]`, "")
				classify := func(err error) string {
					switch {
					case err == nil:
						return "ok"
					case errors.Is(err, service.ErrInvalidEntity):
						return "invalid_entity"
					default:
						return "other:" + err.Error()
					}
				}
				return []any{
					classify(detailsOK), classify(detailsNoTrigger), classify(detailsBadMetadata), classify(detailsNoAuthURL),
					classify(commentsOK), classify(commentsEmpty), classify(commentsNotArray),
					classify(acknowledgeOK), classify(acknowledgeBad),
				}, nil
			},
		},
		{
			// External uploads, public links, canvases, bots and unfurls have
			// nothing in common except that each is a durable object a message
			// or a member points at, and each was uncovered. They share a case
			// because they share a fixture, not because they share a story.
			name:  "durable attachments agree across the seam",
			blobs: true,
			seed:  seedWebhookParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// An external upload is a ticket, bytes, then a completion that
				// turns both into a shared file. The completion is the seam
				// method under test; the first two set it up.
				upload, err := chat.CreateExternalUpload(ctx, "T1", "U1", "report.txt", "text/plain", 6, time.Minute)
				if err != nil {
					return nil, err
				}
				if err := chat.UploadExternalFile(ctx, upload.ID, 6, bytes.NewReader([]byte("report"))); err != nil {
					return nil, err
				}
				files, err := chat.CompleteExternalUploads(ctx, "T1", "U1",
					[]domain.ExternalUploadCompletion{{ID: upload.ID, Title: "Quarterly report"}},
					[]domain.ConversationID{"C1"}, "Here it is", "", "")
				if err != nil {
					return nil, err
				}
				completed := make([]string, 0, len(files))
				for _, file := range files {
					completed = append(completed, file.Name+"|"+file.Title+"|"+strconv.FormatInt(file.Size, 10))
				}
				sort.Strings(completed)
				// A public link is a token that anyone holding it may read, so
				// the case reads the bytes back through it rather than trusting
				// the token to exist.
				shared, err := chat.ShareFilePublic(ctx, "T1", "U1", files[0].ID)
				if err != nil {
					return nil, err
				}
				public, reader, err := chat.OpenPublicFile(ctx, shared.PublicToken)
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				content, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				revoked, err := chat.RevokeFilePublic(ctx, "T1", "U1", files[0].ID)
				if err != nil {
					return nil, err
				}
				_, _, revokedErr := chat.OpenPublicFile(ctx, shared.PublicToken)

				canvas, err := chat.CreateCanvas(ctx, "T1", "U1", "Runbook", `{"type":"markdown","markdown":"# Runbook\n\nStep one."}`, "")
				if err != nil {
					return nil, err
				}
				sections, err := chat.LookupCanvasSections(ctx, "T1", "U1", canvas.ID, `{"section_types":["h1"]}`)
				if err != nil {
					return nil, err
				}
				found := make([]string, 0, len(sections))
				for _, section := range sections {
					found = append(found, string(section.Type)+":"+section.Text)
				}
				sort.Strings(found)
				if err := chat.DeleteCanvas(ctx, "T1", "U1", canvas.ID); err != nil {
					return nil, err
				}
				_, deletedErr := chat.Canvas(ctx, "T1", "U1", canvas.ID)

				bot, err := chat.BotInfo(ctx, "T1", "U1", "Bhook")
				if err != nil {
					return nil, err
				}
				// An unfurl attaches link previews to a message that already
				// exists, so it is read back from the message rather than from
				// the call's own answer.
				posted, err := chat.Post(ctx, "T1", "U1", "C1", "see https://example.test/page", "", "")
				if err != nil {
					return nil, err
				}
				timestamp := domain.NewMessageTimestamp(posted.CreatedAt)
				unfurled, err := chat.Unfurl(ctx, "T1", "U1", "C1", timestamp, map[string]string{
					"https://example.test/page": `{"title":"A page","text":"Preview"}`,
				})
				if err != nil {
					return nil, err
				}
				previews := make([]string, 0, len(unfurled.Unfurls))
				for link, preview := range unfurled.Unfurls {
					previews = append(previews, link+"="+preview)
				}
				sort.Strings(previews)
				return []any{
					completed, upload.Name,
					public.Name, public.Title, string(content),
					shared.PublicToken != "", revoked.PublicToken,
					revokedErr != nil, errors.Is(revokedErr, storepkg.ErrNotFound),
					canvas.Title, found, deletedErr != nil,
					string(bot.ID), bot.Name, string(bot.AppID), string(bot.UserID),
					previews,
				}, nil
			},
		},
		{
			// Credentials are the one family where a seam disagreement is a
			// security answer rather than a display one: a session or token the
			// two compositions disagree about is one that is live in a
			// deployment that believes it revoked it. The case therefore reads
			// every credential back after each write, and again after revoking.
			name: "sessions, tokens and external identities agree across the seam",
			seed: func(t *testing.T, target *memory.Store) {
				seedBaseline(t, target)
				requireSeed(t, seedWorkspaceRole(target, "T1", "U1", domain.WorkspaceRoleAdmin))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
				if err := chat.CreateSession(ctx, "session-parity", domain.SessionRecord{
					WorkspaceID: "T1", UserID: "U1", Scopes: []string{"channels:history", "chat:write"},
					CreatedAt: expiry.Add(-time.Hour), ExpiresAt: expiry,
					OIDCProvider: "shauth", OIDCSubject: "subject-1", OIDCSID: "sid-1",
				}); err != nil {
					return nil, err
				}
				opened, err := chat.LookupSession(ctx, "session-parity")
				if err != nil {
					return nil, err
				}
				listed, err := chat.UserSessions(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				if err := chat.RevokeSession(ctx, "session-parity"); err != nil {
					return nil, err
				}
				afterRevoke, revokeErr := chat.LookupSession(ctx, "session-parity")

				// A token is revoked by value, and the record must say so
				// rather than disappear: a caller has to be able to tell a
				// revoked credential from one that never existed.
				tokenErr := chat.RevokeToken(ctx, "token")
				revokedToken, tokenLookupErr := chat.Tokens.LookupToken(ctx, "token")

				if err := chat.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "shauth", Enabled: true}); err != nil {
					return nil, err
				}
				method, err := chat.GetAuthMethod(ctx, "T1", "shauth")
				if err != nil {
					return nil, err
				}
				if err := chat.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "shauth", Enabled: false}); err != nil {
					return nil, err
				}
				disabled, err := chat.GetAuthMethod(ctx, "T1", "shauth")
				if err != nil {
					return nil, err
				}
				if err := chat.CreateExternalIdentity(ctx, domain.ExternalIdentity{
					WorkspaceID: "T1", Provider: "shauth", Subject: "subject-1", UserID: "U1",
				}); err != nil {
					return nil, err
				}
				identity, err := chat.GetExternalIdentity(ctx, "T1", "shauth", "subject-1")
				if err != nil {
					return nil, err
				}
				_, missingIdentityErr := chat.GetExternalIdentity(ctx, "T1", "shauth", "subject-absent")

				// A migration exchange maps this workspace's member ids onto
				// their global ones and names the ones it could not, which is
				// the half a caller acts on.
				exchange, err := chat.MigrationExchange(ctx, "T1", "U1", []domain.UserID{"U1", "U-absent"}, false)
				if err != nil {
					return nil, err
				}
				mapped := make([]string, 0, len(exchange.UserIDMap))
				for from, to := range exchange.UserIDMap {
					mapped = append(mapped, string(from)+"->"+strconv.FormatBool(to != ""))
				}
				sort.Strings(mapped)
				invalid := make([]string, 0, len(exchange.InvalidUserIDs))
				for _, id := range exchange.InvalidUserIDs {
					invalid = append(invalid, string(id))
				}
				sort.Strings(invalid)
				return []any{
					opened.WorkspaceID, opened.UserID, len(opened.Scopes),
					opened.ExpiresAt.UTC().Equal(expiry), opened.OIDCProvider, opened.OIDCSubject, opened.OIDCSID,
					len(listed) > 0,
					afterRevoke.Revoked, revokeErr != nil,
					tokenErr == nil, revokedToken.Revoked, tokenLookupErr != nil,
					method.Provider, method.Enabled, disabled.Enabled,
					string(identity.UserID), identity.Provider, identity.Subject,
					missingIdentityErr != nil, errors.Is(missingIdentityErr, storepkg.ErrNotFound),
					string(exchange.WorkspaceID), mapped, invalid,
				}, nil
			},
		},
		{
			// The dispatch family is the only one that leaves this process: each
			// method posts to the app and turns what comes back into durable
			// state. Both compositions dial their own receiver running the same
			// handler, so what they compare is what each stored — a difference
			// cannot come from the app.
			name:        "app dispatch agrees across the seam",
			seedWithApp: seedDispatchParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				slashErr := chat.DispatchSlashCommand(ctx, "T1", "U1", "C1", "", "/deploy", "production", "https://chat.example.test")
				// A slash command in a thread is refused before the app is
				// reached, which both compositions must decide alike.
				threadErr := chat.DispatchSlashCommand(ctx, "T1", "U1", "C1", "1700000000.000100", "/deploy", "production", "https://chat.example.test")
				unknownErr := chat.DispatchSlashCommand(ctx, "T1", "U1", "C1", "", "/nothing", "", "https://chat.example.test")

				posted, err := chat.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Deployment",
					`[{"type":"actions","block_id":"deployment","elements":[{"type":"button","action_id":"go","text":{"type":"plain_text","text":"Go"},"value":"prod"}]}]`,
					"", "", "", "A1")
				if err != nil {
					return nil, err
				}
				actionErr := chat.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
					MessageID: posted.ID, BlockID: "deployment", ActionID: "go", Type: "button", Value: "prod",
				}, "https://chat.example.test")

				view, err := chat.OpenView(ctx, "T1", "UBOT", "A1", "trigger_dispatch",
					`{"type":"modal","callback_id":"deploy","title":{"type":"plain_text","text":"Deploy"},"blocks":[{"type":"actions","block_id":"env","elements":[{"type":"external_select","action_id":"pick","placeholder":{"type":"plain_text","text":"Environment"}}]},{"type":"actions","block_id":"go","elements":[{"type":"button","action_id":"confirm","text":{"type":"plain_text","text":"Confirm"},"value":"x"}]}]}`)
				if err != nil {
					return nil, err
				}
				viewActionErr := chat.DispatchViewBlockAction(ctx, "T1", "U1", "C1", domain.AppViewBlockAction{
					ViewID: view.ID, BlockID: "go", ActionID: "confirm", Type: "button", Value: "x", State: `{}`,
				}, "https://chat.example.test")

				options, optionsErr := chat.LoadAppOptions(ctx, "T1", "U1", "C1", domain.AppOptionQuery{
					AppID: "A1", ViewID: view.ID, BlockID: "env", ActionID: "pick", Value: "pro",
				}, "https://chat.example.test")
				loaded := make([]string, 0, len(options))
				for _, option := range options {
					loaded = append(loaded, option.Text+"="+option.Value)
				}
				sort.Strings(loaded)

				// The response URL was seeded, because a real one is handed to
				// the app over HTTP where this case cannot read it.
				responseErr := chat.HandleAppResponse(ctx, "response_dispatch", `{"text":"from the app"}`)
				spentErr := chat.HandleAppResponse(ctx, "response-absent", `{"text":"nobody"}`)
				page, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 20})
				if err != nil {
					return nil, err
				}
				texts := make([]string, 0, len(page.Messages))
				for _, message := range page.Messages {
					texts = append(texts, message.Text)
				}
				sort.Strings(texts)
				return []any{
					slashErr == nil, threadErr != nil, errors.Is(threadErr, service.ErrSlashCommandInThread),
					unknownErr != nil, errors.Is(unknownErr, service.ErrSlashCommandNotFound),
					actionErr == nil, viewActionErr == nil,
					optionsErr == nil, loaded,
					responseErr == nil, spentErr != nil,
					errors.Is(spentErr, service.ErrInvalidAppResponse), texts,
				}, nil
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
				// Deleting the photo must clear every rendition on the profile,
				// not just the one the member happened to be looking at, so the
				// projection reads them all back rather than the largest.
				if err := chat.DeleteUserPhoto(ctx, "T1", "U1"); err != nil {
					return nil, err
				}
				cleared, err := chat.UserInfo(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				return []any{
					opened.ID, opened.WorkspaceID, opened.Email, opened.Name, opened.RealName,
					opened.Presence, opened.Deleted, opened.Profile.DisplayName,
					opened.Profile.StatusText, opened.Profile.StatusEmoji,
					bytes.Equal(content, photo),
					cleared.Profile.Image24, cleared.Profile.Image32, cleared.Profile.Image48,
					cleared.Profile.Image72, cleared.Profile.Image192, cleared.Profile.Image512,
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
				// Removal is the half of the lifecycle the case used to stop
				// short of, so a bookmark that survived deletion on one
				// composition and not the other read as agreement.
				if err := chat.RemoveBookmark(ctx, "T1", "U1", "C1", bookmark.ID); err != nil {
					return nil, err
				}
				remaining, err := chat.Bookmarks(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				return []any{bookmark.Title, bookmark.Link, bookmark.Emoji, edited.Title, titles, len(remaining)}, nil
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
			// Assistant state is written a field at a time, which is the part
			// the two compositions can disagree about: a whole-record write
			// would clear the fields the caller left empty, and only setting
			// one field then reading all three catches it.
			name: "assistant thread state is written one field at a time",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				root, err := chat.Post(ctx, "T1", "U1", "C1", "assistant root", "", "")
				if err != nil {
					return nil, err
				}
				thread := timestampOf(root)
				if err := chat.SetAssistantThreadTitle(ctx, "T1", "U1", "C1", thread, "Deploy help"); err != nil {
					return nil, err
				}
				if err := chat.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking..."); err != nil {
					return nil, err
				}
				if err := chat.SetAssistantThreadSuggestedPrompts(ctx, "T1", "U1", "C1", thread, "Try", []domain.AssistantPrompt{{Title: "Roll back", Message: "How do I roll back?"}}); err != nil {
					return nil, err
				}
				value, err := chat.AssistantThread(ctx, "T1", "U1", "C1", thread)
				if err != nil {
					return nil, err
				}
				// Clearing the status must leave the title and prompts alone.
				if err := chat.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, ""); err != nil {
					return nil, err
				}
				after, err := chat.AssistantThread(ctx, "T1", "U1", "C1", thread)
				if err != nil {
					return nil, err
				}
				return []any{value.Title, value.Status, value.PromptsTitle, len(value.Prompts), after.Title, after.Status, len(after.Prompts)}, nil
			},
		},
		{
			// Declaring a column is additive by construction — no item has a
			// value under a column that did not exist — and the compositions
			// have to agree on the key it mints, because every cell references
			// it. Two columns with the same name are survivable rather than a
			// collision, which is the part a single-composition test would miss.
			name: "a declared column is appended with a stable key",
			seed: seedListAssignmentParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				const listID = domain.ListID("Lx-assign")
				first, err := chat.AddListColumn(ctx, "T1", "U1", listID, "Due date", domain.ListColumnDate, nil)
				if err != nil {
					return nil, err
				}
				second, err := chat.AddListColumn(ctx, "T1", "U1", listID, "Due date", domain.ListColumnText, nil)
				if err != nil {
					return nil, err
				}
				columns, err := domain.ParseListSchema(second.Schema)
				if err != nil {
					return nil, err
				}
				keys := make([]string, 0, len(columns))
				for _, column := range columns {
					keys = append(keys, column.Key+":"+string(column.Type))
				}
				// A select with no options is a text column that refuses every
				// value, so it is refused rather than created.
				_, emptySelectErr := chat.AddListColumn(ctx, "T1", "U1", listID, "Status", domain.ListColumnSelect, nil)
				// The item that existed before still conforms: declaring a
				// column cannot invalidate what was already written.
				item, err := chat.GetListItem(ctx, "T1", "U1", listID, "Li-assign")
				if err != nil {
					return nil, err
				}
				if _, err := chat.UpdateListItem(ctx, "T1", "U1", listID, item.ID, item.Fields, false); err != nil {
					return nil, err
				}
				return []any{keys, first.Version, second.Version, emptySelectErr != nil}, nil
			},
		},
		{
			// Assignment is where a list stops being a document and becomes
			// work, so the compositions have to agree on who may receive it and
			// on the news reaching them. U2 can read the list; U3 cannot, and
			// assigning to someone who cannot open where the work lives would
			// produce an item they are told about and cannot reach.
			name: "a list item is assigned, told, and refused to someone who cannot see the list",
			seed: seedListAssignmentParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				const listID = domain.ListID("Lx-assign")
				const itemID = domain.ListItemID("Li-assign")
				due := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
				assigned, err := chat.AssignListItem(ctx, "T1", "U1", listID, itemID, "U2", due)
				if err != nil {
					return nil, err
				}
				told, err := chat.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
				if err != nil {
					return nil, err
				}
				strangerErr := chat.assignExpectingFailure(ctx, listID, itemID)
				// The picker asks the same question the write enforces, so the
				// two compositions must agree about who may be offered.
				readerAccess := chat.ListAccessFor(ctx, "T1", "U2", listID) == nil
				strangerAccess := chat.ListAccessFor(ctx, "T1", "U3", listID) == nil
				// Clearing is how a mistaken assignment is undone, and it must
				// not manufacture a second piece of news.
				cleared, err := chat.AssignListItem(ctx, "T1", "U1", listID, itemID, "", time.Time{})
				if err != nil {
					return nil, err
				}
				after, err := chat.Activity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
				if err != nil {
					return nil, err
				}
				return []any{
					string(assigned.AssigneeID), assigned.DueAt.UTC().Format(time.RFC3339), len(told.Items),
					told.Items[0].ListItemID, told.Items[0].SourceAvailable, told.Items[0].ListName,
					strangerErr, string(cleared.AssigneeID), cleared.DueAt.IsZero(), len(after.Items),
					readerAccess, strangerAccess,
				}, nil
			},
		},
		{
			// A schedule is set on its own form and the rest of the preferences
			// on another, so the compositions have to agree that neither
			// silently undoes the other — the setter builds a whole record from
			// its arguments and the schedule is not one of them.
			name: "a notification schedule survives an unrelated preference write",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				schedule := domain.NotificationSchedule{
					Enabled: true, Days: []time.Weekday{time.Monday, time.Friday},
					StartMinute: 9 * 60, EndMinute: 18 * 60, TimeZone: "Europe/Berlin",
				}
				if _, err := chat.SetNotificationSchedule(ctx, "T1", "U1", schedule); err != nil {
					return nil, err
				}
				after, err := chat.SetWorkspaceNotificationPreferences(ctx, "T1", "U1", domain.NotificationAll, []string{"deploy"}, true, true, true)
				if err != nil {
					return nil, err
				}
				// An invalid schedule is refused rather than stored, and an
				// empty window is the commonest way to write one by accident.
				_, invalidErr := chat.SetNotificationSchedule(ctx, "T1", "U1", domain.NotificationSchedule{
					Enabled: true, Days: []time.Weekday{time.Monday}, StartMinute: 9 * 60, EndMinute: 9 * 60, TimeZone: "UTC",
				})
				read, err := chat.WorkspaceNotificationPreferences(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				return []any{after.Schedule.Enabled, after.Schedule.Days, after.Schedule.StartMinute, after.Schedule.EndMinute, after.Schedule.TimeZone, invalidErr != nil, read.Schedule.Enabled}, nil
			},
		},
		{
			// A description is the uploader's account of their own file, so the
			// permission is the interesting part: both compositions have to
			// agree that someone else's attempt fails rather than silently
			// writing nothing, and that clearing one is allowed because it is
			// the only way to correct a description that was wrong.
			name: "a file description is the uploader's to write and to clear",
			seed: seedFileParity,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				const fileID = domain.FileID("Fparity-description")
				if err := chat.SetFileDescription(ctx, "T1", "U1", fileID, "A box diagram of the seam"); err != nil {
					return nil, err
				}
				described, err := chat.FileInfo(ctx, "T1", "U1", fileID)
				if err != nil {
					return nil, err
				}
				strangerErr := chat.SetFileDescription(ctx, "T1", "U2", fileID, "not mine to write")
				unchanged, err := chat.FileInfo(ctx, "T1", "U1", fileID)
				if err != nil {
					return nil, err
				}
				if err := chat.SetFileDescription(ctx, "T1", "U1", fileID, ""); err != nil {
					return nil, err
				}
				cleared, err := chat.FileInfo(ctx, "T1", "U1", fileID)
				if err != nil {
					return nil, err
				}
				return []any{described.Description, strangerErr != nil, unchanged.Description, cleared.Description, described.IsImage()}, nil
			},
		},
		{
			// People and Channels used to be answered by filtering a directory
			// the client had already loaded whole, so nothing crossed the seam
			// and nothing could disagree. Now that both are store questions,
			// the compositions have to agree on the fold, on the page, and — for
			// channels — on the visibility rule, which is the sidebar's: C2 is
			// public, and U2 is not a member of it.
			name: "people and channel search fold and respect visibility",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				page := domain.PageRequest{Limit: 10}
				people, err := chat.SearchPeople(ctx, "T1", "U1", "BOB", page)
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(people.Users))
				for _, user := range people.Users {
					names = append(names, string(user.ID))
				}
				missing, err := chat.SearchPeople(ctx, "T1", "U1", "nobody-by-that-name", page)
				if err != nil {
					return nil, err
				}
				channels, err := chat.SearchChannels(ctx, "T1", "U1", "SECOND", page)
				if err != nil {
					return nil, err
				}
				found := make([]string, 0, len(channels.Conversations))
				for _, conversation := range channels.Conversations {
					found = append(found, string(conversation.ID))
				}
				// A blank query is refused rather than treated as "everything":
				// a search surface that answers an empty question with the whole
				// directory is a directory, not a search.
				_, blankErr := chat.SearchChannels(ctx, "T1", "U1", "  ", page)
				return []any{names, len(missing.Users), found, blankErr != nil}, nil
			},
		},
		{
			// Sessions are what an administrator reviews before deciding to end
			// one, and the review must not hand out the credential it is
			// describing. Both compositions have to agree on that and on who
			// may ask.
			name: "sessions are listed for an administrator without their tokens and can be ended in bulk",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				before, err := chat.UserSessions(ctx, "T1", "UA", "U1")
				if err != nil {
					return nil, err
				}
				identifiers := make([]string, 0, len(before))
				for _, session := range before {
					identifiers = append(identifiers, string(session.UserID)+":"+strconv.Itoa(len(session.ID)))
				}
				// A member cannot read another member's sessions, nor end them.
				_, memberErr := chat.UserSessions(ctx, "T1", "U1", "U1")
				memberBulk := chat.ResetUserSessionsBulk(ctx, "T1", "U1", []domain.UserID{"U1"}) != nil
				// A stranger in the list stops the whole request, so an
				// administrator acting on a pasted list finds out they were
				// wrong instead of signing out an arbitrary prefix of it.
				mixed := chat.ResetUserSessionsBulk(ctx, "T1", "UA", []domain.UserID{"U1", "U-not-here"}) != nil
				empty := chat.ResetUserSessionsBulk(ctx, "T1", "UA", nil) != nil
				bulk := chat.ResetUserSessionsBulk(ctx, "T1", "UA", []domain.UserID{"U1", "U2"})
				after, err := chat.UserSessions(ctx, "T1", "UA", "U1")
				if err != nil {
					return nil, err
				}
				return []any{identifiers, memberErr != nil, memberBulk, mixed, empty, bulk == nil, len(after)}, nil
			},
		},
		{
			// A connection is derived from the channels that carry it, and
			// ending one has to end it everywhere. Both compositions must
			// agree, because an administrator told an organization is
			// disconnected while one channel still carries it has been told
			// something false about who can read their messages.
			name: "external connections are listed for an administrator and ended everywhere at once",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				before, err := chat.ExternalTeams(ctx, "T1", "UA", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				listed := make([]string, 0, len(before.Teams))
				for _, team := range before.Teams {
					listed = append(listed, string(team.ID)+":"+strconv.Itoa(team.Channels))
				}
				// A member who is not an administrator cannot ask at all.
				_, memberErr := chat.ExternalTeams(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				// An organization nothing is shared with cannot be
				// disconnected: saying otherwise would report ending a
				// connection that never existed. Neither can the workspace
				// disconnect itself.
				absent := chat.DisconnectExternalTeam(ctx, "T1", "UA", "T-never-connected") != nil
				itself := chat.DisconnectExternalTeam(ctx, "T1", "UA", "T1") != nil
				return []any{listed, memberErr != nil, absent, itself}, nil
			},
		},
		{
			// Removing a column has to take the values under it with it, and
			// has to refuse the primary column. Both compositions must agree,
			// because a schema and its cells disagreeing is a list nobody can
			// read correctly.
			name: "removing a list column takes its cells and spares the primary column",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				list, err := chat.CreateList(ctx, "T1", "U1", "Launch", "", "[]", "", false, false)
				if err != nil {
					return nil, err
				}
				if _, err := chat.AddListColumn(ctx, "T1", "U1", list.ID, "Task", domain.ListColumnText, nil); err != nil {
					return nil, err
				}
				if _, err := chat.AddListColumn(ctx, "T1", "U1", list.ID, "Status", domain.ListColumnSelect, []string{"open", "done"}); err != nil {
					return nil, err
				}
				item, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"task","value":"ship it"},{"column_id":"status","value":"open"}]`)
				if err != nil {
					return nil, err
				}
				// The primary column is what an item is called, so it stays.
				primaryRefused := func() bool {
					_, err := chat.RemoveListColumn(ctx, "T1", "U1", list.ID, "task")
					return err != nil
				}()
				absentRefused := func() bool {
					_, err := chat.RemoveListColumn(ctx, "T1", "U1", list.ID, "nothing-called-this")
					return err != nil
				}()
				after, err := chat.RemoveListColumn(ctx, "T1", "U1", list.ID, "status")
				if err != nil {
					return nil, err
				}
				stripped, err := chat.GetListItem(ctx, "T1", "U1", list.ID, item.ID)
				if err != nil {
					return nil, err
				}
				return []any{after.Schema, stripped.Fields, primaryRefused, absentRefused}, nil
			},
		},
		{
			// A list carries the same grant model as a canvas, so it has to
			// answer the same pair: a reader sees who it is shared with, and
			// only the owner changes that. Both compositions have to agree,
			// including on the order — a sharing list that reshuffled between
			// page loads would make a member doubt what they just changed.
			name: "list grants are readable by a reader and changeable by the owner",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				created, err := chat.CreateList(ctx, "T1", "U1", "Launch", "", "[]", "", false, true)
				if err != nil {
					return nil, err
				}
				if err := chat.SetListAccess(ctx, "T1", "U1", created.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				if err := chat.SetListAccess(ctx, "T1", "U1", created.ID, "read", []domain.ConversationID{"C1"}, nil); err != nil {
					return nil, err
				}
				grants, err := chat.ListGrants(ctx, "T1", "U2", created.ID)
				if err != nil {
					return nil, err
				}
				listed := make([]string, 0, len(grants))
				for _, grant := range grants {
					listed = append(listed, string(grant.EntityType)+":"+grant.EntityID+":"+string(grant.Access))
				}
				readerGrant := chat.SetListAccess(ctx, "T1", "U2", created.ID, "write", nil, []domain.UserID{"U3"}) != nil
				_, strangerErr := chat.ListGrants(ctx, "T1", "U3", created.ID)
				if err := chat.DeleteListAccess(ctx, "T1", "U1", created.ID, nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				after, err := chat.ListGrants(ctx, "T1", "U1", created.ID)
				if err != nil {
					return nil, err
				}
				return []any{listed, readerGrant, strangerErr != nil, len(after)}, nil
			},
		},
		{
			// Who a canvas is shared with is readable by anyone who may open
			// it, and changeable only by its owner. Both compositions have to
			// agree on that pair, and on the order the grants come back in: a
			// sharing list that reshuffled between page loads would make a
			// member doubt what they just changed.
			name: "canvas grants are readable by a reader and changeable by the owner",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				canvas, err := chat.CreateCanvas(ctx, "T1", "U1", "Shared plan", `{"type":"markdown","markdown":"the plan"}`, "")
				if err != nil {
					return nil, err
				}
				if err := chat.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				if err := chat.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", []domain.ConversationID{"C1"}, nil); err != nil {
					return nil, err
				}
				// A reader may see who else it is shared with.
				grants, err := chat.CanvasGrants(ctx, "T1", "U2", canvas.ID)
				if err != nil {
					return nil, err
				}
				listed := make([]string, 0, len(grants))
				for _, grant := range grants {
					listed = append(listed, string(grant.EntityType)+":"+grant.EntityID+":"+string(grant.Access))
				}
				// A reader may not change them: granting is the strongest
				// operation on a canvas and belongs to its owner.
				readerGrant := chat.SetCanvasAccess(ctx, "T1", "U2", canvas.ID, "write", nil, []domain.UserID{"U3"}) != nil
				// A member with no access at all cannot even ask.
				_, strangerErr := chat.CanvasGrants(ctx, "T1", "U3", canvas.ID)
				if err := chat.DeleteCanvasAccess(ctx, "T1", "U1", canvas.ID, nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				after, err := chat.CanvasGrants(ctx, "T1", "U1", canvas.ID)
				if err != nil {
					return nil, err
				}
				return []any{listed, readerGrant, strangerErr != nil, len(after)}, nil
			},
		},
		{
			// Commenting needs read access rather than write, because a canvas
			// shared for review that only its editors could discuss would make
			// review impossible — and deleting a comment belongs to whoever
			// said it, so an editor cannot delete what others said about their
			// own document. Both compositions have to agree on that pair.
			name: "a canvas comment needs only read access and belongs to its author",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				canvas, err := chat.CreateCanvas(ctx, "T1", "U1", "Under review", `{"type":"markdown","markdown":"the proposal"}`, "")
				if err != nil {
					return nil, err
				}
				if err := chat.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				// A reader may comment even though they may not edit.
				comment, err := chat.CommentOnCanvas(ctx, "T1", "U2", canvas.ID, "s1", "this paragraph is wrong")
				if err != nil {
					return nil, err
				}
				page := domain.PageRequest{Limit: 10}
				seen, err := chat.CanvasComments(ctx, "T1", "U1", canvas.ID, page)
				if err != nil {
					return nil, err
				}
				// The owner cannot delete what somebody else said.
				ownerDelete := chat.DeleteCanvasComment(ctx, "T1", "U1", comment.ID) != nil
				// A member with no grant can neither read nor comment.
				_, strangerErr := chat.CanvasComments(ctx, "T1", "U3", canvas.ID, page)
				if err := chat.DeleteCanvasComment(ctx, "T1", "U2", comment.ID); err != nil {
					return nil, err
				}
				after, err := chat.CanvasComments(ctx, "T1", "U1", canvas.ID, page)
				if err != nil {
					return nil, err
				}
				return []any{
					len(seen.Comments), seen.Comments[0].SectionID, seen.Comments[0].Text,
					string(seen.Comments[0].UserID), ownerDelete, strangerErr != nil, len(after.Comments),
				}, nil
			},
		},
		{
			// The list-item mirror of the canvas-comment rule: read access to the
			// list is enough to leave a comment on a row, and only its author
			// deletes it, so an owner cannot delete what a reader said. Both
			// compositions must agree on that pair.
			name: "a list item comment needs only read access and belongs to its author",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				list, err := chat.CreateList(ctx, "T1", "U1", "Incidents", "[]",
					`[{"key":"title","name":"Title","type":"text"}]`, "", false, false)
				if err != nil {
					return nil, err
				}
				item, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"the outage"}]`)
				if err != nil {
					return nil, err
				}
				if err := chat.SetListAccess(ctx, "T1", "U1", list.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				// A reader may comment even though they may not edit.
				comment, err := chat.CommentOnListItem(ctx, "T1", "U2", list.ID, item.ID, "who owns this")
				if err != nil {
					return nil, err
				}
				page := domain.PageRequest{Limit: 10}
				seen, err := chat.ListItemComments(ctx, "T1", "U1", list.ID, item.ID, page)
				if err != nil {
					return nil, err
				}
				// The owner cannot delete what somebody else said.
				ownerDelete := chat.DeleteListItemComment(ctx, "T1", "U1", comment.ID) != nil
				// A member with no grant can neither read nor comment.
				_, strangerErr := chat.ListItemComments(ctx, "T1", "U3", list.ID, item.ID, page)
				if err := chat.DeleteListItemComment(ctx, "T1", "U2", comment.ID); err != nil {
					return nil, err
				}
				after, err := chat.ListItemComments(ctx, "T1", "U1", list.ID, item.ID, page)
				if err != nil {
					return nil, err
				}
				return []any{
					// The item id is minted per composition, so compare that the
					// comment anchored to this run's item, not the id's bytes.
					len(seen.Comments), seen.Comments[0].ItemID == item.ID, seen.Comments[0].Text,
					string(seen.Comments[0].UserID), ownerDelete, strangerErr != nil, len(after.Comments),
				}, nil
			},
		},
		{
			name:  "a list item file is attached by an editor and readable by every list reader",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				list, err := chat.CreateList(ctx, "T1", "U1", "Assets", "[]",
					`[{"key":"title","name":"Title","type":"text"}]`, "", false, false)
				if err != nil {
					return nil, err
				}
				item, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"the logo"}]`)
				if err != nil {
					return nil, err
				}
				// U2 may read the list; U3 is a member with no grant.
				if err := chat.SetListAccess(ctx, "T1", "U1", list.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
					return nil, err
				}
				file, err := chat.UploadFile(ctx, "T1", "U1", "logo.png", "The logo", "image/png", 4, bytes.NewReader([]byte("data")))
				if err != nil {
					return nil, err
				}
				// A reader may not attach — attaching is an edit of the row.
				_, readerAttachErr := chat.AttachFileToListItem(ctx, "T1", "U2", list.ID, item.ID, file.ID)
				link, err := chat.AttachFileToListItem(ctx, "T1", "U1", list.ID, item.ID, file.ID)
				if err != nil {
					return nil, err
				}
				// A reader sees the attachment and can open the file although it
				// was shared into no conversation — the whole point of the join.
				seen, err := chat.ListItemFiles(ctx, "T1", "U2", list.ID, item.ID)
				if err != nil {
					return nil, err
				}
				_, readerFileErr := chat.FileInfo(ctx, "T1", "U2", file.ID)
				// A member with no grant sees neither the list nor the file.
				_, strangerListErr := chat.ListItemFiles(ctx, "T1", "U3", list.ID, item.ID)
				_, strangerFileErr := chat.FileInfo(ctx, "T1", "U3", file.ID)
				if err := chat.DetachFileFromListItem(ctx, "T1", "U1", list.ID, item.ID, file.ID); err != nil {
					return nil, err
				}
				after, err := chat.ListItemFiles(ctx, "T1", "U2", list.ID, item.ID)
				if err != nil {
					return nil, err
				}
				// Detaching revokes the reader's access again; the uploader keeps it.
				_, afterReaderErr := chat.FileInfo(ctx, "T1", "U2", file.ID)
				_, afterUploaderErr := chat.FileInfo(ctx, "T1", "U1", file.ID)
				return []any{
					len(seen), seen[0].ID == file.ID, seen[0].Title,
					link.ItemID == item.ID, link.FileID == file.ID,
					readerAttachErr != nil, readerFileErr == nil, strangerListErr != nil,
					strangerFileErr != nil, len(after), afterReaderErr != nil, afterUploaderErr == nil,
				}, nil
			},
		},
		{
			// A history is only useful if both compositions agree on what it
			// says and on which way round it reads: a revision records the
			// state it *replaced*, so the newest row is what the canvas said
			// before the last edit, not what it says now. Restoring is an
			// ordinary edit, so the current content becomes a revision of its
			// own and restoring the wrong one is itself undoable.
			name: "canvas revisions record what was replaced and can be restored",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				canvas, err := chat.CreateCanvas(ctx, "T1", "U1", "First title", `{"type":"markdown","markdown":"first body"}`, "")
				if err != nil {
					return nil, err
				}
				if err := chat.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"replace","document_content":{"type":"markdown","markdown":"second body"}}]`); err != nil {
					return nil, err
				}
				page := domain.PageRequest{Limit: 10}
				history, err := chat.CanvasRevisions(ctx, "T1", "U1", canvas.ID, page)
				if err != nil {
					return nil, err
				}
				titles := make([]string, 0, len(history.Revisions))
				for _, revision := range history.Revisions {
					titles = append(titles, revision.Title)
				}
				// A version nobody wrote cannot be restored.
				_, missingErr := chat.RestoreCanvasRevision(ctx, "T1", "U1", canvas.ID, 99)
				restored, err := chat.RestoreCanvasRevision(ctx, "T1", "U1", canvas.ID, 1)
				if err != nil {
					return nil, err
				}
				after, err := chat.CanvasRevisions(ctx, "T1", "U1", canvas.ID, page)
				if err != nil {
					return nil, err
				}
				return []any{
					len(history.Revisions), titles, missingErr != nil,
					restored.Title, restored.Version, len(after.Revisions),
				}, nil
			},
		},
		{
			// A search that matched more than the directory would disclose the
			// title of a canvas the reader cannot open, so the two compositions
			// have to agree on the visibility rule as well as on the matching.
			// U1 owns the canvas; U2 has no grant on it.
			name: "canvas search matches prose and stops at the reader's access",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				canvas, err := chat.CreateCanvas(ctx, "T1", "U1", "Deployment runbook", `{"type":"markdown","markdown":"roll back with the previous revision"}`, "")
				if err != nil {
					return nil, err
				}
				page := domain.PageRequest{Limit: 10}
				byTitle, err := chat.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "runbook", Page: page})
				if err != nil {
					return nil, err
				}
				// Folding is the product's one fold: a different case must
				// still match, in both compositions.
				byBody, err := chat.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "ROLL BACK", Page: page})
				if err != nil {
					return nil, err
				}
				// The document is stored as JSON. Searching for one of its keys
				// must find nothing, or the index is the syntax rather than the
				// prose.
				bySyntax, err := chat.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "sections", Page: page})
				if err != nil {
					return nil, err
				}
				excluded, err := chat.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "runbook -deployment", Page: page})
				if err != nil {
					return nil, err
				}
				stranger, err := chat.SearchCanvases(ctx, "T1", "U2", domain.CanvasSearchRequest{Query: "runbook", Page: page})
				if err != nil {
					return nil, err
				}
				// A conversation modifier has no meaning for an object that is
				// not in a conversation, and is refused rather than dropped.
				_, scopedErr := chat.SearchCanvases(ctx, "T1", "U1", domain.CanvasSearchRequest{Query: "runbook in:#general", Page: page})
				found := len(byTitle.Canvases) == 1 && byTitle.Canvases[0].ID == canvas.ID
				return []any{found, len(byBody.Canvases), len(bySyntax.Canvases), len(excluded.Canvases), len(stranger.Canvases), scopedErr != nil}, nil
			},
		},
		{
			name: "list search matches prose and stops at the reader's access",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				description := `[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"roll back with the previous revision"}]}]}]`
				list, err := chat.CreateList(ctx, "T1", "U1", "Deployment runbook", description, "[]", "", false, false)
				if err != nil {
					return nil, err
				}
				page := domain.PageRequest{Limit: 10}
				byName, err := chat.SearchLists(ctx, "T1", "U1", domain.ListSearchRequest{Query: "runbook", Page: page})
				if err != nil {
					return nil, err
				}
				// Folding is the product's one fold: a different case must still
				// match, in both compositions.
				byBody, err := chat.SearchLists(ctx, "T1", "U1", domain.ListSearchRequest{Query: "ROLL BACK", Page: page})
				if err != nil {
					return nil, err
				}
				// The description is stored as JSON blocks. Searching for a block
				// type must find nothing, or the index is the structure rather
				// than the prose.
				bySyntax, err := chat.SearchLists(ctx, "T1", "U1", domain.ListSearchRequest{Query: "rich_text", Page: page})
				if err != nil {
					return nil, err
				}
				excluded, err := chat.SearchLists(ctx, "T1", "U1", domain.ListSearchRequest{Query: "runbook -deployment", Page: page})
				if err != nil {
					return nil, err
				}
				stranger, err := chat.SearchLists(ctx, "T1", "U2", domain.ListSearchRequest{Query: "runbook", Page: page})
				if err != nil {
					return nil, err
				}
				// A conversation modifier has no meaning for a list either, and is
				// refused rather than dropped.
				_, scopedErr := chat.SearchLists(ctx, "T1", "U1", domain.ListSearchRequest{Query: "runbook in:#general", Page: page})
				found := len(byName.Lists) == 1 && byName.Lists[0].ID == list.ID
				return []any{found, len(byBody.Lists), len(bySyntax.Lists), len(excluded.Lists), len(stranger.Lists), scopedErr != nil}, nil
			},
		},
		{
			// Typing signals are the one piece of state that crosses the seam
			// without an event behind it, so nothing else in this suite would
			// notice if one composition quietly journalled them. What the two
			// have to agree on is who may see a signal: never its own author,
			// and never a reader who is not in the conversation. U1 and U2 are
			// both in C1; only U1 is in C2, which is what makes the second
			// half of this case a visibility check rather than a repeat.
			name: "typing signals reach members and no one else",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
					return nil, err
				}
				if err := chat.SetTyping(ctx, "T1", "U1", "C2"); err != nil {
					return nil, err
				}
				// Renewal replaces rather than accumulates: a client re-sends
				// every few seconds, so a second signal must not produce a
				// second row and make one person read as two typists.
				if err := chat.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
					return nil, err
				}
				readerOne, err := chat.TypingSignals(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				readerTwo, err := chat.TypingSignals(ctx, "T1", "U2")
				if err != nil {
					return nil, err
				}
				inChannel, err := chat.TypingIn(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				// A member who is not in C2 must not learn that anyone is
				// composing there, and a member must never be told about
				// themselves.
				authors := make([]string, 0, len(readerOne))
				for _, signal := range readerOne {
					authors = append(authors, string(signal.Conversation)+"/"+string(signal.UserID))
				}
				// The expiry itself is deliberately not compared: it is wall
				// clock, so the two compositions produce different instants
				// for the same correct behavior. That it is in the future is
				// the part that carries meaning.
				live := len(inChannel) == 1 && inChannel[0].Active(time.Now().UTC())
				return []any{authors, len(readerTwo), len(inChannel), live}, nil
			},
		},
		{
			// A delay parks a run on the clock, and the sweep that resumes it
			// is the only place a run advances without anyone asking. Both
			// compositions have to agree on what is due and on the fact that a
			// spent wait is not resumed twice.
			name: "workflow delays resume once when due",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				before, err := chat.ResumeWorkflowDelays(ctx, "T1", time.Unix(1700000000, 0).UTC(), 10)
				if err != nil {
					return nil, err
				}
				again, err := chat.ResumeWorkflowDelays(ctx, "T1", time.Unix(1900000000, 0).UTC(), 10)
				if err != nil {
					return nil, err
				}
				return []any{before, again}, nil
			},
		},
		{
			// Automatic presence is derived, not stored: the two compositions
			// have to agree that a member who has just been seen is active and
			// that a heartbeat is idempotent.
			name: "automatic presence follows recorded activity",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.RecordActivity(ctx, "T1", "U1"); err != nil {
					return nil, err
				}
				if err := chat.RecordActivity(ctx, "T1", "U1"); err != nil {
					return nil, err
				}
				user, err := chat.UserInfo(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				// The instant itself is wall-clock and differs between runs;
				// what must agree is that it was recorded and that automatic
				// presence reads it as active.
				return []any{user.LastActiveAt.IsZero(), user.Presence.CurrentAt(user.LastActiveAt, user.LastActiveAt.Add(time.Second))}, nil
			},
		},
		{
			// The Threads view is assembled from three reads the two
			// compositions could easily disagree about: which follows exist,
			// what the root says, and how many replies fall after the read
			// cursor.
			name: "followed threads",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				root, err := chat.Post(ctx, "T1", "U1", "C1", "thread root", "", "")
				if err != nil {
					return nil, err
				}
				rootTimestamp := timestampOf(root)
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "first reply", rootTimestamp, ""); err != nil {
					return nil, err
				}
				if err := chat.SetThreadFollowed(ctx, "T1", "U1", "C1", rootTimestamp, true); err != nil {
					return nil, err
				}
				followed, err := chat.FollowedThreads(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				projected := make([]any, 0, len(followed.Threads))
				for _, thread := range followed.Threads {
					projected = append(projected, []any{string(thread.Conversation), thread.RootText, string(thread.RootAuthorID), thread.ReplyCount, thread.UnreadReplies})
				}
				// Unfollowing removes it, which is the half a list test that
				// only ever adds cannot see.
				if err := chat.SetThreadFollowed(ctx, "T1", "U1", "C1", rootTimestamp, false); err != nil {
					return nil, err
				}
				after, err := chat.FollowedThreads(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{projected, len(after.Threads)}, nil
			},
		},
		{
			// Mark-all-read is the one cursor write whose result is a count
			// rather than a cursor, so the two compositions can disagree about
			// how many conversations moved without any single cursor read
			// noticing.
			name: "mark every conversation read",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "unread one", "", ""); err != nil {
					return nil, err
				}
				newest, err := chat.Post(ctx, "T1", "U1", "C1", "unread two", "", "")
				if err != nil {
					return nil, err
				}
				first, err := chat.MarkAllRead(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				cursor, err := chat.ReadCursor(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				// A second pass has nothing left to do, which is what proves
				// the count means "conversations that moved" and not
				// "conversations that exist".
				second, err := chat.MarkAllRead(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				// The cursor's own value is wall-clock and differs between the
				// two runs by construction; what must agree is that both landed
				// on the newest message.
				return []any{first, second, cursor.Conversation, cursor.LastRead == timestampOf(newest)}, nil
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
				// The count must agree with the list it summarizes, and a
				// stranger must be refused rather than told how big a channel
				// is.
				memberCount, err := chat.ConversationMemberCount(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				if memberCount != len(members.Users) {
					return nil, fmt.Errorf("the member count says %d and the member list says %d", memberCount, len(members.Users))
				}
				_, strangerErr := chat.ConversationMemberCount(ctx, "T1", "U-absent", "C1")
				if strangerErr == nil {
					return nil, fmt.Errorf("a stranger was told the member count")
				}
				identifiers := make([]domain.UserID, 0, len(members.Users))
				for _, member := range members.Users {
					identifiers = append(identifiers, member.ID)
				}
				// The cursor a member has is read back through the seam, and a
				// thread's accumulated replies are summarized in one batched
				// call: both are what the unread divider and the parent
				// message's "N replies" line are built from.
				readBack, err := chat.ReadCursor(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				reply, err := chat.Post(ctx, "T1", "U2", "C1", "in thread", timestampOf(message), "")
				if err != nil {
					return nil, err
				}
				_ = reply
				summaries, err := chat.ThreadSummaries(ctx, "T1", "U1", "C1", []domain.MessageTimestamp{timestampOf(message)})
				if err != nil {
					return nil, err
				}
				summary := summaries[timestampOf(message)]
				// The permalink route resolves a message by its public
				// timestamp, which is the identifier every Slack link and
				// action names it by.
				resolved, err := chat.MessageAt(ctx, "T1", "U1", "C1", timestampOf(message))
				if err != nil {
					return nil, err
				}
				return []any{
					cursor.Conversation, cursor.LastRead == timestampOf(message), identifiers, members.HasMore, isMember,
					readBack.Conversation, readBack.LastRead == cursor.LastRead,
					summary.ReplyCount, summary.Participants, !summary.LastReplyAt.IsZero(),
					resolved.ID == message.ID, resolved.Text, memberCount,
				}, nil
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
				// EndDND clears the schedule itself, which EndSnooze does not: a
				// snooze is an override of the schedule and ending it leaves the
				// schedule in force. Reading the state back is what separates them.
				if err := chat.EndDND(ctx, "T1", "U1"); err != nil {
					return nil, err
				}
				afterEndDND, err := chat.DoNotDisturbInfo(ctx, "T1", "U1", "")
				if err != nil {
					return nil, err
				}
				reference := time.Now().UTC()
				return []any{
					initial.Enabled, snoozed.SnoozeEnabled(reference), ended.SnoozeEnabled(reference),
					afterEndDND.Enabled, afterEndDND.SnoozeEnabled(reference),
				}, nil
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
					ctx, "T1", "U1", domain.NotificationAll, []string{"release", "customer escalation"}, false, true, true,
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
				afterAck, err := chat.GetDeveloperAppDeliveryHealth(ctx, "T1", "U1", "A1")
				if err != nil {
					return nil, err
				}
				// After the release then the ack, the retained history is a failed
				// attempt and then a delivered one; both compositions must agree on
				// the counts and on the newest outcome. Timestamps are wall-clock and
				// so are compared by presence, not value.
				newestDelivered, newestReason, newestHasTime := false, "", false
				if len(afterAck.RecentAttempts) != 0 {
					newest := afterAck.RecentAttempts[0]
					newestDelivered, newestReason, newestHasTime = newest.Delivered, newest.Reason, !newest.AttemptedAt.IsZero()
				}
				return []any{
					first.Sequence, first.Event.ID, first.Event.WorkspaceID, first.Event.ActorID, first.Event.Topic, first.Event.Payload,
					firstAttempt, firstReason,
					health.AppID, health.Surface, health.Endpoint, health.Configured, health.Installed,
					health.AcknowledgedSequence, health.InFlightSequence, health.RetryCount, health.RetryReason,
					health.PendingEvaluation, health.NextEventTopic, health.NextEventAt,
					health.FailedCount, health.DeliveredCount, len(health.RecentAttempts),
					second.Sequence, secondAttempt, secondReason,
					afterAck.DeliveredCount, afterAck.FailedCount, len(afterAck.RecentAttempts),
					newestDelivered, newestReason, newestHasTime,
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
				// The inventory lists every token the app issued, named by its hash
				// rather than the secret. Issue a second so a single revoke can be
				// seen to leave the other alone. The id is minted per composition, so
				// compare counts and revoked-flags, not the id's bytes.
				secondToken, err := chat.IssueDeveloperAppToken(ctx, "T1", "U1", app.ID, []string{"connections:write"})
				if err != nil {
					return nil, err
				}
				_ = secondToken
				listed, err := chat.ListDeveloperAppTokens(ctx, "T1", "U1", app.ID)
				if err != nil {
					return nil, err
				}
				var revokeOneErr error
				if len(listed) > 0 {
					revokeOneErr = chat.RevokeDeveloperAppToken(ctx, "T1", "U1", app.ID, listed[0].ID)
				}
				afterOne, err := chat.ListDeveloperAppTokens(ctx, "T1", "U1", app.ID)
				if err != nil {
					return nil, err
				}
				revokedCount := 0
				for _, summary := range afterOne {
					if summary.Revoked {
						revokedCount++
					}
				}
				// An id that names no token of this app is refused like a missing one.
				strangerRevoke := chat.RevokeDeveloperAppToken(ctx, "T1", "U1", app.ID, "not-a-real-token-id")
				// Revoking the app's tokens marks the one just issued revoked, which
				// LookupAppToken — the check every authenticated request runs — reports.
				if err := chat.RevokeDeveloperAppTokens(ctx, "T1", "U1", app.ID); err != nil {
					return nil, err
				}
				revoked, revokedErr := chat.LookupAppToken(ctx, appToken.Token)
				if revokedErr != nil {
					return nil, revokedErr
				}
				// The app declares user scopes as well as bot ones, because the v1
				// exchange and Sign in with Slack both mint user tokens and a
				// scope the manifest does not declare is not granted.
				updatedManifest := `{"display_information":{"name":"Updated"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write"],"user":["identity.basic","openid","email","profile"]}},"settings":{"socket_mode_enabled":true,"token_rotation_enabled":true}}`
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
				// The v1 exchange is a separate method with its own code, so it
				// gets its own authorization: an authorization code is spent by
				// the first exchange that redeems it.
				// The v1 exchange asks for a user token, so the authorization it
				// redeems has to have granted user scopes.
				v1Request := oauthRequest
				v1Request.State = "state-v1"
				v1Request.UserScopes = []string{"identity.basic"}
				v1Authorized, err := chat.AuthorizeOAuth(ctx, v1Request)
				if err != nil {
					return nil, err
				}
				v1Token, err := chat.OAuthExchange(ctx, credentials.ClientID, credentials.ClientSecret, v1Authorized.Code, v1Authorized.RedirectURI)
				if err != nil {
					return nil, err
				}
				// Sign in with Slack rides the same authorization and adds an
				// identity token, which is the whole point of the OIDC pair.
				oidcRequest := oauthRequest
				oidcRequest.State = "state-oidc"
				oidcRequest.UserScopes = []string{"openid", "email", "profile"}
				oidcAuthorized, err := chat.AuthorizeOAuth(ctx, oidcRequest)
				if err != nil {
					return nil, err
				}
				openID, err := chat.OpenIDConnectToken(ctx, credentials.ClientID, credentials.ClientSecret, oidcAuthorized.Code, oidcAuthorized.RedirectURI, "", "", "")
				if err != nil {
					return nil, err
				}
				userInfo, err := chat.OpenIDConnectUserInfo(ctx, openID.AccessToken)
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
				return []any{len(problems), app.Name, credentials.ClientID == app.ClientID, exportedApp.ID == app.ID, exported == manifest, len(apps), detail.ID == app.ID, detailManifest == manifest, strings.HasPrefix(appToken.Token, "xapp-"), appToken.AppID == app.ID, strings.Join(appToken.Scopes, " "), len(listed), revokeOneErr == nil, revokedCount, strangerRevoke != nil, updated.Name, updated.ManifestVersion, inspected.AppName, authorized.Code != "", authorized.BotID != "", authorized.BotUserID != "", strings.HasPrefix(oauthToken.AccessToken, "xoxe.xoxb-"), oauthToken.RefreshToken != "", strings.HasPrefix(refreshed.AccessToken, "xoxe.xoxb-"), refreshed.RefreshToken != "",
					v1Token.AccessToken != "", string(v1Token.TokenType), len(v1Token.Scopes) > 0,
					openID.IDToken != "", openID.AccessToken != "",
					string(userInfo.UserID), string(userInfo.WorkspaceID), userInfo.Email, userInfo.TeamName, revoked.Revoked}, nil
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
// into HTTP 503 and nothing here saw it until the store guards gained
// store.ErrInvalidArgument and the page-limit case above started asserting the
// class instead of tolerating any agreed failure.
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
			t.Errorf("chatapi.Service.%s crosses the seam with no parity case. Add a case to parityCases. Adding %q to parityGaps instead means lowering parityGapCeiling somewhere else in the same change, because the backlog is only allowed to shrink.", name, name)
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
// invisible — a method added to chatapi.Service cannot join it silently, and a
// method whose parity case is deleted reappears here as a failure. That the set
// can only shrink is now enforced by parityGapCeiling rather than asserted
// here; it was asserted here for a long time while nothing checked it.
//
// Priority for closing it, from the failures the deleted guards produced: the
// methods that take a page bound, a timestamp in nanoseconds, or an identifier
// the store treats as optional.
// parityGapCeiling is how large the backlog is allowed to be.
//
// The claim above — that the set can only shrink — was a claim nothing checked,
// and the failure message next to it asked for a reason the structure could not
// hold: parityGaps is a set of names, so "say why it cannot have one" had
// nowhere to be said and 179 entries said nothing. A number is the honest form
// of the promise that was being made. Closing a gap means lowering this, and a
// method that arrives without a parity case cannot join the backlog without
// taking somebody else's place.
//
// It is zero. Every method chatapi.Service declares is exercised by a case, so
// there is no backlog left to shrink and a new method has nowhere to hide: it
// either gets a case or this constant has to be raised, which is a decision
// somebody has to argue for rather than a list somebody can quietly append to.
//
// Two limits are worth knowing rather than discovering. The workflow step
// methods can be checked for a dropped identifier but not for a dropped
// payload, because nothing on this seam reads a stored step back; the case says
// so and the product gap audit records the underlying gap. And a case whose
// methods call an app over HTTP must use seedWithApp: the receiver has to be
// TLS and the app's credentials sealed for real, or every dispatch fails
// identically in both compositions and the case reports an agreement it has
// not established.
const parityGapCeiling = 0

// TestTheParityBacklogOnlyShrinks makes that promise checkable in both
// directions: the backlog cannot grow, and it cannot quietly shrink either —
// coverage that is won has to be recorded, or the ceiling drifts upward again
// the next time somebody needs room.
func TestTheParityBacklogOnlyShrinks(t *testing.T) {
	switch {
	case len(parityGaps) > parityGapCeiling:
		t.Errorf("the parity backlog grew to %d against a ceiling of %d: a method that crosses the seam needs a case, not an entry", len(parityGaps), parityGapCeiling)
	case len(parityGaps) < parityGapCeiling:
		t.Errorf("the parity backlog is down to %d: lower parityGapCeiling to match, so the ground gained is kept", len(parityGaps))
	}
}

var parityGaps = map[string]struct{}{
	// These three credential-aware methods share the scheduled-message RPCs
	// exercised by the legacy wrappers above. Their token/range fields have
	// focused transport tests because parityCases seeds both compositions with
	// fresh independent stores and therefore cannot compare one token's durable
	// schedule across calls.
}
