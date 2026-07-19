package main

import "testing"

func TestCumulativeEvidenceCountsTreatHigherEvidenceAsLowerEvidence(t *testing.T) {
	counts := cumulativeEvidenceCounts([]operation{
		{Method: "api.test", Status: "behavior-compatible"},
		{Method: "chat.postMessage", Status: "schema-compatible"},
		{Method: "files.list", Status: "unimplemented"},
		{Method: "auth.test", Status: "verified-against-slack"},
	})
	want := map[string]int{
		"unimplemented":          4,
		"schema-compatible":      3,
		"sdk-compatible":         2,
		"behavior-compatible":    2,
		"verified-against-slack": 1,
	}
	for status, expected := range want {
		if counts[status] != expected {
			t.Fatalf("%s count = %d, want %d", status, counts[status], expected)
		}
	}
}
