package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Cursor string

type PageRequest struct {
	Limit  int
	Cursor Cursor
	// Descending selects the newest-first direction of a read that supports one:
	// `ORDER BY created_at DESC, id DESC`, with the cursor walking backwards.
	//
	// It exists because the newest window of a conversation was otherwise only
	// reachable by walking the whole conversation forward from its first message.
	// internal/web bounded that walk, so a conversation longer than the bound had
	// no reachable newest window at all — paging forward, "jump to the latest" and
	// a search permalink all landed on the same stale window in the middle of the
	// history — and internal/api/slack filtered a full scan for the same reason.
	// One descending page answers both.
	//
	// The cursor encoding is the same in both directions and carries no direction
	// of its own: it names the last row of the page that produced it, and the
	// request says which side of that row the next page lies on. A cursor minted
	// by a descending page is therefore usable by an ascending request and the
	// reverse, which is what makes a permalink reachable from either end.
	//
	// A read that does not implement the direction refuses a descending request
	// with store.ErrInvalidArgument rather than silently answering ascending; see
	// store.CheckAscendingPage.
	Descending bool
}

// PageAfter reports whether a row keyed (createdAt, id) belongs on the page a
// request with this cursor key asks for: strictly after the cursor when the
// request is ascending, strictly before it when it is descending.
//
// Both repositories and both directions decide the page boundary here, so a row
// cannot be skipped by one profile and repeated by another. The key is the same
// (created_at, id) tuple every message ORDER BY and every message cursor uses,
// which is what makes a forward walk and a backward walk of one conversation
// visit exactly the same rows.
func (r PageRequest) PageAfter(createdAt time.Time, id MessageID, cursorAt time.Time, cursorID MessageID) bool {
	if r.Descending {
		return createdAt.Before(cursorAt) || (createdAt.Equal(cursorAt) && string(id) < string(cursorID))
	}
	return createdAt.After(cursorAt) || (createdAt.Equal(cursorAt) && string(id) > string(cursorID))
}

type ConversationType string

const (
	ConversationTypePublic  ConversationType = "public_channel"
	ConversationTypePrivate ConversationType = "private_channel"
	ConversationTypeIM      ConversationType = "im"
	ConversationTypeMPIM    ConversationType = "mpim"
)

type ConversationListRequest struct {
	Limit                int
	Cursor               Cursor
	ExcludeArchived      bool
	Types                []ConversationType
	MemberUserID         UserID
	IncludeClosedDirects bool
}

func NormalizeConversationTypes(values []string) ([]ConversationType, error) {
	seen := make(map[ConversationType]struct{}, len(values))
	for _, value := range values {
		typeValue := ConversationType(strings.TrimSpace(strings.ToLower(value)))
		if typeValue == "" {
			continue
		}
		switch typeValue {
		case ConversationTypePublic, ConversationTypePrivate, ConversationTypeIM, ConversationTypeMPIM:
		default:
			return nil, errors.New("invalid conversation type")
		}
		seen[typeValue] = struct{}{}
	}
	types := make([]ConversationType, 0, len(seen))
	for typeValue := range seen {
		types = append(types, typeValue)
	}
	sort.Slice(types, func(left, right int) bool { return types[left] < types[right] })
	return types, nil
}

func ValidateConversationTypes(values []ConversationType) error {
	for _, typeValue := range values {
		switch typeValue {
		case ConversationTypePublic, ConversationTypePrivate, ConversationTypeIM, ConversationTypeMPIM:
		default:
			return errors.New("invalid conversation type")
		}
	}
	return nil
}

type MessagePage struct {
	Messages   []Message
	NextCursor Cursor
	HasMore    bool
	// Total is populated by searches, whose public contract includes the
	// complete match count. History and reply pages leave it zero.
	Total int
}

type UserPage struct {
	Users      []User
	NextCursor Cursor
	HasMore    bool
}

type ConversationPage struct {
	Conversations []Conversation
	NextCursor    Cursor
	HasMore       bool
}

type WorkspacePage struct {
	Workspaces []Workspace
	NextCursor Cursor
	HasMore    bool
}

type CanvasPage struct {
	Canvases   []Canvas
	NextCursor Cursor
	HasMore    bool
}

type ListPage struct {
	Lists      []List
	NextCursor Cursor
	HasMore    bool
}

type UserReactionPage struct {
	Items      []UserReaction
	NextCursor Cursor
	HasMore    bool
}

type messageCursor struct {
	CreatedAt time.Time
	ID        MessageID
	Root      bool `json:"root,omitempty"`
}

var ErrInvalidCursor = errors.New("invalid cursor")

type listCursor struct{ ID string }

func NewListCursor(id string) (Cursor, error) {
	if id == "" || !utf8.ValidString(id) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(listCursor{ID: id})
	if err != nil {
		return "", err
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(body)), nil
}

func DecodeListCursor(cursor Cursor) (string, error) {
	if cursor == "" {
		return "", nil
	}
	body, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		return "", ErrInvalidCursor
	}
	var value listCursor
	if err := json.Unmarshal(body, &value); err != nil || value.ID == "" {
		return "", ErrInvalidCursor
	}
	return value.ID, nil
}

// NewMessageCursor refuses a message without a creation instant. Decoding
// rejects a zero CreatedAt, so minting one produced a cursor that always failed
// on the next page request: pagination dead-ended with InvalidArgument instead of
// the caller learning at once that the message was incomplete.
func NewMessageCursor(message Message) (Cursor, error) {
	if message.ID == "" || !utf8.ValidString(string(message.ID)) || message.CreatedAt.IsZero() {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(messageCursor{CreatedAt: message.CreatedAt.UTC(), ID: message.ID, Root: message.ThreadTimestamp == ""})
	if err != nil {
		return "", err
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(body)), nil
}

func DecodeMessageCursor(cursor Cursor) (time.Time, MessageID, error) {
	createdAt, id, _, err := DecodeMessageCursorWithRoot(cursor)
	return createdAt, id, err
}

func DecodeMessageCursorWithRoot(cursor Cursor) (time.Time, MessageID, bool, error) {
	if cursor == "" {
		return time.Time{}, "", false, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		return time.Time{}, "", false, ErrInvalidCursor
	}
	var value messageCursor
	if err := json.Unmarshal(body, &value); err != nil || value.ID == "" || value.CreatedAt.IsZero() {
		return time.Time{}, "", false, ErrInvalidCursor
	}
	return value.CreatedAt.UTC(), value.ID, value.Root, nil
}
