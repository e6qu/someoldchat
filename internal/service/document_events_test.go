package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/crdt"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func mustCanvasEvent(t *testing.T) events.Event {
	t.Helper()
	event, err := canvasEvent("T1", "U1", "canvas.updated", "F1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return event
}

type deliveredDocumentEvent struct {
	Topic      string
	CanvasID   string            `json:"canvas_id"`
	ListID     string            `json:"list_id"`
	CanvasText *canvasTextChange `json:"canvas_text"`
}

func documentEventsFor(t *testing.T, messages Messages, user domain.UserID) []deliveredDocumentEvent {
	t.Helper()
	page, err := messages.ListUserEventsAfter(context.Background(), "T1", user, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var found []deliveredDocumentEvent
	for _, record := range page.Records {
		if !documentEventTopic(record.Event.Topic) {
			continue
		}
		var event deliveredDocumentEvent
		if err := json.Unmarshal([]byte(record.Event.Payload), &event); err != nil {
			t.Fatalf("%s payload %q: %v", record.Event.Topic, record.Event.Payload, err)
		}
		event.Topic = record.Event.Topic
		found = append(found, event)
	}
	return found
}

// A canvas's and a list's records reach the people who can read it and
// nobody else. They name the document rather than a channel, so the channel
// rule used to admit every one of them to every member's live stream: that a
// private canvas exists, who it was shared with, and every edit to it.
func TestDocumentEventsReachOnlyTheirReaders(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Private plan", `{"type":"markdown","markdown":"Secret"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := messages.CreateList(ctx, "T1", "U1", "Private list", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"More"}}]`); err != nil {
		t.Fatal(err)
	}
	if got := documentEventsFor(t, messages, "U2"); len(got) != 0 {
		t.Fatalf("a member with no access sees %+v", got)
	}
	owner := documentEventsFor(t, messages, "U1")
	var sawCanvas, sawList bool
	for _, event := range owner {
		sawCanvas = sawCanvas || event.CanvasID == string(canvas.ID)
		sawList = sawList || event.ListID == string(list.ID)
	}
	if !sawCanvas || !sawList {
		t.Fatalf("the owner's stream lacks their own documents: %+v", owner)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	shared := documentEventsFor(t, messages, "U2")
	if len(shared) == 0 {
		t.Fatal("a reader the canvas was shared with sees none of its records")
	}
	for _, event := range shared {
		if event.CanvasID != string(canvas.ID) {
			t.Fatalf("a canvas reader sees another document's record: %+v", event)
		}
	}
}

// Every write that changes a canvas's text tells its readers how: the version
// it made and the ops that made it, which turn the text a reader had into the
// text the canvas now has. A change too big to carry asks them to fetch it.
func TestCanvasEditsCarryTheirOpsToReaders(t *testing.T) {
	ctx, _, messages := canvasWorld(t)
	canvas, err := messages.CreateCanvas(ctx, "T1", "U1", "Plan", `{"type":"markdown","markdown":"First"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetCanvasAccess(ctx, "T1", "U1", canvas.ID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	reader := openCanvasText(t, messages, canvas.ID)
	editor := openCanvasText(t, messages, canvas.ID)
	op, err := editor.Insert("U1.tab", editor.Len(), " and second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.EditCanvasText(ctx, "T1", "U1", canvas.ID, []crdt.Op{op}); err != nil {
		t.Fatal(err)
	}
	if err := messages.EditCanvas(ctx, "T1", "U1", canvas.ID, `[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"Third"}}]`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := messages.SaveCanvasMarkdown(ctx, "T1", "U1", canvas.ID, 3, "First and second\n\nThird\n\nFourth"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RestoreCanvasRevision(ctx, "T1", "U1", canvas.ID, 1); err != nil {
		t.Fatal(err)
	}
	versions := []int64{}
	for _, event := range documentEventsFor(t, messages, "U2") {
		if event.CanvasText == nil {
			continue
		}
		if event.CanvasText.Resync {
			t.Fatalf("a small %s asks readers to fetch the text", event.Topic)
		}
		for _, op := range event.CanvasText.Ops {
			if _, err := reader.Apply(op); err != nil {
				t.Fatalf("%s op %+v: %v", event.Topic, op, err)
			}
		}
		versions = append(versions, event.CanvasText.Version)
	}
	if want := []int64{2, 3, 4, 5}; len(versions) != len(want) || versions[0] != 2 || versions[3] != 5 {
		t.Fatalf("versions carried = %v, want %v", versions, want)
	}
	if final := openCanvasText(t, messages, canvas.ID); reader.Pending() != 0 || reader.Text() != final.Text() {
		t.Fatalf("a reader applying the records has %q (pending %d), the canvas has %q", reader.Text(), reader.Pending(), final.Text())
	}
}

func TestOversizedCanvasChangesAskReadersToFetchTheText(t *testing.T) {
	ops := []crdt.Op{{ID: crdt.ID{Replica: "server", Clock: 1}, Text: strings.Repeat("x", canvasTextChangeLimit)}}
	event, err := withCanvasTextChange(mustCanvasEvent(t), 7, ops)
	if err != nil {
		t.Fatal(err)
	}
	var change canvasTextChange
	if err := json.Unmarshal([]byte(event.PrivatePayload), &change); err != nil {
		t.Fatal(err)
	}
	if !change.Resync || change.Version != 7 || len(change.Ops) != 0 {
		t.Fatalf("oversized change = %+v", change)
	}
}
