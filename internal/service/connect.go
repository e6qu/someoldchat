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

// SharedInviteLifetime bounds how long an external organization has to accept.
const SharedInviteLifetime = 14 * 24 * time.Hour

// InviteShared records an invitation for an external organization to join one
// conversation. It is created pending: recording who should be invited and
// deciding that they may be are separate, and a member without the manage
// scope may raise one for an administrator to answer.
//
// The recipient is an organization, a person or an address. A person named by
// conversations.inviteShared's user_ids is a member of another organization on
// this deployment, and that organization is the one invited; a person in the
// host's own workspace is already there and cannot be invited to it.
func (m Messages) InviteShared(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, recipient domain.SharedInviteRecipient) (domain.SharedInvite, error) {
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return domain.SharedInvite{}, store.ErrNotFound
	}
	// The host of the shared channel invites through the ordinary host-scoped
	// membership check. A connected organization may invite too — this is what
	// makes the external-invite permission enforceable — but its members are in
	// a different workspace, so the host-scoped check refuses them; a shared
	// channel's membership genuinely spans workspaces, and a connected team's
	// member is verified directly against the conversation instead.
	if conversation.WorkspaceID == workspaceID {
		if err := m.requireConversationMembership(ctx, workspaceID, actorID, conversationID); err != nil {
			return domain.SharedInvite{}, err
		}
	} else {
		// The caller must be a member of the shared channel, must be in the
		// workspace it claims, and that workspace must be a participating team
		// — GetExternalInvitePermission returns not-found when it is not, so a
		// stranger workspace is refused without confirming the channel exists.
		member, memberErr := m.Store.IsConversationMember(ctx, conversationID, actorID)
		if memberErr != nil || !member {
			return domain.SharedInvite{}, store.ErrNotFound
		}
		user, userErr := m.Store.GetUser(ctx, actorID)
		if userErr != nil || user.WorkspaceID != workspaceID || user.Deleted {
			return domain.SharedInvite{}, store.ErrNotFound
		}
		permitted, permErr := m.Store.GetExternalInvitePermission(ctx, conversation.WorkspaceID, conversationID, workspaceID)
		if permErr != nil {
			return domain.SharedInvite{}, store.ErrNotFound
		}
		if !permitted {
			return domain.SharedInvite{}, domain.ErrExternalInviteNotPermitted
		}
	}
	target, email, err := m.sharedInviteTarget(ctx, conversation.WorkspaceID, recipient)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if conversation.IsDirectOrGroup() {
		return domain.SharedInvite{}, domain.ErrInvalidSharedInvite
	}
	if conversation.Archived {
		return domain.SharedInvite{}, domain.ErrConversationAlreadyArchived
	}
	id, err := domain.PublicID("SI_")
	if err != nil {
		return domain.SharedInvite{}, err
	}
	now := time.Now().UTC()
	// The invitation lives on the shared conversation, which belongs to the
	// host. When a connected organization raises one, the record and its event
	// are still the host's — the inviting member is InvitedBy, and the host is
	// where the invitation is stored and approved.
	invite := domain.SharedInvite{
		ID: domain.SharedInviteID(id), WorkspaceID: conversation.WorkspaceID, ConversationID: conversationID,
		TargetWorkspaceID: target, TargetEmail: email, InvitedBy: actorID, ExternalLimited: recipient.ExternalLimited,
		Status: domain.SharedInvitePending, CreatedAt: now, ExpiresAt: now.Add(SharedInviteLifetime),
	}
	event, err := sharedInviteEvent(conversation.WorkspaceID, actorID, "shared_invite.created", invite, now)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if err := m.Store.CreateSharedInvite(ctx, invite, event); err != nil {
		return domain.SharedInvite{}, err
	}
	return invite, nil
}

// ApproveSharedInvite is the host's decision that the invitation may be sent.
// The review may move it to another of the host's conversations, override
// whether the invited organization is external-limited, and attach a note.
func (m Messages) ApproveSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID, review domain.SharedInviteReview) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInvitePending, domain.SharedInviteApproved, "shared_invite.approved", true, review)
}

// DenySharedInvite refuses a request before it is ever sent. It is recorded as
// revoked rather than declined: declining is the invited organization's answer,
// and an administrator reading the record needs to tell the two apart. The
// review's message is the reason the requester is told; a denial moves and
// restricts nothing, so a review asking it to is refused rather than half
// applied.
func (m Messages) DenySharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID, review domain.SharedInviteReview) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInvitePending, domain.SharedInviteRevoked, "shared_invite.revoked", true, review)
}

// RevokeSharedInvite withdraws an approved invitation nobody has accepted yet.
func (m Messages) RevokeSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInviteApproved, domain.SharedInviteRevoked, "shared_invite.revoked", true, domain.SharedInviteReview{})
}

// DeclineSharedInvite is the invited organization's answer, so the authority is
// membership of the *target* workspace rather than the host's.
func (m Messages) DeclineSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return m.decideSharedInvite(ctx, workspaceID, actorID, id, domain.SharedInviteApproved, domain.SharedInviteDeclined, "shared_invite.declined", false, domain.SharedInviteReview{})
}

func (m Messages) decideSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID, from, to domain.SharedInviteStatus, topic string, host bool, review domain.SharedInviteReview) (domain.SharedInvite, error) {
	invite, err := m.Store.GetSharedInvite(ctx, id)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if err := m.authorizeSharedInvite(ctx, workspaceID, actorID, invite, host); err != nil {
		return domain.SharedInvite{}, err
	}
	review.Message = strings.TrimSpace(review.Message)
	if len(review.Message) > domain.MaxSharedInviteReviewMessage {
		return domain.SharedInvite{}, domain.ErrInvalidSharedInvite
	}
	if to != domain.SharedInviteApproved && (review.Conversation != "" || review.SetExternalLimited) {
		return domain.SharedInvite{}, domain.ErrInvalidSharedInvite
	}
	if review.Conversation == invite.ConversationID {
		review.Conversation = ""
	}
	if review.Conversation != "" {
		// Moving the invitation is the host choosing which of its channels the
		// organization is invited to, so it must be one the host could have
		// raised the invitation on in the first place.
		if err := m.requireSharedInviteConversation(ctx, workspaceID, actorID, review.Conversation); err != nil {
			return domain.SharedInvite{}, err
		}
	}
	if invite.Status != from {
		return domain.SharedInvite{}, domain.ErrSharedInviteSettled
	}
	now := time.Now().UTC()
	// Approving a lapsed invitation records it as live and sends nobody
	// anything: acceptance refuses it on the deadline, so the approval can
	// never become a shared channel. Withdrawing one stays available, because
	// clearing a queue of dead invitations is the remaining useful action and
	// refusing it would leave them there permanently.
	if to == domain.SharedInviteApproved && invite.Expired(now) {
		return domain.SharedInvite{}, domain.ErrInvitationExpired
	}
	if review.Conversation != "" {
		invite.ConversationID = review.Conversation
	}
	if review.SetExternalLimited {
		invite.ExternalLimited = review.ExternalLimited
	}
	invite.ReviewMessage = review.Message
	event, err := sharedInviteEvent(workspaceID, actorID, topic, invite, now)
	if err != nil {
		return domain.SharedInvite{}, err
	}
	if err := m.Store.SetSharedInviteStatus(ctx, id, from, to, now, review, event); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return domain.SharedInvite{}, domain.ErrSharedInviteSettled
		}
		return domain.SharedInvite{}, err
	}
	invite.Status = to
	if to == domain.SharedInviteApproved {
		invite.ReviewedAt = now
	} else {
		invite.SettledAt = now
	}
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
//
// The accepting organization does not get a channel of its own: it joins the
// host's conversation, under the host's name and visibility. So a request to
// join as a private channel is refused for a public conversation rather than
// answered by joining one everybody in the organization can read; asking for a
// public channel and joining a private one exposes nothing, and is allowed.
func (m Messages) AcceptSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID, private bool) (domain.Conversation, error) {
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
		return domain.Conversation{}, domain.ErrInvalidSharedInvite
	}
	if private {
		shared, getErr := m.Store.GetConversation(ctx, invite.ConversationID)
		if getErr != nil {
			return domain.Conversation{}, getErr
		}
		if !shared.PrivateFlag() {
			return domain.Conversation{}, domain.ErrInvalidSharedInvite
		}
	}
	now := time.Now().UTC()
	if !invite.Acceptable(now) {
		return domain.Conversation{}, domain.ErrSharedInviteSettled
	}
	accepted, err := sharedInviteEvent(workspaceID, actorID, "shared_invite.accepted", invite, now)
	if err != nil {
		return domain.Conversation{}, err
	}
	connected, err := newEvent(invite.WorkspaceID, actorID, events.NewPayload("conversation.connected",
		events.String("channel_id", string(invite.ConversationID)),
		events.String("team_id", string(invite.TargetWorkspaceID)),
		events.String("external_limited", boolText(invite.ExternalLimited)),
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
				return domain.Conversation{}, domain.ErrSlackConnectFull
			}
			return domain.Conversation{}, domain.ErrSharedInviteSettled
		}
		return domain.Conversation{}, err
	}
	return m.withSharedIdentity(ctx, conversation), nil
}

// ListSharedInvites reports one workspace's invitations in a status, from
// either side: the host sees what it sent and the invited organization sees
// what it was sent, which is what conversations.listConnectInvites and
// conversations.requestSharedInvite.list each ask for.
func (m Messages) ListSharedInvites(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, filter domain.SharedInviteFilter, request domain.PageRequest) (domain.SharedInvitePage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return domain.SharedInvitePage{}, err
	}
	if !filter.Valid() {
		return domain.SharedInvitePage{}, domain.ErrInvalidSharedInvite
	}
	return m.Store.ListSharedInvites(ctx, workspaceID, filter, request)
}

// sharedInviteTarget resolves an invitation's recipient to the organization
// and address it records. Exactly one of the three must be named.
func (m Messages) sharedInviteTarget(ctx context.Context, host domain.WorkspaceID, recipient domain.SharedInviteRecipient) (domain.WorkspaceID, string, error) {
	email := strings.ToLower(strings.TrimSpace(recipient.Email))
	named := 0
	for _, present := range []bool{recipient.Workspace != "", recipient.User != "", email != ""} {
		if present {
			named++
		}
	}
	if named != 1 {
		return "", "", domain.ErrInvalidSharedInvite
	}
	switch {
	case recipient.User != "":
		user, err := m.Store.GetUser(ctx, recipient.User)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", "", domain.ErrInvalidSharedInvite
			}
			return "", "", err
		}
		// A member of the host is already in reach of the channel, and a
		// deactivated account cannot accept anything.
		if user.Deleted || user.WorkspaceID == host || user.WorkspaceID == domain.SlackbotHomeWorkspaceID {
			return "", "", domain.ErrInvalidSharedInvite
		}
		return user.WorkspaceID, "", nil
	case email != "":
		if !strings.Contains(email, "@") {
			return "", "", domain.ErrInvalidSharedInvite
		}
		return "", email, nil
	default:
		return recipient.Workspace, "", nil
	}
}

// requireSharedInviteConversation is the check InviteShared applies to the
// host's own conversation, for an approval that moves an invitation to another
// one: a channel of the host's that the approving member is in, not a direct
// message, and not archived.
func (m Messages) requireSharedInviteConversation(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID) error {
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if err := m.requireConversationMembership(ctx, workspaceID, actorID, conversationID); err != nil {
		return err
	}
	if conversation.IsDirectOrGroup() {
		return domain.ErrInvalidSharedInvite
	}
	if conversation.Archived {
		return domain.ErrConversationAlreadyArchived
	}
	return nil
}

// ExternalTeams reports the organizations this workspace shares channels with.
//
// It is administrative: knowing every organization a workspace is connected to
// is a statement about the whole workspace rather than about a channel someone
// is in, and Slack puts it behind an administrator's token for the same reason.
// A member can still see the organizations in a channel they belong to, which
// is what conversations.info answers.
//
// The filter is applied to what a connection here can be: always connected,
// with no Slack Connect preference override, through this workspace. A filter
// that rules all of that out answers an empty page rather than an error, as a
// filter that matches nothing does.
func (m Messages) ExternalTeams(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, filter domain.ExternalTeamFilter, request domain.PageRequest) (domain.ExternalTeamPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.ExternalTeamPage{}, err
	}
	if !filter.Valid() {
		return domain.ExternalTeamPage{}, domain.ErrInvalidSharedInvite
	}
	if filter.MatchesNothing(workspaceID) {
		if err := store.CheckPage(request); err != nil {
			return domain.ExternalTeamPage{}, err
		}
		return domain.ExternalTeamPage{Teams: []domain.ExternalTeam{}}, nil
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
		return domain.ErrInvalidSharedInvite
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
// ExternalInvitePermission reports whether a connected organization may invite
// further organizations into a conversation. An organization with no recorded
// restriction may: the permission is a restriction a host applies, not a grant.
func (m Messages) ExternalInvitePermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID) (bool, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return false, err
	}
	if target == "" {
		return false, domain.ErrInvalidSharedInvite
	}
	return m.Store.GetExternalInvitePermission(ctx, workspaceID, conversationID, target)
}

func (m Messages) SetExternalInvitePermissions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, conversationID domain.ConversationID, target domain.WorkspaceID, canInvite bool) (domain.Conversation, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.Conversation{}, err
	}
	if target == "" {
		return domain.Conversation{}, domain.ErrInvalidSharedInvite
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
	_ = orgChannel
	// Withdrawing the ability to invite is a downgrade of the association, not
	// a removal of the organization: disconnecting is
	// admin.conversations.disconnectShared, and conflating the two would let a
	// permission change silently eject a participant.
	//
	// This used to re-write the team set with the same teams and announce
	// can_invite in an event that changed no queryable state — so nothing could
	// read the permission back and nothing enforced it. It now writes durable
	// per-team state, which ExternalInvitePermission reads and InviteShared
	// enforces.
	event, err := newEvent(workspaceID, actorID, events.NewPayload("conversation.external_invite_permissions_set",
		events.String("channel_id", string(conversationID)),
		events.String("team_id", string(target)),
		events.String("can_invite", boolText(canInvite)),
	), time.Now().UTC())
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := m.Store.SetExternalInvitePermission(ctx, workspaceID, conversationID, target, canInvite, event); err != nil {
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
