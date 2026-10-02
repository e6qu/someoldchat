package domain

import (
	"strings"
	"testing"
)

// The reference's own examples: a 32-character secret is long, the six- and
// ten-character secrets of tokens created before August 2016 are short.
func TestShortSecretFollowsTheReferenceExamples(t *testing.T) {
	for token, short := range map[string]bool{
		"xoxp-111-222-333-d6bc768406e5c2e6958cfc399b438004": false,
		"xoxp-111-222-333-d6bc76":                           true,
		"xoxp-111-222-333-d6bc768412":                       true,
		"xoxp-0123456789abcdef0123":                         true,
		"xoxp":                                              false,
		"xoxp-":                                             false,
	} {
		if got := HasShortSecret(token); got != short {
			t.Fatalf("HasShortSecret(%q) = %t, want %t", token, got, short)
		}
	}
}

// The replacement is "exactly the same as the original, but with a longer
// secret".
func TestLengthenTokenSecretKeepsEverySectionButTheSecret(t *testing.T) {
	replacement, err := LengthenTokenSecret("xoxp-111-222-333-d6bc76")
	if err != nil {
		t.Fatal(err)
	}
	kept, secret, ok := TokenSecret(replacement)
	if !ok || kept != "xoxp-111-222-333-" || len(secret) != LongTokenSecretLength || strings.Trim(secret, "0123456789abcdef") != "" {
		t.Fatalf("replacement = %q", replacement)
	}
	if HasShortSecret(replacement) {
		t.Fatalf("replacement %q is still short", replacement)
	}
	if other, _ := LengthenTokenSecret("xoxp-111-222-333-d6bc76"); other == replacement {
		t.Fatal("two replacements share a secret")
	}
}
