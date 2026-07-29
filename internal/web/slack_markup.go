package web

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"strconv"
	"strings"
)

// renderMarkdown handles Slack's Markdown block, whose contract is CommonMark
// rather than Slack's older mrkdwn syntax. The deliberately small renderer
// supports the presentation constructs useful inside Block Kit while escaping
// raw HTML and rejecting unsafe links.
func renderMarkdown(text string) template.HTML {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var output strings.Builder
	inCode := false
	listTag := ""
	closeList := func() {
		if listTag != "" {
			output.WriteString("</")
			output.WriteString(listTag)
			output.WriteByte('>')
			listTag = ""
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			closeList()
			if inCode {
				output.WriteString("</code></pre>")
			} else {
				output.WriteString("<pre><code>")
			}
			inCode = !inCode
			continue
		}
		if inCode {
			output.WriteString(html.EscapeString(line))
			output.WriteByte('\n')
			continue
		}
		if trimmed == "" {
			closeList()
			continue
		}
		if level, body, ok := markdownHeading(trimmed); ok {
			closeList()
			fmt.Fprintf(&output, "<h%d>%s</h%d>", level, renderMarkdownInline(body), level)
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			closeList()
			output.WriteString("<blockquote>")
			output.WriteString(renderMarkdownInline(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))))
			output.WriteString("</blockquote>")
			continue
		}
		if body, ok := markdownListItem(trimmed, false); ok {
			if listTag != "ul" {
				closeList()
				output.WriteString("<ul>")
				listTag = "ul"
			}
			output.WriteString("<li>")
			output.WriteString(renderMarkdownInline(body))
			output.WriteString("</li>")
			continue
		}
		if body, ok := markdownListItem(trimmed, true); ok {
			if listTag != "ol" {
				closeList()
				output.WriteString("<ol>")
				listTag = "ol"
			}
			output.WriteString("<li>")
			output.WriteString(renderMarkdownInline(body))
			output.WriteString("</li>")
			continue
		}
		closeList()
		output.WriteString("<p>")
		output.WriteString(renderMarkdownInline(trimmed))
		output.WriteString("</p>")
	}
	closeList()
	if inCode {
		output.WriteString("</code></pre>")
	}
	return template.HTML(output.String()) // #nosec G203 -- all literals are escaped and all links are validated above.
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level == len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level:]), true
}

func markdownListItem(line string, ordered bool) (string, bool) {
	if !ordered {
		if len(line) >= 2 && strings.ContainsRune("-*+", rune(line[0])) && line[1] == ' ' {
			return strings.TrimSpace(line[2:]), true
		}
		return "", false
	}
	dot := strings.IndexByte(line, '.')
	if dot <= 0 || dot+1 >= len(line) || line[dot+1] != ' ' {
		return "", false
	}
	if _, err := strconv.Atoi(line[:dot]); err != nil {
		return "", false
	}
	return strings.TrimSpace(line[dot+2:]), true
}

func renderMarkdownInline(text string) string {
	var output strings.Builder
	for offset := 0; offset < len(text); {
		if strings.HasPrefix(text[offset:], "**") || strings.HasPrefix(text[offset:], "__") ||
			strings.HasPrefix(text[offset:], "~~") {
			delimiter := text[offset : offset+2]
			end := strings.Index(text[offset+2:], delimiter)
			if end >= 0 {
				tag := "strong"
				if delimiter == "~~" {
					tag = "del"
				}
				end += offset + 2
				output.WriteByte('<')
				output.WriteString(tag)
				output.WriteByte('>')
				output.WriteString(renderMarkdownInline(text[offset+2 : end]))
				output.WriteString("</")
				output.WriteString(tag)
				output.WriteByte('>')
				offset = end + 2
				continue
			}
		}
		switch text[offset] {
		case '`':
			if end := strings.IndexByte(text[offset+1:], '`'); end >= 0 {
				end += offset + 1
				output.WriteString("<code>")
				output.WriteString(html.EscapeString(text[offset+1 : end]))
				output.WriteString("</code>")
				offset = end + 1
				continue
			}
		case '*', '_':
			delimiter := text[offset]
			if end := strings.IndexByte(text[offset+1:], delimiter); end > 0 {
				end += offset + 1
				output.WriteString("<em>")
				output.WriteString(renderMarkdownInline(text[offset+1 : end]))
				output.WriteString("</em>")
				offset = end + 1
				continue
			}
		case '[':
			labelEnd := strings.Index(text[offset+1:], "](")
			if labelEnd >= 0 {
				labelEnd += offset + 1
				targetEnd := strings.IndexByte(text[labelEnd+2:], ')')
				if targetEnd >= 0 {
					targetEnd += labelEnd + 2
					label := text[offset+1 : labelEnd]
					target := strings.TrimSpace(text[labelEnd+2 : targetEnd])
					if href, ok := safeSlackLink(target); ok {
						output.WriteString(`<a href="`)
						output.WriteString(html.EscapeString(href))
						output.WriteString(`" rel="noreferrer noopener">`)
						output.WriteString(renderMarkdownInline(label))
						output.WriteString("</a>")
					} else {
						output.WriteString(html.EscapeString(label))
					}
					offset = targetEnd + 1
					continue
				}
			}
		}
		next := strings.IndexAny(text[offset+1:], "`*_~[")
		if next < 0 {
			next = len(text) - offset
		} else {
			next++
		}
		output.WriteString(html.EscapeString(text[offset : offset+next]))
		offset += next
	}
	return output.String()
}

// renderSlackMrkdwn implements the formatting constructs emitted by Slack app
// SDKs. Every app-controlled byte is escaped before it reaches the returned
// trusted template fragment; only this renderer supplies tags and attributes.
func renderSlackMrkdwn(text string) template.HTML {
	text = decodeSlackEntities(text)
	return template.HTML(renderSlackInline(text)) // #nosec G203 -- renderSlackInline escapes every literal and validates every link.
}

func decodeSlackEntities(text string) string {
	// Slack asks publishers to encode only these three characters. Decode them
	// once so "&amp;" is displayed as "&", while an arbitrary HTML entity stays
	// literal and cannot smuggle markup into the renderer.
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")
	return replacer.Replace(text)
}

func renderSlackInline(text string) string {
	var output strings.Builder
	for offset := 0; offset < len(text); {
		switch text[offset] {
		case '\n':
			output.WriteString("<br>\n")
			offset++
		case '`':
			fence := 1
			if strings.HasPrefix(text[offset:], "```") {
				fence = 3
			}
			delimiter := strings.Repeat("`", fence)
			end := strings.Index(text[offset+fence:], delimiter)
			if end < 0 {
				output.WriteString(html.EscapeString(delimiter))
				offset += fence
				continue
			}
			value := text[offset+fence : offset+fence+end]
			if fence == 3 {
				output.WriteString("<pre><code>")
				output.WriteString(html.EscapeString(strings.Trim(value, "\n")))
				output.WriteString("</code></pre>")
			} else {
				output.WriteString("<code>")
				output.WriteString(html.EscapeString(value))
				output.WriteString("</code>")
			}
			offset += fence + end + fence
		case '<':
			end := strings.IndexByte(text[offset+1:], '>')
			if end < 0 {
				output.WriteString("&lt;")
				offset++
				continue
			}
			raw := text[offset+1 : offset+1+end]
			if rendered, ok := renderSlackReference(raw); ok {
				output.WriteString(rendered)
			} else {
				output.WriteString("&lt;")
				output.WriteString(html.EscapeString(raw))
				output.WriteString("&gt;")
			}
			offset += end + 2
		case '*', '_', '~':
			delimiter := text[offset]
			end := strings.IndexByte(text[offset+1:], delimiter)
			if end <= 0 || text[offset+1] == ' ' {
				output.WriteString(html.EscapeString(text[offset : offset+1]))
				offset++
				continue
			}
			end += offset + 1
			if text[end-1] == ' ' {
				output.WriteString(html.EscapeString(text[offset : offset+1]))
				offset++
				continue
			}
			tag := map[byte]string{'*': "strong", '_': "em", '~': "del"}[delimiter]
			output.WriteByte('<')
			output.WriteString(tag)
			output.WriteByte('>')
			output.WriteString(renderSlackInline(text[offset+1 : end]))
			output.WriteString("</")
			output.WriteString(tag)
			output.WriteByte('>')
			offset = end + 1
		default:
			next := strings.IndexAny(text[offset:], "\n`<*_~")
			if next <= 0 {
				next = len(text) - offset
			}
			output.WriteString(html.EscapeString(text[offset : offset+next]))
			offset += next
		}
	}
	return output.String()
}

func renderSlackReference(raw string) (string, bool) {
	target, label, _ := strings.Cut(raw, "|")
	target = strings.TrimSpace(target)
	label = strings.TrimSpace(label)
	switch {
	case strings.HasPrefix(target, "@"):
		if label == "" {
			label = "@" + strings.TrimPrefix(target, "@")
		}
		return `<span class="slack-mention">` + html.EscapeString(label) + `</span>`, true
	case strings.HasPrefix(target, "#"):
		if label == "" {
			label = "#" + strings.TrimPrefix(target, "#")
		} else if !strings.HasPrefix(label, "#") {
			label = "#" + label
		}
		return `<span class="slack-mention">` + html.EscapeString(label) + `</span>`, true
	case strings.HasPrefix(target, "!"):
		if label == "" {
			label = slackSpecialReferenceLabel(target)
		}
		return `<span class="slack-mention">` + html.EscapeString(label) + `</span>`, true
	default:
		if label == "" {
			label = target
		}
		if href, ok := safeSlackLink(target); ok {
			return `<a href="` + html.EscapeString(href) + `" rel="noreferrer noopener">` + html.EscapeString(label) + `</a>`, true
		}
		return "", false
	}
}

func slackSpecialReferenceLabel(target string) string {
	if strings.HasPrefix(target, "!date^") {
		parts := strings.Split(target, "^")
		if len(parts) > 3 && strings.TrimSpace(parts[len(parts)-1]) != "" {
			return parts[len(parts)-1]
		}
	}
	return "@" + strings.TrimPrefix(strings.SplitN(target, "^", 2)[0], "!")
}

func safeSlackLink(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto", "tel":
		return parsed.String(), true
	default:
		return "", false
	}
}

func renderRichText(value any) template.HTML {
	elements, _ := value.([]any)
	var output strings.Builder
	for _, raw := range elements {
		element, _ := raw.(map[string]any)
		renderRichTextElement(&output, element, true)
	}
	return template.HTML(output.String()) // #nosec G203 -- renderRichTextElement escapes all app-controlled values and validates links.
}

func renderRichTextElement(output *strings.Builder, element map[string]any, block bool) {
	elementType := strings.TrimSpace(stringValue(element["type"]))
	switch elementType {
	case "rich_text_section":
		if block {
			output.WriteString("<div>")
		}
		renderRichTextChildren(output, element["elements"])
		if block {
			output.WriteString("</div>")
		}
	case "rich_text_list":
		tag := "ul"
		if strings.TrimSpace(stringValue(element["style"])) == "ordered" {
			tag = "ol"
		}
		output.WriteByte('<')
		output.WriteString(tag)
		if indent, ok := element["indent"].(float64); ok && indent > 0 {
			output.WriteString(` class="rich-text-indent-`)
			output.WriteString(strconv.Itoa(min(int(indent), 8)))
			output.WriteString(`"`)
		}
		output.WriteByte('>')
		children, _ := element["elements"].([]any)
		for _, raw := range children {
			child, _ := raw.(map[string]any)
			output.WriteString("<li>")
			renderRichTextElement(output, child, false)
			output.WriteString("</li>")
		}
		output.WriteString("</")
		output.WriteString(tag)
		output.WriteByte('>')
	case "rich_text_quote":
		output.WriteString("<blockquote>")
		renderRichTextChildren(output, element["elements"])
		output.WriteString("</blockquote>")
	case "rich_text_preformatted":
		output.WriteString("<pre><code>")
		output.WriteString(html.EscapeString(strings.Join(elementTextList(element["elements"]), "")))
		output.WriteString("</code></pre>")
	case "text":
		renderStyledRichText(output, stringValue(element["text"]), element["style"])
	case "link":
		label := stringValue(element["text"])
		href, ok := safeSlackLink(strings.TrimSpace(stringValue(element["url"])))
		if label == "" {
			label = href
		}
		if ok {
			output.WriteString(`<a href="`)
			output.WriteString(html.EscapeString(href))
			output.WriteString(`" rel="noreferrer noopener">`)
			output.WriteString(html.EscapeString(label))
			output.WriteString("</a>")
		} else {
			output.WriteString(html.EscapeString(label))
		}
	case "user":
		output.WriteString(`<span class="slack-mention">@`)
		output.WriteString(html.EscapeString(stringValue(element["user_id"])))
		output.WriteString("</span>")
	case "channel":
		output.WriteString(`<span class="slack-mention">#`)
		output.WriteString(html.EscapeString(stringValue(element["channel_id"])))
		output.WriteString("</span>")
	case "broadcast":
		output.WriteString(`<span class="slack-mention">@`)
		output.WriteString(html.EscapeString(stringValue(element["range"])))
		output.WriteString("</span>")
	case "emoji":
		output.WriteByte(':')
		output.WriteString(html.EscapeString(stringValue(element["name"])))
		output.WriteByte(':')
	case "date":
		fallback := strings.TrimSpace(stringValue(element["fallback"]))
		if fallback == "" {
			fallback = fmt.Sprint(element["timestamp"])
		}
		output.WriteString(html.EscapeString(fallback))
	default:
		renderRichTextChildren(output, element["elements"])
	}
}

func renderRichTextChildren(output *strings.Builder, value any) {
	children, _ := value.([]any)
	for _, raw := range children {
		child, _ := raw.(map[string]any)
		renderRichTextElement(output, child, false)
	}
}

func renderStyledRichText(output *strings.Builder, text string, rawStyle any) {
	style, _ := rawStyle.(map[string]any)
	tags := make([]string, 0, 4)
	for _, candidate := range []struct {
		field string
		tag   string
	}{{"bold", "strong"}, {"italic", "em"}, {"strike", "del"}, {"code", "code"}} {
		if enabled, _ := style[candidate.field].(bool); enabled {
			tags = append(tags, candidate.tag)
			output.WriteByte('<')
			output.WriteString(candidate.tag)
			output.WriteByte('>')
		}
	}
	output.WriteString(html.EscapeString(text))
	for index := len(tags) - 1; index >= 0; index-- {
		output.WriteString("</")
		output.WriteString(tags[index])
		output.WriteByte('>')
	}
}
