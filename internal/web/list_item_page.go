package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// listItemCommentView is one remark on the item page: who said it, when, and —
// only for the reader's own comment — the form that deletes it.
type listItemCommentView struct {
	ID          string
	AuthorName  string
	Text        string
	DisplayTime string
	MachineTime string
	DeleteURL   string
}

// listItemFileView is one file attached to the item: how to download it and,
// only for a list editor, the form that detaches it.
type listItemFileView struct {
	Name        string
	Title       string
	Size        string
	DownloadURL string
	DetachURL   string
}

type listItemPageData struct {
	ListID       string
	ListName     string
	ItemID       string
	Title        string
	Cells        []listCellView
	AssigneeName string
	DueDate      string
	Overdue      bool
	Archived     bool
	Comments     []listItemCommentView
	Files        []listItemFileView
	CanWrite     bool
	FilePath     string
	CSRFToken    string
	ListURL      string
	CommentPath  string
	CommentLimit int
	Notice       string
}

func listItemPath(listID domain.ListID, itemID domain.ListItemID) string {
	return "/app/lists/" + url.PathEscape(string(listID)) + "/items/" + url.PathEscape(string(itemID))
}

// listItemPage is where an item is read on its own and commented on — a list has
// many items, so unlike a canvas its comments live on a per-item page rather than
// beside the whole document.
func (h Handler) listItemPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	list, err := h.Messages.List(r.Context(), principal.WorkspaceID, principal.UserID, listID)
	if err != nil {
		h.writeStoreError(w, err, "That list is not available.")
		return
	}
	item, err := h.Messages.GetListItem(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID)
	if err != nil {
		h.writeStoreError(w, err, "That item is not available.")
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	columns, schemaErr := domain.ParseListSchema(list.Schema)
	if schemaErr != nil {
		columns = nil
	}
	names := h.newUserNames(r.Context(), principal)
	// Attaching and detaching a file edits the row, so those controls are shown
	// only to a list editor; every reader still sees and downloads what is there.
	canWrite := false
	if access, accessErr := h.Messages.ListAccess(r.Context(), principal.WorkspaceID, principal.UserID, listID); accessErr == nil {
		canWrite = (access.Access == domain.AccessWrite || access.Access == domain.AccessOwner) && principal.HasScope(auth.ScopeListsWrite)
	}
	data := listItemPageData{
		ListID: string(listID), ListName: list.Name, ItemID: string(itemID),
		Title: listItemTitle(columns, item.Fields), Archived: item.Archived,
		CanWrite: canWrite, FilePath: listItemPath(listID, itemID) + "/files",
		CSRFToken: csrf, ListURL: "/app/lists/" + url.PathEscape(string(listID)),
		CommentPath: listItemPath(listID, itemID) + "/comments", CommentLimit: domain.ListItemCommentLimit,
		Notice: strings.TrimSpace(r.URL.Query().Get("notice")),
	}
	if files, filesErr := h.Messages.ListItemFiles(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID); filesErr == nil {
		for _, file := range files {
			title := strings.TrimSpace(file.Title)
			if title == "" {
				title = file.Name
			}
			view := listItemFileView{
				Name: file.Name, Title: title, Size: formatFileSize(file.Size),
				DownloadURL: "/app/files/" + url.PathEscape(string(file.ID)),
			}
			if canWrite {
				view.DetachURL = data.FilePath + "/" + url.PathEscape(string(file.ID)) + "/detach"
			}
			data.Files = append(data.Files, view)
		}
	} else {
		data.Notice = "Attachments are temporarily unavailable."
	}
	if domain.ListSchemaIsStructured(columns) {
		data.Cells = listCellViews(columns, item.Fields)
	}
	if item.AssigneeID != "" {
		data.AssigneeName = names.name(item.AssigneeID)
	}
	if !item.DueAt.IsZero() {
		data.DueDate = item.DueAt.UTC().Format("2006-01-02")
		data.Overdue = item.Overdue(time.Now())
	}
	if page, err := h.Messages.ListItemComments(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID, domain.PageRequest{Limit: 100}); err == nil {
		for _, comment := range page.Comments {
			view := listItemCommentView{
				ID: string(comment.ID), AuthorName: names.name(comment.UserID), Text: comment.Text,
				DisplayTime: formatTime(comment.CreatedAt), MachineTime: comment.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			if comment.UserID == principal.UserID {
				view.DeleteURL = data.CommentPath + "/" + url.PathEscape(string(comment.ID)) + "/delete"
			}
			data.Comments = append(data.Comments, view)
		}
	} else {
		data.Notice = "Comments are temporarily unavailable."
	}
	h.writeHTML(w, listItemTemplate, data, http.StatusOK, "item rendering unavailable")
}

func (h Handler) commentOnListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the item and try again.")
	if !ok {
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	if _, err := h.Messages.CommentOnListItem(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID, fields["text"]); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The comment was not added", "Write something to say, and check you can still open this list.")
		return
	}
	h.redirectMutation(w, r, listItemPath(listID, itemID))
}

func (h Handler) deleteListItemComment(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the item and try again."); !ok {
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	if err := h.Messages.DeleteListItemComment(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListItemCommentID(strings.TrimSpace(r.PathValue("commentID")))); err != nil {
		// Someone else's comment answers exactly as a missing one does, so this
		// cannot be used to learn that a comment exists.
		h.writeMutationError(w, r, http.StatusNotFound, "The comment was not deleted", "It is no longer there, or it is not yours to delete.")
		return
	}
	h.redirectMutation(w, r, listItemPath(listID, itemID))
}

// attachFileToListItem uploads one file and records it against the item. The
// upload is guarded first by a write-access check so a reader cannot leave an
// orphaned blob behind; the service enforces the same authority authoritatively.
func (h Handler) attachFileToListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if !principal.HasScope(auth.ScopeFilesWrite) {
		h.writeAuthError(w, r, auth.ErrMissingScope)
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUploadBytes+maxWorkspaceUploadFields)
	if err := r.ParseMultipartForm(maxWorkspaceUploadFields); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That file could not be read", "Choose a file smaller than 100 MiB and try again.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !h.acceptCSRFResult(w, r, auth.ValidateCSRFValue(r, r.FormValue(auth.CSRFTokenFieldName))) {
		return
	}
	access, accessErr := h.Messages.ListAccess(r.Context(), principal.WorkspaceID, principal.UserID, listID)
	if accessErr != nil || (access.Access != domain.AccessWrite && access.Access != domain.AccessOwner) {
		h.writeMutationError(w, r, http.StatusForbidden, "That file was not attached", "You can view this list but not edit it.")
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) != 1 {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose one file", "Attach a single file to this item.")
		return
	}
	header := headers[0]
	name := strings.TrimSpace(header.Filename)
	if name == "" {
		name = "attachment"
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = name
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	source, openErr := header.Open()
	if openErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That file could not be opened", "Choose the file again and retry.")
		return
	}
	file, uploadErr := h.Messages.UploadFile(r.Context(), principal.WorkspaceID, principal.UserID, name, title, mimeType, "", header.Size, source)
	closeErr := source.Close()
	if uploadErr == nil {
		uploadErr = closeErr
	}
	if uploadErr != nil {
		status, reason := http.StatusBadRequest, "Choose a non-empty file with a valid name and try again."
		if errors.Is(uploadErr, service.ErrBlobUnavailable) {
			status, reason = http.StatusServiceUnavailable, "The file store is temporarily unavailable. Try again shortly."
		}
		h.writeMutationError(w, r, status, "That file was not attached", reason)
		return
	}
	if _, err := h.Messages.AttachFileToListItem(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID, file.ID); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That file was not attached", "Check you can still edit this list and try again.")
		return
	}
	h.redirectMutation(w, r, listItemPath(listID, itemID))
}

// detachFileFromListItem removes a file's attachment. The file itself is left
// alone: it stays available to its uploader and anywhere else it is shared.
func (h Handler) detachFileFromListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the item and try again."); !ok {
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	fileID := domain.FileID(strings.TrimSpace(r.PathValue("fileID")))
	if err := h.Messages.DetachFileFromListItem(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID, fileID); err != nil {
		h.writeMutationError(w, r, http.StatusNotFound, "The attachment was not removed", "It is no longer there, or it is not yours to change.")
		return
	}
	h.redirectMutation(w, r, listItemPath(listID, itemID))
}

const listItemMarkup = `{{define "title"}}{{.Title}} · List item · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(760px,calc(100% - 28px));margin:26px auto 56px}.heading h2{margin:0 0 4px}.muted{color:var(--muted)}.item-facts{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:6px 16px;margin:14px 0}.item-facts dt{font-weight:700}.item-facts dd{margin:0}.item-due.overdue{color:var(--danger);font-weight:700}.archived-flag{color:var(--muted)}
.comments{list-style:none;margin:12px 0 0;padding:0;display:grid;gap:10px}.comment{border:1px solid var(--line);border-radius:8px;padding:10px 12px;background:var(--panel)}.comment-head{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}.comment-author{font-weight:700}.comment-time{color:var(--muted);font-size:12px}.comment-text{margin:6px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}.comment form{margin:6px 0 0}.comment button{background:transparent;border:1px solid var(--field-line);color:var(--text);border-radius:6px;padding:5px 9px;font-weight:700}
.new-comment{display:grid;gap:8px;margin-top:14px}.new-comment textarea{width:100%;padding:9px;border:1px solid var(--field-line);border-radius:7px;background:var(--field);color:var(--text);font:inherit}.new-comment button{justify-self:start;border:0;border-radius:7px;padding:9px 13px;background:var(--action);color:var(--on-strong);font-weight:800}
.files{list-style:none;margin:12px 0 0;padding:0;display:grid;gap:8px}.file{display:flex;align-items:baseline;gap:10px;flex-wrap:wrap;border:1px solid var(--line);border-radius:8px;padding:9px 12px;background:var(--panel)}.file-link{font-weight:700;text-decoration:none;color:var(--action)}.file-size{color:var(--muted);font-size:12px}.file form{margin:0 0 0 auto}.file button{background:transparent;border:1px solid var(--field-line);color:var(--text);border-radius:6px;padding:4px 9px;font-weight:700}
.new-file{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-top:12px}.new-file input[type=file]{font:inherit;color:var(--text)}.new-file button{border:0;border-radius:7px;padding:9px 13px;background:var(--action);color:var(--on-strong);font-weight:800}
.empty{color:var(--muted)}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="{{.ListURL}}">← {{.ListName}}</a><h1>Item</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}<div class="heading"><h2>{{.Title}}{{if .Archived}} <span class="archived-flag">(completed)</span>{{end}}</h2></div>
{{if .Cells}}<dl class="item-facts">{{range .Cells}}<dt>{{.ColumnName}}</dt><dd>{{if .Value}}{{.Value}}{{else}}—{{end}}</dd>{{end}}{{if .AssigneeName}}<dt>Assignee</dt><dd>{{.AssigneeName}}</dd>{{end}}{{if .DueDate}}<dt>Due</dt><dd class="item-due{{if .Overdue}} overdue{{end}}">{{.DueDate}}{{if .Overdue}} · overdue{{end}}</dd>{{end}}</dl>{{else}}{{if or .AssigneeName .DueDate}}<dl class="item-facts">{{if .AssigneeName}}<dt>Assignee</dt><dd>{{.AssigneeName}}</dd>{{end}}{{if .DueDate}}<dt>Due</dt><dd class="item-due{{if .Overdue}} overdue{{end}}">{{.DueDate}}{{if .Overdue}} · overdue{{end}}</dd>{{end}}</dl>{{end}}{{end}}
<section aria-labelledby="files-heading"><h3 id="files-heading">Files</h3>
{{if .Files}}<ul class="files">{{range .Files}}<li class="file"><a class="file-link" href="{{.DownloadURL}}">{{.Title}}</a><span class="file-size">{{.Size}}</span>{{if .DetachURL}}<form method="post" action="{{.DetachURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Remove</button></form>{{end}}</li>{{end}}</ul>{{else}}<p class="empty">No files attached.</p>{{end}}
{{if .CanWrite}}<form class="new-file" method="post" action="{{.FilePath}}" enctype="multipart/form-data"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="visually-hidden" for="new-file">Attach a file</label><input id="new-file" type="file" name="file" required><button type="submit">Attach file</button></form>{{end}}
</section>
<section aria-labelledby="comments-heading"><h3 id="comments-heading">Comments</h3>
{{if .Comments}}<ul class="comments">{{range .Comments}}<li class="comment"><span class="comment-head"><span class="comment-author">{{.AuthorName}}</span><time class="comment-time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time></span><p class="comment-text">{{.Text}}</p>{{if .DeleteURL}}<form method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Delete</button></form>{{end}}</li>{{end}}</ul>{{else}}<p class="empty">No comments yet.</p>{{end}}
<form class="new-comment" method="post" action="{{.CommentPath}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="visually-hidden" for="new-comment">Add a comment</label><textarea id="new-comment" name="text" rows="3" maxlength="{{.CommentLimit}}" placeholder="Add a comment" required></textarea><button type="submit">Add comment</button></form>
</section></main>{{end}}`

var listItemTemplate = mustPage(listItemMarkup)
