package slack

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// admin.conversations.bulkSetProperties sets one channel property on up to
// 100 channels. The reference documents a user token only, one property per
// request as a JSON object, and no response body beyond ok. Channels that are
// not channels of this workspace are skipped; only a request naming none of
// them is refused, with no_valid_channels.
func (h Handler) adminConversationsBulkSetProperties(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	ids := parseIDList[domain.ConversationID](fields["channel_ids"])
	if len(ids) == 0 || len(ids) > domain.ConversationPropertyBulkLimit || strings.TrimSpace(fields["property"]) == "" {
		writeError(w, "invalid_arguments")
		return
	}
	property, err := domain.ParseConversationProperty(fields["property"])
	if err != nil {
		writeError(w, conversationPropertyFailure(err))
		return
	}
	if err := h.Messages.AdminBulkSetConversationProperties(r.Context(), principal.WorkspaceID, principal.UserID, ids, property); err != nil {
		writeError(w, conversationPropertyFailure(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// conversationPropertyFailure names a bulkSetProperties failure with the codes
// that method's reference declares.
func conversationPropertyFailure(err error) string {
	switch {
	case errors.Is(err, domain.ErrTooManyConversationProperties):
		return "too_many_properties"
	case errors.Is(err, domain.ErrConversationPropertyNotAllowed):
		return "property_not_allowed"
	case errors.Is(err, domain.ErrInvalidConversationProperty), errors.Is(err, domain.ErrInvalidConversation):
		return "invalid_arguments"
	case errors.Is(err, domain.ErrNoValidChannels):
		return "no_valid_channels"
	// The reference's restricted_action is "user does not have permission to
	// perform this action", which is the member who is not an administrator.
	case errors.Is(err, domain.ErrNotWorkspaceAdmin):
		return "restricted_action"
	}
	return mapServiceErrorNamed(err, "no_valid_channels", "invalid_arguments", "")
}
