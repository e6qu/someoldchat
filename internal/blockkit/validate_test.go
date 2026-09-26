package blockkit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateBlocksMatchesCurrentDocumentedResponseExample(t *testing.T) {
	valid := json.RawMessage(`[{"type":"section","text":{"type":"plain_text","text":"Hello world"}}]`)
	if problems, err := ValidateBlocks(valid, "", 100); err != nil || len(problems) != 0 {
		t.Fatalf("valid problems=%+v err=%v", problems, err)
	}
	invalid := json.RawMessage(`[{"type":"section","text":{"type":"invalid","text":"Hello world"}}]`)
	problems, err := ValidateBlocks(invalid, "", 100)
	if err != nil || len(problems) != 1 {
		t.Fatalf("invalid problems=%+v err=%v", problems, err)
	}
	if problems[0].Pointer != "/0/text/type" || problems[0].Code != "failed_constraint" {
		t.Fatalf("problem=%+v", problems[0])
	}
}

// The validator runs on every message write, so it must accept every block
// Slack accepts: an image named by slack_file, a call block, and a section
// with fields and an accessory. It refuses what Slack refuses about them.
func TestValidateBlocksCoversSlackFileImagesCallsAndSectionFields(t *testing.T) {
	valid := json.RawMessage(`[
		{"type":"image","slack_file":{"url":"https://files.slack.com/x.png"},"alt_text":"a","title":{"type":"plain_text","text":"T"}},
		{"type":"image","image_url":"https://example.com/x.png","alt_text":"a"},
		{"type":"call","call_id":"R123"},
		{"type":"section","fields":[{"type":"mrkdwn","text":"*a*"},{"type":"plain_text","text":"b"}],"accessory":{"type":"image","image_url":"https://example.com/y.png","alt_text":"y"}}
	]`)
	if problems, err := ValidateBlocks(valid, "", 50); err != nil || len(problems) != 0 {
		t.Fatalf("valid problems=%+v err=%v", problems, err)
	}
	fields := `{"type":"mrkdwn","text":"f"}`
	for name, raw := range map[string]string{
		"image with both sources": `[{"type":"image","image_url":"https://example.com/x.png","slack_file":{"id":"F1"},"alt_text":"a"}]`,
		"empty slack_file":        `[{"type":"image","slack_file":{},"alt_text":"a"}]`,
		"call without id":         `[{"type":"call"}]`,
		"eleven fields":           `[{"type":"section","fields":[` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `,` + fields + `]}]`,
		"long field":              `[{"type":"section","fields":[{"type":"mrkdwn","text":"` + strings.Repeat("x", 2001) + `"}]}]`,
		"accessory not an object": `[{"type":"section","text":{"type":"mrkdwn","text":"t"},"accessory":"button"}]`,
	} {
		if problems, err := ValidateBlocks(json.RawMessage(raw), "", 50); err != nil || len(problems) == 0 {
			t.Fatalf("%s accepted: problems=%+v err=%v", name, problems, err)
		}
	}
}

func TestValidateMessageAndViewApplySurfaceLimits(t *testing.T) {
	if problems, err := ValidateMessage(json.RawMessage(`{"text":"fallback","blocks":[{"type":"divider"}]}`)); err != nil || len(problems) != 0 {
		t.Fatalf("message problems=%+v err=%v", problems, err)
	}
	problems, err := ValidateView(json.RawMessage(`{"type":"modal","title":{"type":"mrkdwn","text":"Wrong"},"blocks":[]}`))
	if err != nil || len(problems) != 1 || problems[0].Pointer != "/title/type" {
		t.Fatalf("view problems=%+v err=%v", problems, err)
	}
}

func TestValidateCurrentAlertCardAndCarouselContracts(t *testing.T) {
	valid := json.RawMessage(`[
		{"type":"alert","level":"success","text":{"type":"mrkdwn","text":"Healthy"}},
		{"type":"card","title":{"type":"plain_text","text":"Release"},"actions":[{"type":"button"}]},
		{"type":"carousel","elements":[{"type":"card","body":{"type":"plain_text","text":"One"}}]}
	]`)
	if problems, err := ValidateBlocks(valid, "", 100); err != nil || len(problems) != 0 {
		t.Fatalf("problems=%+v err=%v", problems, err)
	}
	invalid := json.RawMessage(`[{"type":"alert","level":"invented","text":{"type":"plain_text","text":"Alert"}},{"type":"carousel","elements":[]}]`)
	problems, err := ValidateBlocks(invalid, "", 100)
	if err != nil || len(problems) != 2 || problems[0].Pointer != "/0/level" || problems[1].Pointer != "/1/elements" {
		t.Fatalf("problems=%+v err=%v", problems, err)
	}
}

func TestValidateCurrentPlanTaskAndDataTableContracts(t *testing.T) {
	valid := json.RawMessage(`[
		{"type":"task_card","task_id":"one","title":"Fetch","status":"complete"},
		{"type":"plan","title":"Plan","tasks":[]},
		{"type":"data_table","caption":"Results","rows":[
			[{"type":"raw_text","text":"Name"}],
			[{"type":"raw_text","text":"API"}]
		]}
	]`)
	if problems, err := ValidateBlocks(valid, "", 100); err != nil || len(problems) != 0 {
		t.Fatalf("problems=%+v err=%v", problems, err)
	}
	invalid := json.RawMessage(`[{"type":"task_card","task_id":"","title":"Fetch","status":"invented"},{"type":"data_table","caption":"Results","rows":[[],[]]}]`)
	problems, err := ValidateBlocks(invalid, "", 100)
	if err != nil || len(problems) != 4 {
		t.Fatalf("problems=%+v err=%v", problems, err)
	}
}

func TestValidateContainerAndDataVisualizationContracts(t *testing.T) {
	valid := json.RawMessage(`[
		{"type":"container","title":{"type":"plain_text","text":"Bulk update"},"width":"wide",
		 "is_collapsible":true,"default_collapsed":true,
		 "child_blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Ready"}}]},
		{"type":"data_visualization","title":"Weekly sales","chart":{"type":"line",
		 "series":[
			 {"name":"Web","data":[{"label":"Week 1","value":32},{"label":"Week 2","value":45}]},
			 {"name":"Store","data":[{"label":"Week 1","value":20},{"label":"Week 2","value":28}]}
		 ],
		 "axis_config":{"categories":["Week 1","Week 2"],"x_label":"Week","y_label":"Sales"}}}
	]`)
	problems, err := ValidateBlocks(valid, "/blocks", 50)
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems=%+v err=%v", problems, err)
	}

	invalid := json.RawMessage(`[
		{"type":"container","width":"huge","child_blocks":[{"type":"card","title":{"type":"plain_text","text":"Unsupported child"}}]},
		{"type":"data_visualization","title":"Bad series","chart":{"type":"bar",
		 "series":[
			 {"name":"Duplicate","data":[{"label":"Missing","value":1},{"label":"Only","value":2}]},
			 {"name":"Duplicate","data":[{"label":"Only","value":"not-a-number"},{"label":"Only","value":3}]}
		 ],
		 "axis_config":{"categories":["Only","Only"]}}}
	]`)
	problems, err = ValidateBlocks(invalid, "/blocks", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, pointer := range []string{
		"/blocks/0", "/blocks/0/width", "/blocks/0/child_blocks/0/type",
		"/blocks/1/chart/axis_config/categories/1", "/blocks/1/chart/series/0/data/0/label",
		"/blocks/1/chart/series/1/name", "/blocks/1/chart/series/1/data/0/value",
	} {
		found := false
		for _, problem := range problems {
			if problem.Pointer == pointer {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %s in %+v", pointer, problems)
		}
	}
}
