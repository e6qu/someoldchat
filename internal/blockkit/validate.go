package blockkit

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Error mirrors one entry returned by Slack's blocks.validate method.
type Error struct {
	Pointer    string         `json:"pointer"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Constraint map[string]any `json:"constraint"`
}

var blockTypes = []string{
	"actions", "alert", "card", "carousel", "container", "context_actions",
	"context", "data_table", "data_visualization", "divider", "file", "header",
	"image", "input", "markdown", "plan", "rich_text", "section", "table",
	"task_card", "video",
}

// ValidateBlocks checks the stable cross-surface Block Kit invariants exposed
// by Slack's current reference. Surface-specific product constraints remain in
// the message/view services; this function is deliberately fail-open for
// fields owned by newer block kinds but fail-closed for malformed structures.
func ValidateBlocks(raw json.RawMessage, pointer string, limit int) ([]Error, error) {
	var blocks []any
	if err := json.Unmarshal(raw, &blocks); err != nil || blocks == nil {
		return nil, fmt.Errorf("blocks must be a JSON array")
	}
	var problems []Error
	if len(blocks) > limit {
		problems = append(problems, failed(pointer, "maximum item count exceeded", map[string]any{"type": "maxItems", "expected": limit}))
	}
	for index, rawBlock := range blocks {
		blockPointer := fmt.Sprintf("%s/%d", pointer, index)
		block, ok := rawBlock.(map[string]any)
		if !ok {
			problems = append(problems, failed(blockPointer, "block must be an object", map[string]any{"type": "object"}))
			continue
		}
		kind, _ := block["type"].(string)
		kind = strings.TrimSpace(kind)
		if !contains(blockTypes, kind) {
			problems = append(problems, failed(blockPointer+"/type", "unsupported type: "+kind, map[string]any{"type": "enum", "expected": blockTypes}))
			continue
		}
		if blockID, exists := block["block_id"]; exists {
			value, ok := blockID.(string)
			if !ok || value == "" || utf8.RuneCountInString(value) > 255 {
				problems = append(problems, failed(blockPointer+"/block_id", "block_id must be a non-empty string of at most 255 characters", map[string]any{"type": "string", "maxLength": 255}))
			}
		}
		problems = append(problems, validateKnownBlock(block, kind, blockPointer)...)
	}
	return problems, nil
}

func ValidateMessage(raw json.RawMessage) ([]Error, error) {
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return nil, fmt.Errorf("message must be a JSON object")
	}
	blocks, exists := message["blocks"]
	if !exists {
		return nil, nil
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	return ValidateBlocks(encoded, "/blocks", 50)
}

func ValidateView(raw json.RawMessage) ([]Error, error) {
	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil || view == nil {
		return nil, fmt.Errorf("view must be a JSON object")
	}
	kind, _ := view["type"].(string)
	if !contains([]string{"home", "modal", "workflow_step"}, kind) {
		return []Error{failed("/type", "unsupported type: "+kind, map[string]any{"type": "enum", "expected": []string{"home", "modal", "workflow_step"}})}, nil
	}
	if kind == "modal" {
		if problem := validateTextObject(view["title"], "/title", "plain_text", 24); problem != nil {
			return []Error{*problem}, nil
		}
	}
	blocks, exists := view["blocks"]
	if !exists {
		return nil, nil
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	return ValidateBlocks(encoded, "/blocks", 100)
}

func validateKnownBlock(block map[string]any, kind, pointer string) []Error {
	var problems []Error
	switch kind {
	case "section":
		_, hasText := block["text"]
		_, hasFields := block["fields"]
		if !hasText && !hasFields {
			problems = append(problems, failed(pointer, "section requires text or fields", map[string]any{"type": "required", "expected": []string{"text", "fields"}}))
		}
		if hasText {
			if problem := validateTextObject(block["text"], pointer+"/text", "plain_text|mrkdwn", 3000); problem != nil {
				problems = append(problems, *problem)
			}
		}
	case "header":
		if problem := validateTextObject(block["text"], pointer+"/text", "plain_text", 150); problem != nil {
			problems = append(problems, *problem)
		}
	case "actions":
		problems = append(problems, validateArray(block["elements"], pointer+"/elements", 1, 25)...)
	case "context", "context_actions":
		problems = append(problems, validateArray(block["elements"], pointer+"/elements", 1, 10)...)
	case "input":
		if problem := validateTextObject(block["label"], pointer+"/label", "plain_text", 2000); problem != nil {
			problems = append(problems, *problem)
		}
		if _, ok := block["element"].(map[string]any); !ok {
			problems = append(problems, failed(pointer+"/element", "element must be an object", map[string]any{"type": "object"}))
		}
	case "image":
		problems = append(problems, requiredString(block, "image_url", pointer, 3000)...)
		problems = append(problems, requiredString(block, "alt_text", pointer, 2000)...)
	case "markdown":
		problems = append(problems, requiredString(block, "text", pointer, 12000)...)
	case "rich_text":
		problems = append(problems, validateArray(block["elements"], pointer+"/elements", 0, 100)...)
	case "table":
		problems = append(problems, validateArray(block["rows"], pointer+"/rows", 1, 100)...)
	case "video":
		problems = append(problems, requiredString(block, "video_url", pointer, 3000)...)
		problems = append(problems, requiredString(block, "thumbnail_url", pointer, 3000)...)
		problems = append(problems, requiredString(block, "alt_text", pointer, 2000)...)
		if problem := validateTextObject(block["title"], pointer+"/title", "plain_text", 200); problem != nil {
			problems = append(problems, *problem)
		}
	case "file":
		problems = append(problems, requiredString(block, "external_id", pointer, 255)...)
		if source, _ := block["source"].(string); source != "remote" {
			problems = append(problems, failed(pointer+"/source", "source must be remote", map[string]any{"type": "enum", "expected": []string{"remote"}}))
		}
	case "alert":
		if problem := validateTextObject(block["text"], pointer+"/text", "plain_text|mrkdwn", 200); problem != nil {
			problems = append(problems, *problem)
		}
		if level, exists := block["level"]; exists && !contains([]string{"default", "info", "warning", "error", "success"}, stringValue(level)) {
			problems = append(problems, failed(pointer+"/level", "unsupported alert level", map[string]any{"type": "enum", "expected": []string{"default", "info", "warning", "error", "success"}}))
		}
	case "card":
		if block["hero_image"] == nil && block["title"] == nil && block["actions"] == nil && block["body"] == nil {
			problems = append(problems, failed(pointer, "card requires hero_image, title, actions, or body", map[string]any{"type": "required"}))
		}
		for _, field := range []string{"title", "subtitle"} {
			if block[field] != nil {
				if problem := validateTextObject(block[field], pointer+"/"+field, "plain_text|mrkdwn", 150); problem != nil {
					problems = append(problems, *problem)
				}
			}
		}
		for _, field := range []string{"body", "subtext"} {
			if block[field] != nil {
				if problem := validateTextObject(block[field], pointer+"/"+field, "plain_text|mrkdwn", 200); problem != nil {
					problems = append(problems, *problem)
				}
			}
		}
		if block["actions"] != nil {
			problems = append(problems, validateArray(block["actions"], pointer+"/actions", 1, 3)...)
		}
	case "carousel":
		problems = append(problems, validateArray(block["elements"], pointer+"/elements", 1, 10)...)
	case "container":
		if block["title"] == nil && block["rich_text_title"] == nil {
			problems = append(problems, failed(pointer, "container requires title or rich_text_title", map[string]any{"type": "required", "expected": []string{"title", "rich_text_title"}}))
		}
		if block["title"] != nil {
			if problem := validateTextObject(block["title"], pointer+"/title", "plain_text", 150); problem != nil {
				problems = append(problems, *problem)
			}
		}
		if richTitle, exists := block["rich_text_title"]; exists {
			value, ok := richTitle.(map[string]any)
			if !ok || stringValue(value["type"]) != "rich_text" {
				problems = append(problems, failed(pointer+"/rich_text_title", "rich_text_title must be a rich_text block", map[string]any{"type": "object"}))
			}
		}
		if block["subtitle"] != nil {
			if problem := validateTextObject(block["subtitle"], pointer+"/subtitle", "plain_text|mrkdwn", 150); problem != nil {
				problems = append(problems, *problem)
			}
		}
		if width, exists := block["width"]; exists && !contains([]string{"narrow", "standard", "wide", "full"}, stringValue(width)) {
			problems = append(problems, failed(pointer+"/width", "unsupported container width", map[string]any{"type": "enum", "expected": []string{"narrow", "standard", "wide", "full"}}))
		}
		children, ok := block["child_blocks"].([]any)
		if !ok || len(children) < 1 || len(children) > 10 {
			problems = append(problems, failed(pointer+"/child_blocks", "container requires 1 to 10 child blocks", map[string]any{"type": "array", "minItems": 1, "maxItems": 10}))
		} else if encoded, err := json.Marshal(children); err == nil {
			childProblems, _ := ValidateBlocks(encoded, pointer+"/child_blocks", 10)
			problems = append(problems, childProblems...)
			supported := []string{"actions", "context", "divider", "file", "header", "image", "input", "rich_text", "section", "table", "video"}
			for index, rawChild := range children {
				child, _ := rawChild.(map[string]any)
				if kind := stringValue(child["type"]); kind != "" && !contains(supported, kind) {
					problems = append(problems, failed(fmt.Sprintf("%s/child_blocks/%d/type", pointer, index), "block type is not supported in a container", map[string]any{"type": "enum", "expected": supported}))
				}
			}
		}
	case "data_visualization":
		problems = append(problems, validateDataVisualization(block, pointer)...)
	case "task_card":
		problems = append(problems, requiredString(block, "task_id", pointer, 255)...)
		problems = append(problems, requiredString(block, "title", pointer, 256)...)
		if status, exists := block["status"]; exists && !contains([]string{"pending", "in_progress", "complete", "error"}, stringValue(status)) {
			problems = append(problems, failed(pointer+"/status", "unsupported task status", map[string]any{"type": "enum", "expected": []string{"pending", "in_progress", "complete", "error"}}))
		}
	case "plan":
		problems = append(problems, requiredString(block, "title", pointer, 256)...)
		if block["tasks"] != nil {
			problems = append(problems, validateArray(block["tasks"], pointer+"/tasks", 0, 100)...)
		}
	case "data_table":
		problems = append(problems, requiredString(block, "caption", pointer, 2000)...)
		rows, ok := block["rows"].([]any)
		if !ok || len(rows) < 2 || len(rows) > 201 {
			problems = append(problems, failed(pointer+"/rows", "data table requires 2 to 201 rows", map[string]any{"type": "array", "minItems": 2, "maxItems": 201}))
			break
		}
		width := -1
		for index, rawRow := range rows {
			row, ok := rawRow.([]any)
			if !ok || len(row) < 1 || len(row) > 20 || (width >= 0 && len(row) != width) {
				problems = append(problems, failed(fmt.Sprintf("%s/rows/%d", pointer, index), "each data table row must have the same 1 to 20 columns", map[string]any{"type": "array", "minItems": 1, "maxItems": 20}))
				continue
			}
			width = len(row)
		}
	}
	return problems
}

func validateDataVisualization(block map[string]any, pointer string) []Error {
	var problems []Error
	problems = append(problems, requiredString(block, "title", pointer, 50)...)
	chart, ok := block["chart"].(map[string]any)
	if !ok {
		return append(problems, failed(pointer+"/chart", "chart must be an object", map[string]any{"type": "object"}))
	}
	kind := stringValue(chart["type"])
	if !contains([]string{"pie", "bar", "area", "line"}, kind) {
		return append(problems, failed(pointer+"/chart/type", "unsupported chart type", map[string]any{"type": "enum", "expected": []string{"pie", "bar", "area", "line"}}))
	}
	if kind == "pie" {
		segments, ok := chart["segments"].([]any)
		if !ok || len(segments) < 1 || len(segments) > 12 {
			return append(problems, failed(pointer+"/chart/segments", "pie chart requires 1 to 12 segments", map[string]any{"type": "array", "minItems": 1, "maxItems": 12}))
		}
		for index, rawSegment := range segments {
			segment, ok := rawSegment.(map[string]any)
			segmentPointer := fmt.Sprintf("%s/chart/segments/%d", pointer, index)
			if !ok {
				problems = append(problems, failed(segmentPointer, "segment must be an object", map[string]any{"type": "object"}))
				continue
			}
			problems = append(problems, requiredString(segment, "label", segmentPointer, 20)...)
			if value, ok := finiteJSONNumber(segment["value"]); !ok || value <= 0 {
				problems = append(problems, failed(segmentPointer+"/value", "segment value must be a finite number greater than zero", map[string]any{"type": "number", "exclusiveMinimum": 0}))
			}
		}
		return problems
	}

	axis, ok := chart["axis_config"].(map[string]any)
	if !ok {
		problems = append(problems, failed(pointer+"/chart/axis_config", "axis_config must be an object", map[string]any{"type": "object"}))
		return problems
	}
	categories, categoryProblems := chartStringArray(axis["categories"], pointer+"/chart/axis_config/categories", 1, 20, 20)
	problems = append(problems, categoryProblems...)
	for _, field := range []string{"x_label", "y_label"} {
		if value, exists := axis[field]; exists {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 50 {
				problems = append(problems, failed(pointer+"/chart/axis_config/"+field, field+" must be a non-empty string of at most 50 characters", map[string]any{"type": "string", "maxLength": 50}))
			}
		}
	}
	seriesValues, ok := chart["series"].([]any)
	if !ok || len(seriesValues) < 1 || len(seriesValues) > 12 {
		return append(problems, failed(pointer+"/chart/series", "chart requires 1 to 12 series", map[string]any{"type": "array", "minItems": 1, "maxItems": 12}))
	}
	seriesNames := map[string]bool{}
	for seriesIndex, rawSeries := range seriesValues {
		series, ok := rawSeries.(map[string]any)
		seriesPointer := fmt.Sprintf("%s/chart/series/%d", pointer, seriesIndex)
		if !ok {
			problems = append(problems, failed(seriesPointer, "series must be an object", map[string]any{"type": "object"}))
			continue
		}
		problems = append(problems, requiredString(series, "name", seriesPointer, 20)...)
		name := strings.TrimSpace(stringValue(series["name"]))
		if name != "" && seriesNames[name] {
			problems = append(problems, failed(seriesPointer+"/name", "series names must be unique", map[string]any{"type": "unique"}))
		}
		seriesNames[name] = true
		points, ok := series["data"].([]any)
		if !ok || len(points) < 1 || len(points) > 20 || len(categories) != 0 && len(points) != len(categories) {
			problems = append(problems, failed(seriesPointer+"/data", "series data must contain exactly one point for each category", map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "expectedItems": len(categories)}))
			continue
		}
		seenLabels := map[string]bool{}
		for pointIndex, rawPoint := range points {
			point, ok := rawPoint.(map[string]any)
			pointPointer := fmt.Sprintf("%s/data/%d", seriesPointer, pointIndex)
			if !ok {
				problems = append(problems, failed(pointPointer, "data point must be an object", map[string]any{"type": "object"}))
				continue
			}
			problems = append(problems, requiredString(point, "label", pointPointer, 20)...)
			label := strings.TrimSpace(stringValue(point["label"]))
			if !contains(categories, label) || seenLabels[label] {
				problems = append(problems, failed(pointPointer+"/label", "data point label must uniquely match an axis category", map[string]any{"type": "enum", "expected": categories}))
			}
			seenLabels[label] = true
			if _, ok := finiteJSONNumber(point["value"]); !ok {
				problems = append(problems, failed(pointPointer+"/value", "data point value must be a finite number", map[string]any{"type": "number"}))
			}
		}
	}
	return problems
}

func chartStringArray(raw any, pointer string, minimum, maximum, maxLength int) ([]string, []Error) {
	values, ok := raw.([]any)
	if !ok || len(values) < minimum || len(values) > maximum {
		return nil, []Error{failed(pointer, "array item count is outside its allowed range", map[string]any{"type": "array", "minItems": minimum, "maxItems": maximum})}
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	var problems []Error
	for index, rawValue := range values {
		value, ok := rawValue.(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" || utf8.RuneCountInString(value) > maxLength {
			problems = append(problems, failed(fmt.Sprintf("%s/%d", pointer, index), "category must be a non-empty string within its length limit", map[string]any{"type": "string", "maxLength": maxLength}))
			continue
		}
		if seen[value] {
			problems = append(problems, failed(fmt.Sprintf("%s/%d", pointer, index), "categories must be unique", map[string]any{"type": "unique"}))
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, problems
}

func finiteJSONNumber(raw any) (float64, bool) {
	value, ok := raw.(float64)
	return value, ok && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateTextObject(raw any, pointer, allowed string, maximum int) *Error {
	value, ok := raw.(map[string]any)
	if !ok {
		problem := failed(pointer, "text must be an object", map[string]any{"type": "object"})
		return &problem
	}
	kind, _ := value["type"].(string)
	if !contains(strings.Split(allowed, "|"), kind) {
		problem := failed(pointer+"/type", "unsupported type: "+kind, map[string]any{"type": "enum", "expected": strings.Split(allowed, "|")})
		return &problem
	}
	text, ok := value["text"].(string)
	if !ok || text == "" || utf8.RuneCountInString(text) > maximum {
		problem := failed(pointer+"/text", "text must be a non-empty string within its length limit", map[string]any{"type": "string", "maxLength": maximum})
		return &problem
	}
	return nil
}

func validateArray(raw any, pointer string, minimum, maximum int) []Error {
	values, ok := raw.([]any)
	if !ok || len(values) < minimum || len(values) > maximum {
		return []Error{failed(pointer, "array item count is outside its allowed range", map[string]any{"type": "array", "minItems": minimum, "maxItems": maximum})}
	}
	return nil
}

func requiredString(value map[string]any, field, pointer string, maximum int) []Error {
	text, ok := value[field].(string)
	if !ok || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > maximum {
		return []Error{failed(pointer+"/"+field, field+" must be a non-empty string within its length limit", map[string]any{"type": "string", "maxLength": maximum})}
	}
	return nil
}

func failed(pointer, message string, constraint map[string]any) Error {
	return Error{Pointer: pointer, Code: "failed_constraint", Message: message, Constraint: constraint}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
