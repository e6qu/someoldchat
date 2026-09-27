package domain

import "strings"

// CanvasMarkdownBlocks splits markdown document content into the sections a
// canvas is made of, the way Slack's canvases.create and canvases.edit treat
// a markdown document: every "#", "##" or "###" heading becomes a header
// section of that level (so canvases.sections.lookup can find it by type),
// and the prose between headings becomes markdown sections, one per block of
// text separated by a blank line. A list, a checklist, a quote or a fenced
// code block stays one section, because splitting it would turn one list into
// several. Without this, a document such as "# Runbook\n\n- Freeze\n- Deploy"
// was stored as a single paragraph showing its "#" and "-" characters.
//
// Text with no heading and no blank line is returned as one markdown block,
// unchanged, so a single paragraph round-trips exactly.
func CanvasMarkdownBlocks(text string) []CanvasSection {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	var blocks []CanvasSection
	var paragraph []string
	flush := func() {
		body := strings.Trim(strings.Join(paragraph, "\n"), "\n")
		if strings.TrimSpace(body) != "" {
			blocks = append(blocks, CanvasSection{Type: CanvasSectionMarkdown, Text: body})
		}
		paragraph = paragraph[:0]
	}
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				flush()
			}
			inFence = !inFence
			paragraph = append(paragraph, line)
			if !inFence {
				flush()
			}
			continue
		}
		if inFence {
			paragraph = append(paragraph, line)
			continue
		}
		if level, heading, ok := markdownHeading(trimmed); ok {
			flush()
			blocks = append(blocks, CanvasSection{Type: CanvasSectionType("h" + string(rune('0'+level))), Text: heading})
			continue
		}
		if trimmed == "" {
			// A blank line inside a list keeps the list together: the next
			// line decides, so only a blank line followed by non-list text
			// ends the block. The paragraph is flushed lazily below.
			if len(paragraph) > 0 {
				paragraph = append(paragraph, "")
			}
			continue
		}
		if len(paragraph) > 0 && paragraph[len(paragraph)-1] == "" && !(markdownListLine(trimmed) && markdownListLine(strings.TrimSpace(lastNonEmpty(paragraph)))) {
			flush()
		}
		paragraph = append(paragraph, line)
	}
	flush()
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

func lastNonEmpty(lines []string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) != "" {
			return lines[index]
		}
	}
	return ""
}

// markdownHeading recognises an ATX heading of level one to three. Deeper
// levels are prose, as they are in a Slack canvas.
func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 3 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	heading := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[level:]), "#"))
	if heading == "" {
		return 0, "", false
	}
	return level, heading, true
}

// markdownListLine reports whether a line is a bullet, numbered or checklist
// item.
func markdownListLine(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	return digits > 0 && digits+1 < len(line) && (line[digits] == '.' || line[digits] == ')') && line[digits+1] == ' '
}
