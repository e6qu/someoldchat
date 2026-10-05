package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/crdt"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// A canvas's collaborative text (domain.Canvas.TextState) has three kinds of
// writer, each a replica of its own so their characters never share a name:
// the seed, which is the text a canvas's sections write before anyone has
// edited it as text; the server, for every write that arrives as a whole
// document rather than as ops (the Slack API, a form saved without script, a
// restored revision); and each editor, whose replica is its writer's user ID
// and a suffix of its own (CanvasTextReplica). The server's writes cannot
// collide with one another because every canvas write is a compare-and-swap
// on the version: two at once means one is refused and redone.
const (
	canvasSeedReplica   = "seed"
	canvasServerReplica = "server"
	// canvasTextOpLimit bounds one EditCanvasText call; an editor sends what
	// was typed since its last send, which is far fewer.
	canvasTextOpLimit = 500
	// canvasTextAttempts is how many times EditCanvasText redoes an edit
	// another write overtook. Ops commute, so redoing one against the newer
	// canvas is exactly as correct as the first try.
	canvasTextAttempts = 5
	// canvasTextChangeLimit bounds the edits one journal record carries. A
	// write bigger than this — a restore, a whole-document API edit — tells
	// open editors to fetch the text instead, so no reader's stream carries a
	// canvas-sized record.
	canvasTextChangeLimit = 64 << 10
)

// canvasTextChange is what a canvas.updated or canvas.restored record tells
// an open editor about the text: the version the write made and the ops that
// made it, or that the change is too big to carry and the editor fetches the
// text instead. It travels in the record's PrivatePayload and is filled in
// only for a reader who can read the canvas (see prepareDocumentEvent).
type canvasTextChange struct {
	Version int64     `json:"version"`
	Ops     []crdt.Op `json:"ops,omitempty"`
	Resync  bool      `json:"resync,omitempty"`
}

// withCanvasTextChange records on event the ops a write applied to reach
// version.
func withCanvasTextChange(event events.Event, version int64, ops []crdt.Op) (events.Event, error) {
	encoded, err := json.Marshal(canvasTextChange{Version: version, Ops: ops})
	if err != nil {
		return events.Event{}, err
	}
	if len(encoded) > canvasTextChangeLimit {
		if encoded, err = json.Marshal(canvasTextChange{Version: version, Resync: true}); err != nil {
			return events.Event{}, err
		}
	}
	event.PrivatePayload = string(encoded)
	return event, nil
}

// CanvasTextReplica reports whether replica may carry user's edits: their user
// ID, a dot, and the suffix that tells one of their editors from another.
func CanvasTextReplica(user domain.UserID, replica string) bool {
	suffix, ok := strings.CutPrefix(replica, string(user)+".")
	return ok && user != "" && suffix != "" && !strings.Contains(suffix, ".") && crdt.ValidReplica(replica)
}

// canvasText is the canvas's collaborative text as it stands: the stored one,
// or for a canvas nobody has edited as text, the seed its sections write.
// The seed is the same whoever computes it, because it is a function of
// content no write can change without storing a text: an editor that loaded
// the seed and a write that stores it agree on every character's name.
func canvasText(canvas domain.Canvas) (*crdt.Sequence, error) {
	if canvas.TextState == "" {
		markdown, err := domain.CanvasDocumentMarkdown(canvas.DocumentContent)
		if err != nil {
			return nil, err
		}
		seed := crdt.New()
		if markdown = strings.TrimSuffix(markdown, "\n"); markdown != "" {
			if _, err := seed.Insert(canvasSeedReplica, 0, markdown); err != nil {
				return nil, err
			}
		}
		return seed, nil
	}
	var runs []crdt.Run
	if err := json.Unmarshal([]byte(canvas.TextState), &runs); err != nil {
		return nil, domain.ErrInvalidCanvas
	}
	text, err := crdt.Load(runs)
	if err != nil {
		return nil, domain.ErrInvalidCanvas
	}
	return text, nil
}

func encodeCanvasText(text *crdt.Sequence) (string, error) {
	encoded, err := json.Marshal(text.Snapshot())
	if err != nil {
		return "", err
	}
	if len(encoded) > domain.CanvasTextStateLimit {
		return "", domain.ErrCanvasTooLarge
	}
	return string(encoded), nil
}

// withCanvasText fills in the text a canvas nobody has edited as text starts
// from, so an editor opening it has the names every later edit refers to. A
// canvas whose stored document cannot be read has no text: it reads as it is
// stored and cannot be edited, here or through EditCanvasText.
func withCanvasText(canvas domain.Canvas) (domain.Canvas, error) {
	if canvas.TextState != "" {
		return canvas, nil
	}
	if _, err := domain.CanvasDocumentSections(canvas.DocumentContent); err != nil {
		return canvas, nil
	}
	text, err := canvasText(canvas)
	if err != nil {
		return domain.Canvas{}, err
	}
	canvas.TextState, err = encodeCanvasText(text)
	return canvas, err
}

// syncCanvasText keeps the text in step with a write that changed next's
// sections without ops. The text changes only when it no longer reads as
// those sections, and then by the smallest edit, so an editor's text survives
// a write that did not touch it — a rename, or an API edit to another part —
// and an editor's ops still find every character they name. It answers the
// ops it applied, which open editors apply in turn.
func syncCanvasText(previous domain.Canvas, next *domain.Canvas) ([]crdt.Op, error) {
	text, err := canvasText(previous)
	if err != nil {
		return nil, err
	}
	sections, err := domain.CanvasDocumentSections(next.DocumentContent)
	if err != nil {
		return nil, err
	}
	markdown := domain.CanvasSectionsMarkdown(sections)
	if domain.CanvasSectionsMarkdown(domain.CanvasMarkdownBlocks(text.Text())) == markdown {
		next.TextState = previous.TextState
		return nil, nil
	}
	ops, err := text.Replace(canvasServerReplica, strings.TrimSuffix(markdown, "\n"))
	if err != nil {
		return nil, err
	}
	next.TextState, err = encodeCanvasText(text)
	return ops, err
}

// EditCanvasText applies an editor's ops to the canvas's text and makes its
// sections what the text now says, keeping every section that did not
// change and the comments anchored to it. Ops from any number of editors
// merge whatever order they arrive in, so there is no version to save
// against and nothing to refuse as stale; an op sent twice changes nothing.
// An op naming an edit the canvas never had, or written under another
// person's replica, is domain.ErrInvalidCanvas: the editor is out of step and
// reloads. It answers the version the canvas is at.
func (m Messages) EditCanvasText(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, ops []crdt.Op) (int64, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
		return 0, err
	}
	if len(ops) == 0 || len(ops) > canvasTextOpLimit {
		return 0, domain.ErrInvalidCanvas
	}
	for _, op := range ops {
		if op.Valid() != nil || (op.Text != "" && !CanvasTextReplica(userID, op.ID.Replica)) {
			return 0, domain.ErrInvalidCanvas
		}
	}
	for attempt := 1; ; attempt++ {
		version, err := m.editCanvasText(ctx, workspaceID, userID, id, ops)
		if errors.Is(err, store.ErrConflict) && attempt < canvasTextAttempts {
			continue
		}
		return version, err
	}
}

func (m Messages) editCanvasText(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, ops []crdt.Op) (int64, error) {
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if err != nil {
		return 0, err
	}
	text, err := canvasText(canvas)
	if err != nil {
		return 0, err
	}
	changed := false
	for _, op := range ops {
		// An editor's clock is the highest it has seen plus one, and it has
		// seen nothing the canvas has not: its own ops arrive in order and
		// everyone else's came from here. A clock further ahead is not an
		// edit any editor makes, and one near the limit would leave no clock
		// for anyone's next insert.
		if op.Text != "" && op.ID.Clock > text.Clock()+1 {
			return 0, domain.ErrInvalidCanvas
		}
		applied, err := text.Apply(op)
		if err != nil {
			return 0, domain.ErrInvalidCanvas
		}
		changed = changed || applied
	}
	if text.Pending() > 0 {
		return 0, domain.ErrInvalidCanvas
	}
	if !changed {
		return canvas.Version, nil
	}
	markdown := text.Text()
	if len(markdown) > domain.CanvasMarkdownLimit {
		return 0, domain.ErrCanvasTooLarge
	}
	document, err := decodeCanvasDocument(canvas.DocumentContent)
	if err != nil {
		return 0, err
	}
	if _, err := projectCanvasMarkdown(&document, markdown); err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return 0, err
	}
	if canvas.TextState, err = encodeCanvasText(text); err != nil {
		return 0, err
	}
	canvas.DocumentContent = string(encoded)
	canvas.Version++
	canvas.UpdatedAt = time.Now().UTC()
	event, err := canvasEvent(workspaceID, userID, "canvas.updated", id, canvas.UpdatedAt)
	if err != nil {
		return 0, err
	}
	if event, err = withCanvasTextChange(event, canvas.Version, ops); err != nil {
		return 0, err
	}
	if err := m.Store.UpdateCanvas(ctx, canvas, event); err != nil {
		return 0, err
	}
	return canvas.Version, nil
}
