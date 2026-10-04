package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// localConfig is the smallest configuration this binary accepts, so a test that
// exercises one rule does not have to restate the others.
func localConfig() startupConfig {
	return startupConfig{addr: ":8080", chatMode: "local", storeName: "memory", apiToken: "xoxb-test"}
}

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want %q", got, "ok\\n")
	}
}

func TestApplicationRootRedirectsToTheAuthenticatedApplication(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	applicationRootHandler(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/app" {
		t.Fatalf("root response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
}

func TestReadinessChecksTheSelectedService(t *testing.T) {
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "Tdev"})
	selected.SeedUser(domain.User{ID: "Udev", WorkspaceID: "Tdev"})
	selected.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "Tdev", Name: "general"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: selected}, discardLogger(), "Tdev", "Udev"))
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusOK || result.Body.String() != "ready\n" {
		t.Fatalf("ready status=%d body=%q", result.Code, result.Body.String())
	}
	mux = http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: memory.New()}, discardLogger(), "Tdev", "Udev"))
	result = httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusServiceUnavailable || result.Body.String() != "not ready\n" {
		t.Fatalf("not-ready status=%d body=%q", result.Code, result.Body.String())
	}
}

// The probe used to name the hardcoded "Tdev"/"Udev" seed pair, so a deployment
// whose workspace is its own was permanently unready with no diagnosis.
func TestReadinessProbesTheConfiguredWorkspace(t *testing.T) {
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "Tacme"})
	selected.SeedUser(domain.User{ID: "Uowner", WorkspaceID: "Tacme"})
	selected.SeedConversation(domain.Conversation{ID: "Cgeneral", WorkspaceID: "Tacme", Name: "general"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: selected}, discardLogger(), "Tacme", "Uowner"))
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if result.Code != http.StatusOK || result.Body.String() != "ready\n" {
		t.Fatalf("configured-workspace readiness status=%d body=%q", result.Code, result.Body.String())
	}
}

func TestResolveAcceptsTheSmallestLocalConfiguration(t *testing.T) {
	resolved, err := localConfig().resolve()
	if err != nil {
		t.Fatalf("minimal local configuration rejected: %v", err)
	}
	if resolved.workspace != defaultWorkspace || resolved.lookupUser != defaultLookupUser {
		t.Fatalf("workspace=%q user=%q", resolved.workspace, resolved.lookupUser)
	}
	if resolved.socketHost != "" {
		t.Fatalf("socket host = %q, want none: connection URLs follow the request origin", resolved.socketHost)
	}
}

func TestResolveValidatesMonitoringToken(t *testing.T) {
	settings := localConfig()
	settings.monitoringToken = "short"
	if _, err := settings.resolve(); err == nil || !strings.Contains(err.Error(), "SAMEOLDCHAT_MONITORING_TOKEN") {
		t.Fatalf("weak monitoring token error = %v", err)
	}
	settings.monitoringToken = "sameoldchat-monitoring-token-00000000000000000000"
	resolved, err := settings.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.monitoringTokenDigest == nil {
		t.Fatal("valid monitoring token was not retained as a digest")
	}
}

// terraform/ecs-runtime exported SAMEOLDCHAT_OIDC_ISSUER and
// SAMEOLDCHAT_SESSION_TOKEN together, which this binary refuses, so the module
// could not start the binary it configures. The rule is pinned here and
// scripts/check-terraform-module-startup.sh pins the module side of it.
func TestResolveRefusesAStaticSessionAlongsideAnIdentityProvider(t *testing.T) {
	settings := localConfig()
	settings.sessionToken = "dev-session"
	settings.oidcIssuer = "https://id.example.com"
	settings.oidcClientID = "client"
	settings.oidcClientSecret = "secret"
	settings.authWorkspace = "Tdev"
	settings.authLookupUser = "Udev"
	settings.authPublicURL = "https://chat.example.com"
	settings.authStateKeyHex = strings.Repeat("ab", 32)
	_, err := settings.resolve()
	if err == nil || !strings.Contains(err.Error(), "-session-token") {
		t.Fatalf("error = %v, want a static-session refusal", err)
	}
}

// -session-admin escalates a credential every holder shares, so its refusals
// matter more than the escalation itself. Each of these three is a separate
// way the flag could otherwise reach a deployment that has real identities.
func TestResolveRefusesAnAdministrativeSessionWithoutASession(t *testing.T) {
	settings := localConfig()
	settings.sessionAdmin = true
	_, err := settings.resolve()
	if err == nil || !strings.Contains(err.Error(), "-session-admin") {
		t.Fatalf("error = %v, want -session-admin refused without -session-token", err)
	}
}

func TestResolveRefusesAnAdministrativeSessionAlongsideAnIdentityProvider(t *testing.T) {
	settings := localConfig()
	settings.sessionToken = "dev-session"
	settings.sessionAdmin = true
	settings.oidcIssuer = "https://id.example.com"
	settings.oidcClientID = "client"
	settings.oidcClientSecret = "secret"
	settings.authWorkspace = "Tdev"
	settings.authLookupUser = "Udev"
	settings.authPublicURL = "https://chat.example.com"
	settings.authStateKeyHex = strings.Repeat("ab", 32)
	_, err := settings.resolve()
	if err == nil || !strings.Contains(err.Error(), "-session") {
		t.Fatalf("error = %v, want the escalated session refused alongside a provider", err)
	}
}

// The default is the security property: without the flag the shared session is
// a plain member, and the API token is never escalated even with it.
func TestAdministrativeSessionEscalatesTheSessionAndNotTheAPIToken(t *testing.T) {
	plain := localConfig()
	plain.sessionToken = "dev-session"
	resolvedPlain, err := plain.resolve()
	if err != nil {
		t.Fatal(err)
	}
	member, err := auth.ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolvedPlain.sessionScopes) != len(member.Values()) {
		t.Fatalf("session scopes = %d, want the member set by default", len(resolvedPlain.sessionScopes))
	}

	escalated := localConfig()
	escalated.sessionToken = "dev-session"
	escalated.sessionAdmin = true
	resolvedAdmin, err := escalated.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolvedAdmin.sessionScopes) <= len(resolvedPlain.sessionScopes) {
		t.Fatalf("escalated session scopes = %d, want more than the member set (%d)", len(resolvedAdmin.sessionScopes), len(resolvedPlain.sessionScopes))
	}
	if len(resolvedAdmin.scopes) != len(resolvedPlain.scopes) {
		t.Fatalf("API token scopes changed with -session-admin: %d vs %d — only the browser session may be escalated", len(resolvedAdmin.scopes), len(resolvedPlain.scopes))
	}
}

// -app-token/-app-id are seeded only by local composition, so accepting them in
// grpc composition started a deployment that then answered every
// apps.connections.open with an authentication failure and no startup signal.
func TestResolveRefusesLocalOnlySettingsInDistributedComposition(t *testing.T) {
	base := startupConfig{
		addr: ":8080", chatMode: "grpc", apiToken: "xoxb-test",
		chatAddress: "chat:9443", chatCA: "ca.pem", chatServerName: "chat",
		chatClientCert: "client.pem", chatClientKey: "client-key.pem", socketHost: "chat.example.com:443",
	}
	if _, err := base.resolve(); err != nil {
		t.Fatalf("clean distributed configuration rejected: %v", err)
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*startupConfig)
		wanting string
	}{
		{name: "app token", mutate: func(c *startupConfig) { c.appToken = "xapp-1"; c.appID = "A1" }, wanting: "-app-token"},
		{name: "app id", mutate: func(c *startupConfig) { c.appToken = "xapp-1"; c.appID = "A1" }, wanting: "-app-id"},
		{name: "bootstrap admin", mutate: func(c *startupConfig) { c.bootstrapAdminEmail = "admin@example.com" }, wanting: "-bootstrap-admin-email"},
		{name: "store", mutate: func(c *startupConfig) { c.storeName = "sqlite" }, wanting: "-store"},
		{name: "blob bucket", mutate: func(c *startupConfig) { c.blobS3Bucket = "bucket" }, wanting: "-blob-s3-bucket"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			settings := base
			testCase.mutate(&settings)
			_, err := settings.resolve()
			if err == nil || !strings.Contains(err.Error(), testCase.wanting) {
				t.Fatalf("error = %v, want a rejection naming %s", err, testCase.wanting)
			}
		})
	}
}

func TestResolveRejectsAnUnknownChatComposition(t *testing.T) {
	settings := localConfig()
	settings.chatMode = "monolith"
	if _, err := settings.resolve(); err == nil {
		t.Fatal("unknown chat composition was accepted")
	}
}

// A run that only validates must never open a store, dial a peer, or bind a
// listener; it is what scripts/check-terraform-module-startup.sh relies on.
func TestCheckConfigValidatesWithoutStartingAnything(t *testing.T) {
	arguments := []string{"-check-config", "-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test", "-addr", "127.0.0.1:0"}
	if code := run(t.Context(), discardLogger(), arguments); code != 0 {
		t.Fatalf("check-config exit = %d, want 0", code)
	}
	rejected := []string{"-check-config", "-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test", "-session-token", "dev", "-oidc-issuer", "https://id.example.com"}
	if code := run(t.Context(), discardLogger(), rejected); code != exitConfiguration {
		t.Fatalf("refused configuration exit = %d, want %d", code, exitConfiguration)
	}
}

func TestCheckConfigRefusesAnUnusableClientAddressOrHuddlePort(t *testing.T) {
	base := []string{"-check-config", "-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test", "-addr", "127.0.0.1:0"}
	if code := run(t.Context(), discardLogger(), append(base, "-trusted-proxies", "192.0.2.10, 192.168.7.0/24")); code != 0 {
		t.Fatalf("trusted proxies exit = %d, want 0", code)
	}
	if code := run(t.Context(), discardLogger(), append(base, "-trusted-proxies", "caddy")); code != exitConfiguration {
		t.Fatalf("a proxy named by host name exit = %d, want %d", code, exitConfiguration)
	}
	t.Setenv("SAMEOLDCHAT_HUDDLE_UDP_PORT", "ten")
	if code := run(t.Context(), discardLogger(), base); code != exitConfiguration {
		t.Fatalf("an unparseable huddle port exit = %d, want %d", code, exitConfiguration)
	}
}

// TestCheckConfigAcceptsNothingTheRealStartRefuses is the property
// scripts/check-terraform-module-startup.sh treats -check-config as authoritative
// for: if -check-config says yes, a task built from that configuration starts.
//
// Four values broke it. Each was validated after resolve had already returned,
// so -check-config exited 0 and the real start exited 1 or 2 — a crash-looping
// task with the deployment gate green, which is the exact failure that gate
// exists to prevent. Measured before the fix, with the rest of the command line
// held at the accepted local configuration:
//
//	-release-revision not-a-commit        -check-config 0, real start 2
//	-auth-cookie-domain "not a domain!!"  -check-config 0, real start 1
//	-dqlite-cluster "a,,b"                -check-config 0, real start 2
//	-addr no-such-host.invalid:99999      -check-config 0, real start refused at Listen
func TestCheckConfigAcceptsNothingTheRealStartRefuses(t *testing.T) {
	for name, extra := range map[string][]string{
		"release revision": {"-release-revision", "not-a-commit"},
		"cookie domain":    {"-auth-cookie-domain", "not a domain!!"},
		"dqlite cluster":   {"-dqlite-cluster", "a,,b"},
		"listen address":   {"-addr", "no-such-host.invalid:99999"},
		"metrics address":  {"-metrics-listen", "127.0.0.1:99999"},
	} {
		arguments := append([]string{"-check-config", "-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test"}, extra...)
		if code := run(t.Context(), discardLogger(), arguments); code != exitConfiguration {
			t.Fatalf("%s: -check-config exit = %d, want %d", name, code, exitConfiguration)
		}
	}
	// The values a real deployment supplies still pass.
	accepted := []string{
		"-check-config", "-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test",
		"-addr", "0.0.0.0:8080", "-metrics-listen", "127.0.0.1:9464",
		"-auth-cookie-domain", "example.com",
		"-release-revision", "0123456789abcdef0123456789abcdef01234567",
		"-dqlite-cluster", "10.0.0.1:9000,10.0.0.2:9000",
	}
	if code := run(t.Context(), discardLogger(), accepted); code != 0 {
		t.Fatalf("a real deployment's configuration was refused: exit = %d", code)
	}
}

// A configuration fault must not be reported as a runtime failure: an
// orchestrator with a restart-on-runtime-failure-only policy restart-loops
// forever on a mistyped path otherwise.
func TestConfigurationFaultsExitTwo(t *testing.T) {
	for _, arguments := range [][]string{
		{"-chat-mode", "local", "-store", "memory"},
		{"-chat-mode", "grpc", "-api-token", "t"},
		{"-chat-mode", "local", "-store", "memory", "-api-token", "t", "-app-token", "xapp-1"},
		// Refused by web.NewHandler after every store was already open. It
		// exited 1, so the orchestrator retried it forever.
		{"-chat-mode", "local", "-store", "memory", "-api-token", "t", "-auth-cookie-domain", "not a domain!!"},
	} {
		if code := run(t.Context(), discardLogger(), arguments); code != exitConfiguration {
			t.Fatalf("run(%v) = %d, want %d", arguments, code, exitConfiguration)
		}
	}
}

// TestStartupInterruptedByShutdownIsACleanStop covers the rolling-deploy defect:
// the signal context is established before the first durable resource — which is
// right — and is then passed to localchat.Open, the seeding calls and OpenID
// Connect discovery, so a SIGTERM during a slow cold start made every one of
// them fail and run returned exitRuntime. A task stopped mid-startup was
// recorded as a task failure, which a deployment circuit breaker rolls back on.
func TestStartupInterruptedByShutdownIsACleanStop(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	arguments := []string{"-chat-mode", "local", "-store", "sqlite", "-db", filepath.Join(t.TempDir(), "chat.db"), "-api-token", "xoxb-test", "-addr", "127.0.0.1:0", "-app-credential-key-hex", strings.Repeat("01", 32)}
	if code := run(stopped, discardLogger(), arguments); code != 0 {
		t.Fatalf("a startup interrupted by SIGTERM exited %d, want 0", code)
	}
}

// The default command line is the production shape: -api-rate-limit is on.
// Every harness that boots this binary turned it off, so when the limited
// registration mounted only /api/ nothing noticed that the external upload URL
// files_upload_v2 posts to, incoming webhooks and public file URLs all answered
// the mux's text/plain 404. This boots the binary with defaults and asks each
// surface outside /api/ for an answer from its own handler. (The public file
// and photo URLs answer an unknown token with the same text/plain 404 by
// design, so their reachability is held by the slack package route gate.)
func TestDefaultConfigurationServesTheSurfacesOutsideTheWebAPI(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	exited := make(chan int, 1)
	go func() {
		exited <- run(ctx, discardLogger(), []string{"-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test", "-addr", address})
	}()
	t.Cleanup(func() {
		cancel()
		<-exited
	})
	base := "http://" + address
	deadline := time.Now().Add(30 * time.Second)
	for {
		response, err := http.Get(base + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case code := <-exited:
			t.Fatalf("server exited %d before becoming healthy", code)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, probe := range []struct{ method, path string }{
		{http.MethodPost, "/internal/files/external/unknown-upload"},
		{http.MethodPost, "/services/T0/A0/secret"},
		{http.MethodPost, "/services/triggers/T0/Ft0/secret"},
		{http.MethodPost, "/internal/admin/incoming-webhooks/create"},
		{http.MethodGet, "/api/dnd.setSnooze"},
	} {
		request, err := http.NewRequest(probe.method, base+probe.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if string(body) == "404 page not found\n" || response.StatusCode == http.StatusMethodNotAllowed {
			t.Errorf("%s %s is unrouted in the default configuration: %d %q", probe.method, probe.path, response.StatusCode, body)
		}
	}
}

func TestReleaseRevisionSkipsOnlyTheDevelopmentIdentity(t *testing.T) {
	settings := localConfig()
	if got := settings.releaseRevision(developmentReleaseRevision); got != "" {
		t.Fatalf("development identity = %q, want it skipped", got)
	}
	if got := settings.releaseRevision(" 0123456789ab "); got != "0123456789ab" {
		t.Fatalf("configured identity = %q", got)
	}
}

func TestParseClusterNormalizesAddresses(t *testing.T) {
	got, err := localchat.ParseCluster(" node-a:19001, node-b:19001 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node-a:19001", "node-b:19001"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cluster = %#v, want %#v", got, want)
	}
}

func TestParseClusterRejectsEmptyAddress(t *testing.T) {
	if _, err := localchat.ParseCluster("node-a:19001,,node-b:19001"); err == nil {
		t.Fatal("empty cluster address was accepted")
	}
}

func TestDatabaseDSNDefaultUsesRuntimeEnvironment(t *testing.T) {
	t.Setenv("SAMEOLDCHAT_DATABASE_URL", "postgres://sameoldchat:secret@postgres.example:5432/sameoldchat?sslmode=require")
	if got, want := databaseDSNDefault(), "postgres://sameoldchat:secret@postgres.example:5432/sameoldchat?sslmode=require"; got != want {
		t.Fatalf("database DSN default = %q, want %q", got, want)
	}
}

func TestReleaseRevisionDefaultPrefersRuntimeCoordinateThenBakedIdentity(t *testing.T) {
	original := releaseRevision
	releaseRevision = "0123456789abcdef0123456789abcdef01234567"
	t.Cleanup(func() { releaseRevision = original })
	t.Setenv("SAMEOLDCHAT_RELEASE_REVISION", "")
	if got := releaseRevisionDefault(); got != releaseRevision {
		t.Fatalf("baked release revision=%q", got)
	}
	t.Setenv("SAMEOLDCHAT_RELEASE_REVISION", "abcdef012345abcdef012345abcdef012345abcd")
	if got := releaseRevisionDefault(); got != "abcdef012345abcdef012345abcdef012345abcd" {
		t.Fatalf("runtime release revision=%q", got)
	}
}

func TestResolveDatabaseDSNUsesEnvironmentOnlyForLocalComposition(t *testing.T) {
	t.Setenv("SAMEOLDCHAT_DATABASE_URL", "postgres://sameoldchat:secret@postgres.example/sameoldchat")
	if got, err := resolveDatabaseDSN("local", ""); err != nil || got != "postgres://sameoldchat:secret@postgres.example/sameoldchat" {
		t.Fatalf("local DSN = %q, error=%v", got, err)
	}
	if got, err := resolveDatabaseDSN("grpc", ""); err != nil || got != "" {
		t.Fatalf("distributed DSN = %q, error=%v", got, err)
	}
}

func TestResolveDatabaseDSNRejectsExplicitDistributedLocalStorage(t *testing.T) {
	if _, err := resolveDatabaseDSN("grpc", "file:chat.db"); err == nil || !strings.Contains(err.Error(), "cannot use a local database DSN") {
		t.Fatalf("error=%v, want explicit distributed local-storage rejection", err)
	}
}

func TestResolveDatabaseDSNRejectsUnknownComposition(t *testing.T) {
	if _, err := resolveDatabaseDSN("", ""); err == nil {
		t.Fatal("unknown chat composition was accepted")
	}
}

// -socket-host names a host, and a URL there would be embedded verbatim in
// every connection URL apps.connections.open hands out.
func TestResolveRejectsASocketHostThatIsNotAHost(t *testing.T) {
	for _, host := range []string{"https://chat.example.com", "chat.example.com/socket-mode", "user@chat.example.com"} {
		config := localConfig()
		config.socketHost = host
		if _, err := config.resolve(); err == nil || !strings.Contains(err.Error(), "-socket-host") {
			t.Errorf("-socket-host %q: error=%v, want a -socket-host rejection", host, err)
		}
	}
	config := localConfig()
	config.socketHost = "chat.example.com:443"
	if _, err := config.resolve(); err != nil {
		t.Fatalf("-socket-host chat.example.com:443 rejected: %v", err)
	}
}

// startServer runs this binary's composition on a free loopback port and
// returns its base URL once it answers.
func startServer(t *testing.T, arguments ...string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan int, 1)
	go func() { exited <- run(ctx, discardLogger(), append([]string{"-addr", addr}, arguments...)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := http.Get(base + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return base
			}
		}
		select {
		case code := <-exited:
			t.Fatalf("server exited with %d before answering", code)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not answer: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func postAPI(t *testing.T, base, method, token string, header http.Header) map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/api/"+method, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true {
		t.Fatalf("%s status=%d body=%v", method, response.StatusCode, body)
	}
	return body
}

func dialHello(t *testing.T, address string) map[string]any {
	t.Helper()
	client, _, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	if err := client.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
	return hello
}

// This drives the production composition itself, not a copy of it. cmd/server
// configured Socket Mode after Register had bound every route to a copy of the
// handler, so apps.connections.open answered socket_mode_unavailable in every
// real deployment while the SDK qualification fixture, which wired the same
// pieces in the other order, passed.
func TestProductionCompositionServesSocketModeAndRTM(t *testing.T) {
	base := startServer(t, "-chat-mode", "local", "-store", "memory", "-api-token", "xoxb-test", "-api-rate-limit=false",
		"-app-token", "xapp-test", "-app-id", "A0TEST")
	opened := postAPI(t, base, "apps.connections.open", "xapp-test", nil)
	address, _ := opened["url"].(string)
	if want := "ws" + strings.TrimPrefix(base, "http") + "/socket-mode?"; !strings.HasPrefix(address, want) {
		t.Fatalf("apps.connections.open url=%q, want the origin the client called: %s", address, want)
	}
	hello := dialHello(t, address)
	if info, _ := hello["connection_info"].(map[string]any); info["app_id"] != "A0TEST" {
		t.Fatalf("Socket Mode hello=%v", hello)
	}
	behindProxy := postAPI(t, base, "apps.connections.open", "xapp-test", http.Header{"X-Forwarded-Proto": []string{"https"}})
	if address, _ := behindProxy["url"].(string); !strings.HasPrefix(address, "wss://") {
		t.Fatalf("apps.connections.open behind a TLS proxy url=%q, want wss://", address)
	}
	connected := postAPI(t, base, "rtm.connect", "xoxb-test", nil)
	rtmAddress, _ := connected["url"].(string)
	if !strings.HasPrefix(rtmAddress, "ws"+strings.TrimPrefix(base, "http")+"/rtm?") {
		t.Fatalf("rtm.connect url=%q", rtmAddress)
	}
	dialHello(t, rtmAddress)
}

// The service layer enforces conversation membership on chat.postMessage and
// nine other operations. The dev seed created the API token and the static
// session and joined neither to the seeded channel, so every seeded credential
// authenticated and then answered `not_in_channel` on its first write.
func TestSeedDevelopmentCredentialsJoinsTheSeededConversation(t *testing.T) {
	backing := memory.New()
	backing.SeedWorkspace(domain.Workspace{ID: defaultWorkspace})
	backing.SeedUser(domain.User{ID: defaultLookupUser, WorkspaceID: defaultWorkspace})
	backing.SeedConversation(domain.Conversation{ID: defaultConversation, WorkspaceID: defaultWorkspace, Name: "general"})
	messages := service.Messages{Store: backing}
	runtime := localchat.Runtime{Service: messages, Store: backing, TokenSeeder: backing, SessionSeeder: backing}
	resolved := resolvedConfig{workspace: defaultWorkspace, lookupUser: defaultLookupUser, apiToken: "xoxb-test", scopes: []string{"chat:write"}}

	// Twice, because the seed runs on every start against a durable store.
	for attempt := range 2 {
		if err := seedDevelopmentCredentials(t.Context(), runtime, resolved, discardLogger(), time.Now().UTC()); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}

	members, err := backing.ListConversationMembers(t.Context(), defaultConversation, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, member := range members.Users {
		if member.ID == defaultLookupUser {
			found = true
		}
	}
	if !found {
		t.Fatalf("seeded user %q is not a member of %q; members=%v", defaultLookupUser, defaultConversation, members.Users)
	}
}

// A deployment whose workspace is its own has no seeded conversation to join,
// and that must be reported rather than fail startup.
func TestSeedDevelopmentCredentialsToleratesNoSeededConversation(t *testing.T) {
	backing := memory.New()
	backing.SeedWorkspace(domain.Workspace{ID: "Tacme"})
	backing.SeedUser(domain.User{ID: "Uowner", WorkspaceID: "Tacme"})
	runtime := localchat.Runtime{Service: service.Messages{Store: backing}, Store: backing, TokenSeeder: backing, SessionSeeder: backing}
	resolved := resolvedConfig{workspace: "Tacme", lookupUser: "Uowner", apiToken: "xoxb-test", scopes: []string{"chat:write"}}

	if err := seedDevelopmentCredentials(t.Context(), runtime, resolved, discardLogger(), time.Now().UTC()); err != nil {
		t.Fatalf("startup failed because there was no seeded conversation: %v", err)
	}
}

// The Web API budget is shared wherever a deployment may run more than one web
// replica, and kept in process where it cannot: memory and SQLite are
// single-replica, so sharing there would cost a write per call for nothing.
func TestRateLimitsAreSharedWhereReplicasCanBeMany(t *testing.T) {
	for _, item := range []struct {
		chatMode, store string
		shared          bool
	}{
		{"local", "memory", false},
		{"local", "sqlite", false},
		{"local", "postgresql", true},
		{"local", "dqlite", true},
		{"grpc", "", true},
	} {
		if got := sharesRateLimits(startupConfig{chatMode: item.chatMode, storeName: item.store}); got != item.shared {
			t.Errorf("chat-mode %s store %q: shared=%v, want %v", item.chatMode, item.store, got, item.shared)
		}
	}
}
