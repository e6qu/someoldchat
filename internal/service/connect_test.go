package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// CONNECT-01 forbids promising a place from a stale count, so the capacity is
// enforced inside the transaction that appends the organization. This fills a
// channel to the documented maximum and shows the next acceptance refused —
// with the invitation left acceptable, so the refusal is the capacity and not
// a settled invitation.
func TestSlackConnectCapacityIsEnforcedAtAcceptance(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")
	messages := Messages{Store: store}

	// The host counts towards the capacity, so the channel is full once it and
	// the connected organizations reach the documented maximum.
	teams := make([]domain.WorkspaceID, 0, domain.SlackConnectCapacity)
	for index := 0; index < domain.SlackConnectCapacity-1; index++ {
		id := domain.WorkspaceID("T-seat-" + strconv.Itoa(index))
		store.SeedWorkspace(domain.Workspace{ID: id, Name: "seat"})
		teams = append(teams, id)
	}
	if err := store.SetConversationTeams(ctx, "T1", "C1", teams, false, events.Event{ID: "Eseats", WorkspaceID: "T1", Topic: "conversation.connected", Payload: `{"type":"conversation.connected"}`}); err != nil {
		t.Fatal(err)
	}

	store.SeedWorkspace(domain.Workspace{ID: "T-late", Name: "late"})
	store.SeedUser(domain.User{ID: "U-late", WorkspaceID: "T-late", Name: "late-admin"})
	if err := store.SeedWorkspaceRole("T-late", "U-late", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	invite, err := messages.InviteShared(ctx, "T1", "U1", "C1", domain.SharedInviteRecipient{Workspace: "T-late"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", invite.ID, domain.SharedInviteReview{}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AcceptSharedInvite(ctx, "T-late", "U-late", invite.ID, false); !errors.Is(err, domain.ErrSlackConnectFull) {
		t.Fatalf("acceptance into a full channel err=%v, want the capacity refusal", err)
	}
	// The refusal left the invitation acceptable: nothing was consumed by
	// being told there was no room.
	stored, err := store.GetSharedInvite(ctx, invite.ID)
	if err != nil || stored.Status != domain.SharedInviteApproved {
		t.Fatalf("invitation=%+v err=%v, want it still approved", stored, err)
	}
}

// CONNECT-02: approval and acceptance are different decisions taken by
// different organizations, so neither side can take the other's.
func TestSharedInviteDecisionsBelongToTheirOwnSide(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T2", Name: "guest-admin"})
	for workspace, user := range map[domain.WorkspaceID]domain.UserID{"T1": "U1", "T2": "U2"} {
		if err := store.SeedWorkspaceRole(workspace, user, domain.WorkspaceRoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")
	messages := Messages{Store: store}

	invite, err := messages.InviteShared(ctx, "T1", "U1", "C1", domain.SharedInviteRecipient{Workspace: "T2"})
	if err != nil {
		t.Fatal(err)
	}
	// The invited organization cannot approve its own invitation.
	if _, err := messages.ApproveSharedInvite(ctx, "T2", "U2", invite.ID, domain.SharedInviteReview{}); err == nil {
		t.Fatal("the invited organization approved the host's invitation")
	}
	if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", invite.ID, domain.SharedInviteReview{}); err != nil {
		t.Fatal(err)
	}
	// And the host cannot accept on the invited organization's behalf.
	if _, err := messages.AcceptSharedInvite(ctx, "T1", "U1", invite.ID, false); err == nil {
		t.Fatal("the host accepted its own invitation")
	}
	if _, err := messages.AcceptSharedInvite(ctx, "T2", "U2", invite.ID, false); err != nil {
		t.Fatal(err)
	}
}

// One outstanding invitation per organization per conversation: two would let
// two acceptances each claim a place.
func TestASecondOutstandingInvitationIsRefused(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")
	messages := Messages{Store: store}
	if _, err := messages.InviteShared(ctx, "T1", "U1", "C1", domain.SharedInviteRecipient{Workspace: "T2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.InviteShared(ctx, "T1", "U1", "C1", domain.SharedInviteRecipient{Workspace: "T2"}); err == nil {
		t.Fatal("a second outstanding invitation was recorded for the same organization")
	}
}

// CONNECT-01 requires the expired state to be explicit. Expiry used to be
// spelled out inside SharedInvite.Acceptable, which made acceptance the only
// operation that knew about the deadline: an invitation could be approved long
// after it lapsed, and the approval recorded a live invitation that nothing
// could ever accept.
func TestALapsedInvitationCannotBeApprovedButCanBeWithdrawn(t *testing.T) {
	ctx := context.Background()
	newFixture := func(t *testing.T) (*memory.Store, Messages) {
		t.Helper()
		store := memory.New()
		store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
		store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
		if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
			t.Fatal(err)
		}
		store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
		store.SeedConversationMember("C1", "U1")
		store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
		return store, Messages{Store: store}
	}
	// The deadline is written directly: InviteShared always dates it fourteen
	// days out, and waiting is not a test.
	lapsed := func(t *testing.T, store *memory.Store, id domain.SharedInviteID) {
		t.Helper()
		past := time.Now().UTC().Add(-time.Hour)
		invite := domain.SharedInvite{
			ID: id, WorkspaceID: "T1", ConversationID: "C1", TargetWorkspaceID: "T2",
			InvitedBy: "U1", Status: domain.SharedInvitePending,
			CreatedAt: past.Add(-SharedInviteLifetime), ExpiresAt: past,
		}
		event, err := events.New(domain.EventID("E"+string(id)), "T1", "U1",
			events.NewPayload("shared_invite.created", events.String("invite_id", string(id))), past)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CreateSharedInvite(ctx, invite, event); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("approval is refused", func(t *testing.T) {
		store, messages := newFixture(t)
		lapsed(t, store, "SI_lapsed")
		if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", "SI_lapsed", domain.SharedInviteReview{}); !errors.Is(err, domain.ErrInvitationExpired) {
			t.Fatalf("approving a lapsed invitation err=%v, want ErrInvitationExpired", err)
		}
		// The refusal changed nothing: the invitation is still pending, not
		// approved and not silently settled.
		stored, err := store.GetSharedInvite(ctx, "SI_lapsed")
		if err != nil || stored.Status != domain.SharedInvitePending {
			t.Fatalf("invitation=%+v err=%v, want it still pending", stored, err)
		}
	})

	// Withdrawing stays available, because clearing a queue of dead
	// invitations is the remaining useful action and refusing it would leave
	// them there permanently.
	t.Run("withdrawal is allowed", func(t *testing.T) {
		store, messages := newFixture(t)
		lapsed(t, store, "SI_withdrawn")
		invite, err := messages.DenySharedInvite(ctx, "T1", "U1", "SI_withdrawn", domain.SharedInviteReview{})
		if err != nil {
			t.Fatalf("withdrawing a lapsed invitation: %v", err)
		}
		if invite.Status != domain.SharedInviteRevoked {
			t.Fatalf("status=%q, want revoked", invite.Status)
		}
	})
}

// TestExternalInvitePermissionIsStoredReadableAndEnforced closes the gap where
// SetExternalInvitePermissions wrote an event and changed no queryable state:
// the permission is durable now, read back through the service, and enforced
// when a connected organization tries to invite another.
func TestExternalInvitePermissionIsStoredReadableAndEnforced(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")

	// A connected organization, and a member of it who is in the shared channel.
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest-org"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T2", Name: "guest-admin"})
	if err := store.SeedWorkspaceRole("T2", "U2", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversationMember("C1", "U2")
	if err := store.SetConversationTeams(ctx, "T1", "C1", []domain.WorkspaceID{"T2"}, false, events.Event{
		ID: "E-teams", WorkspaceID: "T1", Topic: "conversation.connected", Payload: `{"type":"conversation.connected"}`,
	}); err != nil {
		t.Fatal(err)
	}
	// A third organization the connected team might invite.
	store.SeedWorkspace(domain.Workspace{ID: "T3", Name: "third-org"})
	store.SeedWorkspace(domain.Workspace{ID: "T4", Name: "fourth-org"})
	messages := Messages{Store: store}

	// With no restriction recorded, the permission reads as allowed and the
	// connected team may invite.
	allowed, err := messages.ExternalInvitePermission(ctx, "T1", "U1", "C1", "T2")
	if err != nil || !allowed {
		t.Fatalf("default permission = %v err = %v, want may-invite", allowed, err)
	}
	if _, err := messages.InviteShared(ctx, "T2", "U2", "C1", domain.SharedInviteRecipient{Workspace: "T3"}); err != nil {
		t.Fatalf("a permitted connected team was refused: %v", err)
	}

	// Restrict it. The stored decision is read back, not merely announced.
	if _, err := messages.SetExternalInvitePermissions(ctx, "T1", "U1", "C1", "T2", false); err != nil {
		t.Fatal(err)
	}
	restricted, err := messages.ExternalInvitePermission(ctx, "T1", "U1", "C1", "T2")
	if err != nil || restricted {
		t.Fatalf("restricted permission = %v err = %v, want denied", restricted, err)
	}

	// Now the connected team is refused when it tries to invite, and with the
	// classified sentinel rather than a not-found.
	if _, err := messages.InviteShared(ctx, "T2", "U2", "C1", domain.SharedInviteRecipient{Workspace: "T3"}); !errors.Is(err, domain.ErrExternalInviteNotPermitted) {
		t.Fatalf("a restricted connected team's invite = %v, want ErrExternalInviteNotPermitted", err)
	}

	// The host is never restricted by this: it owns the channel. A distinct
	// target, because T3 was already invited above and inviting it twice is a
	// separate refusal.
	if _, err := messages.InviteShared(ctx, "T1", "U1", "C1", domain.SharedInviteRecipient{Workspace: "T4"}); err != nil {
		t.Fatalf("the host was refused its own channel's invite: %v", err)
	}
}

// The host's reason for denying a request reaches the member who asked, with
// the decision. conversations.requestSharedInvite.deny accepted the message and
// dropped it.
func TestADenialsReasonReachesTheMemberWhoAsked(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	repository.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "requester"})
	repository.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"})
	if err := repository.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	repository.SeedConversationMember("C1", "U1")
	repository.SeedConversationMember("C1", "UA")
	messages := Messages{Store: repository}

	invite, err := messages.InviteShared(ctx, "T1", "U1", "C1", domain.SharedInviteRecipient{Workspace: "T2"})
	if err != nil {
		t.Fatal(err)
	}
	// A denial moves and restricts nothing, so a review asking it to is
	// refused rather than half applied.
	if _, err := messages.DenySharedInvite(ctx, "T1", "UA", invite.ID, domain.SharedInviteReview{Conversation: "C1", Message: "no"}); !errors.Is(err, domain.ErrInvalidSharedInvite) {
		t.Fatalf("a denial that moves the invitation err=%v", err)
	}
	if _, err := messages.DenySharedInvite(ctx, "T1", "UA", invite.ID, domain.SharedInviteReview{Message: strings.Repeat("x", domain.MaxSharedInviteReviewMessage+1)}); !errors.Is(err, domain.ErrInvalidSharedInvite) {
		t.Fatalf("an oversized reason err=%v", err)
	}
	denied, err := messages.DenySharedInvite(ctx, "T1", "UA", invite.ID, domain.SharedInviteReview{Message: "  Not this quarter  "})
	if err != nil {
		t.Fatal(err)
	}
	if denied.ReviewMessage != "Not this quarter" || denied.Decision() != domain.SharedInviteDecisionDenied || denied.SettledAt.IsZero() {
		t.Fatalf("denied=%+v, want the trimmed reason on a denied, settled request", denied)
	}
	told, err := messages.Activity(ctx, "T1", "U1", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range told.Items {
		if item.SharedInviteID == invite.ID {
			found = true
			if item.SharedInviteMessage != "Not this quarter" || item.SharedInviteStatus != domain.SharedInviteRevoked {
				t.Fatalf("decision=%+v, want the reason with the denial", item)
			}
		}
	}
	if !found {
		t.Fatalf("the requester was not told: %+v", told.Items)
	}
}

// An approval may move the invitation to another of the host's channels, but
// only to one the invitation could have been raised on, and never into a
// channel that already has an outstanding invitation for the same
// organization: two acceptances would each claim a place.
func TestAnApprovalMovesAnInvitationOnlyWhereItCouldHaveBeenRaised(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	repository.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	repository.SeedWorkspace(domain.Workspace{ID: "T3", Name: "elsewhere"})
	repository.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T2", Name: "guest"})
	repository.SeedUser(domain.User{ID: "U-gone", WorkspaceID: "T2", Name: "gone", Deleted: true})
	if err := repository.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []domain.Conversation{
		{ID: "C1", WorkspaceID: "T1", Name: "one"},
		{ID: "C2", WorkspaceID: "T1", Name: "two"},
		{ID: "C-out", WorkspaceID: "T1", Name: "not-a-member"},
		{ID: "D1", WorkspaceID: "T1", Kind: domain.ConversationTypeIM},
		{ID: "C-other", WorkspaceID: "T3", Name: "other"},
	} {
		repository.SeedConversation(conversation)
	}
	for _, conversation := range []domain.ConversationID{"C1", "C2", "D1"} {
		repository.SeedConversationMember(conversation, "UA")
	}
	messages := Messages{Store: repository}

	// A person names their organization; one in the host, or deactivated, or
	// unknown, names nobody who can be invited, and so does naming two kinds
	// of recipient at once.
	for _, recipient := range []domain.SharedInviteRecipient{
		{User: "UA"}, {User: "U-gone"}, {User: "U-nobody"}, {User: "U2", Email: "two@example.org"}, {},
	} {
		if _, err := messages.InviteShared(ctx, "T1", "UA", "C1", recipient); !errors.Is(err, domain.ErrInvalidSharedInvite) {
			t.Fatalf("recipient=%+v err=%v", recipient, err)
		}
	}
	first, err := messages.InviteShared(ctx, "T1", "UA", "C1", domain.SharedInviteRecipient{User: "U2", ExternalLimited: true})
	if err != nil || first.TargetWorkspaceID != "T2" || !first.ExternalLimited {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := messages.InviteShared(ctx, "T1", "UA", "C2", domain.SharedInviteRecipient{Workspace: "T2"})
	if err != nil {
		t.Fatal(err)
	}
	for conversation, want := range map[domain.ConversationID]error{
		"C2":      store.ErrAlreadyExists,
		"C-out":   domain.ErrNotInConversation,
		"C-other": store.ErrNotFound,
		"D1":      domain.ErrInvalidSharedInvite,
	} {
		if _, err := messages.ApproveSharedInvite(ctx, "T1", "UA", first.ID, domain.SharedInviteReview{Conversation: conversation}); !errors.Is(err, want) {
			t.Fatalf("moving to %s err=%v, want %v", conversation, err, want)
		}
	}
	if stored, err := repository.GetSharedInvite(ctx, first.ID); err != nil || stored.Status != domain.SharedInvitePending || stored.ConversationID != "C1" {
		t.Fatalf("a refused move changed the invitation: %+v err=%v", stored, err)
	}
	if _, err := messages.DenySharedInvite(ctx, "T1", "UA", second.ID, domain.SharedInviteReview{}); err != nil {
		t.Fatal(err)
	}
	moved, err := messages.ApproveSharedInvite(ctx, "T1", "UA", first.ID, domain.SharedInviteReview{Conversation: "C2", SetExternalLimited: true, ExternalLimited: false, Message: "welcome"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetSharedInvite(ctx, first.ID)
	if err != nil || stored.ConversationID != "C2" || stored.ExternalLimited || stored.ReviewMessage != "welcome" || stored.ReviewedAt.IsZero() || moved.ConversationID != "C2" {
		t.Fatalf("moved=%+v stored=%+v err=%v", moved, stored, err)
	}

	// The listing's filter: by request state, by requester and by deadline.
	listed, err := messages.ListSharedInvites(ctx, "T1", "UA", domain.SharedInviteFilter{Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionDenied}}, domain.PageRequest{Limit: 10})
	if err != nil || len(listed.Invites) != 1 || listed.Invites[0].ID != second.ID {
		t.Fatalf("denied requests=%+v err=%v", listed.Invites, err)
	}
	if _, err := messages.ListSharedInvites(ctx, "T1", "UA", domain.SharedInviteFilter{}, domain.PageRequest{Limit: 10}); !errors.Is(err, domain.ErrInvalidSharedInvite) {
		t.Fatalf("an empty filter err=%v", err)
	}
	late, err := messages.ListSharedInvites(ctx, "T1", "UA", domain.SharedInviteFilter{
		Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionApproved}, ExcludeExpiredAt: time.Now().Add(SharedInviteLifetime + time.Hour),
	}, domain.PageRequest{Limit: 10})
	if err != nil || len(late.Invites) != 0 {
		t.Fatalf("an approval past its deadline was listed as live: %+v err=%v", late.Invites, err)
	}
}
