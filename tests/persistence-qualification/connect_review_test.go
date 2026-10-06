package qualification

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// sharedInvitesKeepTheirReviewAndFilterAlike holds the parts of a Slack
// Connect invitation the SDKs' arguments reach in storage: the external-limited
// restriction, the host's review (a note kept with either decision, and an
// approval that moves the invitation or overrides its restriction), the
// restriction applied when the organization accepts, and the listing filter
// conversations.requestSharedInvite.list reads through. Both profiles must
// agree, because a filter one of them spells differently lists different
// requests to the administrator deciding them.
func sharedInvitesKeepTheirReviewAndFilterAlike(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	guest := domain.WorkspaceID("T-guest-" + f.suffix)
	other := domain.WorkspaceID("T-other-" + f.suffix)
	for _, id := range []domain.WorkspaceID{guest, other} {
		if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: id, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	second := domain.ConversationID("C-second-" + f.suffix)
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: second, WorkspaceID: f.workspaceID, Name: "second"}); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1_700_002_000, 0).UTC()
	invite := func(id string, conversation domain.ConversationID, target domain.WorkspaceID, limited bool, expires time.Time) domain.SharedInvite {
		value := domain.SharedInvite{
			ID: domain.SharedInviteID(id + "-" + f.suffix), WorkspaceID: f.workspaceID, ConversationID: conversation,
			TargetWorkspaceID: target, InvitedBy: f.userID, Status: domain.SharedInvitePending,
			CreatedAt: created, ExpiresAt: expires, ExternalLimited: limited,
		}
		if err := f.repository.CreateSharedInvite(ctx, value, f.event(id, "shared_invite.created", string(value.ID))); err != nil {
			t.Fatal(err)
		}
		return value
	}
	live := time.Unix(1_900_000_000, 0).UTC()
	limited := invite("SI-limited", f.channelID, guest, true, live)
	denied := invite("SI-denied", f.channelID, other, false, live)
	blocking := invite("SI-blocking", second, guest, false, live)
	lapsed := invite("SI-lapsed", second, other, false, time.Unix(1_700_002_100, 0).UTC())

	stored, err := f.repository.GetSharedInvite(ctx, limited.ID)
	if err != nil || !stored.ExternalLimited || stored.ReviewMessage != "" {
		t.Fatalf("created=%+v err=%v, want the restriction and no note", stored, err)
	}

	// A denial keeps its note, and may neither move nor restrict.
	decided := time.Unix(1_700_002_200, 0).UTC()
	if err := f.repository.SetSharedInviteStatus(ctx, denied.ID, domain.SharedInvitePending, domain.SharedInviteRevoked, decided, domain.SharedInviteReview{Conversation: second}, f.event("deny-move", "shared_invite.revoked", string(denied.ID))); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("a denial that moves err=%v", err)
	}
	if err := f.repository.SetSharedInviteStatus(ctx, denied.ID, domain.SharedInvitePending, domain.SharedInviteRevoked, decided, domain.SharedInviteReview{Message: "not now"}, f.event("deny", "shared_invite.revoked", string(denied.ID))); err != nil {
		t.Fatal(err)
	}
	// An approval cannot move an invitation into a conversation that already
	// has one outstanding for the same organization, nor into another
	// workspace's conversation, and a refused move changes nothing.
	if err := f.repository.SetSharedInviteStatus(ctx, limited.ID, domain.SharedInvitePending, domain.SharedInviteApproved, decided, domain.SharedInviteReview{Conversation: second}, f.event("approve-blocked", "shared_invite.approved", string(limited.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("a move onto an outstanding invitation err=%v", err)
	}
	if err := f.repository.SetSharedInviteStatus(ctx, limited.ID, domain.SharedInvitePending, domain.SharedInviteApproved, decided, domain.SharedInviteReview{Conversation: "C-nowhere-" + domain.ConversationID(f.suffix)}, f.event("approve-nowhere", "shared_invite.approved", string(limited.ID))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a move to an unknown conversation err=%v", err)
	}
	if stored, err := f.repository.GetSharedInvite(ctx, limited.ID); err != nil || stored.Status != domain.SharedInvitePending || stored.ConversationID != f.channelID {
		t.Fatalf("a refused move changed the invitation: %+v err=%v", stored, err)
	}
	if err := f.repository.SetSharedInviteStatus(ctx, blocking.ID, domain.SharedInvitePending, domain.SharedInviteRevoked, decided, domain.SharedInviteReview{}, f.event("clear", "shared_invite.revoked", string(blocking.ID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SetSharedInviteStatus(ctx, limited.ID, domain.SharedInvitePending, domain.SharedInviteApproved, decided,
		domain.SharedInviteReview{Conversation: second, SetExternalLimited: true, ExternalLimited: true, Message: "welcome"}, f.event("approve", "shared_invite.approved", string(limited.ID))); err != nil {
		t.Fatal(err)
	}
	if stored, err := f.repository.GetSharedInvite(ctx, limited.ID); err != nil || stored.ConversationID != second || !stored.ExternalLimited || stored.ReviewMessage != "welcome" || stored.ReviewedAt.IsZero() {
		t.Fatalf("approved=%+v err=%v", stored, err)
	}
	if stored, err := f.repository.GetSharedInvite(ctx, denied.ID); err != nil || stored.ReviewMessage != "not now" || stored.Decision() != domain.SharedInviteDecisionDenied {
		t.Fatalf("denied=%+v err=%v", stored, err)
	}

	// The filter, on both profiles, by request state, status, identifier,
	// conversation, requester and deadline.
	list := func(filter domain.SharedInviteFilter) []domain.SharedInviteID {
		t.Helper()
		page, err := f.repository.ListSharedInvites(ctx, f.workspaceID, filter, domain.PageRequest{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]domain.SharedInviteID, 0, len(page.Invites))
		for _, value := range page.Invites {
			if !filter.Matches(value) {
				t.Fatalf("listed %+v, which the filter %+v does not match", value, filter)
			}
			ids = append(ids, value.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...domain.SharedInviteID) []domain.SharedInviteID {
		slices.Sort(ids)
		return ids
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, check := range []struct {
		name   string
		filter domain.SharedInviteFilter
		want   []domain.SharedInviteID
	}{
		{"pending", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionPending}}, sorted(lapsed.ID)},
		{"pending and live", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionPending}, ExcludeExpiredAt: now}, sorted()},
		{"approved", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionApproved}}, sorted(limited.ID)},
		{"denied", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionDenied}}, sorted(denied.ID, blocking.ID)},
		{"revoked", domain.SharedInviteFilter{Statuses: []domain.SharedInviteStatus{domain.SharedInviteRevoked}}, sorted(denied.ID, blocking.ID)},
		{"by id", domain.SharedInviteFilter{Statuses: []domain.SharedInviteStatus{domain.SharedInviteRevoked, domain.SharedInviteApproved}, IDs: []domain.SharedInviteID{limited.ID, denied.ID}}, sorted(limited.ID, denied.ID)},
		{"by conversation", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionApproved, domain.SharedInviteDecisionPending}, Conversation: second}, sorted(limited.ID, lapsed.ID)},
		{"by requester", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionDenied}, InvitedBy: "U-nobody"}, sorted()},
	} {
		if got := list(check.filter); !slices.Equal(got, check.want) {
			t.Fatalf("%s listed %v, want %v", check.name, got, check.want)
		}
	}

	// An external-limited organization joins unable to invite further
	// organizations.
	if _, err := f.repository.AcceptSharedInvite(ctx, limited.ID, decided.Add(time.Minute), []events.Event{f.event("accept", "shared_invite.accepted", string(limited.ID))}); err != nil {
		t.Fatal(err)
	}
	if canInvite, err := f.repository.GetExternalInvitePermission(ctx, f.workspaceID, second, guest); err != nil || canInvite {
		t.Fatalf("the external-limited organization may invite=%v err=%v", canInvite, err)
	}
}

// externalTeamsPageByNameInBothDirections holds team.externalTeams.list's sort:
// by name, then identifier, in either direction, with a cursor that walks the
// same order. Both profiles must agree, or a paginator sees an organization
// twice or never.
func externalTeamsPageByNameInBothDirections(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	teams := []domain.WorkspaceID{f.workspaceID}
	for _, team := range []struct{ id, name string }{{"T-z-", "Zebra"}, {"T-a-", "Aardvark"}, {"T-m1-", "Mongoose"}, {"T-m2-", "Mongoose"}} {
		id := domain.WorkspaceID(team.id + f.suffix)
		if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: id, Name: team.name}); err != nil {
			t.Fatal(err)
		}
		teams = append(teams, id)
	}
	if err := f.repository.SetConversationTeams(ctx, f.workspaceID, f.channelID, teams, false, f.event("teams", "conversation.connected", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	walk := func(descending bool) []domain.WorkspaceID {
		t.Helper()
		request := domain.PageRequest{Limit: 1, Descending: descending}
		var seen []domain.WorkspaceID
		for range 10 {
			page, err := f.repository.ListExternalTeams(ctx, f.workspaceID, request)
			if err != nil {
				t.Fatal(err)
			}
			for _, team := range page.Teams {
				seen = append(seen, team.ID)
			}
			if !page.HasMore {
				return seen
			}
			request.Cursor = page.NextCursor
		}
		t.Fatalf("the walk did not end: %v", seen)
		return nil
	}
	ascending := []domain.WorkspaceID{teams[2], teams[3], teams[4], teams[1]}
	if got := walk(false); !slices.Equal(got, ascending) {
		t.Fatalf("ascending=%v, want %v", got, ascending)
	}
	descending := slices.Clone(ascending)
	slices.Reverse(descending)
	if got := walk(true); !slices.Equal(got, descending) {
		t.Fatalf("descending=%v, want %v", got, descending)
	}
}

// userReactionsCountTheirTotal holds the total reactions.list's legacy paging
// object reports: every reacted message the member can see, on every page.
func userReactionsCountTheirTotal(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	for index, name := range []string{"total-a", "total-b", "total-c"} {
		message := f.message(t, ctx, name, time.Unix(1_700_003_000+int64(index), 0).UTC())
		for _, reaction := range []string{"eyes", "tada"} {
			if err := f.repository.AddReaction(ctx, domain.Reaction{Message: message.ID, Name: reaction, UserID: f.userID, CreatedAt: message.CreatedAt}, f.event("reaction-"+reaction+"-"+name, "reaction.added", string(message.ID))); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := domain.PageRequest{Limit: 2}
	for page := 1; ; page++ {
		value, err := f.repository.ListUserReactions(ctx, f.workspaceID, f.userID, request)
		if err != nil {
			t.Fatal(err)
		}
		if value.Total != 3 {
			t.Fatalf("page %d total=%d, want every reacted message", page, value.Total)
		}
		if !value.HasMore {
			break
		}
		request.Cursor = value.NextCursor
	}
	if empty, err := f.repository.ListUserReactions(ctx, f.workspaceID, "U-nobody", domain.PageRequest{Limit: 2}); err != nil || empty.Total != 0 {
		t.Fatalf("a member with no reactions total=%d err=%v", empty.Total, err)
	}
}
