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

// RemoveListColumn drops one column from a schema. It reports whether the
// column was there, so a caller can tell "removed" from "there was nothing to
// remove" rather than reporting success for a key nobody declared.
//
// The primary column cannot be removed. It is what an item is called in every
// place a list is shown as a row, so a list without one would render as a set
// of unlabelled cells; Slack does not offer it either.
func RemoveListColumn(columns []ListColumn, key string) ([]ListColumn, error) {
	key = strings.TrimSpace(key)
	remaining := make([]ListColumn, 0, len(columns))
	found := false
	for _, column := range columns {
		if column.Key == key {
			if column.Primary {
				return nil, errInvalidListSchema
			}
			found = true
			continue
		}
		remaining = append(remaining, column)
	}
	if !found {
		return nil, errInvalidListSchema
	}
	return remaining, nil
}

// ListFieldsWithout returns an item's cells with one column's cell removed.
//
// Removing a column has to remove the values under it, not just stop showing
// them: a cell left behind would be invisible, would still be carried by every
// later edit, and would come back to life the day somebody declared a new
// column that happened to mint the same key.
func ListFieldsWithout(fields string, key string) (string, error) {
	cells, err := ParseListFields(fields)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	kept := make([]ListCell, 0, len(cells))
	for _, cell := range cells {
		if strings.TrimSpace(cell.ColumnID) == key {
			continue
		}
		kept = append(kept, cell)
	}
	if len(kept) == len(cells) {
		// Nothing to rewrite. Returning the stored text unchanged keeps an item
		// that never held this cell byte-identical, so a removal does not
		// rewrite every row in the list to reformat its JSON.
		return fields, nil
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
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
//
// previous is what the item already held, and its column names are accepted
// even when the schema does not declare them. Declaring the first column on a
// list that has been used free-form would otherwise make every existing item
// unwritable: the member did not introduce an invisible cell, they merely did
// not delete one, and refusing their edit would punish them for adding
// structure to their own list. New cells under undeclared columns are still
// refused, so the rule tightens going forward without breaking what is there.
func ValidateListFields(schema string, fields string, previous string) error {
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
	inherited := make(map[string]struct{})
	if existing, err := ParseListFields(previous); err == nil {
		for _, cell := range existing {
			inherited[strings.TrimSpace(cell.ColumnID)] = struct{}{}
		}
	}
	for _, cell := range cells {
		id := strings.TrimSpace(cell.ColumnID)
		column, known := declared[id]
		if !known {
			if _, carried := inherited[id]; carried {
				continue
			}
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

// ListColumnKey mints a stable identifier from a column's name.
//
// A key is what every item's cells reference, so it has to be something a
// person can recognise in stored JSON while they are working out why a value is
// not showing — "due_date" rather than an opaque draw. It is derived from the
// name and then made unique against the columns already there, because two
// columns called "Status" are a mistake worth surviving rather than a collision
// worth failing on.
func ListColumnKey(name string, existing []ListColumn) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == ' ' || character == '-' || character == '_':
			builder.WriteByte('_')
		}
	}
	base := strings.Trim(builder.String(), "_")
	if base == "" {
		base = "column"
	}
	taken := make(map[string]struct{}, len(existing))
	for _, column := range existing {
		taken[column.Key] = struct{}{}
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, clash := taken[candidate]; !clash {
			return candidate
		}
		candidate = base + "_" + strconv.Itoa(suffix)
	}
}

// EncodeListSchema writes columns back in the shape they are stored in, so a
// schema this product authored is read by the same parser that reads one an app
// authored.
func EncodeListSchema(columns []ListColumn) (string, error) {
	if len(columns) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(columns)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
