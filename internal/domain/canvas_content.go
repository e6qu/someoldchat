package domain

import (
	"encoding/json"
	"html"
	"strconv"
	"strings"
)

// CanvasContentFormat is the representation canvases.getContent returns a
// canvas in. Slack documents exactly two, and markdown is the default.
type CanvasContentFormat string

const (
	CanvasContentMarkdown CanvasContentFormat = "markdown"
	CanvasContentHTML     CanvasContentFormat = "html"
)

// ParseCanvasContentFormat reads the content_type argument. An absent value is
// the documented default; any value other than the two acceptable ones is
// refused rather than guessed at.
func ParseCanvasContentFormat(raw string) (CanvasContentFormat, bool) {
	switch CanvasContentFormat(strings.TrimSpace(raw)) {
	case "", CanvasContentMarkdown:
		return CanvasContentMarkdown, true
	case CanvasContentHTML:
		return CanvasContentHTML, true
	}
	return "", false
}

// canvasHeadingLevel is 1, 2 or 3 for the heading kinds a markdown document
// produces (CanvasMarkdownBlocks), and 0 for everything else.
func canvasHeadingLevel(kind CanvasSectionType) int {
	switch kind {
	case CanvasSectionHeading1:
		return 1
	case CanvasSectionHeading2:
		return 2
	case CanvasSectionHeading3:
		return 3
	}
	return 0
}

func decodeCanvasDocument(content string) (CanvasDocument, error) {
	var document CanvasDocument
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		return CanvasDocument{}, ErrInvalidCanvas
	}
	return document, nil
}

// CanvasDocumentSections is a stored canvas body's sections, in order.
func CanvasDocumentSections(content string) ([]CanvasSection, error) {
	document, err := decodeCanvasDocument(content)
	return document.Sections, err
}

// CanvasDocumentMarkdown writes a stored canvas body back out as the markdown
// canvases.create and canvases.edit accept: a heading section as its "#"
// line, every other section as its text, sections separated by a blank line.
// Reading that markdown back through CanvasMarkdownBlocks yields the same
// sections, which is what lets a caller read a canvas, change the text and
// write it again.
func CanvasDocumentMarkdown(content string) (string, error) {
	document, err := decodeCanvasDocument(content)
	if err != nil {
		return "", err
	}
	return CanvasSectionsMarkdown(document.Sections), nil
}

// CanvasMarkdownLimit is the longest a canvas's markdown may be, in bytes.
const CanvasMarkdownLimit = 400000

// CanvasTextStateLimit bounds a canvas's stored collaborative text
// (Canvas.TextState), which besides the markdown names every deleted
// character still holding a place.
const CanvasTextStateLimit = 8 << 20

// CanvasSectionsMarkdown is CanvasDocumentMarkdown for sections in hand.
func CanvasSectionsMarkdown(sections []CanvasSection) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		text := strings.Trim(strings.ReplaceAll(section.Text, "\r\n", "\n"), "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if level := canvasHeadingLevel(section.Type); level > 0 {
			text = strings.Repeat("#", level) + " " + text
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// CanvasDocumentHTML renders a stored canvas body as one HTML fragment: a
// heading section as its own heading level, every other section through
// CanvasMarkdownHTML, which is the rendering the product's reading view uses.
func CanvasDocumentHTML(content string, style CanvasHTMLStyle) (string, error) {
	document, err := decodeCanvasDocument(content)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, section := range document.Sections {
		if strings.TrimSpace(section.Text) == "" {
			continue
		}
		if level := canvasHeadingLevel(section.Type); level > 0 {
			tag := "h" + strconv.Itoa(level)
			out.WriteString("<" + tag + ">" + html.EscapeString(strings.TrimSpace(section.Text)) + "</" + tag + ">")
			continue
		}
		out.WriteString(CanvasMarkdownHTML(section.Text, style))
	}
	return out.String(), nil
}
