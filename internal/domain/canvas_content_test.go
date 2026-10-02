package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func storedCanvas(t *testing.T, sections ...CanvasSection) string {
	t.Helper()
	encoded, err := json.Marshal(CanvasDocument{Sections: sections})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// The markdown canvases.getContent returns is the markdown canvases.create
// accepts: reading it back through the create path's splitter yields the same
// sections, kinds and text, so a caller can read, change and write again.
func TestCanvasDocumentMarkdownRoundTripsThroughTheCreateSplitter(t *testing.T) {
	source := "# Project plan\n\n- [ ] Draft spec\n- [x] Kickoff meeting\n\n## Notes\n\nShip on **Friday**.\nSecond line.\n\n```\ncode block\n```\n"
	sections := CanvasMarkdownBlocks(source)
	got, err := CanvasDocumentMarkdown(storedCanvas(t, sections...))
	if err != nil {
		t.Fatal(err)
	}
	if got != source {
		t.Fatalf("markdown =\n%q\nwant\n%q", got, source)
	}
	if again := CanvasMarkdownBlocks(got); !reflect.DeepEqual(again, sections) {
		t.Fatalf("re-read sections = %+v, want %+v", again, sections)
	}
}

// Slack's documented example: a heading and a checklist read back as the
// heading line, a blank line, and the list.
func TestCanvasDocumentMarkdownMatchesTheReferenceExample(t *testing.T) {
	got, err := CanvasDocumentMarkdown(storedCanvas(t,
		CanvasSection{ID: "s1", Type: CanvasSectionHeading1, Text: "Project plan"},
		CanvasSection{ID: "s2", Type: CanvasSectionMarkdown, Text: "- [ ] Draft spec\n- [x] Kickoff meeting"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Project plan\n\n- [ ] Draft spec\n- [x] Kickoff meeting\n"; got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
	empty, err := CanvasDocumentMarkdown(storedCanvas(t))
	if err != nil || empty != "" {
		t.Fatalf("empty canvas markdown = %q err=%v", empty, err)
	}
	if _, err := CanvasDocumentMarkdown("not json"); !errors.Is(err, ErrInvalidCanvas) {
		t.Fatalf("undecodable body err=%v, want ErrInvalidCanvas", err)
	}
}

func TestCanvasDocumentHTMLUsesTheSharedRendererAndEscapesEverything(t *testing.T) {
	got, err := CanvasDocumentHTML(storedCanvas(t,
		CanvasSection{Type: CanvasSectionHeading2, Text: "Plan <b>"},
		CanvasSection{Type: CanvasSectionMarkdown, Text: "- [ ] Draft\n- [x] Kickoff\n\nHi <@U1> and <script>x</script>"},
	), CanvasHTMLStyle{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<h2>Plan &lt;b&gt;</h2>",
		`<ul class="checklist"><li><input type="checkbox" disabled> Draft</li><li><input type="checkbox" checked disabled> Kickoff</li></ul>`,
		`<span class="mention" data-user-id="U1">@U1</span>`,
		"&lt;script&gt;x&lt;/script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("html lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script") {
		t.Fatalf("unsafe markup survived: %s", got)
	}
	styled := CanvasMarkdownHTML("<@U2>", CanvasHTMLStyle{Mention: func(id UserID) string { return "[" + string(id) + "]" }})
	if styled != "<p>[U2]</p>" {
		t.Fatalf("styled mention = %q", styled)
	}
}

func TestParseCanvasContentFormat(t *testing.T) {
	for raw, want := range map[string]CanvasContentFormat{"": CanvasContentMarkdown, "markdown": CanvasContentMarkdown, " html ": CanvasContentHTML} {
		if got, ok := ParseCanvasContentFormat(raw); !ok || got != want {
			t.Fatalf("ParseCanvasContentFormat(%q) = %q, %t", raw, got, ok)
		}
	}
	for _, raw := range []string{"HTML", "text", "pdf"} {
		if _, ok := ParseCanvasContentFormat(raw); ok {
			t.Fatalf("ParseCanvasContentFormat(%q) accepted a value the reference does not list", raw)
		}
	}
}
