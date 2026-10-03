package service

import (
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A member requests an invitation for an administrator to approve. The
// channels are optional and must be ones the member is in; a guest may not
// request one, and nothing about the request skips the pending queue.
func TestAMemberRequestsAnInvitationForAnAdministratorToApprove(t *testing.T) {
	ctx, m, s := postingWorld(t)
	if err := s.SeedConversation(domain.Conversation{ID: "Cprivate", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate}); err != nil {
		t.Fatal(err)
	}

	if err := m.RequestInvitation(ctx, "T1", "Umember", " New.Person@Example.com ", []domain.ConversationID{"C1", "C1"}, "We worked together"); err != nil {
		t.Fatal(err)
	}
	if err := m.RequestInvitation(ctx, "T1", "Umember", "second@example.com", nil, ""); err != nil {
		t.Fatalf("a request without channels: %v", err)
	}
	for _, refused := range []struct {
		name     string
		actor    domain.UserID
		email    string
		channels []domain.ConversationID
		want     error
	}{
		{"a channel the member is not in", "Umember", "x@example.com", []domain.ConversationID{"Cprivate"}, domain.ErrInvalidInviteRequest},
		{"an address with no @", "Umember", "not-an-address", nil, domain.ErrInvalidInviteRequest},
		{"a guest", "Uguest", "x@example.com", nil, domain.ErrUserIsRestricted},
	} {
		if err := m.RequestInvitation(ctx, "T1", refused.actor, refused.email, refused.channels, ""); !errors.Is(err, refused.want) {
			t.Errorf("%s: %v, want %v", refused.name, err, refused.want)
		}
	}
	if err := m.RequestInvitation(ctx, "T1", "Ustranger", "x@example.com", nil, ""); err == nil {
		t.Error("someone outside the workspace requested an invitation")
	}

	pending, err := m.AdminListInviteRequests(ctx, "T1", "Uadmin", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Requests) != 2 {
		t.Fatalf("pending=%+v", pending.Requests)
	}
	byEmail := map[string]domain.InviteRequest{}
	for _, request := range pending.Requests {
		byEmail[request.Email] = request
	}
	first := byEmail["new.person@example.com"]
	if first.RequestedBy != "Umember" || len(first.ChannelIDs) != 1 || first.ChannelIDs[0] != "C1" || first.CustomMessage != "We worked together" || first.Restricted {
		t.Fatalf("request=%+v", first)
	}
}
