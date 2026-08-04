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
// invisible. Each section now renders as a section, and the markdown ones are
// editable in place.
func TestMultiSectionCanvasIsEditableSectionBySection(t *testing.T) {
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
	requireContains(t, "multi-section canvas", body, "First paragraph", "Second paragraph", "Save section 1", "Save section 2")
	requireMissing(t, "multi-section canvas", body, "could not be read as a document")

	sections := regexp.MustCompile(`name="section_id" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(sections) != 2 {
		t.Fatalf("editors = %d, want one per section", len(sections))
	}

	// Editing the second section leaves the first alone. A whole-document save
	// would have replaced both, which is exactly the flattening the old
	// read-only rule was protecting against — by refusing to edit at all.
	saved := postForm(t, mux, target+"/update", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "section_id": {sections[1][1]},
		"title": {"Two parts"}, "body": {"Second paragraph, revised"},
	}.Encode(), false)
	if saved.Code != 303 {
		t.Fatalf("save second section = %d: %s", saved.Code, saved.Body)
	}
	after := get(t, mux, target).Body.String()
	requireContains(t, "after the save", after, "First paragraph", "Second paragraph, revised")
	if strings.Count(after, "Second paragraph<") > 0 {
		t.Error("the original second section survived alongside its replacement")
	}
}

// A section this client has no editor for keeps its content and says why,
// rather than making the whole canvas read-only or offering a textarea that
// would flatten it on save.
func TestUneditableSectionDoesNotBlockTheRest(t *testing.T) {
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
	body := get(t, mux, "/app/canvases/"+string(value.ID)).Body.String()
	requireContains(t, "mixed canvas", body, "Plan", "Editable body", "editing it here would flatten it")
	if strings.Count(body, "Save section") != 1 {
		t.Errorf("editors offered = %d, want exactly one for the markdown section", strings.Count(body, "Save section"))
	}
}
