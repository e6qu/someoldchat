package domain

import (
	"html"
	"net/url"
	"strings"
)

// CanvasHTMLStyle is what differs between the two readers of a canvas: how a
// member mention and a checklist box are drawn. Everything else about a
// canvas's markup is one rendering, shared by the product's reading view and
// canvases.getContent, so the two cannot disagree about what a document says.
//
// A nil function selects the plain, self-contained default, which needs no
// stylesheet and no route of this product to make sense.
type CanvasHTMLStyle struct {
	// Mention renders one member mention. The id is already validated as a
	// bare identifier; the result is trusted markup.
	Mention func(UserID) string
	// Checkbox renders the box in front of one checklist item.
	Checkbox func(checked bool) string
}

func (style CanvasHTMLStyle) mention(id UserID) string {
	if style.Mention != nil {
		return style.Mention(id)
	}
	return `<span class="mention" data-user-id="` + html.EscapeString(string(id)) + `">@` + html.EscapeString(string(id)) + `</span>`
}

func (style CanvasHTMLStyle) checkbox(checked bool) string {
	if style.Checkbox != nil {
		return style.Checkbox(checked)
	}
	if checked {
		return `<input type="checkbox" checked disabled> `
	}
	return `<input type="checkbox" disabled> `
}

// CanvasMarkdownHTML renders one canvas block's markdown as HTML: bulleted,
// numbered and checklist items, quotes, fenced code, and inline bold, italic,
// strikethrough, code, links and member mentions. The stored text is
// untouched, and every byte of it is escaped before any of the tags this
// function emits are added, so the only markup in the result is markup this
// function (or the style it was handed) wrote. A link must be http, https or
// mailto: anything else stays text.
func CanvasMarkdownHTML(text string, style CanvasHTMLStyle) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out strings.Builder
	list := ""
	closeList := func() {
		if list != "" {
			out.WriteString("</" + list + ">")
			list = ""
		}
	}
	openList := func(kind, class string) {
		if list == kind {
			return
		}
		closeList()
		list = kind
		if class != "" {
			out.WriteString("<" + kind + ` class="` + class + `">`)
		} else {
			out.WriteString("<" + kind + ">")
		}
	}
	var paragraph []string
	flush := func() {
		if len(paragraph) == 0 {
			return
		}
		out.WriteString("<p>")
		for index, line := range paragraph {
			if index > 0 {
				out.WriteString("<br>")
			}
			out.WriteString(canvasInline(line, style))
		}
		out.WriteString("</p>")
		paragraph = paragraph[:0]
	}
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flush()
			closeList()
			var code []string
			for index++; index < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[index]), "```"); index++ {
				code = append(code, lines[index])
			}
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>")
		case trimmed == "":
			flush()
			closeList()
		case strings.HasPrefix(trimmed, "- [ ] ") || strings.HasPrefix(trimmed, "- [x] ") || strings.HasPrefix(trimmed, "- [X] "):
			flush()
			openList("ul", "checklist")
			out.WriteString("<li>" + style.checkbox(trimmed[3] != ' ') + canvasInline(trimmed[6:], style) + "</li>")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ "):
			flush()
			openList("ul", "")
			out.WriteString("<li>" + canvasInline(trimmed[2:], style) + "</li>")
		case canvasNumberedItem(trimmed) > 0:
			flush()
			openList("ol", "")
			out.WriteString("<li>" + canvasInline(strings.TrimSpace(trimmed[canvasNumberedItem(trimmed):]), style) + "</li>")
		case strings.HasPrefix(trimmed, ">"):
			flush()
			closeList()
			out.WriteString("<blockquote>" + canvasInline(strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), style) + "</blockquote>")
		default:
			closeList()
			paragraph = append(paragraph, trimmed)
		}
	}
	flush()
	closeList()
	return out.String()
}

// canvasNumberedItem is the length of a "12. " or "3) " prefix, or 0.
func canvasNumberedItem(line string) int {
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits+1 >= len(line) || (line[digits] != '.' && line[digits] != ')') || line[digits+1] != ' ' {
		return 0
	}
	return digits + 2
}

// canvasInline renders one line's inline markdown. It scans the raw text, so
// a marker inside a code span is literal, and escapes each run of plain text
// as it copies it.
func canvasInline(text string, style CanvasHTMLStyle) string {
	var out strings.Builder
	for len(text) > 0 {
		switch {
		case text[0] == '`':
			if end := strings.IndexByte(text[1:], '`'); end >= 0 {
				out.WriteString("<code>" + html.EscapeString(text[1:1+end]) + "</code>")
				text = text[end+2:]
				continue
			}
		case strings.HasPrefix(text, "**") || strings.HasPrefix(text, "__"):
			marker := text[:2]
			if end := strings.Index(text[2:], marker); end > 0 {
				out.WriteString("<strong>" + canvasInline(text[2:2+end], style) + "</strong>")
				text = text[end+4:]
				continue
			}
		case strings.HasPrefix(text, "~~"):
			if end := strings.Index(text[2:], "~~"); end > 0 {
				out.WriteString("<s>" + canvasInline(text[2:2+end], style) + "</s>")
				text = text[end+4:]
				continue
			}
		case text[0] == '*' || text[0] == '_':
			marker := text[:1]
			if end := strings.Index(text[1:], marker); end > 0 && text[1] != ' ' {
				out.WriteString("<em>" + canvasInline(text[1:1+end], style) + "</em>")
				text = text[end+2:]
				continue
			}
		case text[0] == '[':
			if closeLabel := strings.Index(text, "]("); closeLabel > 0 {
				if closeURL := strings.IndexByte(text[closeLabel+2:], ')'); closeURL > 0 {
					target := strings.TrimSpace(text[closeLabel+2 : closeLabel+2+closeURL])
					if safeCanvasURL(target) {
						out.WriteString(`<a href="` + html.EscapeString(target) + `" rel="noopener noreferrer" target="_blank">` + canvasInline(text[1:closeLabel], style) + "</a>")
						text = text[closeLabel+3+closeURL:]
						continue
					}
				}
			}
		case strings.HasPrefix(text, "<@"):
			if end := strings.IndexByte(text, '>'); end > 2 {
				id := strings.SplitN(text[2:end], "|", 2)[0]
				if id != "" && !strings.ContainsAny(id, " \t<") {
					out.WriteString(style.mention(UserID(id)))
					text = text[end+1:]
					continue
				}
			}
		case strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "http://"):
			end := strings.IndexAny(text, " \t<>\"")
			if end < 0 {
				end = len(text)
			}
			target := strings.TrimRight(text[:end], ".,;:!?)")
			out.WriteString(`<a href="` + html.EscapeString(target) + `" rel="noopener noreferrer" target="_blank">` + html.EscapeString(target) + "</a>")
			text = text[len(target):]
			continue
		}
		// Copy one character of plain text, escaped.
		size := 1
		for size < len(text) && text[size]&0xC0 == 0x80 {
			size++
		}
		out.WriteString(html.EscapeString(text[:size]))
		text = text[size:]
	}
	return out.String()
}

func safeCanvasURL(target string) bool {
	parsed, err := url.Parse(target)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "mailto":
		return true
	}
	return false
}
