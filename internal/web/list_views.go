package web

import (
	"context"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// boardItemCap bounds how many items a board view groups into lanes. A board has
// to group the whole list to be honest — a lane that stops at a cursor-page
// boundary hides work — so the page loads up to this many items rather than one
// page. Real lists sit far below it; a list larger than this reports itself
// truncated rather than pretending its lanes are complete.
const boardItemCap = 1000

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

// loadListItemsForBoard reads every item of a list up to boardItemCap, paging
// the cursor the ordinary list view turns into a "more" link. It returns whether
// the list ran past the cap so the board can say its lanes stop short rather than
// look complete.
func (h Handler) loadListItemsForBoard(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ListID) ([]domain.ListItem, bool, error) {
	items := make([]domain.ListItem, 0, 128)
	cursor := domain.Cursor("")
	for {
		page, err := h.Messages.ListItems(ctx, workspace, user, id, domain.PageRequest{Limit: 100, Cursor: cursor}, true)
		if err != nil {
			return nil, false, err
		}
		items = append(items, page.Items...)
		if len(items) >= boardItemCap {
			return items[:boardItemCap], true, nil
		}
		if !page.HasMore || page.NextCursor == "" {
			return items, false, nil
		}
		cursor = page.NextCursor
	}
}
