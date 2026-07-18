package domain

import (
	"testing"
	"time"
)

func FuzzDecodeListCursorNeverPanics(f *testing.F) {
	f.Add("")
	f.Add("eyJJRCI6ImMxIn0")
	f.Add("not-a-cursor")
	f.Fuzz(func(_ *testing.T, value string) {
		_, _ = DecodeListCursor(Cursor(value))
	})
}

func FuzzDecodeMessageCursorNeverPanics(f *testing.F) {
	f.Add("")
	f.Add("eyJDcmVhdGVkQXQiOiIxOTcwLTAxLTAxVDAwOjAwOjAwWiIsIklEIjoiTTMifQ")
	f.Add("not-a-cursor")
	f.Fuzz(func(_ *testing.T, value string) {
		_, _, _ = DecodeMessageCursor(Cursor(value))
	})
}

func FuzzListCursorRoundTrips(f *testing.F) {
	f.Add("conversation-1")
	f.Add("unicode-✓")
	f.Fuzz(func(t *testing.T, id string) {
		if id == "" {
			t.Skip()
		}
		cursor, err := NewListCursor(id)
		if err != nil {
			return
		}
		got, err := DecodeListCursor(cursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		if got != id {
			t.Fatalf("decoded id %q, want %q", got, id)
		}
	})
}

func FuzzMessageCursorRoundTrips(f *testing.F) {
	f.Add("message-1", int64(1))
	f.Add("unicode-✓", int64(-1))
	f.Fuzz(func(t *testing.T, id string, unixNano int64) {
		if id == "" {
			t.Skip()
		}
		message := Message{ID: MessageID(id), CreatedAt: time.Unix(0, unixNano).UTC()}
		cursor, err := NewMessageCursor(message)
		if err != nil {
			return
		}
		createdAt, gotID, err := DecodeMessageCursor(cursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		if gotID != message.ID || !createdAt.Equal(message.CreatedAt) {
			t.Fatalf("decoded cursor (%s, %s), want (%s, %s)", createdAt, gotID, message.CreatedAt, message.ID)
		}
	})
}

func TestCursorRejectsInvalidUTF8(t *testing.T) {
	invalidID := string([]byte{0xff})
	if _, err := NewListCursor(invalidID); err != ErrInvalidCursor {
		t.Fatalf("list cursor error = %v, want %v", err, ErrInvalidCursor)
	}
	if _, err := NewMessageCursor(Message{ID: MessageID(invalidID), CreatedAt: time.Unix(0, 1)}); err != ErrInvalidCursor {
		t.Fatalf("message cursor error = %v, want %v", err, ErrInvalidCursor)
	}
}
