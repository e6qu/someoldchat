package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The workspace's custom emoji, as Slack's "Customize workspace › Emoji" page
// keeps them: the emoji picker's "Add emoji" leads here. The Web API has had
// admin.emoji.add/remove since custom emoji were stored, but the first-party
// client had no way to see or manage them, so a custom emoji could only exist
// if an administrator scripted it.
//
// Adding takes an image URL, which is the contract admin.emoji.add has and the
// service validates (HTTP or HTTPS, a host, a bounded length). Uploading the
// image itself would need a public, durable URL for a blob; that is recorded in
// specs/product-gap-audit.md rather than faked here.

type customEmojiPage struct {
	Channel   string
	CSRFToken string
	CanManage bool
	Emoji     []customEmojiRow
	Notice    string
}

type customEmojiRow struct {
	Name     string
	ImageURL string
	AliasFor string
}

func (h Handler) customEmojiPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
	if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	emoji, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Custom emoji are temporarily unavailable.")
		return
	}
	images := customEmojiImages(emoji)
	data := customEmojiPage{
		Channel:   strings.TrimSpace(r.URL.Query().Get("channel")),
		CSRFToken: auth.CSRFToken(sessionCookie.Value),
		CanManage: h.canShowWorkspaceAdmin(r.Context(), principal),
		Notice:    boundedNotice(r.URL.Query().Get("notice")),
	}
	for _, value := range emoji {
		data.Emoji = append(data.Emoji, customEmojiRow{Name: value.Name, ImageURL: images[strings.ToLower(value.Name)], AliasFor: value.AliasFor})
	}
	sort.Slice(data.Emoji, func(left, right int) bool { return data.Emoji[left].Name < data.Emoji[right].Name })
	h.writeHTML(w, customEmojiTemplate, data, http.StatusOK, "Custom emoji rendering unavailable")
}

func boundedNotice(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}

func (h Handler) addCustomEmoji(w http.ResponseWriter, r *http.Request) {
	h.mutateCustomEmoji(w, r, true)
}

func (h Handler) removeCustomEmoji(w http.ResponseWriter, r *http.Request) {
	h.mutateCustomEmoji(w, r, false)
}

func (h Handler) mutateCustomEmoji(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The emoji could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	name := strings.Trim(strings.TrimSpace(fields["name"]), ":")
	notice := "Removed :" + name + ":"
	if add {
		err = h.Messages.AdminAddEmoji(r.Context(), principal.WorkspaceID, principal.UserID, name, strings.TrimSpace(fields["url"]))
		notice = "Added :" + name + ":"
	} else {
		err = h.Messages.AdminRemoveEmoji(r.Context(), principal.WorkspaceID, principal.UserID, name)
	}
	if err != nil {
		status, heading, reason := http.StatusServiceUnavailable, "The emoji was not saved", "The workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrNotWorkspaceAdmin):
			status, reason = http.StatusForbidden, "Only workspace admins and owners can manage custom emoji."
		case errors.Is(err, service.ErrInvalidEmoji):
			status, reason = http.StatusBadRequest, "Use a name of lowercase letters, numbers, hyphens and underscores, and an http or https image URL."
		case errors.Is(err, service.ErrEmojiAlreadyExists):
			status, reason = http.StatusConflict, "That name is already a standard or custom emoji. Choose another name."
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That emoji no longer exists."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	query := url.Values{"notice": {notice}}
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		query.Set("channel", channel)
	}
	h.redirectMutation(w, r, "/app/customize/emoji?"+query.Encode())
}

var customEmojiTemplate = mustPage(customEmojiMarkup)

const customEmojiMarkup = `{{define "title"}}Custom emoji · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(760px,calc(100% - 32px));margin:28px auto 48px;display:grid;gap:20px}
.heading h2,.heading p{margin:0}.heading p{color:var(--muted);margin-top:4px}
.emoji-form{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:10px;align-items:end;padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}
.emoji-form h3{grid-column:1/-1;margin:0;font-size:16px}
.emoji-form label{display:grid;gap:4px;font-weight:700;font-size:14px}
.emoji-form input{border:1px solid var(--field-line);border-radius:6px;padding:7px 10px;background:var(--panel-strong);color:var(--text);font:inherit}
.emoji-form button,.emoji-table button{justify-self:start;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:7px 12px;font-weight:700}
.emoji-form button{border-color:var(--ok);background:var(--ok);color:#fff}
.emoji-table{width:100%;border-collapse:collapse}.emoji-table th,.emoji-table td{padding:8px 10px;border-bottom:1px solid var(--line);text-align:left}
.emoji-table img{width:28px;height:28px;object-fit:contain}
.empty{padding:24px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="/app{{if .Channel}}?channel={{.Channel}}{{end}}">← Back to chat</a><h1>Custom emoji</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Custom emoji</h2><p>Emoji added here can be used in messages and reactions by everyone in the workspace.</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .CanManage}}<form class="emoji-form" method="post" action="/app/customize/emoji/add{{if .Channel}}?channel={{.Channel}}{{end}}">
  <h3>Add custom emoji</h3>
  <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
  <label>Name<input name="name" maxlength="100" pattern=":?[a-z0-9_+\-]+:?" placeholder="partyparrot" required></label>
  <label>Image URL<input name="url" type="url" maxlength="2048" placeholder="https://…" required></label>
  <button type="submit">Save</button>
</form>{{else}}<p class="notice" role="note">Only workspace admins and owners can add or remove custom emoji.</p>{{end}}
{{if .Emoji}}<table class="emoji-table"><caption class="visually-hidden">Custom emoji</caption><thead><tr><th scope="col">Emoji</th><th scope="col">Name</th>{{if .CanManage}}<th scope="col"><span class="visually-hidden">Actions</span></th>{{end}}</tr></thead><tbody>{{range .Emoji}}<tr><td>{{if .ImageURL}}<img src="{{.ImageURL}}" alt=":{{.Name}}:" loading="lazy">{{end}}</td><td>:{{.Name}}:{{if .AliasFor}} <small>alias of :{{.AliasFor}}:</small>{{end}}</td>{{if $.CanManage}}<td><form method="post" action="/app/customize/emoji/remove{{if $.Channel}}?channel={{$.Channel}}{{end}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="name" value="{{.Name}}"><button type="submit" aria-label="Remove :{{.Name}}:">Remove</button></form></td>{{end}}</tr>{{end}}</tbody></table>
{{else}}<p class="empty">This workspace has no custom emoji yet.</p>{{end}}
</main>{{end}}`
