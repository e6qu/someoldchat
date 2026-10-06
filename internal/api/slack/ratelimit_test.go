package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func limiterAt(now *time.Time) *RateLimiter {
	limiter := NewRateLimiter()
	limiter.tokens.(*localRateTokens).now = func() time.Time { return *now }
	return limiter
}

func limitedRequest(t *testing.T, handler http.Handler, method, target, token, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The method budget answers exactly the way official SDK retry handlers key
// on: 429, a positive integer Retry-After, and Slack's ratelimited code (the one python-slack-sdk retries apps.connections.open and rtm.connect on) —
// and the budget is per credential and per method, so one caller cannot
// starve another and one hot method cannot silence the rest of the API.
func TestRateLimiterAnswers429WithRetryAfterPerCredentialAndMethod(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	limiter := limiterAt(&now)
	passed := 0
	wrapped := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed++ }))
	for i := 0; i < methodBudgetPerMinute; i++ {
		if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.info", "xoxb-one", "", ""); response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d", i, response.Code)
		}
	}
	limited := limitedRequest(t, wrapped, http.MethodPost, "/api/users.info", "xoxb-one", "", "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget status=%d, want %d", limited.Code, http.StatusTooManyRequests)
	}
	if !strings.Contains(limited.Body.String(), `"error":"ratelimited"`) || !strings.Contains(limited.Body.String(), `"ok":false`) {
		t.Fatalf("over-budget body=%s", limited.Body)
	}
	retryAfter, err := strconv.Atoi(limited.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After=%q, want a positive integer of seconds", limited.Header().Get("Retry-After"))
	}
	if passed != methodBudgetPerMinute {
		t.Fatalf("handler ran %d times, want %d — a limited request must cost no work", passed, methodBudgetPerMinute)
	}
	// Another credential and another method are separate budgets.
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.info", "xoxb-two", "", ""); response.Code != http.StatusOK {
		t.Fatalf("other credential status=%d", response.Code)
	}
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.profile.get", "xoxb-one", "", ""); response.Code != http.StatusOK {
		t.Fatalf("other method status=%d", response.Code)
	}
	// Waiting the advertised time restores service.
	now = now.Add(time.Duration(retryAfter) * time.Second)
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.info", "xoxb-one", "", ""); response.Code != http.StatusOK {
		t.Fatalf("after Retry-After status=%d, want %d", response.Code, http.StatusOK)
	}
}

// chat.postMessage carries Slack's documented special allowance: one message
// per second per channel with a short burst. The channel is read without
// consuming the body the handler still needs, sustained one-per-second
// posting never trips it, and a different channel is a different budget.
func TestRateLimiterEnforcesThePerChannelPostingAllowance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	limiter := limiterAt(&now)
	var seenBody string
	wrapped := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 256)
		n, _ := r.Body.Read(raw)
		seenBody = string(raw[:n])
	}))
	form := "channel=C1&text=hello"
	for i := 0; i < postMessageBurst; i++ {
		if response := limitedRequest(t, wrapped, http.MethodPost, "/api/chat.postMessage", "xoxb-one", form, "application/x-www-form-urlencoded"); response.Code != http.StatusOK {
			t.Fatalf("burst request %d status=%d", i, response.Code)
		}
	}
	if seenBody != form {
		t.Fatalf("the limiter consumed the body: handler saw %q, want %q", seenBody, form)
	}
	limited := limitedRequest(t, wrapped, http.MethodPost, "/api/chat.postMessage", "xoxb-one", form, "application/x-www-form-urlencoded")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("burst overflow status=%d Retry-After=%q", limited.Code, limited.Header().Get("Retry-After"))
	}
	// The posting limit is the pinned chat.postMessage rate_limited, not the
	// method budget's ratelimited.
	if !strings.Contains(limited.Body.String(), `"error":"rate_limited"`) {
		t.Fatalf("burst overflow body=%s", limited.Body)
	}
	// Another channel posts freely; JSON bodies are understood too.
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/chat.postMessage", "xoxb-one", `{"channel":"C2","text":"hello"}`, "application/json"); response.Code != http.StatusOK {
		t.Fatalf("other channel status=%d", response.Code)
	}
	// Sustained posting at the documented rate is never limited.
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		if response := limitedRequest(t, wrapped, http.MethodPost, "/api/chat.postMessage", "xoxb-one", form, "application/x-www-form-urlencoded"); response.Code != http.StatusOK {
			t.Fatalf("sustained post %d status=%d", i, response.Code)
		}
	}
}

// Register mounts the limiter in front of every /api/ route, including the
// unknown-method catch-all, and the rest of the handler keeps working through
// the wrapper.
func TestRegisterMountsTheLimiterOverTheWholeAPITree(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	// api.test and the unknown-method catch-all touch no service state, so a
	// zero Handler is enough to prove the mounting.
	handler := Handler{Limiter: limiterAt(&now)}
	mux := http.NewServeMux()
	handler.Register(mux)
	if response := limitedRequest(t, mux, http.MethodPost, "/api/api.test", "", "", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("api.test through the limiter status=%d body=%s", response.Code, response.Body)
	}
	if response := limitedRequest(t, mux, http.MethodPost, "/api/definitely.not.a.method", "", "", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "unknown_method") {
		t.Fatalf("catch-all through the limiter status=%d body=%s", response.Code, response.Body)
	}
	for i := 0; i < methodBudgetPerMinute; i++ {
		limitedRequest(t, mux, http.MethodPost, "/api/api.test", "", "", "")
	}
	if response := limitedRequest(t, mux, http.MethodPost, "/api/api.test", "", "", ""); response.Code != http.StatusTooManyRequests {
		t.Fatalf("mounted limiter never limited: status=%d", response.Code)
	}
}

// The limiter buckets by the same bearer credential the authenticator reads,
// whatever the scheme's case: a lowercase `bearer` used to fall to the
// client-address bucket, so the same app's budget depended on its spelling.
func TestRateLimiterBucketsTheBearerCredentialCaseInsensitively(t *testing.T) {
	for _, header := range []string{"Bearer xoxb-one", "bearer xoxb-one", "BEARER xoxb-one"} {
		request := httptest.NewRequest(http.MethodPost, "/api/users.list", nil)
		request.Header.Set("Authorization", header)
		if got := rateLimitCredential(request); got != domain.HashToken("xoxb-one") {
			t.Errorf("%q bucketed as %q", header, got)
		}
	}
}

// Register once mounted only "/api/" on the outer mux when a limiter was set,
// so every route outside /api/ — the files_upload_v2 upload URL, incoming
// webhooks, workflow trigger webhooks, public file and photo URLs — answered
// the mux's text/plain 404 in the default, rate-limited production
// configuration while every unlimited test passed. Every registered route must
// resolve to the same pattern with and without a limiter.
func TestRegisterKeepsEveryRouteReachableBehindTheLimiter(t *testing.T) {
	limited := http.NewServeMux()
	Handler{Limiter: NewRateLimiter()}.Register(limited)
	unlimited := http.NewServeMux()
	Handler{}.Register(unlimited)
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	checked := 0
	for _, route := range registeredRoutes(t) {
		if route.method == "" {
			continue
		}
		target := wildcard.ReplaceAllString(route.path, "x")
		request := httptest.NewRequest(route.method, target, nil)
		// The Web API is one fronted /api/ route on the outer mux, with or
		// without a limiter; everything else is its own route there.
		want := route.method + " " + route.path
		if strings.HasPrefix(route.path, "/api/") {
			want = "/api/"
		}
		for name, mux := range map[string]*http.ServeMux{"without a limiter": unlimited, "behind the limiter": limited} {
			if _, pattern := mux.Handler(request); pattern != want {
				t.Errorf("%s %s resolves to %q %s, want %q", route.method, target, pattern, name, want)
			}
		}
		checked++
	}
	if checked < 600 {
		t.Fatalf("only %d routes checked; the route scan is broken", checked)
	}
}

// Incoming webhooks carry Slack's documented one-per-second-per-webhook
// allowance. In the production (limited) configuration a webhook delivers,
// a burst beyond the allowance answers 429 with Retry-After and the
// plain-text body the webhook surface uses, and another webhook — a different
// URL — is a different budget.
func TestIncomingWebhookIsServedAndLimitedPerWebhookBehindTheLimiter(t *testing.T) {
	handler, store := testHandlerValueAs(false, domain.TokenUser, defaultTestScopes()...)
	now := time.Unix(1_700_000_000, 0).UTC()
	handler.Limiter = limiterAt(&now)
	mux := http.NewServeMux()
	handler.Register(mux)
	if err := store.CreateAppInstallation(t.Context(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	created := callAPI(t, mux, http.MethodPost, "/internal/admin/incoming-webhooks/create", "app_id=A1&channel_id=C1&bot_user_id=U2")
	var hook struct {
		OK              bool `json:"ok"`
		IncomingWebhook struct {
			URL string `json:"url"`
		} `json:"incoming_webhook"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &hook); err != nil || !hook.OK {
		t.Fatalf("create through the limited mux status=%d body=%s", created.Code, created.Body)
	}
	path := hook.IncomingWebhook.URL[strings.Index(hook.IncomingWebhook.URL, "/services/"):]
	deliver := func(path string) *httptest.ResponseRecorder {
		return limitedRequest(t, mux, http.MethodPost, path, "", `{"text":"hello"}`, "application/json")
	}
	for i := 0; i < postMessageBurst; i++ {
		if response := deliver(path); response.Code != http.StatusOK || response.Body.String() != "ok" {
			t.Fatalf("delivery %d status=%d body=%q", i, response.Code, response.Body)
		}
	}
	limited := deliver(path)
	if limited.Code != http.StatusTooManyRequests || limited.Body.String() != "rate_limited" || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("burst overflow status=%d body=%q Retry-After=%q", limited.Code, limited.Body, limited.Header().Get("Retry-After"))
	}
	// Another URL is another webhook's budget; it is answered by the handler.
	if response := deliver(path + "-other"); response.Code != http.StatusNotFound {
		t.Fatalf("another webhook status=%d body=%q", response.Code, response.Body)
	}
	now = now.Add(time.Second)
	if response := deliver(path); response.Code != http.StatusOK {
		t.Fatalf("after one second status=%d body=%q", response.Code, response.Body)
	}
}

// Each method is held to the tier Slack publishes for it: its burst passes, the
// next call answers 429 with ratelimited, and the refill is that tier's
// per-minute floor. The cases span every tier and both sources: the pinned
// Java SDK table (rtm.connect and apps.connections.open at Tier 1,
// conversations.list, users.list and workflows.featured.set at Tier 2,
// conversations.history and chat.scheduleMessage at Tier 3, users.info at
// Tier 4, auth.test at its special tier) and reference pages the table
// predates (admin.apps.permissions.add at Tier 2, admin.usergroups.create at
// Tier 1). A method no table names, like the unknown-method catch-all, keeps
// Tier 4's floor.
func TestRateLimiterHoldsEachMethodToItsDocumentedTier(t *testing.T) {
	for _, test := range []struct {
		method    string
		burst     int
		perMinute float64
	}{
		{"rtm.connect", tier1Burst, 1},
		{"apps.connections.open", tier1Burst, 1},
		{"conversations.list", 20, 20},
		{"users.list", 20, 20},
		{"workflows.featured.set", 20, 20},
		{"conversations.history", 50, 50},
		{"chat.scheduleMessage", 50, 50},
		{"chat.scheduledMessages.list", 50, 50},
		{"users.info", methodBudgetPerMinute, methodBudgetPerMinute},
		{"auth.test", specialBudgetPerMinute, specialBudgetPerMinute},
		{"admin.apps.permissions.add", 20, 20},
		{"admin.usergroups.create", tier1Burst, 1},
		{"definitely.not.a.method", methodBudgetPerMinute, methodBudgetPerMinute},
	} {
		t.Run(test.method, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			limiter := limiterAt(&now)
			wrapped := limiter.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			for i := 0; i < test.burst; i++ {
				if response := limitedRequest(t, wrapped, http.MethodPost, "/api/"+test.method, "xoxb-tier", "", ""); response.Code != http.StatusOK {
					t.Fatalf("call %d of the burst status=%d", i+1, response.Code)
				}
			}
			limited := limitedRequest(t, wrapped, http.MethodPost, "/api/"+test.method, "xoxb-tier", "", "")
			if limited.Code != http.StatusTooManyRequests || !strings.Contains(limited.Body.String(), `"error":"ratelimited"`) {
				t.Fatalf("call past the burst status=%d body=%s", limited.Code, limited.Body)
			}
			retryAfter, err := strconv.Atoi(limited.Header().Get("Retry-After"))
			want := int((60 / test.perMinute) + 0.999999) // one call's refill, rounded up to whole seconds
			if err != nil || retryAfter != want {
				t.Fatalf("Retry-After=%q, want %d: the refill is the tier's per-minute floor", limited.Header().Get("Retry-After"), want)
			}
			now = now.Add(time.Duration(retryAfter) * time.Second)
			if response := limitedRequest(t, wrapped, http.MethodPost, "/api/"+test.method, "xoxb-tier", "", ""); response.Code != http.StatusOK {
				t.Fatalf("after Retry-After status=%d", response.Code)
			}
		})
	}
}

// pinnedPublishedTiers reads the rate-limit tier table the pinned official
// Java SDK publishes (slack-api-client 1.49.0, MethodsRateLimits), vendored
// at specs/upstream/java-slack-sdk/methods-rate-limits.json with its hash
// pinned by contractcheck and its contents re-derived from the pinned jar by
// the SDK qualification.
func pinnedPublishedTiers(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../../specs/upstream/java-slack-sdk/methods-rate-limits.json")
	if err != nil {
		t.Fatal(err)
	}
	var tiers map[string]string
	if err := json.Unmarshal(raw, &tiers); err != nil {
		t.Fatal(err)
	}
	if len(tiers) < 300 {
		t.Fatalf("the pinned table lists %d methods; it is truncated", len(tiers))
	}
	return tiers
}

// ledgerMethods is every operation the compatibility ledger tracks.
func ledgerMethods(t *testing.T) map[string]struct{} {
	t.Helper()
	raw, err := os.ReadFile("../../../specs/compatibility.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, operations, ok := strings.Cut(string(raw), "\noperations:\n")
	if !ok {
		t.Fatal("the ledger has no operations section")
	}
	operations, _, _ = strings.Cut(operations, "\ndecisions:\n")
	methods := make(map[string]struct{})
	for _, match := range regexp.MustCompile(`(?m)^  - method: (\S+)$`).FindAllStringSubmatch(operations, -1) {
		methods[match[1]] = struct{}{}
	}
	if len(methods) < 300 {
		t.Fatalf("read %d ledger methods; the ledger scan is broken", len(methods))
	}
	return methods
}

// Every ledger method has exactly one tier source, and a method the pinned
// SDK table lists is enforced at the tier it publishes: a new ledger method
// with no tier, a table entry that disagrees with the pinned table, or a
// reference citation for a method the table already decides fails here.
func TestEveryLedgerMethodIsEnforcedAtItsPublishedTier(t *testing.T) {
	published := pinnedPublishedTiers(t)
	ledger := ledgerMethods(t)
	want := map[string]rateTier{"Tier1": tier1, "Tier2": tier2, "Tier3": tier3, "Tier4": tier4}
	for method := range ledger {
		_, fromTable := publishedMethodTiers[method]
		_, fromReference := referenceMethodTiers[method]
		_, uncited := uncitedTierMethods[method]
		sources := 0
		for _, named := range []bool{fromTable, fromReference, uncited} {
			if named {
				sources++
			}
		}
		if sources != 1 {
			t.Errorf("%s has %d tier sources (table %v, reference %v, uncited %v), want exactly one", method, sources, fromTable, fromReference, uncited)
			continue
		}
		tier, listed := published[method]
		switch {
		case listed && !fromTable:
			t.Errorf("%s: the pinned table publishes %s, so publishedMethodTiers must carry it", method, tier)
		case listed && strings.HasPrefix(tier, "SpecialTier_"):
			expected := tierHundreds
			if tier == "SpecialTier_blocks_validate" {
				expected = tierBlocksValidate
			}
			if got := methodTier(method); got != expected {
				t.Errorf("%s: the pinned table publishes %s, enforced at %+v, want %+v", method, tier, got, expected)
			}
		case listed:
			expected, known := want[tier]
			if !known {
				t.Errorf("%s: the pinned table publishes an unknown tier %q", method, tier)
			} else if got := methodTier(method); got != expected {
				t.Errorf("%s: enforced at %+v, the pinned table publishes %s", method, got, tier)
			}
		case fromTable:
			t.Errorf("%s: publishedMethodTiers names it, but the pinned table does not list it", method)
		case uncited:
			if got := methodTier(method); got != tier4 {
				t.Errorf("%s: its reference tier is uncited, so it must hold Tier 4's floor, enforced at %+v", method, got)
			}
		}
	}
	for name, table := range map[string]map[string]rateTier{"publishedMethodTiers": publishedMethodTiers, "referenceMethodTiers": referenceMethodTiers} {
		for method := range table {
			if _, ok := ledger[method]; !ok {
				t.Errorf("%s names %s, which the compatibility ledger does not", name, method)
			}
		}
	}
	for method := range uncitedTierMethods {
		if _, ok := ledger[method]; !ok {
			t.Errorf("uncitedTierMethods names %s, which the compatibility ledger does not", method)
		}
	}
}

// storeRateTokens is the shared backend as the chat service presents it: the
// store every replica shares, at a fixed instant.
type storeRateTokens struct {
	store *memory.Store
	now   time.Time
}

func (s storeRateTokens) TakeRateToken(ctx context.Context, key string, allowance domain.RateAllowance) (time.Duration, bool, error) {
	return s.store.TakeRateToken(ctx, key, allowance, s.now)
}

type failingRateTokens struct{}

func (failingRateTokens) TakeRateToken(context.Context, string, domain.RateAllowance) (time.Duration, bool, error) {
	return 0, false, errors.New("store unavailable")
}

// Two web replicas of one deployment share the Web API budget: what one
// admits is gone for the other, so N replicas admit what one would rather
// than N times the documented rate.
func TestSharedRateLimiterSharesOneBudgetAcrossReplicas(t *testing.T) {
	shared := storeRateTokens{store: memory.New(), now: time.Unix(1_700_000_000, 0).UTC()}
	passed := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed++ })
	first := NewSharedRateLimiter(shared, nil).Middleware(next)
	second := NewSharedRateLimiter(shared, nil).Middleware(next)
	tier := methodTier("chat.scheduleMessage")
	for i := 0; i < int(tier.burst); i++ {
		replica := first
		if i%2 == 1 {
			replica = second
		}
		if response := limitedRequest(t, replica, http.MethodPost, "/api/chat.scheduleMessage", "xoxb-shared", "", ""); response.Code != http.StatusOK {
			t.Fatalf("call %d status=%d", i, response.Code)
		}
	}
	for name, replica := range map[string]http.Handler{"first": first, "second": second} {
		limited := limitedRequest(t, replica, http.MethodPost, "/api/chat.scheduleMessage", "xoxb-shared", "", "")
		if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" || !strings.Contains(limited.Body.String(), `"error":"ratelimited"`) {
			t.Fatalf("%s replica past the shared budget: status=%d retry-after=%q body=%s", name, limited.Code, limited.Header().Get("Retry-After"), limited.Body)
		}
	}
	if passed != int(tier.burst) {
		t.Fatalf("the handler ran %d times, want the shared burst of %v", passed, tier.burst)
	}
}

// A call the shared store cannot decide is served, not refused as rate
// limited: the store's failure is the request's own to report, and a 429
// would send the client's retry handler waiting on a limit that was never hit.
func TestSharedRateLimiterServesACallItCannotDecide(t *testing.T) {
	passed := 0
	wrapped := NewSharedRateLimiter(failingRateTokens{}, nil).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed++ }))
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.list", "xoxb-one", "", ""); response.Code != http.StatusOK || passed != 1 {
		t.Fatalf("status=%d passed=%d, want the call served", response.Code, passed)
	}
}
