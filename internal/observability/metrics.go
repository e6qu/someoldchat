package observability

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry stores process-local aggregate measurements. It intentionally has
// no label support: lifecycle and request metrics must remain bounded and must
// not encode tenant data.
type Registry struct {
	mu        sync.RWMutex
	counters  map[string]uint64
	gauges    map[string]int64
	durations map[string]durationSummary
}

type durationSummary struct {
	count uint64
	sum   time.Duration
}

type Snapshot struct {
	Counters  map[string]uint64
	Gauges    map[string]int64
	Durations map[string]DurationSummary
}

type DurationSummary struct {
	Count      uint64
	SumSeconds float64
}

func NewRegistry() *Registry {
	return &Registry{
		counters:  make(map[string]uint64),
		gauges:    make(map[string]int64),
		durations: make(map[string]durationSummary),
	}
}

func (r *Registry) AddCounter(name string, value uint64) {
	r.require()
	requireName(name)
	r.mu.Lock()
	r.counters[name] += value
	r.mu.Unlock()
}

func (r *Registry) SetGauge(name string, value int64) {
	r.require()
	requireName(name)
	r.mu.Lock()
	r.gauges[name] = value
	r.mu.Unlock()
}

func (r *Registry) ObserveDuration(name string, value time.Duration) {
	r.require()
	requireName(name)
	if value < 0 {
		panic("observability duration cannot be negative")
	}
	r.mu.Lock()
	valueSet := r.durations[name]
	valueSet.count++
	valueSet.sum += value
	r.durations[name] = valueSet
	r.mu.Unlock()
}

func (r *Registry) Snapshot() Snapshot {
	r.require()
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := Snapshot{
		Counters:  make(map[string]uint64, len(r.counters)),
		Gauges:    make(map[string]int64, len(r.gauges)),
		Durations: make(map[string]DurationSummary, len(r.durations)),
	}
	for name, value := range r.counters {
		snapshot.Counters[name] = value
	}
	for name, value := range r.gauges {
		snapshot.Gauges[name] = value
	}
	for name, value := range r.durations {
		snapshot.Durations[name] = DurationSummary{Count: value.count, SumSeconds: value.sum.Seconds()}
	}
	return snapshot
}

// Handler publishes Prometheus-compatible aggregate metrics. It never emits
// request, workspace, user, or message identifiers.
func (r *Registry) Handler() http.Handler {
	r.require()
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		snapshot := r.Snapshot()
		for _, name := range sortedKeys(snapshot.Counters) {
			fmt.Fprintf(w, "# TYPE %s counter\n%s %d\n", name, name, snapshot.Counters[name])
		}
		for _, name := range sortedKeys(snapshot.Gauges) {
			fmt.Fprintf(w, "# TYPE %s gauge\n%s %d\n", name, name, snapshot.Gauges[name])
		}
		for _, name := range sortedKeys(snapshot.Durations) {
			value := snapshot.Durations[name]
			fmt.Fprintf(w, "# TYPE %s summary\n%s_count %d\n%s_sum_seconds %f\n", name, name, value.Count, name, value.SumSeconds)
		}
	})
}

func (r *Registry) require() {
	if r == nil {
		panic("observability registry is required")
	}
}

func requireName(name string) {
	if strings.TrimSpace(name) == "" {
		panic("observability metric name is required")
	}
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
