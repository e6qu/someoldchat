package slack

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func FuzzNormalizeJSONScalarNeverPanics(f *testing.F) {
	f.Add(`"text"`)
	f.Add(`true`)
	f.Add(`12.5`)
	f.Add(`{"nested":true}`)
	f.Fuzz(func(_ *testing.T, value string) {
		_, _ = normalizeJSONScalar([]byte(value))
	})
}

func FuzzDecodeFieldsNeverPanics(f *testing.F) {
	f.Add(`{"channel":"C1","limit":20}`)
	f.Add(`{"user_ids":["U1","U2"]}`)
	f.Add(`{"channel":"C1","channel":"C2"}`)
	f.Add("not-json")
	f.Fuzz(func(_ *testing.T, value string) {
		request := httptest.NewRequest("POST", "/api/test", strings.NewReader(value))
		request.Header.Set("Content-Type", "application/json")
		_, _ = decodeFields(httptest.NewRecorder(), request)
	})
}

func FuzzNormalizeJSONListFieldNeverPanics(f *testing.F) {
	f.Add(`["U1", "U2"]`)
	f.Add(`"U1,U2"`)
	f.Add(`null`)
	f.Fuzz(func(_ *testing.T, value string) {
		_, _ = normalizeJSONListField([]byte(value))
	})
}

// The history window, the reminder time parser and the ID list parser all read
// untrusted request values. None may panic, and parseSlackTimestamp must agree
// with itself for anything it accepts.
func FuzzParseSlackTimestampNeverPanics(f *testing.F) {
	f.Add("1700000000.000000")
	f.Add("1700000000")
	f.Add("")
	f.Add("-1.5")
	f.Add("9223372036854775807.999999")
	f.Fuzz(func(t *testing.T, value string) {
		micros, ok := parseSlackTimestamp(value)
		if ok && micros < 0 {
			t.Fatalf("parseSlackTimestamp(%q) accepted a negative instant %d", value, micros)
		}
	})
}

func FuzzReminderTimeNeverPanics(f *testing.F) {
	f.Add("300")
	f.Add("1700000000")
	f.Add("in 15 minutes")
	f.Add("")
	f.Add("-9223372036854775808")
	f.Fuzz(func(t *testing.T, value string) {
		when, err := reminderTime(value, time.Unix(1700000000, 0).UTC())
		if err == nil && when.IsZero() {
			t.Fatalf("reminderTime(%q) accepted and produced the zero time", value)
		}
		if err != nil && decodeErrorCode(err) != "cannot_parse" {
			t.Fatalf("reminderTime(%q) error code = %q, want cannot_parse", value, decodeErrorCode(err))
		}
	})
}

func FuzzParseIDListNeverPanics(f *testing.F) {
	f.Add(`["C1","C2"]`)
	f.Add("C1,C2")
	f.Add("[")
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		for _, id := range parseIDList[domain.ConversationID](value) {
			if strings.TrimSpace(string(id)) == "" {
				t.Fatalf("parseIDList(%q) produced a blank id", value)
			}
		}
	})
}

func FuzzClampLimitNeverPanics(f *testing.F) {
	f.Add("100")
	f.Add("0")
	f.Add("-1")
	f.Add("99999999999999999999")
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		limit, err := clampLimit(value, 100, 200)
		if err == nil && (limit < 1 || limit > 200) {
			t.Fatalf("clampLimit(%q) accepted %d, which is outside 1..200", value, limit)
		}
	})
}
