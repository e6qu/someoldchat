package service

import (
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Declaring a column appends. Rewriting the schema wholesale would let one edit
// silently orphan every value in the list, because each cell references a
// column by key.
func TestAddingAColumnAppendsAndMintsAStableKey(t *testing.T) {
	ctx, messages := schemaWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Due date", domain.ListColumnDate, nil); err != nil {
		t.Fatal(err)
	}
	// Two columns with the same name are a mistake worth surviving rather than
	// a collision worth failing on.
	updated, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Due date", domain.ListColumnText, nil)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := domain.ParseListSchema(updated.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 || columns[0].Key != "title" {
		t.Fatalf("columns = %+v, want the original followed by the two additions", columns)
	}
	if columns[1].Key != "due_date" || columns[2].Key != "due_date_2" {
		t.Fatalf("keys = %q/%q, want a readable key made unique", columns[1].Key, columns[2].Key)
	}
}

// A select with no options is a text column that refuses every value, so it is
// refused here rather than created and discovered by whoever tries to fill it.
func TestASelectColumnNeedsOptions(t *testing.T) {
	ctx, messages := schemaWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Status", domain.ListColumnSelect, nil); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("a select with no options = %v, want ErrInvalidList", err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "", domain.ListColumnText, nil); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("a nameless column = %v, want ErrInvalidList", err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Status", "colour", nil); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("a type nobody defined = %v, want ErrInvalidList", err)
	}
}

// Structuring a list that has been used free-form must not make its existing
// items unwritable. The member did not introduce an invisible cell — they
// merely did not delete one — and refusing their edit would punish them for
// adding structure to their own list.
func TestDeclaringAColumnDoesNotStrandExistingItems(t *testing.T) {
	ctx, messages := schemaWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Checklist", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	freeForm := `[{"column_id":"note","value":"written before anyone declared anything"}]`
	item, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", freeForm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Status", domain.ListColumnSelect, []string{"open", "done"}); err != nil {
		t.Fatal(err)
	}
	// The old item still saves, keeping the cell it already had.
	if _, err := messages.UpdateListItem(ctx, "T1", "U1", list.ID, item.ID, freeForm, false); err != nil {
		t.Fatalf("an item written before the schema became unwritable: %v", err)
	}
	// A new undeclared cell is still refused, so the rule tightens going
	// forward without breaking what is there.
	if _, err := messages.UpdateListItem(ctx, "T1", "U1", list.ID, item.ID, `[{"column_id":"invented","value":"x"}]`, false); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("a newly invented column was accepted: %v", err)
	}
	// And a declared column is enforced as usual.
	if _, err := messages.UpdateListItem(ctx, "T1", "U1", list.ID, item.ID, `[{"column_id":"status","value":"blocked"}]`, false); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("an unoffered option was accepted: %v", err)
	}
}
