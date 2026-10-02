package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// DialogDefinition is a legacy dialog as dialog.open accepts it. The
// first-party client renders it with the modal machinery; the service
// validates it once when it is opened and again reads it at submission.
type DialogDefinition struct {
	CallbackID     string          `json:"callback_id"`
	Title          string          `json:"title"`
	SubmitLabel    string          `json:"submit_label"`
	NotifyOnCancel bool            `json:"notify_on_cancel"`
	State          string          `json:"state"`
	Elements       []DialogElement `json:"elements"`
}

// DialogElement is one text, textarea, or select element of a dialog.
type DialogElement struct {
	Type            string         `json:"type"`
	Label           string         `json:"label"`
	Name            string         `json:"name"`
	Placeholder     string         `json:"placeholder"`
	Hint            string         `json:"hint"`
	Subtype         string         `json:"subtype"`
	Value           string         `json:"value"`
	Optional        bool           `json:"optional"`
	MinLength       *int           `json:"min_length"`
	MaxLength       *int           `json:"max_length"`
	DataSource      string         `json:"data_source"`
	MinQueryLength  *int           `json:"min_query_length"`
	Options         []DialogOption `json:"options"`
	OptionGroups    []DialogGroup  `json:"option_groups"`
	SelectedOptions []DialogOption `json:"selected_options"`
}

type DialogOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type DialogGroup struct {
	Label   string         `json:"label"`
	Options []DialogOption `json:"options"`
}

// Limits returns an element's effective minimum and maximum length: text
// defaults to 0..150, textarea to 0..3000.
func (element DialogElement) Limits() (int, int) {
	minimum, maximum := 0, 150
	if element.Type == "textarea" {
		maximum = 3000
	}
	if element.MinLength != nil {
		minimum = *element.MinLength
	}
	if element.MaxLength != nil {
		maximum = *element.MaxLength
	}
	return minimum, maximum
}

// ParseDialog decodes and validates a dialog against Slack's documented
// dialog.open limits.
func ParseDialog(payload string) (DialogDefinition, error) {
	var dialog DialogDefinition
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(payload)))
	if strings.TrimSpace(payload) == "" || decoder.Decode(&dialog) != nil {
		return DialogDefinition{}, ErrInvalidDialog
	}
	runes := utf8.RuneCountInString
	if title := strings.TrimSpace(dialog.Title); title == "" || runes(title) > 24 ||
		strings.TrimSpace(dialog.CallbackID) == "" || runes(dialog.CallbackID) > 255 ||
		runes(dialog.SubmitLabel) > 48 || strings.ContainsAny(strings.TrimSpace(dialog.SubmitLabel), " \t\n") ||
		runes(dialog.State) > 3000 || len(dialog.Elements) == 0 || len(dialog.Elements) > 10 {
		return DialogDefinition{}, ErrInvalidDialog
	}
	names := make(map[string]bool, len(dialog.Elements))
	for _, element := range dialog.Elements {
		name := strings.TrimSpace(element.Name)
		if name == "" || runes(name) > 300 || names[name] || strings.TrimSpace(element.Label) == "" || runes(element.Label) > 48 ||
			runes(element.Placeholder) > 150 || runes(element.Hint) > 150 {
			return DialogDefinition{}, ErrInvalidDialog
		}
		names[name] = true
		switch element.Type {
		case "text", "textarea":
			limit := 150
			if element.Type == "textarea" {
				limit = 3000
			}
			minimum, maximum := element.Limits()
			if minimum < 0 || maximum < 1 || maximum > limit || minimum > maximum || runes(element.Value) > limit ||
				(element.Subtype != "" && element.Subtype != "email" && element.Subtype != "number" && element.Subtype != "tel" && element.Subtype != "url") {
				return DialogDefinition{}, ErrInvalidDialog
			}
		case "select":
			switch element.DataSource {
			case "", "static":
				count := len(element.Options)
				for _, group := range element.OptionGroups {
					count += len(group.Options)
				}
				if count == 0 || len(element.Options) > 100 || len(element.OptionGroups) > 100 || (len(element.Options) != 0 && len(element.OptionGroups) != 0) {
					return DialogDefinition{}, ErrInvalidDialog
				}
			case "users", "channels", "conversations", "external":
			default:
				return DialogDefinition{}, ErrInvalidDialog
			}
		default:
			return DialogDefinition{}, ErrInvalidDialog
		}
	}
	return dialog, nil
}

func (definition DialogDefinition) Element(name string) (DialogElement, bool) {
	for _, element := range definition.Elements {
		if element.Name == name {
			return element, true
		}
	}
	return DialogElement{}, false
}
