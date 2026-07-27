package observability

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDurationFamilyKeepsAnExplicitSecondsSuffix(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveDuration("sameoldchat_rpc_duration_seconds", time.Second)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	output := string(body)
	if strings.Contains(output, "seconds_seconds") {
		t.Fatalf("metrics output %q doubled the unit suffix", output)
	}
	if !strings.Contains(output, "sameoldchat_rpc_duration_seconds_sum 1.000000") {
		t.Fatalf("metrics output %q does not expose the summary sample", output)
	}
}

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
	// A summary family named X exposes X_sum and X_count, and a duration carries
	// its unit on the family name. The exposition used to declare the family as
	// "sameoldchat_wake_duration" and then emit no sample belonging to it, which
	// promtool rejects and Prometheus scrapes as an unrelated untyped series.
	for _, expected := range []string{
		"sameoldchat_requests_total 2",
		"sameoldchat_lifecycle_generation 7",
		"# TYPE sameoldchat_wake_duration_seconds summary",
		"sameoldchat_wake_duration_seconds_count 1",
		"sameoldchat_wake_duration_seconds_sum 1.500000",
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
		// A name outside the Prometheus grammar makes the whole exposition body
		// unparseable, so the entire scrape fails rather than one series. Callers
		// build names by concatenation, so the rejection has to happen where the
		// name is built.
		"counter name with a label separator": func() { registry.AddCounter("sameoldchat_state{a}", 1) },
		"gauge name with a hyphen":            func() { registry.SetGauge("sameoldchat-state", 1) },
		"duration name with a space":          func() { registry.ObserveDuration("sameoldchat duration", time.Second) },
		"counter name starting with a digit":  func() { registry.AddCounter("1_sameoldchat", 1) },
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
