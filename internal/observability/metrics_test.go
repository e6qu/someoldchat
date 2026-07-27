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

// TestACounterAccumulatesAndAGaugeDoesNot is the difference between the two
// kinds, and nothing asserted it: every existing test incremented a counter once,
// so replacing `r.counters[name] += value` with `= value` — turning every counter
// into a last-write-wins gauge — left the whole package green. A burst of
// failures would then report 1, and rate() over any *_total series would be flat
// through an incident.
func TestACounterAccumulatesAndAGaugeDoesNot(t *testing.T) {
	registry := NewRegistry()
	registry.AddCounter("sameoldchat_failures_total", 2)
	registry.AddCounter("sameoldchat_failures_total", 3)
	registry.AddCounter("sameoldchat_failures_total", 1)
	registry.SetGauge("sameoldchat_generation", 7)
	registry.SetGauge("sameoldchat_generation", 9)

	snapshot := registry.Snapshot()
	if got := snapshot.Counters["sameoldchat_failures_total"]; got != 6 {
		t.Errorf("counter = %d after adding 2, 3 and 1, want 6: a counter accumulates", got)
	}
	if got := snapshot.Gauges["sameoldchat_generation"]; got != 9 {
		t.Errorf("gauge = %d, want the last value 9", got)
	}

	// The exposition must carry the accumulated value, not the last increment.
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "sameoldchat_failures_total 6") {
		t.Errorf("metrics output %q does not expose the accumulated counter", string(body))
	}
}

// TestADurationSummaryAccumulatesEveryObservation is the same property for the
// summary: a count that stayed at 1 or a sum that was overwritten would make the
// duration series describe the last request rather than the traffic.
func TestADurationSummaryAccumulatesEveryObservation(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveDuration("sameoldchat_rpc_duration_seconds", 250*time.Millisecond)
	registry.ObserveDuration("sameoldchat_rpc_duration_seconds", 750*time.Millisecond)
	registry.ObserveDuration("sameoldchat_rpc_duration_seconds", time.Second)

	summary := registry.Snapshot().Durations["sameoldchat_rpc_duration_seconds"]
	if summary.Count != 3 {
		t.Errorf("count = %d after three observations, want 3", summary.Count)
	}
	if summary.SumSeconds != 2 {
		t.Errorf("sum = %f seconds, want 2 (0.25 + 0.75 + 1)", summary.SumSeconds)
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
