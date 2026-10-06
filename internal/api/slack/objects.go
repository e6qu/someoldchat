package slack

import (
	"bytes"
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
	"github.com/sameoldchat/sameoldchat/internal/l10n"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
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

// workspaceLocale is the locale include_locale reports for a conversation,
// and for a member who chose no language. Nothing here stores a
// per-workspace locale, and the source catalog the product falls back to is
// English, which Slack spells en-US.
const workspaceLocale = "en-US"

// memberLocale is the locale include_locale reports for a member: the
// language they chose in Language & region, as the catalog it resolves to,
// else the source language. users.info and users.list used to drop
// include_locale, so no member object carried one.
func memberLocale(user domain.User) string {
	chosen := strings.TrimSpace(user.Locale)
	if chosen == "" {
		return workspaceLocale
	}
	locale := l10n.Negotiate(l10n.Locale(chosen), "")
	if locale == l10n.Default {
		return workspaceLocale
	}
	return string(locale)
}

// defaultAvatar serves the image a member without a photo is shown with: a
// square in their color at one of the profile sizes. It is a PNG rendered
// here, not a script-capable format, so it is safe on a public URL; the path
// names the member only to pick the color, and discloses nothing userColor
// does not already put in every user object.
func (h Handler) defaultAvatar(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("workspace") == "" || r.PathValue("user") == "" {
		http.NotFound(w, r)
		return
	}
	writeGeneratedSquare(w, r, slackobject.DefaultAvatarSizes, slackobject.UserColor(domain.UserID(r.PathValue("user"))))
}

// teamIconSizes are the sizes Slack's icon object names an image for.
var teamIconSizes = map[int]bool{34: true, 44: true, 68: true, 88: true, 102: true, 132: true, 230: true}

// defaultTeamIcon is defaultAvatar for a workspace without an icon.
func (h Handler) defaultTeamIcon(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("workspace") == "" {
		http.NotFound(w, r)
		return
	}
	writeGeneratedSquare(w, r, teamIconSizes, slackobject.UserColor(domain.UserID(r.PathValue("workspace"))))
}

// writeGeneratedSquare answers {size}.png, for one of the given sizes, with a
// square of the given color.
func writeGeneratedSquare(w http.ResponseWriter, r *http.Request, sizes map[int]bool, hexColor string) {
	name, found := strings.CutSuffix(r.PathValue("file"), ".png")
	size, err := strconv.Atoi(name)
	rgb, colorErr := hex.DecodeString(hexColor)
	if !found || err != nil || !sizes[size] || colorErr != nil || len(rgb) != 3 {
		http.NotFound(w, r)
		return
	}
	var encoded bytes.Buffer
	square := &boundedImage{Uniform: image.NewUniform(color.RGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 0xff}), bounds: image.Rect(0, 0, size, size)}
	if err := png.Encode(&encoded, square); err != nil {
		// Encoding a uniform image into memory has no failure mode a caller
		// could act on; this is an unexpected fault, not a handled refusal.
		http.Error(w, "image unavailable", http.StatusInternalServerError)
		return
	}
	blobHeaders(w, "image/png", "image")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(encoded.Bytes())
}

// teamResponse renders Slack's team object. The pinned objs_team requires id,
// name, domain, email_domain and icon; team.info reported the first three, with
// an empty domain for every workspace that was not created through
// admin.teams.create.
func teamResponse(origin string, team domain.Workspace) map[string]any {
	icon := map[string]any{"image_default": team.IconURL == ""}
	for size := range teamIconSizes {
		value := team.IconURL
		switch {
		case value == "":
			value = origin + "/team-icons/" + url.PathEscape(string(team.ID)) + "/" + strconv.Itoa(size) + ".png"
		case strings.HasPrefix(value, "/"):
			value = origin + value
		}
		icon["image_"+strconv.Itoa(size)] = value
	}
	return map[string]any{
		"id": team.ID, "name": team.Name, "domain": team.SlackDomain(),
		// Nothing here restricts sign-up to an email domain, which is what
		// Slack reports as an empty email_domain.
		"email_domain": "", "icon": icon, "url": origin + "/",
		"avatar_base_url": origin + "/avatars/", "is_verified": false,
	}
}

// boundedImage is a uniform color with finite bounds, which is what png.Encode
// needs; image.Uniform alone is infinitely large.
type boundedImage struct {
	*image.Uniform
	bounds image.Rectangle
}

func (b *boundedImage) Bounds() image.Rectangle { return b.bounds }
