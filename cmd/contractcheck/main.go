package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

const revision = "bc08db49625630e3585bf2f1322128ea04f2a7f3"

const (
	currentMethodsPath = "specs/upstream/slack-reference/current-methods.txt"
	currentMethodsHash = "0c591b74d588fa66ecf442fd672243582ecb2ddbf6de03a5652277bdb0b0d996"
	currentEventsPath  = "specs/upstream/slack-reference/current-events.txt"
	currentEventsHash  = "fcf14b31acd19e3666002f99af845cef1d4f5b1c56ad1984df83ac1c6880596b"
)

type source struct {
	Path string
	Hash string
}

type compatibilityLedger struct {
	Operations []operation `yaml:"operations"`
}

type operation struct {
	Method     string          `yaml:"method"`
	Status     string          `yaml:"status"`
	Provenance string          `yaml:"provenance"`
	Reference  string          `yaml:"reference"`
	Evidence   []string        `yaml:"evidence,omitempty"`
	Deviations []string        `yaml:"deviations,omitempty"`
	Audit      *downgradeAudit `yaml:"audit,omitempty"`
}

// downgradeAudit makes a correction to an overstated evidence level explicit.
// Compatibility evidence is normally monotonic, but forbidding every downgrade
// made a false claim permanent. A correction is accepted only when it names the
// exact prior status and retains both a human reason and reviewable evidence.
type downgradeAudit struct {
	DowngradedFrom string   `yaml:"downgraded_from"`
	Reason         string   `yaml:"reason"`
	Evidence       []string `yaml:"evidence"`
}

var statusRank = map[string]int{
	"unimplemented":          0,
	"schema-compatible":      1,
	"sdk-compatible":         2,
	"behavior-compatible":    3,
	"verified-against-slack": 4,
}

var sources = []source{
	{Path: "specs/upstream/slack-api-specs/web-api/slack_web_openapi_v2.json", Hash: "742a5c977180a829df8767cf57bc417d99b3713583aee83741efb9c08ca731e7"},
	{Path: "specs/upstream/slack-api-specs/events-api/slack_events_api_async_v1.json", Hash: "a491c82393abf9ef1aa38334cf71c747fbc2ea7a2b78533882ea24c888fe84be"},
	{Path: "specs/upstream/slack-api-specs/events-api/slack_common_event_wrapper_schema.json", Hash: "f6d1704676f4866fc62704086a916dd4c3d9f53a570b9e2976a80450e641d05a"},
}

func main() {
	ratchetBase := flag.String("ratchet-base", "", "git ref whose compatibility ledger must not regress")
	report := flag.Bool("report", false, "print compatibility progress")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("contractcheck does not accept positional arguments"))
	}
	if err := verify(); err != nil {
		fail(err)
	}
	if *ratchetBase != "" {
		if err := ratchet(*ratchetBase); err != nil {
			fail(err)
		}
	}
	if *report {
		if err := printReport(); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "contractcheck:", err)
	os.Exit(1)
}

func verify() error {
	ledger, err := os.ReadFile("specs/compatibility.yaml")
	if err != nil {
		return err
	}
	if !strings.Contains(string(ledger), revision) {
		return errors.New("ledger does not record vendored revision")
	}
	for _, item := range sources {
		body, err := os.ReadFile(item.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", item.Path, err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != item.Hash {
			return fmt.Errorf("checksum mismatch for %s: got %s want %s", item.Path, got, item.Hash)
		}
		var document map[string]any
		if err := json.Unmarshal(body, &document); err != nil {
			return fmt.Errorf("invalid JSON %s: %w", item.Path, err)
		}
	}
	var openapi struct {
		Swagger string                     `json:"swagger"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	// The error used to be discarded, so an I/O failure here surfaced as
	// "unexpected end of JSON input" and sent the operator to the wrong cause.
	body, err := os.ReadFile(sources[0].Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", sources[0].Path, err)
	}
	if err := json.Unmarshal(body, &openapi); err != nil {
		return fmt.Errorf("decode %s: %w", sources[0].Path, err)
	}
	if openapi.Swagger != "2.0" {
		return fmt.Errorf("OpenAPI version = %q", openapi.Swagger)
	}
	compatibility, err := decodeLedger(ledger)
	if err != nil {
		return fmt.Errorf("decode compatibility ledger: %w", err)
	}
	seenMethods := make(map[string]struct{}, len(compatibility.Operations))
	seenCurrentMethods := make(map[string]struct{}, len(compatibility.Operations))
	implementedMethods := make(map[string]struct{}, len(compatibility.Operations))
	for _, operation := range compatibility.Operations {
		if strings.TrimSpace(operation.Method) == "" || strings.TrimSpace(operation.Status) == "" {
			return errors.New("compatibility ledger contains an incomplete operation")
		}
		if _, exists := seenMethods[operation.Method]; exists {
			return fmt.Errorf("compatibility ledger contains duplicate operation %q", operation.Method)
		}
		switch operation.Status {
		case "unimplemented", "schema-compatible", "sdk-compatible", "behavior-compatible", "verified-against-slack":
		default:
			return fmt.Errorf("compatibility ledger operation %q has invalid status %q", operation.Method, operation.Status)
		}
		if operation.Audit != nil {
			if _, ok := statusRank[operation.Audit.DowngradedFrom]; !ok ||
				statusRank[operation.Status] >= statusRank[operation.Audit.DowngradedFrom] ||
				strings.TrimSpace(operation.Audit.Reason) == "" ||
				len(operation.Audit.Evidence) == 0 {
				return fmt.Errorf("compatibility ledger operation %q has an incomplete downgrade audit", operation.Method)
			}
			for _, evidence := range operation.Audit.Evidence {
				if strings.TrimSpace(evidence) == "" {
					return fmt.Errorf("compatibility ledger operation %q has empty downgrade evidence", operation.Method)
				}
			}
		}
		for _, evidence := range operation.Evidence {
			if strings.TrimSpace(evidence) == "" {
				return fmt.Errorf("compatibility ledger operation %q has empty evidence", operation.Method)
			}
		}
		for _, deviation := range operation.Deviations {
			if strings.TrimSpace(deviation) == "" {
				return fmt.Errorf("compatibility ledger operation %q has an empty deviation", operation.Method)
			}
		}
		seenMethods[operation.Method] = struct{}{}
		seenCurrentMethods[strings.ToLower(operation.Method)] = struct{}{}
		if operation.Status != "unimplemented" {
			implementedMethods[operation.Method] = struct{}{}
		}
		if operation.Provenance == "slack-reference" && !strings.HasPrefix(operation.Reference, "https://docs.slack.dev/reference/methods/") {
			return fmt.Errorf("compatibility ledger operation %q requires an official Slack reference", operation.Method)
		}
		if _, ok := openapi.Paths["/"+operation.Method]; !ok && operation.Provenance != "slack-reference" {
			return fmt.Errorf("compatibility ledger operation %q is absent from pinned OpenAPI", operation.Method)
		}
	}
	for method := range openapi.Paths {
		if _, ok := seenMethods[strings.TrimPrefix(method, "/")]; !ok {
			return fmt.Errorf("pinned OpenAPI operation %q is missing from compatibility ledger", method)
		}
	}
	if err := verifyCurrentMethods(seenCurrentMethods); err != nil {
		return err
	}
	if _, err := readCurrentCatalog(currentEventsPath, currentEventsHash, 150); err != nil {
		return fmt.Errorf("verify current Slack event catalog: %w", err)
	}
	for _, path := range []string{"/api.test", "/auth.revoke", "/auth.test", "/chat.postMessage", "/chat.meMessage", "/chat.update", "/chat.delete", "/chat.getPermalink", "/conversations.create", "/conversations.join", "/conversations.invite", "/conversations.leave", "/conversations.kick", "/conversations.rename", "/conversations.setTopic", "/conversations.setPurpose", "/conversations.archive", "/conversations.unarchive", "/conversations.close", "/conversations.open", "/conversations.mark", "/conversations.history", "/conversations.replies", "/conversations.info", "/conversations.list", "/conversations.members", "/files.delete", "/files.info", "/files.list", "/files.upload", "/pins.add", "/pins.remove", "/pins.list", "/reactions.add", "/reactions.remove", "/reactions.get", "/reactions.list", "/search.messages", "/team.info", "/users.info", "/users.list", "/users.lookupByEmail", "/users.getPresence", "/users.setPresence", "/users.profile.get", "/users.profile.set"} {
		if _, ok := openapi.Paths[path]; !ok {
			return fmt.Errorf("required path %s missing", path)
		}
	}
	if err := verifyHandlerRegistrations(slackHandlerPackage, implementedMethods, seenMethods); err != nil {
		return err
	}
	return nil
}

// verifyCurrentMethods complements the archived OpenAPI source with Slack's
// current method reference. The documentation sitemap lower-cases method page
// paths, so comparison is deliberately case-insensitive while the ledger keeps
// the public method casing consumed by SDKs.
func verifyCurrentMethods(ledgerMethods map[string]struct{}) error {
	lines, err := readCurrentCatalog(currentMethodsPath, currentMethodsHash, 310)
	if err != nil {
		return fmt.Errorf("verify current Slack method catalog: %w", err)
	}
	for _, method := range lines {
		if _, exists := ledgerMethods[method]; !exists {
			return fmt.Errorf("current Slack method %q is missing from compatibility ledger", method)
		}
	}
	return nil
}

func readCurrentCatalog(path, wantHash string, wantCount int) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != wantHash {
		return nil, fmt.Errorf("checksum mismatch for %s: got %s want %s", path, got, wantHash)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != wantCount {
		return nil, fmt.Errorf("%s contains %d entries, want %d", path, len(lines), wantCount)
	}
	previous := ""
	for _, line := range lines {
		entry := strings.TrimSpace(line)
		if entry == "" || entry != strings.ToLower(entry) {
			return nil, fmt.Errorf("%s contains invalid entry %q", path, line)
		}
		if entry <= previous {
			return nil, fmt.Errorf("%s is not strictly sorted at %q", path, entry)
		}
		previous = entry
	}
	return lines, nil
}

func decodeLedger(body []byte) (compatibilityLedger, error) {
	var ledger compatibilityLedger
	if err := yaml.Unmarshal(body, &ledger); err != nil {
		return compatibilityLedger{}, err
	}
	return ledger, nil
}

func ratchet(baseRef string) error {
	command := exec.Command("git", "show", baseRef+":specs/compatibility.yaml")
	body, err := command.Output()
	if err != nil {
		return fmt.Errorf("read compatibility ledger from %q: %w", baseRef, err)
	}
	baseline, err := decodeLedger(body)
	if err != nil {
		return fmt.Errorf("decode compatibility ledger from %q: %w", baseRef, err)
	}
	currentBody, err := os.ReadFile("specs/compatibility.yaml")
	if err != nil {
		return err
	}
	current, err := decodeLedger(currentBody)
	if err != nil {
		return fmt.Errorf("decode current compatibility ledger: %w", err)
	}
	if len(current.Operations) < len(baseline.Operations) {
		return fmt.Errorf("compatibility ledger operation count regressed: got %d, want at least %d", len(current.Operations), len(baseline.Operations))
	}
	currentByMethod := make(map[string]operation, len(current.Operations))
	for _, item := range current.Operations {
		currentByMethod[item.Method] = item
	}
	for _, previous := range baseline.Operations {
		currentItem, ok := currentByMethod[previous.Method]
		if !ok {
			return fmt.Errorf("compatibility ledger operation %q was removed", previous.Method)
		}
		if statusRank[currentItem.Status] < statusRank[previous.Status] && !auditedDowngrade(previous, currentItem) {
			return fmt.Errorf("compatibility ledger operation %q regressed from %q to %q", previous.Method, previous.Status, currentItem.Status)
		}
	}
	return nil
}

func auditedDowngrade(previous, current operation) bool {
	if current.Audit == nil || current.Audit.DowngradedFrom != previous.Status ||
		strings.TrimSpace(current.Audit.Reason) == "" || len(current.Audit.Evidence) == 0 {
		return false
	}
	return statusRank[current.Status] < statusRank[previous.Status]
}

func printReport() error {
	body, err := os.ReadFile("specs/compatibility.yaml")
	if err != nil {
		return err
	}
	ledger, err := decodeLedger(body)
	if err != nil {
		return fmt.Errorf("decode compatibility ledger: %w", err)
	}
	currentMethods, err := readCurrentCatalog(currentMethodsPath, currentMethodsHash, 310)
	if err != nil {
		return fmt.Errorf("read current method catalog: %w", err)
	}
	currentSet := make(map[string]struct{}, len(currentMethods))
	for _, method := range currentMethods {
		currentSet[method] = struct{}{}
	}
	current, legacy := partitionOperations(ledger.Operations, currentSet)
	printOperationReport("current", current)
	printOperationReport("legacy", legacy)
	fmt.Printf("ledger.operations=%d\n", len(ledger.Operations))
	return nil
}

func partitionOperations(operations []operation, current map[string]struct{}) ([]operation, []operation) {
	var currentOperations, legacyOperations []operation
	for _, item := range operations {
		if _, ok := current[strings.ToLower(item.Method)]; ok {
			currentOperations = append(currentOperations, item)
		} else {
			legacyOperations = append(legacyOperations, item)
		}
	}
	return currentOperations, legacyOperations
}

func printOperationReport(prefix string, operations []operation) {
	counts := cumulativeEvidenceCounts(operations)
	metadata := operationMetadataCounts(operations)
	total := len(operations)
	fmt.Printf("%s.operations=%d %s.implemented=%d/%d %s.verified-against-slack=%d/%d\n", prefix, total, prefix, metadata.Implemented, total, prefix, counts["verified-against-slack"], total)
	fmt.Printf("%s.unimplemented=%d %s.method-evidence=%d/%d %s.known-deviations=%d\n", prefix, metadata.Unimplemented, prefix, metadata.Evidenced, total, prefix, metadata.Deviating)
	fmt.Printf("%s.sdk-compatible-claims-with-method-evidence=%d/%d %s.sdk-compatible-claims-without-method-evidence=%d\n", prefix, metadata.EvidencedSDKClaims, metadata.SDKClaims, prefix, metadata.SDKClaims-metadata.EvidencedSDKClaims)
	for _, namespace := range unimplementedNamespaces(operations) {
		fmt.Printf("%s.unimplemented.%s=%d\n", prefix, namespace.Name, namespace.Count)
	}
	for _, status := range []string{"schema-compatible", "sdk-compatible", "behavior-compatible", "verified-against-slack"} {
		fmt.Printf("%s.%s-or-better=%d/%d\n", prefix, status, counts[status], total)
	}
}

type operationMetadata struct {
	Implemented        int
	Unimplemented      int
	Evidenced          int
	Deviating          int
	SDKClaims          int
	EvidencedSDKClaims int
}

func operationMetadataCounts(operations []operation) operationMetadata {
	var counts operationMetadata
	for _, item := range operations {
		if item.Status == "unimplemented" {
			counts.Unimplemented++
		} else {
			counts.Implemented++
		}
		if len(item.Evidence) != 0 {
			counts.Evidenced++
		}
		if len(item.Deviations) != 0 {
			counts.Deviating++
		}
		if statusRank[item.Status] >= statusRank["sdk-compatible"] {
			counts.SDKClaims++
			if len(item.Evidence) != 0 {
				counts.EvidencedSDKClaims++
			}
		}
	}
	return counts
}

type namespaceCount struct {
	Name  string
	Count int
}

func unimplementedNamespaces(operations []operation) []namespaceCount {
	counts := make(map[string]int)
	for _, operation := range operations {
		if operation.Status != "unimplemented" {
			continue
		}
		namespace := operation.Method
		if index := strings.IndexByte(namespace, '.'); index >= 0 {
			namespace = namespace[:index]
		}
		counts[namespace]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]namespaceCount, 0, len(names))
	for _, name := range names {
		result = append(result, namespaceCount{Name: name, Count: counts[name]})
	}
	return result
}

func cumulativeEvidenceCounts(operations []operation) map[string]int {
	counts := make(map[string]int, len(statusRank))
	for _, operation := range operations {
		rank := statusRank[operation.Status]
		for status, level := range statusRank {
			if rank >= level {
				counts[status]++
			}
		}
	}
	return counts
}

// slackHandlerPackage is the directory whose every non-test file registers the
// Slack surface. The gate reads the directory rather than a named file: a route
// registered in a second file of the same package is just as live as one in
// handler.go, and reading one file made such a route invisible to the ledger.
const slackHandlerPackage = "internal/api/slack"

// stringLiteral reports the value of an unadorned string literal. Anything the
// gate would have to evaluate — a constant, a concatenation, a variable — is
// deliberately not accepted, so it becomes a reported error rather than a route
// the gate cannot see.
func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

func verifyHandlerRegistrations(directory string, implementedMethods, ledgerMethods map[string]struct{}) error {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, directory, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		return fmt.Errorf("parse %s: %w", directory, err)
	}
	if len(packages) == 0 {
		return fmt.Errorf("parse %s: no non-test Go files", directory)
	}
	registered := make(map[string]struct{})
	var unreadable []string
	for _, pkg := range packages {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				// Handle registers a route exactly as HandleFunc does; matching
				// only the latter let a route ship undeclared.
				if !ok || (selector.Sel.Name != "HandleFunc" && selector.Sel.Name != "Handle") {
					return true
				}
				route, ok := stringLiteral(call.Args[0])
				if !ok {
					// A route this gate cannot read is a route it cannot hold to
					// the ledger. Refusing it keeps the ledger complete by
					// construction; skipping it silently did not.
					unreadable = append(unreadable, fmt.Sprintf("%s:%d", path, fileSet.Position(call.Pos()).Line))
					return true
				}
				parts := strings.Fields(route)
				if len(parts) != 2 || !strings.HasPrefix(parts[1], "/api/") {
					return true
				}
				method := strings.TrimPrefix(parts[1], "/api/")
				if !strings.Contains(method, "{") {
					registered[method] = struct{}{}
				}
				return true
			})
		}
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return fmt.Errorf("route registrations in %s must use a string literal so the compatibility ledger can be checked against them; found %d that cannot be read: %s",
			directory, len(unreadable), strings.Join(unreadable, ", "))
	}
	for method := range registered {
		if _, ok := ledgerMethods[method]; !ok {
			return fmt.Errorf("registered Slack handler %q is absent from compatibility ledger", method)
		}
	}
	for method := range implementedMethods {
		if _, ok := registered[method]; !ok {
			return fmt.Errorf("compatibility ledger operation %q has no registered Slack handler", method)
		}
	}
	return nil
}
