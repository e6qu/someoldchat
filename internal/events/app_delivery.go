package events

import (
	"slices"
	"strings"
	"time"
)

// AppEventClaim is one journal record leased to a worker for delivery to one
// app on one surface, with that record's own delivery state.
//
// Delivery state is per record, not per app: a record the app failed to accept
// waits for its own retry while the records after it are delivered. A single
// per-app position used to hold the retry state, so one failing callback parked
// every later event for that app until its retries ran out.
type AppEventClaim struct {
	Record Record
	// Attempt counts the deliveries of this record the app was already sent
	// and did not accept. It is the X-Slack-Retry-Num of the next delivery;
	// zero means this is the first. Failures on this side of the delivery never
	// count (see AppEventRelease.Internal).
	Attempt int
	// RetryReason is Slack's reason for the last failed delivery, sent as
	// X-Slack-Retry-Reason with a retry.
	RetryReason string
	// Delivered lists the event_ids of this record's callbacks the app already
	// accepted. A record that fans out into several callbacks is retried only
	// for the ones that were not.
	Delivered []string
}

// AppEventRelease returns a claimed record for a later retry.
type AppEventRelease struct {
	// Reason explains the failure. For a delivery the app failed it is one of
	// Slack's retry reasons and is sent with the retry; an internal reason is
	// recorded for operators and never sent to the app.
	Reason string
	// RetryAt is when the record becomes claimable again.
	RetryAt time.Time
	// Internal marks a failure on this side of the delivery — the record could
	// not be projected, the app's configuration could not be read — rather
	// than a delivery the app failed. It does not count as an attempt.
	Internal bool
	// Delivered replaces the record's set of accepted callbacks (see
	// AppEventClaim.Delivered).
	Delivered []string
}

// Valid reports whether the release carries what every store requires.
func (r AppEventRelease) Valid() bool {
	return strings.TrimSpace(r.Reason) != "" && !r.RetryAt.IsZero()
}

// NormalizeDelivered returns the accepted-callback set in a canonical form —
// trimmed, without blanks or duplicates, sorted — so every store records and
// compares it the same way.
func NormalizeDelivered(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

// SlackRetryReasons are the X-Slack-Retry-Reason values Slack documents for
// the Events API. A reason outside this set is never sent to an app.
var SlackRetryReasons = map[string]bool{
	"http_timeout": true, "too_many_redirects": true, "connection_failed": true,
	"ssl_error": true, "http_error": true, "unknown_error": true,
}
