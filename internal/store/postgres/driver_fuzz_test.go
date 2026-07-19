package postgres

import "testing"

func FuzzRewriteIsIdempotent(f *testing.F) {
	f.Add("SELECT ? AS value, '?' AS literal")
	f.Add("BEGIN IMMEDIATE")
	f.Add("INSERT OR IGNORE INTO users(id) VALUES (?)")
	f.Add("CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT)")
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > 4096 {
			t.Skip()
		}
		first := rewrite(query)
		if second := rewrite(first); second != first {
			t.Fatalf("rewrite is not idempotent: first %q, second %q", first, second)
		}
	})
}
