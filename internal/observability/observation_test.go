package observability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const observationTestToken = "sameoldchat-monitoring-token-00000000000000000000"

func observationTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMonitoringTokenDigestRejectsWeakTokens(t *testing.T) {
	for _, token := range []string{strings.Repeat("a", 31), strings.Repeat("a", 32) + " ", strings.Repeat("a", 32) + "\n"} {
		if _, err := MonitoringTokenDigest(token); err == nil {
			t.Fatalf("MonitoringTokenDigest(%q) succeeded, want error", token)
		}
	}
	if digest, err := MonitoringTokenDigest(""); err != nil || digest != nil {
		t.Fatalf("empty optional token = (%v, %v), want (nil, nil)", digest, err)
	}
}

func TestObservationHandlerAuthenticatesAndPublishesRealSnapshot(t *testing.T) {
	digest, err := MonitoringTokenDigest(observationTestToken)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.AddCounter("sameoldchat_requests_total", 4)
	registry.ObserveDuration("sameoldchat_request_duration", 1)
	handler := ObservationHandler(digest, registry, func(context.Context) error { return nil }, observationTestLogger())

	for _, authorization := range []string{"", "bearer " + observationTestToken, "Bearer wrong-monitoring-token-00000000000000000000"} {
		request := httptest.NewRequest(http.MethodGet, "/monitoring/observation", http.NoBody)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, response.Code)
		}
		if response.Header().Get("WWW-Authenticate") != `Bearer realm="sameoldchat-monitoring"` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unauthorized headers = %#v", response.Header())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/monitoring/observation", http.NoBody)
	request.Header.Set("Authorization", "Bearer "+observationTestToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["schema_version"] != applicationObservationSchema {
		t.Fatalf("schema_version = %v", document["schema_version"])
	}
	if _, present := document["cost_estimate"]; present {
		t.Fatal("application observation fabricated a cost estimate")
	}
	resource := document["resources"].([]any)[0].(map[string]any)
	if resource["health"] != "healthy" || resource["kind"] != "application" {
		t.Fatalf("resource = %#v", resource)
	}
	metrics := resource["metrics"].([]any)
	if len(metrics) != 5 || metrics[0].(map[string]any)["value"] != float64(4) {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestObservationReportsReadinessFailure(t *testing.T) {
	digest, err := MonitoringTokenDigest(observationTestToken)
	if err != nil {
		t.Fatal(err)
	}
	handler := ObservationHandler(digest, NewRegistry(), func(context.Context) error { return errors.New("store unavailable") }, observationTestLogger())
	request := httptest.NewRequest(http.MethodGet, "/monitoring/observation", http.NoBody)
	request.Header.Set("Authorization", "Bearer "+observationTestToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var document struct {
		Resources []struct {
			Health string `json:"health"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Resources[0].Health != "unhealthy" {
		t.Fatalf("health = %q", document.Resources[0].Health)
	}
}
