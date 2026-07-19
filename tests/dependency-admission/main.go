package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

const defaultInventoryPath = "specs/dependency-admission.yaml"

var (
	sha256DigestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitRevisionPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	moduleSumPattern     = regexp.MustCompile(`^h1:[A-Za-z0-9+/]+={0,2}$`)
	sha256HexPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	stableVersionPattern = regexp.MustCompile(`(?i)(?:[-.]alpha(?:[-.]|$)|[-.]beta(?:[-.]|$)|[-.]rc(?:[-.]|$)|[-.]dev(?:[-.]|$))`)
)

type inventory struct {
	Version         int          `yaml:"version"`
	Project         string       `yaml:"project"`
	QuarantineHours int          `yaml:"quarantine_hours"`
	Entries         []dependency `yaml:"entries"`
}

type dependency struct {
	ID          string `yaml:"id"`
	Kind        string `yaml:"kind"`
	Canonical   string `yaml:"canonical"`
	Source      string `yaml:"source"`
	Version     string `yaml:"version"`
	Revision    string `yaml:"revision"`
	PublishedAt string `yaml:"published_at"`
	Evidence    string `yaml:"evidence"`
	Checksum    string `yaml:"checksum"`
	Provenance  string `yaml:"provenance"`
	License     string `yaml:"license"`
	Purpose     string `yaml:"purpose"`
	Runtime     bool   `yaml:"runtime"`
}

func main() {
	path := flag.String("file", defaultInventoryPath, "dependency inventory path")
	asOf := flag.String("as-of", "", "UTC evaluation time in RFC3339 format; defaults to the current time")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("dependencycheck does not accept positional arguments"))
	}

	evaluationTime := time.Now().UTC()
	if *asOf != "" {
		parsed, err := time.Parse(time.RFC3339, *asOf)
		if err != nil {
			fail(fmt.Errorf("parse -as-of: %w", err))
		}
		evaluationTime = parsed.UTC()
	}

	body, err := os.ReadFile(*path)
	if err != nil {
		fail(err)
	}
	var value inventory
	if err := yaml.UnmarshalStrict(body, &value); err != nil {
		fail(fmt.Errorf("decode dependency inventory: %w", err))
	}
	if err := validate(value, evaluationTime); err != nil {
		fail(err)
	}
	if err := validateRepository(".", value); err != nil {
		fail(err)
	}
}

func validate(value inventory, evaluationTime time.Time) error {
	if value.Version != 1 {
		return fmt.Errorf("dependency inventory version is %d, want 1", value.Version)
	}
	if strings.TrimSpace(value.Project) == "" {
		return errors.New("dependency inventory requires project")
	}
	if value.QuarantineHours < 24 {
		return fmt.Errorf("dependency inventory quarantine_hours is %d, want at least 24", value.QuarantineHours)
	}
	if len(value.Entries) == 0 {
		return errors.New("dependency inventory has no entries")
	}

	seen := make(map[string]struct{}, len(value.Entries))
	cutoff := evaluationTime.Add(-time.Duration(value.QuarantineHours) * time.Hour)
	for index, item := range value.Entries {
		if err := validateEntry(item, index, cutoff); err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("dependency inventory contains duplicate ID %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func validateEntry(item dependency, index int, cutoff time.Time) error {
	position := fmt.Sprintf("entry %d", index)
	for name, value := range map[string]string{
		"id": item.ID, "kind": item.Kind, "canonical": item.Canonical,
		"source": item.Source, "version": item.Version, "revision": item.Revision,
		"published_at": item.PublishedAt, "evidence": item.Evidence,
		"checksum": item.Checksum, "provenance": item.Provenance,
		"license": item.License, "purpose": item.Purpose,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s %q is missing %s", position, item.ID, name)
		}
	}
	if !strings.HasPrefix(item.Source, "https://") || !strings.HasPrefix(item.Evidence, "https://") {
		return fmt.Errorf("%s %q source and evidence must use HTTPS", position, item.ID)
	}
	if stableVersionPattern.MatchString(item.Version) {
		return fmt.Errorf("%s %q uses a prerelease version %q", position, item.ID, item.Version)
	}
	publishedAt, err := time.Parse(time.RFC3339, item.PublishedAt)
	if err != nil {
		return fmt.Errorf("%s %q has invalid published_at: %w", position, item.ID, err)
	}
	if publishedAt.After(cutoff) {
		return fmt.Errorf("%s %q was published at %s, after the quarantine cutoff %s", position, item.ID, publishedAt.UTC().Format(time.RFC3339), cutoff.UTC().Format(time.RFC3339))
	}
	if !immutableRevision(item.Revision) {
		return fmt.Errorf("%s %q revision %q is not immutable", position, item.ID, item.Revision)
	}
	if !immutableChecksum(item.Checksum) {
		return fmt.Errorf("%s %q checksum %q is not a supported immutable checksum", position, item.ID, item.Checksum)
	}
	return nil
}

func immutableRevision(value string) bool {
	return gitRevisionPattern.MatchString(value) || sha256DigestPattern.MatchString(value) || moduleSumPattern.MatchString(value)
}

func immutableChecksum(value string) bool {
	if moduleSumPattern.MatchString(value) || sha256DigestPattern.MatchString(value) {
		return true
	}
	if strings.HasPrefix(value, "git:") {
		return gitRevisionPattern.MatchString(strings.TrimPrefix(value, "git:"))
	}
	if strings.HasPrefix(value, "sha256:") {
		return sha256HexPattern.MatchString(strings.TrimPrefix(value, "sha256:"))
	}
	return false
}

func validateRepository(root string, value inventory) error {
	byID := make(map[string]dependency, len(value.Entries))
	for _, item := range value.Entries {
		byID[item.ID] = item
	}
	goBody, err := os.ReadFile(root + "/go.mod")
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	goSumBody, err := os.ReadFile(root + "/go.sum")
	if err != nil {
		return fmt.Errorf("read go.sum: %w", err)
	}
	if err := validateGoModuleSums(string(goBody), string(goSumBody)); err != nil {
		return err
	}
	for _, module := range directGoModules(string(goBody)) {
		item, ok := byID["go/"+module.path]
		if !ok {
			return fmt.Errorf("direct Go module %q is absent from dependency inventory", module.path)
		}
		if item.Kind != "go-module" || item.Version != module.version {
			return fmt.Errorf("dependency inventory entry for Go module %q does not match go.mod version %q", module.path, module.version)
		}
	}
	workflowFiles, err := workflowPaths(root)
	if err != nil {
		return err
	}
	for _, path := range workflowFiles {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read workflow %q: %w", path, err)
		}
		for lineNumber, line := range strings.Split(string(body), "\n") {
			repository, revision, ok := parseActionUse(line)
			if !ok {
				continue
			}
			item, exists := byID["action/"+repository]
			if !exists {
				return fmt.Errorf("workflow %s:%d action %q is absent from dependency inventory", path, lineNumber+1, repository)
			}
			if item.Kind != "github-action" || item.Revision != revision {
				return fmt.Errorf("dependency inventory entry for action %q does not match workflow revision %q", repository, revision)
			}
		}
	}
	if err := validateWorkflowPins(root, workflowFiles); err != nil {
		return err
	}
	if err := validateDockerfiles(root); err != nil {
		return err
	}
	return nil
}

func validateGoModuleSums(goModBody, goSumBody string) error {
	sums := make(map[string]struct{})
	for _, line := range strings.Split(goSumBody, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && moduleSumPattern.MatchString(fields[2]) {
			sums[fields[0]+"@"+fields[1]] = struct{}{}
		}
	}
	for _, module := range declaredGoModules(goModBody, true) {
		key := module.path + "@" + module.version
		if _, ok := sums[key]; !ok {
			return fmt.Errorf("declared Go module %q has no archive checksum in go.sum", key)
		}
		if _, ok := sums[key+"/go.mod"]; !ok {
			return fmt.Errorf("declared Go module %q has no go.mod checksum in go.sum", key)
		}
	}
	return nil
}

type goModule struct {
	path    string
	version string
}

func directGoModules(body string) []goModule {
	return declaredGoModules(body, false)
}

func declaredGoModules(body string, includeIndirect bool) []goModule {
	var modules []goModule
	inRequireBlock := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "require (" {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && trimmed == ")" {
			inRequireBlock = false
			continue
		}
		if !inRequireBlock && !strings.HasPrefix(trimmed, "require ") {
			continue
		}
		if strings.HasPrefix(trimmed, "require ") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "require "))
		}
		if strings.HasPrefix(trimmed, "//") || (!includeIndirect && strings.Contains(trimmed, "// indirect")) {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		modules = append(modules, goModule{path: fields[0], version: fields[1]})
	}
	return modules
}

func workflowPaths(root string) ([]string, error) {
	entries, err := os.ReadDir(root + "/.github/workflows")
	if err != nil {
		return nil, fmt.Errorf("read workflow directory: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !(strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		paths = append(paths, root+"/.github/workflows/"+entry.Name())
	}
	return paths, nil
}

func parseActionUse(line string) (repository, revision string, ok bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "-")
	trimmed = strings.TrimSpace(trimmed)
	if !strings.HasPrefix(trimmed, "uses:") {
		return "", "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:"))
	if value == "" {
		return "", "", false
	}
	value = strings.Fields(value)[0]
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return "", "", false
	}
	repository = value[:separator]
	revision = value[separator+1:]
	if !gitRevisionPattern.MatchString(revision) {
		return repository, revision, true
	}
	return repository, revision, true
}

func validateWorkflowPins(root string, paths []string) error {
	contents := make([]string, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read workflow %q: %w", path, err)
		}
		contents = append(contents, string(body))
	}
	all := strings.Join(contents, "\n")
	for _, pin := range []string{
		"terraform_version: 1.13.5",
		"node-version: 24.7.0",
		"python-version: '3.12.11'",
		"java-version: '17.0.15'",
		"deno-version: 2.8.1",
		"version: 1.71.0",
		"dqlite-tools-v3=3.0.4~noble1",
		"libdqlite-dev=1.18.3~noble1",
		"libdqlite1.18=1.18.7~noble1",
		"libdqlite1.18-dev=1.18.7~noble1",
		"libuv1-dev=1.48.0-1.1build1",
	} {
		if !strings.Contains(all, pin) {
			return fmt.Errorf("workflow pin %q is absent", pin)
		}
	}
	return nil
}

func validateDockerfiles(root string) error {
	for _, path := range []string{root + "/Dockerfile", root + "/deploy/ecs-scale-zero/Dockerfile.websocket-edge"} {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Dockerfile %q: %w", path, err)
		}
		for lineNumber, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "# syntax=") {
				if !hasImageDigest(strings.TrimPrefix(trimmed, "# syntax=")) {
					return fmt.Errorf("Dockerfile %s:%d syntax image is not pinned by digest", path, lineNumber+1)
				}
				continue
			}
			if !strings.HasPrefix(trimmed, "FROM ") {
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) < 2 {
				return fmt.Errorf("Dockerfile %s:%d has an incomplete FROM instruction", path, lineNumber+1)
			}
			imageIndex := 1
			for imageIndex < len(fields) && strings.HasPrefix(fields[imageIndex], "--") {
				imageIndex++
			}
			if imageIndex == len(fields) || !hasImageDigest(fields[imageIndex]) {
				return fmt.Errorf("Dockerfile %s:%d base image is not pinned by digest", path, lineNumber+1)
			}
		}
	}
	return nil
}

func hasImageDigest(value string) bool {
	separator := strings.LastIndex(value, "@")
	return separator >= 0 && sha256DigestPattern.MatchString(value[separator+1:])
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dependencycheck:", err)
	os.Exit(1)
}
