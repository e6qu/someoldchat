package web

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/slackemoji"
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
	return renderSlackMrkdwnMarking(text, nil, nil)
}

// renderSlackMrkdwnMarking renders a message body and marks the search terms
// inside it. The <mark> elements come from the renderer's literal-prose branch,
// so the guarantee in the #nosec note below is unchanged: every literal is
// escaped and every URL validated, and marking adds one tag around a span of
// already-escaped text.
func renderSlackMrkdwnMarking(text string, customEmoji map[string]string, terms []string) template.HTML {
	text = decodeSlackEntities(text)
	return template.HTML(renderSlackBlocks(text, customEmoji, terms)) // #nosec G203 -- the renderer escapes every literal and validates every URL.
}

// renderSlackBlocks is the block level of Slack's mrkdwn: fenced code, quotes
// and the bullet/number lines Slack's own client writes as a list's fallback
// text. Everything else is inline prose, one line per <br>.
//
// It emits no raw newline outside a <pre>. The message body is displayed with
// white-space:pre-wrap so a member's runs of spaces survive, and a newline
// beside each <br> — which the renderer used to write — was rendered as a
// second line break, double-spacing every multi-line message.
//
// A body that is nothing but prose is returned as bare inline markup, so a
// caller placing it inside a label or a link is unaffected by the block pass.
func renderSlackBlocks(text string, customEmoji map[string]string, terms []string) string {
	var output strings.Builder
	for offset := 0; offset < len(text); {
		start := strings.Index(text[offset:], "```")
		if start < 0 {
			renderSlackProse(&output, text[offset:], customEmoji, terms)
			break
		}
		start += offset
		end := strings.Index(text[start+3:], "```")
		if end < 0 {
			// An unclosed fence is literal text, as it is in Slack.
			renderSlackProse(&output, text[offset:], customEmoji, terms)
			break
		}
		end += start + 3
		renderSlackProse(&output, strings.TrimSuffix(text[offset:start], "\n"), customEmoji, terms)
		output.WriteString("<pre><code>")
		output.WriteString(html.EscapeString(strings.Trim(text[start+3:end], "\n")))
		output.WriteString("</code></pre>")
		offset = end + 3
		if offset < len(text) && text[offset] == '\n' {
			offset++
		}
	}
	return output.String()
}

// slackListItem reports whether a line is one item of the list fallback text
// Slack's composer writes: "• item" (or a nested "◦ item") for a bulleted
// list and "1. item" for a numbered one. A hyphen is not a list in mrkdwn.
func slackListItem(line string) (tag, body string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	for _, bullet := range []string{"• ", "◦ ", "▪ "} {
		if strings.HasPrefix(trimmed, bullet) {
			return "ul", trimmed[len(bullet):], true
		}
	}
	digits := 0
	for digits < len(trimmed) && digits < 4 && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits > 0 && strings.HasPrefix(trimmed[digits:], ". ") {
		return "ol", trimmed[digits+2:], true
	}
	return "", "", false
}

// renderSlackProse renders the text between fences. Consecutive quote lines
// become one <blockquote>, and ">>>" quotes everything after it, as in Slack.
func renderSlackProse(output *strings.Builder, text string, customEmoji map[string]string, terms []string) {
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	inline := func(line string) string { return renderSlackInlineMarking(line, customEmoji, terms) }
	for index := 0; index < len(lines); {
		line := lines[index]
		switch {
		case strings.HasPrefix(line, ">>>"):
			rest := append([]string{strings.TrimPrefix(strings.TrimPrefix(line, ">>>"), " ")}, lines[index+1:]...)
			output.WriteString("<blockquote>")
			for position, quoted := range rest {
				if position > 0 {
					output.WriteString("<br>")
				}
				output.WriteString(inline(quoted))
			}
			output.WriteString("</blockquote>")
			return
		case strings.HasPrefix(line, ">"):
			output.WriteString("<blockquote>")
			for first := true; index < len(lines) && strings.HasPrefix(lines[index], ">") && !strings.HasPrefix(lines[index], ">>>"); index++ {
				if !first {
					output.WriteString("<br>")
				}
				first = false
				output.WriteString(inline(strings.TrimPrefix(strings.TrimPrefix(lines[index], ">"), " ")))
			}
			output.WriteString("</blockquote>")
			continue
		}
		if tag, _, ok := slackListItem(line); ok {
			output.WriteString("<" + tag + ">")
			for index < len(lines) {
				itemTag, body, isItem := slackListItem(lines[index])
				if !isItem || itemTag != tag {
					break
				}
				output.WriteString("<li>")
				output.WriteString(inline(body))
				output.WriteString("</li>")
				index++
			}
			output.WriteString("</" + tag + ">")
			continue
		}
		// A run of prose lines, joined by line breaks. The run ends at the
		// next quote or list, which starts a block of its own, so no break is
		// written before it.
		for first := true; index < len(lines); index++ {
			current := lines[index]
			if strings.HasPrefix(current, ">") {
				break
			}
			if _, _, isItem := slackListItem(current); isItem {
				break
			}
			if !first {
				output.WriteString("<br>")
			}
			first = false
			output.WriteString(inline(current))
		}
	}
}

func decodeSlackEntities(text string) string {
	// Slack asks publishers to encode only these three characters. Decode them
	// once so "&amp;" is displayed as "&", while an arbitrary HTML entity stays
	// literal and cannot smuggle markup into the renderer.
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")
	return replacer.Replace(text)
}

// renderSlackInlineMarking is the renderer with search terms threaded through
// it. The terms reach exactly one branch — the literal prose below — so marking
// can never land inside a tag, an attribute, a URL or an entity, which is what
// a string replace over the finished HTML would do.
func renderSlackInlineMarking(text string, customEmoji map[string]string, terms []string) string {
	var output strings.Builder
	for offset := 0; offset < len(text); {
		switch text[offset] {
		case '\n':
			output.WriteString("<br>")
			offset++
		case '\\':
			if offset+1 < len(text) && strings.ContainsRune(`\*_~`+"`", rune(text[offset+1])) {
				output.WriteString(html.EscapeString(text[offset+1 : offset+2]))
				offset += 2
				continue
			}
			output.WriteByte('\\')
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
			if rendered, ok := renderSlackReferenceMarking(raw, terms); ok {
				output.WriteString(rendered)
			} else {
				output.WriteString("&lt;")
				output.WriteString(html.EscapeString(raw))
				output.WriteString("&gt;")
			}
			offset += end + 2
		case ':':
			end := strings.IndexByte(text[offset+1:], ':')
			if end < 1 {
				output.WriteByte(':')
				offset++
				continue
			}
			end += offset + 1
			name := text[offset+1 : end]
			if !validEmojiCode(name) {
				output.WriteByte(':')
				offset++
				continue
			}
			if imageURL := customEmoji[strings.ToLower(name)]; imageURL != "" {
				output.WriteString(`<img class="custom-emoji" src="`)
				output.WriteString(html.EscapeString(imageURL))
				output.WriteString(`" alt=":`)
				output.WriteString(html.EscapeString(name))
				output.WriteString(`:" title=":`)
				output.WriteString(html.EscapeString(name))
				output.WriteString(`:" loading="lazy"><span class="emoji-code" aria-hidden="true">:`)
				output.WriteString(html.EscapeString(name))
				output.WriteString(`:</span>`)
				offset = end + 1
				continue
			}
			if emoji, ok := slackemoji.Lookup(name); ok {
				glyph := slackemoji.Unicode(emoji)
				// Slack writes a skin tone as a second code straight after the
				// first, ":wave::skin-tone-3:"; it modifies the glyph rather
				// than printing as text.
				if rest := text[end+1:]; strings.HasPrefix(rest, ":skin-tone-") && len(rest) >= len(":skin-tone-2:") && rest[len(":skin-tone-2:")-1] == ':' {
					if toned, ok := slackemoji.ReactionUnicode(name + ":" + rest[:len(":skin-tone-2:")-1]); ok {
						glyph = toned
						end += len(":skin-tone-2:")
					}
				}
				output.WriteString(`<span class="standard-emoji" role="img" aria-label=":`)
				output.WriteString(html.EscapeString(name))
				output.WriteString(`:">`)
				output.WriteString(html.EscapeString(glyph))
				output.WriteString(`</span><span class="emoji-code" aria-hidden="true">:`)
				output.WriteString(html.EscapeString(name))
				output.WriteString(`:</span>`)
				offset = end + 1
				continue
			}
			output.WriteByte(':')
			offset++
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
			output.WriteString(renderSlackInlineMarking(text[offset+1:end], customEmoji, terms))
			output.WriteString("</")
			output.WriteString(tag)
			output.WriteByte('>')
			offset = end + 1
		default:
			next := strings.IndexAny(text[offset:], "\n\\`<*_~:")
			if next <= 0 {
				next = len(text) - offset
			}
			// The one place literal prose is emitted, and therefore the only
			// place a search hit may be marked.
			output.WriteString(markTerms(text[offset:offset+next], terms))
			offset += next
		}
	}
	return output.String()
}

func validEmojiCode(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '+' {
			continue
		}
		return false
	}
	return true
}

// renderSlackReferenceMarking marks the visible label of a reference and never
// its target. A member searching for a word they can see in a link should find
// it emphasised; the href is machinery, and marking it would put a tag inside
// an attribute — which is the whole reason marking is threaded through the
// renderer rather than applied to its output.
func renderSlackReferenceMarking(raw string, terms []string) (string, bool) {
	target, label, _ := strings.Cut(raw, "|")
	target = strings.TrimSpace(target)
	label = strings.TrimSpace(label)
	switch {
	case strings.HasPrefix(target, "@"):
		// A member mention opens that person, as it does in Slack. The
		// renderer does not know who is reading, so the viewer's own
		// mentions are flagged afterwards by markSelfMentions, keyed on
		// data-user-id.
		id := strings.TrimPrefix(target, "@")
		if label == "" {
			label = "@" + id
		}
		if !slackIdentifier(id) {
			return `<span class="slack-mention">` + markTerms(label, terms) + `</span>`, true
		}
		return `<a class="slack-mention" href="/app/members?q=` + html.EscapeString(url.QueryEscape(strings.TrimPrefix(label, "@"))) + `" data-user-id="` + id + `">` + markTerms(label, terms) + `</a>`, true
	case strings.HasPrefix(target, "#"):
		id := strings.TrimPrefix(target, "#")
		if label == "" {
			label = "#" + id
		} else if !strings.HasPrefix(label, "#") {
			label = "#" + label
		}
		if !slackIdentifier(id) {
			return `<span class="slack-mention">` + markTerms(label, terms) + `</span>`, true
		}
		return `<a class="slack-mention" href="/app?channel=` + id + `">` + markTerms(label, terms) + `</a>`, true
	case strings.HasPrefix(target, "!date^"):
		// A formatted date is text, not a mention.
		if label == "" {
			label = slackSpecialReferenceLabel(target)
		}
		return `<span class="slack-date">` + markTerms(label, terms) + `</span>`, true
	case target == "!here" || target == "!channel" || target == "!everyone":
		if label == "" {
			label = slackSpecialReferenceLabel(target)
		}
		return `<span class="slack-mention mention-broadcast">` + markTerms(label, terms) + `</span>`, true
	case strings.HasPrefix(target, "!"):
		if label == "" {
			label = slackSpecialReferenceLabel(target)
		}
		return `<span class="slack-mention">` + markTerms(label, terms) + `</span>`, true
	default:
		if label == "" {
			label = target
		}
		if href, ok := safeSlackLink(target); ok {
			return `<a href="` + html.EscapeString(href) + `" rel="noreferrer noopener">` + markTerms(label, terms) + `</a>`, true
		}
		return "", false
	}
}

// slackIdentifier accepts the shape of a Slack user or conversation ID, the
// only thing a mention writes into its href and data attribute unescaped.
func slackIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// markSelfMentions flags the viewer's own mentions in rendered message markup,
// which the stylesheet highlights the way Slack does. The renderer escapes
// every quote in literal text, so the attribute it matches can only be one the
// renderer wrote on a mention.
func markSelfMentions(rendered template.HTML, viewer string) template.HTML {
	if viewer == "" || !slackIdentifier(viewer) {
		return rendered
	}
	needle := `data-user-id="` + viewer + `"`
	if !strings.Contains(string(rendered), needle) {
		return rendered
	}
	return template.HTML(strings.ReplaceAll(string(rendered), needle, needle+` data-self="true"`)) // #nosec G203 -- adds a fixed attribute beside one the renderer wrote.
}

// jumbomojiLimit is the largest number of emoji Slack still enlarges when a
// message is nothing else.
const jumbomojiLimit = 23

// jumbomoji reports whether a message body is only emoji codes — at most
// jumbomojiLimit of them, each one a standard or workspace emoji — which Slack
// displays enlarged.
func jumbomoji(text string, customEmoji map[string]string) bool {
	text = strings.TrimSpace(text)
	count := 0
	for text != "" {
		if text[0] == ' ' || text[0] == '\n' || text[0] == '\t' {
			text = text[1:]
			continue
		}
		if text[0] != ':' {
			return false
		}
		end := strings.IndexByte(text[1:], ':')
		if end < 1 {
			return false
		}
		name := text[1 : end+1]
		text = text[end+2:]
		if strings.HasPrefix(name, "skin-tone-") && count > 0 {
			continue
		}
		if _, ok := slackemoji.Lookup(name); !ok && customEmoji[strings.ToLower(name)] == "" {
			return false
		}
		count++
		if count > jumbomojiLimit {
			return false
		}
	}
	return count > 0
}

// mentionsViewer reports whether a message's source text mentions the viewer
// directly or through @here, @channel or @everyone — the messages Slack
// highlights in the timeline.
func mentionsViewer(text, viewer string) bool {
	if viewer != "" && (strings.Contains(text, "<@"+viewer+">") || strings.Contains(text, "<@"+viewer+"|")) {
		return true
	}
	for _, broadcast := range []string{"<!here", "<!channel", "<!everyone"} {
		if strings.Contains(text, broadcast+">") || strings.Contains(text, broadcast+"|") {
			return true
		}
	}
	return false
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
	case "usergroup":
		handle := strings.TrimSpace(stringValue(element["display_handle"]))
		if handle == "" {
			handle = stringValue(element["usergroup_id"])
		}
		output.WriteString(`<span class="slack-mention">@`)
		output.WriteString(html.EscapeString(handle))
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
