package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestSearchHistoryIsPrivateDeduplicatedAndBounded(t *testing.T) {
	ctx := context.Background()
	s := New()
	base := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	for index := 0; index < store.MaxSearchHistoryEntries+2; index++ {
		err := s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{
			WorkspaceID: "T1",
			UserID:      "U1",
			Query:       fmt.Sprintf("query-%02d", index),
			SearchedAt:  base.Add(time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{
		WorkspaceID: "T1", UserID: "U1", Query: "query-10", SearchedAt: base.Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSearchHistory(ctx, domain.SearchHistoryEntry{
		WorkspaceID: "T1", UserID: "U2", Query: "private-to-u2", SearchedAt: base.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	values, err := s.ListSearchHistory(ctx, "T1", "U1", store.MaxSearchHistoryEntries)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != store.MaxSearchHistoryEntries {
		t.Fatalf("history length = %d, want %d", len(values), store.MaxSearchHistoryEntries)
	}
	if values[0].Query != "query-10" {
		t.Fatalf("first query = %q, want refreshed query-10", values[0].Query)
	}
	for _, value := range values {
		if value.Query == "private-to-u2" || value.Query == "query-00" || value.Query == "query-01" {
			t.Fatalf("unexpected private or pruned entry: %+v", value)
		}
	}
}
