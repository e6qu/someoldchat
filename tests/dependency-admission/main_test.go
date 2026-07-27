package main

import (
	"os"
	"path/filepath"
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
		"sha512-8nKv6+0RJSL9FE4jYOEGXnPeM/Hg12qZpmqzZjRh3qM0Y7c3z1mrOTfFLids72RDQYVh9WpLEfR5WdpNX4fkig==",
		"h1:0123456789+/=",
	} {
		if !immutableChecksum(value) {
			t.Errorf("immutableChecksum(%q) = false", value)
		}
	}
}

func TestDirectNPMPackagesRequiresExactResolvedDependencies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package-lock.json")
	body := `{"packages":{"":{"dependencies":{"runtime":"2.0.0"},"devDependencies":{"test":"1.0.0"}},"node_modules/runtime":{"version":"2.0.0","integrity":"sha512-cnVudGltZQ=="},"node_modules/test":{"version":"1.0.0","integrity":"sha512-dGVzdA=="}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	packages, err := directNPMPackages(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0].name != "runtime" || packages[1].name != "test" {
		t.Fatalf("directNPMPackages() = %#v", packages)
	}

	body = strings.Replace(body, `"test":"1.0.0"`, `"test":"^1.0.0"`, 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := directNPMPackages(path); err == nil || !strings.Contains(err.Error(), "mutable version constraint") {
		t.Fatalf("directNPMPackages() error = %v, want mutable constraint error", err)
	}
}

func TestDirectGoModulesIgnoresIndirectRequirements(t *testing.T) {
	got := directGoModules("require example.test/direct v1.2.3\n\nrequire (\n\texample.test/another v2.0.0\n\tExample.test/indirect v3.0.0 // indirect\n)")
	if len(got) != 2 || got[0].path != "example.test/direct" || got[1].path != "example.test/another" {
		t.Fatalf("directGoModules() = %#v", got)
	}
}

func TestValidateGoModuleSumsRequiresArchiveAndGoModChecksums(t *testing.T) {
	goMod := "require example.test/direct v1.2.3\n\nrequire (\n\texample.test/indirect v2.0.0 // indirect\n)"
	goSum := strings.Join([]string{
		"example.test/direct v1.2.3 h1:0123456789+/=",
		"example.test/direct v1.2.3/go.mod h1:0123456789+/=",
		"example.test/indirect v2.0.0 h1:0123456789+/=",
		"example.test/indirect v2.0.0/go.mod h1:0123456789+/=",
	}, "\n")
	if err := validateGoModuleSums(goMod, goSum); err != nil {
		t.Fatalf("validateGoModuleSums() error = %v", err)
	}
}

func TestValidateGoModuleSumsRejectsMissingIndirectChecksum(t *testing.T) {
	goMod := "require example.test/direct v1.2.3\n\nrequire (\n\texample.test/indirect v2.0.0 // indirect\n)"
	goSum := strings.Join([]string{
		"example.test/direct v1.2.3 h1:0123456789+/=",
		"example.test/direct v1.2.3/go.mod h1:0123456789+/=",
	}, "\n")
	if err := validateGoModuleSums(goMod, goSum); err == nil || !strings.Contains(err.Error(), "indirect@v2.0.0") {
		t.Fatalf("validateGoModuleSums() error = %v, want missing indirect checksum", err)
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

// specs/dependency-policy.md forbids `latest`. Six workflow jobs used
// `runs-on: ubuntu-latest` while five named an exact image, and nothing
// inspected `runs-on` at all.
//
// The rule then matched lines, so the identical selection written as a YAML
// sequence bypassed it entirely — proved with actionlint agreeing the workflow
// was valid and GitHub honouring it. Every case below is now decided from the
// parsed document, and a selection this cannot resolve is refused rather than
// skipped, which is what let the sequence form through in the first place.
func TestValidateJobRunnerRejectsFloatingLabelsInEveryShape(t *testing.T) {
	parse := func(t *testing.T, document string) workflowJob {
		t.Helper()
		path := filepath.Join(t.TempDir(), "workflow.yml")
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		parsed, err := parseWorkflow(path)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return parsed.Jobs["job"]
	}
	for name, document := range map[string]string{
		"scalar":            "jobs:\n  job:\n    runs-on: ubuntu-latest\n    steps: []\n",
		"sequence":          "jobs:\n  job:\n    runs-on:\n      - ubuntu-latest\n    steps: []\n",
		"flow sequence":     "jobs:\n  job:\n    runs-on: [ubuntu-latest]\n    steps: []\n",
		"matrix reference":  "jobs:\n  job:\n    runs-on: ${{ matrix.os }}\n    strategy:\n      matrix:\n        os: [ubuntu-latest]\n    steps: []\n",
		"matrix object":     "jobs:\n  job:\n    runs-on: ${{ matrix.arch.runner }}\n    strategy:\n      matrix:\n        arch:\n          - runner: ubuntu-latest\n    steps: []\n",
		"unresolvable":      "jobs:\n  job:\n    runs-on: ${{ vars.RUNNER }}\n    steps: []\n",
		"absent selection":  "jobs:\n  job:\n    steps: []\n",
		"missing matrix":    "jobs:\n  job:\n    runs-on: ${{ matrix.os }}\n    steps: []\n",
		"non-label runs-on": "jobs:\n  job:\n    runs-on: 24\n    steps: []\n",
	} {
		if err := validateJobRunner("workflow.yml", "job", parse(t, document)); err == nil {
			t.Errorf("%s: a runner selection that is floating or unresolvable was accepted", name)
		}
	}
	for name, document := range map[string]string{
		"scalar":            "jobs:\n  job:\n    runs-on: ubuntu-24.04\n    steps: []\n",
		"sequence":          "jobs:\n  job:\n    runs-on:\n      - ubuntu-24.04\n      - self-hosted\n    steps: []\n",
		"matrix object":     "jobs:\n  job:\n    runs-on: ${{ matrix.arch.runner }}\n    strategy:\n      matrix:\n        arch:\n          - runner: ubuntu-24.04\n          - runner: ubuntu-24.04-arm\n    steps: []\n",
		"reusable workflow": "jobs:\n  job:\n    uses: ./.github/workflows/ci.yml\n",
	} {
		if err := validateJobRunner("workflow.yml", "job", parse(t, document)); err != nil {
			t.Errorf("%s: %v, want acceptance", name, err)
		}
	}
}

// TestAGateIsNotSatisfiedByAComment covers the miss that defeated the whole
// publication contract: the required-target list was collected with grep over
// the raw ci.yml, so deleting `run: make test` and leaving the comment
// "# we used to run make test here" published a container with no tests run.
func TestAGateIsNotSatisfiedByAComment(t *testing.T) {
	commands := jobCommands(workflowJob{Steps: []workflowStep{
		{Run: "# we used to run make test here\nmake vet"},
		{Run: "echo done"},
	}})
	invoked := makeTargetsInvoked(commands)
	if _, ok := invoked["test"]; ok {
		t.Fatal("a shell comment satisfied a required gate")
	}
	if _, ok := invoked["vet"]; !ok {
		t.Fatal("a real make invocation was not collected")
	}
	// A step that does not exist runs nothing at all.
	if len(makeTargetsInvoked(jobCommands(workflowJob{}))) != 0 {
		t.Fatal("a job with no steps invoked a gate")
	}
}

// The repository's own workflows are the fixture that keeps the contract
// honest: it must hold at HEAD.
func TestTheRepositoryWorkflowsSatisfyTheStructureContract(t *testing.T) {
	if err := validateWorkflowStructure("../.."); err != nil {
		t.Fatalf("workflow structure contract: %v", err)
	}
}
