package domain

import (
	"errors"
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

func TestMessageCursorRejectsAMessageWithoutACreationInstant(t *testing.T) {
	// Decoding rejects a zero CreatedAt, so minting must reject it too: otherwise
	// the store hands out a cursor that dead-ends the very next page request.
	if _, err := NewMessageCursor(Message{ID: "M1"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor for a message without a creation instant: err=%v, want %v", err, ErrInvalidCursor)
	}
	cursor, err := NewMessageCursor(Message{ID: "M1", CreatedAt: time.Unix(0, 1).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeMessageCursor(cursor); err != nil {
		t.Fatalf("every minted cursor must decode: %v", err)
	}
}

func TestParseMessageTimestampReportsOneSentinel(t *testing.T) {
	for _, value := range []MessageTimestamp{"", "1700000000", "1700000000.12", "abc.000000", "1700000000.abcdef"} {
		if _, err := ParseMessageTimestamp(value); !errors.Is(err, ErrInvalidMessageTimestamp) {
			t.Fatalf("ParseMessageTimestamp(%q) err=%v, want %v", value, err, ErrInvalidMessageTimestamp)
		}
	}
	parsed, err := ParseMessageTimestamp("1700000000.123456")
	if err != nil || !parsed.Equal(time.Unix(1700000000, 123456000).UTC()) {
		t.Fatalf("ParseMessageTimestamp round trip = %s err=%v", parsed, err)
	}
}
