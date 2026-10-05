package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/crdt"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func openCanvasText(t *testing.T, messages Messages, id domain.CanvasID) *crdt.Sequence {
	t.Helper()
	canvas, err := messages.Canvas(context.Background(), "T1", "U1", id)
	if err != nil {
		t.Fatal(err)
	}
	var runs []crdt.Run
	if err := json.Unmarshal([]byte(canvas.TextState), &runs); err != nil {
		t.Fatalf("text state %q: %v", canvas.TextState, err)
	}
	text, err := crdt.Load(runs)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func storedCanvasMarkdown(t *testing.T, repository store.Store, id domain.CanvasID) string {
	t.Helper()
	canvas, err := repository.GetCanvas(context.Background(), "T1", id)
	if err != nil {
		t.Fatal(err)
	}
	markdown, err := domain.CanvasDocumentMarkdown(canvas.DocumentContent)
	if err != nil {
		t.Fatal(err)
	}
	return markdown
}

// Every way a canvas is written keeps its collaborative text and its sections
// saying the same thing, and an editor's ops, made against the text as it
// opened, still land after the canvas was written another way meanwhile.
func TestCanvasTextStaysInStepWithEveryWriter(t *testing.T) {
	ctx, repository, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Plan", `{"type":"markdown","markdown":"Keep me\n\nChange me"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	// A canvas nobody has edited as text has none stored; it opens with the
	// seed its sections write, the same whoever computes it.
	if stored, _ := repository.GetCanvas(ctx, "T1", canvas.ID); stored.TextState != "" {
		t.Fatalf("stored text before any edit = %q", stored.TextState)
	}
	editor := openCanvasText(t, messages, canvas.ID)
	if editor.Text() != "Keep me\n\nChange me" {
		t.Fatalf("seed = %q", editor.Text())
	}
	sections := canvasDocumentSections(t, mustGetCanvas(t, repository, canvas.ID))
	comment, err := messages.CommentOnCanvas(ctx, "T1", "U1", canvas.ID, sections[0].ID, "Keep this")
	if err != nil {
		t.Fatal(err)
	}

	ops, err := editor.Replace("U1.tab", "Keep me\n\nChanged")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.EditCanvasText(ctx, "T1", "U1", canvas.ID, ops); err != nil {
		t.Fatal(err)
	}
	if got := storedCanvasMarkdown(t, repository, canvas.ID); got != "Keep me\n\nChanged\n" {
		t.Fatalf("after the editor = %q", got)
	}
	if kept := canvasDocumentSections(t, mustGetCanvas(t, repository, canvas.ID)); kept[0].ID != comment.SectionID {
		t.Fatal("the unchanged section lost its identity, and its comment with it")
	}

	// The Slack API writes sections; the text follows by the smallest edit.
	if err := messages.EditCanvas(ctx, "T1", "U2", canvas.ID, `[]`); err == nil {
		t.Fatal("a member without a grant edited the canvas")
	}
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"From the API"}}]`); err != nil {
		t.Fatal(err)
	}
	if text := openCanvasText(t, messages, canvas.ID); text.Text() != "Keep me\n\nChanged\n\nFrom the API" {
		t.Fatalf("text after an API edit = %q", text.Text())
	}
	// The editor never saw that edit, and its next ops still land beside it.
	more, err := editor.Insert("U1.tab", 0, "# Title\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.EditCanvasText(ctx, "T1", "U1", canvas.ID, []crdt.Op{more}); err != nil {
		t.Fatal(err)
	}
	if got := storedCanvasMarkdown(t, repository, canvas.ID); got != "# Title\n\nKeep me\n\nChanged\n\nFrom the API\n" {
		t.Fatalf("after both = %q", got)
	}

	// A rename leaves the text alone, extra blank lines and all.
	spaced, err := openCanvasText(t, messages, canvas.ID).Insert("U1.tab2", 0, "\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.EditCanvasText(ctx, "T1", "U1", canvas.ID, []crdt.Op{spaced}); err != nil {
		t.Fatal(err)
	}
	before := mustGetCanvas(t, repository, canvas.ID).TextState
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"replace","title_content":{"title":"Renamed"}}]`); err != nil {
		t.Fatal(err)
	}
	if after := mustGetCanvas(t, repository, canvas.ID).TextState; after != before {
		t.Fatal("a rename rewrote the text")
	}

	// Restoring a revision is a write like any other.
	history, err := messages.CanvasRevisions(ctx, "T1", "U1", canvas.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Revisions) == 0 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	oldest := history.Revisions[len(history.Revisions)-1]
	if _, err := messages.RestoreCanvasRevision(ctx, "T1", "U1", canvas.ID, oldest.Version); err != nil {
		t.Fatal(err)
	}
	restored, _ := domain.CanvasDocumentMarkdown(oldest.DocumentContent)
	if text := openCanvasText(t, messages, canvas.ID); text.Text()+"\n" != restored {
		t.Fatalf("text after a restore = %q, want %q", text.Text(), restored)
	}
	// The markdown form saves through the same path.
	opened := mustGetCanvas(t, repository, canvas.ID)
	if _, _, err := messages.SaveCanvasMarkdown(ctx, "T1", "U1", canvas.ID, opened.Version, "From the form"); err != nil {
		t.Fatal(err)
	}
	if text := openCanvasText(t, messages, canvas.ID); text.Text() != "From the form" {
		t.Fatalf("text after a form save = %q", text.Text())
	}
}

func mustGetCanvas(t *testing.T, repository store.Store, id domain.CanvasID) domain.Canvas {
	t.Helper()
	canvas, err := repository.GetCanvas(context.Background(), "T1", id)
	if err != nil {
		t.Fatal(err)
	}
	return canvas
}

// An editor writes only under its writer's own name, and only ops the canvas
// can place.
func TestCanvasTextRefusesOpsItCannotTrust(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Plan", `{"type":"markdown","markdown":"Text"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	insert := func(replica string) crdt.Op {
		return crdt.Op{ID: crdt.ID{Replica: replica, Clock: 5}, Text: "x"}
	}
	if _, err := messages.EditCanvasText(ctx, "T1", "U2", canvas.ID, []crdt.Op{insert("U2.tab")}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a member without a grant = %v", err)
	}
	for name, ops := range map[string][]crdt.Op{
		"another writer's name":   {insert("U2.tab")},
		"no suffix":               {insert("U1")},
		"the server's name":       {insert(canvasServerReplica)},
		"a nested suffix":         {insert("U1.a.b")},
		"an unplaceable op":       {{ID: crdt.ID{Replica: "U1.tab", Clock: 5}, After: crdt.ID{Replica: "U1.gone", Clock: 3}, Text: "x"}},
		"an unplaceable delete":   {{Delete: []crdt.Span{{Replica: "U1.gone", Start: 1, End: 1}}}},
		"a malformed op":          {{}},
		"a clock from the future": {{ID: crdt.ID{Replica: "U1.tab", Clock: 1 << 40}, Text: "x"}},
		"no ops":                  nil,
		"more ops than one write": make([]crdt.Op, canvasTextOpLimit+1),
	} {
		if _, err := messages.EditCanvasText(ctx, "T1", "U1", canvas.ID, ops); !errors.Is(err, domain.ErrInvalidCanvas) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

// overtakingStore refuses the first canvas write as overtaken, as a second
// replica of the server writing the same canvas at the same moment would.
type overtakingStore struct {
	store.Store
	refused int
}

func (s *overtakingStore) UpdateCanvas(ctx context.Context, canvas domain.Canvas, event events.Event) error {
	if s.refused == 0 {
		s.refused++
		return store.ErrConflict
	}
	return s.Store.UpdateCanvas(ctx, canvas, event)
}

// Ops commute, so an edit another write overtook is redone against the newer
// canvas rather than refused.
func TestCanvasTextRedoesAnOvertakenEdit(t *testing.T) {
	ctx, repository, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Plan", `{"type":"markdown","markdown":"Text"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := openCanvasText(t, messages, canvas.ID).Replace("U1.tab", "Text, and more")
	if err != nil {
		t.Fatal(err)
	}
	overtaking := &overtakingStore{Store: repository}
	if _, err := (Messages{Store: overtaking}).EditCanvasText(ctx, "T1", "U1", canvas.ID, ops); err != nil || overtaking.refused != 1 {
		t.Fatalf("err=%v refused=%d", err, overtaking.refused)
	}
	if got := storedCanvasMarkdown(t, repository, canvas.ID); got != "Text, and more\n" {
		t.Fatalf("after a redone edit = %q", got)
	}
}
