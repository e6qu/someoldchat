package web

import (
	"strings"
	"testing"
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

// A fold that changes the byte length destroys the mapping from a match back to
// a span of the original, and the characters where that happens are ones a
// mis-placed span would corrupt rather than merely misplace. Marking is
// decoration and the text is not, so the decoration is dropped.
func TestMarkingIsSkippedWhenFoldingChangesLength(t *testing.T) {
	text := "İstanbul"
	marked := markTerms(text, []string{"stanbul"})
	if strings.Contains(marked, "<mark>") {
		t.Fatalf("marked = %q, want no marking where the fold changes length", marked)
	}
	if marked != text {
		t.Fatalf("marked = %q, want the text intact", marked)
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
