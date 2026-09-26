package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Behind a TLS-terminating proxy the request reaches this process over plain
// HTTP with X-Forwarded-Proto: https. rtm.connect read the scheme from r.TLS
// alone and handed every such client a ws:// URL the proxy does not serve.
func TestRTMConnectFollowsTheForwardedScheme(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/rtm.connect?token=token", nil)
	request.Host = "chat.example.test"
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	var body struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	streamURL, err := url.Parse(body.URL)
	if err != nil || !body.OK || streamURL.Scheme != "wss" || streamURL.Host != "chat.example.test" || streamURL.Path != "/rtm" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func socketModeTestMux(t *testing.T, socket socketmode.Service) (*http.ServeMux, *memory.Store) {
	t.Helper()
	repository := memory.New()
	repository.SeedAppToken(context.Background(), "xapp-test", domain.AppTokenRecord{AppID: "A1", Scopes: []string{string(auth.ScopeConnectionsWrite)}})
	userAuth, err := auth.NewStatic("user-token", auth.Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	appAuth, err := auth.NewAppStored(repository)
	if err != nil {
		t.Fatal(err)
	}
	socket.Store = repository
	handler, err := NewHandler(service.Messages{Store: repository}, userAuth, WithAppAuthenticator(appAuth), WithSocketMode(socket))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux, repository
}

func openConnection(mux http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.Host = "chat.example.test"
	request.Header.Set("Authorization", "Bearer xapp-test")
	for name, values := range header {
		request.Header[name] = values
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

// The Socket Mode URL follows the origin the SDK reached apps.connections.open
// on unless the operator configured a host. It was fixed at startup, so a
// deployment that configured nothing handed out ws://localhost:8080.
func TestAppsConnectionsOpenFollowsTheRequestOrigin(t *testing.T) {
	for _, test := range []struct {
		name    string
		service socketmode.Service
		header  http.Header
		want    string
	}{
		{"plain", socketmode.Service{}, nil, "ws://chat.example.test/socket-mode"},
		{"behind a TLS proxy", socketmode.Service{}, http.Header{"X-Forwarded-Proto": []string{"https"}}, "wss://chat.example.test/socket-mode"},
		{"configured host", socketmode.Service{Host: "sockets.example.test", TLS: true}, nil, "wss://sockets.example.test/socket-mode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux, _ := socketModeTestMux(t, test.service)
			response := openConnection(mux, "/api/apps.connections.open", test.header)
			var body struct {
				OK  bool   `json:"ok"`
				URL string `json:"url"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(body.URL)
			if err != nil || !body.OK || parsed.Scheme+"://"+parsed.Host+parsed.Path != test.want || parsed.Query().Get("connection_id") == "" {
				t.Fatalf("status=%d body=%s, want %s", response.Code, response.Body, test.want)
			}
		})
	}
}

// Holding every permitted connection is temporary: one closing frees a slot.
// It used to be answered with fatal_error, which official Socket Mode clients
// treat as permanent; 429 with Retry-After is what every official Web API
// client retries on its own.
func TestAppsConnectionsOpenAtTheLimitIsRetryable(t *testing.T) {
	mux, repository := socketModeTestMux(t, socketmode.Service{})
	for range domain.SocketModeConnectionLimit {
		response := openConnection(mux, "/api/apps.connections.open", nil)
		var body struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(body.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ConsumeSocketModeConnection(context.Background(), parsed.Query().Get("connection_id")); err != nil {
			t.Fatal(err)
		}
	}
	response := openConnection(mux, "/api/apps.connections.open", nil)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d retry-after=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body)
	}
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.OK || body.Error != "rate_limited" {
		t.Fatalf("body=%s", response.Body)
	}
}
