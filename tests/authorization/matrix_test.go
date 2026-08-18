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
			arguments[index] = fixtureArgument(argument, caller, chosen, name)
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
func fixtureArgument(argument reflect.Type, caller domain.UserID, chosen filling, method string) reflect.Value {
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
	case reflect.TypeOf(domain.WorkspaceID("")):
		// The workspace and caller are already filled by invoke, so a
		// WorkspaceID reaching here is a target organization. InviteShared needs
		// a real one it has no outstanding invitation to, or it dies at its
		// argument check for everyone and the guard-mutation gate can strip its
		// membership guard unnoticed. Other operations that take a target
		// organization are left zeroed, so this changes only the one it names.
		if method == "InviteShared" {
			return reflect.ValueOf(domain.WorkspaceID("T4"))
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(domain.ConversationID("")):
		// ConvertGroupDirectToPrivate needs a DM or group DM, not the seeded
		// channel, so it alone is handed the group direct message; every other
		// operation keeps the channel it can act on.
		if method == "ConvertGroupDirectToPrivate" {
			return reflect.ValueOf(fixtureGroupDMID)
		}
		return reflect.ValueOf(domain.ConversationID("C1"))
	case reflect.TypeOf(domain.CanvasID("")):
		return reflect.ValueOf(fixtureCanvasID)
	case reflect.TypeOf(domain.ListID("")):
		return reflect.ValueOf(fixtureListID)
	case reflect.TypeOf(domain.ListItemID("")):
		return reflect.ValueOf(fixtureListItemID)
	case reflect.TypeOf([]domain.ListItemID(nil)):
		// A non-empty selection, so a bulk delete reaches its own front door
		// instead of being refused for an empty payload.
		return reflect.ValueOf([]domain.ListItemID{fixtureListItemID})
	case reflect.TypeOf(domain.CallID("")):
		return reflect.ValueOf(fixtureCallID)
	case reflect.TypeOf(domain.ReminderID("")):
		return reflect.ValueOf(fixtureReminderID)
	case reflect.TypeOf(domain.LaterReminderID("")):
		return reflect.ValueOf(fixtureLaterReminderID)
	case reflect.TypeOf(domain.BookmarkID("")):
		return reflect.ValueOf(fixtureBookmarkID)
	case reflect.TypeOf(domain.UserGroupID("")):
		return reflect.ValueOf(fixtureUserGroupID)
	case reflect.TypeOf(domain.FileID("")):
		return reflect.ValueOf(fixtureFileID)
	case reflect.TypeOf(domain.WorkflowID("")):
		return reflect.ValueOf(fixtureWorkflowID)
	case reflect.TypeOf(domain.AppID("")):
		return reflect.ValueOf(fixtureAppID)
	case reflect.TypeOf(domain.SharedInviteID("")):
		// The operation decides which invitation it needs: approving and
		// denying act on a pending one, revoking on an approved one. Handing
		// the wrong-state invitation would refuse the holder for the state
		// rather than for standing, which is the trap this fixture avoids.
		if method == "RevokeSharedInvite" {
			return reflect.ValueOf(fixtureApprovedInviteID)
		}
		return reflect.ValueOf(fixtureSharedInviteID)
	case reflect.TypeOf(domain.WorkflowTriggerID("")):
		return reflect.ValueOf(fixtureWorkflowTriggerD)
	case reflect.TypeOf(domain.WorkflowRunID("")):
		return reflect.ValueOf(fixtureWorkflowRunID)
	case reflect.TypeOf(domain.SavedItemID("")):
		// RemoveSavedItem acts on the caller's own saved item, so the holder is
		// handed its own; other operations that name one keep the member's.
		if method == "RemoveSavedItem" {
			return reflect.ValueOf(fixtureHolderSavedItemID)
		}
		return reflect.ValueOf(fixtureSavedItemID)
	case reflect.TypeOf(domain.ScheduledStatusID("")):
		return reflect.ValueOf(fixtureScheduledStatusID)
	case reflect.TypeOf(domain.ListDownloadID("")):
		return reflect.ValueOf(fixtureListDownloadID)
	case reflect.TypeOf(domain.ActivitySavedViewID("")):
		return reflect.ValueOf(fixtureActivityViewID)
	case reflect.TypeOf(domain.SidebarSectionID("")):
		return reflect.ValueOf(fixtureSidebarSectionID)
	case reflect.TypeOf([]domain.ConversationID(nil)):
		// A non-empty channel selection for the user-group operations that add or
		// remove channels; named by method so the sharing operations that also
		// take a channel slice keep the empty one they are inconclusive with.
		switch method {
		case "AddUserGroupChannels", "RemoveUserGroupChannels":
			return reflect.ValueOf([]domain.ConversationID{"C1"})
		}
		return reflect.Zero(argument)
	case reflect.TypeOf([]domain.WorkspaceID(nil)):
		if method == "AdminAddUserGroupTeams" {
			return reflect.ValueOf([]domain.WorkspaceID{"T2"})
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(domain.MessageID("")):
		return reflect.ValueOf(fixtureMessageID)
	case reflect.TypeOf(domain.MessageTimestamp("")):
		if chosen == fillingWithoutTimestamps {
			return reflect.Zero(argument)
		}
		return reflect.ValueOf(fixtureMessageTimestamp)
	case reflect.TypeOf(domain.PageRequest{}):
		return reflect.ValueOf(domain.PageRequest{Limit: 10})
	case reflect.TypeOf(domain.ListColumnType("")):
		// A real column type, so AddListColumn passes schema validation and the
		// holder's write grant carries it to success.
		return reflect.ValueOf(domain.ListColumnText)
	case reflect.TypeOf(domain.LaterReminderRequest{}):
		// A valid personal reminder edit, so UpdateLaterReminder — acting on the
		// holder's own seeded reminder after authorizeWorkspace — reaches success
		// while a caller below membership is refused for standing.
		return reflect.ValueOf(domain.LaterReminderRequest{
			Target: domain.LaterReminderPersonal, Text: "updated fixture reminder",
			Recurrence: domain.ReminderOnce, DueAt: time.Now().Add(24 * time.Hour).UTC(),
		})
	case reflect.TypeOf(domain.WorkspaceRole("")):
		// A real role, so SetUserRole reaches success promoting the seeded member
		// rather than dying on an empty role. Named by method so the provisioning
		// operations that also take a role — which authenticate a credential, not
		// a member — keep the empty role they are inconclusive with.
		if method == "SetUserRole" {
			return reflect.ValueOf(domain.WorkspaceRoleAdmin)
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(domain.NotificationLevel("")):
		// Named by method so SetSidebarSectionNotificationLevel, which needs a
		// sidebar section this fixture does not seed, is not handed a valid level
		// that would make it reach "not found" indistinguishably.
		if method == "SetConversationNotificationPreferences" {
			return reflect.ValueOf(domain.NotificationAll)
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(time.Time{}):
		// Scheduling needs an instant in the future; other operations that take a
		// time are content with the zero value, so only the schedulers are named.
		switch method {
		case "ScheduleMessage", "ScheduleMessageWithBlocks", "ScheduleMessageWithBlocksAndAttachments":
			return reflect.ValueOf(time.Now().Add(24 * time.Hour).UTC())
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(int(0)):
		// A valid retention duration; other int arguments keep the zero value.
		if method == "SetConversationRetention" {
			return reflect.ValueOf(30)
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(uint64(0)):
		// The fixture workflow's version, so DeleteWorkflow's optimistic-version
		// check matches and the manager reaches the delete rather than a conflict.
		if method == "DeleteWorkflow" {
			return reflect.ValueOf(uint64(1))
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(int64(0)):
		// The version the fixture canvas holds a revision at, so
		// RestoreCanvasRevision finds one to restore rather than answering the
		// holder and a stranger alike with not-found.
		if method == "RestoreCanvasRevision" {
			return reflect.ValueOf(int64(1))
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(domain.AutomationPermission{}):
		// A valid permission, so SetTriggerPermission — whose trigger and app the
		// fixture already match — reaches success rather than an invalid-payload
		// refusal. Named by method so the function-permission operation, which is
		// still refused at its missing function, keeps the empty value.
		if method == "SetTriggerPermission" {
			return reflect.ValueOf(domain.AutomationPermission{PermissionType: domain.PermissionEveryone})
		}
		return reflect.Zero(argument)
	case reflect.TypeOf(""):
		// A plain string argument is meaningful only per operation, so it is
		// filled by name like the typed-target cases above and left empty for
		// every operation not named here. Supplying a blanket non-empty string
		// would change how unrelated operations answer their own validation.
		return fixtureStringArgument(method)
	case reflect.TypeOf(false):
		// A bool is meaningful only per operation too. SetConversationArchived
		// with false is a no-op on a conversation that is not archived — a state
		// error that is not about standing — so it is handed true, which archives
		// the seeded conversation and lets the holder reach success.
		if method == "SetConversationArchived" {
			return reflect.ValueOf(true)
		}
		return reflect.Zero(argument)
	}
	return reflect.Zero(argument)
}

// fixtureStringArgument supplies the one string an operation needs to reach its
// own front door as the holder. It names only the operations the matrix drives
// past argument validation; everything else keeps the empty string, which is
// honest about the probe not knowing that argument's meaning.
func fixtureStringArgument(method string) reflect.Value {
	switch method {
	case "AddReaction":
		// A name the holder has not used, so adding it succeeds rather than
		// colliding with the holder's own seeded reaction.
		return reflect.ValueOf("wave")
	case "RemoveReaction":
		return reflect.ValueOf(fixtureHolderReaction)
	case "UpdateListItem":
		// An empty cell array is a valid no-op update: it keeps the row's fields
		// and passes schema validation, so the holder reaches success.
		return reflect.ValueOf("[]")
	case "CommentOnListItem", "CommentOnCanvas":
		return reflect.ValueOf("a fixture comment")
	case "EditCanvas":
		// A single append the holder's write grant admits, so the edit succeeds
		// rather than dying on an empty change set.
		return reflect.ValueOf(`[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"x"}}]`)
	case "LookupCanvasSections":
		// An empty criteria object matches every section, so the read succeeds.
		return reflect.ValueOf("{}")
	case "UserByEmail":
		// The seeded member's own address, so the lookup finds a user.
		return reflect.ValueOf("U-member@example.test")
	case "RenameConversation":
		return reflect.ValueOf("renamed-fixture")
	case "UpdateCall":
		// Both the external id and the join URL are required and are the only
		// strings this operation takes, so one non-empty value reaches success.
		return reflect.ValueOf("https://example.test/updated-call")
	case "AddListColumn":
		// A column name; paired with the real column type above, the holder's
		// write grant carries the add to success.
		return reflect.ValueOf("Status")
	case "RemoveListColumn":
		// The key of the column the fixture list carries, so the remove finds one.
		return reflect.ValueOf("priority")
	case "ConvertGroupDirectToPrivate":
		// The name for the new private channel the group DM becomes.
		return reflect.ValueOf("converted-channel")
	case "PostEphemeral", "ScheduleMessage", "SaveDraft":
		// Message text, the only free string these take, so a member posting to
		// the seeded conversation reaches success.
		return reflect.ValueOf("a fixture message")
	case "PostEphemeralWithBlocks", "ScheduleMessageWithBlocks":
		// These take text and a blocks payload; a valid single-block layout reads
		// as valid for both, so the holder reaches success while the string stays
		// meaningful rather than a bare word standing in for JSON.
		return reflect.ValueOf(`[{"type":"section","text":{"type":"mrkdwn","text":"x"}}]`)
	}
	return reflect.ValueOf("")
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
	seedFixtureObjects(t, repository, created)
	return service.Messages{Store: repository}
}

// seedFixtureObjects gives the workspace one of each thing an operation can
// name. A failure here fails the fixture rather than the operation under test:
// a probe that silently ran without its objects would report the very blindness
// this exists to remove.
func seedFixtureObjects(t *testing.T, repository *memory.Store, at time.Time) {
	t.Helper()
	ctx := context.Background()
	event := func(id string, topic string) events.Event {
		return events.Event{ID: domain.EventID(id), WorkspaceID: "T1", Topic: topic, CreatedAt: at}
	}
	seed := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	seed("canvas", repository.CreateCanvas(ctx, domain.Canvas{
		ID: fixtureCanvasID, WorkspaceID: "T1", OwnerID: "U-member", Title: "fixture canvas",
		// A valid, empty document rather than the empty string: an operation that
		// decodes the document (EditCanvas, LookupCanvasSections) reaches its own
		// authorization instead of dying on unparseable content, so the holder's
		// write grant carries it to success where a stranger is refused for access.
		DocumentContent: `{"sections":[]}`,
		Version:         1, CreatedAt: at, UpdatedAt: at,
	}, event("E-canvas", "canvas.created")))
	seed("list", repository.CreateList(ctx, domain.List{
		ID: fixtureListID, WorkspaceID: "T1", OwnerID: "U-member", Name: "fixture list",
		// One column, so RemoveListColumn has one to remove; keyed "priority" so it
		// does not collide with the "Status" column AddListColumn adds.
		Schema:  `[{"id":"priority","key":"priority","name":"Priority","type":"text"}]`,
		Version: 1, CreatedAt: at, UpdatedAt: at,
	}, event("E-list", "list.created")))
	// The holder the differential asks is the workspace owner, and a document
	// belonging to a member is not theirs to read. Without a grant the holder
	// and the tier beneath are refused alike — "it is not there" both times —
	// which is the very blindness this fixture exists to remove. Ownership stays
	// with the member so "mine" and "anybody's" remain different questions; the
	// grant is what lets the holder through.
	seed("canvas access", repository.SetCanvasAccess(ctx, domain.CanvasAccess{
		CanvasID: fixtureCanvasID, EntityType: domain.GrantUser, EntityID: "U-owner", Access: domain.AccessWrite,
	}, event("E-canvas-access", "canvas.access_set")))
	seed("list access", repository.SetListAccess(ctx, domain.ListAccess{
		ListID: fixtureListID, EntityType: domain.GrantUser, EntityID: "U-owner", Access: domain.AccessWrite,
	}, event("E-list-access", "list.access_set")))
	// One edit to the canvas by the holder (now that it has a write grant), so a
	// revision exists at version 1 for RestoreCanvasRevision to restore. Without
	// a revision, restoring answers the holder and a stranger alike with the same
	// not-found.
	if err := (service.Messages{Store: repository}).EditCanvas(ctx, "T1", "U-owner", fixtureCanvasID,
		`[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"revised"}}]`); err != nil {
		t.Fatalf("seed canvas revision: %v", err)
	}
	// A finished list download, so GetListDownload has one to return to a member.
	seed("list download", repository.CreateListDownload(ctx, domain.ListDownload{
		ID: fixtureListDownloadID, ListID: fixtureListID, WorkspaceID: "T1", Status: "ready",
		URL: "https://example.test/download.csv", CreatedAt: at,
	}, event("E-list-download", "list.download_created")))
	// A row in that list, so the operations that read, update, assign, comment on,
	// attach a file to, or delete an item have one the holder — granted write on
	// the list above — can reach, while a caller below membership is refused at
	// requireListAccess. Created by the member, so "mine" and "anybody's" stay
	// different questions the way the list's own ownership does.
	seed("list item", repository.CreateListItem(ctx, domain.ListItem{
		ID: fixtureListItemID, ListID: fixtureListID, WorkspaceID: "T1", Fields: "[]",
		CreatedBy: "U-member", UpdatedBy: "U-member", Version: 1, CreatedAt: at, UpdatedAt: at,
	}, event("E-list-item", "list.item.created")))
	seed("call", repository.CreateCall(ctx, domain.Call{
		ID: fixtureCallID, WorkspaceID: "T1", ConversationID: "C1", ExternalUniqueID: "fixture-call",
		JoinURL: "https://example.test/call", Title: "fixture call", CreatedBy: "U-member",
		Participants: []domain.UserID{"U-member"}, StartedAt: at,
	}, event("E-call", "call.created")))
	// A reminder and a Later reminder belong to the HOLDER the differential asks
	// (U-owner), because these operations act on the caller's own reminder:
	// owned by anyone else, the holder is refused "not found" exactly as a
	// stranger is, which is the blindness this fixture removes rather than a
	// refusal about standing.
	seed("reminder", repository.CreateReminder(ctx, domain.Reminder{
		WorkspaceID: "T1", ID: fixtureReminderID, Creator: "U-owner", User: "U-owner",
		Text: "fixture reminder", Time: at.Add(time.Hour),
	}, event("E-reminder", "reminder.created")))
	seed("later reminder", repository.CreateLaterReminder(ctx, domain.LaterReminder{
		ID: fixtureLaterReminderID, WorkspaceID: "T1", Creator: "U-owner", UserID: "U-owner",
		Text: "fixture later reminder", Target: domain.LaterReminderPersonal, Recurrence: domain.ReminderOnce,
		DueAt: at.Add(time.Hour), CreatedAt: at,
	}, event("E-later-reminder", "later_reminder.created")))
	seed("bookmark", repository.CreateBookmark(ctx, domain.Bookmark{
		ID: fixtureBookmarkID, WorkspaceID: "T1", Conversation: "C1", Title: "fixture bookmark",
		Type: "link", Link: "https://example.test/bookmark", UpdatedBy: "U-member",
		CreatedAt: at, UpdatedAt: at,
	}, event("E-bookmark", "bookmark.created")))
	// A workflow names an app, and an app identifier the probe hands out with no
	// app behind it is worse than none: the holder is then refused "not found"
	// exactly as a stranger is, which is the blindness rather than a step out of
	// it. The app is real, owned by the member, with the manifest revision and
	// OAuth client the store requires before it will accept one.
	seed("app", repository.CreateApp(ctx, domain.App{
		ID: fixtureAppID, DevelopmentWorkspaceID: "T1", OwnerID: "U-member", Name: "Fixture app",
		ClientID: "fixture-client", SigningSecretHash: "fixture-signing-hash",
		SigningSecretCiphertext:     "fixture-signing-ciphertext",
		VerificationTokenHash:       "fixture-verification-hash",
		VerificationTokenCiphertext: "fixture-verification-ciphertext",
		ManifestVersion:             1, Distribution: "private", CreatedAt: at, UpdatedAt: at,
	}, domain.AppManifestRevision{
		AppID: fixtureAppID, Version: 1, CreatedBy: "U-member", Manifest: `{"display_information":{"name":"Fixture app"}}`, CreatedAt: at,
	}, domain.OAuthClient{
		ID: "fixture-client", AppID: fixtureAppID, SecretHash: "fixture-client-secret-hash",
	}))
	// The app is installed, not merely created. Three operations were recorded
	// as unreachable because an installation was believed to need the whole
	// OAuth redemption; CreateAppInstallation is the whole of it, and the
	// lifecycle fixtures have been doing this since they were written.
	seed("app installation", repository.CreateAppInstallation(ctx, domain.AppInstallation{
		AppID: fixtureAppID, WorkspaceID: "T1", Enabled: true, CreatedAt: at,
	}))
	// A live huddle in the seeded conversation, started by the holder so the
	// operations that act on "the huddle I am in" have one to find.
	if _, _, err := repository.StartHuddle(ctx, domain.Call{
		ID: fixtureHuddleID, WorkspaceID: "T1", Kind: domain.CallKindHuddle, ConversationID: "C1",
		ExternalUniqueID: "fixture-huddle", JoinURL: "https://example.test/huddle", Title: "fixture huddle",
		CreatedBy: "U-owner", Participants: []domain.UserID{"U-owner"}, StartedAt: at,
	}, event("E-huddle", "huddle.started"), event("E-huddle-join", "huddle.joined")); err != nil {
		t.Fatalf("seed huddle: %v", err)
	}
	// An external authorization token for the installed app, so an operation
	// that revokes one has one to revoke.
	seed("external auth token", repository.SetExternalAuthToken(ctx, domain.ExternalAuthToken{
		ID: "F-external-token", AppID: fixtureAppID, WorkspaceID: "T1", UserID: "U-owner",
		Provider: "fixture", Ciphertext: "fixture-ciphertext", CreatedAt: at,
	}, event("E-external-token", "app.external_auth_token_set")))
	seed("file", repository.CreateFile(ctx, domain.File{
		// Uploaded by the holder rather than by the member: a file has no grant
		// separate from its shares, so the uploader is the only thing that can
		// make it visible to the caller the differential asks. A file the holder
		// cannot see is answered "not found" exactly as a stranger is.
		ID: fixtureFileID, WorkspaceID: "T1", Uploader: "U-owner", Name: "fixture.txt",
		Title: "fixture file", MIMEType: "text/plain", BlobKey: "fixture-blob", Size: 4,
	}, event("E-file", "file.created")))
	seed("workflow", repository.CreateWorkflow(ctx, domain.WorkflowDefinition{
		ID: fixtureWorkflowID, WorkspaceID: "T1", AppID: fixtureAppID, OwnerID: "U-owner",
		CallbackID: "fixture-workflow", Title: "fixture workflow", Status: domain.WorkflowDraft,
		CreatedAt: at, UpdatedAt: at,
	}, event("E-workflow", "workflow.created")))
	// A shared invitation needs a second organization; a conversation cannot be
	// shared with the workspace that already holds it. The second workspace
	// exists for that alone — the matrix asks about standing, not federation.
	seed("second workspace", repository.SeedWorkspace(domain.Workspace{ID: "T2", Name: "other", Domain: "other"}))
	seed("third workspace", repository.SeedWorkspace(domain.Workspace{ID: "T3", Name: "third", Domain: "third"}))
	// A fourth organization with no outstanding invitation to C1, so InviteShared
	// can be driven to a real creation: T2 and T3 already hold invitations and a
	// second to either is refused for the state. Handing InviteShared a target it
	// can actually invite lets the holder through to success while a caller below
	// membership is still refused for standing — the pair the guard-mutation gate
	// needs to catch the membership check being deleted.
	seed("fourth workspace", repository.SeedWorkspace(domain.Workspace{ID: "T4", Name: "fourth", Domain: "fourth"}))
	// A live expiry: the fixture's deterministic `at` is in the past, so an
	// invitation expiring fourteen days after it is already lapsed, and
	// approving a lapsed invitation is refused for everyone — for the state, not
	// for standing, which is why approve/deny/revoke were all indistinguishable.
	inviteExpiry := time.Now().Add(14 * 24 * time.Hour).UTC()
	seed("shared invite", repository.CreateSharedInvite(ctx, domain.SharedInvite{
		ID: fixtureSharedInviteID, WorkspaceID: "T1", ConversationID: "C1", TargetWorkspaceID: "T2",
		TargetEmail: "invited@example.test", InvitedBy: "U-owner", Status: domain.SharedInvitePending,
		CreatedAt: at, ExpiresAt: inviteExpiry,
	}, event("E-invite", "conversation.shared_invite_sent")))
	// A second invitation driven to approved through the product, so revoking —
	// which acts on an approved invitation — has one in the state it needs.
	// Seeding an approved row directly is refused: CreateSharedInvite admits
	// only pending, which is the store enforcing the machine at creation.
	seed("approved shared invite", repository.CreateSharedInvite(ctx, domain.SharedInvite{
		ID: fixtureApprovedInviteID, WorkspaceID: "T1", ConversationID: "C1", TargetWorkspaceID: "T3",
		TargetEmail: "approved@example.test", InvitedBy: "U-owner", Status: domain.SharedInvitePending,
		CreatedAt: at, ExpiresAt: inviteExpiry,
	}, event("E-approved-invite", "conversation.shared_invite_sent")))
	if _, err := (service.Messages{Store: repository}).ApproveSharedInvite(ctx, "T1", "U-owner", fixtureApprovedInviteID); err != nil {
		t.Fatalf("driving the approved invitation: %v", err)
	}
	seed("workflow trigger", repository.SetWorkflowTrigger(ctx, domain.WorkflowTrigger{
		ID: fixtureWorkflowTriggerD, WorkflowID: fixtureWorkflowID, WorkspaceID: "T1", AppID: fixtureAppID,
		Title: "fixture trigger", Type: domain.WorkflowTriggerWebhook, Config: "{}", Enabled: true,
		Version: 1, CreatedAt: at, UpdatedAt: at,
	}, event("E-trigger", "workflow.trigger_set")))
	seed("workflow run", repository.CreateWorkflowRun(ctx, domain.WorkflowRun{
		ID: fixtureWorkflowRunID, WorkflowID: fixtureWorkflowID, WorkflowVersion: 1,
		TriggerID: fixtureWorkflowTriggerD, WorkspaceID: "T1", AppID: fixtureAppID, ActorID: "U-owner",
		ConversationID: "C1", Status: domain.WorkflowRunRunning, CreatedAt: at, UpdatedAt: at,
	}, nil, []events.Event{event("E-run", "workflow.run_started")}))
	// Things attached to the one seeded message, so an operation that removes one
	// finds something to remove. They belong to the member and not to the holder:
	// a pin the holder already owns makes AddPin answer "already exists", which is
	// neither an answer nor a refusal about standing, so the probe falls through
	// to its leaner filling and lands on "not found" for everybody — trading two
	// decidable operations for two indistinguishable ones.
	seed("reaction", repository.AddReaction(ctx, domain.Reaction{
		Message: fixtureMessageID, Name: "wave", UserID: "U-member", CreatedAt: at,
	}, event("E-reaction", "reaction.added")))
	seed("pin", repository.AddPin(ctx, domain.Pin{
		Message: fixtureMessageID, UserID: "U-member", CreatedAt: at,
	}, event("E-pin", "pin.added")))
	seed("star", repository.AddStar(ctx, domain.Star{
		Message:      domain.Message{ID: fixtureMessageID, WorkspaceID: "T1", Conversation: "C1"},
		Conversation: "C1", UserID: "U-member", CreatedAt: at,
	}, event("E-star", "star.added")))
	// The same message carries a reaction owned by the HOLDER the differential
	// asks (U-owner) too. Reactions are keyed by (message, user, name), so
	// RemoveReaction on the holder's name answers a caller who has that reaction
	// — the holder, who succeeds — differently from a caller below conversation
	// membership, who is refused at authorizeConversation, while AddReaction with
	// a name the holder has not used still succeeds. A pin and a star are keyed by
	// (message, user) with no name, so the single seeded message cannot carry a
	// holder-owned pin for RemovePin to find without making AddPin answer "already
	// exists" for the same holder — the probe hands both the one timestamp — so
	// RemovePin and RemoveStar stay a documented residue rather than being driven.
	seed("holder reaction", repository.AddReaction(ctx, domain.Reaction{
		Message: fixtureMessageID, Name: fixtureHolderReaction, UserID: "U-owner", CreatedAt: at,
	}, event("E-holder-reaction", "reaction.added")))
	if _, _, err := repository.CreateSavedItem(ctx, domain.SavedItem{
		ID: fixtureSavedItemID, WorkspaceID: "T1", UserID: "U-member", MessageID: fixtureMessageID,
		Conversation: "C1", State: domain.SavedItemInProgress, CreatedAt: at, UpdatedAt: at,
	}, event("E-saved", "saved_item.created")); err != nil {
		t.Fatalf("seed saved item: %v", err)
	}
	// The HOLDER's own saved item on the same message, its own scheduled status,
	// and its own draft, so the operations that read or remove "mine" — which
	// authorize the workspace or conversation and then act on the caller's object
	// — reach success as the holder while a caller below membership is refused for
	// standing. Each belongs to U-owner, the caller the differential asks.
	if _, _, err := repository.CreateSavedItem(ctx, domain.SavedItem{
		ID: fixtureHolderSavedItemID, WorkspaceID: "T1", UserID: "U-owner", MessageID: fixtureMessageID,
		Conversation: "C1", State: domain.SavedItemInProgress, CreatedAt: at, UpdatedAt: at,
	}, event("E-holder-saved", "saved_item.created")); err != nil {
		t.Fatalf("seed holder saved item: %v", err)
	}
	seed("scheduled status", repository.CreateScheduledStatus(ctx, domain.ScheduledStatus{
		ID: fixtureScheduledStatusID, WorkspaceID: "T1", UserID: "U-owner", StatusText: "away",
		StartsAt: at.Add(time.Hour), EndsAt: at.Add(2 * time.Hour), CreatedAt: at, UpdatedAt: at,
	}))
	if _, err := repository.UpsertDraft(ctx, domain.Draft{
		WorkspaceID: "T1", UserID: "U-owner", ConversationID: "C1", ThreadTimestamp: fixtureMessageTimestamp,
		Text: "seeded draft", UpdatedAt: at,
	}, event("E-holder-draft", "draft.updated")); err != nil {
		t.Fatalf("seed holder draft: %v", err)
	}
	seed("usergroup", repository.CreateUserGroup(ctx, domain.UserGroup{
		WorkspaceID: "T1", ID: fixtureUserGroupID, Name: "fixture group", Handle: "fixture-group",
		Creator: "U-member", UpdatedBy: "U-member", CreatedAt: at, UpdatedAt: at, Enabled: true,
		Users: []domain.UserID{"U-member"},
	}, event("E-usergroup", "usergroup.created")))
	// The seeded conversation carries the seeded user group as an access group,
	// so an operation that adds, removes or lists them has one to act on.
	seed("conversation access group", repository.SetConversationAccessGroups(ctx, "T1", "C1",
		[]domain.UserGroupID{fixtureUserGroupID}, event("E-access-group", "conversation.access_groups_set")))
	// A saved Activity view and a sidebar section owned by the HOLDER, so the
	// operations that delete or collapse the caller's own object reach success as
	// the holder while a caller below membership is refused for standing.
	seed("activity saved view", repository.CreateActivitySavedView(ctx, domain.ActivitySavedView{
		ID: fixtureActivityViewID, WorkspaceID: "T1", UserID: "U-owner", Name: "fixture view",
		Kinds: []domain.ActivityKind{domain.ActivityMention}, CreatedAt: at,
	}))
	seed("sidebar section", repository.CreateSidebarSection(ctx, domain.SidebarSection{
		ID: fixtureSidebarSectionID, WorkspaceID: "T1", UserID: "U-owner", Name: "fixture section",
		Position: 1, NotificationLevel: domain.NotificationInherit, CreatedAt: at,
	}))
	// The seeded conversation's own canvas, so ConversationCanvas has one to
	// return to a member of the channel.
	if _, err := (service.Messages{Store: repository}).CreateConversationCanvas(ctx, "T1", "U-owner", "C1", "channel canvas", `{"type":"markdown","markdown":"x"}`); err != nil {
		t.Fatalf("seed conversation canvas: %v", err)
	}
	// A group direct message the holder belongs to, so ConvertGroupDirectToPrivate
	// — which needs a DM or group DM, not the seeded channel — reaches success as
	// the holder while a caller below membership is refused for standing.
	seed("group dm", repository.SeedConversation(domain.Conversation{
		ID: fixtureGroupDMID, WorkspaceID: "T1", Kind: domain.ConversationTypeMPIM, Name: "fixture-mpim",
	}))
	seed("group dm member owner", repository.SeedConversationMember(fixtureGroupDMID, "U-owner"))
	seed("group dm member", repository.SeedConversationMember(fixtureGroupDMID, "U-member"))
}

// fixtureMessageTimestamp names the one seeded message.
const fixtureMessageTimestamp domain.MessageTimestamp = "1700000000.000100"

// fixtureHolderReaction is the emoji the holder has reacted with, so
// RemoveReaction has one of the holder's own to remove. AddReaction is handed a
// different name the holder has not used, so it too reaches success.
const fixtureHolderReaction = "clap"

// The objects the fixture holds so that an operation naming one reaches its own
// authorization instead of dying at "not found".
//
// Without them a caller who may act and a caller who may not are answered
// identically — store.ErrNotFound both times, meaning "it is not there" rather
// than "it is not for you" — and the matrix cannot tell enforcement from its
// absence. That is what refusalDoesNotDistinguishTheHolder records, and it is
// what the guard-mutation gate independently reported as operations whose guards
// can all be deleted with this suite still green.
//
// Each belongs to U-member rather than to the caller under test, so an operation
// that distinguishes "mine" from "anybody's" is asked the question it actually
// answers.
const (
	fixtureCanvasID        domain.CanvasID        = "F-canvas"
	fixtureListID          domain.ListID          = "F-list"
	fixtureCallID          domain.CallID          = "F-call"
	fixtureReminderID      domain.ReminderID      = "F-reminder"
	fixtureLaterReminderID domain.LaterReminderID = "F-later-reminder"
	fixtureBookmarkID      domain.BookmarkID      = "F-bookmark"
	fixtureUserGroupID     domain.UserGroupID     = "F-usergroup"
	fixtureFileID          domain.FileID          = "F-file"
	fixtureWorkflowID      domain.WorkflowID      = "F-workflow"
	fixtureAppID           domain.AppID           = "F-app"
	fixtureHuddleID        domain.CallID          = "F-huddle"

	fixtureSharedInviteID    domain.SharedInviteID      = "F-invite"
	fixtureApprovedInviteID  domain.SharedInviteID      = "F-approved-invite"
	fixtureWorkflowTriggerD  domain.WorkflowTriggerID   = "F-trigger"
	fixtureWorkflowRunID     domain.WorkflowRunID       = "F-run"
	fixtureSavedItemID       domain.SavedItemID         = "F-saved"
	fixtureHolderSavedItemID domain.SavedItemID         = "F-holder-saved"
	fixtureMessageID         domain.MessageID           = "M1"
	fixtureListItemID        domain.ListItemID          = "F-list-item"
	fixtureScheduledStatusID domain.ScheduledStatusID   = "F-scheduled-status"
	fixtureActivityViewID    domain.ActivitySavedViewID = "F-activity-view"
	fixtureSidebarSectionID  domain.SidebarSectionID    = "F-sidebar-section"
	fixtureListDownloadID    domain.ListDownloadID      = "F-list-download"
	fixtureGroupDMID         domain.ConversationID      = "Cmpim"
)

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
