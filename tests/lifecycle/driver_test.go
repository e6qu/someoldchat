package lifecycle

import (
	"bytes"
	"context"
	"testing"
	"time"

	"path/filepath"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A machine on its own is a document. A driver is what makes the product obey
// it: it puts a real instance into a state and asks the real service for every
// move the model forbids, requiring each to be refused, and for every move the
// model allows, requiring each to be taken.
//
// One driver per lifecycle is real work — each needs a fixture the operation
// can find — so the lifecycles without one are counted rather than assumed. The
// count only shrinks.
type driver struct {
	// start puts a fresh instance into the given state, or reports that it
	// cannot be reached directly.
	start func(t *testing.T, state string) (service.Messages, bool)
	// attempt asks the product to move the instance from one state to another.
	// The from-state matters: a shared invitation reaches "revoked" by being
	// denied while pending and by being revoked once approved, and those are
	// different operations with different rules.
	attempt func(messages service.Messages, from, to string) error
	// observe reads the state the product actually holds.
	observe func(t *testing.T, messages service.Messages) string
}

func drivers() map[string]driver {
	return map[string]driver{
		"AppApprovalStatus":    appApprovalDriver(),
		"SavedItemState":       savedItemDriver(),
		"SharedInviteStatus":   sharedInviteDriver(),
		"InviteRequestStatus":  inviteRequestDriver(),
		"ExternalUploadStatus": externalUploadDriver(),
		"WorkflowStatus":       workflowDriver(),
	}
}

// undrivenLifecycleCeiling is how many declared machines are still checked only
// against themselves. Each is a set of rules nothing holds the product to.
//
// The two left are WorkflowRunStatus and WorkflowStepStatus, and the reason is
// not that they resist driving — it is what driving them costs. A run needs an
// app whose manifest declares the step's function, an installation, a published
// workflow, a trigger, and then the app-facing completion calls that advance a
// step; the run and the step machines have to be driven together because a step
// is what moves the run. That is a fixture, not a driver, and it belongs in its
// own change rather than bolted onto this one.
//
// Looking for the driver found something first: a run can never be queued.
// runWorkflow creates every run already running, nothing else sets the state,
// and both stores count queued runs into the Workflow Activity summary — so it
// reports a figure that is always zero. That is recorded in the product gap
// audit, because it is a question about the execution model rather than a bug
// with an obvious fix.
const undrivenLifecycleCeiling = 2

func TestEveryLifecycleWithoutADriverIsCounted(t *testing.T) {
	undriven := 0
	for name := range machines() {
		if _, ok := drivers()[name]; !ok {
			undriven++
			t.Logf("%s is declared and not driven: its rules are checked against themselves and nothing else", name)
		}
	}
	if undriven > undrivenLifecycleCeiling {
		t.Fatalf("%d lifecycles have no driver, above the ceiling of %d", undriven, undrivenLifecycleCeiling)
	}
	if undriven < undrivenLifecycleCeiling {
		t.Fatalf("only %d lifecycles lack a driver now: lower undrivenLifecycleCeiling to %d, so the ground gained is kept", undriven, undriven)
	}
}

// TestTheProductRefusesEveryTransitionTheMachineForbids is the property. For
// each reachable state, every move the model does not allow must be refused,
// and every move it does allow must be taken.
func TestTheProductRefusesEveryTransitionTheMachineForbids(t *testing.T) {
	for name, wiring := range drivers() {
		model := machines()[name]
		states := allStates(model)
		t.Run(name, func(t *testing.T) {
			for _, from := range states {
				allowed := map[string]bool{}
				for _, to := range model.transitions[from] {
					allowed[to] = true
				}
				for _, to := range states {
					if to == from {
						continue
					}
					messages, ok := wiring.start(t, from)
					if !ok {
						continue
					}
					err := wiring.attempt(messages, from, to)
					switch {
					case allowed[to] && err != nil:
						t.Errorf("%s says %q may become %q and the product refused with %v", name, from, to, err)
					case !allowed[to] && err == nil:
						// Accepting the call is not the same as making the
						// move. A staged edit to a published workflow is
						// accepted and deliberately leaves it published, and
						// reading that as a forbidden transition would be this
						// harness confusing an operation with a transition —
						// the mirror of the mistake it just caught on the
						// allowed side. What the machine constrains is the
						// state the instance ends in.
						if reached := wiring.observe(t, messages); reached == to {
							t.Errorf("%s says %q may not become %q and the product did it anyway", name, from, to)
						}
					case allowed[to] && err == nil:
						// A transition that is accepted and does not arrive is
						// the defect this whole project keeps finding: the call
						// answered, so the test was satisfied, and the state
						// went somewhere else. Requiring only "no error" here
						// let a driver map two transitions onto one operation
						// and pass while one of them landed elsewhere.
						if reached := wiring.observe(t, messages); reached != to {
							t.Errorf("%s says %q may become %q, the product accepted it, and the instance is now %q", name, from, to, reached)
						}
					}
				}
			}
		})
	}
}

func allStates(model machine) []string {
	seen := map[string]bool{}
	for _, state := range model.terminal {
		seen[state] = true
	}
	for from, targets := range model.transitions {
		seen[from] = true
		for _, target := range targets {
			seen[target] = true
		}
	}
	states := make([]string, 0, len(seen))
	for state := range seen {
		states = append(states, state)
	}
	return states
}

func appApprovalDriver() driver {
	const workspace = domain.WorkspaceID("T1")
	const admin = domain.UserID("U-admin")
	const app = domain.AppID("A1")
	const request = domain.AppRequestID("R1")
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			now := time.Now().UTC()
			if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "test"}); err != nil {
				t.Fatal(err)
			}
			membership := domain.WorkspaceMembership{WorkspaceID: workspace, UserID: admin, Role: domain.WorkspaceRoleMember, Active: true}
			if err := repository.CreateUser(ctx, domain.User{ID: admin, WorkspaceID: workspace, Name: "admin", Email: "admin@example.test"}, membership, events.Event{
				ID: "E-user", WorkspaceID: workspace, Topic: "user.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.SetWorkspaceRole(ctx, workspace, admin, domain.WorkspaceRoleAdmin, events.Event{
				ID: "E-role", WorkspaceID: workspace, Topic: "workspace.role_changed", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.SetAppApproval(ctx, workspace, app, request, domain.AppApprovalStatus(state), now, events.Event{
				ID: "E-app", WorkspaceID: workspace, Topic: "app.requested", Payload: string(app), CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			return service.Messages{Store: repository}, true
		},
		attempt: func(messages service.Messages, _, to string) error {
			ctx := context.Background()
			switch domain.AppApprovalStatus(to) {
			case domain.AppApprovalApproved:
				return messages.AdminApproveApp(ctx, workspace, admin, app, request)
			case domain.AppApprovalRestricted:
				return messages.AdminRestrictApp(ctx, workspace, admin, app, request)
			case domain.AppApprovalCancelled:
				return messages.AdminCancelAppRequest(ctx, workspace, admin, app, request)
			}
			// No operation moves an approval back to requested, so the model
			// must not claim one does.
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			current, err := messages.Store.GetAppApproval(context.Background(), workspace, app)
			if err != nil {
				t.Fatal(err)
			}
			return string(current.Status)
		},
	}
}

var errNoSuchTransition = errNoTransition{}

type errNoTransition struct{}

func (errNoTransition) Error() string {
	return "the product has no operation that performs this transition"
}

// savedItemDriver drives a member's own saved item. The machine declares no
// terminal state — marking something done is not a promise it stays done — so
// what this asks is the other half: every move the machine allows must actually
// be taken, and a state the product refuses that the machine permits is a
// disagreement worth having.
func savedItemDriver() driver {
	const (
		workspace = domain.WorkspaceID("T1")
		member    = domain.UserID("U-member")
		channel   = domain.ConversationID("C1")
		message   = domain.MessageID("M1")
		item      = domain.SavedItemID("S1")
	)
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			now := time.Now().UTC()
			if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "test"}); err != nil {
				t.Fatal(err)
			}
			membership := domain.WorkspaceMembership{WorkspaceID: workspace, UserID: member, Role: domain.WorkspaceRoleMember, Active: true}
			if err := repository.CreateUser(ctx, domain.User{ID: member, WorkspaceID: workspace, Name: "member", Email: "member@example.test"}, membership, events.Event{
				ID: "E-user", WorkspaceID: workspace, Topic: "user.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.CreateConversation(ctx, domain.Conversation{ID: channel, WorkspaceID: workspace, Name: "general"}, member, events.Event{
				ID: "E-conversation", WorkspaceID: workspace, Topic: "conversation.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.CreateMessage(ctx, domain.Message{
				ID: message, WorkspaceID: workspace, Conversation: channel, AuthorID: member, Text: "saved", CreatedAt: now,
			}, events.Event{ID: "E-message", WorkspaceID: workspace, Topic: "message.created", CreatedAt: now}, ""); err != nil {
				t.Fatal(err)
			}
			if _, _, err := repository.CreateSavedItem(ctx, domain.SavedItem{
				ID: item, WorkspaceID: workspace, UserID: member, MessageID: message, Conversation: channel,
				State: domain.SavedItemState(state), CreatedAt: now, UpdatedAt: now,
			}, events.Event{ID: "E-saved", WorkspaceID: workspace, Topic: "saved_item.created", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			return service.Messages{Store: repository}, true
		},
		attempt: func(messages service.Messages, _, to string) error {
			_, err := messages.SetSavedItemState(context.Background(), workspace, member, item, domain.SavedItemState(to))
			return err
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			current, err := messages.Store.GetSavedItem(context.Background(), workspace, member, item)
			if err != nil {
				t.Fatal(err)
			}
			return string(current.State)
		},
	}
}

// sharedInviteDriver drives a Slack Connect invitation across two workspaces.
//
// Which side acts differs by transition — the host approves, revokes and denies;
// the invited organization accepts and declines — so the driver uses the actor
// each operation is for. Who MAY act is the authorization matrix's question;
// this one is whether the state machine is enforced at all.
func sharedInviteDriver() driver {
	const (
		host      = domain.WorkspaceID("T1")
		target    = domain.WorkspaceID("T2")
		hostAdmin = domain.UserID("U-host")
		guest     = domain.UserID("U-target")
		channel   = domain.ConversationID("C1")
		invite    = domain.SharedInviteID("I1")
	)
	admin := func(t *testing.T, repository *memory.Store, workspace domain.WorkspaceID, id domain.UserID, at time.Time) {
		t.Helper()
		membership := domain.WorkspaceMembership{WorkspaceID: workspace, UserID: id, Role: domain.WorkspaceRoleMember, Active: true}
		if err := repository.CreateUser(ctx0(), domain.User{ID: id, WorkspaceID: workspace, Name: string(id), Email: string(id) + "@example.test"}, membership, events.Event{
			ID: domain.EventID("E-user-" + id), WorkspaceID: workspace, Topic: "user.created", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repository.SetWorkspaceRole(ctx0(), workspace, id, domain.WorkspaceRoleAdmin, events.Event{
			ID: domain.EventID("E-role-" + id), WorkspaceID: workspace, Topic: "workspace.role_changed", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			now := time.Now().UTC()
			for _, workspace := range []domain.WorkspaceID{host, target} {
				if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: string(workspace)}); err != nil {
					t.Fatal(err)
				}
			}
			admin(t, repository, host, hostAdmin, now)
			admin(t, repository, target, guest, now)
			if err := repository.CreateConversation(ctx, domain.Conversation{ID: channel, WorkspaceID: host, Name: "shared"}, hostAdmin, events.Event{
				ID: "E-conversation", WorkspaceID: host, Topic: "conversation.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			// An invitation can only be CREATED pending — the store refuses any
			// other status, which is itself the machine being enforced at the
			// point of creation. Every other state is therefore reached by
			// driving the product to it rather than by writing the row, which
			// is a stronger fixture: a state that cannot be reached through the
			// product is a state the product cannot be in.
			if err := repository.CreateSharedInvite(ctx, domain.SharedInvite{
				ID: invite, WorkspaceID: host, ConversationID: channel, TargetWorkspaceID: target,
				TargetEmail: string(guest) + "@example.test", InvitedBy: hostAdmin,
				Status: domain.SharedInvitePending, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour),
			}, events.Event{ID: "E-invite", WorkspaceID: host, Topic: "conversation.shared_invite_sent", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			messages := service.Messages{Store: repository}
			for _, step := range routeToSharedInviteState(state) {
				if err := step(messages); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			}
			return messages, true
		},
		attempt: func(messages service.Messages, from, to string) error {
			ctx := context.Background()
			switch domain.SharedInviteStatus(to) {
			case domain.SharedInviteApproved:
				_, err := messages.ApproveSharedInvite(ctx, host, hostAdmin, invite)
				return err
			case domain.SharedInviteRevoked:
				// Denying is the host refusing to send one it has not approved;
				// revoking is withdrawing one it has. Both land on revoked.
				if domain.SharedInviteStatus(from) == domain.SharedInvitePending {
					_, err := messages.DenySharedInvite(ctx, host, hostAdmin, invite)
					return err
				}
				_, err := messages.RevokeSharedInvite(ctx, host, hostAdmin, invite)
				return err
			case domain.SharedInviteDeclined:
				_, err := messages.DeclineSharedInvite(ctx, target, guest, invite)
				return err
			case domain.SharedInviteAccepted:
				_, err := messages.AcceptSharedInvite(ctx, target, guest, invite)
				return err
			}
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			current, err := messages.Store.GetSharedInvite(context.Background(), invite)
			if err != nil {
				t.Fatal(err)
			}
			return string(current.Status)
		},
	}
}

// routeToSharedInviteState is how the driver reaches a state that cannot be
// written directly. Nothing here is a shortcut: each step is the product's own
// operation, so a route that stops working is a transition that stopped working.
func routeToSharedInviteState(state string) []func(service.Messages) error {
	const (
		host      = domain.WorkspaceID("T1")
		target    = domain.WorkspaceID("T2")
		hostAdmin = domain.UserID("U-host")
		guest     = domain.UserID("U-target")
		invite    = domain.SharedInviteID("I1")
	)
	approve := func(messages service.Messages) error {
		_, err := messages.ApproveSharedInvite(context.Background(), host, hostAdmin, invite)
		return err
	}
	switch domain.SharedInviteStatus(state) {
	case domain.SharedInvitePending:
		return nil
	case domain.SharedInviteApproved:
		return []func(service.Messages) error{approve}
	case domain.SharedInviteAccepted:
		return []func(service.Messages) error{approve, func(messages service.Messages) error {
			_, err := messages.AcceptSharedInvite(context.Background(), target, guest, invite)
			return err
		}}
	case domain.SharedInviteDeclined:
		return []func(service.Messages) error{approve, func(messages service.Messages) error {
			_, err := messages.DeclineSharedInvite(context.Background(), target, guest, invite)
			return err
		}}
	case domain.SharedInviteRevoked:
		return []func(service.Messages) error{func(messages service.Messages) error {
			_, err := messages.DenySharedInvite(context.Background(), host, hostAdmin, invite)
			return err
		}}
	}
	return nil
}

func ctx0() context.Context { return context.Background() }

// inviteRequestDriver drives an invitation to join a workspace. Denying is the
// answer to a request and revoking is the withdrawal of an answer already
// given, so one operation covers both and the from-state chooses which.
func inviteRequestDriver() driver {
	const (
		workspace = domain.WorkspaceID("T1")
		admin     = domain.UserID("U-admin")
		request   = domain.InviteRequestID("R1")
		invitee   = "invited@example.test"
	)
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			now := time.Now().UTC()
			if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "test"}); err != nil {
				t.Fatal(err)
			}
			membership := domain.WorkspaceMembership{WorkspaceID: workspace, UserID: admin, Role: domain.WorkspaceRoleMember, Active: true}
			if err := repository.CreateUser(ctx, domain.User{ID: admin, WorkspaceID: workspace, Name: "admin", Email: "admin@example.test"}, membership, events.Event{
				ID: "E-user", WorkspaceID: workspace, Topic: "user.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.SetWorkspaceRole(ctx, workspace, admin, domain.WorkspaceRoleAdmin, events.Event{
				ID: "E-role", WorkspaceID: workspace, Topic: "workspace.role_changed", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.CreateInviteRequest(ctx, domain.InviteRequest{
				ID: request, WorkspaceID: workspace, Email: invitee, RequestedBy: admin,
				Status: domain.InviteRequestPending, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
			}, events.Event{ID: "E-request", WorkspaceID: workspace, Topic: "invite_request.created", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			messages := service.Messages{Store: repository}
			if domain.InviteRequestStatus(state) != domain.InviteRequestPending {
				if err := messages.AdminApproveInviteRequest(ctx, workspace, admin, request); err != nil {
					t.Fatalf("reaching approved on the way to %s: %v", state, err)
				}
			}
			switch domain.InviteRequestStatus(state) {
			case domain.InviteRequestPending, domain.InviteRequestApproved:
			case domain.InviteRequestAccepted:
				if _, err := messages.AcceptInvitationForEmail(ctx, workspace, invitee, "Invited"); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			default:
				// denied and revoked are both reached by denying, from pending
				// and from approved respectively.
				if err := messages.AdminDenyInviteRequest(ctx, workspace, admin, request); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			}
			return messages, true
		},
		attempt: func(messages service.Messages, from, to string) error {
			ctx := context.Background()
			switch domain.InviteRequestStatus(to) {
			case domain.InviteRequestApproved:
				return messages.AdminApproveInviteRequest(ctx, workspace, admin, request)
			case domain.InviteRequestDenied:
				// One operation, and where it lands depends on where it starts.
				// Asking it for "denied" from an approved request would call a
				// real operation that legitimately produces revoked, and reading
				// that as "the product allowed a forbidden transition" would be
				// the driver's confusion rather than the product's fault.
				if domain.InviteRequestStatus(from) != domain.InviteRequestPending {
					return errNoSuchTransition
				}
				return messages.AdminDenyInviteRequest(ctx, workspace, admin, request)
			case domain.InviteRequestRevoked:
				if domain.InviteRequestStatus(from) != domain.InviteRequestApproved {
					return errNoSuchTransition
				}
				return messages.AdminDenyInviteRequest(ctx, workspace, admin, request)
			case domain.InviteRequestAccepted:
				_, err := messages.AcceptInvitationForEmail(ctx, workspace, invitee, "Invited")
				return err
			}
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			current, err := messages.Store.GetInviteRequest(context.Background(), workspace, request)
			if err != nil {
				t.Fatal(err)
			}
			return string(current.Status)
		},
	}
}

// externalUploadDriver drives Slack's two-step upload: a ticket is created, the
// bytes are put, and the upload is completed into a real file.
//
// It needs a blob store, because putting the bytes is the transition. A driver
// that skipped that step would be writing rows rather than driving the product,
// which is the thing these drivers exist not to do.
func externalUploadDriver() driver {
	const (
		workspace = domain.WorkspaceID("T1")
		member    = domain.UserID("U-member")
		channel   = domain.ConversationID("C1")
		payload   = "hello"
	)
	var uploadID domain.ExternalUploadID
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			now := time.Now().UTC()
			if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "test"}); err != nil {
				t.Fatal(err)
			}
			membership := domain.WorkspaceMembership{WorkspaceID: workspace, UserID: member, Role: domain.WorkspaceRoleMember, Active: true}
			if err := repository.CreateUser(ctx, domain.User{ID: member, WorkspaceID: workspace, Name: "member", Email: "member@example.test"}, membership, events.Event{
				ID: "E-user", WorkspaceID: workspace, Topic: "user.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repository.CreateConversation(ctx, domain.Conversation{ID: channel, WorkspaceID: workspace, Name: "general"}, member, events.Event{
				ID: "E-conversation", WorkspaceID: workspace, Topic: "conversation.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
			if err != nil {
				t.Fatal(err)
			}
			messages := service.Messages{Store: repository, Blob: objects}
			ticket, err := messages.CreateExternalUpload(ctx, workspace, member, "fixture.txt", "text/plain", int64(len(payload)), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			uploadID = ticket.ID
			switch domain.ExternalUploadStatus(state) {
			case domain.ExternalUploadPending:
			case domain.ExternalUploadUploaded:
				if err := messages.UploadExternalFile(ctx, uploadID, int64(len(payload)), bytes.NewReader([]byte(payload))); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			case domain.ExternalUploadCompleted:
				if err := messages.UploadExternalFile(ctx, uploadID, int64(len(payload)), bytes.NewReader([]byte(payload))); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
				if _, err := messages.CompleteExternalUpload(ctx, workspace, member, uploadID, "fixture", []domain.ConversationID{channel}, "", "", ""); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			}
			return messages, true
		},
		attempt: func(messages service.Messages, from, to string) error {
			ctx := context.Background()
			switch domain.ExternalUploadStatus(to) {
			case domain.ExternalUploadUploaded:
				return messages.UploadExternalFile(ctx, uploadID, int64(len(payload)), bytes.NewReader([]byte(payload)))
			case domain.ExternalUploadCompleted:
				_, err := messages.CompleteExternalUpload(ctx, workspace, member, uploadID, "fixture", []domain.ConversationID{channel}, "", "", "")
				return err
			}
			// Nothing puts a spent ticket back to pending.
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			current, err := messages.Store.GetExternalUpload(context.Background(), uploadID)
			if err != nil {
				t.Fatal(err)
			}
			return string(current.Status)
		},
	}
}

// workflowDriver drives a workflow between draft, published and disabled.
//
// One operation carries every transition: UpdateWorkflow publishes when asked
// to, takes the workflow offline when handed a disabled status, and otherwise
// leaves a draft — except on a published workflow, where a plain edit is staged
// and the published revision stays live. That last rule is the interesting one,
// and it is the product's own comment rather than an inference.
func workflowDriver() driver {
	const (
		workspace = domain.WorkspaceID("T1")
		owner     = domain.UserID("U-owner")
		app       = domain.AppID("A1")
	)
	// The service mints the identifier, so the driver reads it back rather than
	// assuming one.
	var workflow domain.WorkflowID
	definition := func(status domain.WorkflowStatus) domain.WorkflowDefinition {
		return domain.WorkflowDefinition{
			ID: workflow, WorkspaceID: workspace, AppID: app, OwnerID: owner,
			CallbackID: "fixture", Title: "fixture workflow", Status: status,
			InputSchema: `{}`,
			// Publishing validates the steps, so a workflow with none cannot be
			// published and the driver would never reach the state it is for.
			Steps: `[{"function_id":"triage","title":"Classify","inputs":{"source":"workflow"}}]`,
		}
	}
	version := func(t *testing.T, messages service.Messages) uint64 {
		t.Helper()
		current, err := messages.Store.GetWorkflow(context.Background(), workspace, workflow)
		if err != nil {
			t.Fatal(err)
		}
		return current.Version
	}
	return driver{
		start: func(t *testing.T, state string) (service.Messages, bool) {
			t.Helper()
			ctx := context.Background()
			repository := memory.New()
			now := time.Now().UTC()
			if err := repository.SeedWorkspace(domain.Workspace{ID: workspace, Name: "test"}); err != nil {
				t.Fatal(err)
			}
			membership := domain.WorkspaceMembership{WorkspaceID: workspace, UserID: owner, Role: domain.WorkspaceRoleMember, Active: true}
			if err := repository.CreateUser(ctx, domain.User{ID: owner, WorkspaceID: workspace, Name: "owner", Email: "owner@example.test"}, membership, events.Event{
				ID: "E-user", WorkspaceID: workspace, Topic: "user.created", CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			// The workflow's step names a function, and a function the app's
			// manifest does not declare is refused — so the app has to be real,
			// with the manifest that declares it, before a workflow can exist
			// at all.
			if err := repository.CreateApp(ctx, domain.App{
				ID: app, DevelopmentWorkspaceID: workspace, OwnerID: owner, Name: "Automation",
				ClientID: "client", SigningSecretHash: "signing-hash",
				SigningSecretCiphertext: "signing-ciphertext", VerificationTokenHash: "verification-hash",
				VerificationTokenCiphertext: "verification-ciphertext",
				ManifestVersion:             1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
			}, domain.AppManifestRevision{
				AppID: app, Version: 1, CreatedBy: owner, CreatedAt: now,
				Manifest: `{
					"display_information":{"name":"Automation"},
					"settings":{"function_runtime":"remote"},
					"functions":{"triage":{
						"title":"Triage incident","description":"Classifies one incident",
						"input_parameters":{"properties":{"incident":{"type":"string","title":"Incident"}},"required":["incident"]},
						"output_parameters":{"properties":{"priority":{"type":"integer","title":"Priority"}},"required":["priority"]}
					}}
				}`,
			}, domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: app}); err != nil {
				t.Fatal(err)
			}
			if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{
				AppID: app, WorkspaceID: workspace, Enabled: true, CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			messages := service.Messages{Store: repository}
			// Created through the product rather than written as a row: the
			// service normalises the step payload, and a workflow whose steps
			// were never normalised cannot be published, so a seeded row would
			// have made the very transition under test unreachable.
			created, err := messages.CreateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDraft))
			if err != nil {
				t.Fatal(err)
			}
			workflow = created.ID
			switch domain.WorkflowStatus(state) {
			case domain.WorkflowDraft:
			case domain.WorkflowPublished:
				if _, err := messages.UpdateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDraft), version(t, messages), true); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			case domain.WorkflowDisabled:
				if _, err := messages.UpdateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDraft), version(t, messages), true); err != nil {
					t.Fatalf("publishing on the way to %s: %v", state, err)
				}
				if _, err := messages.UpdateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDisabled), version(t, messages), false); err != nil {
					t.Fatalf("reaching %s: %v", state, err)
				}
			}
			return messages, true
		},
		attempt: func(messages service.Messages, from, to string) error {
			ctx := context.Background()
			current, err := messages.Store.GetWorkflow(ctx, workspace, workflow)
			if err != nil {
				return err
			}
			switch domain.WorkflowStatus(to) {
			case domain.WorkflowPublished:
				_, err := messages.UpdateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDraft), current.Version, true)
				return err
			case domain.WorkflowDisabled:
				_, err := messages.UpdateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDisabled), current.Version, false)
				return err
			case domain.WorkflowDraft:
				_, err := messages.UpdateWorkflow(ctx, workspace, owner, definition(domain.WorkflowDraft), current.Version, false)
				return err
			}
			return errNoSuchTransition
		},
		observe: func(t *testing.T, messages service.Messages) string {
			t.Helper()
			current, err := messages.Store.GetWorkflow(context.Background(), workspace, workflow)
			if err != nil {
				t.Fatal(err)
			}
			return string(current.Status)
		},
	}
}
