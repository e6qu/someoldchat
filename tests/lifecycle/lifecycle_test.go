// Package lifecycle holds the state-machine gate.
//
// Every one of these lifecycles has a terminal state, and a terminal state is a
// promise: an invitation that lapsed cannot later be approved, a cancelled run
// cannot complete, a revoked invite cannot be accepted. That promise is made in
// eight separate places in internal/service, each with its own conditional, and
// nothing checked that the eight agreed with each other or with the states the
// domain actually declares.
//
// The lifecycle set is derived from the domain rather than listed here, so a
// status type that arrives without a declared machine fails this gate instead
// of quietly having no rules at all. The states are derived too: adding a
// constant to the domain and forgetting to say where it may lead fails here.
package lifecycle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// machine is one lifecycle: what states exist, which of them are final, and
// which moves are legal.
type machine struct {
	// terminal are the states nothing may leave. A machine with none is a
	// setting rather than a lifecycle and does not belong here.
	terminal []string
	// transitions maps a state to the states it may become. A state absent
	// from the map and absent from terminal is unreachable-by-declaration and
	// fails the gate: it is either a state nothing can produce, or a rule
	// nobody wrote down.
	transitions map[string][]string
	// standing are states that deliberately never finish: a decision an
	// administrator may reverse for ever is not a lifecycle that strands a row,
	// it is a setting wearing a status field. Declaring one is how a genuine
	// dead end is told apart from a state nothing can ever complete.
	standing []string
	// why records what the terminal states mean in the product's terms, so a
	// reader can tell a deliberate dead end from an omission.
	why string
}

// machines declares every lifecycle the domain carries.
func machines() map[string]machine {
	return map[string]machine{
		"WorkflowStatus": {
			terminal: []string{},
			// Two edges here were wrong, and UpdateWorkflow's own comments say
			// so. A published workflow is NOT edited back to draft: "a staged
			// edit to a published workflow keeps the published revision live",
			// so the edit is accepted and the status stays published — which is
			// why the driver had to check that a transition arrives rather than
			// that it was accepted. And a draft CAN be taken offline: unpublish
			// is the caller's explicit action and does not ask what the status
			// was, because "every other edit leaves a draft or published head
			// exactly as the caller's action says".
			transitions: map[string][]string{"draft": {"published", "disabled"}, "published": {"disabled"}, "disabled": {"published", "draft"}},
			why:         "a workflow has no dead end: a disabled workflow is published again or edited back to draft",
		},
		"WorkflowRunStatus": {
			terminal: []string{"completed", "failed", "cancelled"},
			// "queued" is declared, counted, and never produced. runWorkflow
			// creates a run already running, and the only other mentions of
			// queued in the tree are the two stores counting it for the Workflow
			// Activity summary — so that view reports a Queued figure which is
			// structurally always zero. It is left in the machine because the
			// domain declares it and the activity view reads it; what to do
			// about a state nothing can reach is recorded in the product gap
			// audit rather than decided by deleting the constant here.
			transitions: map[string][]string{"queued": {"running", "cancelled", "failed"}, "running": {"completed", "failed", "cancelled"}},
			why:         "a run that finished, failed or was cancelled is over; nothing restarts it in place",
		},
		"WorkflowStepStatus": {
			terminal: []string{"completed", "failed", "cancelled"},
			standing: []string{"configured"},
			// Three edges here described a sequence that does not exist, and
			// driving the product showed each one.
			//
			// "configured" is a BUILDER state, written by WorkflowUpdateStep
			// against the step a workflow is being assembled from. Running the
			// workflow creates a separate execution row that merely points back
			// at it, so a configured step never becomes an executing one — the
			// two are different rows, and the machine had them as one moving
			// through a lifecycle.
			//
			// "waiting" is where a delay or form step BEGINS, created that way,
			// not somewhere an executing step goes. Both are entry points rather
			// than a sequence.
			//
			// The driver reaches executing, and through it completed, failed and
			// cancelled. Waiting needs a delay or form step and configured needs
			// the builder; neither is built here, so those two states are
			// modelled and not driven.
			transitions: map[string][]string{"executing": {"completed", "failed", "cancelled"}, "waiting": {"completed", "failed", "cancelled"}},
			why:         "a step ends with the run that contains it, and a completed step is not executed again",
		},
		"SharedInviteStatus": {
			terminal: []string{"accepted", "declined", "revoked"},
			// Pending does NOT lead to declined, and this said it did until a
			// driver asked the product. Declining is the invited organization's
			// answer, and it has nothing to answer until the host has approved
			// the invitation and it has reached them; DeclineSharedInvite is
			// approved-to-declined and there is no operation anywhere that
			// declines a pending one. The host refusing at that stage is denying,
			// which lands on revoked — the same state revoking reaches from
			// approved, by a different operation and a different event.
			transitions: map[string][]string{"pending": {"approved", "revoked"}, "approved": {"accepted", "declined", "revoked"}},
			why:         "acceptance creates the shared channel and is not undone by this machine; declining and revoking end the invitation. Expiry is a deadline read against the clock rather than a stored state, which is why fourteen days is enforced at approval and acceptance instead of appearing here",
		},
		"SavedItemState": {
			terminal:    []string{},
			transitions: map[string][]string{"in_progress": {"completed", "archived"}, "completed": {"in_progress", "archived"}, "archived": {"in_progress", "completed"}},
			why:         "a member's own saved item moves freely; marking something done is not a promise it stays done",
		},
		"InviteRequestStatus": {
			terminal: []string{"accepted", "denied", "revoked"},
			// Denied and revoked are not interchangeable, and this machine had
			// them both reachable from both states. The service says which is
			// which: "denied is the answer to a request, revoked is the
			// withdrawal of an answer already given". So a pending request is
			// denied and never revoked, and an approved one is revoked and
			// never denied — one operation, AdminDenyInviteRequest, and the
			// state it lands on depends on where it started.
			//
			// The driver reported both edges, but only after it was made to
			// check that an accepted transition ARRIVES: while it required no
			// more than the absence of an error, denying a pending request
			// satisfied "pending may become revoked" by landing on denied.
			transitions: map[string][]string{"pending": {"approved", "denied"}, "approved": {"accepted", "revoked"}},
			why:         "an accepted invitation has produced an account; a denied or revoked one is over",
		},
		"AppApprovalStatus": {
			terminal:    []string{"cancelled"},
			standing:    []string{"approved", "restricted"},
			transitions: map[string][]string{"requested": {"approved", "restricted", "cancelled"}, "approved": {"restricted"}, "restricted": {"approved"}},
			why:         "cancelling withdraws the request itself and nothing revives it. Approval and restriction never finish, and that is the product's rule rather than an omission: AdminCancelAppRequest refuses anything but a request nobody has decided, because writing cancelled over an approval reads as \"the request went away\" while the app stays installed and approved. So an approved app oscillates between approved and restricted for as long as the workspace exists.",
		},
		"ExternalUploadStatus": {
			terminal:    []string{"completed"},
			transitions: map[string][]string{"pending": {"uploaded"}, "uploaded": {"completed"}},
			why:         "a completed upload is a durable file; the ticket that produced it is spent",
		},
	}
}

// TestEveryLifecycleIsDeclared derives the status types from the domain, so a
// new one cannot arrive without a machine.
func TestEveryLifecycleIsDeclared(t *testing.T) {
	declared := machines()
	found := lifecycleTypes(t)
	if len(found) == 0 {
		t.Fatal("no lifecycle types were found at all, which means the scan is broken rather than that the domain has none")
	}
	for name := range found {
		if _, ok := declared[name]; !ok {
			t.Errorf("domain.%s is a lifecycle with no declared machine. Say which states it has, which are terminal, and what may follow what: a status nobody wrote rules for is a status with no rules.", name)
		}
	}
	for name := range declared {
		if _, ok := found[name]; !ok {
			t.Errorf("%s is declared here and no longer exists in the domain: remove it rather than leaving a machine for a lifecycle that is gone", name)
		}
	}
}

// TestEveryStateIsAccountedFor requires the declared machine to name exactly
// the states the domain declares, so a constant added to one and not the other
// fails here instead of becoming a state with no rules.
func TestEveryStateIsAccountedFor(t *testing.T) {
	found := lifecycleTypes(t)
	for name, model := range machines() {
		states, ok := found[name]
		if !ok {
			continue
		}
		accounted := map[string]bool{}
		for _, state := range model.terminal {
			accounted[state] = true
		}
		for _, state := range model.standing {
			accounted[state] = true
		}
		for from, to := range model.transitions {
			accounted[from] = true
			for _, target := range to {
				accounted[target] = true
			}
		}
		for _, state := range states {
			if !accounted[state] {
				t.Errorf("domain.%s has the state %q and the machine says nothing about it: it is either unreachable or a rule nobody wrote", name, state)
			}
		}
		for state := range accounted {
			if !contains(states, state) {
				t.Errorf("the machine for %s names the state %q, which the domain does not declare", name, state)
			}
		}
	}
}

// TestTerminalStatesAreFinal is the property the whole gate exists for: nothing
// leaves a terminal state, and every terminal state can be reached.
//
// A terminal state with a way out is not terminal, and a promise the product
// makes elsewhere — a revoked invitation cannot be accepted — is then false in
// the model that is supposed to justify it. A terminal state nothing can reach
// is a dead branch that no code path can produce.
func TestTerminalStatesAreFinal(t *testing.T) {
	for name, model := range machines() {
		t.Run(name, func(t *testing.T) {
			for _, final := range model.terminal {
				if onward, ok := model.transitions[final]; ok && len(onward) > 0 {
					t.Errorf("%s calls %q terminal and also lets it become %v", name, final, onward)
				}
			}
			reachable := map[string]bool{}
			for _, targets := range model.transitions {
				for _, target := range targets {
					reachable[target] = true
				}
			}
			for _, final := range model.terminal {
				if !reachable[final] {
					t.Errorf("%s calls %q terminal and nothing can reach it", name, final)
				}
			}
			if len(model.terminal) == 0 && strings.TrimSpace(model.why) == "" {
				t.Errorf("%s declares no terminal state and does not say why", name)
			}
		})
	}
}

// TestEveryStateCanReachATerminalStateOrIsCyclic walks the machine and reports
// a state from which nothing can ever finish, which is a lifecycle that can
// strand a row for ever.
func TestEveryStateCanReachATerminalStateOrIsCyclic(t *testing.T) {
	for name, model := range machines() {
		if len(model.terminal) == 0 {
			continue
		}
		final := map[string]bool{}
		for _, state := range model.terminal {
			final[state] = true
		}
		standing := map[string]bool{}
		for _, state := range model.standing {
			standing[state] = true
		}
		for from := range model.transitions {
			if standing[from] {
				continue
			}
			if !canReach(model, from, final, map[string]bool{}) {
				t.Errorf("%s can sit in %q with no sequence of transitions that ever finishes, and it is not declared standing", name, from)
			}
		}
		// A standing state that can in fact finish is a stale exemption.
		for _, state := range model.standing {
			if canReach(model, state, final, map[string]bool{}) {
				t.Errorf("%s calls %q standing and it can reach a terminal state after all: remove the exemption", name, state)
			}
		}
	}
}

func canReach(model machine, from string, final map[string]bool, seen map[string]bool) bool {
	if final[from] {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for _, next := range model.transitions[from] {
		if canReach(model, next, final, seen) {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// lifecycleTypes reads the domain for `type X Status string` and the constants
// declared with it.
func lifecycleTypes(t *testing.T) map[string][]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "internal", "domain", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	states := map[string][]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			ident, ok := spec.Type.(*ast.Ident)
			if !ok || ident.Name != "string" {
				return true
			}
			if isLifecycleName(spec.Name.Name) {
				names[spec.Name.Name] = true
			}
			return true
		})
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || value.Type == nil {
					continue
				}
				typeName, ok := value.Type.(*ast.Ident)
				if !ok || !isLifecycleName(typeName.Name) {
					continue
				}
				for _, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}
					states[typeName.Name] = append(states[typeName.Name], unquoted)
				}
			}
		}
	}
	result := map[string][]string{}
	for name := range names {
		values := states[name]
		sort.Strings(values)
		result[name] = values
	}
	return result
}

// isLifecycleName recognises a lifecycle by the shape of its type name. That is
// a real limit — a lifecycle named something else is invisible here — and it is
// stated rather than pretended away.
func isLifecycleName(name string) bool {
	return strings.HasSuffix(name, "Status") || strings.HasSuffix(name, "State")
}
