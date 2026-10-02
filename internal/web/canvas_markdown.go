package web

import (
	"html"
	"html/template"
	"net/url"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// renderCanvasMarkdown renders one canvas block's markdown as HTML for the
// canvas's reading view (CANVAS-02). The rendering itself is
// domain.CanvasMarkdownHTML, the one canvases.getContent also uses; this
// client only decides how a mention and a checklist box look here: a mention
// links to the member's profile under the name this page shows them by, and a
// box carries a visually hidden word for screen readers.
func renderCanvasMarkdown(text string, names *userNames) template.HTML {
	style := domain.CanvasHTMLStyle{
		Mention: func(id domain.UserID) string {
			name := string(id)
			if names != nil {
				name = names.name(id)
			}
			return `<a class="canvas-mention" href="/app/members?user=` + url.QueryEscape(string(id)) + `" data-profile-user="` + html.EscapeString(string(id)) + `">@` + html.EscapeString(name) + "</a>"
		},
		Checkbox: func(checked bool) string {
			if checked {
				return `<span class="check done" aria-hidden="true">☑</span><span class="visually-hidden">Done: </span>`
			}
			return `<span class="check" aria-hidden="true">☐</span><span class="visually-hidden">Not done: </span>`
		},
	}
	return template.HTML(domain.CanvasMarkdownHTML(text, style)) // #nosec G203 -- every stored byte is escaped by domain.CanvasMarkdownHTML; the tags are its own and this style's.
}
