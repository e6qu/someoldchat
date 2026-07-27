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
// two encodings equals the chronological order of the instants they denote, and
// the value round-trips to the exact nanosecond.
//
// That claim holds only while the year field is itself fixed width, which is
// true for years 1 through 9999 and false outside them: Go renders year 10000
// as five digits and a negative year with a leading '-', so the encoding grows
// to 31 bytes and byte order stops agreeing with time order at offset 0.
// Measured, "10000-01-01T00:00:00.000000000Z" sorts BEFORE
// "1969-…" ('0' < '9' at offset 1) and "-4000-…" sorts AFTER "-0001-…".
// StoredTimeMinYear and StoredTimeMaxYear are therefore part of the contract,
// and NewStoredTime enforces them rather than emitting a value that silently
// un-orders the column it lands in.
const storedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// StoredTimeWidth is the number of bytes every non-empty stored timestamp
// occupies. The width is fixed by construction; a stored value of any other
// length was written by an encoding that is not order preserving.
const StoredTimeWidth = len("2006-01-02T15:04:05.000000000Z")

// The representable range. Outside it the four-digit year field is not fixed
// width, so the encoding is not order preserving. Every writer in this
// repository derives its instants from time.Now plus a bounded duration, so the
// range is not a restriction any caller can notice; it is the range in which
// the ordering guarantee is true.
const (
	StoredTimeMinYear = 1
	StoredTimeMaxYear = 9999
)

var (
	storedTimeFloor   = time.Date(StoredTimeMinYear, time.January, 1, 0, 0, 0, 0, time.UTC)
	storedTimeCeiling = time.Date(StoredTimeMaxYear, time.December, 31, 23, 59, 59, 999999999, time.UTC)
)

// StoredTime is the wire form of a timestamp held in a TEXT column. It exists
// so that a timestamp cannot reach a repository column through any encoding
// other than NewStoredTime: the repositories accept this type, and the only
// constructor produces the fixed-width, order-preserving form.
type StoredTime string

// NewStoredTime encodes an instant in the fixed-width, lexically ordered form.
// The zero time encodes as the empty string, which the repositories use as the
// "unset" marker for nullable timestamp columns such as outbox.lease_until.
//
// An instant outside [StoredTimeMinYear, StoredTimeMaxYear] saturates at the
// nearer bound. It cannot be encoded faithfully and stay order preserving, and
// the two alternatives are worse: returning the empty marker would turn an
// out-of-range lease deadline into "no lease", and returning an error would put
// a failure branch on several hundred call sites for an instant no writer can
// produce. Saturation keeps the type's single invariant — every value this
// constructor returns satisfies Ordered — unconditionally true.
func NewStoredTime(value time.Time) StoredTime {
	if value.IsZero() {
		return ""
	}
	value = value.UTC()
	if value.Before(storedTimeFloor) {
		value = storedTimeFloor
	} else if value.After(storedTimeCeiling) {
		value = storedTimeCeiling
	}
	return StoredTime(value.Format(storedTimeLayout))
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

// Ordered reports whether byte comparisons of this encoding agree with
// chronological comparisons.
//
// The width check alone is not sufficient and used to be the whole test: a
// legacy value with a numeric UTC offset — "2026-07-27T10:00:00.1234+01:00" —
// is also exactly StoredTimeWidth bytes long, and it is not comparable with the
// 'Z' values around it at all. Requiring the trailing 'Z' as well pins both
// halves of the encoding: fixed width AND a single zone.
func (value StoredTime) Ordered() bool {
	return value == "" || (len(value) == StoredTimeWidth && value[StoredTimeWidth-1] == 'Z')
}

// ParseStoredTime decodes a timestamp read back from a TEXT column.
func ParseStoredTime(value string) (time.Time, error) { return StoredTime(value).Time() }
