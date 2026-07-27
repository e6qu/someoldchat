package web

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// richMessageContent is the browser projection of the structured content the
// Slack API persists on a message. The API deliberately stores normalized JSON
// rather than a browser-specific model; this projection keeps that transport
// shape out of the template while making block-only and attachment-only
// messages visible to people who use the first-party client.
type richMessageContent struct {
	Text        string
	Blocks      []messageBlockView
	Attachments []messageAttachmentView
	Unfurls     []messageAttachmentView
}

type messageBlockView struct {
	Kind       string
	Text       string
	Fields     []string
	ImageURL   string
	ImageAlt   string
	LinkURL    string
	LinkLabel  string
	ActionText []string
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
	content := richMessageContent{
		Blocks:      decodeMessageBlocks(message.Blocks),
		Attachments: decodeMessageAttachments(message.Attachments),
		Unfurls:     decodeMessageUnfurls(message.Unfurls),
	}
	// Slack treats top-level text as notification and accessibility fallback
	// when blocks are present. Rendering it beside the blocks repeats the same
	// message for both visual and assistive-technology users. If a malformed or
	// wholly unknown block payload somehow reaches a legacy store, retain the
	// fallback rather than rendering a blank row.
	if len(content.Blocks) == 0 {
		content.Text = message.Text
	}
	return content
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
		block.Text = textObjectValue(value["text"])
		block.Fields = textObjectList(value["fields"])
		if accessory, ok := value["accessory"].(map[string]any); ok {
			block.ImageURL, block.ImageAlt = imageElement(accessory)
		}
	case "context":
		block.Text = strings.Join(elementTextList(value["elements"]), " · ")
	case "image":
		block.ImageURL = strings.TrimSpace(stringValue(value["image_url"]))
		block.ImageAlt = strings.TrimSpace(stringValue(value["alt_text"]))
		block.Text = textObjectValue(value["title"])
	case "actions":
		block.ActionText = elementTextList(value["elements"])
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
	return block, block.Text != "" || len(block.Fields) > 0 || block.ImageURL != "" || len(block.ActionText) > 0 || blockType == "divider"
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
