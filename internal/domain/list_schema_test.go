package domain

import (
	"errors"
	"testing"
)

// A list created without a schema is given a single primary text column so the
// client has something to show. That substitution is the product's convenience
// rather than the member's declaration, so it is not enforced — enforcing it
// would forbid columns nobody said they did not want and break every list used
// as a free-form checklist.
func TestOneDefaultColumnIsNotAStructure(t *testing.T) {
	single := `[{"key":"title","name":"Title","type":"text","is_primary_column":true}]`
	if err := ValidateListFields(single, `[{"column_id":"anything","value":"free form"}]`); err != nil {
		t.Fatalf("an unstructured list refused a cell: %v", err)
	}
	if err := ValidateListFields("", `[{"column_id":"anything","value":"free form"}]`); err != nil {
		t.Fatalf("a list with no schema refused a cell: %v", err)
	}
	// Two columns, or one that is not a plain text primary, is somebody having
	// said what the list is for.
	declared := `[{"key":"title","name":"Title","type":"text","is_primary_column":true},{"key":"status","name":"Status","type":"select","options":["open","done"]}]`
	if err := ValidateListFields(declared, `[{"column_id":"anything","value":"x"}]`); !errors.Is(err, errInvalidListSchema) {
		t.Fatalf("a declared list accepted an undeclared column: %v", err)
	}
}

// Each type is a promise about what a cell contains. A value the column cannot
// mean is refused, because accepting it would make the column a lie the next
// reader has to discover.
func TestAColumnRefusesWhatItCannotMean(t *testing.T) {
	schema := `[{"key":"title","name":"Title","type":"text"},{"key":"count","name":"Count","type":"number"},{"key":"due","name":"Due","type":"date"},{"key":"done","name":"Done","type":"checkbox"},{"key":"status","name":"Status","type":"select","options":["open","done"]}]`
	for name, fields := range map[string]string{
		"a number that is not one":  `[{"column_id":"count","value":"soon"}]`,
		"a date that is an instant": `[{"column_id":"due","value":"2026-09-01T12:00:00Z"}]`,
		"a checkbox that is a word": `[{"column_id":"done","value":"yes"}]`,
		"an option nobody offered":  `[{"column_id":"status","value":"blocked"}]`,
	} {
		if err := ValidateListFields(schema, fields); !errors.Is(err, errInvalidListSchema) {
			t.Fatalf("%s was accepted", name)
		}
	}
	for name, fields := range map[string]string{
		"a real number":       `[{"column_id":"count","value":42}]`,
		"a calendar date":     `[{"column_id":"due","value":"2026-09-01"}]`,
		"a checkbox":          `[{"column_id":"done","value":true}]`,
		"an offered option":   `[{"column_id":"status","value":"open"}]`,
		"any text":            `[{"column_id":"title","value":"whatever"}]`,
		"a cell left empty":   `[{"column_id":"count","value":""}]`,
		"a cell left unset":   `[{"column_id":"count","value":null}]`,
		"a row missing cells": `[{"column_id":"title","value":"only this"}]`,
	} {
		if err := ValidateListFields(schema, fields); err != nil {
			t.Fatalf("%s was refused: %v", name, err)
		}
	}
}

func TestASchemaRefusesWhatItCannotMean(t *testing.T) {
	for name, schema := range map[string]string{
		"a column with no name":     `[{"key":"a","type":"text"}]`,
		"a column with no key":      `[{"name":"A","type":"text"}]`,
		"a type nobody defined":     `[{"key":"a","name":"A","type":"colour"}]`,
		"two columns sharing a key": `[{"key":"a","name":"A","type":"text"},{"key":"a","name":"B","type":"text"}]`,
		"a select with no options":  `[{"key":"a","name":"A","type":"select"}]`,
		"not a schema at all":       `{"key":"a"}`,
	} {
		if _, err := ParseListSchema(schema); !errors.Is(err, errInvalidListSchema) {
			t.Fatalf("%s was accepted", name)
		}
	}
	// `id` is accepted alongside `key`, so a schema written either way keeps
	// working.
	columns, err := ParseListSchema(`[{"id":"a","name":"A","type":"text"},{"key":"b","name":"B","type":"number"}]`)
	if err != nil || len(columns) != 2 || columns[0].Key != "a" || columns[1].Key != "b" {
		t.Fatalf("columns = %+v err = %v, want both identifier spellings read", columns, err)
	}
}

// A JSON number arrives as a float64, and formatting a million with %v gives
// "1e+06", which is not what anybody typed.
func TestACellRendersAsItWasWritten(t *testing.T) {
	for value, want := range map[any]string{
		float64(1000000): "1000000",
		float64(1.5):     "1.5",
		"text":           "text",
		true:             "true",
		nil:              "",
	} {
		if got := ListCellText(value); got != want {
			t.Fatalf("ListCellText(%v) = %q, want %q", value, got, want)
		}
	}
}
