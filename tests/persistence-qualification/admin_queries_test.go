package qualification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// administratorChannelSearchAgreesOnEveryProfile holds the storage contract for
// admin.conversations.search: an empty query is every channel and never a
// direct conversation, the channel-type flags narrow it, each sort orders it
// the same way on every profile in both directions, and a cursor resumes the
// walk without repeating or dropping a channel.
func administratorChannelSearchAgreesOnEveryProfile(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-chansearch-" + suffix)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Search"}); err != nil {
		t.Fatal(err)
	}
	members := []domain.UserID{"U-chansearch-a-" + domain.UserID(suffix), "U-chansearch-b-" + domain.UserID(suffix), "U-chansearch-c-" + domain.UserID(suffix)}
	for _, id := range members {
		if err := repository.SeedUser(ctx, domain.User{ID: id, WorkspaceID: workspaceID, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	id := func(name string) domain.ConversationID { return domain.ConversationID("C-" + name + "-" + suffix) }
	channels := []struct {
		conversation domain.Conversation
		members      int
	}{
		{domain.Conversation{ID: id("b"), WorkspaceID: workspaceID, Name: "budget", Created: time.Unix(1700000300, 0).UTC()}, 3},
		{domain.Conversation{ID: id("a"), WorkspaceID: workspaceID, Name: "budget-archive", Archived: true, Created: time.Unix(1700000100, 0).UTC()}, 1},
		{domain.Conversation{ID: id("p"), WorkspaceID: workspaceID, Name: "planning", Purpose: "the budget", Kind: domain.ConversationTypePrivate, Created: time.Unix(1700000200, 0).UTC()}, 2},
		{domain.Conversation{ID: id("d"), WorkspaceID: workspaceID, Name: "budget", Kind: domain.ConversationTypeIM, Created: time.Unix(1700000400, 0).UTC()}, 2},
	}
	for _, channel := range channels {
		if err := repository.SeedConversation(ctx, channel.conversation); err != nil {
			t.Fatal(err)
		}
		for _, member := range members[:channel.members] {
			if err := repository.SeedConversationMember(ctx, channel.conversation.ID, member); err != nil {
				t.Fatal(err)
			}
		}
	}
	names := func(page domain.ConversationPage) string {
		values := make([]string, 0, len(page.Conversations))
		for _, conversation := range page.Conversations {
			values = append(values, conversation.Name)
		}
		return strings.Join(values, ",")
	}
	yes, no := true, false
	for _, check := range []struct {
		search     domain.ConversationSearch
		descending bool
		want       string
	}{
		{domain.ConversationSearch{Sort: domain.ConversationSortName}, false, "budget,budget-archive,planning"},
		{domain.ConversationSearch{Sort: domain.ConversationSortName}, true, "planning,budget-archive,budget"},
		{domain.ConversationSearch{Sort: domain.ConversationSortMemberCount}, false, "budget-archive,planning,budget"},
		{domain.ConversationSearch{Sort: domain.ConversationSortCreated}, true, "budget,planning,budget-archive"},
		// Relevance: the exact name, then the name that starts with the
		// query, then the channel whose purpose mentions it.
		{domain.ConversationSearch{Query: "BUDGET", Sort: domain.ConversationSortRelevant}, false, "budget,budget-archive,planning"},
		{domain.ConversationSearch{Query: "budget", Sort: domain.ConversationSortName, Archived: &no}, false, "budget,planning"},
		{domain.ConversationSearch{Sort: domain.ConversationSortName, Private: &yes}, false, "planning"},
		{domain.ConversationSearch{Sort: domain.ConversationSortName, Private: &no, Archived: &yes}, false, "budget-archive"},
		{domain.ConversationSearch{Query: "100%", Sort: domain.ConversationSortName}, false, ""},
	} {
		page, err := repository.SearchConversations(ctx, workspaceID, check.search, domain.PageRequest{Limit: 10, Descending: check.descending})
		if err != nil || names(page) != check.want || page.HasMore {
			t.Fatalf("search=%+v descending=%v got %q err=%v want %q", check.search, check.descending, names(page), err, check.want)
		}
	}
	for _, sort := range []domain.ConversationSearchSort{domain.ConversationSortName, domain.ConversationSortMemberCount, domain.ConversationSortCreated, domain.ConversationSortRelevant} {
		for _, descending := range []bool{false, true} {
			search := domain.ConversationSearch{Query: "b", Sort: sort}
			whole, err := repository.SearchConversations(ctx, workspaceID, search, domain.PageRequest{Limit: 10, Descending: descending})
			if err != nil {
				t.Fatal(err)
			}
			walked := make([]string, 0, len(whole.Conversations))
			request := domain.PageRequest{Limit: 1, Descending: descending}
			for range 10 {
				page, err := repository.SearchConversations(ctx, workspaceID, search, request)
				if err != nil {
					t.Fatalf("sort=%s descending=%v: %v", sort, descending, err)
				}
				walked = append(walked, names(page))
				if !page.HasMore {
					break
				}
				request.Cursor = page.NextCursor
			}
			if strings.Join(walked, ",") != names(whole) {
				t.Fatalf("sort=%s descending=%v walked %v, the whole result is %q", sort, descending, walked, names(whole))
			}
		}
	}
	// A cursor from one sort is refused by another.
	head, err := repository.SearchConversations(ctx, workspaceID, domain.ConversationSearch{Sort: domain.ConversationSortName}, domain.PageRequest{Limit: 1})
	if err != nil || head.NextCursor == "" {
		t.Fatalf("head=%+v err=%v", head, err)
	}
	if _, err := repository.SearchConversations(ctx, workspaceID, domain.ConversationSearch{Sort: domain.ConversationSortCreated}, domain.PageRequest{Limit: 1, Cursor: head.NextCursor}); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("a name cursor under the created sort error=%v, want %v", err, domain.ErrInvalidCursor)
	}
}

// assigningAMemberSetsTheGuestTierItNames holds the storage contract for
// admin.users.assign's guest flags: the tier asked for is the tier stored, no
// tier keeps the one already held, and an administrator is never made a guest.
func assigningAMemberSetsTheGuestTierItNames(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-assign-" + suffix)
	member := domain.UserID("U-assign-member-" + suffix)
	admin := domain.UserID("U-assign-admin-" + suffix)
	now := time.Unix(1700000000, 0).UTC()
	sequence := 0
	event := func(topic string) events.Event {
		sequence++
		return events.Event{ID: domain.EventID(fmt.Sprintf("%s-%d-%s", topic, sequence, suffix)), WorkspaceID: workspaceID, Topic: topic, Payload: "{}", CreatedAt: now}
	}
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Assign"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.UserID{member, admin} {
		if err := repository.SeedUser(ctx, domain.User{ID: id, WorkspaceID: workspaceID, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SetWorkspaceRole(ctx, workspaceID, admin, domain.WorkspaceRoleAdmin, event("user.role_changed")); err != nil {
		t.Fatal(err)
	}
	tier := func(user domain.UserID) string {
		t.Helper()
		membership, err := repository.GetWorkspaceMembership(ctx, workspaceID, user)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("active=%v restricted=%v ultra=%v", membership.Active, membership.Restricted, membership.UltraRestricted)
	}
	for _, step := range []struct {
		tier domain.GuestTier
		want string
	}{
		{domain.GuestTierMultiChannel, "active=true restricted=true ultra=false"},
		{domain.GuestTierUnchanged, "active=true restricted=true ultra=false"},
		{domain.GuestTierSingleChannel, "active=true restricted=false ultra=true"},
		{domain.GuestTierNone, "active=true restricted=false ultra=false"},
	} {
		if err := repository.AssignUser(ctx, workspaceID, member, step.tier, nil, event("user.assigned")); err != nil {
			t.Fatalf("tier=%d: %v", step.tier, err)
		}
		if got := tier(member); got != step.want {
			t.Fatalf("tier=%d stored %s, want %s", step.tier, got, step.want)
		}
	}
	if err := repository.AssignUser(ctx, workspaceID, admin, domain.GuestTierMultiChannel, nil, event("user.assigned")); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("an administrator made a guest error=%v, want %v", err, store.ErrInvalidArgument)
	}
	if got := tier(admin); got != "active=true restricted=false ultra=false" {
		t.Fatalf("the refused assign changed the administrator: %s", got)
	}
}

// adminUserListingsFilterByActivity holds the storage contract for
// admin.users.list's is_active: active and deactivated memberships are the two
// halves of the unfiltered listing.
func adminUserListingsFilterByActivity(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-activity-" + suffix)
	active := domain.UserID("U-activity-a-" + suffix)
	departed := domain.UserID("U-activity-b-" + suffix)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Activity"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.UserID{active, departed} {
		if err := repository.SeedUser(ctx, domain.User{ID: id, WorkspaceID: workspaceID, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SetUserDeleted(ctx, workspaceID, departed, true, events.Event{
		ID: domain.EventID("evt-activity-" + suffix), WorkspaceID: workspaceID, Topic: "user.deactivated", Payload: "{}", CreatedAt: time.Unix(1700000000, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for activity, want := range map[domain.MemberActivity]string{
		domain.MemberActivityAny:         string(active) + "," + string(departed),
		domain.MemberActivityActive:      string(active),
		domain.MemberActivityDeactivated: string(departed),
	} {
		page, err := repository.ListAdminUsers(ctx, workspaceID, activity, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		listed := make([]string, 0, len(page.Users))
		for _, user := range page.Users {
			listed = append(listed, string(user.User.ID))
		}
		if strings.Join(listed, ",") != want {
			t.Fatalf("activity=%q listed %v, want %s", activity, listed, want)
		}
	}
}

// aCreatedChannelKeepsItsDescription holds admin.conversations.create's
// description: it is written with the channel, so it reads back on every
// profile with who set it.
func aCreatedChannelKeepsItsDescription(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-describe-" + suffix)
	creator := domain.UserID("U-describe-" + suffix)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Describe"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: creator, WorkspaceID: workspaceID, Name: string(creator)}); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000000, 0).UTC()
	conversation := domain.Conversation{
		ID: domain.ConversationID("C-describe-" + suffix), WorkspaceID: workspaceID, Name: "described", Created: created, CreatorID: creator,
		Purpose: "What this channel is for", PurposeSetBy: creator, PurposeSetAt: created,
	}
	if err := repository.CreateConversation(ctx, conversation, creator, events.Event{
		ID: domain.EventID("evt-describe-" + suffix), WorkspaceID: workspaceID, Topic: "conversation.created", Payload: "{}", CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetConversation(ctx, conversation.ID)
	if err != nil || stored.Purpose != conversation.Purpose || stored.PurposeSetBy != creator || !stored.PurposeSetAt.Equal(created) {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}
