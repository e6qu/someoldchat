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
// never had: HTTP 429 with a Retry-After header and Slack's error code. The
// mechanism is what official SDKs key on — python-slack-sdk's
// RateLimitErrorRetryHandler, node-slack-sdk's rateLimitedErrorRetryHandler
// and the Java SDK all read status 429 plus Retry-After — so without it their
// retry behavior was untestable against this product.
//
// The code in the body is two different Slack codes, and clients read it:
//
//   - A method's tier budget answers `ratelimited` (writeRateLimited). That is
//     the code python-slack-sdk 3.45.0 compares against before it waits out
//     Retry-After and retries apps.connections.open (SocketModeClient) and
//     rtm.connect (rtm_v2.RTMClient); any other code is raised as a failure,
//     so answering `rate_limited` here turned a transient limit into a crashed
//     Socket Mode app.
//   - chat.postMessage's per-channel posting allowance answers `rate_limited`
//     (writePostingLimited), the posting-limit error the pinned OpenAPI
//     document declares for chat.postMessage, chat.meMessage,
//     chat.scheduleMessage and chat.update ("Application has posted too many
//     messages").
//
// Grounding and recorded boundaries (see the rate-limiting deviations in
// specs/compatibility.yaml):
//
//   - Slack's current reference defines per-method tiers, counted per app per
//     workspace: Tier 1 "1+ per minute", Tier 2 "20+", Tier 3 "50+" and
//     Tier 4 "100+". A method whose stricter tier its reference page names is
//     enforced at that tier (methodTiers); every other method gets the
//     uniform budget at Tier 4's floor. Enforcing a laxer limit than real
//     Slack can never break a conforming client, while a tier written from
//     memory could refuse one, so the table holds only cited tiers and the
//     rest remain a recorded deviation.
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

// rateTier is a documented Web API tier: its per-minute floor, and how many
// calls may arrive at once. Slack documents bursts for every tier without a
// number ("a small amount of burst behavior" for Tier 1); a full minute's
// budget is the burst for Tiers 2 to 4, and Tier 1's is a chosen constant.
type rateTier struct {
	perMinute float64
	burst     float64
}

var (
	tier1 = rateTier{perMinute: 1, burst: tier1Burst}
	tier2 = rateTier{perMinute: 20, burst: 20}
	tier3 = rateTier{perMinute: 50, burst: 50}
	tier4 = rateTier{perMinute: methodBudgetPerMinute, burst: methodBudgetPerMinute}
)

// methodTiers is each method enforced below Tier 4, at the tier its Slack
// reference page names. A method absent here is held to Tier 4.
var methodTiers = map[string]rateTier{
	"admin.apps.mcp.servers.list":             tier3,
	"admin.apps.mcp.servers.permissions.list": tier3,
	"admin.apps.mcp.servers.permissions.set":  tier3,
	"admin.apps.permissions.add":              tier2,
	"admin.apps.permissions.list":             tier3,
	"admin.apps.permissions.remove":           tier2,
	"admin.apps.permissions.set":              tier2,
	"admin.usergroups.addUsers":               tier2,
	"admin.usergroups.create":                 tier1,
	"admin.usergroups.fetch":                  tier1,
	"admin.usergroups.removeTeams":            tier2,
	"admin.usergroups.removeUsers":            tier2,
	"admin.usergroups.update":                 tier1,
	"admin.usergroups.uploadUsers":            tier2,
	"agents.conversations.archive":            tier2,
	"agents.conversations.create":             tier2,
	"agents.conversations.listViews":          tier3,
	"agents.conversations.removeView":         tier3,
	"agents.conversations.setProperties":      tier3,
	"agents.conversations.setView":            tier3,
	"agents.sessions.rename":                  tier3,
	"agents.sessions.setStatus":               tier3,
	"apps.managed.permissions.set":            tier2,
	"auth.teams.list":                         tier2,
	"chat.scheduleMessage":                    tier3,
	"chat.scheduledMessages.list":             tier3,
	"functions.workflows.steps.list":          tier3,
	"team.preferences.list":                   tier3,
	"workflows.featured.set":                  tier3,
}

// methodTier is the tier the method is enforced at.
func methodTier(method string) rateTier {
	if tier, ok := methodTiers[method]; ok {
		return tier
	}
	return tier4
}

const (
	// methodBudgetPerMinute is Tier 4's documented floor, the budget of every
	// method methodTiers does not name.
	methodBudgetPerMinute = 100
	// tier1Burst is how many Tier 1 calls may arrive at once before the
	// one-per-minute refill governs.
	tier1Burst = 3
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
		tier := methodTier(method)
		if retryAfter, limited := l.take("method\x00"+method+"\x00"+credential, tier.burst, tier.perMinute/60); limited {
			writeRateLimited(w, retryAfter)
			return
		}
		if method == "chat.postMessage" {
			if channel, ok := postedChannel(r); ok {
				if retryAfter, limited := l.take("channel\x00"+channel+"\x00"+credential, postMessageBurst, postMessagePerSecond); limited {
					writePostingLimited(w, retryAfter)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// limitedIncomingWebhook applies Slack's documented incoming-webhook allowance
// — one message per second per webhook, short bursts tolerated — before the
// webhook handler runs. The webhook URL is the webhook's identity, so the
// bucket is keyed by a hash of the path: the path carries the secret, and the
// limiter must not hold it in plain text. Incoming webhooks answer in plain
// text, so a limited delivery answers 429 with Retry-After and the plain-text
// body `rate_limited`, which is what the official WebhookClient retry handlers
// key on. A Handler without a limiter serves unlimited, like the Web API.
func (h Handler) limitedIncomingWebhook(w http.ResponseWriter, r *http.Request) {
	if h.Limiter != nil {
		if retryAfter, limited := h.Limiter.take("webhook\x00"+domain.HashToken(r.URL.Path), postMessageBurst, postMessagePerSecond); limited {
			w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
			writePlain(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
	}
	h.incomingWebhook(w, r)
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
	if token := headerToken(r); token != "" {
		return domain.HashToken(token)
	}
	// r.RemoteAddr is the client the access log records too: the peer, or
	// the client a trusted proxy forwarded (clientaddr.Resolver.Middleware).
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

// writeRateLimited answers a method budget: status 429 and Retry-After, which
// official SDKs key on at the HTTP layer, and Slack's ratelimited in the body
// for the clients that read it there (see RateLimiter).
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "ratelimited"})
}

// writePostingLimited answers chat.postMessage's per-channel posting allowance
// with the posting-limit code the pinned document declares for it.
func writePostingLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate_limited"})
}

// retryAfterSeconds renders a wait as the whole, positive number of seconds
// Retry-After carries.
func retryAfterSeconds(retryAfter time.Duration) string {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
