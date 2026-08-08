package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
)

// TestEveryForeignKeyNamesATableThatExists migrates a fresh database and then
// asks SQLite what each table's foreign keys point at. A migration names its
// target table in a string, so a typo compiles, migrates, and only fails when
// somebody first writes the row - as REFERENCES usergroups(id) and REFERENCES
// apps(id) both did, where the real tables are user_groups and slack_apps.
func TestEveryForeignKeyNamesATableThatExists(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tables := map[string]struct{}{}
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables[name] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("a migrated database declares no tables; the reader is wrong")
	}

	checked := 0
	for table := range tables {
		keys, err := store.db.QueryContext(ctx, `SELECT "table" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for keys.Next() {
			var target string
			if err := keys.Scan(&target); err != nil {
				keys.Close()
				t.Fatal(err)
			}
			checked++
			if _, exists := tables[target]; !exists {
				t.Errorf("%s has a foreign key to %q, which is not a table; every write to %s fails", table, target, table)
			}
		}
		keys.Close()
		if err := keys.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("read no foreign keys at all; the reader is wrong")
	}
}
