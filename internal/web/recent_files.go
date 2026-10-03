package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The composer's + menu offers the member's recent files, as Slack's does:
// choosing one shares it into the conversation, or into the thread a reply
// composer belongs to, as its own message.

// recentFileLimit is how many recent files the menu offers, and
// recentFileScan how many visible files it reads to find them.
const (
	recentFileLimit = 5
	recentFileScan  = 100
)

type recentFileView struct {
	ID    string
	Title string
	Kind  string
}

// composerRecentFiles is the member's own most recent files, the ones they
// may share. A listing failure leaves the menu without them rather than
// failing the page.
func (h Handler) composerRecentFiles(ctx context.Context, principal auth.Principal) []recentFileView {
	if !principal.HasScope(auth.ScopeFilesRead) || !principal.HasScope(auth.ScopeChatWrite) {
		return nil
	}
	page, err := h.Messages.Files(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: recentFileScan})
	if err != nil {
		return nil
	}
	var files []recentFileView
	for _, file := range page.Files {
		if file.Deleted || file.Uploader != principal.UserID {
			continue
		}
		_, label, _ := fileKind(file)
		files = append(files, recentFileView{ID: string(file.ID), Title: fileTitle(file), Kind: label})
		if len(files) == recentFileLimit {
			break
		}
	}
	return files
}

func (h Handler) shareRecentFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The file could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	thread := strings.TrimSpace(r.URL.Query().Get("thread"))
	_, err = h.Messages.ShareFile(r.Context(), principal.WorkspaceID, principal.UserID, domain.FileID(strings.TrimSpace(fields["file"])), domain.ConversationID(channel), domain.MessageTimestamp(thread))
	if err != nil {
		status, reason := http.StatusServiceUnavailable, "The workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, domain.ErrInvalidFile), errors.Is(err, domain.ErrInvalidTimestamp):
			status, reason = http.StatusBadRequest, "Choose a file to share."
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That file, or the thread it was to be shared into, is no longer available."
		case errors.Is(err, domain.ErrNotInConversation):
			status, reason = http.StatusForbidden, "Join the conversation to share files in it."
		case errors.Is(err, domain.ErrConversationAlreadyArchived):
			status, reason = http.StatusConflict, "The conversation is archived."
		}
		h.writeMutationError(w, r, status, "The file was not shared", reason)
		return
	}
	h.redirectMutation(w, r, appURL(channel, thread, "", "", ""))
}
