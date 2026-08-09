package lifecycle

import (
	"context"
	"testing"
	"time"

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
	// attempt asks the product to move the instance to a state.
	attempt func(messages service.Messages, to string) error
	// observe reads the state the product actually holds.
	observe func(t *testing.T, messages service.Messages) string
}

func drivers() map[string]driver {
	return map[string]driver{"AppApprovalStatus": appApprovalDriver()}
}

// undrivenLifecycleCeiling is how many declared machines are still checked only
// against themselves. Each is a set of rules nothing holds the product to.
const undrivenLifecycleCeiling = 7

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
					err := wiring.attempt(messages, to)
					switch {
					case allowed[to] && err != nil:
						t.Errorf("%s says %q may become %q and the product refused with %v", name, from, to, err)
					case !allowed[to] && err == nil:
						t.Errorf("%s says %q may not become %q and the product allowed it, leaving %q", name, from, to, wiring.observe(t, messages))
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
		attempt: func(messages service.Messages, to string) error {
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
