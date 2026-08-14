package web

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// tableHeaderView is one column header of the table view: its name, the link
// that sorts by it, and whether the table is currently sorted by it and which
// way, so the header can show a direction arrow and toggle on the next click.
type tableHeaderView struct {
	Name   string
	URL    string
	Sorted string // "asc", "desc", or ""
}

// listSortColumn resolves which column a table is sorted by: the requested one
// when it names a real column, otherwise the primary column that names each
// item, so an absent or stale sort key falls back to the list's own spine rather
// than to nothing.
func listSortColumn(columns []domain.ListColumn, requested string) domain.ListColumn {
	for _, column := range columns {
		if column.Key == requested {
			return column
		}
	}
	for _, column := range columns {
		if column.Primary {
			return column
		}
	}
	if len(columns) > 0 {
		return columns[0]
	}
	return domain.ListColumn{}
}

// buildTableHeaders builds the sortable column headers. The header for the
// column in effect links to the opposite direction so a click reverses it;
// every other header links to ascending, the way a fresh sort starts.
func buildTableHeaders(columns []domain.ListColumn, listPath, sortKey string, desc bool) []tableHeaderView {
	headers := make([]tableHeaderView, 0, len(columns))
	for _, column := range columns {
		nextDir := "asc"
		sorted := ""
		if column.Key == sortKey {
			if desc {
				sorted = "desc"
			} else {
				sorted = "asc"
				nextDir = "desc"
			}
		}
		headers = append(headers, tableHeaderView{
			Name:   column.Name,
			URL:    listPath + "?view=table&sort=" + url.QueryEscape(column.Key) + "&dir=" + nextDir,
			Sorted: sorted,
		})
	}
	return headers
}

// sortListItemsByColumn orders items by their value under the sort column,
// keeping the list's own order for ties (a stable sort) and always sinking items
// with no value to the bottom, in both directions — an empty cell is not a value
// that should sort ahead of a real one just because the order reversed.
// values[i] is the sort value of items[i].
func sortListItemsByColumn(items []listItemView, values []string, column domain.ListColumn, desc bool) {
	type pair struct {
		item  listItemView
		value string
	}
	pairs := make([]pair, len(items))
	for i := range items {
		pairs[i] = pair{item: items[i], value: values[i]}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		a, b := pairs[i].value, pairs[j].value
		if (a == "") != (b == "") {
			return b == "" // the empty one sinks
		}
		if a == "" {
			return false
		}
		if desc {
			a, b = b, a
		}
		return listValueLess(a, b, column.Type)
	})
	for i := range pairs {
		items[i] = pairs[i].item
	}
}

// listValueLess compares two non-empty cell values the way their column reads:
// numbers numerically, a checkbox unchecked-before-checked, and everything else
// (text, select, person, and ISO dates whose lexical order is chronological) as
// case-folded text.
func listValueLess(a, b string, columnType domain.ListColumnType) bool {
	if columnType == domain.ListColumnNumber {
		fa, ea := strconv.ParseFloat(a, 64)
		fb, eb := strconv.ParseFloat(b, 64)
		if ea == nil && eb == nil {
			return fa < fb
		}
	}
	if columnType == domain.ListColumnCheckbox {
		return a == "false" && b == "true"
	}
	return strings.ToLower(a) < strings.ToLower(b)
}

// listViewItemCap bounds how many items a whole-list view (board or table) reads. A board has
// to group the whole list to be honest — a lane that stops at a cursor-page
// boundary hides work — so the page loads up to this many items rather than one
// page. Real lists sit far below it; a list larger than this reports itself
// truncated rather than pretending its lanes are complete.
const listViewItemCap = 1000

// listLaneView is one column of a board: a group value and the items that carry
// it, kept in the list's own row order.
type listLaneView struct {
	Label string
	Items []listItemView
	Count int
}

// groupChoiceView is one option in the board's "group by" control: a column the
// board can group into lanes, with the link that switches to grouping by it.
type groupChoiceView struct {
	Name     string
	URL      string
	Selected bool
}

// listGroupableColumns returns the columns a board can group into lanes. Slack's
// board groups by a status-like field, so this is the select and checkbox
// columns — the ones with a small, closed set of values a lane can stand for. A
// text, number, date, or person column is open-ended and would make one lane per
// value, which is a table, not a board.
func listGroupableColumns(columns []domain.ListColumn) []domain.ListColumn {
	groupable := make([]domain.ListColumn, 0, len(columns))
	for _, column := range columns {
		switch column.Type {
		case domain.ListColumnSelect, domain.ListColumnCheckbox:
			groupable = append(groupable, column)
		}
	}
	return groupable
}

// listItemGroupValue is an item's value under the group column, rendered as the
// same text the cell shows, so a lane and a cell never disagree about what an
// item says.
func listItemGroupValue(fields, key string) string {
	cells, err := domain.ParseListFields(fields)
	if err != nil {
		return ""
	}
	for _, cell := range cells {
		if strings.TrimSpace(cell.ColumnID) == key {
			return strings.TrimSpace(domain.ListCellText(cell.Value))
		}
	}
	return ""
}

// buildListLanes groups items into board lanes by the group column. Lane order
// is the column's own order — a select's declared options, then a trailing lane
// for items that have no value; a checkbox's unchecked lane before its checked
// one — so the board reads the same way every time rather than in map order.
// values[i] is the group value of items[i].
func buildListLanes(group domain.ListColumn, items []listItemView, values []string) []listLaneView {
	byValue := map[string][]listItemView{}
	for index, item := range items {
		byValue[values[index]] = append(byValue[values[index]], item)
	}
	lane := func(label, key string) listLaneView {
		return listLaneView{Label: label, Items: byValue[key], Count: len(byValue[key])}
	}
	if group.Type == domain.ListColumnCheckbox {
		unchecked := lane("Unchecked", "false")
		// A checkbox that was never set has no value and reads as unchecked.
		if empties := byValue[""]; len(empties) > 0 {
			unchecked.Items = append(append([]listItemView{}, empties...), unchecked.Items...)
			unchecked.Count = len(unchecked.Items)
		}
		return []listLaneView{unchecked, lane("Checked", "true")}
	}
	lanes := make([]listLaneView, 0, len(group.Options)+1)
	for _, option := range group.Options {
		lanes = append(lanes, lane(option, option))
	}
	if empties := byValue[""]; len(empties) > 0 {
		lanes = append(lanes, listLaneView{Label: "No " + group.Name, Items: empties, Count: len(empties)})
	}
	return lanes
}

// loadAllListItems reads every item of a list up to listViewItemCap, paging
// the cursor the ordinary list view turns into a "more" link. It returns whether
// the list ran past the cap so the board can say its lanes stop short rather than
// look complete.
func (h Handler) loadAllListItems(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ListID) ([]domain.ListItem, bool, error) {
	items := make([]domain.ListItem, 0, 128)
	cursor := domain.Cursor("")
	for {
		page, err := h.Messages.ListItems(ctx, workspace, user, id, domain.PageRequest{Limit: 100, Cursor: cursor}, true)
		if err != nil {
			return nil, false, err
		}
		items = append(items, page.Items...)
		if len(items) >= listViewItemCap {
			return items[:listViewItemCap], true, nil
		}
		if !page.HasMore || page.NextCursor == "" {
			return items, false, nil
		}
		cursor = page.NextCursor
	}
}
