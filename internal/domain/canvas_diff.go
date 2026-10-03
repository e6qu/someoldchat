package domain

// MergeCanvasSections lays a document's new sections over its current ones.
// A section whose kind and text are unchanged keeps its identifier, so the
// comments anchored to it stay with it; every other new section has no
// identifier yet, for the caller to mint. changed counts the sections that
// differ: within each run of edits between unchanged sections, the larger of
// the sections removed and the sections added, so rewriting one paragraph is
// one change and inserting one is one change.
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
	removed, added := 0, 0
	settle := func() {
		changed += max(removed, added)
		removed, added = 0, 0
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
			merged = append(merged, section)
			added++
			j++
		default:
			removed++
			i++
		}
	}
	settle()
	return merged, changed
}
