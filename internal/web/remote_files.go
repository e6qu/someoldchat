package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Remote files are files an app registered with this workspace but did not
// upload here: the bytes live wherever the app keeps them, and SameOldChat
// holds the metadata, the external link and the preview. FILE-07 is explicit
// that the product must not pretend to host them, so this surface never
// offers a download — it links out, and says where the file actually lives.
//
// The whole family (files.remote.add/info/list/remove/share/update) has been
// implemented and reachable over the Slack API since it was added, with no
// way to see any of it from the browser: a remote file did not appear in the
// timeline, in search, or anywhere else a person could look.

type remoteFileView struct {
	ID          string
	Title       string
	FileType    string
	ExternalID  string
	ExternalURL string
	Preview     string
	CreatedAt   string
	Channels    []conversationView
	ShareURL    string
	RemoveURL   string
}

type remoteFilesData struct {
	CSRFToken   string
	Notice      string
	Channel     string
	Files       []remoteFileView
	Channels    []conversationView
	MoreURL     string
	CanManage   bool
	WorkspaceID string
}

const remoteFilesMarkup = `{{define "title"}}Remote files · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0;font-size:16px}
.layout{width:min(940px,calc(100% - 32px));margin:28px auto 56px}
.heading h2,.heading p{margin:0}.heading p{color:var(--muted);font-size:13px;margin-top:4px}
.remote-list{display:grid;gap:12px;margin-top:18px}
.remote-file{display:grid;gap:8px;padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}
.remote-file h3{margin:0;font-size:15px}
.remote-meta{color:var(--muted);font-size:12px}
.remote-host{display:inline-block;padding:2px 8px;border:1px solid var(--field-line);border-radius:999px;color:var(--muted);font-size:11px}
.remote-actions{display:flex;flex-wrap:wrap;gap:10px;align-items:end}
.remote-actions form{display:flex;gap:8px;align-items:end}
.remote-actions label{display:grid;gap:4px;font-size:12px}
.empty{color:var(--muted)}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Remote files</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Remote files</h2><p>Files an app registered with this workspace. The contents stay with the app that hosts them; SameOldChat keeps the link and the preview.</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<div class="remote-list">
{{range .Files}}
<article class="remote-file" aria-labelledby="remote-{{.ID}}">
  <h3 id="remote-{{.ID}}">{{.Title}}</h3>
  <p class="remote-meta">{{.FileType}} · external id {{.ExternalID}} · added <time datetime="{{.CreatedAt}}">{{.CreatedAt}}</time></p>
  {{if .Preview}}<p class="remote-meta">{{.Preview}}</p>{{end}}
  <p><a href="{{.ExternalURL}}" rel="noreferrer noopener">Open where it is hosted</a> <span class="remote-host">Hosted elsewhere</span></p>
  {{if .Channels}}<p class="remote-meta">Shared in {{range .Channels}}#{{.Name}} {{end}}</p>{{end}}
  {{if $.CanManage}}
  <div class="remote-actions">
    <form method="post" action="{{.ShareURL}}">
      <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
      <label>Share into<select name="destination">{{range $.Channels}}<option value="{{.ID}}">#{{.Name}}</option>{{end}}</select></label>
      <button type="submit">Share</button>
    </form>
    <form method="post" action="{{.RemoveURL}}">
      <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
      <button type="submit">Remove from this workspace</button>
    </form>
  </div>
  {{end}}
</article>
{{else}}
<p class="empty">No app has registered a remote file with this workspace yet.</p>
{{end}}
</div>
{{if .MoreURL}}<p><a href="{{.MoreURL}}">Show more remote files</a></p>{{end}}
</main>{{end}}`

var remoteFilesTemplate = mustPage(remoteFilesMarkup)

func (h Handler) remoteFiles(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("before")))
	page, err := h.Messages.RemoteFiles(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 50, Cursor: cursor})
	if err != nil {
		h.writeStoreError(w, err, "Remote files are temporarily unavailable.")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	data := remoteFilesData{
		CSRFToken:   auth.CSRFToken(sessionCookie.Value),
		Notice:      strings.TrimSpace(r.URL.Query().Get("notice")),
		Channel:     channel,
		CanManage:   principal.HasScope(auth.ScopeRemoteFilesWrite),
		WorkspaceID: string(principal.WorkspaceID),
	}
	names := h.newUserNames(r.Context(), principal)
	for _, file := range page.Files {
		view := remoteFileView{
			ID: string(file.ID), Title: file.Title, FileType: file.FileType,
			ExternalID: file.ExternalID, ExternalURL: file.ExternalURL,
			Preview:   file.IndexableContents,
			CreatedAt: file.CreatedAt.UTC().Format(time.RFC3339Nano),
			ShareURL:  "/app/remote-files/share?file=" + url.QueryEscape(file.ExternalID) + "&channel=" + url.QueryEscape(channel),
			RemoveURL: "/app/remote-files/remove?file=" + url.QueryEscape(file.ExternalID) + "&channel=" + url.QueryEscape(channel),
		}
		for _, shared := range file.SharedChannels {
			view.Channels = append(view.Channels, conversationView{ID: string(shared), Name: names.channelName(shared)})
		}
		data.Files = append(data.Files, view)
	}
	if data.CanManage {
		if options, optionsErr := h.visibleChannelOptions(r.Context(), principal); optionsErr == nil {
			data.Channels = options
		}
	}
	if page.NextCursor != "" {
		data.MoreURL = "/app/remote-files?channel=" + url.QueryEscape(channel) + "&before=" + url.QueryEscape(string(page.NextCursor))
	}
	h.writeHTML(w, remoteFilesTemplate, data, http.StatusOK, "remote file rendering unavailable")
}

func (h Handler) shareRemoteFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesShare)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the page and try again.")
	if !ok {
		return
	}
	external := strings.TrimSpace(r.URL.Query().Get("file"))
	destination := domain.ConversationID(strings.TrimSpace(fields["destination"]))
	if external == "" || destination == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The file was not shared", "Choose a conversation and try again.")
		return
	}
	if _, err := h.Messages.ShareRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID,
		domain.RemoteFileLookup{ExternalID: external}, []domain.ConversationID{destination}); err != nil {
		h.writeMessageMutationError(w, r, err, "shared")
		return
	}
	h.redirectMutation(w, r, "/app/remote-files?channel="+url.QueryEscape(strings.TrimSpace(r.URL.Query().Get("channel")))+"&notice="+url.QueryEscape("Remote file shared"))
}

func (h Handler) removeRemoteFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the page and try again."); !ok {
		return
	}
	external := strings.TrimSpace(r.URL.Query().Get("file"))
	if external == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The file was not removed", "Reload the page and try again.")
		return
	}
	if err := h.Messages.RemoveRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, domain.RemoteFileLookup{ExternalID: external}); err != nil {
		h.writeMessageMutationError(w, r, err, "removed")
		return
	}
	h.redirectMutation(w, r, "/app/remote-files?channel="+url.QueryEscape(strings.TrimSpace(r.URL.Query().Get("channel")))+"&notice="+url.QueryEscape("Remote file removed"))
}
