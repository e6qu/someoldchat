package web

import (
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestSlackMarkupPreservesEscapedFormattingCharacters(t *testing.T) {
	rendered := string(renderSlackMrkdwn(`release day ¯\\\_(ツ)\_/¯ and \*literal\*`))
	if rendered != `release day ¯\_(ツ)_/¯ and *literal*` {
		t.Fatalf("rendered=%q", rendered)
	}
}

func TestActionElementListProjectsSupportedBlockKitControlsWithoutInertFakes(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"actions","block_id":"controls","elements":[
			{"type":"button","action_id":"button","text":{"type":"plain_text","text":"Run"},"value":"run"},
			{"type":"multi_static_select","action_id":"multi","placeholder":{"type":"plain_text","text":"Regions"},"options":[
				{"text":{"type":"plain_text","text":"Europe"},"value":"eu"},
				{"text":{"type":"plain_text","text":"US"},"value":"us"}
			],"initial_options":[{"text":{"type":"plain_text","text":"US"},"value":"us"}]},
			{"type":"checkboxes","action_id":"checks","options":[
				{"text":{"type":"plain_text","text":"Alert"},"value":"alert"},
				{"text":{"type":"plain_text","text":"Audit"},"value":"audit"}
			],"initial_options":[{"text":{"type":"plain_text","text":"Alert"},"value":"alert"}]},
			{"type":"radio_buttons","action_id":"radio","options":[
				{"text":{"type":"plain_text","text":"Now"},"value":"now"},
				{"text":{"type":"plain_text","text":"Later"},"value":"later"}
			],"initial_option":{"text":{"type":"plain_text","text":"Later"},"value":"later"}},
			{"type":"datetimepicker","action_id":"when"},
			{"type":"users_select","action_id":"owner"},
			{"type":"multi_conversations_select","action_id":"targets"},
			{"type":"external_select","action_id":"external","placeholder":{"type":"plain_text","text":"Remote"}}
		]}
	]`)
	if len(blocks) != 1 {
		t.Fatalf("blocks=%+v", blocks)
	}
	actions := blocks[0].Actions
	if len(actions) != 8 {
		t.Fatalf("actions=%+v, want every supported control including dynamic external selects", actions)
	}
	byID := make(map[string]messageActionView, len(actions))
	for _, action := range actions {
		byID[action.ActionID] = action
	}
	if byID["button"].Control != "button" || byID["multi"].Control != "select" || !byID["multi"].Multiple ||
		byID["checks"].Control != "checkbox" || byID["radio"].Control != "radio" ||
		byID["when"].Control != "datetime" || byID["owner"].Control != "select" ||
		!byID["targets"].Multiple {
		t.Fatalf("controls=%+v", byID)
	}
	if !byID["multi"].Options[1].Selected || !byID["checks"].Options[0].Selected || !byID["radio"].Options[1].Selected {
		t.Fatalf("initial selections were lost: multi=%+v checks=%+v radio=%+v", byID["multi"], byID["checks"], byID["radio"])
	}
	if byID["external"].Control != "external" || byID["external"].MinQueryLength != 3 {
		t.Fatalf("external select=%+v, want a dynamic app-backed search control", byID["external"])
	}
}

func TestInputBlocksRemainStateOnlyUnlessSlackRequestsDispatch(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"input","block_id":"draft","label":{"type":"plain_text","text":"Draft"},"element":{"type":"plain_text_input","action_id":"draft_text"}},
		{"type":"input","block_id":"filter","dispatch_action":true,"label":{"type":"plain_text","text":"Filter"},"element":{"type":"plain_text_input","action_id":"filter_text"}}
	]`)
	if len(blocks) != 2 || len(blocks[0].Actions) != 1 || len(blocks[1].Actions) != 1 {
		t.Fatalf("input blocks=%+v", blocks)
	}
	if blocks[0].Text != "Draft" || blocks[0].Actions[0].Dispatch {
		t.Fatalf("state-only input=%+v", blocks[0])
	}
	if blocks[1].Text != "Filter" || !blocks[1].Actions[0].Dispatch {
		t.Fatalf("dispatching input=%+v", blocks[1])
	}
}

func TestCurrentMarkdownTableAndContextActionBlocksHaveTruthfulProjections(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"markdown","text":"## Result\nComplete"},
		{"type":"table","rows":[
			[{"type":"raw_text","text":"Service"},{"type":"raw_number","value":42}],
			[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"API"}]}]},{"type":"raw_text","text":"Healthy"}]
		]},
		{"type":"context_actions","block_id":"context","elements":[
			{"type":"icon_button","icon":"trash","text":{"type":"plain_text","text":"Delete"},"action_id":"delete","value":"item-1"},
			{"type":"icon_button","icon":"trash","text":{"type":"plain_text","text":"Private delete"},"action_id":"private","visible_to_user_ids":["U1"]}
		]}
	]`)
	if len(blocks) != 3 {
		t.Fatalf("blocks=%+v", blocks)
	}
	if blocks[0].Text != "## Result\nComplete" {
		t.Fatalf("markdown=%+v", blocks[0])
	}
	if got := string(blocks[0].HTML); !strings.Contains(got, "<h2>Result</h2>") || !strings.Contains(got, "<p>Complete</p>") {
		t.Fatalf("rendered markdown=%q", got)
	}
	if len(blocks[1].Table) != 2 || blocks[1].Table[0][1] != "42" || blocks[1].Table[1][0] != "API" {
		t.Fatalf("table=%+v", blocks[1].Table)
	}
	if len(blocks[2].Actions) != 1 || blocks[2].Actions[0].Type != "icon_button" ||
		blocks[2].Actions[0].ActionID != "delete" {
		t.Fatalf("context actions=%+v", blocks[2].Actions)
	}
}

func TestFeedbackButtonsProjectBothChoicesAndAccessibilityLabels(t *testing.T) {
	blocks := decodeMessageBlocks(`[{
		"type":"context_actions","block_id":"answer-feedback","elements":[{
			"type":"feedback_buttons","action_id":"feedback",
			"positive_button":{"text":{"type":"plain_text","text":"Good"},"value":"positive","accessibility_label":"Mark this answer as useful"},
			"negative_button":{"text":{"type":"plain_text","text":"Bad"},"value":"negative","accessibility_label":"Mark this answer as not useful"}
		}]
	}]`)
	if len(blocks) != 1 || len(blocks[0].Actions) != 2 {
		t.Fatalf("feedback blocks=%+v", blocks)
	}
	positive, negative := blocks[0].Actions[0], blocks[0].Actions[1]
	if positive.Type != "feedback_buttons" || positive.ActionID != "feedback" || positive.Value != "positive" ||
		positive.Tone != "positive" || positive.AccessibilityLabel != "Mark this answer as useful" ||
		negative.Value != "negative" || negative.Tone != "negative" ||
		negative.AccessibilityLabel != "Mark this answer as not useful" {
		t.Fatalf("feedback actions=%+v", blocks[0].Actions)
	}
}

func TestCurrentAlertCardAndCarouselBlocksRenderTheirDocumentedStructureSafely(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"alert","level":"warning","text":{"type":"mrkdwn","text":"*Check* the deployment"}},
		{"type":"card","block_id":"release-card",
		 "icon":{"type":"image","image_url":"https://example.com/icon.png","alt_text":"Release"},
		 "title":{"type":"mrkdwn","text":"*Release 42*"},"subtitle":{"type":"plain_text","text":"Production"},
		 "body":{"type":"mrkdwn","text":"Healthy <script>alert(1)</script>"},
		 "actions":[{"type":"button","action_id":"open_release","text":{"type":"plain_text","text":"Open"},"value":"42"}]},
		{"type":"carousel","elements":[
		 {"type":"card","block_id":"one","title":{"type":"plain_text","text":"First"},"hero_image":{"type":"image","image_url":"javascript:alert(1)","alt_text":"Unsafe"}},
		 {"type":"card","block_id":"two","title":{"type":"plain_text","text":"Second"},"body":{"type":"plain_text","text":"Details"}}
		]}
	]`)
	if len(blocks) != 3 {
		t.Fatalf("blocks=%+v", blocks)
	}
	if blocks[0].Kind != "alert warning" || !strings.Contains(string(blocks[0].HTML), "<strong>Check</strong>") {
		t.Fatalf("alert=%+v", blocks[0])
	}
	card := string(blocks[1].HTML)
	if blocks[1].Kind != "card" || len(blocks[1].Actions) != 1 ||
		!strings.Contains(card, `src="https://example.com/icon.png"`) ||
		!strings.Contains(card, "<strong>Release 42</strong>") ||
		strings.Contains(card, "<script>") {
		t.Fatalf("card=%+v html=%q", blocks[1], card)
	}
	carousel := string(blocks[2].HTML)
	if blocks[2].Kind != "carousel" || !strings.Contains(carousel, "First") ||
		!strings.Contains(carousel, "Second") || strings.Contains(carousel, "javascript:") {
		t.Fatalf("carousel=%+v html=%q", blocks[2], carousel)
	}
}

func TestCurrentPlanTaskCardAndDataTableBlocksRenderSemantically(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"task_card","task_id":"fetch","title":"Fetch profile","status":"complete",
		 "output":{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Profile loaded","style":{"bold":true}}]}]},
		 "sources":[{"type":"url","text":"Profile API","url":"https://example.com/profile"}]},
		{"type":"plan","title":"Thinking completed","tasks":[
		 {"task_id":"one","title":"Check permissions","status":"in_progress","details":{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Inspecting roles"}]}]}},
		 {"task_id":"two","title":"Build report","status":"pending"}
		]},
		{"type":"data_table","caption":"Service health","rows":[
		 [{"type":"raw_text","text":"Service"},{"type":"raw_text","text":"Latency"}],
		 [{"type":"raw_text","text":"API"},{"type":"raw_number","value":42}]
		]}
	]`)
	if len(blocks) != 3 {
		t.Fatalf("blocks=%+v", blocks)
	}
	if blocks[0].Kind != "task-card timeline complete" ||
		!strings.Contains(string(blocks[0].HTML), "<strong>Profile loaded</strong>") ||
		!strings.Contains(string(blocks[0].HTML), `href="https://example.com/profile"`) {
		t.Fatalf("task=%+v", blocks[0])
	}
	plan := string(blocks[1].HTML)
	if blocks[1].Kind != "plan" || !strings.Contains(plan, "Thinking completed") ||
		!strings.Contains(plan, "Check permissions") || !strings.Contains(plan, "Build report") {
		t.Fatalf("plan=%+v", blocks[1])
	}
	if blocks[2].Kind != "data-table" || blocks[2].Caption != "Service health" || !blocks[2].HeaderRow ||
		len(blocks[2].Table) != 2 || blocks[2].Table[1][1] != "42" {
		t.Fatalf("data table=%+v", blocks[2])
	}
}

func TestOnlyDataTableDeclaresItsFirstRowAsAHeader(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"table","rows":[[{"type":"raw_text","text":"A"}],[{"type":"raw_text","text":"B"}]]},
		{"type":"data_table","caption":"Values","rows":[[{"type":"raw_text","text":"A"}],[{"type":"raw_text","text":"B"}]]}
	]`)
	if len(blocks) != 2 || blocks[0].HeaderRow || !blocks[1].HeaderRow {
		t.Fatalf("blocks=%+v", blocks)
	}
}

func TestCurrentContainerAndDataVisualizationBlocksRenderAccessibly(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"container","width":"wide","title":{"type":"plain_text","text":"Bulk update"},
		 "subtitle":{"type":"mrkdwn","text":"Review *two* records"},"is_collapsible":true,"default_collapsed":true,
		 "child_blocks":[
			{"type":"section","text":{"type":"mrkdwn","text":"*DCW-1024* → Closed"}},
			{"type":"divider"},
			{"type":"context","elements":[{"type":"mrkdwn","text":"Ready <script>alert(1)</script>"}]}
		 ]},
		{"type":"data_visualization","title":"Favorite pies","chart":{"type":"pie","segments":[
			{"label":"Pumpkin","value":70},{"label":"Blueberry","value":30}
		]}},
		{"type":"data_visualization","title":"Weekly sales","chart":{"type":"line",
		 "series":[{"name":"Web","data":[{"label":"Week 1","value":32},{"label":"Week 2","value":45}]}],
		 "axis_config":{"categories":["Week 1","Week 2"],"x_label":"Week","y_label":"Sales"}}}
	]`)
	if len(blocks) != 3 {
		t.Fatalf("blocks=%+v", blocks)
	}
	container := string(blocks[0].HTML)
	if blocks[0].Kind != "container wide" || !strings.Contains(container, `<details class="block-container-frame">`) ||
		!strings.Contains(container, "<strong>DCW-1024</strong>") || strings.Contains(container, "<script>") {
		t.Fatalf("container=%+v html=%q", blocks[0], container)
	}
	pie := string(blocks[1].HTML)
	if !strings.Contains(pie, "conic-gradient(") || !strings.Contains(pie, `<th scope="row">Pumpkin</th>`) ||
		!strings.Contains(pie, "<td>70</td>") {
		t.Fatalf("pie=%+v html=%q", blocks[1], pie)
	}
	line := string(blocks[2].HTML)
	if !strings.Contains(line, `<svg class="block-chart-svg"`) || !strings.Contains(line, "<polyline") ||
		!strings.Contains(line, `<th scope="col">Web</th>`) || !strings.Contains(line, "<td>45</td>") {
		t.Fatalf("line=%+v html=%q", blocks[2], line)
	}
}

func TestSlackMrkdwnAndRichTextRenderFormattingWithoutTrustingAppHTML(t *testing.T) {
	blocks := decodeMessageBlocks(`[
		{"type":"section","text":{"type":"mrkdwn","text":"*Ready* for <https://example.com/build|build> and <@U123> <script>alert(1)</script>"}},
		{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[
			{"type":"text","text":"Strong","style":{"bold":true}},
			{"type":"text","text":" <img src=x onerror=alert(1)>"},
			{"type":"link","url":"javascript:alert(1)","text":"unsafe"}
		]}]}
	]`)
	if len(blocks) != 2 {
		t.Fatalf("blocks=%+v", blocks)
	}
	mrkdwn := string(blocks[0].HTML)
	if !strings.Contains(mrkdwn, "<strong>Ready</strong>") ||
		!strings.Contains(mrkdwn, `href="https://example.com/build"`) ||
		!strings.Contains(mrkdwn, `<span class="slack-mention">@U123</span>`) {
		t.Fatalf("mrkdwn=%q", mrkdwn)
	}
	rich := string(blocks[1].HTML)
	for name, output := range map[string]string{"mrkdwn": mrkdwn, "rich text": rich} {
		if strings.Contains(output, "<script>") || strings.Contains(output, "<img") ||
			strings.Contains(output, "javascript:") {
			t.Fatalf("%s trusted app markup: %q", name, output)
		}
	}
	if !strings.Contains(rich, "<strong>Strong</strong>") ||
		!strings.Contains(rich, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("rich text=%q", rich)
	}
}

func TestTopLevelMessageTextRendersSlackMarkupSafely(t *testing.T) {
	content := newRichMessageContent(domain.Message{Text: `*Ready* <@U1|@Ada> <script>alert(1)</script>`})
	rendered := string(content.Text)
	if !strings.Contains(rendered, "<strong>Ready</strong>") || !strings.Contains(rendered, `<span class="slack-mention">@Ada</span>`) {
		t.Fatalf("rendered=%q", rendered)
	}
	if strings.Contains(rendered, "<script>") {
		t.Fatalf("top-level message trusted unsafe markup: %q", rendered)
	}
}

func TestMarkdownBlockEscapesRawHTMLAndDangerousLinks(t *testing.T) {
	output := string(renderMarkdown("# Safe\n<script>alert(1)</script>\n[unsafe](javascript:alert(1))"))
	if !strings.Contains(output, "<h1>Safe</h1>") {
		t.Fatalf("markdown=%q", output)
	}
	if strings.Contains(output, "<script>") || strings.Contains(output, `href="javascript:`) {
		t.Fatalf("markdown trusted unsafe content: %q", output)
	}
}

func TestStreamingMessagesProjectMarkdownTasksChunkBlocksAndActiveState(t *testing.T) {
	message := domain.Message{
		Text:   "**Answer**",
		Blocks: `[{"type":"context","elements":[{"type":"plain_text","text":"Final block"}]}]`,
		StreamState: `{
			"active":true,
			"task_display_mode":"dense",
			"username":"Release assistant",
			"icon_emoji":":rocket:",
			"plan_title":"Release plan",
			"tasks":[{"id":"deploy","title":"Deploy API","status":"complete","output":"Healthy","sources":[{"type":"url","text":"Runbook","url":"https://example.com/runbook"}]}],
			"chunk_blocks":[{"type":"section","text":{"type":"plain_text","text":"Chunk block"}}]
		}`,
	}
	content := newRichMessageContent(message)
	if !messageStreamActive(message.StreamState) || len(content.Blocks) != 5 {
		t.Fatalf("content=%+v", content)
	}
	presentation := decodeMessageStreamPresentation(message.StreamState)
	if presentation.Username != "Release assistant" || presentation.IconEmoji != ":rocket:" {
		t.Fatalf("presentation=%+v", presentation)
	}
	if got := string(content.Blocks[0].HTML); !strings.Contains(got, "<strong>Answer</strong>") {
		t.Fatalf("stream markdown=%q", got)
	}
	if content.Blocks[1].Kind != "header" || content.Blocks[1].Text != "Release plan" {
		t.Fatalf("plan=%+v", content.Blocks[1])
	}
	task := string(content.Blocks[2].HTML)
	if content.Blocks[2].Kind != "task-card dense complete" {
		t.Fatalf("task kind=%q", content.Blocks[2].Kind)
	}
	if !strings.Contains(task, "Deploy API") || !strings.Contains(task, "Complete") ||
		!strings.Contains(task, `href="https://example.com/runbook"`) {
		t.Fatalf("task=%q", task)
	}
	if content.Blocks[3].Text != "Chunk block" || content.Blocks[4].Text != "Final block" {
		t.Fatalf("block order=%+v", content.Blocks)
	}
}
