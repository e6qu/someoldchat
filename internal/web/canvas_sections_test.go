package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
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
		{"_csrf": {auth.CSRFToken("session")}, "version": {"1"}, "markdown": {strings.Repeat("x", maxCanvasMarkdownBytes+1)}},
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

// The editor's autosave is the same save answered in JSON: the version to
// save against next, or a fixed reason it was refused, never a page or a
// redirect it could not act on.
func TestCanvasAutosaveAnswersInJSON(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	value, err := service.Messages{Store: s}.CreateCanvas(context.Background(), "T1", "U1", "Notes", `{"type":"markdown","markdown":"First thought"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID) + "/document"
	version := pageVersion(t, get(t, mux, "/app/canvases/"+string(value.ID)).Body.String())
	autosave := func(version, markdown string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(url.Values{"_csrf": {auth.CSRFToken("session")}, "version": {version}, "markdown": {markdown}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("X-SameOldChat-Autosave", "true")
		addBrowserCookies(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}

	first := autosave(version, "First thought\n\nSecond thought")
	var saved struct {
		OK      bool  `json:"ok"`
		Version int64 `json:"version"`
		Changed int   `json:"changed"`
	}
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &saved) != nil || !saved.OK || saved.Changed != 1 {
		t.Fatalf("autosave = %d %s", first.Code, first.Body)
	}
	if stored, _ := s.GetCanvas(context.Background(), "T1", value.ID); stored.Version != saved.Version {
		t.Fatalf("answered version %d, stored %d", saved.Version, stored.Version)
	}
	// The next save goes against the answered version.
	next := autosave(strconv.FormatInt(saved.Version, 10), "First thought\n\nSecond thought, refined")
	if next.Code != http.StatusOK {
		t.Fatalf("second autosave = %d %s", next.Code, next.Body)
	}
	// A version someone else has moved past is refused with a reason the editor
	// can act on, and nothing is written.
	stale := autosave(version, "Overwrite everything")
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"error":"conflict"`) {
		t.Fatalf("stale autosave = %d %s", stale.Code, stale.Body)
	}
	if large := autosave(strconv.FormatInt(saved.Version+1, 10), strings.Repeat("x", maxCanvasMarkdownBytes+1)); large.Code != http.StatusRequestEntityTooLarge || !strings.Contains(large.Body.String(), `"too_large"`) {
		t.Fatalf("oversize autosave = %d %s", large.Code, large.Body)
	}
	if bad := autosave("soon", "x"); bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid version autosave = %d", bad.Code)
	}
	// One writer's run of saves keeps one revision: the state before it.
	history, err := service.Messages{Store: s}.CanvasRevisions(context.Background(), "T1", "U1", value.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Revisions) != 1 || !strings.Contains(history.Revisions[0].DocumentContent, "First thought") || strings.Contains(history.Revisions[0].DocumentContent, "Second") {
		t.Fatalf("history after a run of autosaves = %+v err=%v", history.Revisions, err)
	}
}
