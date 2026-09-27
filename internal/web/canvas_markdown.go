package web

import (
	"html"
	"html/template"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// renderCanvasMarkdown renders one canvas block's markdown as HTML: bulleted,
// numbered and checklist items, quotes, fenced code, and inline bold, italic,
// strikethrough, code, links and member mentions. It is the canvas's reading
// view (CANVAS-02); the stored text is untouched, and every byte of it is
// escaped before any of the tags this function emits are added, so the only
// markup in the result is markup this function wrote. A link must be http,
// https or mailto: anything else stays text.
func renderCanvasMarkdown(text string, names *userNames) template.HTML {
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
			out.WriteString(canvasInline(line, names))
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
			checked := trimmed[3] != ' '
			box := `<span class="check" aria-hidden="true">☐</span><span class="visually-hidden">Not done: </span>`
			if checked {
				box = `<span class="check done" aria-hidden="true">☑</span><span class="visually-hidden">Done: </span>`
			}
			out.WriteString("<li>" + box + canvasInline(trimmed[6:], names) + "</li>")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ "):
			flush()
			openList("ul", "")
			out.WriteString("<li>" + canvasInline(trimmed[2:], names) + "</li>")
		case numberedItem(trimmed) > 0:
			flush()
			openList("ol", "")
			out.WriteString("<li>" + canvasInline(strings.TrimSpace(trimmed[numberedItem(trimmed):]), names) + "</li>")
		case strings.HasPrefix(trimmed, ">"):
			flush()
			closeList()
			out.WriteString("<blockquote>" + canvasInline(strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), names) + "</blockquote>")
		default:
			closeList()
			paragraph = append(paragraph, trimmed)
		}
	}
	flush()
	closeList()
	return template.HTML(out.String()) // #nosec G203 -- every stored byte is escaped by canvasInline or html.EscapeString; the tags are this function's own.
}

// numberedItem is the length of a "12. " or "3) " prefix, or 0.
func numberedItem(line string) int {
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
func canvasInline(text string, names *userNames) string {
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
				out.WriteString("<strong>" + canvasInline(text[2:2+end], names) + "</strong>")
				text = text[end+4:]
				continue
			}
		case strings.HasPrefix(text, "~~"):
			if end := strings.Index(text[2:], "~~"); end > 0 {
				out.WriteString("<s>" + canvasInline(text[2:2+end], names) + "</s>")
				text = text[end+4:]
				continue
			}
		case text[0] == '*' || text[0] == '_':
			marker := text[:1]
			if end := strings.Index(text[1:], marker); end > 0 && text[1] != ' ' {
				out.WriteString("<em>" + canvasInline(text[1:1+end], names) + "</em>")
				text = text[end+2:]
				continue
			}
		case text[0] == '[':
			if closeLabel := strings.Index(text, "]("); closeLabel > 0 {
				if closeURL := strings.IndexByte(text[closeLabel+2:], ')'); closeURL > 0 {
					target := strings.TrimSpace(text[closeLabel+2 : closeLabel+2+closeURL])
					if safeCanvasURL(target) {
						out.WriteString(`<a href="` + html.EscapeString(target) + `" rel="noopener noreferrer" target="_blank">` + canvasInline(text[1:closeLabel], names) + "</a>")
						text = text[closeLabel+3+closeURL:]
						continue
					}
				}
			}
		case strings.HasPrefix(text, "<@"):
			if end := strings.IndexByte(text, '>'); end > 2 {
				id := strings.SplitN(text[2:end], "|", 2)[0]
				if id != "" && !strings.ContainsAny(id, " \t<") {
					name := id
					if names != nil {
						name = names.name(domain.UserID(id))
					}
					out.WriteString(`<a class="canvas-mention" href="/app/members?user=` + url.QueryEscape(id) + `" data-profile-user="` + html.EscapeString(id) + `">@` + html.EscapeString(name) + "</a>")
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
