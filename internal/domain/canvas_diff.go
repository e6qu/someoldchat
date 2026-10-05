package domain

// MergeCanvasSections lays a document's new sections over its current ones.
// A section whose kind and text are unchanged keeps its identifier, so the
// comments anchored to it stay with it; every other new section has no
// identifier yet, for the caller to mint. changed counts the sections that
// differ: within each run of edits between unchanged sections, the larger of
// the sections removed and the sections added, so rewriting one paragraph is
// one change and inserting one is one change.
//
// Markdown cannot spell a section kind an app wrote (canvases.create takes any
// kind), so a document rewritten through markdown brings such a section back as
// prose. When an edit replaces sections one for one, the k-th new section of a
// run takes the place of the k-th removed one, and a prose section standing in
// for an app's section keeps the app's kind: editing its text does not turn it
// into a paragraph.
//
// A section moved elsewhere in the document is not in the common subsequence,
// so the walk sees it removed in one place and added in another. It is the
// same section, and it keeps its identifier: the comments on a paragraph stay
// with it when the paragraph is moved. The move still counts as a change.
func MergeCanvasSections(current, next []CanvasSection) (merged []CanvasSection, changed int) {
	same := func(left, right CanvasSection) bool { return left.Type == right.Type && left.Text == right.Text }
	// lengths[i][j] is the longest common subsequence of current[i:] and
	// next[j:].
	lengths := make([][]int, len(current)+1)
	for index := range lengths {
		lengths[index] = make([]int, len(next)+1)
	}
	for i := len(current) - 1; i >= 0; i-- {
		for j := len(next) - 1; j >= 0; j-- {
			if same(current[i], next[j]) {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}
	merged = make([]CanvasSection, 0, len(next))
	var removed []CanvasSection
	var added []int
	settle := func() {
		changed += max(len(removed), len(added))
		for k := 0; k < len(removed) && k < len(added); k++ {
			kind := removed[k].Type
			if merged[added[k]].Type == CanvasSectionMarkdown && !kind.Markdown() {
				merged[added[k]].Type = kind
			}
		}
		removed, added = removed[:0], added[:0]
	}
	i, j := 0, 0
	for i < len(current) || j < len(next) {
		switch {
		case i < len(current) && j < len(next) && same(current[i], next[j]):
			settle()
			merged = append(merged, current[i])
			i, j = i+1, j+1
		case j < len(next) && (i == len(current) || lengths[i][j+1] >= lengths[i+1][j]):
			section := next[j]
			section.ID = ""
			added = append(added, len(merged))
			merged = append(merged, section)
			j++
		default:
			removed = append(removed, current[i])
			i++
		}
	}
	settle()
	moved := make(map[CanvasSection][]string)
	kept := make(map[string]bool, len(merged))
	for _, section := range merged {
		kept[section.ID] = section.ID != ""
	}
	for _, section := range current {
		if section.ID != "" && !kept[section.ID] {
			key := CanvasSection{Type: section.Type, Text: section.Text}
			moved[key] = append(moved[key], section.ID)
		}
	}
	for index := range merged {
		key := CanvasSection{Type: merged[index].Type, Text: merged[index].Text}
		if ids := moved[key]; merged[index].ID == "" && len(ids) > 0 {
			merged[index].ID, moved[key] = ids[0], ids[1:]
		}
	}
	return merged, changed
}
