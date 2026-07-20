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
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

const revision = "bc08db49625630e3585bf2f1322128ea04f2a7f3"

type source struct {
	Path string
	Hash string
}

type compatibilityLedger struct {
	Operations []operation `yaml:"operations"`
}

type operation struct {
	Method     string `yaml:"method"`
	Status     string `yaml:"status"`
	Provenance string `yaml:"provenance"`
	Reference  string `yaml:"reference"`
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
	body, _ := os.ReadFile(sources[0].Path)
	if err := json.Unmarshal(body, &openapi); err != nil {
		return err
	}
	if openapi.Swagger != "2.0" {
		return fmt.Errorf("OpenAPI version = %q", openapi.Swagger)
	}
	compatibility, err := decodeLedger(ledger)
	if err != nil {
		return fmt.Errorf("decode compatibility ledger: %w", err)
	}
	seenMethods := make(map[string]struct{}, len(compatibility.Operations))
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
		seenMethods[operation.Method] = struct{}{}
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
	for _, path := range []string{"/api.test", "/auth.revoke", "/auth.test", "/chat.postMessage", "/chat.meMessage", "/chat.update", "/chat.delete", "/chat.getPermalink", "/conversations.create", "/conversations.join", "/conversations.invite", "/conversations.leave", "/conversations.kick", "/conversations.rename", "/conversations.setTopic", "/conversations.setPurpose", "/conversations.archive", "/conversations.unarchive", "/conversations.close", "/conversations.open", "/conversations.mark", "/conversations.history", "/conversations.replies", "/conversations.info", "/conversations.list", "/conversations.members", "/files.delete", "/files.info", "/files.list", "/files.upload", "/pins.add", "/pins.remove", "/pins.list", "/reactions.add", "/reactions.remove", "/reactions.get", "/reactions.list", "/search.messages", "/team.info", "/users.info", "/users.list", "/users.lookupByEmail", "/users.getPresence", "/users.setPresence", "/users.profile.get", "/users.profile.set"} {
		if _, ok := openapi.Paths[path]; !ok {
			return fmt.Errorf("required path %s missing", path)
		}
	}
	if err := verifyHandlerRegistrations(implementedMethods, seenMethods); err != nil {
		return err
	}
	return nil
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
		if statusRank[currentItem.Status] < statusRank[previous.Status] {
			return fmt.Errorf("compatibility ledger operation %q regressed from %q to %q", previous.Method, previous.Status, currentItem.Status)
		}
	}
	return nil
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
	counts := cumulativeEvidenceCounts(ledger.Operations)
	total := len(ledger.Operations)
	implemented := 0
	unimplemented := 0
	for _, operation := range ledger.Operations {
		if operation.Status == "unimplemented" {
			unimplemented++
			continue
		}
		implemented++
	}
	fmt.Printf("operations=%d implemented=%d/%d verified-against-slack=%d/%d\n", total, implemented, total, counts["verified-against-slack"], total)
	fmt.Printf("unimplemented=%d\n", unimplemented)
	for _, status := range []string{"schema-compatible", "sdk-compatible", "behavior-compatible", "verified-against-slack"} {
		fmt.Printf("%s-or-better=%d/%d\n", status, counts[status], total)
	}
	return nil
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

func verifyHandlerRegistrations(implementedMethods, ledgerMethods map[string]struct{}) error {
	file, err := parser.ParseFile(token.NewFileSet(), "internal/api/slack/handler.go", nil, 0)
	if err != nil {
		return fmt.Errorf("parse Slack handler: %w", err)
	}
	registered := make(map[string]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		route, err := strconv.Unquote(literal.Value)
		if err != nil {
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
