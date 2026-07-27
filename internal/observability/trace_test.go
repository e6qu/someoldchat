package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTraceRoundTripsThroughATraceParentHeader(t *testing.T) {
	trace, err := NewTrace()
	if err != nil {
		t.Fatal(err)
	}
	if !trace.Valid() {
		t.Fatalf("new trace %+v is not valid", trace)
	}
	header := trace.TraceParent()
	if !strings.HasPrefix(header, "00-") || len(header) != 55 {
		t.Fatalf("traceparent %q is not the W3C format", header)
	}
	parsed, err := ParseTraceParent(header)
	if err != nil {
		t.Fatalf("parse %q: %v", header, err)
	}
	if parsed != trace {
		t.Fatalf("parsed %+v, want %+v", parsed, trace)
	}
}

func TestChildKeepsTheTraceAndChangesTheSpan(t *testing.T) {
	parent, err := NewTrace()
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.Child()
	if err != nil {
		t.Fatal(err)
	}
	if child.TraceID != parent.TraceID {
		t.Fatalf("child trace %q, want %q", child.TraceID, parent.TraceID)
	}
	if child.SpanID == parent.SpanID {
		t.Fatal("child reused the parent span identifier")
	}
	if child.Sampled != parent.Sampled {
		t.Fatal("child changed the sampling decision")
	}
	if _, err := (Trace{}).Child(); !errors.Is(err, ErrInvalidTrace) {
		t.Fatal("the zero trace must not produce a child")
	}
}

// A traceparent that cannot be parsed must be rejected rather than repaired: a
// request that cannot be correlated has to start a new trace instead of joining a
// fabricated one.
func TestParseTraceParentRejectsUnusableValues(t *testing.T) {
	for name, value := range map[string]string{
		"empty":             "",
		"wrong version":     "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"missing flags":     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
		"short trace":       "00-4bf92f3577b34da6-00f067aa0ba902b7-01",
		"short span":        "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa-01",
		"zero trace":        "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"zero span":         "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"non hexadecimal":   "00-4bf92f3577b34da6a3ce929d0e0e473z-00f067aa0ba902b7-01",
		"unparsable flags":  "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-zz",
		"empty trace field": "00--00f067aa0ba902b7-01",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTraceParent(value); !errors.Is(err, ErrInvalidTrace) {
				t.Fatalf("ParseTraceParent(%q) error = %v, want ErrInvalidTrace", value, err)
			}
		})
	}
}

func TestSamplingFlagSurvivesTheHeader(t *testing.T) {
	unsampled := Trace{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7"}
	parsed, err := ParseTraceParent(unsampled.TraceParent())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Sampled {
		t.Fatal("an unsampled trace came back sampled")
	}
}

func TestContextCarriesOnlyAValidTrace(t *testing.T) {
	trace, err := NewTrace()
	if err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithTrace(context.Background(), trace)
	loaded, ok := TraceFromContext(ctx)
	if !ok || loaded != trace {
		t.Fatalf("TraceFromContext = %+v, %t", loaded, ok)
	}
	if _, ok := TraceFromContext(ContextWithTrace(context.Background(), Trace{})); ok {
		t.Fatal("the zero trace must not be attached to a context")
	}
	if _, ok := TraceFromContext(context.Background()); ok {
		t.Fatal("a bare context reported a trace")
	}
}

func TestTraceParentOfAnInvalidTraceIsEmpty(t *testing.T) {
	if header := (Trace{}).TraceParent(); header != "" {
		t.Fatalf("TraceParent() = %q, want the empty string so no caller sends a fabricated identity", header)
	}
}
