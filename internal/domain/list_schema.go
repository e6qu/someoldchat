package domain

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// A list's schema is what turns it from a checklist into a list.
//
// Slack's lists carry typed columns — a status you pick from a set, a date, a
// number — and an item is a row under them. Until a schema is declared and
// enforced, the cells are free-form JSON that every reader has to guess at, and
// the product cannot answer the questions the columns exist for: what is this
// item's status, is it a number I can total, is that date real.
//
// The schema is stored as the JSON a list was created with, so an app that
// authored one keeps exactly what it wrote. Everything below reads that JSON;
// nothing rewrites it.

// errInvalidListSchema refuses a schema or an item that cannot mean what it
// claims. One sentinel rather than several, because every one of them is the
// same answer to the member: this list does not work that way.
//
// It is deliberately unexported. The service translates it into ErrInvalidList
// before anything else sees it, so a caller has one answer to handle rather
// than two that mean the same thing — and a sentinel that never crosses the
// seam does not need a classification for a boundary it never reaches.
var errInvalidListSchema = errors.New("list schema is invalid")

// ListColumnType is the closed set of column kinds a list may declare. It is
// closed because each kind is a promise about what a cell contains, and a kind
// nothing validates is a promise nobody keeps.
type ListColumnType string

const (
	ListColumnText     ListColumnType = "text"
	ListColumnNumber   ListColumnType = "number"
	ListColumnDate     ListColumnType = "date"
	ListColumnSelect   ListColumnType = "select"
	ListColumnCheckbox ListColumnType = "checkbox"
	ListColumnPerson   ListColumnType = "person"
)

func (t ListColumnType) Valid() bool {
	switch t {
	case ListColumnText, ListColumnNumber, ListColumnDate, ListColumnSelect, ListColumnCheckbox, ListColumnPerson:
		return true
	}
	return false
}

// ListColumn is one declared column.
//
// The identifier is `key` because that is what this product has always written
// and what an app authoring a schema through the API sends; `id` is accepted as
// well so a schema written either way keeps working, and the one that is
// present wins. Items reference a column by `column_id`, which is Slack's own
// asymmetry rather than ours.
type ListColumn struct {
	Key     string         `json:"key,omitempty"`
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name"`
	Type    ListColumnType `json:"type"`
	Primary bool           `json:"is_primary_column,omitempty"`
	// Options are the permitted values of a select column. They are required
	// for one, because a select with no options is a text column that refuses
	// every value.
	Options []string `json:"options,omitempty"`
}

// ListColumnLimit bounds a schema. A list with more columns than a screen can
// show is a spreadsheet, and Slack's own lists are not one.
const ListColumnLimit = 30

// ParseListSchema reads a stored schema. An empty schema is valid and means an
// unstructured list, which is what every list created before columns existed
// is: enforcing columns on those retroactively would invalidate their items.
func ParseListSchema(raw string) ([]ListColumn, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var columns []ListColumn
	if err := json.Unmarshal([]byte(raw), &columns); err != nil {
		return nil, errInvalidListSchema
	}
	if len(columns) > ListColumnLimit {
		return nil, errInvalidListSchema
	}
	seen := make(map[string]struct{}, len(columns))
	for index := range columns {
		column := &columns[index]
		column.Key = strings.TrimSpace(column.Key)
		column.ID = strings.TrimSpace(column.ID)
		if column.Key == "" {
			column.Key = column.ID
		}
		column.Name = strings.TrimSpace(column.Name)
		if column.Key == "" || column.Name == "" || !column.Type.Valid() {
			return nil, errInvalidListSchema
		}
		if _, repeated := seen[column.Key]; repeated {
			return nil, errInvalidListSchema
		}
		seen[column.Key] = struct{}{}
		if column.Type == ListColumnSelect && len(column.Options) == 0 {
			return nil, errInvalidListSchema
		}
	}
	return columns, nil
}

// ListSchemaIsStructured reports whether a schema declares a structure worth
// enforcing.
//
// A list created without a schema is given a single primary text column so the
// client has something to show, and that substitution is the product's
// convenience rather than the member's declaration. Enforcing it would forbid
// columns nobody said they did not want, and would break every list used as a
// free-form checklist — which is what lists were before columns existed. One
// column is not a structure; two or more, or one that is not a plain text
// primary, is somebody having said what this list is for.
func ListSchemaIsStructured(columns []ListColumn) bool {
	if len(columns) == 0 {
		return false
	}
	if len(columns) == 1 && columns[0].Primary && columns[0].Type == ListColumnText {
		return false
	}
	return true
}

// ListCell is one value in one item.
type ListCell struct {
	ColumnID string `json:"column_id"`
	Value    any    `json:"value"`
}

// ParseListFields reads an item's stored cells.
func ParseListFields(raw string) ([]ListCell, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var cells []ListCell
	if err := json.Unmarshal([]byte(raw), &cells); err != nil {
		return nil, errInvalidListSchema
	}
	return cells, nil
}

// ValidateListFields checks an item against its list's columns.
//
// A list with no schema accepts anything, which is what an unstructured list
// is. A list with one refuses a cell naming a column it does not have — a value
// under a column nobody declared is invisible to every reader of the list, so
// accepting it silently loses the member's work — and refuses a value the
// column's type cannot mean.
//
// A missing cell is not an error. A row with a blank status is an ordinary row,
// and demanding every column would make a list unusable exactly when it is most
// useful: while it is being filled in.
func ValidateListFields(schema string, fields string) error {
	columns, err := ParseListSchema(schema)
	if err != nil {
		return err
	}
	cells, err := ParseListFields(fields)
	if err != nil {
		return err
	}
	if !ListSchemaIsStructured(columns) {
		return nil
	}
	declared := make(map[string]ListColumn, len(columns))
	for _, column := range columns {
		declared[column.Key] = column
	}
	for _, cell := range cells {
		column, known := declared[strings.TrimSpace(cell.ColumnID)]
		if !known {
			return errInvalidListSchema
		}
		if !columnAccepts(column, cell.Value) {
			return errInvalidListSchema
		}
	}
	return nil
}

// columnAccepts decides whether one value can mean what its column promises.
// An absent value always can: clearing a cell is how a member corrects one.
func columnAccepts(column ListColumn, value any) bool {
	if value == nil {
		return true
	}
	text := strings.TrimSpace(ListCellText(value))
	if text == "" {
		return true
	}
	switch column.Type {
	case ListColumnNumber:
		_, err := strconv.ParseFloat(text, 64)
		return err == nil
	case ListColumnDate:
		// A date is a calendar date. Accepting an instant here would let two
		// members write the same day differently and sort wrongly.
		_, err := time.Parse("2006-01-02", text)
		return err == nil
	case ListColumnCheckbox:
		return text == "true" || text == "false"
	case ListColumnSelect:
		for _, option := range column.Options {
			if option == text {
				return true
			}
		}
		return false
	}
	return true
}

// ListCellText renders a stored cell value for display and comparison. JSON
// numbers arrive as float64, and formatting them with %v gives "1e+06" for a
// million, which is not what anybody typed.
func ListCellText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
