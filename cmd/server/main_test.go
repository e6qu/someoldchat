package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
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
	if resolved.socketHost != "localhost:8080" {
		t.Fatalf("socket host = %q", resolved.socketHost)
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

// A configuration fault must not be reported as a runtime failure: an
// orchestrator with a restart-on-runtime-failure-only policy restart-loops
// forever on a mistyped path otherwise.
func TestConfigurationFaultsExitTwo(t *testing.T) {
	for _, arguments := range [][]string{
		{"-chat-mode", "local", "-store", "memory"},
		{"-chat-mode", "grpc", "-api-token", "t"},
		{"-chat-mode", "local", "-store", "memory", "-api-token", "t", "-app-token", "xapp-1"},
	} {
		if code := run(t.Context(), discardLogger(), arguments); code != exitConfiguration {
			t.Fatalf("run(%v) = %d, want %d", arguments, code, exitConfiguration)
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

// The Socket Mode connection host used to be hardcoded to "localhost:8080",
// which ignored -addr entirely, so a server on another port handed every Socket
// Mode client an unreachable URL from apps.connections.open.
func TestSocketHostDefaultFollowsTheListenAddress(t *testing.T) {
	for _, testCase := range []struct{ addr, want string }{
		{addr: ":8080", want: "localhost:8080"},
		{addr: ":9000", want: "localhost:9000"},
		{addr: " :9000 ", want: "localhost:9000"},
		{addr: "0.0.0.0:9100", want: "localhost:9100"},
		{addr: "[::]:9200", want: "localhost:9200"},
		{addr: "chat.example.com:443", want: "chat.example.com:443"},
		{addr: "10.0.0.5:8080", want: "10.0.0.5:8080"},
		{addr: "not-an-address", want: "localhost:8080"},
	} {
		if got := socketHostDefault(testCase.addr); got != testCase.want {
			t.Errorf("socketHostDefault(%q) = %q, want %q", testCase.addr, got, testCase.want)
		}
	}
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
