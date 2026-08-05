package web

import (
	"html"
	"strings"
)

// Marking what matched.
//
// Slack emphasises the terms you searched for inside every result, and this is
// the only part of search where the rendering and the security boundary meet:
// the message body is already HTML by the time a result is drawn, and inserting
// <mark> into finished HTML with a string replace would put tags inside
// attributes, split entities, and mark the "a" in <a href>. So marking is not
// applied to rendered output at all. It is threaded into the one place the
// markup renderer emits literal prose, and applied to plain fields directly.
//
// The consequence is worth stating because it is visible: a term that spans a
// formatting boundary — the "old" in *bold* — is not marked. Marking it would
// mean reasoning about the tags between the halves, which is the thing this
// design exists to avoid.

// markTerms escapes text and wraps each occurrence of a term in <mark>. The
// input is raw text and the output is HTML, so every byte that is not a tag this
// function itself emits goes through html.EscapeString.
func markTerms(text string, terms []string) string {
	if len(terms) == 0 || text == "" {
		return html.EscapeString(text)
	}
	lowered := strings.ToLower(text)
	// A fold that changes the byte length destroys the mapping from a match in
	// the folded string back to a span of the original, and the Unicode cases
	// where it happens (U+0130 folds to two runes) are exactly the ones where a
	// wrong span would corrupt a character rather than merely misplace an
	// emphasis. Marking is decoration; the text is not. Skip the decoration.
	if len(lowered) != len(text) {
		return html.EscapeString(text)
	}
	spans := matchedSpans(lowered, terms)
	if len(spans) == 0 {
		return html.EscapeString(text)
	}
	var output strings.Builder
	cursor := 0
	for _, span := range spans {
		output.WriteString(html.EscapeString(text[cursor:span.start]))
		output.WriteString("<mark>")
		output.WriteString(html.EscapeString(text[span.start:span.end]))
		output.WriteString("</mark>")
		cursor = span.end
	}
	output.WriteString(html.EscapeString(text[cursor:]))
	return output.String()
}

type textSpan struct{ start, end int }

// matchedSpans returns the non-overlapping spans of lowered that any term
// covers, in order. Overlapping matches are merged rather than nested, because
// two <mark> elements inside one another render as one emphasis with two
// boundaries and read as a rendering fault.
func matchedSpans(lowered string, terms []string) []textSpan {
	spans := make([]textSpan, 0, 4)
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		for offset := 0; ; {
			index := strings.Index(lowered[offset:], term)
			if index < 0 {
				break
			}
			start := offset + index
			spans = append(spans, textSpan{start: start, end: start + len(term)})
			offset = start + len(term)
		}
	}
	if len(spans) < 2 {
		return spans
	}
	sortSpans(spans)
	merged := spans[:1]
	for _, span := range spans[1:] {
		last := &merged[len(merged)-1]
		if span.start <= last.end {
			if span.end > last.end {
				last.end = span.end
			}
			continue
		}
		merged = append(merged, span)
	}
	return merged
}

func sortSpans(spans []textSpan) {
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start < spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
}
