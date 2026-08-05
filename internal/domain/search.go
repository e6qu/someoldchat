package domain

import (
	"errors"
	"strings"
	"time"
)

type SearchSort string

const (
	SearchSortScore     SearchSort = "score"
	SearchSortTimestamp SearchSort = "timestamp"
)

type SearchDirection string

const (
	SearchDirectionAscending  SearchDirection = "asc"
	SearchDirectionDescending SearchDirection = "desc"
)

// SearchHistoryEntry is private first-party client state. Slack's public Web
// API exposes search execution, not the member's recent-search list, so this
// value belongs on SameOldChat's authenticated application/gRPC seam.
type SearchHistoryEntry struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	Query       string
	SearchedAt  time.Time
}

func NormalizeSearchOrder(sortValue, direction string) (SearchSort, SearchDirection, error) {
	sortOrder := SearchSort(strings.TrimSpace(strings.ToLower(sortValue)))
	if sortOrder == "" {
		sortOrder = SearchSortScore
	}
	if sortOrder != SearchSortScore && sortOrder != SearchSortTimestamp {
		return "", "", errors.New("invalid search sort")
	}
	sortDirection := SearchDirection(strings.TrimSpace(strings.ToLower(direction)))
	if sortDirection == "" {
		sortDirection = SearchDirectionDescending
	}
	if sortDirection != SearchDirectionAscending && sortDirection != SearchDirectionDescending {
		return "", "", errors.New("invalid search direction")
	}
	return sortOrder, sortDirection, nil
}

// SearchQueryTokens splits Slack's search language without destroying quoted
// phrases. Modifier interpretation remains in the service because resolving a
// name to a workspace object is an authorization-aware operation.
func SearchQueryTokens(raw string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	quoted := false
	for _, character := range raw {
		switch {
		case character == '"':
			quoted = !quoted
		case !quoted && (character == ' ' || character == '\t' || character == '\n' || character == '\r'):
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(character)
		}
	}
	if quoted {
		return nil, errors.New("unterminated search phrase")
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

// MessageSearch is the normalized, authorization-independent search plan used
// by every repository. Human-readable Slack modifiers are resolved to durable
// IDs by the service before this value reaches storage.
type MessageSearch struct {
	Terms                []string
	ExcludedTerms        []string
	Conversation         ConversationID
	ExcludedConversation ConversationID
	Author               UserID
	ExcludedAuthor       UserID
	WithUser             UserID
	After                time.Time
	Before               time.Time
	ThreadOnly           bool
	HasFiles             bool
	HasPins              bool
	HasReactions         bool
	SavedBy              UserID
	Sort                 SearchSort
	Direction            SearchDirection
	Page                 PageRequest
}

type MessageSearchRequest struct {
	Query        string
	Conversation ConversationID
	Sort         SearchSort
	Direction    SearchDirection
	Page         PageRequest
}

type FileSearch struct {
	Terms                []string
	ExcludedTerms        []string
	Uploader             UserID
	ExcludedUploader     UserID
	Conversation         ConversationID
	ExcludedConversation ConversationID
	FileType             string
	After                time.Time
	Before               time.Time
	Sort                 SearchSort
	Direction            SearchDirection
	Count                int
	Page                 int
}

type FileSearchRequest struct {
	Query        string
	Conversation ConversationID
	Sort         SearchSort
	Direction    SearchDirection
	Count        int
	Page         int
}

// CanvasSearch is the normalized plan every repository executes for a canvas
// search. Like the message and file plans it carries resolved identifiers, not
// human text: turning "from:@ada" into a user id is an authorization-aware step
// and belongs to the service.
//
// It is deliberately smaller than the file plan. Slack's canvas results are not
// scoped by conversation, because a canvas is not in one — a channel canvas is
// reached through its channel and a standalone canvas through a grant — so a
// conversation modifier here would answer a question the object model does not
// ask.
type CanvasSearch struct {
	Terms         []string
	ExcludedTerms []string
	Owner         UserID
	ExcludedOwner UserID
	After         time.Time
	Before        time.Time
	Sort          SearchSort
	Direction     SearchDirection
	Page          PageRequest
}

type CanvasSearchRequest struct {
	Query     string
	Sort      SearchSort
	Direction SearchDirection
	Page      PageRequest
}
