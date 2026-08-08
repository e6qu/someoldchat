package domain

import "testing"

// TestCanvasSectionTypeRules holds the two rules the type names. They were
// inline string tests in three places, and the header rule is a prefix rather
// than an enumeration: Slack numbers headings upward, so an enumeration would
// stop being true at h4.
func TestCanvasSectionTypeRules(t *testing.T) {
	for _, testCase := range []struct {
		kind     CanvasSectionType
		header   bool
		editable bool
	}{
		{CanvasSectionMarkdown, false, true},
		{CanvasSectionHeading1, true, false},
		{CanvasSectionHeading2, true, false},
		{CanvasSectionHeading3, true, false},
		{CanvasSectionType("h4"), true, false},
		{CanvasSectionType(""), false, true},
		{CanvasSectionType("table"), false, false},
	} {
		if got := testCase.kind.IsHeader(); got != testCase.header {
			t.Errorf("%q IsHeader=%t, want %t", testCase.kind, got, testCase.header)
		}
		if got := testCase.kind.Editable(); got != testCase.editable {
			t.Errorf("%q Editable=%t, want %t", testCase.kind, got, testCase.editable)
		}
	}
}
