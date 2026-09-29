package clientaddr

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRefusesWhatIsNotAnAddressOrRange(t *testing.T) {
	for _, list := range []string{"caddy", "10.0.0.0/40", "::ffff:10.0.0.0/104"} {
		if _, err := Parse(list); err == nil {
			t.Errorf("Parse(%q) accepted it", list)
		}
	}
}

func TestClientBehindATrustedProxyIsTheForwardedAddress(t *testing.T) {
	resolver, err := Parse("192.0.2.10, 192.168.7.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer string
		forwarded  []string
		want       string
	}{
		{name: "direct client ignores its own header", peer: "203.0.113.9:4000", forwarded: []string{"198.51.100.1"}, want: "203.0.113.9"},
		{name: "trusted proxy forwards the client", peer: "192.0.2.10:5000", forwarded: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "a spoofed leading entry is skipped", peer: "192.0.2.10:5000", forwarded: []string{"1.2.3.4, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "a chain of trusted proxies is walked", peer: "192.0.2.10:5000", forwarded: []string{"198.51.100.1", "192.168.7.4"}, want: "198.51.100.1"},
		{name: "no header leaves the proxy", peer: "192.0.2.10:5000", want: "192.0.2.10"},
		{name: "a mapped peer is its IPv4 address", peer: "[::ffff:192.0.2.10]:5000", forwarded: []string{"198.51.100.1"}, want: "198.51.100.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = tc.peer
			for _, value := range tc.forwarded {
				request.Header.Add("X-Forwarded-For", value)
			}
			got, err := resolver.Client(request)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("client = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMiddlewareRewritesRemoteAddrAndRefusesAnUnusableHeader(t *testing.T) {
	resolver, err := Parse("192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	handler := resolver.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		seen = request.RemoteAddr
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.10:5000"
	request.Header.Set("X-Forwarded-For", "198.51.100.1")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if seen != "198.51.100.1:0" {
		t.Fatalf("handler saw RemoteAddr %q", seen)
	}

	seen = ""
	refused := httptest.NewRequest(http.MethodGet, "/", nil)
	refused.RemoteAddr = "192.0.2.10:5000"
	refused.Header.Set("X-Forwarded-For", "unknown")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, refused)
	if recorder.Code != http.StatusBadRequest || seen != "" {
		t.Fatalf("an unusable forwarded entry answered %d and reached the handler: %q", recorder.Code, seen)
	}
}
