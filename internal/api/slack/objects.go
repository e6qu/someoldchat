package slack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// userResponse renders Slack's user object.
//
// The pinned objs_user requires id, name, profile, is_bot, updated and
// is_app_user, and Slack's clients read the role and guest flags, tz, color and
// the image URLs from it. The object used to carry six fields, so a typed client
// (slack-api-client's User) read a null is_bot for every bot, and every avatar
// was either empty or a path with no host that no client could fetch.
func userResponse(user domain.User, includeEmail bool, origin string) map[string]any {
	profile := profileResponse(user, origin)
	if !includeEmail {
		delete(profile, "email")
	}
	return map[string]any{
		"id": user.ID, "team_id": user.WorkspaceID, "name": user.Name, "real_name": user.RealName, "deleted": user.Deleted, "profile": profile,
		"color": userColor(user.ID),
		// Nothing here records a member's time zone, so every member is on UTC
		// rather than on a zone somebody guessed for them.
		"tz": "UTC", "tz_label": "Coordinated Universal Time", "tz_offset": 0,
		"is_admin": user.Role == domain.WorkspaceRoleAdmin || user.Role == domain.WorkspaceRoleOwner,
		// A workspace here has owners but no distinguished primary owner, so
		// each owner reports both, as admin.users.list always has.
		"is_owner": user.Role == domain.WorkspaceRoleOwner, "is_primary_owner": user.Role == domain.WorkspaceRoleOwner,
		"is_restricted": user.Restricted, "is_ultra_restricted": user.UltraRestricted,
		"is_bot": user.IsBot(), "is_app_user": false, "is_email_confirmed": user.Email != "", "has_2fa": false,
		"updated": unixSeconds(user.Updated),
	}
}

// profileResponse renders Slack's user profile object. Every image is an
// absolute URL: a photo uploaded here, an external URL a member set, or the
// generated default avatar this server serves at the requested size.
func profileResponse(user domain.User, origin string) map[string]any {
	firstName, lastName, _ := strings.Cut(strings.TrimSpace(user.RealName), " ")
	profile := map[string]any{
		"display_name": user.Profile.DisplayName, "display_name_normalized": user.Profile.DisplayName, "email": user.Email,
		"real_name": user.RealName, "real_name_normalized": user.RealName,
		"first_name": firstName, "last_name": strings.TrimSpace(lastName),
		"title": "", "phone": "", "skype": "", "pronouns": "", "fields": map[string]any{},
		"status_text": user.Profile.StatusText, "status_emoji": user.Profile.StatusEmoji, "status_expiration": unixSeconds(user.Profile.StatusExpiration),
		"avatar_hash": avatarHash(user),
		"team":        user.WorkspaceID, "user_id": user.ID,
	}
	for _, image := range []struct {
		size   int
		stored string
	}{{24, user.Profile.Image24}, {32, user.Profile.Image32}, {48, user.Profile.Image48}, {72, user.Profile.Image72},
		{192, user.Profile.Image192}, {512, user.Profile.Image512}, {1024, user.Profile.Image1024}} {
		profile["image_"+strconv.Itoa(image.size)] = userImageURL(origin, user, image.size, image.stored)
	}
	if user.IsBot() {
		profile["bot_id"], profile["api_app_id"], profile["always_active"] = user.BotID, user.AppID, false
	}
	return profile
}

// userImageURL makes a stored image reference absolute. A stored path is one of
// this server's photo URLs; an empty one falls back to the default avatar.
func userImageURL(origin string, user domain.User, size int, stored string) string {
	switch {
	case stored == "":
		return origin + defaultAvatarPath(user.WorkspaceID, user.ID, size)
	case strings.HasPrefix(stored, "/"):
		return origin + stored
	}
	return stored
}

// defaultAvatarSizes are the sizes Slack's profile object names an image for.
var defaultAvatarSizes = map[int]bool{24: true, 32: true, 48: true, 72: true, 192: true, 512: true, 1024: true}

func defaultAvatarPath(workspace domain.WorkspaceID, user domain.UserID, size int) string {
	return "/avatars/" + url.PathEscape(string(workspace)) + "/" + url.PathEscape(string(user)) + "/" + strconv.Itoa(size) + ".png"
}

// avatarHash identifies the image a profile shows, so a client can tell when to
// refetch it. An uploaded photo is identified by its token; the default avatar
// by the member, with Slack's g prefix for a generated image.
func avatarHash(user domain.User) string {
	if _, token, found := strings.Cut(user.Profile.Image24, "/photo/"); found && token != "" {
		if len(token) > 12 {
			token = token[:12]
		}
		return token
	}
	sum := sha256.Sum256([]byte(string(user.WorkspaceID) + "/" + string(user.ID)))
	return "g" + hex.EncodeToString(sum[:])[:11]
}

// userColors is the palette Slack draws a member's name color from.
var userColors = []string{"9f69e7", "4bbe2e", "e7392d", "3c989f", "674b1b", "e96699", "e0a729", "5b89d5", "2b6836", "99d04a", "df3dc0", "dc7dbb", "d1707d", "a63024", "aba727", "965d1b", "8f4a2b", "902d59", "de5f24", "385a86"}

// userColor is a member's stable color: the same member is always drawn the same.
func userColor(user domain.UserID) string {
	sum := sha256.Sum256([]byte(user))
	return userColors[int(sum[0])%len(userColors)]
}

// defaultAvatar serves the image a member without a photo is shown with: a
// square in their color at one of the profile sizes. It is a PNG rendered
// here, not a script-capable format, so it is safe on a public URL; the path
// names the member only to pick the color, and discloses nothing userColor
// does not already put in every user object.
func (h Handler) defaultAvatar(w http.ResponseWriter, r *http.Request) {
	name, found := strings.CutSuffix(r.PathValue("file"), ".png")
	size, err := strconv.Atoi(name)
	if !found || err != nil || !defaultAvatarSizes[size] || r.PathValue("workspace") == "" || r.PathValue("user") == "" {
		http.NotFound(w, r)
		return
	}
	rgb, err := hex.DecodeString(userColor(domain.UserID(r.PathValue("user"))))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canvas := image.NewUniform(color.RGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, &boundedImage{Uniform: canvas, bounds: image.Rect(0, 0, size, size)}); err != nil {
		http.Error(w, "avatar unavailable", http.StatusInternalServerError)
		return
	}
	capabilityHeaders(w)
	blobHeaders(w, "image/png", "avatar")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(encoded.Bytes())
}

// boundedImage is a uniform color with finite bounds, which is what png.Encode
// needs; image.Uniform alone is infinitely large.
type boundedImage struct {
	*image.Uniform
	bounds image.Rectangle
}

func (b *boundedImage) Bounds() image.Rectangle { return b.bounds }
