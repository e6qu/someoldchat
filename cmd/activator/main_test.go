package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidControlTokenRequiresBearerSchemeAndExactToken(t *testing.T) {
	for _, test := range []struct {
		header string
		want   bool
	}{
		{header: "Bearer secret", want: true},
		{header: "Bearer  secret ", want: true},
		{header: "secret", want: false},
		{header: "bearer secret", want: false},
		{header: "Bearer", want: false},
		{header: "Bearer other", want: false},
		{header: "Bearer secret extra", want: false},
	} {
		if got := validControlToken(test.header, "secret"); got != test.want {
			t.Fatalf("validControlToken(%q)=%t, want %t", test.header, got, test.want)
		}
	}
}

func TestValidControlTokenRejectsEmptyExpectedToken(t *testing.T) {
	if validControlToken("Bearer secret", "") {
		t.Fatal("empty expected control token was accepted")
	}
}

// /metrics is served on the same public listener as forwarded application
// traffic and publishes lifecycle state, snapshot sizes, and queue depths. It was
// reachable without the control-plane token.
func TestControlTokenGuardsMetricsAlongsideLifecycleEndpoints(t *testing.T) {
	forwarded := false
	serve := func(w http.ResponseWriter, _ *http.Request) {
		forwarded = true
		w.WriteHeader(http.StatusNoContent)
	}
	// Registered with the same patterns as run(), so the guard is exercised
	// through the pattern lookup it actually uses in production rather than
	// through a bare handler that never matches a route.
	mux := http.NewServeMux()
	mux.HandleFunc("/", serve)
	mux.HandleFunc("GET /healthz", serve)
	mux.HandleFunc("GET /metrics", serve)
	mux.HandleFunc("POST /hibernate", serve)
	mux.HandleFunc("POST /recover", serve)
	guarded := requireControlToken(mux, "secret")

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/metrics"},
		{http.MethodPost, "/hibernate"},
		{http.MethodPost, "/recover"},
	} {
		forwarded = false
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusUnauthorized || forwarded {
			t.Fatalf("%s %s status=%d forwarded=%t, want the control token required", route.method, route.path, response.Code, forwarded)
		}
		authorized := httptest.NewRequest(route.method, route.path, nil)
		authorized.Header.Set("Authorization", "Bearer secret")
		response = httptest.NewRecorder()
		guarded.ServeHTTP(response, authorized)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s %s authorized status=%d", route.method, route.path, response.Code)
		}
	}

	// Application traffic and the liveness probe are the only unauthenticated
	// patterns; everything else is protected by omission from the allow-list.
	for _, path := range []string{"/api/message", "/healthz"} {
		forwarded = false
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if !forwarded || response.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d forwarded=%t, want it served without the control token", path, response.Code, forwarded)
		}
	}
}

// A route added to the mux without being added to the allow-list must be
// protected, which is the property the previous deny-list did not have.
func TestUnlistedControlRouteIsProtectedByDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /newly-added-control-endpoint", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an endpoint absent from the allow-list was served without the control token")
	})
	guarded := requireControlToken(mux, "secret")
	response := httptest.NewRecorder()
	guarded.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/newly-added-control-endpoint", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want the control token required", response.Code)
	}
}
