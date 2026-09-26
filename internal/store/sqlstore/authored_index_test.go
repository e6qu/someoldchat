package sqlstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The Sent view lists one member's messages across every conversation. Every
// other message index leads with the conversation, so the listing read every
// message in the workspace and sorted them; the plan must now come from the
// author index, in order, without a temporary sort.
func TestAuthoredMessagesAreReadThroughTheAuthorIndex(t *testing.T) {
	ctx := context.Background()
	s := openDrained(t, ctx, filepath.Join(t.TempDir(), "authored.db"))
	defer s.Close()

	for _, order := range []string{` ORDER BY m.created_at, m.id LIMIT ?`, ` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`} {
		rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+authoredMessagesQuery+order, "T1", "U1", "U1", 10)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(plan, " | ")
		if !strings.Contains(joined, "messages_workspace_author_created") {
			t.Fatalf("the authored-messages query does not use the author index: %s", joined)
		}
		if strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
			t.Fatalf("the authored-messages query still sorts the member's messages itself: %s", joined)
		}
	}
}
