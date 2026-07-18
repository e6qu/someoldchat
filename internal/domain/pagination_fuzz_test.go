package domain

import "testing"

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
