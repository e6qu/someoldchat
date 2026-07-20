package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Block Kit, attachment, and unfurl normalisers are the first code to see
// client-supplied JSON on the message write path. They must reject malformed
// input rather than panic, and — because their output is persisted and later
// re-read — normalising an already-normalised value must be a no-op. A
// normaliser that is not idempotent lets a value drift each time a message is
// rewritten.

func fuzzArrayNormaliser(f *testing.F, normalise func([]byte) (string, error), label string) {
	f.Helper()
	f.Add([]byte(""))
	f.Add([]byte("[]"))
	f.Add([]byte(`[{"type":"section"}]`))
	f.Add([]byte(`[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`))
	f.Add([]byte(`  [ { "a" : 1 } ]  `))
	f.Add([]byte("null"))
	f.Add([]byte("[null]"))
	f.Add([]byte(`[1]`))
	f.Add([]byte(`{"type":"section"}`))
	f.Add([]byte(`[{"dup":1,"dup":2}]`))
	f.Add([]byte("[" + strings.Repeat(`{},`, 100) + "{}]"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		normalised, err := normalise(raw)
		if err != nil {
			if normalised != "" {
				t.Fatalf("%s rejected input but returned %q", label, normalised)
			}
			return
		}
		if normalised == "" {
			// Only blank input normalises to the empty sentinel.
			if strings.TrimSpace(string(raw)) != "" {
				t.Fatalf("%s accepted %q and returned empty", label, raw)
			}
			return
		}
		if !json.Valid([]byte(normalised)) {
			t.Fatalf("%s accepted %q and produced invalid JSON %q", label, raw, normalised)
		}
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(normalised), &items); err != nil {
			t.Fatalf("%s output %q is not a JSON array: %v", label, normalised, err)
		}
		if len(items) > 100 {
			t.Fatalf("%s accepted %d items, above the documented maximum", label, len(items))
		}
		for _, item := range items {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(item, &object); err != nil || object == nil {
				t.Fatalf("%s output contains a non-object item %q", label, item)
			}
		}
		// Persisted values are read back and normalised again; that must not
		// change them.
		again, err := normalise([]byte(normalised))
		if err != nil {
			t.Fatalf("%s rejected its own output %q: %v", label, normalised, err)
		}
		if again != normalised {
			t.Fatalf("%s is not idempotent: %q then %q", label, normalised, again)
		}
	})
}

func FuzzNormalizeBlocksIsSafeAndIdempotent(f *testing.F) {
	fuzzArrayNormaliser(f, NormalizeBlocks, "NormalizeBlocks")
}

func FuzzNormalizeAttachmentsIsSafeAndIdempotent(f *testing.F) {
	fuzzArrayNormaliser(f, NormalizeAttachments, "NormalizeAttachments")
}

func FuzzNormalizeUnfurlsIsSafeAndIdempotent(f *testing.F) {
	f.Add("https://example.com", `{"title":"x"}`)
	f.Add("", `{}`)
	f.Add("  spaced  ", `{"a":[1,2,3]}`)
	f.Add("k", `not json`)
	f.Add("k", ``)
	f.Add("k", `  {"a"  :  1}  `)

	f.Fuzz(func(t *testing.T, key, raw string) {
		normalised, err := NormalizeUnfurls(map[string]string{key: raw})
		if err != nil {
			if normalised != nil {
				t.Fatalf("NormalizeUnfurls rejected input but returned %v", normalised)
			}
			return
		}
		for gotKey, gotValue := range normalised {
			if strings.TrimSpace(gotKey) != gotKey || gotKey == "" {
				t.Fatalf("NormalizeUnfurls kept an untrimmed or empty key %q", gotKey)
			}
			if !json.Valid([]byte(gotValue)) {
				t.Fatalf("NormalizeUnfurls produced invalid JSON %q for key %q", gotValue, gotKey)
			}
		}
		again, err := NormalizeUnfurls(normalised)
		if err != nil {
			t.Fatalf("NormalizeUnfurls rejected its own output %v: %v", normalised, err)
		}
		if len(again) != len(normalised) {
			t.Fatalf("NormalizeUnfurls is not idempotent: %v then %v", normalised, again)
		}
		for gotKey, gotValue := range normalised {
			if again[gotKey] != gotValue {
				t.Fatalf("NormalizeUnfurls is not idempotent for %q: %q then %q", gotKey, gotValue, again[gotKey])
			}
		}
	})
}

// The kind check that replaced a full per-element decode must reject exactly
// what the decode rejected: anything that is not a JSON object.
func TestNormalizeArrayObjectsRejectsNonObjectItems(t *testing.T) {
	for _, raw := range []string{
		`[null]`, `[1]`, `["text"]`, `[true]`, `[false]`, `[[]]`, `[[{"a":1}]]`,
		`[{"a":1},null]`, `[{"a":1},2]`, `"not an array"`, `{"a":1}`, `null`, `123`,
	} {
		if _, err := NormalizeBlocks([]byte(raw)); err == nil {
			t.Fatalf("NormalizeBlocks(%s) was accepted, want rejection", raw)
		}
	}
	for _, raw := range []string{
		`[]`, `[{}]`, `[{"a":1}]`, `[ {"a":1} , {"b":2} ]`, `[{"a":{"b":[1,2,null]}}]`,
	} {
		if _, err := NormalizeBlocks([]byte(raw)); err != nil {
			t.Fatalf("NormalizeBlocks(%s) was rejected: %v", raw, err)
		}
	}
}
