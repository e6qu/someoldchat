package slack

import (
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
)

// A reactions.* or pins.* request may name a file or a file comment as its
// item instead of a message. The SDKs send file and file_comment (python
// slack_sdk to reactions.get and reactions.remove, the Java client to those
// and to reactions.add, pins.add and pins.remove), and they were read as a
// missing item, so each answered no_item_specified to a request that
// specified one.
//
// Reactions and pins are kept on messages here; a file can carry neither, and
// file comments were retired by Slack and nothing here creates one. So each
// method answers with what its own pinned enum declares for that state, and
// each has its own function because the route-code gate attributes every code
// a helper can emit to every route that calls it.

// fileItem is the file or file comment a request names, if any.
type fileItem struct {
	file    domain.FileID
	comment string
}

func requestedFileItem(fields map[string]string) (fileItem, bool) {
	item := fileItem{file: domain.FileID(strings.TrimSpace(fields["file"])), comment: strings.TrimSpace(fields["file_comment"])}
	return item, item.file != "" || item.comment != ""
}

// reactionsAddFileItem: reactions.add's pinned arguments are channel,
// timestamp and name, so naming a file is an argument it does not take.
func reactionsAddFileItem(w http.ResponseWriter, fields map[string]string) bool {
	if _, named := requestedFileItem(fields); !named {
		return false
	}
	writeError(w, "invalid_arg_name")
	return true
}

// reactionsGetFileItem answers the file with no reactions on it.
func (h Handler) reactionsGetFileItem(w http.ResponseWriter, r *http.Request, principal auth.Principal, fields map[string]string) bool {
	item, named := requestedFileItem(fields)
	if !named {
		return false
	}
	if item.comment != "" {
		writeError(w, "file_comment_not_found")
		return true
	}
	file, err := h.Messages.FileInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.file)
	if err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "type": "file", "file": slackobject.File(h.origin(r), file)})
	return true
}

// reactionsRemoveFileItem: a file the caller can see carries no reaction to
// remove.
func (h Handler) reactionsRemoveFileItem(w http.ResponseWriter, r *http.Request, principal auth.Principal, fields map[string]string) bool {
	item, named := requestedFileItem(fields)
	if !named {
		return false
	}
	if item.comment != "" {
		writeError(w, "file_comment_not_found")
		return true
	}
	if _, err := h.Messages.FileInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.file); err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return true
	}
	writeError(w, "no_reaction")
	return true
}

// pinsAddFileItem: neither a file nor a file comment can be pinned here.
func pinsAddFileItem(w http.ResponseWriter, fields map[string]string) bool {
	if _, named := requestedFileItem(fields); !named {
		return false
	}
	writeError(w, "not_pinnable")
	return true
}

// pinsRemoveFileItem: a file the caller can see is not pinned.
func (h Handler) pinsRemoveFileItem(w http.ResponseWriter, r *http.Request, principal auth.Principal, fields map[string]string) bool {
	item, named := requestedFileItem(fields)
	if !named {
		return false
	}
	if item.comment != "" {
		writeError(w, "file_comment_not_found")
		return true
	}
	if _, err := h.Messages.FileInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.file); err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return true
	}
	writeError(w, "not_pinned")
	return true
}
