package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Slack Connect, modelled inside one deployment.
//
// The post-connection half already existed: conversation_teams records which
// organizations are in a channel, conversations.info reports it, and
// admin.conversations.disconnectShared removes one. What was missing was how a
// channel ever got there — the invitation.
//
// CONNECT-02 requires approval and acceptance to be distinct transitions, and
// they are: the host approves who may be invited, and the invited organization
// separately decides whether to come. CONNECT-01 forbids promising a place from
// a stale count, so the 250-organization capacity is checked inside the
// transaction that appends the team and nowhere else.
//
// Recorded boundary: an external organization here is another workspace on this
// deployment. Cross-deployment federation — an invitation that leaves this
// process and is accepted by a Slack workspace elsewhere — needs a federation
// transport this product does not have, and a single-workspace mock cannot
// qualify it either way.

var (
	// ErrInvalidSharedInvite refuses a malformed invitation.
	ErrInvalidSharedInvite = errors.New("shared invitation is invalid")
	// ErrSharedInviteSettled refuses a decision on an invitation that already
	// has one. It is distinct from a malformed request: the caller did nothing
	// wrong, someone else simply got there first.
	ErrSharedInviteSettled = errors.New("shared invitation has already been settled")
	// ErrSlackConnectFull refuses the place rather than promising one that is
	// not there.
	ErrSlackConnectFull = errors.New("conversation already holds the maximum number of organizations")
)

// SharedInviteLifetime bounds how long an external organization has to accept.
const SharedInviteLifetime = 14 * 24 * time.Hour

// InviteShared records an invitation for an external organization to join one
// conversation. It is created pending: recording who should be invited and
// deciding that they may be are separate, and a member without the manage
// scope may raise one for an administrator to answer.
func (m Messages) InviteShared(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID, email string) (domain.SharedInvite, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, actorID, conversationID); err != nil {
		return domain.SharedInvite{}, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if target == "" && email == "" {
		return domain.SharedInvite{}, ErrInvalidSharedInvite
	}
	if email != "" && !strings.Contains(email, "@") {
		return domain.SharedInvite{}, ErrInvalidSharedInvite
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return domain.SharedInvite{}, store.ErrNotFound
	}
	if conversation.IsDirectOrGroup() {
		return domain.SharedInvite{}, ErrInvalidSharedInvite
	}
	if conversation.Archived {
		return domain.SharedInvite{}, ErrConversationAlreadyArchived
	}
	id, err := domain.PublicID("SI_")
	if err != nil {
		return domain.SharedInvite{}, err
	}
	now := time.Now().UTC()
	invite := domain.SharedInvite{
		ID: domain.SharedInviteID(id), WorkspaceID: workspaceID, ConversationID: conversationID,
		TargetWorkspaceID: target, TargetEmail: email, InvitedBy: actorID,
		Status: domain.SharedInvitePending, CreatedAt: now, ExpiresAt: now.Add(SharedInviteLifetime),
	}
	event, err := sharedInviteEvent(workspaceID, actorID, "shared_invite.created", invite, now)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if err := m.Store.CreateSharedInvite(ctx, invite, event); err != nil {
		return domain.SharedInvite{}, err
	}
	return invite, nil
}

// ApproveSharedInvite is the host's decision that the invitation may be sent.
func (m Messages) ApproveSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInvitePending, domain.SharedInviteApproved, "shared_invite.approved", true)
}

// DenySharedInvite refuses a request before it is ever sent. It is recorded as
// revoked rather than declined: declining is the invited organization's answer,
// and an administrator reading the record needs to tell the two apart.
func (m Messages) DenySharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInvitePending, domain.SharedInviteRevoked, "shared_invite.revoked", true)
}

// RevokeSharedInvite withdraws an approved invitation nobody has accepted yet.
func (m Messages) RevokeSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInviteApproved, domain.SharedInviteRevoked, "shared_invite.revoked", true)
}

// DeclineSharedInvite is the invited organization's answer, so the authority is
// membership of the *target* workspace rather than the host's.
func (m Messages) DeclineSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInviteApproved, domain.SharedInviteDeclined, "shared_invite.declined", false)
}

func (m Messages) decideSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID, from, to domain.SharedInviteStatus, topic string, host bool) (domain.SharedInvite, error) {
	invite, err := m.Store.GetSharedInvite(ctx, id)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if err := m.authorizeSharedInvite(ctx, workspaceID, actorID, invite, host); err != nil {
		return domain.SharedInvite{}, err
	}
	if invite.Status != from {
		return domain.SharedInvite{}, ErrSharedInviteSettled
	}
	now := time.Now().UTC()
	// Approving a lapsed invitation records it as live and sends nobody
	// anything: acceptance refuses it on the deadline, so the approval can
	// never become a shared channel. Withdrawing one stays available, because
	// clearing a queue of dead invitations is the remaining useful action and
	// refusing it would leave them there permanently.
	if to == domain.SharedInviteApproved && invite.Expired(now) {
		return domain.SharedInvite{}, ErrInvitationExpired
	}
	event, err := sharedInviteEvent(workspaceID, actorID, topic, invite, now)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if err := m.Store.SetSharedInviteStatus(ctx, id, from, to, now, event); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return domain.SharedInvite{}, ErrSharedInviteSettled
		}
		return domain.SharedInvite{}, err
	}
	invite.Status = to
	// The member who asked for the invitation is told what was decided. It is
	// news only to them and only when someone else decided: an administrator
	// approving their own request has not been told anything, and the requester
	// is the one person who has been waiting.
	if invite.InvitedBy != "" && invite.InvitedBy != actorID {
		if err := m.Store.RecordSharedInviteDecision(ctx, invite, actorID, now); err != nil {
			return domain.SharedInvite{}, err
		}
	}
	return invite, nil
}

// AcceptSharedInvite brings the invited organization into the conversation. The
// capacity is enforced by the store, in the transaction that appends the team.
func (m Messages) AcceptSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.Conversation, error) {
	invite, err := m.Store.GetSharedInvite(ctx, id)
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.authorizeSharedInvite(ctx, workspaceID, actorID, invite, false); err != nil {
		return domain.Conversation{}, err
	}
	if invite.TargetWorkspaceID == "" {
		// An invitation that named only an address has no organization to
		// bring in; accepting it is a cross-deployment flow this product does
		// not have.
		return domain.Conversation{}, ErrInvalidSharedInvite
	}
	now := time.Now().UTC()
	if !invite.Acceptable(now) {
		return domain.Conversation{}, ErrSharedInviteSettled
	}
	accepted, err := sharedInviteEvent(workspaceID, actorID, "shared_invite.accepted", invite, now)
	if err != nil {
		return domain.Conversation{}, err
	}
	connected, err := newEvent(invite.WorkspaceID, actorID, events.NewPayload("conversation.connected",
		events.String("channel_id", string(invite.ConversationID)),
		events.String("team_id", string(invite.TargetWorkspaceID)),
	), now)
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := m.Store.AcceptSharedInvite(ctx, id, now, []events.Event{accepted, connected})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// The store refuses both a settled invitation and a full channel
			// with a conflict; only one of the two is still acceptable, so the
			// caller is told which by re-reading it.
			if current, readErr := m.Store.GetSharedInvite(ctx, id); readErr == nil && current.Acceptable(now) {
				return domain.Conversation{}, ErrSlackConnectFull
			}
			return domain.Conversation{}, ErrSharedInviteSettled
		}
		return domain.Conversation{}, err
	}
	return m.withSharedIdentity(ctx, conversation), nil
}

// ListSharedInvites reports one workspace's invitations in a status, from
// either side: the host sees what it sent and the invited organization sees
// what it was sent, which is what conversations.listConnectInvites and
// conversations.requestSharedInvite.list each ask for.
func (m Messages) ListSharedInvites(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, status domain.SharedInviteStatus, request domain.PageRequest) (domain.SharedInvitePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.SharedInvitePage{}, err
	}
	switch status {
	case domain.SharedInvitePending, domain.SharedInviteApproved, domain.SharedInviteAccepted, domain.SharedInviteDeclined, domain.SharedInviteRevoked:
	default:
		return domain.SharedInvitePage{}, ErrInvalidSharedInvite
	}
	return m.Store.ListSharedInvites(ctx, workspaceID, status, request)
}

// ExternalTeams reports the organizations this workspace shares channels with.
//
// It is administrative: knowing every organization a workspace is connected to
// is a statement about the whole workspace rather than about a channel someone
// is in, and Slack puts it behind an administrator's token for the same reason.
// A member can still see the organizations in a channel they belong to, which
// is what conversations.info answers.
func (m Messages) ExternalTeams(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, request domain.PageRequest) (domain.ExternalTeamPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.ExternalTeamPage{}, err
	}
	return m.Store.ListExternalTeams(ctx, workspaceID, request)
}

// DisconnectExternalTeam ends a connection with one organization across every
// channel it is in.
//
// Slack's per-channel disconnection already exists here as
// admin.conversations.disconnectShared. This is the whole-organization form,
// and it is not the same act repeated: an administrator ending a relationship
// wants it ended everywhere, and doing it channel by channel leaves the
// connection alive wherever they missed one.
func (m Messages) DisconnectExternalTeam(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, target domain.WorkspaceID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if strings.TrimSpace(string(target)) == "" || target == workspaceID {
		return ErrInvalidSharedInvite
	}
	event, err := newEvent(workspaceID, actorID, events.NewPayload("team.external_disconnected",
		events.String("team_id", string(target))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DisconnectExternalTeam(ctx, workspaceID, target, event)
}

// SetExternalInvitePermissions narrows or widens an already-connected
// organization's ability to invite further organizations into a conversation.
// It is expressed through the team association the conversation already
// carries, because that is what this deployment can actually enforce.
func (m Messages) SetExternalInvitePermissions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID, canInvite bool) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	if target == "" {
		return domain.Conversation{}, ErrInvalidSharedInvite
	}
	teams, orgChannel, err := m.Store.ListConversationTeams(ctx, workspaceID, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	present := false
	for _, team := range teams {
		if team == target {
			present = true
		}
	}
	if !present {
		return domain.Conversation{}, store.ErrNotFound
	}
	// Withdrawing the ability to invite is a downgrade of the association, not
	// a removal of the organization: disconnecting is
	// admin.conversations.disconnectShared, and conflating the two would let a
	// permission change silently eject a participant.
	event, err := newEvent(workspaceID, actorID, events.NewPayload("conversation.external_invite_permissions_set",
		events.String("channel_id", string(conversationID)),
		events.String("team_id", string(target)),
		events.String("can_invite", boolText(canInvite)),
	), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.Store.SetConversationTeams(ctx, workspaceID, conversationID, teams, orgChannel, event); err != nil {
		return domain.Conversation{}, err
	}
	return m.ConversationInfo(ctx, workspaceID, actorID, conversationID)
}

// authorizeSharedInvite decides who may act on an invitation. The host side is
// the workspace that sent it; the invited side is the organization it names.
// They are different authorities on purpose: CONNECT-02 makes approval and
// acceptance different decisions made by different people.
func (m Messages) authorizeSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, invite domain.SharedInvite, host bool) error {
	if host {
		if invite.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
		return m.requireWorkspaceAdmin(ctx, workspaceID, actorID)
	}
	if invite.TargetWorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	return m.requireWorkspaceAdmin(ctx, workspaceID, actorID)
}

func sharedInviteEvent(workspaceID domain.WorkspaceID, actorID domain.UserID, topic string, invite domain.SharedInvite, at time.Time) (events.Event, error) {
	return newEvent(workspaceID, actorID, events.NewPayload(topic,
		events.String("shared_invite_id", string(invite.ID)),
		events.String("channel_id", string(invite.ConversationID)),
		events.String("team_id", string(invite.TargetWorkspaceID)),
	), at)
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
