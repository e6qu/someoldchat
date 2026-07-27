package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// The registration gate is the only thing tying a live Slack route to the
// compatibility ledger. It used to read one named file and match one method
// name, so three ways of registering a route were invisible to it: a route
// declared in a second file of the same package, a route registered with
// Handle rather than HandleFunc, and a route whose path is not a bare literal.
// Each let an undeclared API method ship with the gate green. These are the
// injections that reproduced that, kept so the gate cannot narrow again.

const handlerPreamble = `package slack

import "net/http"

type Handler struct{}

func (h *Handler) apiTest(http.ResponseWriter, *http.Request) {}
`

func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return directory
}

func ledgerMethodSet(methods ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		set[method] = struct{}{}
	}
	return set
}

func TestVerifyHandlerRegistrationsAcceptsADeclaredRoute(t *testing.T) {
	directory := writePackage(t, map[string]string{
		"handler.go": handlerPreamble + `
func (h *Handler) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat.postMessage", h.apiTest)
}
`,
	})
	declared := ledgerMethodSet("chat.postMessage")
	if err := verifyHandlerRegistrations(directory, declared, declared); err != nil {
		t.Fatalf("declared route rejected: %v", err)
	}
}

func TestVerifyHandlerRegistrationsFindsARouteInASecondFile(t *testing.T) {
	directory := writePackage(t, map[string]string{
		"handler.go": handlerPreamble + `
func (h *Handler) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat.postMessage", h.apiTest)
}
`,
		"lists.go": `package slack

import "net/http"

func (h *Handler) listRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/totally.invented.method", h.apiTest)
}
`,
	})
	declared := ledgerMethodSet("chat.postMessage")
	err := verifyHandlerRegistrations(directory, declared, declared)
	if err == nil {
		t.Fatal("a route in a second file was accepted while absent from the ledger")
	}
	if !strings.Contains(err.Error(), "totally.invented.method") {
		t.Fatalf("error does not name the undeclared route: %v", err)
	}
}

func TestVerifyHandlerRegistrationsFindsHandleAsWellAsHandleFunc(t *testing.T) {
	directory := writePackage(t, map[string]string{
		"handler.go": handlerPreamble + `
func (h *Handler) routes(mux *http.ServeMux) {
	mux.Handle("POST /api/second.invented.method", http.HandlerFunc(h.apiTest))
}
`,
	})
	declared := ledgerMethodSet()
	err := verifyHandlerRegistrations(directory, declared, declared)
	if err == nil {
		t.Fatal("a route registered with Handle was accepted while absent from the ledger")
	}
	if !strings.Contains(err.Error(), "second.invented.method") {
		t.Fatalf("error does not name the undeclared route: %v", err)
	}
}

func TestVerifyHandlerRegistrationsRefusesARouteItCannotRead(t *testing.T) {
	directory := writePackage(t, map[string]string{
		"handler.go": handlerPreamble + `
const prefix = "POST /api/"

func (h *Handler) routes(mux *http.ServeMux) {
	mux.HandleFunc(prefix+"totally.invented.method", h.apiTest)
}
`,
	})
	declared := ledgerMethodSet()
	err := verifyHandlerRegistrations(directory, declared, declared)
	if err == nil {
		t.Fatal("a route path the gate cannot evaluate was skipped silently")
	}
	if !strings.Contains(err.Error(), "string literal") {
		t.Fatalf("error does not explain the requirement: %v", err)
	}
	if !strings.Contains(err.Error(), "handler.go:") {
		t.Fatalf("error does not locate the registration: %v", err)
	}
}

func TestVerifyHandlerRegistrationsFindsADeclaredMethodWithNoRoute(t *testing.T) {
	directory := writePackage(t, map[string]string{
		"handler.go": handlerPreamble + `
func (h *Handler) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat.postMessage", h.apiTest)
}
`,
	})
	declared := ledgerMethodSet("chat.postMessage", "chat.unrouted")
	err := verifyHandlerRegistrations(directory, declared, declared)
	if err == nil {
		t.Fatal("a ledger method with no registered route was accepted")
	}
	if !strings.Contains(err.Error(), "chat.unrouted") {
		t.Fatalf("error does not name the unrouted method: %v", err)
	}
}

func TestVerifyHandlerRegistrationsIgnoresWildcardAndNonAPIRoutes(t *testing.T) {
	directory := writePackage(t, map[string]string{
		"handler.go": handlerPreamble + `
func (h *Handler) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/files/{file}", h.apiTest)
	mux.HandleFunc("GET /healthz", h.apiTest)
}
`,
	})
	declared := ledgerMethodSet()
	if err := verifyHandlerRegistrations(directory, declared, declared); err != nil {
		t.Fatalf("a wildcard download route or a non-API route was held to the ledger: %v", err)
	}
}

func TestVerifyHandlerRegistrationsRefusesADirectoryWithNoSource(t *testing.T) {
	declared := ledgerMethodSet()
	if err := verifyHandlerRegistrations(t.TempDir(), declared, declared); err == nil {
		t.Fatal("an empty package directory was read as a package with no routes")
	}
}

// TestContractHolds runs the real gate, so the contract is covered by `go test`
// and not only by the shell target that invokes the binary.
func TestContractHolds(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	t.Chdir(root)
	if err := verify(); err != nil {
		t.Fatalf("compatibility contract does not hold: %v", err)
	}
}
