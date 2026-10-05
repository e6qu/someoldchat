package domain

import (
	"testing"
	"time"
)

func TestMergeCanvasSectionsKeepsUnchangedSectionsAndCountsEdits(t *testing.T) {
	section := func(id, text string) CanvasSection {
		return CanvasSection{ID: id, Type: CanvasSectionMarkdown, Text: text}
	}
	current := []CanvasSection{{ID: "h", Type: CanvasSectionHeading1, Text: "Plan"}, section("a", "one"), section("b", "two"), section("c", "three")}
	for _, test := range []struct {
		name    string
		next    []CanvasSection
		ids     []string
		changed int
	}{
		{"unchanged", []CanvasSection{{Type: CanvasSectionHeading1, Text: "Plan"}, section("", "one"), section("", "two"), section("", "three")}, []string{"h", "a", "b", "c"}, 0},
		{"one rewritten", []CanvasSection{{Type: CanvasSectionHeading1, Text: "Plan"}, section("", "one"), section("", "TWO"), section("", "three")}, []string{"h", "a", "", "c"}, 1},
		{"one inserted", []CanvasSection{{Type: CanvasSectionHeading1, Text: "Plan"}, section("", "zero"), section("", "one"), section("", "two"), section("", "three")}, []string{"h", "", "a", "b", "c"}, 1},
		{"one removed", []CanvasSection{{Type: CanvasSectionHeading1, Text: "Plan"}, section("", "one"), section("", "three")}, []string{"h", "a", "c"}, 1},
		{"a heading became prose", []CanvasSection{section("", "Plan"), section("", "one"), section("", "two"), section("", "three")}, []string{"", "a", "b", "c"}, 1},
		{"emptied", nil, nil, 4},
		// A moved paragraph is the same paragraph, so it keeps its comments.
		{"one moved", []CanvasSection{{Type: CanvasSectionHeading1, Text: "Plan"}, section("", "two"), section("", "three"), section("", "one")}, []string{"h", "b", "c", "a"}, 2},
	} {
		merged, changed := MergeCanvasSections(current, test.next)
		if changed != test.changed || len(merged) != len(test.ids) {
			t.Errorf("%s: changed=%d merged=%+v, want %d and %d sections", test.name, changed, merged, test.changed, len(test.ids))
			continue
		}
		for index, id := range test.ids {
			if merged[index].ID != id || merged[index].Text != test.next[index].Text {
				t.Errorf("%s: section %d=%+v, want id %q text %q", test.name, index, merged[index], id, test.next[index].Text)
			}
		}
	}
}

// Markdown cannot spell an app's section kind, so a section an app wrote comes
// back from markdown as prose. Rewritten one for one, it keeps the app's kind.
func TestMergeCanvasSectionsKeepsAnAppKindThroughAnEdit(t *testing.T) {
	current := []CanvasSection{{ID: "a", Type: "rich_text", Text: "Summary"}, {ID: "b", Type: CanvasSectionMarkdown, Text: "Body"}}
	merged, changed := MergeCanvasSections(current, []CanvasSection{{Type: CanvasSectionMarkdown, Text: "Revised summary"}, {Type: CanvasSectionMarkdown, Text: "Body"}})
	if changed != 1 || merged[0].Type != "rich_text" || merged[0].ID != "" || merged[1].ID != "b" {
		t.Fatalf("merged=%+v changed=%d", merged, changed)
	}
	// A section that only became prose by being written as a heading is
	// another matter: markdown can spell headings, so the writer meant it.
	merged, _ = MergeCanvasSections([]CanvasSection{{ID: "h", Type: CanvasSectionHeading1, Text: "Plan"}}, []CanvasSection{{Type: CanvasSectionMarkdown, Text: "Plan, as prose"}})
	if merged[0].Type != CanvasSectionMarkdown {
		t.Fatalf("a heading rewritten as prose kept its heading kind: %+v", merged)
	}
}

// A writer's run of ordinary edits keeps one revision; another writer, a
// restore, or the run outlasting the window keeps one of its own.
func TestCanvasRevisionGrouping(t *testing.T) {
	made := time.Unix(1_700_000_000, 0)
	for _, test := range []struct {
		name    string
		topic   string
		actor   UserID
		after   time.Duration
		grouped bool
	}{
		{"the same writer moments later", "canvas.updated", "U1", time.Minute, true},
		{"another writer", "canvas.updated", "U2", time.Minute, false},
		{"a restore", "canvas.restored", "U1", time.Minute, false},
		{"past the window", "canvas.updated", "U1", CanvasRevisionGroupWindow, false},
		{"no actor", "canvas.updated", "", time.Minute, false},
	} {
		if got := CanvasRevisionGrouped(test.topic, test.actor, made.Add(test.after), "U1", made); got != test.grouped {
			t.Errorf("%s: grouped=%v, want %v", test.name, got, test.grouped)
		}
	}
}
