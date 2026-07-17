package main

import (
	"os"
	"testing"
)

func TestRegisteredHandlerMethodsRejectsDuplicateRoutes(t *testing.T) {
	path := t.TempDir() + "/handler.go"
	source := `package example
func register(mux interface{ HandleFunc(string, func()) }) {
	mux.HandleFunc("GET /api/example", func() {})
	mux.HandleFunc("GET /api/example", func() {})
}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registeredHandlerMethods(path); err == nil {
		t.Fatal("duplicate route registration was accepted")
	}
}

func TestRegisteredHandlerMethodsAcceptsDistinctMethods(t *testing.T) {
	path := t.TempDir() + "/handler.go"
	source := `package example
func register(mux interface{ HandleFunc(string, func()) }) {
	mux.HandleFunc("GET /api/example", func() {})
	mux.HandleFunc("POST /api/example", func() {})
}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	methods, err := registeredHandlerMethods(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := methods["example"]; !ok {
		t.Fatal("distinct methods did not register the API operation")
	}
}

func TestVerifyQualificationReferencesRejectsUnreferencedOperation(t *testing.T) {
	if err := verifyQualificationReferences([]operation{{Method: "operation.not_in_suite", Status: "behavior-compatible"}}); err == nil {
		t.Fatal("verifyQualificationReferences accepted an unreferenced operation")
	}
}
