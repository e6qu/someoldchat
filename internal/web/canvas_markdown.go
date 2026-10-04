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

// renderCanvasEditorMarkdown renders one canvas block for the editor. It is
// the same rendering as the reading view with two differences an editor
// needs: a mention is an atomic pill carrying the <@U…> it saves as, so it is
// deleted whole and never typed into, and a checklist box is a control the
// writer can toggle.
func renderCanvasEditorMarkdown(text string, names *userNames) template.HTML {
	style := domain.CanvasHTMLStyle{
		Mention: func(id domain.UserID) string {
			name := string(id)
			if names != nil {
				name = names.name(id)
			}
			return `<span class="canvas-mention" contenteditable="false" data-entity="&lt;@` + html.EscapeString(string(id)) + `&gt;">@` + html.EscapeString(name) + "</span>"
		},
		Checkbox: func(checked bool) string {
			if checked {
				return `<span class="check done" contenteditable="false" role="checkbox" aria-checked="true" aria-label="Done" data-check>☑</span>`
			}
			return `<span class="check" contenteditable="false" role="checkbox" aria-checked="false" aria-label="Done" data-check>☐</span>`
		},
	}
	return template.HTML(domain.CanvasMarkdownHTML(text, style)) // #nosec G203 -- every stored byte is escaped by domain.CanvasMarkdownHTML; the tags are its own and this style's.
}
