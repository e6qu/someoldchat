package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

// Trace is a W3C Trace Context identity: the trace a request belongs to and the
// span that is currently handling it.
//
// It exists so a request that crosses a process boundary stays correlatable. The
// chat seam carries no context values — identity, tenant and actor are explicit
// request fields on every RPC, which is the right design — and it carried no
// correlation identifier either, so a request logged by the HTTP process and the
// work it caused in the chat process could not be tied together. Trace is
// deliberately the wire format and nothing more: it holds no attributes, so no
// tenant or user data can be attached to it, and it does not depend on a tracing
// SDK, so it costs nothing to propagate.
type Trace struct {
	TraceID string
	SpanID  string
	Sampled bool
}

// ErrInvalidTrace reports a traceparent header that does not carry a usable
// identity.
var ErrInvalidTrace = errors.New("invalid trace context")

type traceContextKey struct{}

// NewTrace starts a new trace with a fresh root span.
func NewTrace() (Trace, error) {
	traceID, err := randomHex(16)
	if err != nil {
		return Trace{}, err
	}
	spanID, err := randomHex(8)
	if err != nil {
		return Trace{}, err
	}
	return Trace{TraceID: traceID, SpanID: spanID, Sampled: true}, nil
}

// Child continues the same trace in a new span, which is what a caller records
// for the outbound half of a cross-process call.
func (t Trace) Child() (Trace, error) {
	if !t.Valid() {
		return Trace{}, ErrInvalidTrace
	}
	spanID, err := randomHex(8)
	if err != nil {
		return Trace{}, err
	}
	return Trace{TraceID: t.TraceID, SpanID: spanID, Sampled: t.Sampled}, nil
}

// Valid reports whether the identity can be encoded as a traceparent. The
// all-zero identifiers are invalid per the specification and are rejected so a
// zero Trace cannot be mistaken for a real one.
func (t Trace) Valid() bool {
	return validTraceIdentifier(t.TraceID, 32) && validTraceIdentifier(t.SpanID, 16)
}

// TraceParent encodes the identity as a W3C traceparent header value.
func (t Trace) TraceParent() string {
	if !t.Valid() {
		return ""
	}
	flags := "00"
	if t.Sampled {
		flags = "01"
	}
	return "00-" + t.TraceID + "-" + t.SpanID + "-" + flags
}

// ParseTraceParent reads a W3C traceparent header value. An unparsable value is
// rejected rather than repaired: a caller that cannot be correlated must start a
// new trace instead of joining a fabricated one.
func ParseTraceParent(value string) (Trace, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[3]) != 2 {
		return Trace{}, ErrInvalidTrace
	}
	result := Trace{TraceID: strings.ToLower(parts[1]), SpanID: strings.ToLower(parts[2])}
	if !result.Valid() {
		return Trace{}, ErrInvalidTrace
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil {
		return Trace{}, ErrInvalidTrace
	}
	result.Sampled = flags[0]&0x01 != 0
	return result, nil
}

// ContextWithTrace attaches an identity to a context.
func ContextWithTrace(ctx context.Context, value Trace) context.Context {
	if ctx == nil || !value.Valid() {
		return ctx
	}
	return context.WithValue(ctx, traceContextKey{}, value)
}

// TraceFromContext reports the identity attached to a context.
func TraceFromContext(ctx context.Context) (Trace, bool) {
	if ctx == nil {
		return Trace{}, false
	}
	value, ok := ctx.Value(traceContextKey{}).(Trace)
	if !ok || !value.Valid() {
		return Trace{}, false
	}
	return value, true
}

func validTraceIdentifier(value string, length int) bool {
	if len(value) != length {
		return false
	}
	zero := true
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		default:
			return false
		}
		if character != '0' {
			zero = false
		}
	}
	return !zero
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
