package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// RateAllowance is a rate limit stated the way Slack states its own: a
// sustained rate, as the interval between two calls, and how many calls may
// arrive at once before that rate governs.
//
// It is enforced as the generic cell rate algorithm, which admits exactly
// what a token bucket of Burst tokens refilled every Interval admits, but
// keeps one instant per key — the theoretical arrival time of the next call —
// instead of a token count and a refill time. One instant can be advanced by
// a single conditional write, which is what lets every replica of a
// deployment share one budget through the store without a lock.
type RateAllowance struct {
	Interval time.Duration
	Burst    int
}

// Valid reports whether the allowance can be enforced: it admits at least
// one call and refills.
func (a RateAllowance) Valid() bool {
	return a.Interval > 0 && a.Burst >= 1
}

// Tolerance is how far ahead of now the theoretical arrival time may run
// while a call is still admitted: the burst beyond the first call.
func (a RateAllowance) Tolerance() time.Duration {
	return time.Duration(a.Burst-1) * a.Interval
}

// Admit decides one call arriving at now against the key's theoretical
// arrival time arrival, which is the zero time for a key never seen. An
// admitted call reports the arrival time to store; a refused one reports how
// long the caller must wait and leaves the arrival time unchanged.
func (a RateAllowance) Admit(arrival, now time.Time) (time.Time, time.Duration, bool) {
	start := arrival
	if start.Before(now) {
		start = now
	}
	if wait := start.Sub(now) - a.Tolerance(); wait > 0 {
		return arrival, wait, false
	}
	return start.Add(a.Interval), 0, true
}

// MaxRateLimitKeyBytes bounds a rate-limit key. Callers key a limit by a hash
// of what it limits, which fits with room to spare.
const MaxRateLimitKeyBytes = 128

// ValidRateLimitKey reports whether every store can hold the key: present,
// bounded, and text every profile stores alike. PostgreSQL's text type
// refuses a NUL byte that SQLite and memory accept, so a key carrying one
// would limit on two profiles and fail on the third.
func ValidRateLimitKey(key string) bool {
	return key != "" && len(key) <= MaxRateLimitKeyBytes && utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}
