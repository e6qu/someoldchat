package domain

import (
	"regexp"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/crdt"
)

// CanvasPresence is one open canvas page: who has it open and, for a writer,
// the character their cursor is at. Like a typing signal (TypingSignal) it is
// state with an expiry rather than an event: a page renews its row while it
// is open, and a page that closes without saying so — a laptop shut, a tab
// killed — drops out when the row lapses.
//
// The cursor is named by a character of the canvas's collaborative text
// rather than by an offset, so it stays on the same words while other people
// edit before it. A page with nothing to point at (a reader, or a writer
// whose cursor is outside the document) has a zero Caret.
type CanvasPresence struct {
	WorkspaceID WorkspaceID
	CanvasID    CanvasID
	UserID      UserID
	// Session tells one page from another, so a member with the canvas open
	// in two tabs is two rows and closing one leaves the other.
	Session string
	// Caret is the character the cursor is just after. Anchor is the other
	// end of a selection, and zero when nothing is selected.
	Caret     crdt.ID
	Anchor    crdt.ID
	ExpiresAt time.Time
	// Name is how the member is shown. The store does not keep it; the
	// service fills it in for the reader.
	Name string
}

const (
	// CanvasPresenceTTL is how long a page stays present without renewing.
	CanvasPresenceTTL = 15 * time.Second
	// CanvasPresenceInterval is how often an open page renews, comfortably
	// inside the TTL so a page that is open never blinks out.
	CanvasPresenceInterval = 5 * time.Second
)

var canvasPresenceSession = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ValidCanvasPresenceSession reports whether a page's session name is one a
// page makes: a random token of letters, digits, - and _.
func ValidCanvasPresenceSession(session string) bool {
	return canvasPresenceSession.MatchString(session)
}

func (p CanvasPresence) Valid() bool {
	if p.WorkspaceID == "" || p.CanvasID == "" || p.UserID == "" || !ValidCanvasPresenceSession(p.Session) || p.ExpiresAt.IsZero() {
		return false
	}
	return p.Cursor().Valid()
}

// CanvasCursor is where a page's cursor is: the character it is just after
// and, for a selection, the character at its other end.
type CanvasCursor struct {
	Caret  crdt.ID
	Anchor crdt.ID
}

func (p CanvasPresence) Cursor() CanvasCursor { return CanvasCursor{Caret: p.Caret, Anchor: p.Anchor} }

// Valid reports whether each end names a character or nothing, and a
// selection has a cursor to go with its anchor.
func (c CanvasCursor) Valid() bool {
	named := func(id crdt.ID) bool { return id.IsZero() || (crdt.ValidReplica(id.Replica) && id.Clock > 0) }
	return named(c.Caret) && named(c.Anchor) && (c.Anchor.IsZero() || !c.Caret.IsZero())
}
