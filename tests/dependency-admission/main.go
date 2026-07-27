package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

const defaultInventoryPath = "specs/dependency-admission.yaml"

var (
	sha256DigestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitRevisionPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	moduleSumPattern     = regexp.MustCompile(`^h1:[A-Za-z0-9+/]+={0,2}$`)
	sriChecksumPattern   = regexp.MustCompile(`^sha512-[A-Za-z0-9+/]+={0,2}$`)
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
	if moduleSumPattern.MatchString(value) || sriChecksumPattern.MatchString(value) || sha256DigestPattern.MatchString(value) {
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
	npmModules, err := directNPMPackages(root + "/tests/browser/package-lock.json")
	if err != nil {
		return err
	}
	for _, module := range npmModules {
		item, ok := byID["npm/"+module.name]
		if !ok {
			return fmt.Errorf("direct npm package %q is absent from dependency inventory", module.name)
		}
		if item.Kind != "npm-package" || item.Version != module.version || item.Checksum != module.integrity {
			return fmt.Errorf("dependency inventory entry for npm package %q does not match package-lock.json version %q and integrity %q", module.name, module.version, module.integrity)
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
			// specs/dependency-policy.md requires a full commit SHA. It was
			// enforced only as a side effect of the inventory comparison below,
			// while parseActionUse contained a conditional whose two branches
			// were identical, so the rule looked enforced and was not.
			if !gitRevisionPattern.MatchString(revision) {
				return fmt.Errorf("workflow %s:%d action %q is pinned to %q, not a full 40-character commit SHA", path, lineNumber+1, repository, revision)
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
	if err := validateWorkflowPins(root, workflowFiles, byID); err != nil {
		return err
	}
	if err := validateDockerfiles(root, byID); err != nil {
		return err
	}
	if err := validateTerraformPins(root, byID); err != nil {
		return err
	}
	if err := validateToolPins(root, byID); err != nil {
		return err
	}
	if err := validateSourceCheckoutPins(root, byID); err != nil {
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

type npmPackage struct {
	name      string
	version   string
	integrity string
}

func directNPMPackages(path string) ([]npmPackage, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read npm lockfile: %w", err)
	}
	var lock struct {
		Packages map[string]struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
			Version         string            `json:"version"`
			Integrity       string            `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &lock); err != nil {
		return nil, fmt.Errorf("decode npm lockfile: %w", err)
	}
	root, ok := lock.Packages[""]
	if !ok {
		return nil, errors.New("npm lockfile is missing its root package")
	}
	direct := make(map[string]string, len(root.Dependencies)+len(root.DevDependencies))
	for name, version := range root.Dependencies {
		direct[name] = version
	}
	for name, version := range root.DevDependencies {
		direct[name] = version
	}
	modules := make([]npmPackage, 0, len(direct))
	for name, requestedVersion := range direct {
		resolved, ok := lock.Packages["node_modules/"+name]
		if !ok || resolved.Version == "" || resolved.Integrity == "" {
			return nil, fmt.Errorf("direct npm package %q has no resolved version and integrity", name)
		}
		if requestedVersion != resolved.Version {
			return nil, fmt.Errorf("direct npm package %q uses mutable version constraint %q instead of exact version %q", name, requestedVersion, resolved.Version)
		}
		modules = append(modules, npmPackage{name: name, version: resolved.Version, integrity: resolved.Integrity})
	}
	slices.SortFunc(modules, func(left, right npmPackage) int { return strings.Compare(left.name, right.name) })
	return modules, nil
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
	return repository, revision, true
}

// validateWorkflowPins rejects any version selection in a workflow that is not a
// full exact version, and requires every workflow container image to carry a
// digest that the inventory records.
//
// This used to assert that eleven exact strings appeared somewhere in the
// concatenated workflow text, which enforced nothing that
// specs/dependency-policy.md claims: adding `node-version: 24` in a new job
// passed unchanged as long as `node-version: 24.7.0` still appeared elsewhere,
// and a removed setup step was invisible because only the literal was checked.
func validateWorkflowPins(root string, paths []string, byID map[string]dependency) error {
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read workflow %q: %w", path, err)
		}
		lines := strings.Split(string(body), "\n")
		for index, line := range lines {
			if err := validateWorkflowVersionKey(path, index+1, line); err != nil {
				return err
			}
			if err := validateWorkflowImage(path, index+1, line, byID); err != nil {
				return err
			}
		}
		if err := validateAptPins(path, lines); err != nil {
			return err
		}
	}
	return nil
}

var (
	workflowVersionKeyPattern = regexp.MustCompile(`^[\t ]*(?:-[\t ]+)?([A-Za-z0-9_.-]*[Vv]ersion)[\t ]*:[\t ]*(.+?)[\t ]*$`)
	workflowImageKeyPattern   = regexp.MustCompile(`^[\t ]*(?:-[\t ]+)?image[\t ]*:[\t ]*(.+?)[\t ]*$`)
	exactVersionPattern       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	aptPackagePattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
)

// versionKeysNamingAFile lists keys whose value is a path, not a version.
var versionKeysNamingAFile = map[string]struct{}{"go-version-file": {}}

func validateWorkflowVersionKey(path string, lineNumber int, line string) error {
	match := workflowVersionKeyPattern.FindStringSubmatch(line)
	if match == nil {
		return nil
	}
	key := match[1]
	if _, ok := versionKeysNamingAFile[key]; ok {
		return nil
	}
	value := unquoteYAMLScalar(match[2])
	// A templated value is resolved at run time and cannot be checked here; a
	// bare "1" style selection is exactly what the policy forbids.
	if value == "" || strings.Contains(value, "${{") {
		return nil
	}
	if !exactVersionPattern.MatchString(strings.TrimPrefix(value, "v")) {
		return fmt.Errorf("workflow %s:%d selects %s %q, which is not an exact major.minor.patch version", path, lineNumber, key, value)
	}
	return nil
}

func validateWorkflowImage(path string, lineNumber int, line string, byID map[string]dependency) error {
	match := workflowImageKeyPattern.FindStringSubmatch(line)
	if match == nil {
		return nil
	}
	reference := unquoteYAMLScalar(match[1])
	if reference == "" || strings.Contains(reference, "${{") {
		return nil
	}
	if !hasImageDigest(reference) {
		return fmt.Errorf("workflow %s:%d container image %q is not pinned by digest", path, lineNumber, reference)
	}
	return requireInventoriedImage(fmt.Sprintf("workflow %s:%d", path, lineNumber), reference, byID)
}

// validateAptPins requires every package in an apt-get install invocation to
// carry an explicit "=version". The literal allow-list it replaces could only
// notice a removed pin, never an added unpinned package.
func validateAptPins(path string, lines []string) error {
	for index := 0; index < len(lines); index++ {
		if !strings.Contains(lines[index], "apt-get install") {
			continue
		}
		command := lines[index]
		for strings.HasSuffix(strings.TrimRight(command, " \t"), "\\") && index+1 < len(lines) {
			command = strings.TrimSuffix(strings.TrimRight(command, " \t"), "\\") + " " + lines[index+1]
			index++
		}
		fields := strings.Fields(command)
		for position, field := range fields {
			if !aptPackagePattern.MatchString(field) {
				continue
			}
			// Skip the command words themselves and anything before "install".
			if position == 0 || field == "install" || field == "apt-get" || field == "sudo" {
				continue
			}
			installed := false
			for _, earlier := range fields[:position] {
				if earlier == "install" {
					installed = true
				}
			}
			if installed {
				return fmt.Errorf("workflow %s installs package %q without an explicit =version", path, field)
			}
		}
	}
	return nil
}

func unquoteYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			value = value[1 : len(value)-1]
		}
	}
	return strings.TrimSpace(value)
}

// dockerfilePaths walks the repository instead of naming two files, so a new
// Dockerfile cannot escape digest pinning by simply not being on a hardcoded
// list.
func dockerfilePaths(root string) ([]string, error) {
	skipped := map[string]struct{}{".git": {}, ".cache": {}, ".terraform": {}, ".qualification": {}, "node_modules": {}, "bin": {}, "dist": {}}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if _, ok := skipped[entry.Name()]; ok {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.") || strings.HasSuffix(name, ".Dockerfile") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk for Dockerfiles: %w", err)
	}
	slices.Sort(paths)
	if len(paths) == 0 {
		return nil, errors.New("no Dockerfile found; the digest-pinning check would silently cover nothing")
	}
	return paths, nil
}

func validateDockerfiles(root string, byID map[string]dependency) error {
	paths, err := dockerfilePaths(root)
	if err != nil {
		return err
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Dockerfile %q: %w", path, err)
		}
		for lineNumber, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "# syntax=") {
				reference := strings.TrimPrefix(trimmed, "# syntax=")
				if !hasImageDigest(reference) {
					return fmt.Errorf("Dockerfile %s:%d syntax image is not pinned by digest", path, lineNumber+1)
				}
				if err := requireInventoriedImage(fmt.Sprintf("Dockerfile %s:%d", path, lineNumber+1), reference, byID); err != nil {
					return err
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
			if err := requireInventoriedImage(fmt.Sprintf("Dockerfile %s:%d", path, lineNumber+1), fields[imageIndex], byID); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireInventoriedImage ties a digest-pinned reference to an inventory entry,
// so the 24-hour publication quarantine finally applies to the images the
// product actually ships and builds with. Asserting only that "a digest is
// present" let a base-image bump published minutes ago pass untouched.
func requireInventoriedImage(position, reference string, byID map[string]dependency) error {
	name, tag, digest := splitImageReference(reference)
	name = normalizeImageName(name)
	item, ok := byID["container/"+name]
	if !ok {
		return fmt.Errorf("%s container image %q is absent from the dependency inventory", position, name)
	}
	if item.Kind != "container-image" {
		return fmt.Errorf("%s inventory entry for %q has kind %q, want container-image", position, name, item.Kind)
	}
	if item.Revision != digest || item.Checksum != digest {
		return fmt.Errorf("%s container image %q uses digest %q, which the inventory does not record", position, name, digest)
	}
	if tag == "" {
		return fmt.Errorf("%s container image %q carries no tag, so a digest bump cannot be reviewed against a version", position, name)
	}
	if tag != item.Version {
		return fmt.Errorf("%s container image %q is tagged %q while the inventory records version %q", position, name, tag, item.Version)
	}
	return nil
}

// normalizeImageName expands a short Docker Hub reference to its canonical form,
// so `postgres` and `docker.io/library/postgres` resolve to one inventory entry.
func normalizeImageName(name string) string {
	first, _, hasSlash := strings.Cut(name, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return name
	}
	if !hasSlash {
		return "docker.io/library/" + name
	}
	return "docker.io/" + name
}

// splitImageReference separates name, tag, and digest. A digest may be present
// with or without a tag; a registry host may carry a port, so the tag separator
// is searched after the last path element begins.
func splitImageReference(reference string) (name, tag, digest string) {
	if at := strings.LastIndex(reference, "@"); at >= 0 {
		digest = reference[at+1:]
		reference = reference[:at]
	}
	lastSlash := strings.LastIndex(reference, "/")
	if colon := strings.LastIndex(reference, ":"); colon > lastSlash {
		tag = reference[colon+1:]
		reference = reference[:colon]
	}
	return reference, tag, digest
}

var (
	terraformRequiredVersionPattern = regexp.MustCompile(`required_version[\t ]*=[\t ]*"([^"]*)"`)
	terraformProviderPattern        = regexp.MustCompile(`source[\t ]*=[\t ]*"([^"]*)"[,\s]*version[\t ]*=[\t ]*"([^"]*)"`)
	terraformProviderBlockPattern   = regexp.MustCompile(`(?s)source[\t ]*=[\t ]*"([^"]*)"\s*\n\s*version[\t ]*=[\t ]*"([^"]*)"`)
)

// validateTerraformPins enforces specs/dependency-policy.md on *.tf: an exact
// Terraform version and an exact provider version, each recorded in the
// inventory. Nothing checked these before, so every constraint was a floating
// range and `terraform init` in CI could silently take any newer provider — with
// no quarantine and no inventory entry.
func validateTerraformPins(root string, byID map[string]dependency) error {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", ".terraform", ".qualification", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".tf") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk for Terraform configuration: %w", err)
	}
	slices.Sort(paths)
	terraform, ok := byID["tool/terraform"]
	if !ok {
		return errors.New("inventory has no tool/terraform entry, so the Terraform version pin cannot be verified")
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Terraform configuration %q: %w", path, err)
		}
		text := string(body)
		for _, match := range terraformRequiredVersionPattern.FindAllStringSubmatch(text, -1) {
			if match[1] != terraform.Version {
				return fmt.Errorf("%s pins required_version = %q; the inventory records Terraform %s and specs/dependency-policy.md requires an exact version", path, match[1], terraform.Version)
			}
		}
		matches := terraformProviderPattern.FindAllStringSubmatch(text, -1)
		matches = append(matches, terraformProviderBlockPattern.FindAllStringSubmatch(text, -1)...)
		for _, match := range matches {
			source, version := match[1], match[2]
			if !exactVersionPattern.MatchString(version) {
				return fmt.Errorf("%s pins provider %q to %q, which is not an exact major.minor.patch version", path, source, version)
			}
			item, exists := byID["terraform-provider/"+source]
			if !exists {
				return fmt.Errorf("%s provider %q is absent from the dependency inventory", path, source)
			}
			if item.Kind != "terraform-provider" || item.Version != version {
				return fmt.Errorf("inventory entry for provider %q does not match the %q version %q", source, path, version)
			}
		}
	}
	return nil
}

var makefileToolVersionPattern = regexp.MustCompile(`(?m)^([A-Z0-9_]+)_VERSION[\t ]*:?=[\t ]*([0-9][0-9A-Za-z.+-]*)[\t ]*$`)

// makefileToolInventoryIDs maps a Makefile version variable to the inventory
// entry that must agree with it, so a pinned generator cannot drift from its
// recorded integrity evidence.
var makefileToolInventoryIDs = map[string]string{
	"PROTOC_GEN_GO_GRPC": "tool/google.golang.org/grpc/cmd/protoc-gen-go-grpc",
	"GOVULNCHECK":        "tool/golang.org/x/vuln",
}

func validateToolPins(root string, byID map[string]dependency) error {
	body, err := os.ReadFile(root + "/Makefile")
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	found := make(map[string]struct{}, len(makefileToolInventoryIDs))
	for _, match := range makefileToolVersionPattern.FindAllStringSubmatch(string(body), -1) {
		identifier, ok := makefileToolInventoryIDs[match[1]]
		if !ok {
			continue
		}
		found[match[1]] = struct{}{}
		item, exists := byID[identifier]
		if !exists {
			return fmt.Errorf("Makefile pins %s_VERSION but %s is absent from the dependency inventory", match[1], identifier)
		}
		if item.Version != strings.TrimPrefix(match[2], "v") {
			return fmt.Errorf("Makefile pins %s_VERSION = %s while the inventory records %s", match[1], match[2], item.Version)
		}
	}
	for variable, identifier := range makefileToolInventoryIDs {
		if _, ok := found[variable]; !ok {
			return fmt.Errorf("Makefile no longer defines %s_VERSION, so the pin for %s is unenforced", variable, identifier)
		}
	}
	return nil
}

var (
	shauthWorkflowRefPattern = regexp.MustCompile(`(?m)^[\t ]*ref:[\t ]*([0-9a-f]{40})[\t ]*$`)
	shauthScriptPinPattern   = regexp.MustCompile(`(?m)^expected_shauth_commit=([0-9a-f]{40})[\t ]*$`)
)

// validateSourceCheckoutPins covers the third-party repository CI checks out and
// then executes. parseActionUse inspects only `uses:` values, so the
// repository/ref inputs of a checkout step were never compared with anything.
func validateSourceCheckoutPins(root string, byID map[string]dependency) error {
	item, ok := byID["source/e6qu/shauth"]
	if !ok {
		return errors.New("inventory has no source/e6qu/shauth entry, yet CI checks out and executes that repository")
	}
	workflow, err := os.ReadFile(root + "/.github/workflows/ci.yml")
	if err != nil {
		return fmt.Errorf("read integration workflow: %w", err)
	}
	refs := shauthWorkflowRefPattern.FindAllStringSubmatch(string(workflow), -1)
	if len(refs) != 1 || refs[0][1] != item.Revision {
		return fmt.Errorf("ci.yml must check out exactly the inventoried Shauth revision %s", item.Revision)
	}
	script, err := os.ReadFile(root + "/scripts/test-shauth-sso.sh")
	if err != nil {
		return fmt.Errorf("read Shauth qualification script: %w", err)
	}
	pins := shauthScriptPinPattern.FindAllStringSubmatch(string(script), -1)
	if len(pins) != 1 || pins[0][1] != item.Revision {
		return fmt.Errorf("scripts/test-shauth-sso.sh must assert the inventoried Shauth revision %s", item.Revision)
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
