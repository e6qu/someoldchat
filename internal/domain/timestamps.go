package domain

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidStoredTimestamp reports a stored timestamp that cannot be decoded.
var ErrInvalidStoredTimestamp = errors.New("invalid stored timestamp")

// storedTimeLayout is the only textual timestamp encoding a repository may
// write. It differs from time.RFC3339Nano in one decisive way: the fraction is
// fixed width. time.RFC3339Nano strips trailing zeros from the fraction, which
// makes the encoding variable length, so byte order stops agreeing with time
// order as soon as one fraction is a prefix of another:
//
//	.120000 encodes as "…:00.12Z"     and
//	.123456 encodes as "…:00.123456Z"
//
// and "…:00.12Z" > "…:00.123456Z" because 'Z' (0x5A) > '3' (0x33) — the
// earlier instant compares greater. Every ordering key, keyset-pagination
// predicate and lease-fencing comparison in the SQL repositories is a
// comparison of that text, so a variable-width encoding silently reorders
// pages, skips rows, and lets an expired lease owner pass a `lease_until > now`
// guard.
//
// The fixed-width form pads the fraction to nine digits, so the byte order of
// two encodings equals the chronological order of the instants they denote for
// every representable year, and the value round-trips to the exact nanosecond.
const storedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// StoredTimeWidth is the number of bytes every non-empty stored timestamp
// occupies. The width is fixed by construction; a stored value of any other
// length was written by an encoding that is not order preserving.
const StoredTimeWidth = len("2006-01-02T15:04:05.000000000Z")

// StoredTime is the wire form of a timestamp held in a TEXT column. It exists
// so that a timestamp cannot reach a repository column through any encoding
// other than NewStoredTime: the repositories accept this type, and the only
// constructor produces the fixed-width, order-preserving form.
type StoredTime string

// NewStoredTime encodes an instant in the fixed-width, lexically ordered form.
// The zero time encodes as the empty string, which the repositories use as the
// "unset" marker for nullable timestamp columns such as outbox.lease_until.
func NewStoredTime(value time.Time) StoredTime {
	if value.IsZero() {
		return ""
	}
	return StoredTime(value.UTC().Format(storedTimeLayout))
}

// Time decodes a stored timestamp. It accepts both the fixed-width form and
// the variable-width time.RFC3339Nano form that databases written before the
// encoding was corrected still contain, so a read never fails on a row the
// migration has not rewritten yet.
func (value StoredTime) Time() (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, string(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w %q: %w", ErrInvalidStoredTimestamp, string(value), err)
	}
	return parsed.UTC(), nil
}

// Ordered reports whether the encoding is fixed width, and therefore whether
// byte comparisons of it agree with chronological comparisons.
func (value StoredTime) Ordered() bool {
	return value == "" || len(value) == StoredTimeWidth
}

// ParseStoredTime decodes a timestamp read back from a TEXT column.
func ParseStoredTime(value string) (time.Time, error) { return StoredTime(value).Time() }

// NormalizeStoredTime rewrites a stored timestamp into the fixed-width form.
// It is idempotent, so a migration may apply it to rows that are already
// correct, and it reports whether the value changed.
func NormalizeStoredTime(value string) (StoredTime, bool, error) {
	if value == "" {
		return "", false, nil
	}
	parsed, err := ParseStoredTime(value)
	if err != nil {
		return "", false, err
	}
	normalized := NewStoredTime(parsed)
	return normalized, string(normalized) != value, nil
}
