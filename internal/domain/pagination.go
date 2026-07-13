package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type Cursor string

type PageRequest struct {
	Limit  int
	Cursor Cursor
}

type ConversationType string

const (
	ConversationTypePublic  ConversationType = "public_channel"
	ConversationTypePrivate ConversationType = "private_channel"
	ConversationTypeIM      ConversationType = "im"
	ConversationTypeMPIM    ConversationType = "mpim"
)

type ConversationListRequest struct {
	Limit           int
	Cursor          Cursor
	ExcludeArchived bool
	Types           []ConversationType
	MemberUserID    UserID
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
	if id == "" {
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

func NewMessageCursor(message Message) (Cursor, error) {
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
