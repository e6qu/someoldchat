package observability

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistryPublishesBoundedAggregateMetrics(t *testing.T) {
	registry := NewRegistry()
	registry.AddCounter("sameoldchat_requests_total", 2)
	registry.SetGauge("sameoldchat_lifecycle_generation", 7)
	registry.ObserveDuration("sameoldchat_wake_duration", 1500*time.Millisecond)

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	output := string(body)
	for _, expected := range []string{
		"sameoldchat_requests_total 2",
		"sameoldchat_lifecycle_generation 7",
		"sameoldchat_wake_duration_count 1",
		"sameoldchat_wake_duration_sum_seconds 1.500000",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("metrics output %q does not contain %q", output, expected)
		}
	}
}

func TestRegistryRejectsInvalidMeasurements(t *testing.T) {
	registry := NewRegistry()
	for name, operation := range map[string]func(){
		"empty counter name": func() { registry.AddCounter("", 1) },
		"empty gauge name":   func() { registry.SetGauge("", 1) },
		"empty duration name": func() {
			registry.ObserveDuration("", time.Second)
		},
		"negative duration": func() {
			registry.ObserveDuration("sameoldchat_duration", -time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected invalid measurement to fail loudly")
				}
			}()
			operation()
		})
	}
}
