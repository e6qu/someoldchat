package main

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAcceptsAnAgedImmutableEntry(t *testing.T) {
	value := inventory{
		Version:         1,
		Project:         "sameoldchat",
		QuarantineHours: 24,
		Entries: []dependency{{
			ID: "module/example", Kind: "go-module", Canonical: "example",
			Source: "https://example.test/source", Version: "v1.2.3",
			Revision:    "0123456789012345678901234567890123456789",
			PublishedAt: "2026-07-18T00:00:00Z", Evidence: "https://example.test/evidence",
			Checksum: "h1:0123456789+/=", Provenance: "vcs-tag", License: "MIT",
			Purpose: "test dependency",
		}},
	}

	if err := validate(value, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestValidateRejectsAnEntryInsideTheQuarantine(t *testing.T) {
	value := validInventory()
	value.Entries[0].PublishedAt = "2026-07-19T12:00:00Z"

	err := validate(value, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "after the quarantine cutoff") {
		t.Fatalf("validate() error = %v, want quarantine error", err)
	}
}

func TestValidateRejectsMissingEvidence(t *testing.T) {
	value := validInventory()
	value.Entries[0].Evidence = ""

	err := validate(value, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "missing evidence") {
		t.Fatalf("validate() error = %v, want missing evidence error", err)
	}
}

func TestValidateRejectsMutableRevision(t *testing.T) {
	value := validInventory()
	value.Entries[0].Revision = "main"

	err := validate(value, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "not immutable") {
		t.Fatalf("validate() error = %v, want immutable revision error", err)
	}
}

func TestValidateRejectsDuplicateIDs(t *testing.T) {
	value := validInventory()
	value.Entries = append(value.Entries, value.Entries[0])

	err := validate(value, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "duplicate ID") {
		t.Fatalf("validate() error = %v, want duplicate ID error", err)
	}
}

func TestValidateRejectsPrerelease(t *testing.T) {
	value := validInventory()
	value.Entries[0].Version = "v1.2.3-rc.1"

	err := validate(value, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("validate() error = %v, want prerelease error", err)
	}
}

func TestImmutableChecksumAcceptsGitAndDigestChecksums(t *testing.T) {
	for _, value := range []string{
		"git:0123456789012345678901234567890123456789",
		"sha256:0123456789012345678901234567890123456789012345678901234567890123",
		"h1:0123456789+/=",
	} {
		if !immutableChecksum(value) {
			t.Errorf("immutableChecksum(%q) = false", value)
		}
	}
}

func TestDirectGoModulesIgnoresIndirectRequirements(t *testing.T) {
	got := directGoModules("require example.test/direct v1.2.3\n\nrequire (\n\texample.test/another v2.0.0\n\tExample.test/indirect v3.0.0 // indirect\n)")
	if len(got) != 2 || got[0].path != "example.test/direct" || got[1].path != "example.test/another" {
		t.Fatalf("directGoModules() = %#v", got)
	}
}

func TestParseActionUseRequiresAnImmutableRevision(t *testing.T) {
	repository, revision, ok := parseActionUse("- uses: actions/checkout@0123456789012345678901234567890123456789 # pinned")
	if !ok || repository != "actions/checkout" || revision != "0123456789012345678901234567890123456789" {
		t.Fatalf("parseActionUse() = %q, %q, %t", repository, revision, ok)
	}
}

func TestParseActionUseReportsMutableRevisionForCaller(t *testing.T) {
	_, revision, ok := parseActionUse("uses: actions/checkout@main")
	if !ok || revision != "main" {
		t.Fatalf("parseActionUse() = revision %q, ok %t", revision, ok)
	}
}

func TestHasImageDigestRequiresASha256Digest(t *testing.T) {
	if !hasImageDigest("docker.io/library/alpine:3.20@sha256:0123456789012345678901234567890123456789012345678901234567890123") {
		t.Fatal("hasImageDigest() rejected a valid digest")
	}
	for _, value := range []string{"alpine:3.20", "alpine:latest@sha1:0123", "alpine@sha256:short"} {
		if hasImageDigest(value) {
			t.Errorf("hasImageDigest(%q) accepted an invalid digest", value)
		}
	}
}

func validInventory() inventory {
	return inventory{
		Version:         1,
		Project:         "sameoldchat",
		QuarantineHours: 24,
		Entries: []dependency{{
			ID: "module/example", Kind: "go-module", Canonical: "example",
			Source: "https://example.test/source", Version: "v1.2.3",
			Revision:    "0123456789012345678901234567890123456789",
			PublishedAt: "2026-07-18T00:00:00Z", Evidence: "https://example.test/evidence",
			Checksum: "h1:0123456789+/=", Provenance: "vcs-tag", License: "MIT",
			Purpose: "test dependency",
		}},
	}
}
