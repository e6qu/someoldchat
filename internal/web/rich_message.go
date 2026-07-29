package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// richMessageContent is the browser projection of the structured content the
// Slack API persists on a message. The API deliberately stores normalized JSON
// rather than a browser-specific model; this projection keeps that transport
// shape out of the template while making block-only and attachment-only
// messages visible to people who use the first-party client.
type richMessageContent struct {
	Text        template.HTML
	Blocks      []messageBlockView
	Attachments []messageAttachmentView
	Unfurls     []messageAttachmentView
}

type messageBlockView struct {
	Kind      string
	Text      string
	HTML      template.HTML
	Fields    []string
	FieldHTML []template.HTML
	ImageURL  string
	ImageAlt  string
	LinkURL   string
	LinkLabel string
	Table     [][]string
	Caption   string
	HeaderRow bool
	Actions   []messageActionView
}

type messageActionView struct {
	Type               string
	ActionID           string
	BlockID            string
	Text               string
	Value              string
	Options            []messageActionOptionView
	Multiple           bool
	Multiline          bool
	Control            string
	InitialValues      []string
	MinQueryLength     int
	Dispatch           bool
	AccessibilityLabel string
	Tone               string
}

type messageActionOptionView struct {
	Text     string
	Value    string
	Selected bool
}

type messageAttachmentView struct {
	SourceURL string
	Pretext   string
	Author    string
	Title     string
	TitleURL  string
	Text      string
	Fields    []messageAttachmentFieldView
	Footer    string
	ImageURL  string
	ImageAlt  string
	Blocks    []messageBlockView
}

type messageAttachmentFieldView struct {
	Title string
	Value string
}

func newRichMessageContent(message domain.Message) richMessageContent {
	streamBlocks, hasStream := decodeMessageStream(message)
	content := richMessageContent{
		Blocks:      append(streamBlocks, decodeMessageBlocks(message.Blocks)...),
		Attachments: decodeMessageAttachments(message.Attachments),
		Unfurls:     decodeMessageUnfurls(message.Unfurls),
	}
	// Slack treats top-level text as notification and accessibility fallback
	// when blocks are present. Rendering it beside the blocks repeats the same
	// message for both visual and assistive-technology users. If a malformed or
	// wholly unknown block payload somehow reaches a legacy store, retain the
	// fallback rather than rendering a blank row.
	if len(content.Blocks) == 0 && !hasStream {
		content.Text = renderSlackMrkdwn(message.Text)
	}
	return content
}

func decodeMessageStream(message domain.Message) ([]messageBlockView, bool) {
	if strings.TrimSpace(message.StreamState) == "" {
		return nil, false
	}
	var state domain.MessageStreamState
	if json.Unmarshal([]byte(message.StreamState), &state) != nil {
		return nil, false
	}
	result := make([]messageBlockView, 0, 2+len(state.Tasks)+len(state.ChunkBlocks))
	if message.Text != "" {
		result = append(result, messageBlockView{Kind: "markdown", Text: message.Text, HTML: renderMarkdown(message.Text)})
	}
	if state.PlanTitle != "" {
		result = append(result, messageBlockView{Kind: "header", Text: state.PlanTitle})
	}
	for _, raw := range state.Tasks {
		if block, ok := streamTaskBlock(raw, state.TaskDisplayMode); ok {
			result = append(result, block)
		}
	}
	if len(state.ChunkBlocks) != 0 {
		encoded, err := json.Marshal(state.ChunkBlocks)
		if err == nil {
			result = append(result, decodeMessageBlocks(string(encoded))...)
		}
	}
	return result, true
}

func messageStreamActive(raw string) bool {
	var state domain.MessageStreamState
	return json.Unmarshal([]byte(raw), &state) == nil && state.Active
}

type messageStreamPresentation struct {
	Username  string
	IconEmoji string
	IconURL   string
}

func decodeMessageStreamPresentation(raw string) messageStreamPresentation {
	var state domain.MessageStreamState
	if json.Unmarshal([]byte(raw), &state) != nil {
		return messageStreamPresentation{}
	}
	return messageStreamPresentation{Username: state.Username, IconEmoji: state.IconEmoji, IconURL: state.IconURL}
}

func streamTaskBlock(raw json.RawMessage, displayMode string) (messageBlockView, bool) {
	var task map[string]any
	if json.Unmarshal(raw, &task) != nil {
		return messageBlockView{}, false
	}
	title := strings.TrimSpace(stringValue(task["title"]))
	status := strings.TrimSpace(stringValue(task["status"]))
	if title == "" || status == "" {
		return messageBlockView{}, false
	}
	statusLabel := map[string]string{"pending": "Pending", "in_progress": "In progress", "complete": "Complete", "error": "Error"}[status]
	var output strings.Builder
	output.WriteString(`<div class="stream-task-title"><strong>`)
	output.WriteString(template.HTMLEscapeString(title))
	output.WriteString(`</strong><span class="stream-task-status">`)
	output.WriteString(template.HTMLEscapeString(statusLabel))
	output.WriteString(`</span></div>`)
	if details := strings.TrimSpace(stringValue(task["details"])); details != "" {
		output.WriteString("<p>")
		output.WriteString(template.HTMLEscapeString(details))
		output.WriteString("</p>")
	}
	if value := strings.TrimSpace(stringValue(task["output"])); value != "" {
		output.WriteString(`<p class="stream-task-output">`)
		output.WriteString(template.HTMLEscapeString(value))
		output.WriteString("</p>")
	}
	sources, _ := task["sources"].([]any)
	if len(sources) != 0 {
		output.WriteString(`<ul class="stream-task-sources">`)
		for _, rawSource := range sources {
			source, _ := rawSource.(map[string]any)
			href, ok := safeSlackLink(strings.TrimSpace(stringValue(source["url"])))
			if !ok {
				continue
			}
			label := strings.TrimSpace(stringValue(source["text"]))
			if label == "" {
				label = href
			}
			output.WriteString(`<li><a href="`)
			output.WriteString(template.HTMLEscapeString(href))
			output.WriteString(`" rel="noreferrer noopener">`)
			output.WriteString(template.HTMLEscapeString(label))
			output.WriteString("</a></li>")
		}
		output.WriteString("</ul>")
	}
	if !oneOfString(displayMode, "timeline", "plan", "dense") {
		displayMode = "timeline"
	}
	return messageBlockView{
		Kind: "task-card " + displayMode + " " + status, Text: title + " — " + statusLabel,
		HTML: template.HTML(output.String()), // #nosec G203 -- every task value is escaped and source URLs are scheme-validated.
	}, true
}

func oneOfString(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func hasStructuredMessageContent(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && raw != "[]"
}

func decodeMessageBlocks(raw string) []messageBlockView {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []map[string]any
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil
	}
	blocks := make([]messageBlockView, 0, len(values))
	for _, value := range values {
		block, ok := newMessageBlockView(value)
		if ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func newMessageBlockView(value map[string]any) (messageBlockView, bool) {
	blockType := strings.TrimSpace(stringValue(value["type"]))
	block := messageBlockView{Kind: blockType}
	switch blockType {
	case "divider":
		return block, true
	case "header":
		block.Text = textObjectValue(value["text"])
	case "section":
		block.Text, block.HTML = formattedTextObject(value["text"])
		block.Fields, block.FieldHTML = formattedTextObjectList(value["fields"])
		if accessory, ok := value["accessory"].(map[string]any); ok {
			block.ImageURL, block.ImageAlt = imageElement(accessory)
			if block.ImageURL == "" {
				block.Actions = actionElementList([]any{accessory}, strings.TrimSpace(stringValue(value["block_id"])))
			}
		}
	case "context":
		block.Text, block.HTML = formattedElementList(value["elements"], " · ")
	case "image":
		block.ImageURL = strings.TrimSpace(stringValue(value["image_url"]))
		block.ImageAlt = strings.TrimSpace(stringValue(value["alt_text"]))
		block.Text = textObjectValue(value["title"])
	case "actions":
		block.Actions = actionElementList(value["elements"], strings.TrimSpace(stringValue(value["block_id"])))
	case "context_actions":
		block.Actions = actionElementList(value["elements"], strings.TrimSpace(stringValue(value["block_id"])))
	case "input":
		block.Text = textObjectValue(value["label"])
		element, _ := value["element"].(map[string]any)
		block.Actions = actionElementList([]any{element}, strings.TrimSpace(stringValue(value["block_id"])))
		for index := range block.Actions {
			block.Actions[index].Dispatch = boolValue(value["dispatch_action"])
		}
	case "video":
		block.Text = textObjectValue(value["title"])
		if description := textObjectValue(value["description"]); description != "" {
			if block.Text != "" {
				block.Text += "\n"
			}
			block.Text += description
		}
		block.ImageURL = strings.TrimSpace(stringValue(value["thumbnail_url"]))
		block.ImageAlt = strings.TrimSpace(stringValue(value["alt_text"]))
		block.LinkURL = strings.TrimSpace(stringValue(value["video_url"]))
		block.LinkLabel = "Open video"
	case "rich_text":
		block.Kind = "section"
		block.Text = strings.Join(elementTextList(value["elements"]), "\n")
		block.HTML = renderRichText(value["elements"])
	case "markdown":
		block.Text = strings.TrimSpace(stringValue(value["text"]))
		block.HTML = renderMarkdown(block.Text)
	case "table":
		block.Table = tableRows(value["rows"])
	case "data_table":
		block.Kind = "data-table"
		block.Table = tableRows(value["rows"])
		block.Caption = strings.TrimSpace(stringValue(value["caption"]))
		block.HeaderRow = true
	case "alert":
		block.Kind = "alert " + alertLevel(value["level"])
		block.Text, block.HTML = formattedTextObject(value["text"])
	case "card":
		block.Kind = "card"
		block.Text, block.HTML = cardBlockContent(value)
		block.Actions = actionElementList(value["actions"], strings.TrimSpace(stringValue(value["block_id"])))
	case "carousel":
		block.Kind = "carousel"
		block.Text, block.HTML, block.Actions = carouselBlockContent(value)
	case "container":
		block.Kind = "container " + containerWidth(value["width"])
		block.Text, block.HTML, block.Actions = containerBlockContent(value)
	case "data_visualization":
		block.Kind = "data-visualization"
		block.Text, block.HTML = dataVisualizationBlockContent(value)
	case "task_card":
		block = currentTaskCardBlock(value, "timeline")
	case "plan":
		block.Kind = "plan"
		block.Text, block.HTML = planBlockContent(value)
	default:
		// Block Kit grows over time. Unknown blocks still commonly contain text
		// objects or nested elements; extract those human-facing values instead
		// of exposing implementation JSON or silently dropping the message.
		block.Kind = "section"
		block.Text = strings.Join(elementTextList(value["elements"]), "\n")
		if block.Text == "" {
			block.Text = textObjectValue(value["text"])
		}
	}
	if block.Kind == "" {
		block.Kind = "section"
	}
	return block, block.Text != "" || block.HTML != "" || len(block.Fields) > 0 || block.ImageURL != "" || len(block.Table) > 0 || len(block.Actions) > 0 || blockType == "divider"
}

func containerWidth(value any) string {
	width := strings.TrimSpace(stringValue(value))
	if oneOfString(width, "narrow", "standard", "wide", "full") {
		return width
	}
	return "standard"
}

func containerBlockContent(value map[string]any) (string, template.HTML, []messageActionView) {
	title, titleHTML := formattedTextObject(value["title"])
	if richTitle, ok := value["rich_text_title"].(map[string]any); ok &&
		strings.TrimSpace(stringValue(richTitle["type"])) == "rich_text" {
		if rendered := renderRichText(richTitle["elements"]); rendered != "" {
			title = strings.Join(elementTextList(richTitle["elements"]), "\n")
			titleHTML = rendered
		}
	}
	if title == "" {
		return "", "", nil
	}
	if titleHTML == "" {
		titleHTML = template.HTML(template.HTMLEscapeString(title))
	}
	subtitle, subtitleHTML := formattedTextObject(value["subtitle"])
	if subtitle != "" && subtitleHTML == "" {
		subtitleHTML = template.HTML(template.HTMLEscapeString(subtitle))
	}
	var header strings.Builder
	if icon, ok := value["icon"].(map[string]any); ok {
		appendCardImage(&header, icon, "block-container-icon")
	}
	header.WriteString(`<span class="block-container-heading"><strong>`)
	header.WriteString(string(titleHTML))
	header.WriteString(`</strong>`)
	if subtitle != "" {
		header.WriteString(`<span>`)
		header.WriteString(string(subtitleHTML))
		header.WriteString(`</span>`)
	}
	header.WriteString(`</span>`)

	var body strings.Builder
	var textParts []string
	var actions []messageActionView
	children, _ := value["child_blocks"].([]any)
	for _, raw := range children {
		child, _ := raw.(map[string]any)
		view, ok := newMessageBlockView(child)
		if !ok {
			continue
		}
		textParts = append(textParts, view.Text)
		actions = append(actions, view.Actions...)
		appendNestedBlockHTML(&body, view)
	}

	var output strings.Builder
	collapsible := boolValue(value["is_collapsible"])
	if collapsible {
		output.WriteString(`<details class="block-container-frame"`)
		if !boolValue(value["default_collapsed"]) {
			output.WriteString(` open`)
		}
		output.WriteString(`><summary>`)
		output.WriteString(header.String())
		output.WriteString(`</summary><div class="block-container-children">`)
		output.WriteString(body.String())
		output.WriteString(`</div></details>`)
	} else {
		output.WriteString(`<section class="block-container-frame"><header`)
		if boolValue(value["has_header_divider"]) {
			output.WriteString(` class="with-divider"`)
		}
		output.WriteString(`>`)
		output.WriteString(header.String())
		output.WriteString(`</header><div class="block-container-children">`)
		output.WriteString(body.String())
		output.WriteString(`</div></section>`)
	}
	textParts = append([]string{title, subtitle}, textParts...)
	return strings.Join(nonEmptyStrings(textParts), "\n"), template.HTML(output.String()), actions // #nosec G203 -- all app text is escaped by the safe block renderers.
}

func appendNestedBlockHTML(output *strings.Builder, block messageBlockView) {
	if block.Kind == "divider" {
		output.WriteString(`<hr class="message-block divider">`)
		return
	}
	output.WriteString(`<div class="block-container-child message-block `)
	output.WriteString(template.HTMLEscapeString(block.Kind))
	output.WriteString(`">`)
	if block.HTML != "" {
		output.WriteString(`<div class="formatted-text">`)
		output.WriteString(string(block.HTML))
		output.WriteString(`</div>`)
	} else if block.Text != "" {
		output.WriteString(template.HTMLEscapeString(block.Text))
	}
	if len(block.Fields) != 0 {
		output.WriteString(`<ul class="message-block-fields">`)
		for index, field := range block.Fields {
			output.WriteString(`<li>`)
			if index < len(block.FieldHTML) && block.FieldHTML[index] != "" {
				output.WriteString(string(block.FieldHTML[index]))
			} else {
				output.WriteString(template.HTMLEscapeString(field))
			}
			output.WriteString(`</li>`)
		}
		output.WriteString(`</ul>`)
	}
	if len(block.Table) != 0 {
		appendBlockTableHTML(output, block)
	}
	if source, ok := safeSlackLink(block.ImageURL); ok &&
		(strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://")) {
		output.WriteString(`<img class="message-media" src="`)
		output.WriteString(template.HTMLEscapeString(source))
		output.WriteString(`" alt="`)
		output.WriteString(template.HTMLEscapeString(block.ImageAlt))
		output.WriteString(`" loading="lazy">`)
	}
	output.WriteString(`</div>`)
}

func appendBlockTableHTML(output *strings.Builder, block messageBlockView) {
	output.WriteString(`<div class="block-table-wrap"><table class="block-table">`)
	if block.Caption != "" {
		output.WriteString(`<caption>`)
		output.WriteString(template.HTMLEscapeString(block.Caption))
		output.WriteString(`</caption>`)
	}
	output.WriteString(`<tbody>`)
	for rowIndex, row := range block.Table {
		output.WriteString(`<tr>`)
		for _, cell := range row {
			if block.HeaderRow && rowIndex == 0 {
				output.WriteString(`<th scope="col">`)
				output.WriteString(template.HTMLEscapeString(cell))
				output.WriteString(`</th>`)
			} else {
				output.WriteString(`<td>`)
				output.WriteString(template.HTMLEscapeString(cell))
				output.WriteString(`</td>`)
			}
		}
		output.WriteString(`</tr>`)
	}
	output.WriteString(`</tbody></table></div>`)
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func dataVisualizationBlockContent(value map[string]any) (string, template.HTML) {
	title := strings.TrimSpace(stringValue(value["title"]))
	chart, _ := value["chart"].(map[string]any)
	chartType := strings.TrimSpace(stringValue(chart["type"]))
	if title == "" || !oneOfString(chartType, "pie", "bar", "area", "line") {
		return "", ""
	}
	var output strings.Builder
	output.WriteString(`<figure class="block-chart `)
	output.WriteString(template.HTMLEscapeString(chartType))
	output.WriteString(`"><figcaption>`)
	output.WriteString(template.HTMLEscapeString(title))
	output.WriteString(`</figcaption>`)
	var textParts = []string{title}
	if chartType == "pie" {
		textParts = append(textParts, appendPieChartHTML(&output, chart)...)
	} else {
		textParts = append(textParts, appendSeriesChartHTML(&output, chart, chartType)...)
	}
	output.WriteString(`</figure>`)
	return strings.Join(nonEmptyStrings(textParts), "\n"), template.HTML(output.String()) // #nosec G203 -- labels are escaped; styles and SVG coordinates are generated exclusively from finite numeric values.
}

var chartColors = []string{
	"#1264a3", "#2eb67d", "#ecb22e", "#e01e5a", "#611f69", "#36c5f0",
	"#007a5a", "#d72b3f", "#4a154b", "#f2c744", "#1d9bd1", "#8c5fc2",
}

type chartPoint struct {
	Label string
	Value float64
}

type chartSeries struct {
	Name   string
	Points []chartPoint
}

func appendPieChartHTML(output *strings.Builder, chart map[string]any) []string {
	rawSegments, _ := chart["segments"].([]any)
	segments := make([]chartPoint, 0, len(rawSegments))
	total := 0.0
	for _, raw := range rawSegments {
		segment, _ := raw.(map[string]any)
		label := strings.TrimSpace(stringValue(segment["label"]))
		numeric, ok := finiteNumber(segment["value"])
		if label == "" || !ok || numeric <= 0 {
			continue
		}
		segments = append(segments, chartPoint{Label: label, Value: numeric})
		total += numeric
	}
	if len(segments) == 0 || total <= 0 {
		return nil
	}
	var stops []string
	cursor := 0.0
	for index, segment := range segments {
		next := cursor + segment.Value/total*100
		stops = append(stops, fmt.Sprintf("%s %.4f%% %.4f%%", chartColors[index%len(chartColors)], cursor, next))
		cursor = next
	}
	output.WriteString(`<div class="block-chart-pie-layout"><div class="block-chart-pie-graphic" role="img" aria-label="Pie chart" style="background:conic-gradient(`)
	output.WriteString(strings.Join(stops, ","))
	output.WriteString(`)"></div><ul class="block-chart-legend">`)
	for index, segment := range segments {
		output.WriteString(`<li><span class="block-chart-swatch" style="background:`)
		output.WriteString(chartColors[index%len(chartColors)])
		output.WriteString(`"></span><span>`)
		output.WriteString(template.HTMLEscapeString(segment.Label))
		output.WriteString(`</span><strong>`)
		output.WriteString(template.HTMLEscapeString(formatChartNumber(segment.Value)))
		output.WriteString(`</strong></li>`)
	}
	output.WriteString(`</ul></div>`)
	appendChartDataTable(output, nil, []chartSeries{{Name: "Value", Points: segments}})
	text := make([]string, 0, len(segments))
	for _, segment := range segments {
		text = append(text, segment.Label+": "+formatChartNumber(segment.Value))
	}
	return text
}

func appendSeriesChartHTML(output *strings.Builder, chart map[string]any, chartType string) []string {
	axis, _ := chart["axis_config"].(map[string]any)
	categories := stringList(axis["categories"])
	rawSeries, _ := chart["series"].([]any)
	series := make([]chartSeries, 0, len(rawSeries))
	for _, raw := range rawSeries {
		value, _ := raw.(map[string]any)
		item := chartSeries{Name: strings.TrimSpace(stringValue(value["name"]))}
		rawData, _ := value["data"].([]any)
		for _, rawPoint := range rawData {
			point, _ := rawPoint.(map[string]any)
			number, ok := finiteNumber(point["value"])
			if !ok {
				continue
			}
			item.Points = append(item.Points, chartPoint{Label: strings.TrimSpace(stringValue(point["label"])), Value: number})
		}
		if item.Name != "" && len(item.Points) != 0 {
			series = append(series, item)
		}
	}
	if len(categories) == 0 || len(series) == 0 {
		return nil
	}
	minimum, maximum := series[0].Points[0].Value, series[0].Points[0].Value
	for _, item := range series {
		for _, point := range item.Points {
			if point.Value < minimum {
				minimum = point.Value
			}
			if point.Value > maximum {
				maximum = point.Value
			}
		}
	}
	if minimum > 0 {
		minimum = 0
	}
	if maximum < 0 {
		maximum = 0
	}
	if maximum == minimum {
		maximum = minimum + 1
	}
	if chartType == "bar" {
		appendBarChartHTML(output, categories, series, minimum, maximum)
	} else {
		appendLineChartSVG(output, categories, series, minimum, maximum, chartType == "area")
	}
	appendChartLegend(output, series)
	appendChartDataTable(output, categories, series)
	var text []string
	for _, item := range series {
		for _, point := range item.Points {
			text = append(text, item.Name+" — "+point.Label+": "+formatChartNumber(point.Value))
		}
	}
	return text
}

func appendBarChartHTML(output *strings.Builder, categories []string, series []chartSeries, minimum, maximum float64) {
	output.WriteString(`<div class="block-chart-bars" role="img" aria-label="Bar chart">`)
	for categoryIndex, category := range categories {
		output.WriteString(`<div class="block-chart-bar-group"><span class="block-chart-category">`)
		output.WriteString(template.HTMLEscapeString(category))
		output.WriteString(`</span><div class="block-chart-bar-series">`)
		for seriesIndex, item := range series {
			if categoryIndex >= len(item.Points) {
				continue
			}
			point := item.Points[categoryIndex]
			size := (point.Value - minimum) / (maximum - minimum) * 100
			output.WriteString(`<span class="block-chart-bar" title="`)
			output.WriteString(template.HTMLEscapeString(item.Name + ": " + formatChartNumber(point.Value)))
			output.WriteString(`" style="width:`)
			output.WriteString(strconv.FormatFloat(size, 'f', 3, 64))
			output.WriteString(`%;background:`)
			output.WriteString(chartColors[seriesIndex%len(chartColors)])
			output.WriteString(`"></span>`)
		}
		output.WriteString(`</div></div>`)
	}
	output.WriteString(`</div>`)
}

func appendLineChartSVG(output *strings.Builder, categories []string, series []chartSeries, minimum, maximum float64, area bool) {
	const width, left, top, plotWidth, plotHeight = 560.0, 36.0, 12.0, 512.0, 174.0
	output.WriteString(`<svg class="block-chart-svg" viewBox="0 0 560 220" role="img" aria-label="`)
	if area {
		output.WriteString(`Area chart`)
	} else {
		output.WriteString(`Line chart`)
	}
	output.WriteString(`">`)
	baseline := top + (maximum/(maximum-minimum))*plotHeight
	output.WriteString(fmt.Sprintf(`<line x1="%.2f" y1="%.2f" x2="%.2f" y2="%.2f" class="block-chart-axis"/>`, left, baseline, width-12, baseline))
	for seriesIndex, item := range series {
		coordinates := make([]string, 0, len(item.Points))
		for index, point := range item.Points {
			x := left + plotWidth/float64(maxInt(1, len(categories)-1))*float64(index)
			y := top + (maximum-point.Value)/(maximum-minimum)*plotHeight
			coordinates = append(coordinates, fmt.Sprintf("%.2f,%.2f", x, y))
		}
		color := chartColors[seriesIndex%len(chartColors)]
		if area {
			polygon := append([]string{fmt.Sprintf("%.2f,%.2f", left, baseline)}, coordinates...)
			polygon = append(polygon, fmt.Sprintf("%.2f,%.2f", left+plotWidth, baseline))
			output.WriteString(`<polygon points="`)
			output.WriteString(strings.Join(polygon, " "))
			output.WriteString(`" fill="`)
			output.WriteString(color)
			output.WriteString(`" opacity=".18"/>`)
		}
		output.WriteString(`<polyline points="`)
		output.WriteString(strings.Join(coordinates, " "))
		output.WriteString(`" fill="none" stroke="`)
		output.WriteString(color)
		output.WriteString(`" stroke-width="3" stroke-linejoin="round" stroke-linecap="round"/>`)
	}
	output.WriteString(`</svg>`)
}

func appendChartLegend(output *strings.Builder, series []chartSeries) {
	output.WriteString(`<ul class="block-chart-legend">`)
	for index, item := range series {
		output.WriteString(`<li><span class="block-chart-swatch" style="background:`)
		output.WriteString(chartColors[index%len(chartColors)])
		output.WriteString(`"></span><span>`)
		output.WriteString(template.HTMLEscapeString(item.Name))
		output.WriteString(`</span></li>`)
	}
	output.WriteString(`</ul>`)
}

func appendChartDataTable(output *strings.Builder, categories []string, series []chartSeries) {
	output.WriteString(`<details class="block-chart-data"><summary>View chart data</summary><div class="block-table-wrap"><table class="block-table"><thead><tr><th scope="col">Category</th>`)
	for _, item := range series {
		output.WriteString(`<th scope="col">`)
		output.WriteString(template.HTMLEscapeString(item.Name))
		output.WriteString(`</th>`)
	}
	output.WriteString(`</tr></thead><tbody>`)
	rowCount := 0
	if len(categories) != 0 {
		rowCount = len(categories)
	} else if len(series) != 0 {
		rowCount = len(series[0].Points)
	}
	for index := 0; index < rowCount; index++ {
		label := ""
		if index < len(categories) {
			label = categories[index]
		} else if len(series) != 0 && index < len(series[0].Points) {
			label = series[0].Points[index].Label
		}
		output.WriteString(`<tr><th scope="row">`)
		output.WriteString(template.HTMLEscapeString(label))
		output.WriteString(`</th>`)
		for _, item := range series {
			output.WriteString(`<td>`)
			if index < len(item.Points) {
				output.WriteString(template.HTMLEscapeString(formatChartNumber(item.Points[index].Value)))
			}
			output.WriteString(`</td>`)
		}
		output.WriteString(`</tr>`)
	}
	output.WriteString(`</tbody></table></div></details>`)
}

func finiteNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	if !ok || number != number || number > 1.7976931348623157e308 || number < -1.7976931348623157e308 {
		return 0, false
	}
	return number, true
}

func formatChartNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func planBlockContent(value map[string]any) (string, template.HTML) {
	title := strings.TrimSpace(stringValue(value["title"]))
	if title == "" {
		title = textObjectValue(value["title"])
	}
	var output strings.Builder
	output.WriteString(`<section class="block-plan"><strong class="block-plan-title">`)
	output.WriteString(template.HTMLEscapeString(title))
	output.WriteString(`</strong><div class="block-plan-tasks">`)
	textParts := []string{title}
	tasks, _ := value["tasks"].([]any)
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		view := currentTaskCardBlock(task, "plan")
		if view.HTML == "" {
			continue
		}
		textParts = append(textParts, view.Text)
		output.WriteString(`<article class="block-plan-task ` + template.HTMLEscapeString(strings.TrimPrefix(view.Kind, "task-card ")) + `">`)
		output.WriteString(string(view.HTML))
		output.WriteString(`</article>`)
	}
	output.WriteString(`</div></section>`)
	return strings.Join(textParts, "\n"), template.HTML(output.String()) // #nosec G203 -- every task value is escaped or rendered by the safe rich-text renderer.
}

func currentTaskCardBlock(task map[string]any, mode string) messageBlockView {
	title := strings.TrimSpace(stringValue(task["title"]))
	status := strings.TrimSpace(stringValue(task["status"]))
	if status == "" {
		status = "pending"
	}
	if title == "" || !oneOfString(status, "pending", "in_progress", "complete", "error") {
		return messageBlockView{}
	}
	statusLabel := map[string]string{"pending": "Pending", "in_progress": "In progress", "complete": "Complete", "error": "Error"}[status]
	var output strings.Builder
	output.WriteString(`<div class="stream-task-title"><strong>`)
	output.WriteString(template.HTMLEscapeString(title))
	output.WriteString(`</strong><span class="stream-task-status">`)
	output.WriteString(template.HTMLEscapeString(statusLabel))
	output.WriteString(`</span></div>`)
	for _, field := range []string{"details", "output"} {
		value, _ := task[field].(map[string]any)
		if strings.TrimSpace(stringValue(value["type"])) != "rich_text" {
			continue
		}
		rendered := renderRichText(value["elements"])
		if rendered == "" {
			continue
		}
		output.WriteString(`<div class="stream-task-` + field + `">`)
		output.WriteString(string(rendered))
		output.WriteString(`</div>`)
	}
	appendTaskSources(&output, task["sources"])
	return messageBlockView{
		Kind: "task-card " + mode + " " + status, Text: title + " — " + statusLabel,
		HTML: template.HTML(output.String()), // #nosec G203 -- task values are escaped and sources are scheme-validated.
	}
}

func appendTaskSources(output *strings.Builder, raw any) {
	sources, _ := raw.([]any)
	if len(sources) == 0 {
		return
	}
	output.WriteString(`<ul class="stream-task-sources">`)
	for _, rawSource := range sources {
		source, _ := rawSource.(map[string]any)
		href, ok := safeSlackLink(strings.TrimSpace(stringValue(source["url"])))
		if !ok {
			continue
		}
		label := strings.TrimSpace(stringValue(source["text"]))
		if label == "" {
			label = href
		}
		output.WriteString(`<li><a href="`)
		output.WriteString(template.HTMLEscapeString(href))
		output.WriteString(`" rel="noreferrer noopener">`)
		output.WriteString(template.HTMLEscapeString(label))
		output.WriteString("</a></li>")
	}
	output.WriteString("</ul>")
}

func alertLevel(value any) string {
	level := strings.TrimSpace(stringValue(value))
	if oneOfString(level, "info", "warning", "error", "success") {
		return level
	}
	return "default"
}

func carouselBlockContent(value map[string]any) (string, template.HTML, []messageActionView) {
	elements, _ := value["elements"].([]any)
	var textParts []string
	var actions []messageActionView
	var output strings.Builder
	output.WriteString(`<div class="block-carousel-track">`)
	for _, raw := range elements {
		card, _ := raw.(map[string]any)
		if strings.TrimSpace(stringValue(card["type"])) != "card" {
			continue
		}
		text, body := cardBlockContent(card)
		if text != "" {
			textParts = append(textParts, text)
		}
		output.WriteString(`<article class="block-carousel-card">`)
		output.WriteString(string(body))
		output.WriteString(`</article>`)
		actions = append(actions, actionElementList(card["actions"], strings.TrimSpace(stringValue(card["block_id"])))...)
	}
	output.WriteString(`</div>`)
	return strings.Join(textParts, "\n"), template.HTML(output.String()), actions // #nosec G203 -- cardBlockContent escapes text and validates image URLs.
}

func cardBlockContent(value map[string]any) (string, template.HTML) {
	var textParts []string
	var output strings.Builder
	if image, ok := value["hero_image"].(map[string]any); ok {
		appendCardImage(&output, image, "block-card-hero")
	}
	output.WriteString(`<div class="block-card-content"><div class="block-card-heading">`)
	if image, ok := value["icon"].(map[string]any); ok {
		appendCardImage(&output, image, "block-card-icon")
	}
	output.WriteString(`<div>`)
	for _, field := range []string{"title", "subtitle"} {
		text, formatted := formattedTextObject(value[field])
		if text == "" {
			continue
		}
		if formatted == "" {
			formatted = template.HTML(template.HTMLEscapeString(text))
		}
		textParts = append(textParts, text)
		tag := "strong"
		if field == "subtitle" {
			tag = "span"
		}
		output.WriteString("<" + tag + ` class="block-card-` + field + `">`)
		output.WriteString(string(formatted))
		output.WriteString("</" + tag + ">")
	}
	output.WriteString(`</div></div>`)
	for _, field := range []string{"body", "subtext"} {
		text, formatted := formattedTextObject(value[field])
		if text == "" {
			continue
		}
		if formatted == "" {
			formatted = template.HTML(template.HTMLEscapeString(text))
		}
		textParts = append(textParts, text)
		output.WriteString(`<div class="block-card-` + field + `">`)
		output.WriteString(string(formatted))
		output.WriteString(`</div>`)
	}
	output.WriteString(`</div>`)
	return strings.Join(textParts, "\n"), template.HTML(output.String()) // #nosec G203 -- formatted text escapes app input and images are scheme-validated.
}

func appendCardImage(output *strings.Builder, image map[string]any, className string) {
	source := strings.TrimSpace(stringValue(image["image_url"]))
	parsed, ok := safeSlackLink(source)
	if !ok || (!strings.HasPrefix(parsed, "https://") && !strings.HasPrefix(parsed, "http://")) {
		return
	}
	output.WriteString(`<img class="`)
	output.WriteString(className)
	output.WriteString(`" src="`)
	output.WriteString(template.HTMLEscapeString(parsed))
	output.WriteString(`" alt="`)
	output.WriteString(template.HTMLEscapeString(strings.TrimSpace(stringValue(image["alt_text"]))))
	output.WriteString(`" loading="lazy">`)
}

func tableRows(value any) [][]string {
	rows, _ := value.([]any)
	result := make([][]string, 0, len(rows))
	for _, rawRow := range rows {
		cells, _ := rawRow.([]any)
		row := make([]string, 0, len(cells))
		for _, rawCell := range cells {
			cell, _ := rawCell.(map[string]any)
			switch strings.TrimSpace(stringValue(cell["type"])) {
			case "raw_text":
				row = append(row, strings.TrimSpace(stringValue(cell["text"])))
			case "raw_number":
				if cell["value"] == nil {
					row = append(row, "")
				} else {
					row = append(row, strings.TrimSpace(fmt.Sprint(cell["value"])))
				}
			case "rich_text":
				row = append(row, strings.Join(elementTextList(cell["elements"]), "\n"))
			default:
				row = append(row, "")
			}
		}
		if len(row) != 0 {
			result = append(result, row)
		}
	}
	return result
}

func actionElementList(value any, blockID string) []messageActionView {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]messageActionView, 0, len(values))
	for _, raw := range values {
		element, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		elementType := strings.TrimSpace(stringValue(element["type"]))
		if elementType == "feedback_buttons" {
			actionID := strings.TrimSpace(stringValue(element["action_id"]))
			if actionID == "" {
				continue
			}
			for _, feedback := range []struct {
				Field string
				Tone  string
			}{{Field: "positive_button", Tone: "positive"}, {Field: "negative_button", Tone: "negative"}} {
				button, _ := element[feedback.Field].(map[string]any)
				text := textObjectValue(button["text"])
				value := strings.TrimSpace(stringValue(button["value"]))
				if text == "" || value == "" {
					continue
				}
				result = append(result, messageActionView{
					Type: "feedback_buttons", ActionID: actionID, BlockID: blockID,
					Text: text, Value: value, Control: "button", Dispatch: true,
					AccessibilityLabel: strings.TrimSpace(stringValue(button["accessibility_label"])),
					Tone:               feedback.Tone,
				})
			}
			continue
		}
		action := messageActionView{
			Type: elementType, ActionID: strings.TrimSpace(stringValue(element["action_id"])),
			BlockID: blockID, Text: textObjectValue(element["text"]), Value: strings.TrimSpace(stringValue(element["value"])),
			Dispatch: true, AccessibilityLabel: strings.TrimSpace(stringValue(element["accessibility_label"])),
		}
		if action.ActionID == "" {
			continue
		}
		switch action.Type {
		case "button", "icon_button":
			if action.Type == "icon_button" && len(stringList(element["visible_to_user_ids"])) != 0 {
				// Visibility is an authorization rule, not decoration. Do not
				// expose a restricted action until the current viewer is part
				// of this projection.
				continue
			}
			action.Control = "button"
			if action.Text == "" {
				action.Text = "Action"
			}
		case "static_select", "overflow":
			action.Control = "select"
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Choose an option"
			}
			action.Options = actionElementOptions(element)
			if len(action.Options) == 0 {
				continue
			}
			action.InitialValues = optionValues(element["initial_option"])
		case "radio_buttons":
			action.Control = "radio"
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Choose an option"
			}
			action.Options = actionElementOptions(element)
			if len(action.Options) == 0 {
				continue
			}
			action.InitialValues = optionValues(element["initial_option"])
		case "multi_static_select":
			action.Control = "select"
			action.Multiple = true
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Choose options"
			}
			action.Options = actionElementOptions(element)
			if len(action.Options) == 0 {
				continue
			}
			action.InitialValues = optionValues(element["initial_options"])
		case "checkboxes":
			action.Control = "checkbox"
			action.Multiple = true
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Choose options"
			}
			action.Options = actionElementOptions(element)
			if len(action.Options) == 0 {
				continue
			}
			action.InitialValues = optionValues(element["initial_options"])
		case "datepicker":
			action.Control = "date"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = strings.TrimSpace(stringValue(element["initial_date"]))
		case "timepicker":
			action.Control = "time"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = strings.TrimSpace(stringValue(element["initial_time"]))
		case "datetimepicker":
			action.Control = "datetime"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = strings.TrimSpace(stringValue(element["initial_date_time"]))
		case "plain_text_input":
			action.Control = "text"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = stringValue(element["initial_value"])
			action.Multiline, _ = element["multiline"].(bool)
			if action.Multiline {
				action.Control = "textarea"
			}
		case "email_text_input":
			action.Control = "email"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = stringValue(element["initial_value"])
		case "url_text_input":
			action.Control = "url"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = stringValue(element["initial_value"])
		case "number_input":
			action.Control = "number"
			action.Text = textObjectValue(element["placeholder"])
			action.Value = stringValue(element["initial_value"])
		case "users_select", "conversations_select", "channels_select":
			action.Control = "select"
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Choose an option"
			}
			action.InitialValues = []string{strings.TrimSpace(stringValue(element["initial_user"]))}
			if action.Type == "conversations_select" {
				action.InitialValues = []string{strings.TrimSpace(stringValue(element["initial_conversation"]))}
			}
			if action.Type == "channels_select" {
				action.InitialValues = []string{strings.TrimSpace(stringValue(element["initial_channel"]))}
			}
		case "multi_users_select", "multi_conversations_select", "multi_channels_select":
			action.Control = "select"
			action.Multiple = true
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Choose options"
			}
			field := "initial_users"
			if action.Type == "multi_conversations_select" {
				field = "initial_conversations"
			}
			if action.Type == "multi_channels_select" {
				field = "initial_channels"
			}
			action.InitialValues = stringList(element[field])
		case "external_select", "multi_external_select":
			action.Control = "external"
			action.Multiple = action.Type == "multi_external_select"
			action.Text = textObjectValue(element["placeholder"])
			if action.Text == "" {
				action.Text = "Search for an option"
			}
			action.MinQueryLength = 3
			if minimum, ok := element["min_query_length"].(float64); ok && minimum >= 0 && minimum <= 100 {
				action.MinQueryLength = int(minimum)
			}
			initialField := "initial_option"
			if action.Multiple {
				initialField = "initial_options"
			}
			action.InitialValues = optionValues(element[initialField])
			action.Options = actionOptionList(optionListValue(element[initialField]))
		default:
			continue
		}
		markSelectedOptions(action.Options, action.InitialValues)
		result = append(result, action)
	}
	return result
}

func actionElementOptions(element map[string]any) []messageActionOptionView {
	result := actionOptionList(element["options"])
	groups, _ := element["option_groups"].([]any)
	for _, rawGroup := range groups {
		group, _ := rawGroup.(map[string]any)
		result = append(result, actionOptionList(group["options"])...)
	}
	return result
}

func optionListValue(value any) []any {
	switch value := value.(type) {
	case map[string]any:
		return []any{value}
	case []any:
		return value
	default:
		return nil
	}
}

func optionValues(value any) []string {
	switch value := value.(type) {
	case map[string]any:
		if selected := strings.TrimSpace(stringValue(value["value"])); selected != "" {
			return []string{selected}
		}
	case []any:
		result := make([]string, 0, len(value))
		for _, raw := range value {
			if option, ok := raw.(map[string]any); ok {
				if selected := strings.TrimSpace(stringValue(option["value"])); selected != "" {
					result = append(result, selected)
				}
			}
		}
		return result
	}
	return nil
}

func stringList(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, raw := range values {
		if selected := strings.TrimSpace(stringValue(raw)); selected != "" {
			result = append(result, selected)
		}
	}
	return result
}

func markSelectedOptions(options []messageActionOptionView, selected []string) {
	values := make(map[string]bool, len(selected))
	for _, value := range selected {
		values[value] = true
	}
	for index := range options {
		options[index].Selected = values[options[index].Value]
	}
}

func actionOptionList(value any) []messageActionOptionView {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]messageActionOptionView, 0, len(values))
	for _, raw := range values {
		option, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text := textObjectValue(option["text"])
		value := strings.TrimSpace(stringValue(option["value"]))
		if text != "" && value != "" {
			result = append(result, messageActionOptionView{Text: text, Value: value})
		}
	}
	return result
}

func decodeMessageAttachments(raw string) []messageAttachmentView {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []map[string]any
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil
	}
	attachments := make([]messageAttachmentView, 0, len(values))
	for _, value := range values {
		if attachment, ok := newMessageAttachmentView(value, ""); ok {
			attachments = append(attachments, attachment)
		}
	}
	return attachments
}

func decodeMessageUnfurls(values map[string]string) []messageAttachmentView {
	if len(values) == 0 {
		return nil
	}
	urls := make([]string, 0, len(values))
	for sourceURL := range values {
		urls = append(urls, sourceURL)
	}
	sort.Strings(urls)
	unfurls := make([]messageAttachmentView, 0, len(urls))
	for _, sourceURL := range urls {
		var value map[string]any
		if json.Unmarshal([]byte(values[sourceURL]), &value) != nil {
			continue
		}
		if attachment, ok := newMessageAttachmentView(value, sourceURL); ok {
			unfurls = append(unfurls, attachment)
		}
	}
	return unfurls
}

func newMessageAttachmentView(value map[string]any, sourceURL string) (messageAttachmentView, bool) {
	attachment := messageAttachmentView{
		SourceURL: sourceURL,
		Pretext:   strings.TrimSpace(stringValue(value["pretext"])),
		Author:    strings.TrimSpace(stringValue(value["author_name"])),
		Title:     strings.TrimSpace(stringValue(value["title"])),
		TitleURL:  strings.TrimSpace(stringValue(value["title_link"])),
		Text:      strings.TrimSpace(stringValue(value["text"])),
		Footer:    strings.TrimSpace(stringValue(value["footer"])),
		ImageURL:  strings.TrimSpace(stringValue(value["image_url"])),
		ImageAlt:  strings.TrimSpace(stringValue(value["alt_text"])),
	}
	if attachment.Title == "" && attachment.Text == "" {
		attachment.Text = strings.TrimSpace(stringValue(value["fallback"]))
	}
	if fields, ok := value["fields"].([]any); ok {
		for _, raw := range fields {
			field, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			title := strings.TrimSpace(stringValue(field["title"]))
			text := strings.TrimSpace(stringValue(field["value"]))
			if title != "" || text != "" {
				attachment.Fields = append(attachment.Fields, messageAttachmentFieldView{Title: title, Value: text})
			}
		}
	}
	if blocks, ok := value["blocks"]; ok {
		encoded, err := json.Marshal(blocks)
		if err == nil {
			attachment.Blocks = decodeMessageBlocks(string(encoded))
		}
	}
	if attachment.TitleURL == "" && attachment.Title != "" && sourceURL != "" {
		attachment.TitleURL = sourceURL
	}
	if attachment.ImageURL != "" && attachment.ImageAlt == "" {
		attachment.ImageAlt = attachment.Title
		if attachment.ImageAlt == "" {
			attachment.ImageAlt = "Message attachment"
		}
	}
	visible := attachment.Pretext != "" || attachment.Author != "" || attachment.Title != "" || attachment.Text != "" || len(attachment.Fields) > 0 || attachment.Footer != "" || attachment.ImageURL != "" || len(attachment.Blocks) > 0
	if !visible && sourceURL != "" {
		attachment.Title = sourceURL
		attachment.TitleURL = sourceURL
		visible = true
	}
	return attachment, visible
}

func textObjectList(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text := textObjectValue(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func formattedTextObjectList(value any) ([]string, []template.HTML) {
	values, ok := value.([]any)
	if !ok {
		return nil, nil
	}
	texts := make([]string, 0, len(values))
	formatted := make([]template.HTML, 0, len(values))
	for _, item := range values {
		text, html := formattedTextObject(item)
		if text == "" {
			continue
		}
		texts = append(texts, text)
		formatted = append(formatted, html)
	}
	return texts, formatted
}

func formattedTextObject(value any) (string, template.HTML) {
	text := textObjectValue(value)
	object, _ := value.(map[string]any)
	if text != "" && strings.TrimSpace(stringValue(object["type"])) == "mrkdwn" {
		return text, renderSlackMrkdwn(text)
	}
	return text, ""
}

func formattedElementList(value any, separator string) (string, template.HTML) {
	values, ok := value.([]any)
	if !ok {
		return "", ""
	}
	texts := make([]string, 0, len(values))
	fragments := make([]string, 0, len(values))
	hasFormatted := false
	for _, item := range values {
		text, formatted := formattedTextObject(item)
		if text == "" {
			if object, ok := item.(map[string]any); ok {
				text = strings.TrimSpace(stringValue(object["alt_text"]))
			}
		}
		if text == "" {
			continue
		}
		texts = append(texts, text)
		if formatted != "" {
			fragments = append(fragments, string(formatted))
			hasFormatted = true
		} else {
			fragments = append(fragments, template.HTMLEscapeString(text))
		}
	}
	if !hasFormatted {
		return strings.Join(texts, separator), ""
	}
	return strings.Join(texts, separator), template.HTML(strings.Join(fragments, template.HTMLEscapeString(separator))) // #nosec G203 -- every fragment is escaped or produced by the safe renderers in slack_markup.go.
}

func elementTextList(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		text := textObjectValue(item)
		if object, ok := item.(map[string]any); ok {
			if text == "" {
				text = strings.TrimSpace(stringValue(object["alt_text"]))
			}
			if nested := elementTextList(object["elements"]); len(nested) > 0 {
				if text != "" {
					result = append(result, text)
				}
				result = append(result, nested...)
				continue
			}
		}
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

func textObjectValue(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		if raw, ok := value["text"]; ok {
			if text := textObjectValue(raw); text != "" {
				return text
			}
		}
		if title, ok := value["title"].(string); ok {
			return strings.TrimSpace(title)
		}
	}
	return ""
}

func imageElement(value map[string]any) (string, string) {
	if strings.TrimSpace(stringValue(value["type"])) != "image" {
		return "", ""
	}
	return strings.TrimSpace(stringValue(value["image_url"])), strings.TrimSpace(stringValue(value["alt_text"]))
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
