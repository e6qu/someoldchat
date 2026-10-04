package web

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The Files browser is Slack's Files view (FILE-04): every hosted file,
// canvas and list the member can reach, with Slack's three ownership tabs
// (All files, Created by you, Shared with you), a type filter, a sort, and a
// search box. A file opens a file view with its preview, details, where it
// was shared and its actions; a canvas or a list opens its own page. It reads
// the same visibility-checked listings files.list, canvases and lists answer,
// so it can never show something the member could not open.

// filesBrowserScan bounds how many files one page load reads. A workspace
// with more visible files than this is told the list is truncated and to
// narrow it, rather than the page reading without end.
const filesBrowserScan = 2000

type filesBrowserData struct {
	// Shell is the workspace frame the page renders inside.
	Shell     shellView
	Channel   string
	Query     string
	Owner     string
	Type      string
	Sort      string
	From      string
	FromName  string
	Tabs      []filesTabView
	Types     []filesOptionView
	Sorts     []filesOptionView
	Files     []fileRowView
	Truncated bool
	Summary   string
	Notice    string
}

type filesTabView struct {
	Label   string
	URL     string
	Current bool
}

type filesOptionView struct {
	Value    string
	Label    string
	Selected bool
}

type fileRowView struct {
	ID           string
	Title        string
	Name         string
	Kind         string
	KindLabel    string
	Icon         string
	ThumbnailURL string
	Uploader     string
	UploaderID   string
	Size         string
	MachineTime  string
	DisplayTime  string
	ViewURL      string
	DownloadURL  string
}

// fileKind is the Files view's type filter bucket for a file.
func fileKind(file domain.File) (string, string, string) {
	mimeType := strings.ToLower(strings.TrimSpace(file.MIMEType))
	name := strings.ToLower(file.Name)
	switch {
	case file.FileType != "":
		return "snippets", "Snippet", "</>"
	case strings.HasPrefix(mimeType, "image/"):
		return "images", "Image", "🖼"
	case mimeType == "application/pdf" || strings.HasSuffix(name, ".pdf"):
		return "pdfs", "PDF", "📕"
	case strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "document") || strings.Contains(mimeType, "msword") || strings.Contains(mimeType, "rtf") || strings.Contains(mimeType, "spreadsheet") || strings.Contains(mimeType, "presentation") || strings.HasSuffix(name, ".md"):
		return "documents", "Document", "📄"
	case strings.HasPrefix(mimeType, "video/") || strings.HasPrefix(mimeType, "audio/"):
		return "media", "Audio or video", "🎞"
	}
	return "other", "File", "📎"
}

func (h Handler) filesBrowser(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	switch owner {
	case "", "mine", "shared":
	default:
		owner = ""
	}
	kind := strings.TrimSpace(r.URL.Query().Get("type"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("sort"))
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	data := filesBrowserData{Channel: string(h.requestChannel(r)), Query: query, Owner: owner, Type: kind, Sort: sortOrder, From: from}
	switch r.URL.Query().Get("notice") {
	case "deleted":
		data.Notice = "File deleted."
	}
	names := h.newUserNames(r.Context(), principal)
	if from != "" {
		data.FromName = names.name(domain.UserID(from))
	}
	folded := domain.FoldSearchText(query)
	// Slack's Files view holds canvases and lists beside uploaded files, so
	// one listing gathers all three and sorts them together. A type filter
	// for one kind reads only that kind.
	type entry struct {
		row     fileRowView
		created time.Time
	}
	var entries []entry
	keep := func(ownerID domain.UserID, text string) bool {
		mine := ownerID == principal.UserID
		if (owner == "mine" && !mine) || (owner == "shared" && mine) || (from != "" && string(ownerID) != from) {
			return false
		}
		return folded == "" || strings.Contains(domain.FoldSearchText(text), folded)
	}
	if kind != "canvases" && kind != "lists" {
		request := domain.PageRequest{Limit: 200}
		for scanned := 0; ; {
			page, listErr := h.Messages.Files(r.Context(), principal.WorkspaceID, principal.UserID, request)
			if listErr != nil {
				h.writeStoreError(w, listErr, "Files are temporarily unavailable.")
				return
			}
			for _, file := range page.Files {
				scanned++
				if file.Deleted || !keep(file.Uploader, file.Name+" "+file.Title+" "+file.Description) {
					continue
				}
				if fileKindValue, _, _ := fileKind(file); kind != "" && kind != fileKindValue {
					continue
				}
				entries = append(entries, entry{row: h.fileRow(file, names), created: file.CreatedAt})
			}
			if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
				break
			}
			if scanned >= filesBrowserScan {
				data.Truncated = true
				break
			}
			request.Cursor = page.NextCursor
		}
	}
	if (kind == "" || kind == "canvases") && principal.HasScope(auth.ScopeCanvasesRead) {
		request := domain.PageRequest{Limit: 200}
		for scanned := 0; ; {
			page, listErr := h.Messages.Canvases(r.Context(), principal.WorkspaceID, principal.UserID, request)
			if listErr != nil {
				h.writeStoreError(w, listErr, "Canvases are temporarily unavailable.")
				return
			}
			for _, canvas := range page.Canvases {
				scanned++
				if keep(canvas.OwnerID, canvas.Title) {
					entries = append(entries, entry{row: documentRow("canvases", "Canvas", "📝", string(canvas.ID), canvas.Title, "/app/canvases/"+url.PathEscape(string(canvas.ID)), canvas.OwnerID, canvas.CreatedAt, names), created: canvas.CreatedAt})
				}
			}
			if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
				break
			}
			if scanned >= filesBrowserScan {
				data.Truncated = true
				break
			}
			request.Cursor = page.NextCursor
		}
	}
	if (kind == "" || kind == "lists") && principal.HasScope(auth.ScopeListsRead) {
		request := domain.PageRequest{Limit: 200}
		for scanned := 0; ; {
			page, listErr := h.Messages.Lists(r.Context(), principal.WorkspaceID, principal.UserID, request)
			if listErr != nil {
				h.writeStoreError(w, listErr, "Lists are temporarily unavailable.")
				return
			}
			for _, list := range page.Lists {
				scanned++
				if keep(list.OwnerID, list.Name) {
					entries = append(entries, entry{row: documentRow("lists", "List", "☑", string(list.ID), list.Name, "/app/lists/"+url.PathEscape(string(list.ID)), list.OwnerID, list.CreatedAt, names), created: list.CreatedAt})
				}
			}
			if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
				break
			}
			if scanned >= filesBrowserScan {
				data.Truncated = true
				break
			}
			request.Cursor = page.NextCursor
		}
	}
	sort.SliceStable(entries, func(left, right int) bool {
		switch sortOrder {
		case "oldest":
			return entries[left].created.Before(entries[right].created)
		case "name":
			return strings.ToLower(entries[left].row.Title) < strings.ToLower(entries[right].row.Title)
		}
		return entries[left].created.After(entries[right].created)
	})
	for _, value := range entries {
		data.Files = append(data.Files, value.row)
	}
	switch len(data.Files) {
	case 0:
		data.Summary = "No files."
	case 1:
		data.Summary = "1 file."
	default:
		data.Summary = strconv.Itoa(len(data.Files)) + " files."
	}
	base := url.Values{}
	if query != "" {
		base.Set("q", query)
	}
	if kind != "" {
		base.Set("type", kind)
	}
	if sortOrder != "" {
		base.Set("sort", sortOrder)
	}
	for _, tab := range []struct{ value, label string }{{"", "All files"}, {"mine", "Created by you"}, {"shared", "Shared with you"}} {
		values := cloneURLValues(base)
		if tab.value != "" {
			values.Set("owner", tab.value)
		}
		address := "/app/files"
		if encoded := values.Encode(); encoded != "" {
			address += "?" + encoded
		}
		data.Tabs = append(data.Tabs, filesTabView{Label: tab.label, URL: address, Current: owner == tab.value && from == ""})
	}
	for _, option := range []struct{ value, label string }{{"", "All types"}, {"canvases", "Canvases"}, {"lists", "Lists"}, {"images", "Images"}, {"pdfs", "PDFs"}, {"documents", "Documents"}, {"snippets", "Snippets"}, {"media", "Audio and video"}, {"other", "Other"}} {
		data.Types = append(data.Types, filesOptionView{Value: option.value, Label: option.label, Selected: kind == option.value})
	}
	for _, option := range []struct{ value, label string }{{"", "Newest first"}, {"oldest", "Oldest first"}, {"name", "Name A–Z"}} {
		data.Sorts = append(data.Sorts, filesOptionView{Value: option.value, Label: option.label, Selected: sortOrder == option.value})
	}
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationMore})
	h.writeHTML(w, filesBrowserTemplate, data, http.StatusOK, "Files rendering unavailable")
}

// documentRow is a canvas's or a list's row in the Files view: it opens the
// document's own page and, having no bytes of its own, offers no download
// and shows no size.
func documentRow(kind, label, icon, id, title, viewURL string, ownerID domain.UserID, created time.Time, names *userNames) fileRowView {
	if strings.TrimSpace(title) == "" {
		title = "Untitled " + strings.ToLower(label)
	}
	return fileRowView{
		ID: id, Title: title, Kind: kind, KindLabel: label, Icon: icon,
		Uploader: names.name(ownerID), UploaderID: string(ownerID),
		MachineTime: created.UTC().Format(time.RFC3339Nano), DisplayTime: formatTime(created),
		ViewURL: viewURL,
	}
}

func fileTitle(file domain.File) string {
	if title := strings.TrimSpace(file.Title); title != "" {
		return title
	}
	return file.Name
}

func (h Handler) fileRow(file domain.File, names *userNames) fileRowView {
	kind, label, icon := fileKind(file)
	row := fileRowView{
		ID: string(file.ID), Title: fileTitle(file), Name: file.Name, Kind: kind, KindLabel: label, Icon: icon,
		Uploader: names.name(file.Uploader), UploaderID: string(file.Uploader), Size: formatFileSize(file.Size),
		MachineTime: file.CreatedAt.UTC().Format(time.RFC3339Nano), DisplayTime: formatTime(file.CreatedAt),
		ViewURL:     "/app/files/" + url.PathEscape(string(file.ID)) + "/view",
		DownloadURL: "/app/files/" + url.PathEscape(string(file.ID)),
	}
	if kind == "images" {
		row.ThumbnailURL = "/app/files/" + url.PathEscape(string(file.ID)) + "/thumbnail"
	}
	return row
}

type fileViewData struct {
	// Shell is the workspace frame the page renders inside.
	Shell       shellView
	Channel     string
	CSRFToken   string
	File        fileRowView
	Description string
	Preview     string
	PreviewCut  bool
	Shares      []fileShareView
	CanDelete   bool
	Notice      string
}

type fileShareView struct {
	Label   string
	URL     string
	Private bool
	By      string
	Time    string
	Machine string
}

// filePreviewLimit is how much of a text file the view shows inline.
const filePreviewLimit = 64 << 10

// fileView is Slack's file view: the preview (an image, or the opening of a
// text file), the file's name, type, size, uploader and date, every message
// that shared it, and Download, Copy link and — for its uploader — Delete.
func (h Handler) fileView(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(r.PathValue("fileID")))
	file, err := h.Messages.FileInfo(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.writePageError(w, http.StatusNotFound, "That file is not available", "It may have been deleted, or it was never shared with you.")
			return
		}
		h.writeStoreError(w, err, "The file is temporarily unavailable.")
		return
	}
	names := h.newUserNames(r.Context(), principal)
	data := fileViewData{
		Channel: string(h.requestChannel(r)), File: h.fileRow(file, names), Description: file.Description,
		CanDelete: file.Uploader == principal.UserID && principal.HasScope(auth.ScopeFilesWrite),
	}
	if sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName); cookieErr == nil {
		data.CSRFToken = auth.CSRFToken(sessionCookie.Value)
	}
	if data.File.Kind == "snippets" || (data.File.Kind == "documents" && strings.HasPrefix(strings.ToLower(file.MIMEType), "text/")) {
		if _, source, openErr := h.Messages.OpenFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID); openErr == nil {
			var buffer bytes.Buffer
			_, _ = io.CopyN(&buffer, source, filePreviewLimit+1)
			_ = source.Close()
			content := buffer.Bytes()
			if len(content) > filePreviewLimit {
				content = content[:filePreviewLimit]
				data.PreviewCut = true
			}
			if utf8.Valid(content) {
				data.Preview = string(content)
			}
		}
	}
	for _, share := range file.Shares {
		label := share.ConversationName
		if label == "" {
			label = string(share.Conversation)
		}
		view := fileShareView{
			Label: label, Private: share.Private, By: names.name(share.SharedBy),
			URL: appURL(string(share.Conversation), string(share.ThreadTimestamp), "", "", ""),
		}
		if at, parseErr := domain.ParseMessageTimestamp(share.Timestamp); parseErr == nil {
			view.Machine = at.UTC().Format(time.RFC3339Nano)
			view.Time = formatTime(at)
		}
		data.Shares = append(data.Shares, view)
	}
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationMore})
	h.writeHTML(w, fileViewTemplate, data, http.StatusOK, "File view rendering unavailable")
}

const filesBrowserStyle = `<style>
.file-back{margin:0 0 10px}.file-back a{color:var(--action);font-weight:700;text-decoration:none}.file-back a:hover{text-decoration:underline}
.files-filters{display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin:0 0 12px}
.files-filters .v-search{flex:1 1 260px;max-width:480px}
.file-thumb{display:grid;place-items:center;width:40px;height:40px;overflow:hidden;border-radius:6px;background:var(--hover);font-size:18px}
.file-thumb img{width:100%;height:100%;object-fit:cover}
.file-title{margin:0;font-weight:700;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.file-title a{display:inline-block;min-height:24px;line-height:24px;max-width:100%;overflow:hidden;text-overflow:ellipsis;vertical-align:top;color:var(--text);text-decoration:none}.file-title a:hover{text-decoration:underline}
.file-view{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:20px;align-items:start}
.file-preview{display:grid;place-items:center;min-height:260px;padding:12px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}
.file-preview img{max-width:100%;max-height:calc(70vh / var(--zoom, 1));border-radius:6px}
.file-preview pre{justify-self:stretch;margin:0;max-height:calc(70vh / var(--zoom, 1));overflow:auto;padding:12px;border-radius:6px;background:var(--bg);font-size:13px;white-space:pre-wrap;overflow-wrap:anywhere}
.file-preview .big-icon{font-size:64px}
.file-details{display:grid;gap:14px}
.file-details h3{margin:0 0 6px;font-size:14px}
.file-details dl{display:grid;grid-template-columns:auto minmax(0,1fr);gap:6px 12px;margin:0;font-size:14px}
.file-details dt{color:var(--muted)}
.file-details dd{margin:0;overflow-wrap:anywhere}
.file-actions{display:flex;flex-wrap:wrap;gap:8px}
.file-actions form{margin:0}
.shares{margin:0;padding:0;list-style:none;display:grid;gap:6px}
.shares a{display:block;padding:8px 10px;border:1px solid var(--line);border-radius:8px;color:var(--text);text-decoration:none}
.shares a:hover{background:var(--hover)}
@media(max-width:760px){.file-view{grid-template-columns:minmax(0,1fr)}}
</style>`

var filesBrowserTemplate = mustPage(`{{define "title"}}Files · SameOldChat{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + viewStyle + filesBrowserStyle + `{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + localTimeScript + liveFilterScript + rowLinkScript + profilePanelScript + `{{end}}
{{define "content"}}{{template "shell-open" .Shell}}{{template "files-view" .}}{{template "shell-close" .Shell}}{{end}}
{{define "files-view"}}<main class="v-page files-page">
<div class="v-head"><h1>Files</h1><a class="v-btn quiet" href="/app/remote-files?channel={{.Channel}}">External files</a></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<nav class="v-tabs" aria-label="Whose files">{{range .Tabs}}<a href="{{.URL}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
<form class="files-filters" role="search" method="get" action="/app/files" data-live-filter="#files-results">
{{if .Owner}}<input type="hidden" name="owner" value="{{.Owner}}">{{end}}{{if .From}}<input type="hidden" name="from" value="{{.From}}">{{end}}
<label class="v-search"><span aria-hidden="true">⌕</span><span class="visually-hidden">Search files</span><input type="search" name="q" value="{{.Query}}" placeholder="Search files" autocomplete="off"></label>
<label class="v-chip"><span class="visually-hidden">File type</span><select name="type" aria-label="File type">{{range .Types}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<label class="v-chip"><span class="visually-hidden">Sort</span><select name="sort" aria-label="Sort files">{{range .Sorts}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<noscript><button class="v-btn" type="submit">Filter</button></noscript>
</form>
{{if .From}}<p class="v-chips"><span class="v-chip on">From: {{.FromName}} <a href="/app/files" aria-label="Show files from everyone">×</a></span></p>{{end}}
<p class="visually-hidden" id="view-status" aria-live="polite"></p>
<div id="files-results" data-live-summary="{{.Summary}}">
{{if .Files}}<ul class="v-list" aria-label="Files">{{range .Files}}<li class="v-row" data-row-href="{{.ViewURL}}">
<span class="file-thumb" aria-hidden="true">{{if .ThumbnailURL}}<img src="{{.ThumbnailURL}}" alt="" loading="lazy">{{else}}{{.Icon}}{{end}}</span>
<div class="v-row-main"><p class="file-title"><a href="{{.ViewURL}}">{{.Title}}</a></p><div class="v-row-meta"><a href="/app/members?user={{.UploaderID}}" data-profile-user="{{.UploaderID}}">{{.Uploader}}</a><span aria-hidden="true">·</span><time datetime="{{.MachineTime}}">{{.DisplayTime}}</time><span aria-hidden="true">·</span><span>{{.KindLabel}}</span>{{if .Size}}<span aria-hidden="true">·</span><span>{{.Size}}</span>{{end}}</div></div>
<div class="v-row-side"><div class="v-hover-actions">{{if .DownloadURL}}<a class="v-icon" href="{{.DownloadURL}}" aria-label="Download {{.Title}}" title="Download"><span aria-hidden="true">⤓</span></a>{{end}}<button class="v-icon" type="button" data-copy-text="{{.ViewURL}}" data-copy-done="Link copied." aria-label="Copy link to {{.Title}}" title="Copy link"><span aria-hidden="true">🔗</span></button></div></div>
</li>{{end}}</ul>{{else}}<p class="v-empty">{{if or .Query .Type .Owner .From}}<strong>No files match</strong>Try a different search or filter.{{else}}<strong>No files yet</strong>Files, canvases and lists shared in conversations you can see appear here.{{end}}</p>{{end}}
{{if .Truncated}}<p class="pager">Only the most recent files were searched. Narrow the search to find older ones.</p>{{end}}
</div>
</main>{{end}}`)

var fileViewTemplate = mustPage(`{{define "title"}}{{.File.Title}} · SameOldChat{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + viewStyle + filesBrowserStyle + `{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + localTimeScript + rowLinkScript + profilePanelScript + `{{end}}
{{define "content"}}{{template "shell-open" .Shell}}
<main class="v-page file-page">
<p class="file-back"><a href="/app/files?channel={{.Channel}}">← All files</a></p>
<div class="v-head"><h1>{{.File.Title}}</h1></div>
<p class="visually-hidden" id="view-status" aria-live="polite"></p>
<div class="file-view">
<div class="file-preview">{{if .File.ThumbnailURL}}<img src="{{.File.ThumbnailURL}}" alt="{{if .Description}}{{.Description}}{{else}}{{.File.Title}}{{end}}">{{else if .Preview}}<pre>{{.Preview}}</pre>{{else}}<span class="big-icon" aria-hidden="true">{{.File.Icon}}</span><p class="pp-muted">No preview for this file type. Download it to open it.</p>{{end}}</div>
<aside class="file-details" aria-label="File details">
<div class="file-actions"><a class="v-btn primary" href="{{.File.DownloadURL}}">Download</a><button class="v-btn" type="button" data-copy-text="{{.File.ViewURL}}" data-copy-done="Link copied.">Copy link</button>{{if .CanDelete}}<form method="post" action="/app/files/delete?file={{.File.ID}}&amp;next=files"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="v-btn danger" type="submit" aria-label="Delete {{.File.Title}} for everyone">Delete file</button></form>{{end}}</div>
{{if .PreviewCut}}<p class="pp-muted">Only the beginning of the file is shown.</p>{{end}}
<section><h3>Details</h3><dl><dt>Name</dt><dd>{{.File.Name}}</dd><dt>Type</dt><dd>{{.File.KindLabel}}</dd><dt>Size</dt><dd>{{.File.Size}}</dd><dt>Shared by</dt><dd><a href="/app/members?user={{.File.UploaderID}}" data-profile-user="{{.File.UploaderID}}">{{.File.Uploader}}</a></dd><dt>Created</dt><dd><time datetime="{{.File.MachineTime}}">{{.File.DisplayTime}}</time></dd>{{if .Description}}<dt>Description</dt><dd>{{.Description}}</dd>{{end}}</dl></section>
<section><h3>Shared in</h3>{{if .Shares}}<ul class="shares">{{range .Shares}}<li><a href="{{.URL}}">{{if .Private}}<span aria-label="Private">🔒</span> {{else}}# {{end}}{{.Label}}<br><span class="pp-muted">by {{.By}}{{if .Time}} · <time datetime="{{.Machine}}">{{.Time}}</time>{{end}}</span></a></li>{{end}}</ul>{{else}}<p class="pp-muted">Not shared in any conversation you can see.</p>{{end}}</section>
</aside>
</div>
</main>{{template "shell-close" .Shell}}{{end}}`)
