package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func schemaWorld(t *testing.T) (context.Context, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"})
	return ctx, Messages{Store: repository}
}

const declaredSchema = `[{"key":"title","name":"Title","type":"text","is_primary_column":true},{"key":"status","name":"Status","type":"select","options":["open","done"]},{"key":"due","name":"Due","type":"date"}]`

// An item is checked against the columns its list declares. A cell under a
// column nobody declared is invisible to every reader, so accepting it silently
// would lose the member's work while looking like it had been saved.
func TestAnItemMustConformToItsListsColumns(t *testing.T) {
	ctx, messages := schemaWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Launch", "", declaredSchema, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	good := `[{"column_id":"title","value":"ship it"},{"column_id":"status","value":"open"},{"column_id":"due","value":"2026-09-01"}]`
	item, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", good)
	if err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string]string{
		"an undeclared column": `[{"column_id":"priority","value":"high"}]`,
		"an unoffered option":  `[{"column_id":"status","value":"blocked"}]`,
		"a date that is not":   `[{"column_id":"due","value":"soon"}]`,
	} {
		if _, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", fields); !errors.Is(err, ErrInvalidList) {
			t.Fatalf("%s was accepted on create: %v", name, err)
		}
		if _, err := messages.UpdateListItem(ctx, "T1", "U1", list.ID, item.ID, fields, false); !errors.Is(err, ErrInvalidList) {
			t.Fatalf("%s was accepted on update: %v", name, err)
		}
	}
	// The good item is still what it was: a refused write changes nothing.
	stored, err := messages.GetListItem(ctx, "T1", "U1", list.ID, item.ID)
	if err != nil || stored.Fields != good {
		t.Fatalf("item = %+v err = %v, want the accepted fields intact", stored, err)
	}
}

// A list created without a schema is free-form, which is what lists were before
// columns existed. Enforcing the substituted default would forbid columns
// nobody said they did not want.
func TestAListWithoutDeclaredColumnsStaysFreeForm(t *testing.T) {
	ctx, messages := schemaWorld(t)
	list, err := messages.CreateList(ctx, "T1", "U1", "Checklist", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"anything","value":"free form"}]`); err != nil {
		t.Fatalf("a free-form list refused a cell: %v", err)
	}
}

// A schema that cannot be read is refused at the door rather than stored and
// discovered later by every reader of the list.
func TestAListRefusesASchemaItCannotRead(t *testing.T) {
	ctx, messages := schemaWorld(t)
	for name, schema := range map[string]string{
		"a type nobody defined":    `[{"key":"a","name":"A","type":"colour"}]`,
		"a select with no options": `[{"key":"a","name":"A","type":"select"}]`,
		"a column with no name":    `[{"key":"a","type":"text"}]`,
	} {
		if _, err := messages.CreateList(ctx, "T1", "U1", "Bad", "", schema, "", false, false); !errors.Is(err, ErrInvalidList) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
}
