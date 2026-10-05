package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/crdt"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The canvas is one document to write in (CANVAS-02): every section renders
// inside one editor, carrying the markdown it was stored as, and the whole
// document saves at once through /document. A section the writer did not
// change keeps its identity, so the comment anchored to it stays with it.
func TestTheCanvasIsOneDocumentSavedAsAWhole(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	ctx := context.Background()
	value, err := messages.CreateCanvas(ctx, "T1", "U1", "Two parts", `{"type":"markdown","markdown":"First paragraph\n\nSecond paragraph"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	first := storedCanvasSections(t, s, value.ID)[0]
	if _, err := messages.CommentOnCanvas(ctx, "T1", "U1", value.ID, first.ID, "Agreed"); err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)

	body := get(t, mux, target).Body.String()
	requireContains(t, "canvas editor", body,
		`data-canvas-editor`, `data-markdown="First paragraph"`, `data-markdown="Second paragraph"`,
		`name="markdown"`, "First paragraph\n\nSecond paragraph\n</textarea>", "Save canvas", "Rename canvas")
	requireMissing(t, "canvas editor", body, "Save block", `name="section_id" value="temp`, "Move up", "Delete block")
	if strings.Count(body, "data-canvas-block data-markdown") != 2 {
		t.Fatalf("blocks in the document = %d, want 2", strings.Count(body, "data-canvas-block data-markdown"))
	}

	saved := postForm(t, mux, target+"/document", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "version": {pageVersion(t, body)},
		"markdown": {"First paragraph\r\n\r\nSecond paragraph, revised for <@U2>\r\n\r\n## Next steps\r\n\r\n- [ ] Ship it"},
	}.Encode(), false)
	if saved.Code != http.StatusSeeOther || !strings.Contains(saved.Header().Get("Location"), "notice=Canvas+saved") {
		t.Fatalf("save = %d %q: %s", saved.Code, saved.Header().Get("Location"), saved.Body)
	}
	sections := storedCanvasSections(t, s, value.ID)
	if len(sections) != 4 || sections[0].ID != first.ID || sections[1].Text != "Second paragraph, revised for <@U2>" ||
		sections[2].Type != domain.CanvasSectionHeading2 || sections[3].Text != "- [ ] Ship it" {
		t.Fatalf("stored sections = %+v", sections)
	}

	after := get(t, mux, target).Body.String()
	// The mention is an atomic pill in the editor, carrying what it saves as,
	// and the checklist box is a control; the comment still names its section.
	requireContains(t, "after the save", after,
		`class="canvas-mention" contenteditable="false" data-entity="&lt;@U2&gt;">@`, `role="checkbox" aria-checked="false"`,
		"<h4>Next steps</h4>", "on Section 1")

	// Saving the same document again writes nothing, and still says the
	// canvas is saved: autosave usually got there first.
	before, _ := s.GetCanvas(ctx, "T1", value.ID)
	again := postForm(t, mux, target+"/document", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "version": {pageVersion(t, after)},
		"markdown": {"First paragraph\n\nSecond paragraph, revised for <@U2>\n\n## Next steps\n\n- [ ] Ship it"},
	}.Encode(), false)
	if again.Code != http.StatusSeeOther || !strings.Contains(again.Header().Get("Location"), "notice=Canvas+saved") {
		t.Fatalf("unchanged save = %d %q", again.Code, again.Header().Get("Location"))
	}
	if unchanged, _ := s.GetCanvas(ctx, "T1", value.ID); unchanged.Version != before.Version {
		t.Fatalf("a save that changed nothing wrote version %d over %d", unchanged.Version, before.Version)
	}
}

// A save from a page someone else has since changed is refused rather than
// overwriting their work, and the writer's text comes back on the page with
// the reason, in the markdown field, so nothing they typed is lost.
func TestAStaleCanvasSaveKeepsTheWritersText(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Shared plan", `{"type":"markdown","markdown":"Keep this body"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)
	opened := pageVersion(t, get(t, mux, target).Body.String())
	if err := messages.EditCanvas(context.Background(), "T1", "U1", value.ID, `[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"Someone else's line"}}]`); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetCanvas(context.Background(), "T1", value.ID)
	if err != nil {
		t.Fatal(err)
	}

	response := postForm(t, mux, target+"/document", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "version": {opened}, "markdown": {"My unsaved paragraph"},
	}.Encode(), false)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale save = %d: %s", response.Code, response.Body)
	}
	requireContains(t, "refused save", response.Body.String(), "someone else changed it after you opened it", "My unsaved paragraph</textarea>", "data-draft", `<details class="canvas-source" open>`)
	stored, err := s.GetCanvas(context.Background(), "T1", value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DocumentContent != before.DocumentContent || stored.Version != before.Version {
		t.Fatalf("refused save changed the canvas: got %#v want %#v", stored, before)
	}

	for _, invalid := range []url.Values{
		{"_csrf": {auth.CSRFToken("session")}, "version": {"not-a-number"}, "markdown": {"x"}},
		{"_csrf": {auth.CSRFToken("session")}, "version": {"1"}, "markdown": {strings.Repeat("x", domain.CanvasMarkdownLimit+1)}},
	} {
		if refused := postForm(t, mux, target+"/document", invalid.Encode(), false); refused.Code != http.StatusBadRequest && refused.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("invalid save = %d", refused.Code)
		}
	}
	if missing := postForm(t, mux, "/app/canvases/F0MISSING/document", url.Values{"_csrf": {auth.CSRFToken("session")}, "version": {"1"}, "markdown": {"x"}}.Encode(), false); missing.Code != http.StatusNotFound {
		t.Fatalf("save of a missing canvas = %d", missing.Code)
	}
}

// A section of a kind markdown cannot spell (an app wrote it through
// canvases.create) is edited like any other part of the document and keeps
// its kind through the save, whether its text changed or not.
func TestAnAppSectionKeepsItsKindThroughTheDocumentEditor(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Mixed", `{"type":"heading","text":"Plan"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.EditCanvas(context.Background(), "T1", "U1", value.ID,
		`[{"operation":"insert_at_end","document_content":{"type":"rich_text","markdown":"App summary"}}]`); err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)
	body := get(t, mux, target).Body.String()
	requireContains(t, "mixed canvas", body, `class="canvas-block app-block"`, "heading content from an app; it keeps its kind as you edit it", `data-markdown="Plan"`)

	saved := postForm(t, mux, target+"/document", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "version": {pageVersion(t, body)}, "markdown": {"Revised plan\n\nApp summary"},
	}.Encode(), false)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save = %d: %s", saved.Code, saved.Body)
	}
	sections := storedCanvasSections(t, s, value.ID)
	if len(sections) != 2 || sections[0].Type != "heading" || sections[0].Text != "Revised plan" || sections[1].Type != "rich_text" {
		t.Fatalf("stored sections = %+v, want both app kinds kept", sections)
	}
}

// Renaming has its own route now that the document has one write path.
func TestRenamingACanvas(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	value, err := service.Messages{Store: s}.CreateCanvas(context.Background(), "T1", "U1", "Draft", "", "")
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)
	if blank := postForm(t, mux, target+"/rename", url.Values{"_csrf": {auth.CSRFToken("session")}, "title": {"  "}}.Encode(), false); blank.Code != http.StatusBadRequest {
		t.Fatalf("blank title = %d", blank.Code)
	}
	renamed := postForm(t, mux, target+"/rename", url.Values{"_csrf": {auth.CSRFToken("session")}, "title": {"Launch plan"}}.Encode(), false)
	if renamed.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d: %s", renamed.Code, renamed.Body)
	}
	if stored, _ := s.GetCanvas(context.Background(), "T1", value.ID); stored.Title != "Launch plan" {
		t.Fatalf("title = %q", stored.Title)
	}
}

func pageVersion(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="version" value="(\d+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("the editor carries no version: %s", body)
	}
	return match[1]
}

func storedCanvasSections(t *testing.T, s *memory.Store, id domain.CanvasID) []domain.CanvasSection {
	t.Helper()
	value, err := s.GetCanvas(context.Background(), "T1", id)
	if err != nil {
		t.Fatal(err)
	}
	var document domain.CanvasDocument
	if err := json.Unmarshal([]byte(value.DocumentContent), &document); err != nil {
		t.Fatal(err)
	}
	return document.Sections
}

// The editor writes ops on the canvas's collaborative text, answered in JSON.
// Two editors opened on the same page and typing at once both keep their
// words: nothing is refused as stale, an op sent twice changes nothing, and
// an op the canvas cannot place is refused with a reason the editor acts on.
func TestCanvasTextEditsMergeAndAnswerInJSON(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	value, err := service.Messages{Store: s}.CreateCanvas(context.Background(), "T1", "U1", "Notes", `{"type":"markdown","markdown":"First thought"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	page := get(t, mux, "/app/canvases/"+string(value.ID)).Body.String()
	state, replica := pageAttribute(t, page, "data-canvas-text"), pageAttribute(t, page, "data-canvas-replica")
	if !strings.HasPrefix(replica, "U1.") {
		t.Fatalf("replica = %q", replica)
	}
	open := func() *crdt.Sequence {
		t.Helper()
		var runs []crdt.Run
		if err := json.Unmarshal([]byte(state), &runs); err != nil {
			t.Fatal(err)
		}
		text, err := crdt.Load(runs)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	send := func(ops ...crdt.Op) *httptest.ResponseRecorder {
		t.Helper()
		encoded, _ := json.Marshal(ops)
		return postForm(t, mux, "/app/canvases/"+string(value.ID)+"/text", url.Values{"_csrf": {auth.CSRFToken("session")}, "ops": {string(encoded)}}.Encode(), false)
	}
	version := func(response *httptest.ResponseRecorder) int64 {
		t.Helper()
		var answer struct {
			OK      bool  `json:"ok"`
			Version int64 `json:"version"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &answer) != nil || !answer.OK {
			t.Fatalf("edit = %d %s", response.Code, response.Body)
		}
		return answer.Version
	}

	left, right := open(), open()
	leftOps, err := left.Replace(replica, "First thought, sharpened")
	if err != nil {
		t.Fatal(err)
	}
	rightOps, err := right.Replace("U1.othertab", "## Plan\n\nFirst thought")
	if err != nil {
		t.Fatal(err)
	}
	first := version(send(leftOps...))
	second := version(send(rightOps...))
	if second <= first {
		t.Fatalf("versions %d then %d", first, second)
	}
	stored, err := s.GetCanvas(context.Background(), "T1", value.ID)
	if err != nil {
		t.Fatal(err)
	}
	markdown, _ := domain.CanvasDocumentMarkdown(stored.DocumentContent)
	if markdown != "## Plan\n\nFirst thought, sharpened\n" {
		t.Fatalf("merged canvas = %q", markdown)
	}
	if again := version(send(leftOps...)); again != second {
		t.Fatalf("resent ops moved the canvas to %d", again)
	}

	for name, ops := range map[string][]crdt.Op{
		"someone else's replica": {{ID: crdt.ID{Replica: "U2.tab", Clock: 99}, Text: "x"}},
		"an unknown character":   {{ID: crdt.ID{Replica: replica, Clock: 6}, After: crdt.ID{Replica: "U1.gone", Clock: 5}, Text: "x"}},
		"nothing":                {},
	} {
		if refused := send(ops...); refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), `"invalid_ops"`) {
			t.Fatalf("%s = %d %s", name, refused.Code, refused.Body)
		}
	}
	// Past the limit with what the canvas already says.
	huge, err := open().Insert("U1.big", 0, strings.Repeat("x", domain.CanvasMarkdownLimit))
	if err != nil {
		t.Fatal(err)
	}
	if refused := send(huge); refused.Code != http.StatusRequestEntityTooLarge || !strings.Contains(refused.Body.String(), `"too_large"`) {
		t.Fatalf("oversize edit = %d %s", refused.Code, refused.Body)
	}
	if missing := postForm(t, mux, "/app/canvases/F0MISSING/text", url.Values{"_csrf": {auth.CSRFToken("session")}, "ops": {"[]"}}.Encode(), false); missing.Code != http.StatusNotFound {
		t.Fatalf("edit of a missing canvas = %d", missing.Code)
	}
	// One writer's run of edits keeps one revision: the state before it.
	history, err := service.Messages{Store: s}.CanvasRevisions(context.Background(), "T1", "U1", value.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Revisions) != 1 || strings.Contains(history.Revisions[0].DocumentContent, "Plan") {
		t.Fatalf("history after a run of edits = %+v err=%v", history.Revisions, err)
	}
}

func pageAttribute(t *testing.T, page, name string) string {
	t.Helper()
	match := regexp.MustCompile(name + `="([^"]*)"`).FindStringSubmatch(page)
	if match == nil {
		t.Fatalf("page has no %s", name)
	}
	return html.UnescapeString(match[1])
}

// An open editor that falls out of step reads the canvas's text back as JSON,
// and renders blocks another writer added the way the page renders a stored
// section. Neither answers for a canvas the reader cannot open, and neither
// turns a refusal into a 500.
func TestCanvasTextAndBlocksAnswerTheEditorInJSON(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "other"})
	messages := service.Messages{Store: s}
	mine, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Mine", `{"type":"markdown","markdown":"## Plan\n\nAsk <@U1> first"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := messages.CreateCanvas(context.Background(), "T1", "U2", "Theirs", `{"type":"markdown","markdown":"Private"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	page := get(t, mux, "/app/canvases/"+string(mine.ID)).Body.String()

	var text struct {
		OK        bool   `json:"ok"`
		Version   int64  `json:"version"`
		TextState string `json:"text_state"`
	}
	response := get(t, mux, "/app/canvases/"+string(mine.ID)+"/text")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &text) != nil || !text.OK {
		t.Fatalf("text = %d %s", response.Code, response.Body)
	}
	if text.TextState != pageAttribute(t, page, "data-canvas-text") || text.Version != mine.Version {
		t.Fatalf("text = %+v, page holds %q at version %d", text, pageAttribute(t, page, "data-canvas-text"), mine.Version)
	}

	markdown := "## Plan\n\nAsk <@U1> first"
	response = postForm(t, mux, "/app/canvases/"+string(mine.ID)+"/blocks", url.Values{"_csrf": {auth.CSRFToken("session")}, "markdown": {markdown}}.Encode(), false)
	var rendered struct {
		OK     bool `json:"ok"`
		Blocks []struct {
			Markdown string `json:"markdown"`
			Heading  int    `json:"heading"`
			Text     string `json:"text"`
			HTML     string `json:"html"`
		} `json:"blocks"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rendered) != nil || !rendered.OK || len(rendered.Blocks) != 2 {
		t.Fatalf("blocks = %d %s", response.Code, response.Body)
	}
	if rendered.Blocks[0].Markdown+"\n\n"+rendered.Blocks[1].Markdown != markdown || rendered.Blocks[0].Heading != 2 || rendered.Blocks[0].Text != "Plan" {
		t.Fatalf("blocks do not read back as the markdown: %+v", rendered.Blocks)
	}
	// The body is the page's own rendering of the same section, mention and all.
	if !strings.Contains(page, rendered.Blocks[1].HTML) || !strings.Contains(rendered.Blocks[1].HTML, "data-entity") {
		t.Fatalf("block html %q is not the page's rendering", rendered.Blocks[1].HTML)
	}

	for _, refused := range []*httptest.ResponseRecorder{
		get(t, mux, "/app/canvases/"+string(theirs.ID)+"/text"),
		postForm(t, mux, "/app/canvases/"+string(theirs.ID)+"/blocks", url.Values{"_csrf": {auth.CSRFToken("session")}, "markdown": {"x"}}.Encode(), false),
	} {
		if refused.Code != http.StatusNotFound || !strings.Contains(refused.Body.String(), `"not_found"`) || strings.Contains(refused.Body.String(), "Private") {
			t.Fatalf("a canvas the reader cannot open answered %d %s", refused.Code, refused.Body)
		}
	}
	tooLong := postForm(t, mux, "/app/canvases/"+string(mine.ID)+"/blocks", url.Values{"_csrf": {auth.CSRFToken("session")}, "markdown": {strings.Repeat("x", domain.CanvasMarkdownLimit+1)}}.Encode(), false)
	if tooLong.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an over-long render answered %d", tooLong.Code)
	}
}

// A page says it is on a canvas, and where its cursor is, in JSON. A canvas
// the member cannot open, a session no page would make and a cursor that is
// not a character all answer as refusals rather than 500s, and leaving takes
// the page off at once.
func TestCanvasPresenceAnswersThePageInJSON(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "other"})
	messages := service.Messages{Store: s}
	mine, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Mine", `{"type":"markdown","markdown":"Here"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := messages.CreateCanvas(context.Background(), "T1", "U2", "Theirs", `{"type":"markdown","markdown":"Private"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	post := func(id domain.CanvasID, fields url.Values) *httptest.ResponseRecorder {
		t.Helper()
		fields.Set("_csrf", auth.CSRFToken("session"))
		return postForm(t, mux, "/app/canvases/"+string(id)+"/presence", fields.Encode(), false)
	}
	if response := post(mine.ID, url.Values{"session": {"page-one-abc"}, "caret_r": {"seed"}, "caret_c": {"2"}}); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("presence = %d %s", response.Code, response.Body)
	}
	present, err := messages.CanvasPresence(context.Background(), "T1", "U1", mine.ID)
	if err != nil || len(present) != 1 || present[0].Session != "page-one-abc" || present[0].Caret.Replica != "seed" || present[0].Caret.Clock != 2 || present[0].Name != "Ada Developer" {
		t.Fatalf("present = %+v err=%v", present, err)
	}
	for name, refused := range map[string]struct {
		response *httptest.ResponseRecorder
		status   int
		code     string
	}{
		"someone else's canvas":  {post(theirs.ID, url.Values{"session": {"page-one-abc"}}), http.StatusNotFound, "not_found"},
		"a short session":        {post(mine.ID, url.Values{"session": {"x"}}), http.StatusBadRequest, "invalid_presence"},
		"a cursor with no clock": {post(mine.ID, url.Values{"session": {"page-one-abc"}, "caret_r": {"seed"}, "caret_c": {"soon"}}), http.StatusBadRequest, "invalid_presence"},
	} {
		if refused.response.Code != refused.status || !strings.Contains(refused.response.Body.String(), `"`+refused.code+`"`) {
			t.Fatalf("%s answered %d %s", name, refused.response.Code, refused.response.Body)
		}
	}
	if response := post(mine.ID, url.Values{"session": {"page-one-abc"}, "leave": {"1"}}); response.Code != http.StatusOK {
		t.Fatalf("leaving = %d %s", response.Code, response.Body)
	}
	if present, err := messages.CanvasPresence(context.Background(), "T1", "U1", mine.ID); err != nil || len(present) != 0 {
		t.Fatalf("after leaving present = %+v err=%v", present, err)
	}
	if page := get(t, mux, "/app/canvases/"+string(mine.ID)).Body.String(); !strings.Contains(page, `data-event-canvas="`+string(mine.ID)+`"`) || !strings.Contains(page, "data-canvas-present") {
		t.Fatal("the canvas page does not name its canvas to its stream")
	}
}
