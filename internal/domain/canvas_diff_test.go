package domain

import "testing"

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
