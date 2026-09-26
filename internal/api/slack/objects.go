package slack

import (
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// conversationResponse renders Slack's conversation object for the reader the
// service described the conversation for.
//
// The pinned objs_conversation schema requires id, name, created, creator,
// is_archived, is_channel, is_general, is_mpim, is_group, is_org_shared, is_im,
// is_shared, is_private, name_normalized, topic and purpose for a channel, and
// id, created, is_im, is_org_shared, user and priority for an IM. The object
// used to carry about half of that, with is_member the literal true, so a typed
// client (slack-api-client's Conversation) read a zero creator and a null topic
// creator, and every reader was told it belonged to every channel it listed.
//
// num_members is left to the methods that report it, because Slack's own
// users.conversations omits it and conversations.info reports it only on
// request.
func conversationResponse(conversation domain.Conversation) map[string]any {
	if conversation.Kind == domain.ConversationTypeIM {
		return imResponse(conversation)
	}
	name := conversation.Name
	if conversation.Kind == domain.ConversationTypeMPIM && conversation.GroupDirectHandle != "" {
		name = conversation.GroupDirectHandle
	}
	return map[string]any{
		"id": conversation.ID, "name": name, "name_normalized": name,
		"created": unixSeconds(conversation.Created), "creator": conversation.CreatorID,
		"is_archived": conversation.Archived, "is_private": conversation.PrivateFlag(),
		"is_channel": conversation.Kind.OrPublic() == domain.ConversationTypePublic,
		// is_group is Slack's marker for a private channel created before
		// March 2021, whose identifier begins with G. No conversation here has
		// one, so every private channel is the modern kind: is_private alone.
		"is_group": false,
		"is_im":    false, "is_mpim": conversation.Kind == domain.ConversationTypeMPIM,
		"is_general": conversation.IsGeneral, "is_member": conversation.IsMember,
		"team_id": conversation.WorkspaceID, "context_team_id": conversation.WorkspaceID,
		"shared_team_ids": []domain.WorkspaceID{conversation.WorkspaceID},
		// The Slack Connect identity. Pending and shared are different facts —
		// an outstanding invitation is not a connection — and a client renders
		// each differently, so neither is derived from the other. An
		// organization-wide share is an Enterprise Grid feature this product
		// does not have, so is_org_shared is always false.
		"is_ext_shared": conversation.IsExtShared, "is_pending_ext_shared": conversation.IsPendingExtShared,
		"is_shared": conversation.IsExtShared, "is_org_shared": false,
		"pending_shared": []string{}, "pending_connected_team_ids": []string{},
		"unlinked": 0, "parent_conversation": nil, "previous_names": []string{},
		"topic":   conversationTextResponse(conversation.Topic, conversation.TopicSetBy, conversation.TopicSetAt),
		"purpose": conversationTextResponse(conversation.Purpose, conversation.PurposeSetBy, conversation.PurposeSetAt),
	}
}

// imResponse is the IM variant of the conversation object: a one-to-one DM is
// described by who it is with, not by a name.
func imResponse(conversation domain.Conversation) map[string]any {
	return map[string]any{
		"id": conversation.ID, "created": unixSeconds(conversation.Created),
		"is_im": true, "is_channel": false, "is_group": false, "is_mpim": false, "is_private": true,
		"is_archived": conversation.Archived, "is_member": conversation.IsMember,
		"is_org_shared": false, "is_shared": false, "is_ext_shared": false,
		"user": conversation.DirectUserID, "is_user_deleted": conversation.DirectUserDeleted,
		// Slack ranks a reader's DMs by a priority it computes from their
		// activity. There is no such ranking here, so every DM is equal.
		"priority":        0,
		"team_id":         conversation.WorkspaceID,
		"context_team_id": conversation.WorkspaceID,
	}
}

// conversationTextResponse is a topic or purpose: the value, who last set it,
// and when. An unset one is the empty creator at zero, which the pinned
// defs_topic_purpose_creator pattern admits.
func conversationTextResponse(value string, setBy domain.UserID, setAt time.Time) map[string]any {
	return map[string]any{"value": value, "creator": setBy, "last_set": unixSeconds(setAt)}
}

// workspaceLocale is the locale include_locale reports. Nothing here stores a
// per-member or per-workspace locale and the first-party client is English
// only, so this is the one locale every reader actually gets.
const workspaceLocale = "en-US"
