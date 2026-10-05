package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
//   - Slack publishes every method's tier, counted per app per workspace:
//     Tier 1 "1+ per minute", Tier 2 "20+", Tier 3 "50+" and Tier 4 "100+".
//     Each ledger method is enforced at its published tier (methodTier):
//     the pinned official Java SDK's table where it lists the method
//     (publishedMethodTiers, tested against the vendored table), the tier
//     the method's reference page names where that table predates it
//     (referenceMethodTiers), and Tier 4's floor — the laxest documented
//     tier, which can never refuse a conforming client — for the methods
//     whose reference tier is not yet cited (uncitedTierMethods). A
//     method outside the ledger (the unknown-method catch-all) has the
//     Tier 4 floor too. The table's special tiers — auth.test and
//     chat.getPermalink "hundreds of requests per minute",
//     chat.postMessage "several hundred messages per minute" to the
//     workspace, and assistant.threads.setStatus "similar" to it — are
//     tierHundreds, the pinned SDK's 600 per minute. assistant.threads.setStatus's
//     per-conversation allowance is not enforced separately: laxer than
//     Slack, so it refuses no conforming client.
//   - chat.postMessage's special allowance IS documented method-level
//     behavior — one message per second per channel with short bursts
//     tolerated — and is enforced per credential and channel. The burst
//     capacity of five is a chosen constant, recorded, not a pinned value.
//   - A deployment with more than one web replica shares one budget: the
//     shared limiter (NewSharedRateLimiter) draws every call from the chat
//     module's store, which all replicas share, so N replicas admit what one
//     would. A single-replica deployment keeps the budget in process.
//
// Limits are keyed by the presented credential (hashed bearer token) so one
// app cannot starve another, and by client address when no bearer token is
// presented, so an unauthenticated flood is bounded too.
type RateLimiter struct {
	tokens RateTokens
	logger *slog.Logger
}

// RateTokens admits or refuses one call against a key's allowance. The chat
// service implements it over the store every replica shares; localRateTokens
// implements it in process.
type RateTokens interface {
	TakeRateToken(ctx context.Context, key string, allowance domain.RateAllowance) (time.Duration, bool, error)
}

// localRateTokens is one process's rate limits: each key's theoretical
// arrival time (domain.RateAllowance), with keys whose time has passed
// collected at most once per rateLimitSweepInterval.
type localRateTokens struct {
	mu        sync.Mutex
	arrivals  map[string]time.Time
	lastSweep time.Time
	// now is a test seam; production uses the wall clock.
	now func() time.Time
}

func (l *localRateTokens) TakeRateToken(_ context.Context, key string, allowance domain.RateAllowance) (time.Duration, bool, error) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= rateLimitSweepInterval {
		l.lastSweep = now
		for existing, arrival := range l.arrivals {
			if arrival.Before(now) {
				delete(l.arrivals, existing)
			}
		}
	}
	arrival, wait, admitted := allowance.Admit(l.arrivals[key], now)
	if admitted {
		l.arrivals[key] = arrival
	}
	return wait, admitted, nil
}

// rateTier is a documented Web API tier: its per-minute floor, and how many
// calls may arrive at once. Slack documents bursts for every tier without a
// number ("a small amount of burst behavior" for Tier 1); a full minute's
// budget is the burst for the other tiers, and Tier 1's is a chosen constant.
type rateTier struct {
	perMinute float64
	burst     float64
}

var (
	tier1 = rateTier{perMinute: 1, burst: tier1Burst}
	tier2 = rateTier{perMinute: 20, burst: 20}
	tier3 = rateTier{perMinute: 50, burst: 50}
	tier4 = rateTier{perMinute: methodBudgetPerMinute, burst: methodBudgetPerMinute}
	// tierHundreds is the special tier the pinned SDK names for auth.test,
	// chat.getPermalink, chat.postMessage and assistant.threads.setStatus:
	// "hundreds of requests per minute", which the SDK paces at 600.
	tierHundreds = rateTier{perMinute: specialBudgetPerMinute, burst: specialBudgetPerMinute}
)

// methodTier is the tier the method is enforced at.
func methodTier(method string) rateTier {
	if tier, ok := publishedMethodTiers[method]; ok {
		return tier
	}
	if tier, ok := referenceMethodTiers[method]; ok {
		return tier
	}
	return tier4
}

const (
	// methodBudgetPerMinute is Tier 4's documented floor, the budget of every
	// method no tier table names.
	methodBudgetPerMinute = 100
	// specialBudgetPerMinute is the special tiers' budget: the 600 a minute
	// the pinned Java SDK's MethodsRateLimitTier gives "hundreds of requests
	// per minute".
	specialBudgetPerMinute = 600
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

// NewRateLimiter keeps the budget in this process, which is exact for a
// deployment with one web replica.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{tokens: &localRateTokens{arrivals: make(map[string]time.Time), now: time.Now}}
}

// NewSharedRateLimiter draws every call from tokens, which every web replica
// shares. A call the shared store cannot decide is served rather than
// refused: the store being unreachable is the request's own failure to
// report, and refusing it as rate limited would misname that failure to the
// client and its retry handler.
func NewSharedRateLimiter(tokens RateTokens, logger *slog.Logger) *RateLimiter {
	if logger == nil {
		logger = slog.Default()
	}
	return &RateLimiter{tokens: tokens, logger: logger}
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
		if retryAfter, limited := l.take(r.Context(), "method\x00"+method+"\x00"+credential, tier.allowance()); limited {
			writeRateLimited(w, retryAfter)
			return
		}
		if method == "chat.postMessage" {
			if channel, ok := postedChannel(r); ok {
				if retryAfter, limited := l.take(r.Context(), "channel\x00"+channel+"\x00"+credential, postingAllowance); limited {
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
		if retryAfter, limited := h.Limiter.take(r.Context(), "webhook\x00"+domain.HashToken(r.URL.Path), postingAllowance); limited {
			w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
			writePlain(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
	}
	h.incomingWebhook(w, r)
}

// take draws one call from the named limit, reporting how long the caller
// must wait when it is refused.
//
// The limit is named by a hash of its key. The key joins its parts with NUL,
// which no part can contain, and carries the client address or a hashed
// credential; the hash keeps all of that out of the store while keeping keys
// distinct and bounded.
func (l *RateLimiter) take(ctx context.Context, key string, allowance domain.RateAllowance) (time.Duration, bool) {
	wait, admitted, err := l.tokens.TakeRateToken(ctx, domain.HashToken(key), allowance)
	if err != nil {
		if l.logger != nil {
			l.logger.WarnContext(ctx, "rate limit undecided; serving the call", "error", err)
		}
		return 0, false
	}
	return wait, !admitted
}

// allowance is the tier as a rate allowance: one call per minute divided by
// the per-minute floor, with the tier's burst.
func (t rateTier) allowance() domain.RateAllowance {
	return domain.RateAllowance{Interval: time.Duration(float64(time.Minute) / t.perMinute), Burst: int(t.burst)}
}

// postingAllowance is chat.postMessage's per-channel allowance and an
// incoming webhook's: one message a second with a short burst.
var postingAllowance = domain.RateAllowance{Interval: time.Second / postMessagePerSecond, Burst: postMessageBurst}

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
