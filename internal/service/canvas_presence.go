package service

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// SetCanvasPresence says one page has the canvas open, and where its cursor
// is (and, for a selection, its other end); leaving removes the page at once rather than waiting for it to lapse.
// Anyone who can read the canvas can be present on it — a reader is someone
// the writers may want to know is looking — but only a writer's page points
// at the text, so a cursor from someone who cannot write is dropped.
func (m Messages) SetCanvasPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, session string, cursor domain.CanvasCursor, leaving bool) error {
	if !domain.ValidCanvasPresenceSession(session) {
		return domain.ErrInvalidCanvas
	}
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return err
	}
	if leaving {
		return m.Store.ClearCanvasPresence(ctx, workspaceID, id, userID, session)
	}
	if !cursor.Valid() {
		return domain.ErrInvalidCanvas
	}
	if cursor != (domain.CanvasCursor{}) {
		if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessWrite); errors.Is(err, store.ErrNotFound) {
			cursor = domain.CanvasCursor{}
		} else if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	presence := domain.CanvasPresence{WorkspaceID: workspaceID, CanvasID: id, UserID: userID, Session: session, Caret: cursor.Caret, Anchor: cursor.Anchor, ExpiresAt: now.Add(domain.CanvasPresenceTTL)}
	if !presence.Valid() {
		return domain.ErrInvalidCanvas
	}
	return m.Store.RecordCanvasPresence(ctx, presence, now)
}

// CanvasPresence reports the pages open on a canvas, each named as the
// reader sees its member, to someone who can read the canvas. The reader's
// own pages are included: a page tells itself apart by its session, and a
// member's other tab is someone else's view for all the page knows.
func (m Messages) CanvasPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) ([]domain.CanvasPresence, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return nil, err
	}
	present, err := m.Store.ListCanvasPresence(ctx, workspaceID, id, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	names := make(map[domain.UserID]string, len(present))
	for index, value := range present {
		name, known := names[value.UserID]
		if !known {
			user, userErr := m.Store.GetUser(ctx, value.UserID)
			switch {
			case userErr == nil:
				name = user.ShownName()
			case errors.Is(userErr, store.ErrNotFound):
				name = string(value.UserID)
			default:
				return nil, userErr
			}
			names[value.UserID] = name
		}
		present[index].Name = name
	}
	return present, nil
}
