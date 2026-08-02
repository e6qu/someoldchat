package slack

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// RateLimiter enforces the Web API rate-limiting contract this transport
// never had: HTTP 429 with a Retry-After header and the pinned `rate_limited`
// error code. The mechanism is what official SDKs key on — python-slack-sdk's
// RateLimitErrorRetryHandler, node-slack-sdk's rateLimitedErrorRetryHandler
// and the Java SDK all read status 429 plus Retry-After — so without it their
// retry behavior was untestable against this product.
//
// Grounding and recorded boundaries (see the rate-limiting deviations in
// specs/compatibility.yaml):
//
//   - Slack's current reference defines per-method tiers, counted per app per
//     workspace, with Tier 4 documented as "100+ per minute". Every method
//     here gets a uniform budget at that most-permissive floor rather than a
//     per-method tier assignment: enforcing a laxer limit than real Slack can
//     never break a conforming client, while a hand-written 310-method tier
//     table would be guesswork the pinned material does not settle. The
//     per-method assignments therefore remain a recorded deviation.
//   - chat.postMessage's special allowance IS documented method-level
//     behavior — one message per second per channel with short bursts
//     tolerated — and is enforced per credential and channel. The burst
//     capacity of five is a chosen constant, recorded, not a pinned value.
//   - Budgets are replica-local. A deployment with N web replicas multiplies
//     the effective budget by up to N; the deviation records this.
//
// Buckets are keyed by the presented credential (hashed bearer token) so one
// app cannot starve another, and by client address when no bearer token is
// presented, so an unauthenticated flood is bounded too.
type RateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*rateBucket
	lastSweep time.Time
	// now is a test seam; production uses the wall clock.
	now func() time.Time
}

type rateBucket struct {
	tokens    float64
	capacity  float64
	perSecond float64
	last      time.Time
}

const (
	// methodBudgetPerMinute is Tier 4's documented floor, applied uniformly.
	methodBudgetPerMinute = 100
	// postMessagePerSecond and postMessageBurst enforce the documented
	// one-per-second-per-channel posting allowance with a short burst.
	postMessagePerSecond = 1
	postMessageBurst     = 5
	// rateLimitSweepInterval bounds how often idle buckets are collected.
	rateLimitSweepInterval = time.Minute
	// postedChannelBodyLimit bounds how much of a posting body is read to
	// learn its channel before the handler reads the same body again.
	postedChannelBodyLimit = 1 << 20
)

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{buckets: make(map[string]*rateBucket), now: time.Now}
}

// Middleware wraps the /api/ tree. It answers 429 before the wrapped handler
// runs, so a limited request costs no storage work.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		if method == "" || strings.Contains(method, "/") {
			// Unknown shapes are the catch-all's business, not the limiter's.
			next.ServeHTTP(w, r)
			return
		}
		credential := rateLimitCredential(r)
		if retryAfter, limited := l.take("method\x00"+method+"\x00"+credential, methodBudgetPerMinute, float64(methodBudgetPerMinute)/60); limited {
			writeRateLimited(w, retryAfter)
			return
		}
		if method == "chat.postMessage" {
			if channel, ok := postedChannel(r); ok {
				if retryAfter, limited := l.take("channel\x00"+channel+"\x00"+credential, postMessageBurst, postMessagePerSecond); limited {
					writeRateLimited(w, retryAfter)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// take draws one token from the named bucket, reporting how long the caller
// must wait when the bucket is dry.
func (l *RateLimiter) take(key string, capacity, perSecond float64) (time.Duration, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &rateBucket{tokens: capacity, capacity: capacity, perSecond: perSecond, last: now}
		l.buckets[key] = bucket
	}
	bucket.tokens = math.Min(bucket.capacity, bucket.tokens+now.Sub(bucket.last).Seconds()*bucket.perSecond)
	bucket.last = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return 0, false
	}
	wait := time.Duration((1 - bucket.tokens) / bucket.perSecond * float64(time.Second))
	return wait, true
}

// sweep drops buckets that have refilled completely: they hold no state a
// fresh bucket would not reproduce.
func (l *RateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < rateLimitSweepInterval {
		return
	}
	l.lastSweep = now
	for key, bucket := range l.buckets {
		if bucket.tokens+now.Sub(bucket.last).Seconds()*bucket.perSecond >= bucket.capacity {
			delete(l.buckets, key)
		}
	}
}

// rateLimitCredential buckets by the presented bearer credential — Slack
// counts per app per workspace, and the token is that identity — falling back
// to the client address so requests with no bearer token are bounded too.
// Form-carried tokens deliberately fall to the address bucket: reading the
// body here would tax every request to serve a legacy authentication shape.
func rateLimitCredential(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if token, ok := strings.CutPrefix(header, "Bearer "); ok && strings.TrimSpace(token) != "" {
		return domain.HashToken(strings.TrimSpace(token))
	}
	// r.RemoteAddr is the peer the listener accepted, the same identity the
	// access log records; a forwarded-for header is spoofable and is not
	// consulted for admission decisions.
	host := r.RemoteAddr
	if index := strings.LastIndex(host, ":"); index > 0 {
		host = host[:index]
	}
	return "addr\x00" + host
}

// postedChannel learns which channel a chat.postMessage addresses without
// consuming the body the handler still has to read: the read bytes are
// restored. A request whose channel cannot be determined is limited by the
// method bucket alone rather than refused — the handler owns argument errors.
func postedChannel(r *http.Request) (string, bool) {
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		return channel, true
	}
	if r.Body == nil {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, postedChannelBodyLimit))
	remainder := r.Body
	r.Body = readCloserWithRest(body, remainder)
	if err != nil {
		return "", false
	}
	contentType := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		var payload struct {
			Channel string `json:"channel"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return "", false
		}
		channel := strings.TrimSpace(payload.Channel)
		return channel, channel != ""
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", false
		}
		channel := strings.TrimSpace(values.Get("channel"))
		return channel, channel != ""
	}
	return "", false
}

func readCloserWithRest(read []byte, rest io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(read), rest), rest}
}

// writeRateLimited is the one Slack failure whose handling official SDKs key
// on at the HTTP layer: status 429 and Retry-After, with the pinned
// rate_limited code in the body for callers that read it there.
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate_limited"})
}
