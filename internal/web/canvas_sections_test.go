package web

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// A canvas with more than one section used to be flattened into one blob of
// joined text and marked read-only in its entirety, so a document an app had
// created through canvases.create — which has taken structured sections since
// it was built — could be read here but never edited, and its structure was
// invisible. Every block now renders as its own block with its own editor, and
// editing one leaves the others alone.
func TestMultiSectionCanvasIsEditableBlockByBlock(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	// canvases.create takes a single section, so a multi-section document is
	// built the way an app builds one: by editing.
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Two parts", `{"type":"markdown","markdown":"First paragraph"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.EditCanvas(context.Background(), "T1", "U1", value.ID,
		`[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"Second paragraph"}}]`); err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)

	page := get(t, mux, target)
	body := page.Body.String()
	requireContains(t, "multi-section canvas", body, "First paragraph", "Second paragraph", "Save block 1", "Save block 2")
	requireMissing(t, "multi-section canvas", body, "could not be read as a document")

	sections := regexp.MustCompile(`name="section_id" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	// One id per block editor plus one per delete form.
	ids := map[string]bool{}
	for _, match := range sections {
		ids[match[1]] = true
	}
	if len(ids) != 2 {
		t.Fatalf("distinct block ids = %d, want two", len(ids))
	}

	// Editing the second block leaves the first alone. A whole-document save
	// would have replaced both, which is exactly the flattening the old
	// read-only rule was protecting against — by refusing to edit at all.
	second := sections[len(sections)-1][1]
	saved := postForm(t, mux, target+"/sections", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "op": {"save"}, "section_id": {second},
		"type": {"markdown"}, "body": {"Second paragraph, revised"},
	}.Encode(), false)
	if saved.Code != 303 {
		t.Fatalf("save second block = %d: %s", saved.Code, saved.Body)
	}
	after := get(t, mux, target).Body.String()
	requireContains(t, "after the save", after, "First paragraph", "Second paragraph, revised")
	// Scoped to the document rather than the page: the history panel below it
	// shows what the canvas said before, and that is the point of the history —
	// the replaced text appearing there is correct, and appearing twice in the
	// document is the duplication this guards against.
	document := after
	if history := strings.Index(after, `class="canvas-history"`); history >= 0 {
		document = after[:history]
	}
	if strings.Count(document, "Second paragraph<") > 0 {
		t.Error("the original second block survived alongside its replacement")
	}
}

// A block carrying a kind this client does not name — an app wrote it through
// canvases.create — is still editable, and editing its text keeps its kind
// rather than flattening it to a paragraph. The heading below is such a block.
func TestBlockWithAnUnnamedKindKeepsItsKindWhenEdited(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Mixed", `{"type":"heading","text":"Plan"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.EditCanvas(context.Background(), "T1", "U1", value.ID,
		`[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"Editable body"}}]`); err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)
	body := get(t, mux, target).Body.String()
	// Both blocks are editable; the heading names its kept kind rather than
	// being shut out.
	requireContains(t, "mixed canvas", body, "Plan", "Editable body", "Save block 1", "Save block 2", "its kind is kept as you edit")
	requireMissing(t, "mixed canvas", body, "editing it here would flatten it")

	// Edit the heading's text. Its "heading" kind is carried through the hidden
	// field, so the stored block stays a heading.
	sections := regexp.MustCompile(`name="section_id" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	heading := sections[0][1]
	saved := postForm(t, mux, target+"/sections", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "op": {"save"}, "section_id": {heading},
		"type": {"heading"}, "body": {"Revised plan"},
	}.Encode(), false)
	if saved.Code != 303 {
		t.Fatalf("save heading = %d: %s", saved.Code, saved.Body)
	}
	stored, err := messages.LookupCanvasSections(context.Background(), "T1", "U1", value.ID, `{"section_types":["heading"]}`)
	if err != nil || len(stored) != 1 || stored[0].Text != "Revised plan" {
		t.Fatalf("heading after edit = %+v err=%v (kind must stay 'heading')", stored, err)
	}
}

// The block editor reorders and deletes blocks and adds new ones, all through
// the one /sections write path.
func TestCanvasBlocksReorderAddAndDeleteFromTheBrowser(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	csrf := auth.CSRFToken("session")
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Ordered", `{"type":"markdown","markdown":"Alpha"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)

	// Add a heading at the end.
	if added := postForm(t, mux, target+"/sections", url.Values{
		"_csrf": {csrf}, "op": {"add"}, "type": {"h2"}, "body": {"Omega"},
	}.Encode(), false); added.Code != 303 {
		t.Fatalf("add block = %d: %s", added.Code, added.Body)
	}

	// The new block is last; move it up so it leads. Read the ids in order.
	orderIDs := func() []string {
		body := get(t, mux, target).Body.String()
		editors := regexp.MustCompile(`class="editor block"[\s\S]*?name="section_id" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
		ids := make([]string, 0, len(editors))
		for _, match := range editors {
			ids = append(ids, match[1])
		}
		return ids
	}
	ids := orderIDs()
	if len(ids) != 2 {
		t.Fatalf("blocks = %d, want 2", len(ids))
	}
	if moved := postForm(t, mux, target+"/sections", url.Values{
		"_csrf": {csrf}, "op": {"move_up"}, "section_id": {ids[1]},
	}.Encode(), false); moved.Code != 303 {
		t.Fatalf("move up = %d: %s", moved.Code, moved.Body)
	}
	stored, err := messages.LookupCanvasSections(context.Background(), "T1", "U1", value.ID, `{}`)
	if err != nil || len(stored) != 2 || stored[0].Text != "Omega" || stored[1].Text != "Alpha" {
		t.Fatalf("after move up = %+v err=%v", stored, err)
	}

	// Delete the now-second block (Alpha).
	if deleted := postForm(t, mux, target+"/sections", url.Values{
		"_csrf": {csrf}, "op": {"delete"}, "section_id": {stored[1].ID},
	}.Encode(), false); deleted.Code != 303 {
		t.Fatalf("delete = %d: %s", deleted.Code, deleted.Body)
	}
	remaining, err := messages.LookupCanvasSections(context.Background(), "T1", "U1", value.ID, `{}`)
	if err != nil || len(remaining) != 1 || remaining[0].Text != "Omega" {
		t.Fatalf("after delete = %+v err=%v", remaining, err)
	}
}
