package domain

import (
	"strings"
	"testing"
)

// These cover the per-request work on the message write and pagination paths:
// every message carrying Block Kit or attachments is normalised before it is
// stored, and every paginated response encodes a cursor. Both scale with
// payload size, so the benchmarks vary size rather than reporting a single
// number.

func blockPayload(count int) []byte {
	var builder strings.Builder
	builder.WriteByte('[')
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"type":"section","text":{"type":"mrkdwn","text":"line of message text"}}`)
	}
	builder.WriteByte(']')
	return []byte(builder.String())
}

func BenchmarkNormalizeBlocks(b *testing.B) {
	for _, count := range []int{1, 10, 100} {
		payload := blockPayload(count)
		b.Run(sizeName(count), func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for index := 0; index < b.N; index++ {
				if _, err := NormalizeBlocks(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNormalizeAttachments(b *testing.B) {
	payload := blockPayload(10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		if _, err := NormalizeAttachments(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeUnfurls(b *testing.B) {
	values := make(map[string]string, 10)
	for index := 0; index < 10; index++ {
		values["https://example.com/"+strings.Repeat("a", index+1)] = `{"title":"page","text":"summary"}`
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		if _, err := NormalizeUnfurls(values); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListCursorRoundTrip(b *testing.B) {
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		cursor, err := NewListCursor("C0123456789")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := DecodeListCursor(cursor); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeScopes(b *testing.B) {
	raw := []string{"chat:write", "channels:history", "users:read", "  chat:write ", "files:read"}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		NormalizeScopes(raw)
	}
}

func sizeName(count int) string {
	switch count {
	case 1:
		return "1_block"
	case 10:
		return "10_blocks"
	default:
		return "100_blocks"
	}
}
