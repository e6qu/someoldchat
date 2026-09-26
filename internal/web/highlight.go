package web

import (
	"html"
	"strings"
	"unicode"
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
	lowered, origins := foldWithOrigins(text)
	spans := matchedSpans(lowered, terms)
	if len(spans) == 0 {
		return html.EscapeString(text)
	}
	var output strings.Builder
	cursor := 0
	for _, span := range spans {
		// Each span is mapped back through the rune it came from, never used
		// as an offset into text directly: folding changes the byte length of
		// some runes (U+0130 shrinks, U+023A grows), so a folded offset is not
		// an original one even when the two strings happen to be the same
		// length overall — the case the old length check let through, which
		// cut a character in half.
		start, end := origins[span.start], origins[span.end]
		output.WriteString(html.EscapeString(text[cursor:start]))
		output.WriteString("<mark>")
		output.WriteString(html.EscapeString(text[start:end]))
		output.WriteString("</mark>")
		cursor = end
	}
	output.WriteString(html.EscapeString(text[cursor:]))
	return output.String()
}

// foldWithOrigins lowercases text exactly as strings.ToLower does — one rune
// at a time through unicode.ToLower — and records, for every byte of the folded
// string and for its end, the byte offset in text of the rune that produced it.
// A match in the folded string starts and ends on rune boundaries, so origins
// maps both ends to rune boundaries of the original.
func foldWithOrigins(text string) (string, []int) {
	var folded strings.Builder
	folded.Grow(len(text))
	origins := make([]int, 0, len(text)+1)
	for offset, r := range text {
		before := folded.Len()
		folded.WriteRune(unicode.ToLower(r))
		for range folded.Len() - before {
			origins = append(origins, offset)
		}
	}
	origins = append(origins, len(text))
	return folded.String(), origins
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
