package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// twoMemberWorkspace seeds T1 with an administrator U1, a plain member U2 and a
// third member U3, one public channel C1 that nobody has joined, and one private
// channel CPRIV owned by U2.
func twoMemberWorkspace(t *testing.T) (*memory.Store, Messages) {
	t.Helper()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.UserID{"U1", "U2", "U3"} {
		if err := s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "CPRIV", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("CPRIV", "U2"); err != nil {
		t.Fatal(err)
	}
	return s, Messages{Store: s}
}

// A canvas or a list grant addressed to a channel used to be validated for
// workspace membership of the CHANNEL and never for reachability by the ACTOR,
// so any member could plant an attacker-authored document — with write access —
// into every private channel in the workspace by naming its identifier.
//
// Before the fix both halves of this test failed:
//
//	authorization_test.go:NN: U3 planted a canvas into a private channel it cannot read
//	authorization_test.go:NN: U3 planted a list into a private channel it cannot read
func TestDocumentGrantsRefuseAPrivateChannelTheActorCannotReach(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)

	// The premise: U3 genuinely cannot reach CPRIV.
	if _, err := messages.ConversationInfo(ctx, "T1", "U3", "CPRIV"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("premise broken: U3 read the private channel: %v", err)
	}

	canvas, err := messages.CreateCanvas(ctx, "T1", "U3", "Notes", `{"type":"h1","markdown":"hello"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U3", canvas.ID, "write", []domain.ConversationID{"CPRIV"}, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("U3 planted a canvas into a private channel it cannot read: err=%v", err)
	}
	// The channel binding on creation is the same grant by another name.
	if _, err := messages.CreateCanvas(ctx, "T1", "U3", "Notes", `{"type":"h1","markdown":"hello"}`, "CPRIV"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("U3 bound a new canvas to a private channel it cannot read: err=%v", err)
	}

	list, err := messages.CreateList(ctx, "T1", "U3", "Roadmap", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetListAccess(ctx, "T1", "U3", list.ID, "write", []domain.ConversationID{"CPRIV"}, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("U3 planted a list into a private channel it cannot read: err=%v", err)
	}

	// A member of the channel may still share into it, and a public channel is
	// still reachable by every member.
	u2Canvas, err := messages.CreateCanvas(ctx, "T1", "U2", "Shared", `{"type":"h1","markdown":"hello"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U2", u2Canvas.ID, "write", []domain.ConversationID{"CPRIV"}, nil); err != nil {
		t.Fatalf("a member of the private channel was refused: %v", err)
	}
	if err := messages.SetListAccess(ctx, "T1", "U3", list.ID, "read", []domain.ConversationID{"C1"}, nil); err != nil {
		t.Fatalf("sharing into a public channel was refused: %v", err)
	}
}

// admin.conversations.restrictAccess.addGroup persisted a restriction that no
// authorization path consulted: the operator saw ok:true and read the value
// back, and membership alone still decided who could open the channel.
//
// Before the fix:
//
//	authorization_test.go:NN: a member outside every access group still read the restricted channel
func TestPrivateConversationAccessGroupsAreEnforced(t *testing.T) {
	ctx := context.Background()
	s, messages := twoMemberWorkspace(t)
	if err := s.SeedConversationMember("CPRIV", "U3"); err != nil {
		t.Fatal(err)
	}

	// Both U2 and U3 are members of the private channel, so membership alone
	// admits both.
	for _, actor := range []domain.UserID{"U2", "U3"} {
		if _, err := messages.ConversationInfo(ctx, "T1", actor, "CPRIV"); err != nil {
			t.Fatalf("premise broken: %s could not read the private channel: %v", actor, err)
		}
	}

	group, err := messages.CreateUserGroup(ctx, "T1", "U1", "Security", "security", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetUserGroupUsers(ctx, "T1", "U1", group.ID, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminAddConversationAccessGroup(ctx, "T1", "U1", "CPRIV", group.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := messages.ConversationInfo(ctx, "T1", "U2", "CPRIV"); err != nil {
		t.Fatalf("a member of the access group was locked out: %v", err)
	}
	if _, err := messages.ConversationInfo(ctx, "T1", "U3", "CPRIV"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a member outside every access group still read the restricted channel: err=%v", err)
	}
	// Removing the restriction restores plain membership.
	if err := messages.AdminRemoveConversationAccessGroup(ctx, "T1", "U1", "CPRIV", group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ConversationInfo(ctx, "T1", "U3", "CPRIV"); err != nil {
		t.Fatalf("removing the last access group did not restore access: %v", err)
	}
}

// Once an access group decides who may open a private channel, whoever can
// rewrite the group's membership holds a key to every channel restricted to it.
// All six user-group mutations authorized on workspace membership alone.
//
// Before the fix every call in the first loop returned nil.
func TestUserGroupMutationsRequireWorkspaceAdmin(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)

	group, err := messages.CreateUserGroup(ctx, "T1", "U1", "Security", "security", "")
	if err != nil {
		t.Fatal(err)
	}

	refusals := map[string]error{}
	_, refusals["CreateUserGroup"] = messages.CreateUserGroup(ctx, "T1", "U2", "Rogue", "rogue", "")
	_, refusals["UpdateUserGroup"] = messages.UpdateUserGroup(ctx, "T1", "U2", group.ID, "Seized", "", "")
	_, refusals["SetUserGroupEnabled"] = messages.SetUserGroupEnabled(ctx, "T1", "U2", group.ID, false)
	_, refusals["SetUserGroupUsers"] = messages.SetUserGroupUsers(ctx, "T1", "U2", group.ID, []domain.UserID{"U2"})
	refusals["AddUserGroupChannels"] = messages.AddUserGroupChannels(ctx, "T1", "U2", group.ID, []domain.ConversationID{"C1"})
	refusals["RemoveUserGroupChannels"] = messages.RemoveUserGroupChannels(ctx, "T1", "U2", group.ID, []domain.ConversationID{"C1"})
	for name, err := range refusals {
		if !errors.Is(err, ErrNotWorkspaceAdmin) {
			t.Errorf("%s by a plain member: err=%v, want ErrNotWorkspaceAdmin", name, err)
		}
	}

	// Reads stay at member authority: the directory of groups is ordinary
	// workspace information and @-mentioning a group needs it.
	if _, err := messages.ListUserGroups(ctx, "T1", "U2", false, domain.PageRequest{Limit: 10}); err != nil {
		t.Fatalf("a member could not list user groups: %v", err)
	}
	if _, err := messages.UserGroupUsers(ctx, "T1", "U2", group.ID); err != nil {
		t.Fatalf("a member could not read a user group's members: %v", err)
	}
	// The administrator is not locked out.
	if _, err := messages.SetUserGroupUsers(ctx, "T1", "U1", group.ID, []domain.UserID{"U2"}); err != nil {
		t.Fatalf("the administrator was refused: %v", err)
	}
}

// views.publish is authorized by app ownership, not workspace administration:
// Slack apps publish their own Home surface for each target member.
func TestPublishViewAllowsAnAppsPerUserHome(t *testing.T) {
	ctx := context.Background()
	repository, messages := twoMemberWorkspace(t)
	seedHomeApp(t, repository, "A1")
	payload := `{"type":"home","blocks":[]}`

	if _, err := messages.PublishView(ctx, "T1", "U2", "A1", "U3", payload, ""); err != nil {
		t.Fatalf("the app could not publish a target member's App Home: %v", err)
	}
	if _, err := messages.PublishView(ctx, "T1", "U2", "A1", "U2", payload, ""); err != nil {
		t.Fatalf("a member could not publish their own App Home: %v", err)
	}
	if _, err := messages.PublishView(ctx, "T1", "U1", "A1", "U3", payload, ""); err != nil {
		t.Fatalf("the administrator was refused: %v", err)
	}
}

// authorizeConversation checks membership only for a private conversation, so a
// member could post into, rename, mark, or set the topic of a public channel
// they had never joined — and `not_in_channel`, declared by ten pinned
// operations, was produced nowhere in the repository.
//
// Before the fix every call in the table returned nil.
func TestOperationsThatDeclareNotInChannelRequireMembership(t *testing.T) {
	ctx := context.Background()
	s, messages := twoMemberWorkspace(t)

	// U2 has never joined C1, which is public and which U2 can read.
	if _, err := messages.ConversationInfo(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatalf("premise broken: a member cannot read a public channel: %v", err)
	}

	refusals := map[string]error{}
	_, refusals["chat.postMessage"] = messages.Post(ctx, "T1", "U2", "C1", "hello", "", "")
	_, refusals["chat.scheduleMessage"] = messages.ScheduleMessageWithBlocks(ctx, "T1", "U2", "C1", "later", "", time.Now().UTC().Add(time.Hour))
	_, refusals["conversations.rename"] = messages.RenameConversation(ctx, "T1", "U2", "C1", "seized")
	_, refusals["conversations.setTopic"] = messages.SetConversationTopic(ctx, "T1", "U2", "C1", "seized")
	_, refusals["conversations.setPurpose"] = messages.SetConversationPurpose(ctx, "T1", "U2", "C1", "seized")
	_, refusals["conversations.mark"] = messages.MarkRead(ctx, "T1", "U2", "C1", domain.NewMessageTimestamp(time.Now().UTC()))
	_, refusals["conversations.invite"] = messages.InviteConversationMembers(ctx, "T1", "U2", "C1", []domain.UserID{"U3"})
	refusals["conversations.kick"] = messages.KickConversationMember(ctx, "T1", "U2", "C1", "U3")
	refusals["conversations.leave"] = messages.LeaveConversation(ctx, "T1", "U2", "C1")
	for name, err := range refusals {
		if !errors.Is(err, ErrNotInConversation) {
			t.Errorf("%s from outside the channel: err=%v, want ErrNotInConversation", name, err)
		}
	}

	// Reading a public channel still does not require membership, and joining
	// makes every one of the operations above available.
	if _, err := messages.History(ctx, "T1", "U2", "C1", domain.PageRequest{Limit: 10}); err != nil {
		t.Fatalf("reading a public channel now requires membership: %v", err)
	}
	if err := s.SeedConversationMember("C1", "U2"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U2", "C1", "hello", "", ""); err != nil {
		t.Fatalf("a member of the channel was refused: %v", err)
	}
	// A channel the actor cannot see at all stays store.ErrNotFound, so the new
	// refusal cannot be used to probe for private channels.
	if _, err := messages.Post(ctx, "T1", "U3", "CPRIV", "hello", "", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("posting into an unreachable private channel: err=%v, want store.ErrNotFound", err)
	}
}

// team.integrationLogs and team.billableInfo are administrative disclosures that
// gated on workspace membership, and both walk the whole workspace to answer.
//
// Before the fix both calls by U2 returned nil.
func TestAdministrativeWorkspaceReadsRequireWorkspaceAdmin(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)

	if _, err := messages.IntegrationLogs(ctx, "T1", "U2", "", "", "", "", 10, 1); !errors.Is(err, ErrNotWorkspaceAdmin) {
		t.Errorf("IntegrationLogs by a plain member: err=%v, want ErrNotWorkspaceAdmin", err)
	}
	if _, err := messages.TeamBillableInfo(ctx, "T1", "U2", ""); !errors.Is(err, ErrNotWorkspaceAdmin) {
		t.Errorf("TeamBillableInfo by a plain member: err=%v, want ErrNotWorkspaceAdmin", err)
	}
	if _, err := messages.IntegrationLogs(ctx, "T1", "U1", "", "", "", "", 10, 1); err != nil {
		t.Fatalf("the administrator was refused IntegrationLogs: %v", err)
	}
	info, err := messages.TeamBillableInfo(ctx, "T1", "U1", "")
	if err != nil || len(info.Users) != 3 {
		t.Fatalf("the administrator was refused TeamBillableInfo: info=%+v err=%v", info, err)
	}
}

// admin.conversations.setTeams accepted any workspace id that existed, which
// wrote a cross-tenant association and answered a non-existent id differently
// from a foreign one — an oracle enumerating every tenant on the deployment.
//
// Before the fix the first call returned nil and the association read back.
func TestAdminSetConversationTeamsRefusesAForeignWorkspace(t *testing.T) {
	ctx := context.Background()
	s, messages := twoMemberWorkspace(t)
	if err := s.SeedWorkspace(domain.Workspace{ID: "T2", Name: "other"}); err != nil {
		t.Fatal(err)
	}

	foreign := messages.AdminSetConversationTeams(ctx, "T1", "U1", "C1", []domain.WorkspaceID{"T2"}, false)
	if !errors.Is(foreign, ErrInvalidConversation) {
		t.Fatalf("T1 administrator associated its channel with unrelated workspace T2: err=%v", foreign)
	}
	// A workspace that does not exist is refused identically, so the refusal
	// discloses nothing about which tenants exist.
	absent := messages.AdminSetConversationTeams(ctx, "T1", "U1", "C1", []domain.WorkspaceID{"T-absent"}, false)
	if !errors.Is(absent, ErrInvalidConversation) || foreign.Error() != absent.Error() {
		t.Fatalf("foreign=%v absent=%v: a foreign workspace must be indistinguishable from an absent one", foreign, absent)
	}
	if err := messages.AdminSetConversationTeams(ctx, "T1", "U1", "C1", []domain.WorkspaceID{"T1"}, false); err != nil {
		t.Fatalf("the actor's own workspace was refused: %v", err)
	}
}

// conversations.join stated no privacy precondition and relied entirely on the
// store refusing inside its write transaction.
//
// Before the fix a store that did not re-check would have admitted U3 to CPRIV;
// the service-level assertion is what makes the precondition visible here.
func TestJoinConversationRefusesPrivateAndDirectConversations(t *testing.T) {
	ctx := context.Background()
	s, messages := twoMemberWorkspace(t)
	if err := s.SeedConversation(domain.Conversation{ID: "CDM", WorkspaceID: "T1", Name: "dm", Kind: domain.ConversationTypeIM}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ConversationID{"CPRIV", "CDM"} {
		if _, err := messages.JoinConversation(ctx, "T1", "U3", id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("JoinConversation(%s) by an outsider: err=%v, want store.ErrNotFound", id, err)
		}
	}
	if _, err := messages.JoinConversation(ctx, "T1", "U3", "C1"); err != nil {
		t.Fatalf("joining a public channel was refused: %v", err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var join *events.Record
	for index := range records {
		if records[index].Event.Topic == "conversation.member_added" {
			join = &records[index]
		}
	}
	if join == nil {
		t.Fatal("joining a public channel did not publish conversation.member_added")
	}
	bodies, err := events.SlackEventBodies(*join, "A1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !strings.Contains(string(bodies[0]), `"type":"member_joined_channel"`) || !strings.Contains(string(bodies[0]), `"user":"U3"`) || !strings.Contains(string(bodies[0]), `"channel":"C1"`) {
		t.Fatalf("join event bodies=%q, want one complete member_joined_channel", bodies)
	}
}

// AdminDisconnectSharedConversation went straight from the role check to the
// store, so it minted a journal record for a conversation that may belong to
// another workspace or not exist, and refused in a different shape from every
// sibling.
//
// Before the fix the call returned the store's own refusal rather than
// store.ErrNotFound from the service, and the event had already been built.
func TestAdminDisconnectSharedConversationProvesTheConversationIsOwned(t *testing.T) {
	ctx := context.Background()
	s, messages := twoMemberWorkspace(t)
	if err := s.SeedWorkspace(domain.Workspace{ID: "T2", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "CFOREIGN", WorkspaceID: "T2", Name: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminDisconnectSharedConversation(ctx, "T1", "U1", "CFOREIGN", []domain.WorkspaceID{"T1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a foreign conversation: err=%v, want store.ErrNotFound", err)
	}
	if err := messages.AdminDisconnectSharedConversation(ctx, "T1", "U1", "C-absent", []domain.WorkspaceID{"T1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an absent conversation: err=%v, want store.ErrNotFound", err)
	}
}

// UpdateListCells iterated a Go map, so the order rows were written in — and the
// order they came back in — depended on the map seed, and a batch naming a row
// that does not exist committed whatever prefix happened to come first.
//
// Before the fix:
//
//	authorization_test.go:NN: a batch naming a missing row committed 1 of its rows
func TestUpdateListCellsRefusesABatchWholeAndKeepsRequestOrder(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Roadmap", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"a","text":"0"}]`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"a","text":"0"}]`)
	if err != nil {
		t.Fatal(err)
	}

	batch := `[{"row_id":"` + string(first.ID) + `","column_id":"a","text":"written"},` +
		`{"row_id":"` + string(second.ID) + `","column_id":"a","text":"written"},` +
		`{"row_id":"row-does-not-exist","column_id":"a","text":"written"}]`
	if _, err := messages.UpdateListCells(ctx, "T1", "U1", list.ID, batch); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a batch naming a missing row: err=%v, want store.ErrNotFound", err)
	}
	written := 0
	for _, id := range []domain.ListItemID{first.ID, second.ID} {
		item, err := messages.GetListItem(ctx, "T1", "U1", list.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(item.Fields, "written") {
			written++
		}
	}
	if written != 0 {
		t.Fatalf("a batch naming a missing row committed %d of its rows", written)
	}

	// A valid batch returns its rows in the order the request named them, not in
	// map order.
	valid := `[{"row_id":"` + string(second.ID) + `","column_id":"a","text":"two"},` +
		`{"row_id":"` + string(first.ID) + `","column_id":"a","text":"one"}]`
	updated, err := messages.UpdateListCells(ctx, "T1", "U1", list.ID, valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || updated[0].ID != second.ID || updated[1].ID != first.ID {
		t.Fatalf("rows returned out of request order: %+v", updated)
	}
}

// lists.create with copy_from paged the source with no cap, drawing an
// identifier and opening a write transaction per record, for any member holding
// read access on any list.
//
// Before the fix the copy of a source above the cap succeeded, publishing
// list.created and then one transaction per record.
func TestCreateListRefusesACopyAboveTheRecordCap(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)
	source, err := messages.CreateList(ctx, "T1", "U1", "Source", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= maxCopiedListRecords; index++ {
		if _, err := messages.CreateListItem(ctx, "T1", "U1", source.ID, "", `[{"column_id":"a","text":"x"}]`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := messages.CreateList(ctx, "T1", "U1", "Copy", "", "", source.ID, true, false); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("copying a list above the cap: err=%v, want ErrInvalidList", err)
	}
	// Nothing was written: the refusal happens before the list is created, so no
	// half-built list is left behind — and there is no DeleteList to remove one —
	// and list.created was never published for the copy.
	records, err := messages.ListEventsAfter(ctx, "T1", 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, record := range records {
		if record.Event.Topic == "list.created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("list.created records=%d, want 1 (the source list only)", created)
	}
}

// "invalid workspace role" was a bare errors.New reachable from
// admin.users.setRole and from the provider-driven SynchronizeExternalUserRole.
// The chat gRPC boundary classifies by exported sentinel only, so it degraded to
// codes.Unavailable with fixed text — a caller mistake reported remotely as a
// dependency failure asking for a retry that can never succeed.
//
// Before the fix errors.Is was false for every classified sentinel.
func TestRoleAndSettingRefusalsCarryAClassifiedSentinel(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)

	if err := messages.SetUserRole(ctx, "T1", "U1", "U2", domain.WorkspaceRole("superuser")); !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("SetUserRole with an unknown role: err=%v, want ErrInvalidWorkspace", err)
	}
	if err := messages.SynchronizeExternalUserRole(ctx, "T1", "U2", domain.WorkspaceRole("superuser")); !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("SynchronizeExternalUserRole with an unknown role: err=%v, want ErrInvalidWorkspace", err)
	}
	if _, err := messages.AdminCreateUser(ctx, "T1", "U1", "new@example.com", "New", domain.WorkspaceRoleOwner); !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("AdminCreateUser conferring Owner: err=%v, want ErrInvalidWorkspace", err)
	}
	if err := messages.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1"}); !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("SetAuthMethod without a provider: err=%v, want ErrInvalidWorkspace", err)
	}
	if err := messages.CreateExternalIdentity(ctx, domain.ExternalIdentity{WorkspaceID: "T1"}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("CreateExternalIdentity without a subject: err=%v, want store.ErrInvalidArgument", err)
	}
}

// The journal records the service emits must stay decodable by every consumer
// after the access-group read is wired into authorizeConversation.
func TestConversationAccessGroupEventsRemainDeliverable(t *testing.T) {
	ctx := context.Background()
	_, messages := twoMemberWorkspace(t)
	group, err := messages.CreateUserGroup(ctx, "T1", "U1", "Security", "security", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminAddConversationAccessGroup(ctx, "T1", "U1", "CPRIV", group.ID); err != nil {
		t.Fatal(err)
	}
	records, err := messages.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.Event.Topic != "conversation.access_group_added" {
			continue
		}
		found = true
		delivered, err := events.Deliverable(record.Event)
		if err != nil {
			t.Fatalf("access-group event is not deliverable: %v", err)
		}
		if value, ok := delivered.Field("usergroup_id"); !ok || value != string(group.ID) {
			t.Fatalf("access-group event does not name the group: %q ok=%v", value, ok)
		}
	}
	if !found {
		t.Fatal("no conversation.access_group_added record was written")
	}
}

// An identity provider's role claim distinguishes a member from an
// administrator; it cannot express ownership. Writing it through
// unconditionally demoted an owner on their own sign-in, and because only an
// owner may confer ownership, demoting the last one leaves the workspace
// permanently unadministrable with no way back.
func TestProviderSynchronizationNeverDemotesAnOwner(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	if err := store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "member"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleOwner); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: store}

	// The owner signs in and the provider asserts the highest role it can express.
	if err := messages.SynchronizeExternalUserRole(ctx, "T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("owner sign-in was refused: %v", err)
	}
	membership, err := store.GetWorkspaceMembership(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if membership.Role != domain.WorkspaceRoleOwner {
		t.Fatalf("role=%q after the owner signed in, want it unchanged: the workspace has lost its only owner", membership.Role)
	}

	// Everyone else still tracks the claim.
	if err := messages.SynchronizeExternalUserRole(ctx, "T1", "U2", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if membership, err := store.GetWorkspaceMembership(ctx, "T1", "U2"); err != nil || membership.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("membership=%+v err=%v, want the claim applied to a non-owner", membership, err)
	}
}

// Replacing or removing a profile photo changed the profile in one transaction
// and recorded the instruction to reclaim the old blob in a second. A crash
// between them left a blob nothing referenced and nothing would ever collect,
// and a failure of the second reported failure for a change that had already
// been committed. Both now commit together.
func TestProfilePhotoChangesCommitTheirCleanupWithTheChange(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		change func(*testing.T, Messages)
	}{
		{"replacing", func(t *testing.T, messages Messages) {
			if _, err := messages.SetUserPhoto(context.Background(), "T1", "U1", "image/png", int64(len(testImageBytes("two!"))), bytes.NewReader(testImageBytes("two!"))); err != nil {
				t.Fatal(err)
			}
		}},
		{"removing", func(t *testing.T, messages Messages) {
			if err := messages.DeleteUserPhoto(context.Background(), "T1", "U1"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repository := memory.New()
			if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
				t.Fatal(err)
			}
			if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
				t.Fatal(err)
			}
			objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			messages := Messages{Store: repository, Blob: objects}

			first, err := messages.SetUserPhoto(ctx, "T1", "U1", "image/png", int64(len(testImageBytes("one!"))), bytes.NewReader(testImageBytes("one!")))
			if err != nil {
				t.Fatal(err)
			}
			if first.Profile.Image24 == "" {
				t.Fatal("the first photo was not recorded on the profile")
			}
			if claimed := claimPhotoReclamations(t, repository, "before"); claimed != 0 {
				t.Fatalf("a first photo produced %d reclamations, want none", claimed)
			}

			testCase.change(t, messages)

			// Exactly one reclamation, and it names the blob that is no longer
			// referenced by the profile.
			if claimed := claimPhotoReclamations(t, repository, "after"); claimed != 1 {
				t.Fatalf("%s a photo produced %d reclamations, want exactly one committed with the change", testCase.name, claimed)
			}
		})
	}
}

// claimPhotoReclamations reads the outstanding photo reclamations the way the
// blob collector does. They are an internal topic, deliberately withheld from
// the client-facing enumeration so a storage key can never reach a subscriber.
func claimPhotoReclamations(t *testing.T, repository *memory.Store, owner string) int {
	t.Helper()
	records, err := repository.ClaimEventsForTopic(context.Background(), "T1", events.UserPhotoBlobDeleteTopic, owner, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return len(records)
}

// Creating a channel makes the creator a member of it. Membership was recorded
// only for private conversations, which was invisible while membership decided
// only whether a private conversation could be read. Once membership governed
// writing, a caller could create a public channel and immediately be refused
// permission to rename the channel they had just created — which is how the
// official SDK suite found it.
func TestCreatingAConversationMakesTheCreatorAMemberOfIt(t *testing.T) {
	for _, private := range []bool{false, true} {
		name := "public"
		if private {
			name = "private"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repository := memory.New()
			if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
				t.Fatal(err)
			}
			if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "creator"}); err != nil {
				t.Fatal(err)
			}
			messages := Messages{Store: repository}

			conversation, err := messages.CreateConversation(ctx, "T1", "U1", "qualification-"+name, private)
			if err != nil {
				t.Fatal(err)
			}
			member, err := repository.IsConversationMember(ctx, conversation.ID, "U1")
			if err != nil {
				t.Fatal(err)
			}
			if !member {
				t.Fatal("the creator is not a member of the conversation they created")
			}
			// The operations that require membership must accept the creator.
			if _, err := messages.RenameConversation(ctx, "T1", "U1", conversation.ID, "qualification-renamed-"+name); err != nil {
				t.Fatalf("the creator could not rename the channel they created: %v", err)
			}
			if _, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "U1", conversation.ID, "hello", "", "", "", "", ""); err != nil {
				t.Fatalf("the creator could not post into the channel they created: %v", err)
			}
		})
	}
}
