package slack

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// canvases.getContent returns a canvas the caller can view as one markdown or
// HTML string, without pagination. The markdown is the form canvases.create
// and canvases.edit accept, and the HTML is the rendering the product's own
// reading view uses (domain.CanvasDocumentHTML), so neither is a second
// interpretation of the document. A canvas that does not exist and one the
// caller cannot view are the same answer, canvas_not_found.
func (h Handler) getCanvasContent(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	canvasID := domain.CanvasID(strings.TrimSpace(fields["canvas_id"]))
	format, known := domain.ParseCanvasContentFormat(fields["content_type"])
	if canvasID == "" || !known {
		writeError(w, "invalid_arguments")
		return
	}
	canvas, err := h.Messages.Canvas(r.Context(), principal.WorkspaceID, principal.UserID, canvasID)
	if err != nil {
		writeError(w, mapServiceErrorNamed(err, "canvas_not_found", "invalid_arguments", ""))
		return
	}
	var content string
	if format == domain.CanvasContentHTML {
		content, err = domain.CanvasDocumentHTML(canvas.DocumentContent, domain.CanvasHTMLStyle{})
	} else {
		content, err = domain.CanvasDocumentMarkdown(canvas.DocumentContent)
	}
	if err != nil {
		// A stored body this version cannot decode is not something the
		// caller can correct; it is the server failing to complete the read.
		if errors.Is(err, domain.ErrInvalidCanvas) {
			writeError(w, "internal_error")
			return
		}
		writeError(w, mapServiceErrorNamed(err, "canvas_not_found", "invalid_arguments", ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content})
}
