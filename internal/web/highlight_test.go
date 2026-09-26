package web

import (
	"html"
	"strings"
	"testing"
	"unicode/utf8"
)

// The property that matters most: marking never produces markup the text did
// not already have. Everything except the <mark> tags this function emits goes
// through html.EscapeString, so a result carrying a script tag as literal text
// stays literal text however the search terms fall across it.
func TestMarkingEscapesEverythingItDidNotEmit(t *testing.T) {
	marked := markTerms(`<script>alert("x")</script> and a & b`, []string{"script", "&"})
	if strings.Contains(marked, "<script>") {
		t.Fatalf("marked output carries live markup: %q", marked)
	}
	if !strings.Contains(marked, "&lt;<mark>script</mark>&gt;") {
		t.Fatalf("marked output = %q, want the escaped tag with its term marked", marked)
	}
	// The ampersand is a term and an escapable character at once, so it proves
	// the order: escape the span, then wrap it, never the reverse.
	if !strings.Contains(marked, "<mark>&amp;</mark>") {
		t.Fatalf("marked output = %q, want the escaped ampersand marked", marked)
	}
}

// Two terms that overlap must produce one emphasis, not one inside another:
// nested <mark> renders as a single highlight with two boundaries and reads as
// a rendering fault.
func TestOverlappingTermsMergeIntoOneMark(t *testing.T) {
	marked := markTerms("deployment", []string{"deploy", "ployment"})
	if marked != "<mark>deployment</mark>" {
		t.Fatalf("marked = %q, want one merged mark", marked)
	}
	if strings.Count(marked, "<mark>") != 1 {
		t.Fatalf("marked = %q, want exactly one mark element", marked)
	}
}

func TestMarkingIsCaseInsensitiveAndSkipsAbsentTerms(t *testing.T) {
	marked := markTerms("Deployment Runbook", []string{"RUNBOOK", "absent"})
	if marked != "Deployment <mark>Runbook</mark>" {
		t.Fatalf("marked = %q, want the folded match with its original case kept", marked)
	}
	if plain := markTerms("Deployment", nil); plain != "Deployment" {
		t.Fatalf("unmarked = %q, want the text unchanged", plain)
	}
}

// Folding changes the byte length of some characters: U+0130 (İ) is two bytes
// and folds to the one-byte "i", U+023A (Ⱥ) is two and folds to three. A match
// found in the folded text is therefore mapped back rune by rune. Marking used
// to be dropped whenever the total length changed, and applied with raw folded
// offsets when a shrink and a grow cancelled out — which cut "é" in half.
func TestMarkingMapsMatchesBackThroughFoldingThatChangesLength(t *testing.T) {
	for _, tc := range []struct {
		text  string
		terms []string
		want  string
	}{
		{"İstanbul", []string{"stanbul"}, "İ<mark>stanbul</mark>"},
		{"İstanbul", []string{"istanbul"}, "<mark>İstanbul</mark>"},
		{"İstanbul café", []string{"café"}, "İstanbul <mark>café</mark>"},
		// The lengths cancel overall, and "é" sits after the grow and before
		// the shrink, where a folded offset lands one byte into the character.
		{"Ⱥé İ", []string{"é"}, "Ⱥ<mark>é</mark> İ"},
		{"Ⱥé İ", []string{"ⱥ", "i"}, "<mark>Ⱥ</mark>é <mark>İ</mark>"},
	} {
		marked := markTerms(tc.text, tc.terms)
		if marked != tc.want {
			t.Errorf("markTerms(%q, %q) = %q, want %q", tc.text, tc.terms, marked, tc.want)
		}
		if !utf8.ValidString(marked) {
			t.Errorf("markTerms(%q, %q) produced invalid UTF-8: %q", tc.text, tc.terms, marked)
		}
	}
}

// Marking a message body goes through the renderer, so it can only land in the
// one branch that emits literal prose. A term that matches a URL inside a link,
// or the name of a tag, must not produce markup inside markup.
func TestMarkingAMessageBodyCannotReachInsideATag(t *testing.T) {
	rendered := string(renderSlackMrkdwnMarking("see <https://example.test/report|the report> now", nil, []string{"https", "report", "a"}))
	if strings.Contains(rendered, `href="<mark>`) || strings.Contains(rendered, "<mark>https</mark>://") {
		t.Fatalf("marking reached inside the link: %q", rendered)
	}
	// The link's visible label is literal prose and is marked; its target is not.
	if !strings.Contains(rendered, "<mark>report</mark>") {
		t.Fatalf("rendered = %q, want the visible label marked", rendered)
	}
	if !strings.Contains(rendered, `href="https://example.test/report"`) {
		t.Fatalf("rendered = %q, want the href intact", rendered)
	}
}

// A term that spans a formatting boundary is not marked. That is a real
// limitation of marking prose rather than finished HTML, and it is asserted so
// nobody reintroduces a string replace over the rendered output to fix it.
func TestATermSplitByFormattingIsLeftUnmarked(t *testing.T) {
	rendered := string(renderSlackMrkdwnMarking("*bold*est", nil, []string{"boldest"}))
	if strings.Contains(rendered, "<mark>") {
		t.Fatalf("rendered = %q, want no mark across a formatting boundary", rendered)
	}
	if !strings.Contains(rendered, "<strong>bold</strong>est") {
		t.Fatalf("rendered = %q, want the formatting intact", rendered)
	}
}

// Whatever the text and terms, marking only adds <mark> elements: removing them
// and unescaping gives back the original text, byte for byte.
func FuzzMarkingPreservesTheText(f *testing.F) {
	for _, seed := range [][2]string{{"Ⱥé İ", "é"}, {"İstanbul", "stanbul"}, {"Ωmega K", "k"}, {"a & <b>", "&"}, {"ǅungla", "ǆ"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, text, term string) {
		if !utf8.ValidString(text) {
			t.Skip("message text is valid UTF-8 by the time it is rendered")
		}
		marked := markTerms(text, []string{term})
		if !utf8.ValidString(marked) {
			t.Fatalf("markTerms(%q, %q) produced invalid UTF-8: %q", text, term, marked)
		}
		stripped := strings.NewReplacer("<mark>", "", "</mark>", "").Replace(marked)
		if restored := html.UnescapeString(stripped); restored != text {
			t.Fatalf("markTerms(%q, %q) = %q, which does not restore the text (got %q)", text, term, marked, restored)
		}
	})
}
