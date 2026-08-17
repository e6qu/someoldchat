package domain

import (
	"net/url"
	"strings"
	"time"
)

// ProfileFieldID identifies one custom profile field a workspace has defined.
type ProfileFieldID string

// ProfileFieldType is the kind of value a custom profile field holds. Slack's
// team.profile.get reports these; the set here is the portable core — text, a
// calendar date, a link, and a closed option list — each with a value rule the
// service enforces so a member cannot store a value the field cannot mean.
type ProfileFieldType string

const (
	ProfileFieldText        ProfileFieldType = "text"
	ProfileFieldDate        ProfileFieldType = "date"
	ProfileFieldLink        ProfileFieldType = "link"
	ProfileFieldOptionsList ProfileFieldType = "options_list"
)

func (t ProfileFieldType) Valid() bool {
	switch t {
	case ProfileFieldText, ProfileFieldDate, ProfileFieldLink, ProfileFieldOptionsList:
		return true
	default:
		return false
	}
}

// ProfileFieldDefinition is one custom profile field a workspace administrator
// has declared. Every member's profile can carry a value for it, and Slack's
// team.profile.get lists these definitions so a client can label the values it
// reads from users.profile.get.
type ProfileFieldDefinition struct {
	WorkspaceID WorkspaceID
	ID          ProfileFieldID
	Ordering    int
	Label       string
	Hint        string
	Type        ProfileFieldType
	// PossibleValues are the allowed values of an options_list field and are
	// empty for every other type.
	PossibleValues []string
	// IsHidden keeps a field's value visible only to the member it belongs to
	// and to workspace administrators, the way Slack hides sensitive fields from
	// the rest of the workspace.
	IsHidden  bool
	CreatedAt time.Time
}

// Valid reports whether a definition is well formed. An options_list must offer
// options and no other type may, so a client is never handed a menu with
// nothing in it or a free-text field pretending to be a menu.
func (d ProfileFieldDefinition) Valid() bool {
	if label := strings.TrimSpace(d.Label); label == "" || len(label) > 64 {
		return false
	}
	if len(d.Hint) > 255 {
		return false
	}
	if !d.Type.Valid() {
		return false
	}
	if d.Type == ProfileFieldOptionsList {
		if len(d.PossibleValues) == 0 {
			return false
		}
		for _, option := range d.PossibleValues {
			if strings.TrimSpace(option) == "" {
				return false
			}
		}
	} else if len(d.PossibleValues) > 0 {
		return false
	}
	return true
}

// Accepts reports whether a value can mean what this field's type promises.
// Clearing a value is always allowed; otherwise a date must be a calendar date,
// a link an http(s) URL, and an options_list value one of the declared options.
func (d ProfileFieldDefinition) Accepts(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return true
	}
	switch d.Type {
	case ProfileFieldText:
		return len(text) <= 255
	case ProfileFieldDate:
		_, err := time.Parse("2006-01-02", text)
		return err == nil
	case ProfileFieldLink:
		parsed, err := url.Parse(text)
		return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
	case ProfileFieldOptionsList:
		for _, option := range d.PossibleValues {
			if option == text {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// UserProfileFieldValue is one member's value for one custom profile field. Alt
// is Slack's secondary display string — a label for a link, or the human name
// behind an option — and is empty when a field has none.
type UserProfileFieldValue struct {
	FieldID ProfileFieldID
	Value   string
	Alt     string
}
