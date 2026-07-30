package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestSQLiteSearchHistorySurvivesReopenAndRemainsPrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recent-searches.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}))
	require(s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}))
	require(s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}))
	base := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	require(s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{WorkspaceID: "T1", UserID: "U1", Query: "first", SearchedAt: base}))
	require(s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{WorkspaceID: "T1", UserID: "U1", Query: "second", SearchedAt: base.Add(time.Minute)}))
	require(s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{WorkspaceID: "T1", UserID: "U1", Query: "first", SearchedAt: base.Add(2 * time.Minute)}))
	require(s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{WorkspaceID: "T1", UserID: "U2", Query: "private-to-bob", SearchedAt: base.Add(3 * time.Minute)}))
	require(s.Close())

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	values, err := s.ListSearchHistory(ctx, "T1", "U1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Query != "first" || values[1].Query != "second" {
		t.Fatalf("recent searches = %+v", values)
	}
	for _, value := range values {
		if value.Query == "private-to-bob" {
			t.Fatalf("another user's private search leaked: %+v", values)
		}
	}
}

func TestVersion114MigrationCreatesRecentSearchStorage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-114.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE recent_searches`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 113 WHERE version = ?`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	columns, err := s.tableColumns(ctx, s.db, "recent_searches")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"workspace_id", "user_id", "query", "searched_at"} {
		if !columns[column] {
			t.Fatalf("recent_searches.%s was not migrated", column)
		}
	}
}
