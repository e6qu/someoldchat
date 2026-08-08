package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// TestActivityLevelRankMatchesSQL holds the Go rank and the SQL CASE together.
// The filter is a rank comparison and the storage keeps the name, so the two
// expressions have to agree: if they drift, a caller asking for warnings and
// above quietly gets a different set on the SQL profile than on the memory one.
func TestActivityLevelRankMatchesSQL(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "levels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	levels := []domain.ActivityLevel{
		domain.ActivityTrace, domain.ActivityDebug, domain.ActivityInfo,
		domain.ActivityWarn, domain.ActivityError, domain.ActivityFatal,
		domain.ActivityLevel("shouted"), domain.ActivityLevel(""),
	}
	for _, level := range levels {
		var rank int
		if err := store.db.QueryRowContext(ctx, `SELECT `+activityLevelRankExpression+` FROM (SELECT ? AS level)`, string(level)).Scan(&rank); err != nil {
			t.Fatalf("%q: %v", level, err)
		}
		if rank != level.Rank() {
			t.Errorf("%q ranks %d in Go and %d in SQL", level, level.Rank(), rank)
		}
	}
}
