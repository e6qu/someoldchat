package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ConversationSearchSort is admin.conversations.search's sort.
type ConversationSearchSort string

const (
	// ConversationSortRelevant ranks an exact name match first, then a name
	// that starts with the query, then one that contains it, then a channel
	// matched only by its topic or purpose.
	ConversationSortRelevant    ConversationSearchSort = "relevant"
	ConversationSortName        ConversationSearchSort = "name"
	ConversationSortMemberCount ConversationSearchSort = "member_count"
	ConversationSortCreated     ConversationSearchSort = "created"
)

// Valid reports whether the sort is one Slack names.
func (sort ConversationSearchSort) Valid() bool {
	switch sort {
	case ConversationSortRelevant, ConversationSortName, ConversationSortMemberCount, ConversationSortCreated:
		return true
	}
	return false
}

// ConversationSearch is an administrator's channel search. Direct and group
// direct conversations are never in it: the method searches channels.
type ConversationSearch struct {
	// Query is matched against the name, topic and purpose, folded; empty
	// matches every channel.
	Query string
	// Private and Archived, when set, keep only the channels whose flag has
	// that value (search_channel_types).
	Private  *bool
	Archived *bool
	Sort     ConversationSearchSort
}

// Matches reports whether a conversation belongs in the search.
func (search ConversationSearch) Matches(conversation Conversation) bool {
	if conversation.IsDirectOrGroup() {
		return false
	}
	if search.Private != nil && conversation.PrivateFlag() != *search.Private {
		return false
	}
	if search.Archived != nil && conversation.Archived != *search.Archived {
		return false
	}
	if search.Query == "" {
		return true
	}
	return strings.Contains(FoldSearchText(conversation.Name), search.Query) ||
		strings.Contains(FoldSearchText(conversation.Topic), search.Query) ||
		strings.Contains(FoldSearchText(conversation.Purpose), search.Query)
}

// RelevanceRank is the conversation's place under ConversationSortRelevant:
// 0 for an exact name match through 3 for a topic or purpose match.
func (search ConversationSearch) RelevanceRank(conversation Conversation) int64 {
	name := FoldSearchText(conversation.Name)
	switch {
	case name == search.Query:
		return 0
	case strings.HasPrefix(name, search.Query):
		return 1
	case strings.Contains(name, search.Query):
		return 2
	default:
		return 3
	}
}

// ConversationSearchPosition is where a channel falls in a search's order: the
// sort key, then the id that breaks ties. The name sort orders by Text; every
// other sort orders by Number.
type ConversationSearchPosition struct {
	Number int64
	Text   string
	ID     ConversationID
}

// PositionIn is the conversation's position under the search's sort.
// memberCount is its member count, which the store computes.
func (search ConversationSearch) PositionIn(conversation Conversation, memberCount int) ConversationSearchPosition {
	position := ConversationSearchPosition{ID: conversation.ID}
	switch search.Sort {
	case ConversationSortName:
		position.Text = conversation.Name
	case ConversationSortCreated:
		if !conversation.Created.IsZero() {
			position.Number = conversation.Created.Unix()
		}
	case ConversationSortMemberCount:
		position.Number = int64(memberCount)
	default:
		position.Number = search.RelevanceRank(conversation)
	}
	return position
}

// Before reports whether position sorts ahead of other in ascending order.
func (position ConversationSearchPosition) Before(other ConversationSearchPosition) bool {
	if position.Number != other.Number {
		return position.Number < other.Number
	}
	if position.Text != other.Text {
		return position.Text < other.Text
	}
	return position.ID < other.ID
}

type conversationSearchCursor struct {
	Sort   ConversationSearchSort `json:"s"`
	Number int64                  `json:"n,omitempty"`
	Text   string                 `json:"t,omitempty"`
	ID     ConversationID         `json:"i"`
}

// NewConversationSearchCursor names the last channel of a page under sort.
// The sort is part of the cursor, so a cursor from one sort is refused by
// another rather than silently skipping or repeating channels.
func NewConversationSearchCursor(sort ConversationSearchSort, position ConversationSearchPosition) (Cursor, error) {
	if position.ID == "" || !utf8.ValidString(position.Text) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(conversationSearchCursor{Sort: sort, Number: position.Number, Text: position.Text, ID: position.ID})
	if err != nil {
		return "", err
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(body)), nil
}

// DecodeConversationSearchCursor reads a NewConversationSearchCursor cursor
// for sort. ok is false for the empty cursor, which starts at the beginning.
func DecodeConversationSearchCursor(cursor Cursor, sort ConversationSearchSort) (ConversationSearchPosition, bool, error) {
	if cursor == "" {
		return ConversationSearchPosition{}, false, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		return ConversationSearchPosition{}, false, ErrInvalidCursor
	}
	var value conversationSearchCursor
	if err := json.Unmarshal(body, &value); err != nil || value.ID == "" || value.Sort != sort {
		return ConversationSearchPosition{}, false, ErrInvalidCursor
	}
	return ConversationSearchPosition{Number: value.Number, Text: value.Text, ID: value.ID}, true, nil
}

// The search_channel_types values this deployment applies. Slack lists more
// (multi_workspace, org_wide, the external_shared family, exclude_org_shared
// and private_exclude_archived); those describe Enterprise Grid and Slack
// Connect states this search does not model, and are refused rather than
// silently ignored.
const (
	ChannelTypePrivate         = "private"
	ChannelTypePrivateExclude  = "private_exclude"
	ChannelTypeArchived        = "archived"
	ChannelTypeExcludeArchived = "exclude_archived"
)

// ApplyChannelTypes narrows the search to search_channel_types. It reports
// false for a type it does not apply and for two types that contradict each
// other, such as private with private_exclude.
func (search *ConversationSearch) ApplyChannelTypes(types []string) bool {
	set := func(flag **bool, value bool) bool {
		if *flag != nil && **flag != value {
			return false
		}
		*flag = &value
		return true
	}
	for _, kind := range types {
		var ok bool
		switch strings.TrimSpace(kind) {
		case ChannelTypePrivate:
			ok = set(&search.Private, true)
		case ChannelTypePrivateExclude:
			ok = set(&search.Private, false)
		case ChannelTypeArchived:
			ok = set(&search.Archived, true)
		case ChannelTypeExcludeArchived:
			ok = set(&search.Archived, false)
		}
		if !ok {
			return false
		}
	}
	return true
}

// ChannelTypes is the search's search_channel_types, the inverse of
// ApplyChannelTypes.
func (search ConversationSearch) ChannelTypes() []string {
	types := make([]string, 0, 2)
	if search.Private != nil {
		types = append(types, map[bool]string{true: ChannelTypePrivate, false: ChannelTypePrivateExclude}[*search.Private])
	}
	if search.Archived != nil {
		types = append(types, map[bool]string{true: ChannelTypeArchived, false: ChannelTypeExcludeArchived}[*search.Archived])
	}
	return types
}
