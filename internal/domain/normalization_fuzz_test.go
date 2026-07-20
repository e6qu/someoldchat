package domain

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestNormalizeBlocksCompactsObjectsAndRejectsScalars(t *testing.T) {
	got, err := NormalizeBlocks([]byte(` [ { "type": "section", "text": {"type":"plain_text","text":"hello"} } ] `))
	if err != nil || got != `[{"type":"section","text":{"type":"plain_text","text":"hello"}}]` {
		t.Fatalf("blocks=%q err=%v", got, err)
	}
	for _, raw := range []string{`{}`, `null`, `["not an object"]`} {
		if _, err := NormalizeBlocks([]byte(raw)); err == nil {
			t.Fatalf("invalid blocks %s were accepted", raw)
		}
	}
}

func FuzzNormalizeScopes(f *testing.F) {
	f.Add(" chat:write \x00chat:write\x00\x00 channels:read ")
	f.Add("users:read\x00users:read\x00  \x00chat:write")
	f.Fuzz(func(t *testing.T, value string) {
		scopes := strings.Split(value, "\x00")
		got := NormalizeScopes(scopes)
		if !sort.StringsAreSorted(got) {
			t.Fatalf("scopes are not sorted: %q", got)
		}
		for index, scope := range got {
			if scope == "" || scope != strings.TrimSpace(scope) {
				t.Fatalf("scope was not normalized: %q", scope)
			}
			if index > 0 && got[index-1] == scope {
				t.Fatalf("scope was not deduplicated: %q", got)
			}
		}
		if normalized := NormalizeScopes(got); !reflect.DeepEqual(normalized, got) {
			t.Fatalf("normalization is not idempotent: got %q, normalized %q", got, normalized)
		}
	})
}

func FuzzNormalizeConversationTypes(f *testing.F) {
	f.Add(" public_channel \x00public_channel\x00\x00IM")
	f.Add("private_channel\x00mpim")
	f.Fuzz(func(t *testing.T, value string) {
		values := strings.Split(value, "\x00")
		got, err := NormalizeConversationTypes(values)
		if err != nil {
			return
		}
		if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
			t.Fatalf("conversation types are not sorted: %q", got)
		}
		for index, value := range got {
			if index > 0 && got[index-1] == value {
				t.Fatalf("conversation type was not deduplicated: %q", value)
			}
		}
		if err := ValidateConversationTypes(got); err != nil {
			t.Fatalf("normalized types failed validation: %v", err)
		}
	})
}
