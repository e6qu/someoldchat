package domain

import (
	"reflect"
	"testing"
)

func TestCanvasMarkdownBlocksSplitsHeadingsAndKeepsListsWhole(t *testing.T) {
	got := CanvasMarkdownBlocks("# Launch runbook\n\nSteps for the launch.\n\n- Freeze\n- Deploy\n\n- Announce\n\n## Owners ##\nAna runs it.\n```\ncode\n\nstill code\n```\n#### not a heading")
	want := []CanvasSection{
		{Type: CanvasSectionHeading1, Text: "Launch runbook"},
		{Type: CanvasSectionMarkdown, Text: "Steps for the launch."},
		{Type: CanvasSectionMarkdown, Text: "- Freeze\n- Deploy\n\n- Announce"},
		{Type: CanvasSectionHeading2, Text: "Owners"},
		{Type: CanvasSectionMarkdown, Text: "Ana runs it."},
		{Type: CanvasSectionMarkdown, Text: "```\ncode\n\nstill code\n```"},
		{Type: CanvasSectionMarkdown, Text: "#### not a heading"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blocks =\n%#v\nwant\n%#v", got, want)
	}
}

func TestCanvasMarkdownBlocksKeepsOneParagraphExactly(t *testing.T) {
	for _, text := range []string{"notes from the launch bot", "line one\nline two"} {
		got := CanvasMarkdownBlocks(text)
		if len(got) != 1 || got[0].Text != text || got[0].Type != CanvasSectionMarkdown {
			t.Fatalf("%q -> %#v", text, got)
		}
	}
	if got := CanvasMarkdownBlocks("  \n\n "); got != nil {
		t.Fatalf("blank text -> %#v, want no blocks", got)
	}
}
