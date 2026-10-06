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

// MessageWindow is the `oldest`/`latest`/`inclusive` window Slack declares on
// conversations.history and conversations.replies. A zero bound is unbounded.
//
// It is part of the store read rather than a filter applied to a fetched page.
// Filtering after the fact answered `latest=<ts>&inclusive=true&limit=1` with an
// empty page, because the one row fetched was the newest message rather than
// the one the window named, and `has_more` then described the unfiltered scan.
type MessageWindow struct {
	Oldest    time.Time
	Latest    time.Time
	Inclusive bool
}

// Contains reports whether an instant falls inside the window. Every profile
// decides membership here or with the equivalent SQL predicate.
func (w MessageWindow) Contains(at time.Time) bool {
	if !w.Oldest.IsZero() && (at.Before(w.Oldest) || (at.Equal(w.Oldest) && !w.Inclusive)) {
		return false
	}
	if !w.Latest.IsZero() && (at.After(w.Latest) || (at.Equal(w.Latest) && !w.Inclusive)) {
		return false
	}
	return true
}

// HistoryRequest is one page of a conversation's history.
//
// RootsOnly is Slack's conversations.history contract: a thread reply is not
// channel history unless it was also broadcast to the channel. The first-party
// timeline and the Web API both read with it; a caller that needs every row —
// retention, direct-history copies — leaves it false.
type HistoryRequest struct {
	Page      PageRequest
	Window    MessageWindow
	RootsOnly bool
}

// ThreadRequest is one page of a thread. The window narrows the replies; the
// root is always the first row of the first page, which is what
// conversations.replies answers whatever window the caller supplies.
type ThreadRequest struct {
	Page   PageRequest
	Window MessageWindow
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
	// Query narrows the listing to conversations whose name, topic or purpose
	// contains it, folded. It lives on the listing rather than in a separate
	// search method on purpose: the visibility rule is the hard part and it is
	// already here, and a second method would be a second copy of it — which is
	// exactly how a search comes to reveal a private channel the directory
	// withholds.
	Query string
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

// UserReactionPage is one page of reactions.list. It pages by reacted
// message, not by reaction row: Items holds every reaction row of up to Limit
// messages, so a message a member reacted to more than once is never split
// across pages (and never listed on two of them). Paging by row did both.
type UserReactionPage struct {
	Items      []UserReaction
	NextCursor Cursor
	HasMore    bool
	// Total is how many reacted messages the whole listing holds, which
	// reactions.list's legacy paging object reports.
	Total int
}

// UserReactionCursorKey is the keyset position after one reacted message: its
// fixed-width creation instant and its identifier.
func UserReactionCursorKey(message Message) string {
	return string(NewStoredTime(message.CreatedAt)) + "\x00" + string(message.ID)
}

// ParseUserReactionCursorKey reads a position UserReactionCursorKey minted.
// A cursor minted when pages were cut by reaction row carries the reaction's
// name and user after the message; it resumes after that whole message.
func ParseUserReactionCursorKey(key string) (string, MessageID, bool) {
	parts := strings.Split(key, "\x00")
	if (len(parts) != 2 && len(parts) != 4) || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], MessageID(parts[1]), true
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

// PairCursor carries a keyset position that two columns make up. Joining the
// two parts into one string needs a separator that neither part contains, and
// no separator survives a SQL string literal on every profile: SQLite reads
// '\x00' as four ordinary characters and PostgreSQL rejects a NUL byte inside
// text. The two parts therefore travel apart, and the query compares the two
// columns as a tuple.
type PairCursor struct {
	First  string
	Second string
}

func NewPairCursor(first, second string) (Cursor, error) {
	if first == "" || second == "" || !utf8.ValidString(first) || !utf8.ValidString(second) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(PairCursor{First: first, Second: second})
	if err != nil {
		return "", err
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(body)), nil
}

func DecodePairCursor(cursor Cursor) (PairCursor, error) {
	if cursor == "" {
		return PairCursor{}, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		return PairCursor{}, ErrInvalidCursor
	}
	var value PairCursor
	if err := json.Unmarshal(body, &value); err != nil || value.First == "" || value.Second == "" {
		return PairCursor{}, ErrInvalidCursor
	}
	return value, nil
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
