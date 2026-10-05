package sqlstore

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// canvasPresenceSchema is part of the base schema, which creates it on a fresh
// database, and is schema step 215, which creates it on an existing one. A row
// is one open canvas page (domain.CanvasPresence). Like conversation_typing it
// has no outbox record and no foreign keys: it is state that expires, and a
// row outliving its canvas by a few seconds is invisible to every reader,
// because reading presence takes read access to the canvas.
const canvasPresenceSchema = `CREATE TABLE IF NOT EXISTS canvas_presence (
 workspace_id TEXT NOT NULL, canvas_id TEXT NOT NULL, user_id TEXT NOT NULL, session TEXT NOT NULL,
 caret_replica TEXT NOT NULL DEFAULT '', caret_clock INTEGER NOT NULL DEFAULT 0,
 anchor_replica TEXT NOT NULL DEFAULT '', anchor_clock INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL,
 PRIMARY KEY (canvas_id, user_id, session)
);
CREATE INDEX IF NOT EXISTS canvas_presence_expiry ON canvas_presence(expires_at);
`

// RecordCanvasPresence replaces one page's row. Rows that have lapsed are
// cleared on write, as typing signals are, rather than by a worker.
func (s *Store) RecordCanvasPresence(ctx context.Context, presence domain.CanvasPresence, now time.Time) error {
	if !presence.Valid() {
		return store.InvalidArgument("canvas presence requires a workspace, canvas, member, session and expiry")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM canvas_presence WHERE expires_at <= ?`, now.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO canvas_presence(workspace_id, canvas_id, user_id, session, caret_replica, caret_clock, anchor_replica, anchor_clock, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(canvas_id, user_id, session) DO UPDATE SET caret_replica = excluded.caret_replica, caret_clock = excluded.caret_clock,
			anchor_replica = excluded.anchor_replica, anchor_clock = excluded.anchor_clock, expires_at = excluded.expires_at`,
		presence.WorkspaceID, presence.CanvasID, presence.UserID, presence.Session, presence.Caret.Replica, int64(presence.Caret.Clock),
		presence.Anchor.Replica, int64(presence.Anchor.Clock), presence.ExpiresAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	return nil
}

// ClearCanvasPresence removes one page's row: the page said it is leaving.
func (s *Store) ClearCanvasPresence(ctx context.Context, workspace domain.WorkspaceID, canvas domain.CanvasID, user domain.UserID, session string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM canvas_presence WHERE workspace_id = ? AND canvas_id = ? AND user_id = ? AND session = ?`, workspace, canvas, user, session)
	return classify(err)
}

// ListCanvasPresence reports the pages open on one canvas at the given
// instant, in a stable order. Who may read it is the service's to decide.
func (s *Store) ListCanvasPresence(ctx context.Context, workspace domain.WorkspaceID, canvas domain.CanvasID, now time.Time) ([]domain.CanvasPresence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, session, caret_replica, caret_clock, anchor_replica, anchor_clock, expires_at FROM canvas_presence
		WHERE workspace_id = ? AND canvas_id = ? AND expires_at > ? ORDER BY user_id, session`, workspace, canvas, now.UTC().UnixNano())
	if err != nil {
		return nil, err
	}
	present := make([]domain.CanvasPresence, 0, 4)
	for rows.Next() {
		value := domain.CanvasPresence{WorkspaceID: workspace, CanvasID: canvas}
		var clock, anchorClock, expires int64
		if err := rows.Scan(&value.UserID, &value.Session, &value.Caret.Replica, &clock, &value.Anchor.Replica, &anchorClock, &expires); err != nil {
			rows.Close()
			return nil, err
		}
		value.Caret.Clock = uint64(clock)
		value.Anchor.Clock = uint64(anchorClock)
		value.ExpiresAt = time.Unix(0, expires).UTC()
		present = append(present, value)
	}
	return present, closeRows(rows)
}
