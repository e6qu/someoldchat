package realtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// CanvasPresenceSource reports who has a canvas open (domain.CanvasPresence).
// A stream opened by a canvas page (/events?canvas=F…) reads it on its own
// timer, as it reads typing, because presence is state that expires rather
// than a journal record.
type CanvasPresenceSource interface {
	CanvasPresence(context.Context, domain.WorkspaceID, domain.UserID, domain.CanvasID) ([]domain.CanvasPresence, error)
}

// canvasPresencePollInterval is how often a canvas page's stream re-reads who
// is on the canvas: about as often as a cursor moves noticeably, and well
// inside domain.CanvasPresenceTTL.
const canvasPresencePollInterval = time.Second

// canvasPresenceFrame is what a canvas page is told: everyone on the canvas,
// each page with its session (so a page can leave itself out), the member's
// name as the reader sees it, and the character their cursor is at. Expiry
// is left out: it moves on every renewal, and a frame is sent only when what
// the page would show changes.
type canvasPresenceFrame struct {
	CanvasID string               `json:"canvas_id"`
	Present  []canvasPresencePage `json:"present"`
}

type canvasPresencePage struct {
	UserID  string           `json:"user_id"`
	Name    string           `json:"name"`
	Session string           `json:"session"`
	Caret   *canvasCaretWire `json:"caret,omitempty"`
	Anchor  *canvasCaretWire `json:"anchor,omitempty"`
}

type canvasCaretWire struct {
	Replica string `json:"r"`
	Clock   uint64 `json:"c"`
}

// canvasPresenceAnnouncer remembers the last frame a stream sent, so an
// unchanged canvas sends nothing.
type canvasPresenceAnnouncer struct {
	last string
}

func (a *canvasPresenceAnnouncer) due(canvas domain.CanvasID, present []domain.CanvasPresence) (string, bool, error) {
	frame := canvasPresenceFrame{CanvasID: string(canvas), Present: make([]canvasPresencePage, 0, len(present))}
	for _, value := range present {
		page := canvasPresencePage{UserID: string(value.UserID), Name: value.Name, Session: value.Session}
		if !value.Caret.IsZero() {
			page.Caret = &canvasCaretWire{Replica: value.Caret.Replica, Clock: value.Caret.Clock}
		}
		if !value.Anchor.IsZero() {
			page.Anchor = &canvasCaretWire{Replica: value.Anchor.Replica, Clock: value.Anchor.Clock}
		}
		frame.Present = append(frame.Present, page)
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return "", false, err
	}
	if string(encoded) == a.last {
		return "", false, nil
	}
	a.last = string(encoded)
	return a.last, true, nil
}
