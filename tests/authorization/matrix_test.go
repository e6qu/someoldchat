// Package authorization holds the role-and-tier matrix.
//
// Journey 12 states that "authorization tests attempt every action as owner,
// admin, member, guest" and no such test existed. Scope enforcement was
// exhaustive — 302 routes — and role enforcement was not tested at all, which
// is the hole a single-channel guest walked through to join every public
// channel in the workspace.
//
// The matrix is derived rather than listed: it reflects over chatapi.Service,
// so a method that arrives without a declared authority fails here instead of
// quietly defaulting to "anybody may".
package authorization

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// authority is the least standing a caller must hold for an operation to get
// past its own front door. It says nothing about arguments: a caller who holds
// the authority can still be refused for naming a conversation that does not
// exist.
type authority int

const (
	// authorityCredential marks an operation that authenticates something other
	// than a workspace member — a token, an OAuth client, an app configuration.
	// There is no tier to hold, so the matrix has nothing to assert.
	authorityCredential authority = iota
	// authorityAnyMember is the default for member-facing operations: any
	// active member of the workspace, including both guest tiers.
	authorityAnyMember
	// authorityNotGuest is an operation Slack keeps away from guests, who reach
	// channels by being added to them rather than by naming one.
	authorityNotGuest
	// authorityAdmin and authorityOwner are the administrative tiers.
	authorityAdmin
	authorityOwner
)

func (a authority) String() string {
	switch a {
	case authorityCredential:
		return "credential"
	case authorityAnyMember:
		return "any-member"
	case authorityNotGuest:
		return "not-guest"
	case authorityAdmin:
		return "admin"
	case authorityOwner:
		return "owner"
	}
	return "unknown"
}

// tier is one caller the matrix drives every operation as.
type tier struct {
	name string
	user domain.UserID
	// holds reports the authorities this tier satisfies.
	holds func(authority) bool
}

func tiers() []tier {
	member := func(a authority) bool { return a <= authorityNotGuest }
	guest := func(a authority) bool { return a <= authorityAnyMember }
	return []tier{
		{name: "owner", user: "U-owner", holds: func(a authority) bool { return true }},
		{name: "admin", user: "U-admin", holds: func(a authority) bool { return a <= authorityAdmin }},
		{name: "member", user: "U-member", holds: member},
		{name: "multi-channel-guest", user: "U-guest-multi", holds: guest},
		{name: "single-channel-guest", user: "U-guest-single", holds: guest},
		// A deactivated account holds nothing at all: it is not a member.
		{name: "deactivated", user: "U-gone", holds: func(authority) bool { return false }},
		// An identifier belonging to no account holds nothing either, which is
		// what stops an operation treating an unknown caller as a member.
		{name: "stranger", user: "U-stranger", holds: func(authority) bool { return false }},
	}
}

func TestEveryOperationDeclaresItsAuthority(t *testing.T) {
	declared := authorityMatrix()
	serviceType := reflect.TypeOf((*chatapi.Service)(nil)).Elem()
	for index := range serviceType.NumMethod() {
		name := serviceType.Method(index).Name
		if _, ok := declared[name]; !ok {
			t.Errorf("chatapi.Service.%s declares no authority. Add it to authorityMatrix: an operation with no declared tier is one nobody has decided who may call.", name)
		}
	}
	known := make(map[string]struct{}, serviceType.NumMethod())
	for index := range serviceType.NumMethod() {
		known[serviceType.Method(index).Name] = struct{}{}
	}
	for name := range declared {
		if _, ok := known[name]; !ok {
			t.Errorf("authorityMatrix names %s, which chatapi.Service no longer declares", name)
		}
	}
}

// TestATierBelowTheRequirementIsRefusedForItsStanding drives every member-scoped
// operation as every tier that does not hold its authority, and requires the
// refusal to be about standing rather than about arguments.
//
// The arguments are zero values on purpose. An operation must decide who is
// asking before it examines what they asked for; one that validates first tells
// an unauthorized caller about its argument shapes, and — worse — cannot be
// distinguished from one that never checks standing at all.
func TestATierBelowTheRequirementIsRefusedForItsStanding(t *testing.T) {
	declared := authorityMatrix()
	serviceType := reflect.TypeOf((*chatapi.Service)(nil)).Elem()
	for index := range serviceType.NumMethod() {
		method := serviceType.Method(index)
		required := declared[method.Name]
		if required == authorityCredential {
			continue
		}
		for _, caller := range tiers() {
			if caller.holds(required) {
				continue
			}
			t.Run(method.Name+"/"+caller.name, func(t *testing.T) {
				err, decisive := probe(t, method.Name, caller.user)
				if err == nil {
					t.Fatalf("%s answered a %s, which holds less than %s", method.Name, caller.name, required)
				}
				if decisive {
					// "Not found" is a real refusal about standing — Slack
					// declines to confirm that something a caller may not see
					// exists — but it is also what an operation says when the
					// thing genuinely is not there. Those are indistinguishable
					// from one answer, so ask a caller who holds the authority:
					// if the owner is refused the very same way, the refusal is
					// about the argument and this pair proves nothing.
					//
					// Without this, stripping every guard from an operation left
					// the suite green for eighty seam methods, because the call
					// then ran on to its own "not found" and the matrix accepted
					// that as enforcement. The guard-mutation gate is what
					// showed it.
					if err != nil && errors.Is(err, store.ErrNotFound) {
						holder, holderDecisive := probe(t, method.Name, holderOf(required))
						if holderDecisive && holder != nil && holder.Error() == err.Error() {
							if _, declared := refusalDoesNotDistinguishTheHolder()[method.Name]; !declared {
								t.Fatalf("%s refuses a %s and a %s alike with %v, so the refusal is about the argument and not about standing. Give the fixture an object this operation can find, or add it to refusalDoesNotDistinguishTheHolder.", method.Name, caller.name, holderOf(required), err)
							}
							return
						}
					}
					return
				}
				// The probe passes zero arguments, so a method that checks a
				// required argument before it checks the caller refuses this
				// call for the argument and never reaches its own front door.
				// That is a limit of the probe rather than a hole in the
				// product — the authority check is there and does run for a
				// caller who supplies valid arguments — but it means this pair
				// proves nothing, and a pair that proves nothing must say so
				// out loud rather than counting as a pass.
				if _, declared := inconclusiveStanding()[method.Name]; !declared {
					t.Fatalf("%s refused a %s with %v, which is not a refusal about standing. Either it authorizes after validating — in which case add it to inconclusiveStanding and raise the ceiling by taking somebody else's place — or it never checks standing at all.", method.Name, caller.name, err)
				}
			})
		}
	}
}

// holderOf names a caller who holds the given authority, used to tell a
// refusal about standing apart from a refusal about arguments.
func holderOf(required authority) domain.UserID {
	for _, candidate := range tiers() {
		if candidate.user == "U-owner" && candidate.holds(required) {
			return candidate.user
		}
	}
	return "U-owner"
}

// isStandingRefusal reports the refusals that mean "not you". store.ErrNotFound
// is one of them: an operation may answer that a conversation or member the
// caller cannot see does not exist, which is Slack's own way of not confirming
// that something is there.
func isStandingRefusal(err error) bool {
	switch {
	case errors.Is(err, service.ErrNotWorkspaceAdmin),
		errors.Is(err, service.ErrUserIsRestricted),
		errors.Is(err, service.ErrUserIsUltraRestricted),
		errors.Is(err, service.ErrNotInConversation),
		errors.Is(err, service.ErrBarrieredFromMember),
		errors.Is(err, store.ErrNotFound):
		return true
	}
	return false
}

// invoke calls one method with zero arguments, filling only the context, the
// workspace and the caller. Reflection rather than a written call per method:
// there are four hundred of them, and a hand-written list is the thing this
// gate exists to replace.
func invoke(t *testing.T, messages service.Messages, name string, caller domain.UserID, chosen filling) error {
	t.Helper()
	value := reflect.ValueOf(messages).MethodByName(name)
	if !value.IsValid() {
		t.Fatalf("service.Messages has no method %s", name)
	}
	signature := value.Type()
	arguments := make([]reflect.Value, signature.NumIn())
	workspaceFilled, callerFilled := false, false
	for index := range signature.NumIn() {
		argument := signature.In(index)
		switch {
		case argument == reflect.TypeOf((*context.Context)(nil)).Elem():
			arguments[index] = reflect.ValueOf(context.Background())
		case argument == reflect.TypeOf(domain.WorkspaceID("")) && !workspaceFilled:
			arguments[index] = reflect.ValueOf(domain.WorkspaceID("T1"))
			workspaceFilled = true
		case argument == reflect.TypeOf(domain.UserID("")) && !callerFilled:
			arguments[index] = reflect.ValueOf(caller)
			callerFilled = true
		default:
			arguments[index] = fixtureArgument(argument, caller, chosen)
		}
	}
	results := value.Call(arguments)
	for _, result := range results {
		if result.Type() == reflect.TypeOf((*error)(nil)).Elem() {
			if result.IsNil() {
				return nil
			}
			return result.Interface().(error)
		}
	}
	return nil
}

// filling is how much of a signature the probe fills in. They are tried richest
// first, and a leaner one is reached only when the richer one produced an answer
// that is not about standing.
//
// One filling is not enough because reflection sees types, not meanings. A
// domain.MessageTimestamp is a message to one operation and a thread root to
// the next: handing DispatchSlashCommand the seeded message's timestamp makes
// it refuse everybody with "slash commands cannot be invoked in threads", which
// says nothing about who called it. Rather than exempt that method by name — a
// list that grows and never shrinks — the probe drops the argument it cannot
// know the meaning of and asks again.
type filling int

const (
	fillingFixture filling = iota
	fillingWithoutTimestamps
	fillingBare
)

func fillings() []filling { return []filling{fillingFixture, fillingWithoutTimestamps, fillingBare} }

// probe calls one operation as one caller, trying each filling until an answer
// is decisive: either the operation answered, or it refused for standing.
//
// Each attempt gets its own fixture. An earlier attempt may have changed the
// workspace, and a probe whose second question is asked of the state its first
// question left behind is measuring something it did not intend.
func probe(t *testing.T, name string, caller domain.UserID) (err error, decisive bool) {
	t.Helper()
	for _, chosen := range fillings() {
		err = invoke(t, newFixture(t), name, caller, chosen)
		if err == nil || isStandingRefusal(err) {
			return err, true
		}
	}
	return err, false
}

// fixtureArgument supplies a value the fixture actually holds, so a call
// reaches its own front door instead of dying at its argument check.
//
// Every argument beyond the workspace and the caller used to be zeroed. An
// operation that validates before it authorizes therefore refused all seven
// tiers with "page limit must be positive" or "invalid conversation", which
// looks like enforcement and is not: the guard-mutation gate in tests/mutation
// could delete the authorization from such an operation with this suite still
// green. That is what the inconclusive set below was recording, one method at
// a time, and it is the reason it had 39 members.
//
// A type the fixture has nothing for is still zeroed. That is honest — the
// operation is then inconclusive for a reason this file can state — and it is
// where the remaining members of the inconclusive set come from.
func fixtureArgument(argument reflect.Type, caller domain.UserID, chosen filling) reflect.Value {
	if chosen == fillingBare {
		return reflect.Zero(argument)
	}
	switch argument {
	case reflect.TypeOf(domain.UserID("")):
		// The second user in a signature is the target, not the caller, and it
		// must be somebody else. Several operations let a member act on their
		// own record and nobody else's — WorkspaceMembership is one — so a
		// target equal to the caller asks a different question than the one
		// this matrix declares an authority for, and answering it here reported
		// a member reaching an admin-only read.
		if caller == "U-member" {
			return reflect.ValueOf(domain.UserID("U-owner"))
		}
		return reflect.ValueOf(domain.UserID("U-member"))
	case reflect.TypeOf(domain.ConversationID("")):
		return reflect.ValueOf(domain.ConversationID("C1"))
	case reflect.TypeOf(domain.MessageTimestamp("")):
		if chosen == fillingWithoutTimestamps {
			return reflect.Zero(argument)
		}
		return reflect.ValueOf(fixtureMessageTimestamp)
	case reflect.TypeOf(domain.PageRequest{}):
		return reflect.ValueOf(domain.PageRequest{Limit: 10})
	}
	return reflect.Zero(argument)
}

func newFixture(t *testing.T) service.Messages {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	requireSeed(t, repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test", Domain: "test"}))
	requireSeed(t, repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}))
	for _, person := range []struct {
		id          domain.UserID
		role        domain.WorkspaceRole
		restricted  bool
		ultra       bool
		deactivated bool
	}{
		{id: "U-owner", role: domain.WorkspaceRoleOwner},
		{id: "U-admin", role: domain.WorkspaceRoleAdmin},
		{id: "U-member", role: domain.WorkspaceRoleMember},
		{id: "U-guest-multi", role: domain.WorkspaceRoleMember, restricted: true},
		{id: "U-guest-single", role: domain.WorkspaceRoleMember, ultra: true},
		{id: "U-gone", role: domain.WorkspaceRoleMember, deactivated: true},
	} {
		user := domain.User{ID: person.id, WorkspaceID: "T1", Name: string(person.id), Email: string(person.id) + "@example.test"}
		// Everyone is created as an ordinary member and promoted afterwards:
		// CreateUser refuses to mint an owner, which is the store keeping
		// ownership a transition rather than an initial condition.
		membership := domain.WorkspaceMembership{
			WorkspaceID: "T1", UserID: person.id, Role: domain.WorkspaceRoleMember, Active: true,
			Restricted: person.restricted, UltraRestricted: person.ultra,
		}
		requireSeed(t, repository.CreateUser(ctx, user, membership, events.Event{
			ID: domain.EventID("E-" + person.id), WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC(),
		}))
		if person.role != domain.WorkspaceRoleMember {
			requireSeed(t, repository.SetWorkspaceRole(ctx, "T1", person.id, person.role, events.Event{
				ID: domain.EventID("E-role-" + person.id), WorkspaceID: "T1", Topic: "workspace.role_changed", CreatedAt: time.Now().UTC(),
			}))
		}
		requireSeed(t, repository.SeedConversationMember("C1", person.id))
		if person.deactivated {
			requireSeed(t, repository.SetUserDeleted(ctx, "T1", person.id, true, events.Event{
				ID: domain.EventID("E-gone-" + person.id), WorkspaceID: "T1", Topic: "user.removed", CreatedAt: time.Now().UTC(),
			}))
		}
	}
	// A real message in the seeded conversation, so an operation that names one
	// reaches its authorization instead of being refused for naming nothing.
	// A message's public timestamp is derived from its creation instant rather
	// than stored beside it, so the instant is what pins the identifier.
	created := time.Unix(1700000000, 100*1000).UTC()
	requireSeed(t, repository.CreateMessage(ctx, domain.Message{
		ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U-member",
		Text: "seeded", CreatedAt: created,
	}, events.Event{
		ID: "E-message", WorkspaceID: "T1", Topic: "message.created", CreatedAt: created,
	}, ""))
	if got := domain.NewMessageTimestamp(created); got != fixtureMessageTimestamp {
		t.Fatalf("the seeded message's timestamp is %s, and the probe hands out %s", got, fixtureMessageTimestamp)
	}
	return service.Messages{Store: repository}
}

// fixtureMessageTimestamp names the one seeded message.
const fixtureMessageTimestamp domain.MessageTimestamp = "1700000000.000100"

func requireSeed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestEveryInconclusiveMethodReallyIsInconclusive stops the exemption list
// being used to hide a failure.
//
// A method is inconclusive only if the probe cannot tell the tiers apart: the
// owner, who holds everything, must be refused exactly as the stranger is. If
// the two answers differ, the probe can see the difference after all and the
// method has no business on the list.
func TestEveryInconclusiveMethodReallyIsInconclusive(t *testing.T) {
	for name := range inconclusiveStanding() {
		t.Run(name, func(t *testing.T) {
			owner, ownerDecisive := probe(t, name, "U-owner")
			stranger, strangerDecisive := probe(t, name, "U-stranger")
			if ownerDecisive || strangerDecisive {
				t.Fatalf("%s is decidable under some filling: remove it from inconclusiveStanding", name)
			}
			switch {
			case owner == nil && stranger == nil:
				t.Fatalf("%s answered both an owner and a stranger; it is not inconclusive, it is unguarded", name)
			case owner == nil || stranger == nil:
				t.Fatalf("%s answered one of the two and refused the other, so the probe can tell them apart: remove it from inconclusiveStanding", name)
			case owner.Error() != stranger.Error():
				t.Fatalf("%s refuses an owner with %q and a stranger with %q, so the probe can tell them apart: remove it from inconclusiveStanding", name, owner, stranger)
			}
		})
	}
}

// TestTheInconclusiveSetOnlyShrinks holds the count so the probe's blind spot
// cannot grow quietly. Closing one means giving the method arguments valid
// enough to reach its own front door, or moving its authority check ahead of
// its argument check.
func TestTheInconclusiveSetOnlyShrinks(t *testing.T) {
	if len(inconclusiveStanding()) > inconclusiveStandingCeiling {
		t.Fatalf("the probe cannot decide %d operations, above the ceiling of %d", len(inconclusiveStanding()), inconclusiveStandingCeiling)
	}
	if len(inconclusiveStanding()) < inconclusiveStandingCeiling {
		t.Fatalf("the probe now decides all but %d operations: lower inconclusiveStandingCeiling to match, so the ground gained is kept", len(inconclusiveStanding()))
	}
}

// TestEveryIndistinguishableRefusalReallyIsIndistinguishable stops the list
// being used to excuse a method that has since been given a fixture object.
//
// A method belongs there only while a caller who holds the authority is
// refused exactly as one who does not. The moment the fixture lets the holder
// through, the refusal distinguishes them and the entry is a stale exemption.
func TestEveryIndistinguishableRefusalReallyIsIndistinguishable(t *testing.T) {
	declared := authorityMatrix()
	for name := range refusalDoesNotDistinguishTheHolder() {
		t.Run(name, func(t *testing.T) {
			required := declared[name]
			holder, holderDecisive := probe(t, name, holderOf(required))
			if !holderDecisive {
				return
			}
			// The entry earns its place if ANY tier below the requirement is
			// still refused exactly as the holder is. A method may refuse a
			// member with "not a workspace administrator" — which does
			// distinguish — while refusing a stranger with the holder's own
			// "not found", and that second pair is the one proving nothing.
			for _, caller := range tiers() {
				if caller.holds(required) {
					continue
				}
				beneath, decisive := probe(t, name, caller.user)
				if !decisive || beneath == nil || holder == nil {
					continue
				}
				if holder.Error() == beneath.Error() {
					return
				}
			}
			t.Fatalf("%s now answers a %s differently from every tier beneath it, so its refusal does distinguish them: remove it from refusalDoesNotDistinguishTheHolder", name, holderOf(required))
		})
	}
}

// TestTheIndistinguishableRefusalSetOnlyShrinks holds the count, so the
// weakness the guard-mutation gate exposed cannot grow back quietly.
func TestTheIndistinguishableRefusalSetOnlyShrinks(t *testing.T) {
	actual := len(refusalDoesNotDistinguishTheHolder())
	if actual > indistinguishableRefusalCeiling {
		t.Fatalf("%d operations refuse a holder and a non-holder alike, above the ceiling of %d", actual, indistinguishableRefusalCeiling)
	}
	if actual < indistinguishableRefusalCeiling {
		t.Fatalf("only %d operations do now: lower indistinguishableRefusalCeiling to %d, so the ground gained is kept", actual, actual)
	}
}
