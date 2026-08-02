package slack

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func limiterAt(now *time.Time) *RateLimiter {
	limiter := NewRateLimiter()
	limiter.now = func() time.Time { return *now }
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
// on: 429, a positive integer Retry-After, and the pinned rate_limited code —
// and the budget is per credential and per method, so one caller cannot
// starve another and one hot method cannot silence the rest of the API.
func TestRateLimiterAnswers429WithRetryAfterPerCredentialAndMethod(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	limiter := limiterAt(&now)
	passed := 0
	wrapped := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed++ }))
	for i := 0; i < methodBudgetPerMinute; i++ {
		if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.list", "xoxb-one", "", ""); response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d", i, response.Code)
		}
	}
	limited := limitedRequest(t, wrapped, http.MethodPost, "/api/users.list", "xoxb-one", "", "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget status=%d, want %d", limited.Code, http.StatusTooManyRequests)
	}
	if !strings.Contains(limited.Body.String(), `"error":"rate_limited"`) || !strings.Contains(limited.Body.String(), `"ok":false`) {
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
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.list", "xoxb-two", "", ""); response.Code != http.StatusOK {
		t.Fatalf("other credential status=%d", response.Code)
	}
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/conversations.list", "xoxb-one", "", ""); response.Code != http.StatusOK {
		t.Fatalf("other method status=%d", response.Code)
	}
	// Waiting the advertised time restores service.
	now = now.Add(time.Duration(retryAfter) * time.Second)
	if response := limitedRequest(t, wrapped, http.MethodPost, "/api/users.list", "xoxb-one", "", ""); response.Code != http.StatusOK {
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
