// Package slackobject renders the Slack object shapes more than one surface
// emits: the user object and the file object. The Web API (users.info,
// files.info, message file shares) and the event projections (user_change,
// team_join, message and file events on the Events API, Socket Mode and RTM)
// used to build their own copies, and the copies disagreed — the event user
// object reported every bot as a person and carried image paths no client
// could fetch. One renderer makes that disagreement unrepresentable.
//
// Every URL is built on an origin: the deployment's public URL, or for the
// Web API without one, the request's own origin. An empty origin renders
// origin-relative paths, which is what a durable journal snapshot stores; the
// delivery projection resolves them with Absolute when it knows the origin.
package slackobject

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Origin normalizes a configured public URL into the base every URL is
// joined to: no trailing slash, and empty when none is configured.
func Origin(publicURL string) string {
	return strings.TrimRight(strings.TrimSpace(publicURL), "/")
}

// ErrInvalidPublicURL refuses a public URL clients could not be sent to.
var ErrInvalidPublicURL = errors.New("public URL must be an absolute HTTPS URL, except for an explicit loopback development coordinate, without query, fragment or credentials")

// ParsePublicURL validates -auth-public-url for a process that builds URLs on
// it but serves no web client — the chat service and the event delivery
// worker — by the rule the web client applies (web.ValidatePublicURL), and
// returns its Origin.
func ParsePublicURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidPublicURL
	}
	if parsed.Scheme != "https" {
		host := strings.Trim(strings.ToLower(parsed.Hostname()), "[]")
		address := net.ParseIP(host)
		if parsed.Scheme != "http" || !(host == "localhost" || strings.HasSuffix(host, ".localhost") || address != nil && address.IsLoopback()) {
			return "", ErrInvalidPublicURL
		}
	}
	return Origin(parsed.String()), nil
}

// Absolute resolves an origin-relative path this server minted against
// origin. Anything else — an absolute URL a member set, an empty value, a
// protocol-relative reference — is returned unchanged, as is every value when
// origin is empty.
func Absolute(origin, value string) string {
	if origin == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return value
	}
	return origin + value
}

// User renders Slack's user object.
//
// The pinned objs_user requires id, name, profile, is_bot, updated and
// is_app_user, and Slack's clients read the role and guest flags, tz, color and
// the image URLs from it. The e-mail address is included only for a reader
// holding users:read.email.
func User(origin string, user domain.User, includeEmail bool) map[string]any {
	profile := Profile(origin, user)
	if !includeEmail {
		delete(profile, "email")
	}
	tz, tzLabel, tzOffset := Timezone(user, time.Now())
	return map[string]any{
		"id": user.ID, "team_id": user.WorkspaceID, "name": user.Name, "real_name": user.RealName, "deleted": user.Deleted, "profile": profile,
		"color": UserColor(user.ID),
		"tz":    tz, "tz_label": tzLabel, "tz_offset": tzOffset,
		"is_admin": user.Role == domain.WorkspaceRoleAdmin || user.Role == domain.WorkspaceRoleOwner,
		"is_owner": user.Role == domain.WorkspaceRoleOwner, "is_primary_owner": user.PrimaryOwner,
		"is_restricted": user.Restricted, "is_ultra_restricted": user.UltraRestricted,
		"is_bot": user.IsBot(), "is_app_user": false, "is_email_confirmed": user.Email != "", "has_2fa": false,
		"updated": unixSeconds(user.Updated),
	}
}

// Timezone is the tz, tz_label and tz_offset triple Slack's user object
// carries, at the instant given. A member whose client never reported a zone
// (or reported one this host cannot load) is on UTC rather than on a zone
// somebody guessed for them.
func Timezone(user domain.User, at time.Time) (string, string, int) {
	if name := strings.TrimSpace(user.Profile.Timezone); name != "" {
		if location, err := time.LoadLocation(name); err == nil {
			abbreviation, offset := at.In(location).Zone()
			return name, abbreviation, offset
		}
	}
	return "UTC", "Coordinated Universal Time", 0
}

// Profile renders Slack's user profile object. Every image is a URL on origin
// or one a member set: a photo uploaded here, an external URL, or the
// generated default avatar this server serves at the requested size.
func Profile(origin string, user domain.User) map[string]any {
	firstName, lastName := user.NameParts()
	profile := map[string]any{
		"display_name": user.Profile.DisplayName, "display_name_normalized": user.Profile.DisplayName, "email": user.Email,
		"real_name": user.RealName, "real_name_normalized": user.RealName,
		"first_name": firstName, "last_name": lastName,
		"title": user.Profile.Title, "phone": user.Profile.Phone, "skype": "", "pronouns": user.Profile.Pronouns, "fields": map[string]any{},
		"status_text": user.Profile.StatusText, "status_emoji": user.Profile.StatusEmoji, "status_expiration": unixSeconds(user.Profile.StatusExpiration),
		"avatar_hash": AvatarHash(user),
		"team":        user.WorkspaceID, "user_id": user.ID,
	}
	for _, image := range []struct {
		size   int
		stored string
	}{{24, user.Profile.Image24}, {32, user.Profile.Image32}, {48, user.Profile.Image48}, {72, user.Profile.Image72},
		{192, user.Profile.Image192}, {512, user.Profile.Image512}, {1024, user.Profile.Image1024}} {
		profile["image_"+strconv.Itoa(image.size)] = UserImageURL(origin, user, image.size, image.stored)
	}
	if user.IsBot() {
		profile["bot_id"], profile["api_app_id"], profile["always_active"] = user.BotID, user.AppID, false
	}
	return profile
}

// AbsoluteUser resolves the origin-relative image URLs of a user object that
// User rendered with an empty origin — the form a journal snapshot stores.
func AbsoluteUser(origin string, encoded json.RawMessage) (json.RawMessage, error) {
	if origin == "" {
		return encoded, nil
	}
	var user map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &user); err != nil {
		return nil, err
	}
	var profile map[string]json.RawMessage
	if raw, ok := user["profile"]; !ok || json.Unmarshal(raw, &profile) != nil || profile == nil {
		return encoded, nil
	}
	for name, raw := range profile {
		if !strings.HasPrefix(name, "image_") {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		resolved, err := json.Marshal(Absolute(origin, value))
		if err != nil {
			return nil, err
		}
		profile[name] = resolved
	}
	rewritten, err := json.Marshal(profile)
	if err != nil {
		return nil, err
	}
	user["profile"] = rewritten
	return json.Marshal(user)
}

// UserImageURL makes a stored image reference absolute. A stored path is one of
// this server's photo URLs; an empty one falls back to the default avatar.
func UserImageURL(origin string, user domain.User, size int, stored string) string {
	switch {
	case stored == "":
		return origin + DefaultAvatarPath(user.WorkspaceID, user.ID, size)
	case strings.HasPrefix(stored, "/"):
		return origin + stored
	}
	return stored
}

// DefaultAvatarSizes are the sizes Slack's profile object names an image for.
var DefaultAvatarSizes = map[int]bool{24: true, 32: true, 48: true, 72: true, 192: true, 512: true, 1024: true}

// DefaultAvatarPath is where the generated avatar of a member without a photo
// is served.
func DefaultAvatarPath(workspace domain.WorkspaceID, user domain.UserID, size int) string {
	return "/avatars/" + url.PathEscape(string(workspace)) + "/" + url.PathEscape(string(user)) + "/" + strconv.Itoa(size) + ".png"
}

// AvatarHash identifies the image a profile shows, so a client can tell when to
// refetch it. An uploaded photo is identified by its token; the default avatar
// by the member, with Slack's g prefix for a generated image.
func AvatarHash(user domain.User) string {
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

// UserColor is a member's stable color: the same member is always drawn the same.
func UserColor(user domain.UserID) string {
	sum := sha256.Sum256([]byte(user))
	return userColors[int(sum[0])%len(userColors)]
}

// FileURLs are the locations of one file's bytes and pages on an origin.
type FileURLs struct {
	Origin string
}

// Private is url_private and url_private_download: the bytes, behind the
// reader's bearer token.
func (u FileURLs) Private(id string) string {
	return u.Origin + "/api/files/" + url.PathEscape(id)
}

// Permalink is the file's page for a signed-in member of the web client.
func (u FileURLs) Permalink(id string) string {
	return u.Origin + "/app/files/" + url.PathEscape(id)
}

// Public is permalink_public, the unauthenticated link a public share mints.
func (u FileURLs) Public(token string) string {
	return u.Origin + "/files/public/" + url.PathEscape(token)
}

// ExternalUpload is the upload_url files.getUploadURLExternal hands out.
func (u FileURLs) ExternalUpload(id string) string {
	return u.Origin + "/internal/files/external/" + url.PathEscape(id)
}

// File renders Slack's file object.
func File(origin string, file domain.File) map[string]any {
	// A deleted file survives only as a tombstone: Slack keeps its id in the
	// messages that shared it and says nothing else about it, so neither its
	// name nor a download URL outlives the deletion.
	if file.Deleted {
		return map[string]any{"id": file.ID, "mode": file.Mode()}
	}
	fileType, prettyType := file.FileTypes()
	urls := FileURLs{Origin: origin}
	result := map[string]any{
		"id": file.ID, "name": file.Name, "title": file.Title, "mimetype": file.MIMEType,
		"size": file.Size, "created": file.CreatedAt.Unix(), "timestamp": file.CreatedAt.Unix(),
		"user": file.Uploader, "is_public": file.PublicToken != "", "team_id": file.WorkspaceID,
		"filetype": fileType, "pretty_type": prettyType, "mode": file.Mode(),
		"is_external": false, "external_type": "", "public_url_shared": file.PublicToken != "",
		"editable": file.IsSnippet(), "display_as_bot": false,
		// SDKs and apps fetch these verbatim.
		"url_private":          urls.Private(string(file.ID)),
		"url_private_download": urls.Private(string(file.ID)),
		"permalink":            urls.Permalink(string(file.ID)),
	}
	if file.PublicToken != "" {
		result["permalink_public"] = urls.Public(file.PublicToken)
	}
	if len(file.SharedChannels) > 0 {
		result["channels"] = file.SharedChannels
	}
	if shares := fileShares(file); shares != nil {
		result["shares"] = shares
	}
	return result
}

func unixSeconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

// fileShares is Slack's shares map: {public|private: {channel:
// [share, ...]}}, one entry per message that carries the file.
func fileShares(file domain.File) map[string]any {
	if len(file.Shares) == 0 {
		return nil
	}
	shares := map[string]any{}
	for _, share := range file.Shares {
		visibility := "public"
		if share.Private {
			visibility = "private"
		}
		byChannel, _ := shares[visibility].(map[string]any)
		if byChannel == nil {
			byChannel = map[string]any{}
			shares[visibility] = byChannel
		}
		entry := map[string]any{"ts": share.Timestamp, "channel_name": share.ConversationName, "team_id": file.WorkspaceID, "share_user_id": share.SharedBy}
		if share.ThreadTimestamp != "" {
			entry["thread_ts"] = share.ThreadTimestamp
		} else if share.ReplyCount > 0 {
			// A sharing message that starts a thread names its own ts as the
			// thread's and summarizes it, as the message object does.
			users := share.ReplyUsers
			if users == nil {
				users = []domain.UserID{}
			}
			entry["thread_ts"] = share.Timestamp
			entry["reply_count"] = share.ReplyCount
			entry["reply_users"] = users
			entry["reply_users_count"] = len(share.ReplyUsers)
			if !share.LatestReply.IsZero() {
				entry["latest_reply"] = domain.NewMessageTimestamp(share.LatestReply)
			}
		}
		existing, _ := byChannel[string(share.Conversation)].([]map[string]any)
		byChannel[string(share.Conversation)] = append(existing, entry)
	}
	return shares
}
