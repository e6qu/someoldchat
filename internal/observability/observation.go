package observability

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const applicationObservationSchema = "e6qu.monitoring/v2"

// TokenDigest is the only monitoring credential material retained after
// startup validation.
type TokenDigest [sha256.Size]byte

// MonitoringTokenDigest validates an optional deployment token. Nil means the
// deployment did not opt into the observation endpoint, which then refuses all
// callers rather than falling back to another credential.
func MonitoringTokenDigest(token string) (*TokenDigest, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) < 32 || strings.IndexFunc(token, func(character rune) bool {
		return character <= ' ' || character == '\u007f'
	}) >= 0 {
		return nil, errors.New("SAMEOLDCHAT_MONITORING_TOKEN must contain at least 32 non-whitespace characters")
	}
	digest := TokenDigest(sha256.Sum256([]byte(token)))
	return &digest, nil
}

type applicationObservation struct {
	SchemaVersion string                `json:"schema_version"`
	ObservedAt    time.Time             `json:"observed_at"`
	Resources     []applicationResource `json:"resources"`
}

type applicationResource struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Kind    string              `json:"kind"`
	Health  string              `json:"health"`
	Metrics []applicationMetric `json:"metrics"`
}

type applicationMetric struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Status string  `json:"status"`
}

func observationMetric(name, label string, value float64, unit string) applicationMetric {
	return applicationMetric{Name: name, Label: label, Value: value, Unit: unit, Status: "available"}
}

// ObservationHandler publishes deployment-neutral, fixed-cardinality metrics.
// ready must probe the same configured store/service seam as /readyz.
func ObservationHandler(token *TokenDigest, registry *Registry, ready func(context.Context) error, logger *slog.Logger) http.Handler {
	registry.require()
	if logger == nil {
		panic("observation logger is required")
	}
	startedAt := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !observationAuthorized(r.Header.Get("Authorization"), token) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("WWW-Authenticate", `Bearer realm="sameoldchat-monitoring"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		health := "healthy"
		checkContext, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		if ready == nil || ready(checkContext) != nil {
			health = "unhealthy"
		}
		cancel()

		snapshot := registry.Snapshot()
		var operations uint64
		for _, value := range snapshot.Counters {
			operations += value
		}
		var durationObservations uint64
		for _, value := range snapshot.Durations {
			durationObservations += value.Count
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		observation := applicationObservation{
			SchemaVersion: applicationObservationSchema,
			ObservedAt:    time.Now().UTC(),
			Resources: []applicationResource{{
				ID: "sameoldchat-process", Name: "SameOldChat", Kind: "application", Health: health,
				Metrics: []applicationMetric{
					observationMetric("operations.total", "Recorded operations", float64(operations), "operations"),
					observationMetric("durations.observed", "Recorded duration observations", float64(durationObservations), "observations"),
					observationMetric("process.goroutines", "Process goroutines", float64(runtime.NumGoroutine()), "goroutines"),
					observationMetric("process.heap", "Allocated heap", float64(memory.HeapAlloc)/(1024*1024), "MiB"),
					observationMetric("process.uptime", "Process uptime", time.Since(startedAt).Seconds(), "seconds"),
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(observation); err != nil {
			logger.Error("write monitoring observation", "error", err)
		}
	})
}

func observationAuthorized(header string, expected *TokenDigest) bool {
	if expected == nil || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	actual := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}
