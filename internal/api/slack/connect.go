package slack

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The nine conversations.*SharedInvite* methods. They were ledger rows with no
// route: the post-connection half of Slack Connect existed and there was no way
// to reach it.
//
// Read, send and decide carry three different scopes on purpose. Folding them
// into conversations:manage would let anyone who can rename a channel admit an
// outside organization to it.
//
// The arguments are the ones the published SDKs send (python slack_sdk 3.45,
// the Java slack-api-client 1.52 and @slack/web-api 8.2). Each method used to
// read one or two of them and drop the rest, which answered ok while ignoring
// what the caller asked for — an external-limited invitation admitted an
// unrestricted organization, and a denial's reason reached nobody.

func (h Handler) conversationInviteShared(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	// The invitation names one recipient: an address, or a person in another
	// organization on this deployment. external_limited is a boolean that
	// defaults to true; it used to be read as the identifier of the
	// organization to invite, so the published SDKs could not name a recipient
	// at all and a caller passing `external_limited=true` invited a workspace
	// called "true".
	emails := parseIDList[string](fields["emails"])
	users := parseIDList[domain.UserID](fields["user_ids"])
	if len(emails)+len(users) != 1 {
		writeError(w, "invalid_arg_name")
		return
	}
	recipient := domain.SharedInviteRecipient{ExternalLimited: true}
	if raw, present := fields["external_limited"]; present && strings.TrimSpace(raw) != "" {
		if recipient.ExternalLimited, err = parseBoolField(raw); err != nil {
			writeError(w, "invalid_arg_name")
			return
		}
	}
	if len(users) == 1 {
		recipient.User = users[0]
	} else {
		recipient.Email = emails[0]
	}
	invite, err := h.Messages.InviteShared(r.Context(), principal.WorkspaceID, principal.UserID, channel, recipient)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite_id": string(invite.ID), "invite": sharedInviteResponse(invite)})
}

// conversationApproveSharedInvite and its three siblings are separate methods
// rather than one closure factory because both structural gates AST-parse the
// route table and cannot resolve a handler built at registration time.
func (h Handler) conversationApproveSharedInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	h.decideSharedInvite(w, r, principal, fields, h.approveSharedInvite, domain.SharedInviteReview{})
}

// conversationRequestSharedInviteApprove is the same decision reached through
// the request-oriented method name Slack also publishes, which carries the
// host's review: the channel to invite to, whether the organization joins
// external-limited, and a message to attach.
func (h Handler) conversationRequestSharedInviteApprove(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	review := domain.SharedInviteReview{Conversation: domain.ConversationID(strings.TrimSpace(fields["channel_id"]))}
	if raw := strings.TrimSpace(fields["is_external_limited"]); raw != "" {
		review.SetExternalLimited = true
		if review.ExternalLimited, err = parseBoolField(raw); err != nil {
			writeError(w, "invalid_arg_name")
			return
		}
	}
	// message is an object, {is_override, text}, which the SDKs send as JSON.
	// No request here carries a message of its own for the text to override
	// or extend, so the text is the whole message either way.
	if raw := strings.TrimSpace(fields["message"]); raw != "" {
		var message struct {
			IsOverride bool   `json:"is_override"`
			Text       string `json:"text"`
		}
		if json.Unmarshal([]byte(raw), &message) != nil {
			writeError(w, "invalid_arg_name")
			return
		}
		review.Message = message.Text
	}
	h.decideSharedInvite(w, r, principal, fields, h.approveSharedInvite, review)
}

func (h Handler) conversationRequestSharedInviteDeny(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// message is what the member who asked for the invitation is told.
	h.decideSharedInvite(w, r, principal, fields, h.denySharedInvite, domain.SharedInviteReview{Message: fields["message"]})
}

func (h Handler) conversationDeclineSharedInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	h.decideSharedInvite(w, r, principal, fields, h.declineSharedInvite, domain.SharedInviteReview{})
}

func (h Handler) decideSharedInvite(w http.ResponseWriter, r *http.Request, principal auth.Principal, fields map[string]string, decide func(*http.Request, auth.Principal, domain.SharedInviteID, domain.SharedInviteReview) (domain.SharedInvite, error), review domain.SharedInviteReview) {
	id := domain.SharedInviteID(strings.TrimSpace(fields["invite_id"]))
	if id == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	// target_team names the other party to the invitation. When it is given
	// it must be that party: deciding an invitation the caller described as
	// being to or from somebody else would act on the wrong one.
	if target := domain.WorkspaceID(strings.TrimSpace(fields["target_team"])); target != "" {
		invite, ok := h.sharedInviteByID(w, r, principal, id)
		if !ok {
			return
		}
		other := invite.TargetWorkspaceID
		if other == principal.WorkspaceID {
			other = invite.WorkspaceID
		}
		if other != target {
			writeError(w, "invite_not_found")
			return
		}
	}
	invite, err := decide(r, principal, id, review)
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite_id": string(invite.ID), "invite": sharedInviteResponse(invite)})
}

// sharedInviteByID reads one invitation the caller's workspace is party to,
// through the same listing the caller could read it from.
func (h Handler) sharedInviteByID(w http.ResponseWriter, r *http.Request, principal auth.Principal, id domain.SharedInviteID) (domain.SharedInvite, bool) {
	page, err := h.Messages.ListSharedInvites(r.Context(), principal.WorkspaceID, principal.UserID, domain.SharedInviteFilter{
		Statuses: allSharedInviteStatuses, IDs: []domain.SharedInviteID{id},
	}, domain.PageRequest{Limit: 1})
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return domain.SharedInvite{}, false
	}
	if len(page.Invites) == 0 {
		writeError(w, "invite_not_found")
		return domain.SharedInvite{}, false
	}
	return page.Invites[0], true
}

var allSharedInviteStatuses = []domain.SharedInviteStatus{
	domain.SharedInvitePending, domain.SharedInviteApproved, domain.SharedInviteAccepted, domain.SharedInviteDeclined, domain.SharedInviteRevoked,
}

func (h Handler) conversationAcceptSharedInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if namesForeignTeam(fields, principal) {
		writeError(w, "invalid_arg_name")
		return
	}
	private, err := parseBoolField(fields["is_private"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	// The invitation is named by its identifier or by the channel it is for;
	// the SDKs require one of the two. channel_name and free_trial_accepted
	// are read by Slack when the acceptance creates a channel of the invited
	// organization's own and when it starts a paid trial. Neither happens
	// here: the organization joins the host's conversation under the host's
	// name, and there is no billing.
	id := domain.SharedInviteID(strings.TrimSpace(fields["invite_id"]))
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if id == "" && channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if channel != "" {
		filter := domain.SharedInviteFilter{Statuses: []domain.SharedInviteStatus{domain.SharedInviteApproved}, Conversation: channel}
		if id != "" {
			filter.IDs = []domain.SharedInviteID{id}
		}
		page, listErr := h.Messages.ListSharedInvites(r.Context(), principal.WorkspaceID, principal.UserID, filter, domain.PageRequest{Limit: 200})
		if listErr != nil {
			writeError(w, mapServiceError(listErr, "invite_not_found"))
			return
		}
		index := slices.IndexFunc(page.Invites, func(invite domain.SharedInvite) bool { return invite.TargetWorkspaceID == principal.WorkspaceID })
		if index < 0 {
			writeError(w, "invite_not_found")
			return
		}
		id = page.Invites[index].ID
	}
	conversation, err := h.Messages.AcceptSharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id, private)
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                    true,
		"implicit_approval":     false,
		"channel_id":            string(conversation.ID),
		"channel":               map[string]any{"id": string(conversation.ID)},
		"invite_id":             string(id),
		"is_ext_shared":         conversation.IsExtShared,
		"is_pending_ext_shared": conversation.IsPendingExtShared,
	})
}

// conversationListConnectInvites reports invitations that were issued and not
// yet answered. Its page size is `count`, which is what the SDKs send; `limit`
// is still read for a caller that used it.
func (h Handler) conversationListConnectInvites(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if namesForeignTeam(fields, principal) {
		writeError(w, "invalid_arg_name")
		return
	}
	size := fields["count"]
	if strings.TrimSpace(size) == "" {
		size = fields["limit"]
	}
	page, ok := h.sharedInvitePage(w, r, principal, fields, size, domain.SharedInviteFilter{Statuses: []domain.SharedInviteStatus{domain.SharedInviteApproved}})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"invites":           sharedInviteResponses(page.Invites),
		"response_metadata": map[string]any{"next_cursor": string(page.NextCursor)},
	})
}

// conversationRequestSharedInviteList reports members' requests to invite an
// organization. By default only those still awaiting a decision are listed;
// include_approved, include_denied and include_expired add the others, and
// invite_ids and user_id narrow the list to particular requests or to one
// requesting member.
func (h Handler) conversationRequestSharedInviteList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	options, err := parseBoolFields(fields, "include_approved", "include_denied", "include_expired")
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	filter := domain.SharedInviteFilter{
		Decisions: []domain.SharedInviteDecision{domain.SharedInviteDecisionPending},
		IDs:       parseIDList[domain.SharedInviteID](fields["invite_ids"]),
		InvitedBy: domain.UserID(strings.TrimSpace(fields["user_id"])),
	}
	if options[0] {
		filter.Decisions = append(filter.Decisions, domain.SharedInviteDecisionApproved)
	}
	if options[1] {
		filter.Decisions = append(filter.Decisions, domain.SharedInviteDecisionDenied)
	}
	if !options[2] {
		filter.ExcludeExpiredAt = time.Now().UTC()
	}
	page, ok := h.sharedInvitePage(w, r, principal, fields, fields["limit"], filter)
	if !ok {
		return
	}
	requests := make([]map[string]any, 0, len(page.Invites))
	for _, invite := range page.Invites {
		requests = append(requests, sharedInviteRequestResponse(invite))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"invites":           sharedInviteResponses(page.Invites),
		"invite_requests":   requests,
		"response_metadata": map[string]any{"next_cursor": string(page.NextCursor)},
	})
}

// sharedInvitePage reads one page of the listing both methods share. A status
// argument is no longer read: no published SDK sends one, and each method's
// own arguments say which invitations it reports.
func (h Handler) sharedInvitePage(w http.ResponseWriter, r *http.Request, principal auth.Principal, fields map[string]string, size string, filter domain.SharedInviteFilter) (domain.SharedInvitePage, bool) {
	limit, err := clampLimit(size, 100, 200)
	if err != nil {
		writeDecodeError(w, err)
		return domain.SharedInvitePage{}, false
	}
	cursor, err := decodeCursor(fields["cursor"], "invalid_cursor")
	if err != nil {
		writeDecodeError(w, err)
		return domain.SharedInvitePage{}, false
	}
	page, err := h.Messages.ListSharedInvites(r.Context(), principal.WorkspaceID, principal.UserID, filter, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return domain.SharedInvitePage{}, false
	}
	return page, true
}

func sharedInviteResponses(values []domain.SharedInvite) []map[string]any {
	invites := make([]map[string]any, 0, len(values))
	for _, invite := range values {
		invites = append(invites, sharedInviteResponse(invite))
	}
	return invites
}

// sharedInviteRequestResponse is one entry of requestSharedInvite.list's
// invite_requests, in the field names Slack's response carries.
func sharedInviteRequestResponse(invite domain.SharedInvite) map[string]any {
	updated := invite.CreatedAt
	for _, at := range []time.Time{invite.ReviewedAt, invite.SettledAt} {
		if at.After(updated) {
			updated = at
		}
	}
	result := map[string]any{
		"id":                  string(invite.ID),
		"channel":             map[string]any{"id": string(invite.ConversationID)},
		"inviting_user":       map[string]any{"id": string(invite.InvitedBy)},
		"is_external_limited": invite.ExternalLimited,
		"date_created":        invite.CreatedAt.Unix(),
		"date_last_updated":   updated.Unix(),
	}
	if !invite.ExpiresAt.IsZero() {
		result["expires_at"] = invite.ExpiresAt.Unix()
	}
	if invite.TargetEmail != "" {
		result["target_user"] = map[string]any{"recipient_email": invite.TargetEmail}
	}
	return result
}

func (h Handler) conversationExternalInvitePermissionsSet(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	target := domain.WorkspaceID(strings.TrimSpace(fields["target_team"]))
	action := strings.ToLower(strings.TrimSpace(fields["action"]))
	if channel == "" || target == "" || (action != "upgrade" && action != "downgrade") {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.SetExternalInvitePermissions(r.Context(), principal.WorkspaceID, principal.UserID, channel, target, action == "upgrade")
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": map[string]any{"id": string(conversation.ID)}})
}

func sharedInviteResponse(invite domain.SharedInvite) map[string]any {
	result := map[string]any{
		"id":                  string(invite.ID),
		"channel_id":          string(invite.ConversationID),
		"status":              string(invite.Status),
		"invited_by":          string(invite.InvitedBy),
		"date_created":        invite.CreatedAt.Unix(),
		"is_external_limited": invite.ExternalLimited,
	}
	if invite.TargetWorkspaceID != "" {
		result["target_team"] = string(invite.TargetWorkspaceID)
	}
	if invite.TargetEmail != "" {
		result["target_email"] = invite.TargetEmail
	}
	if !invite.ExpiresAt.IsZero() {
		result["date_invalid"] = invite.ExpiresAt.Unix()
	}
	return result
}

// The three host-side decisions and the invited side's one, as methods so the
// route table reads as a list of operations rather than of closures. Only the
// host's decisions on a pending invitation carry a review.
func (h Handler) approveSharedInvite(r *http.Request, principal auth.Principal, id domain.SharedInviteID, review domain.SharedInviteReview) (domain.SharedInvite, error) {
	return h.Messages.ApproveSharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id, review)
}

func (h Handler) denySharedInvite(r *http.Request, principal auth.Principal, id domain.SharedInviteID, review domain.SharedInviteReview) (domain.SharedInvite, error) {
	return h.Messages.DenySharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id, review)
}

func (h Handler) declineSharedInvite(r *http.Request, principal auth.Principal, id domain.SharedInviteID, _ domain.SharedInviteReview) (domain.SharedInvite, error) {
	return h.Messages.DeclineSharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id)
}
