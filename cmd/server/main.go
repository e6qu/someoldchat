package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/api/slack"
	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/generated"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatgrpc "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc"
	"github.com/sameoldchat/sameoldchat/internal/observability"
	"github.com/sameoldchat/sameoldchat/internal/realtime"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var releaseRevision = "development"

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	// -chat-mode, -store, -auth-workspace, -auth-lookup-user, and -auth-public-url
	// take an environment default like their neighbours because
	// terraform/ecs-runtime configures a task purely from environment variables.
	// Without these the module's documented `environment` output produced a task
	// that exited 2 at "invalid chat composition", so following the module README
	// yielded a crash-looping service.
	chatMode := flag.String("chat-mode", os.Getenv("SAMEOLDCHAT_CHAT_MODE"), "chat composition: local or grpc (required)")
	storeName := flag.String("store", os.Getenv("SAMEOLDCHAT_STORE"), "local storage backend: memory, sqlite, postgresql, or dqlite (required for -chat-mode=local)")
	// The default is deliberately empty rather than os.Getenv: the environment
	// variable applies only to local composition (resolveDatabaseDSN), and reading
	// it as the flag default would leak it into grpc composition, where an
	// explicit DSN is rejected. The help text names it so `-h` still discloses it.
	dsn := flag.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql storage; defaults to SAMEOLDCHAT_DATABASE_URL in local composition only")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory; required for local dqlite storage")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address; required for local dqlite storage")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name; required for local dqlite storage")
	blobDirectory := flag.String("blob-dir", "", "external blob directory for file storage")
	blobS3Bucket := flag.String("blob-s3-bucket", os.Getenv("SAMEOLDCHAT_BLOB_S3_BUCKET"), "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flag.String("blob-s3-prefix", os.Getenv("SAMEOLDCHAT_BLOB_S3_PREFIX"), "Amazon Simple Storage Service key prefix for file storage")
	blobMaxBytes := flag.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	chatAddress := flag.String("chat-address", "", "distributed chat gRPC address; required for -chat-mode=grpc")
	chatCA := flag.String("chat-ca", "", "CA certificate for distributed chat gRPC")
	chatServerName := flag.String("chat-server-name", "", "TLS server name for distributed chat gRPC")
	chatClientCert := flag.String("chat-client-cert", "", "client certificate for distributed chat gRPC")
	chatClientKey := flag.String("chat-client-key", "", "client private key for distributed chat gRPC")
	apiToken := flag.String("api-token", os.Getenv("SAMEOLDCHAT_API_TOKEN"), "API bearer token (required)")
	// -session-token is optional. It seeds one static browser session that every
	// visitor holding the value shares, which is a development convenience and
	// never an identity: identityConfig.validate rejects it as soon as a real
	// identity provider is configured, because a shared bearer session bypasses
	// every provider check, every revocation, and every workspace role.
	sessionToken := flag.String("session-token", os.Getenv("SAMEOLDCHAT_SESSION_TOKEN"), "static development browser session token; rejected when an identity provider is configured")
	metricsListen := flag.String("metrics-listen", os.Getenv("SAMEOLDCHAT_METRICS_LISTEN"), "operator-only listen address publishing /metrics; empty serves no metrics endpoint")
	authWorkspace := flag.String("auth-workspace", os.Getenv("SAMEOLDCHAT_AUTH_WORKSPACE"), "workspace for external authorization (required when enabled)")
	authLookupUser := flag.String("auth-lookup-user", os.Getenv("SAMEOLDCHAT_AUTH_LOOKUP_USER"), "existing user used to authorize external identity lookup (required when enabled)")
	authPublicURL := flag.String("auth-public-url", os.Getenv("SAMEOLDCHAT_AUTH_PUBLIC_URL"), "public HTTPS URL used for authorization callbacks")
	authCookieDomain := flag.String("auth-cookie-domain", os.Getenv("SAMEOLDCHAT_AUTH_COOKIE_DOMAIN"), "optional parent DNS domain for SameOldChat session cookies")
	authStateKeyHex := flag.String("auth-state-key-hex", os.Getenv("SAMEOLDCHAT_AUTH_STATE_KEY_HEX"), "HMAC key for authorization state, at least 32 bytes of hex")
	bootstrapAdminEmail := flag.String("bootstrap-admin-email", os.Getenv("SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL"), "email address of the initial local workspace administrator")
	appToken := flag.String("app-token", os.Getenv("SAMEOLDCHAT_APP_TOKEN"), "Socket Mode app-level token")
	appID := flag.String("app-id", os.Getenv("SAMEOLDCHAT_APP_ID"), "Socket Mode app identifier")
	socketHost := flag.String("socket-host", os.Getenv("SAMEOLDCHAT_SOCKET_HOST"), "public host used in Socket Mode connection URLs")
	socketTLS := flag.Bool("socket-tls", os.Getenv("SAMEOLDCHAT_SOCKET_TLS") == "1", "use secure WebSocket URLs for Socket Mode")
	googleClientID := flag.String("google-client-id", "", "Google OAuth client ID")
	googleClientSecret := flag.String("google-client-secret", "", "Google OAuth client secret")
	githubClientID := flag.String("github-client-id", "", "GitHub OAuth client ID")
	githubClientSecret := flag.String("github-client-secret", "", "GitHub OAuth client secret")
	entraClientID := flag.String("entra-client-id", "", "Microsoft Entra application client ID")
	entraClientSecret := flag.String("entra-client-secret", "", "Microsoft Entra application client secret")
	entraTenant := flag.String("entra-tenant", entraTenantDefault, "Microsoft Entra directory (tenant) identifier; multi-tenant endpoints are rejected")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("SAMEOLDCHAT_OIDC_ISSUER"), "OpenID Connect issuer URL")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("SAMEOLDCHAT_OIDC_CLIENT_ID"), "OpenID Connect client ID")
	oidcClientSecret := flag.String("oidc-client-secret", os.Getenv("SAMEOLDCHAT_OIDC_CLIENT_SECRET"), "OpenID Connect client secret")
	release := flag.String("release-revision", releaseRevisionDefault(), "immutable application commit or image digest exposed to Shauth validation")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	resolvedDSN, err := resolveDatabaseDSN(*chatMode, *dsn)
	if err != nil {
		logger.Error("resolve database configuration", "error", err)
		os.Exit(2)
	}
	if *socketHost == "" {
		if *appToken != "" || *appID != "" {
			logger.Error("-socket-host is required when Socket Mode is configured because apps.connections.open hands the value to clients")
			os.Exit(2)
		}
		*socketHost = socketHostDefault(*addr)
	}

	mux := http.NewServeMux()
	if *chatMode != "local" && *chatMode != "grpc" {
		logger.Error("invalid chat composition", "mode", *chatMode, "allowed", "local, grpc")
		os.Exit(2)
	}
	identity := identityConfig{
		apiToken: *apiToken, sessionToken: *sessionToken,
		oidcIssuer: *oidcIssuer, googleClientID: *googleClientID, githubClientID: *githubClientID,
		entraClientID: *entraClientID, entraTenant: *entraTenant,
	}
	if err := identity.validate(); err != nil {
		logger.Error("invalid identity configuration", "error", err)
		os.Exit(2)
	}
	// The registry is always built because the distributed chat client records
	// into it through the transport package's dial options; it is published only
	// on the operator listener, never on the listener that serves users.
	metrics := observability.NewRegistry()
	var chatService chatapi.Service
	var authenticator auth.Authenticator
	var webAuthenticator auth.Authenticator
	var sessionRevoker auth.SessionRevoker
	var socketModeStore socketmode.ConnectionStore
	var socketModeAuth auth.Authenticator
	switch *chatMode {
	case "local":
		if *chatAddress != "" || *chatCA != "" || *chatServerName != "" {
			logger.Error("distributed chat settings supplied for local composition")
			os.Exit(2)
		}
		cluster, err := localchat.ParseCluster(*dqliteCluster)
		if err != nil {
			logger.Error("parse dqlite cluster", "error", err)
			os.Exit(2)
		}
		runtime, err := localchat.Open(context.Background(), localchat.Config{Backend: localchat.Backend(*storeName), DSN: resolvedDSN, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, BlobDirectory: *blobDirectory, BlobS3Bucket: *blobS3Bucket, BlobS3Prefix: *blobS3Prefix, BlobMaxBytes: *blobMaxBytes, BootstrapAdminEmail: *bootstrapAdminEmail})
		if err != nil {
			logger.Error("open local chat", "error", err)
			os.Exit(1)
		}
		chatService = runtime.Service
		socketModeStore = runtime.Store
		if *appToken != "" || *appID != "" {
			if *appToken == "" || *appID == "" {
				logger.Error("Socket Mode requires both app token and app ID")
				os.Exit(2)
			}
			appTokenStore, ok := runtime.TokenStore.(interface {
				SeedAppToken(context.Context, string, domain.AppTokenRecord) error
				auth.AppTokenStore
			})
			if !ok {
				logger.Error("storage backend cannot seed Socket Mode app tokens")
				os.Exit(1)
			}
			if err := appTokenStore.SeedAppToken(context.Background(), *appToken, domain.AppTokenRecord{AppID: domain.AppID(*appID), Scopes: []string{string(auth.ScopeConnectionsWrite)}}); err != nil {
				logger.Error("seed Socket Mode app token", "error", err)
				os.Exit(1)
			}
			if err := runtime.Store.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: domain.AppID(*appID), WorkspaceID: "Tdev", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
				logger.Error("seed Socket Mode app installation", "error", err)
				os.Exit(1)
			}
		}
		appTokenStore, ok := runtime.TokenStore.(auth.AppTokenStore)
		if !ok {
			logger.Error("storage backend cannot authenticate Socket Mode app tokens")
			os.Exit(1)
		}
		socketModeAuth, err = auth.NewAppStored(appTokenStore)
		if err != nil {
			logger.Error("configure Socket Mode authenticator", "error", err)
			os.Exit(1)
		}
		if err := seedDevelopmentCredentials(context.Background(), runtime, *apiToken, *sessionToken, time.Now().UTC()); err != nil {
			logger.Error("seed development credentials", "error", err)
			os.Exit(1)
		}
		authenticator, err = auth.NewStored(runtime.TokenStore)
		if err != nil {
			logger.Error("configure stored authenticator", "error", err)
			os.Exit(1)
		}
		webAuthenticator, err = auth.NewBrowser(runtime.SessionStore)
		if err != nil {
			logger.Error("configure browser authenticator", "error", err)
			os.Exit(1)
		}
		sessionRevoker = runtime.SessionRevoker
		defer func() {
			if err := runtime.Closer.Close(); err != nil {
				logger.Error("close local chat", "error", err)
			}
		}()
	case "grpc":
		if *chatAddress == "" || *chatCA == "" || *chatServerName == "" || *chatClientCert == "" || *chatClientKey == "" {
			logger.Error("grpc chat requires address, server CA/name, and client certificate/key")
			os.Exit(2)
		}
		// bootstrapAdminEmail belongs in this list: grpc composition never calls
		// localchat.Open, so the value was accepted and silently dropped, which
		// made terraform/ecs-runtime's required bootstrap_admin_email inert and
		// left administrator sign-in unreachable. cmd/chatd owns the store in this
		// composition and takes the flag instead.
		if *storeName != "" || resolvedDSN != "" || *dqliteDirectory != "" || *dqliteAddress != "" || *dqliteCluster != "" || *dqliteDatabase != "" || *blobDirectory != "" || *blobS3Bucket != "" || *blobS3Prefix != "" || strings.TrimSpace(*bootstrapAdminEmail) != "" {
			logger.Error("local storage settings supplied for grpc composition", "hint", "-bootstrap-admin-email belongs on sameoldchat-chatd in grpc composition")
			os.Exit(2)
		}
		caPEM, err := os.ReadFile(*chatCA)
		if err != nil {
			logger.Error("read chat gRPC CA", "error", err)
			os.Exit(1)
		}
		rootCAs := x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(caPEM) {
			logger.Error("chat gRPC CA contains no certificates")
			os.Exit(1)
		}
		clientCertificate, err := tls.LoadX509KeyPair(*chatClientCert, *chatClientKey)
		if err != nil {
			logger.Error("load chat gRPC client certificate", "error", err)
			os.Exit(1)
		}
		transportCredentials := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{clientCertificate}, RootCAs: rootCAs, ServerName: *chatServerName, MinVersion: tls.VersionTLS13})
		// The transport package owns the options both peers must agree on: the
		// message bounds, the keepalive that detects a half-open connection, the
		// client-side instrumentation, and the trace propagation that keeps a
		// trace from breaking at the seam. Dialing with transport credentials
		// alone left this process disagreeing with cmd/chatd about which payloads
		// are acceptable and produced no client-side measurement at all.
		//
		// grpc.NewClient replaces the deprecated grpc.DialContext/grpc.WithBlock
		// pair. It does not block, so a chat process that is still starting no
		// longer prevents this process from starting; Connect starts the attempt
		// immediately and GET /readyz reports the seam until it succeeds.
		connection, err := grpc.NewClient(*chatAddress, append(chatgrpc.DialOptions(chatgrpc.Observer{Metrics: metrics, Logger: logger}), grpc.WithTransportCredentials(transportCredentials))...)
		if err != nil {
			logger.Error("connect chat gRPC", "error", err)
			os.Exit(1)
		}
		connection.Connect()
		remote, tokenStore, sessionStore, remoteSessionRevoker, err := generated.ProvideChatServiceRemote(connection)
		if err != nil {
			_ = connection.Close()
			logger.Error("create chat gRPC client", "error", err)
			os.Exit(1)
		}
		chatService = remote
		authenticator, err = auth.NewStored(tokenStore)
		if err != nil {
			logger.Error("configure stored authenticator", "error", err)
			os.Exit(1)
		}
		webAuthenticator, err = auth.NewBrowser(sessionStore)
		if err != nil {
			logger.Error("configure stored browser authenticator", "error", err)
			os.Exit(1)
		}
		sessionRevoker = remoteSessionRevoker
		defer connection.Close()
	}
	if socketModeStore == nil {
		if value, ok := chatService.(socketmode.ConnectionStore); ok {
			socketModeStore = value
		}
		if value, ok := chatService.(auth.AppTokenStore); ok {
			configured, configureErr := auth.NewAppStored(value)
			if configureErr != nil {
				logger.Error("configure distributed Socket Mode authenticator", "error", configureErr)
				os.Exit(1)
			}
			socketModeAuth = configured
		}
	}
	// A registered /socket-mode route with no authenticator answers
	// apps.connections.open with 503 socket_mode_unavailable for the lifetime of
	// the process, reporting a permanently broken feature as a transient outage.
	// Incomplete configuration must fail at startup instead.
	if socketModeStore != nil && socketModeAuth == nil {
		logger.Error("Socket Mode connection store has no app-token authenticator", "mode", *chatMode)
		os.Exit(1)
	}
	slackHandler, err := slack.NewHandler(chatService, authenticator)
	if err != nil {
		logger.Error("configure Slack API", "error", err)
		os.Exit(1)
	}
	slackHandler.Register(mux)
	if socketModeStore != nil {
		slackHandler.ConfigureSocketMode(socketmode.Service{Store: socketModeStore, Host: *socketHost, TLS: *socketTLS}, socketModeAuth)
		mux.Handle("/socket-mode", socketmode.Handler{Store: socketModeStore, Events: chatService, Cursors: chatService, Responses: socketmode.ResponseRecorder{Store: chatService}})
	}
	webHandler, err := web.NewHandler(chatService, webAuthenticator, sessionRevoker, "Cdev", *authCookieDomain)
	if err != nil {
		logger.Error("configure web", "error", err)
		os.Exit(1)
	}
	providerCredentials := *googleClientID != "" || *googleClientSecret != "" || *githubClientID != "" || *githubClientSecret != "" || *entraClientID != "" || *entraClientSecret != "" || *oidcIssuer != "" || *oidcClientID != "" || *oidcClientSecret != ""
	if providerCredentials {
		if *authWorkspace == "" || *authLookupUser == "" || *authPublicURL == "" || *authStateKeyHex == "" {
			logger.Error("external authorization requires workspace, lookup user, public URL, and state key")
			os.Exit(2)
		}
		stateKey, decodeErr := hex.DecodeString(*authStateKeyHex)
		if decodeErr != nil || len(stateKey) < 32 {
			// The decode error is deliberately not logged: encoding/hex reports
			// the offending byte of the key ("invalid byte: U+007A 'z'"), which
			// writes part of a secret into the process log.
			logger.Error("authorization state key must contain at least 32 bytes of hex")
			os.Exit(2)
		}
		providers := make([]web.ProviderConfig, 0, 4)
		if (*googleClientID == "") != (*googleClientSecret == "") {
			logger.Error("Google client ID and secret must be supplied together")
			os.Exit(2)
		}
		if *googleClientID != "" {
			providers = append(providers, web.ProviderConfig{Name: "google", ClientID: *googleClientID, ClientSecret: *googleClientSecret, AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "email", "profile"}})
		}
		if (*githubClientID == "") != (*githubClientSecret == "") {
			logger.Error("GitHub client ID and secret must be supplied together")
			os.Exit(2)
		}
		if *githubClientID != "" {
			providers = append(providers, web.ProviderConfig{Name: "github", ClientID: *githubClientID, ClientSecret: *githubClientSecret, AuthorizeURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", UserInfoURL: "https://api.github.com/user", EmailURL: "https://api.github.com/user/emails", Scopes: []string{"read:user", "user:email"}})
		}
		// The tenant is validated by identityConfig.validate, which runs before any
		// store is opened. Requiring a non-empty tenant here as well made a
		// deployment that configures only Google fail with an Entra message once
		// the tenant default stopped being "common".
		if (*entraClientID == "") != (*entraClientSecret == "") {
			logger.Error("Microsoft Entra client ID and secret must be supplied together")
			os.Exit(2)
		}
		if *entraClientID != "" {
			providers = append(providers, web.ProviderConfig{Name: "entra", ClientID: *entraClientID, ClientSecret: *entraClientSecret, AuthorizeURL: "https://login.microsoftonline.com/" + *entraTenant + "/oauth2/v2.0/authorize", TokenURL: "https://login.microsoftonline.com/" + *entraTenant + "/oauth2/v2.0/token", UserInfoURL: "https://graph.microsoft.com/oidc/userinfo", Scopes: []string{"openid", "profile", "email", "offline_access"}})
		}
		if (*oidcIssuer == "") != (*oidcClientID == "") || (*oidcIssuer == "") != (*oidcClientSecret == "") {
			logger.Error("OpenID Connect issuer, client ID, and client secret must be supplied together")
			os.Exit(2)
		}
		if *oidcIssuer != "" {
			discoveryContext, cancelDiscovery := context.WithTimeout(context.Background(), 10*time.Second)
			oidcProvider, discoveryErr := web.DiscoverOpenIDConnectProvider(discoveryContext, &http.Client{Timeout: 10 * time.Second}, *oidcIssuer, *oidcClientID, *oidcClientSecret)
			cancelDiscovery()
			if discoveryErr != nil {
				logger.Error("discover OpenID Connect provider", "error", discoveryErr)
				os.Exit(1)
			}
			providers = append(providers, oidcProvider)
		}
		loginHandler, loginErr := web.NewLoginHandler(chatService, domain.WorkspaceID(*authWorkspace), domain.UserID(*authLookupUser), *authPublicURL, *authCookieDomain, stateKey, providers)
		if loginErr != nil {
			logger.Error("configure external authorization", "error", loginErr)
			os.Exit(1)
		}
		webHandler.Login = &loginHandler
		// Release identity is required whenever any external provider is
		// configured, not only for OpenID Connect. Nesting this under
		// *oidcIssuer != "" meant a deployment with, say, only GitHub sign-in
		// accepted -release-revision and never exposed it, so
		// docs/deployment.md's promise that every image exposes its immutable
		// commit did not hold and release identity could not be verified.
		if releaseErr := webHandler.SetReleaseRevision(*release); releaseErr != nil {
			logger.Error("configure immutable release identity", "error", releaseErr, "revision", *release)
			os.Exit(2)
		}
	}
	webHandler.Register(mux)
	sseHandler, err := realtime.NewHandler(chatService, "Tdev", webAuthenticator)
	if err != nil {
		logger.Error("configure realtime", "error", err)
		os.Exit(1)
	}
	sseHandler.Register(mux)
	rtmHandler, err := realtime.NewRTMHandler(chatService, "Tdev", chatService, chatService)
	if err != nil {
		logger.Error("configure RTM", "error", err)
		os.Exit(1)
	}
	rtmHandler.RegisterRTM(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", readinessHandler(chatService))
	mux.HandleFunc("GET /{$}", applicationRootHandler)

	// ReadHeaderTimeout bounds the slow-header (Slowloris) window that previously
	// had no limit at all, and IdleTimeout reaps idle keep-alive connections.
	// ReadTimeout and WriteTimeout are deliberately absent: this mux also serves
	// the server-sent event stream at /events, the RTM socket at /rtm, the Socket
	// Mode socket at /socket-mode, and blob uploads up to -blob-max-bytes, all of
	// which a whole-request deadline would sever mid-stream. Per-response
	// deadlines belong in those handlers, not on the listener.
	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Metrics are published on their own listener, never on the listener that
	// serves users: the series describe request volumes and gRPC status codes of
	// the whole deployment, which is operator data.
	if metricsServer := metricsListener(*metricsListen, metrics); metricsServer != nil {
		logger.Info("metrics listening", "addr", metricsServer.Addr)
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener stopped", "error", err)
			}
		}()
		defer func() {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsServer.Shutdown(shutdownContext); err != nil {
				logger.Error("metrics listener drain failed", "error", err)
			}
		}()
	}
	logger.Info("server listening", "addr", *addr)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.ListenAndServe() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-serveErrors:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case sig := <-signals:
		logger.Info("server draining", "signal", sig.String())
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("server drain failed", "error", err)
			os.Exit(1)
		}
	}
}

// entraTenantDefault is empty on purpose.
//
// The default used to be "common", the Microsoft Entra multi-tenant endpoint.
// That endpoint accepts a sign-in from any directory, including one the attacker
// created minutes earlier, and the identity it returns carries whatever email
// address that directory claims. A deployment that only sets an application ID
// and secret therefore accepted a victim's email address from an attacker's
// tenant (nOAuth). internal/web now requires a verified email before it links an
// external identity to a local account, but the default must not invite the
// attack in the first place: an operator has to name the directory it trusts.
const entraTenantDefault = ""

// devSessionLifetime bounds the seeded development browser session. It used to
// last a day, which is a long time for a credential that is a plain flag value
// in a process command line and is shared by everyone who knows it.
const devSessionLifetime = time.Hour

// multiTenantEntraTenants are the Entra endpoints that accept a sign-in from a
// directory this deployment does not control. Each of them makes the returned
// identity attacker-controlled, so they are rejected rather than warned about.
var multiTenantEntraTenants = map[string]bool{"common": true, "organizations": true, "consumers": true}

// identityConfig is the identity configuration of one server process. It is
// validated before any store is opened or any listener is bound, so an
// impossible or unsafe combination fails at startup rather than at the first
// sign-in attempt.
type identityConfig struct {
	apiToken       string
	sessionToken   string
	oidcIssuer     string
	googleClientID string
	githubClientID string
	entraClientID  string
	entraTenant    string
}

// configuredProviders names the real identity providers this process would
// serve. It is the set that must not coexist with a static session token.
func (c identityConfig) configuredProviders() []string {
	providers := make([]string, 0, 4)
	for _, candidate := range []struct{ flag, value string }{
		{flag: "-oidc-issuer", value: c.oidcIssuer},
		{flag: "-google-client-id", value: c.googleClientID},
		{flag: "-github-client-id", value: c.githubClientID},
		{flag: "-entra-client-id", value: c.entraClientID},
	} {
		if strings.TrimSpace(candidate.value) != "" {
			providers = append(providers, candidate.flag)
		}
	}
	return providers
}

func (c identityConfig) validate() error {
	if strings.TrimSpace(c.apiToken) == "" {
		return errors.New("-api-token is required")
	}
	if strings.TrimSpace(c.entraClientID) != "" {
		tenant := strings.TrimSpace(c.entraTenant)
		if tenant == "" {
			return errors.New("-entra-tenant is required with -entra-client-id: it names the Microsoft Entra directory whose sign-ins this deployment accepts")
		}
		if multiTenantEntraTenants[strings.ToLower(tenant)] {
			return fmt.Errorf("-entra-tenant %q is a multi-tenant endpoint: any directory, including one an attacker owns, could sign in and present any email address; name this deployment's directory instead", tenant)
		}
	}
	if strings.TrimSpace(c.sessionToken) != "" {
		if providers := c.configuredProviders(); len(providers) != 0 {
			return fmt.Errorf("-session-token is a static browser session shared by every holder and cannot be combined with the configured identity provider (%s); remove -session-token", strings.Join(providers, ", "))
		}
	}
	return nil
}

// developmentScopes are the scopes the seeded API token and the seeded browser
// session receive.
//
// They used to receive auth.AllScopes(), which includes admin and every
// admin.* scope, so the static development credentials held the workspace
// control plane. The member role is the authority a signed-in user has by
// default, and auth.ScopesForWorkspaceRole is the only place that mapping
// lives, so the two cannot drift.
func developmentScopes() ([]string, error) {
	scopes, err := auth.ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		return nil, err
	}
	return scopes.Values(), nil
}

// seedDevelopmentCredentials seeds the development API token and, only when one
// was supplied, the static browser session. Neither carries control-plane
// authority and the session expires within devSessionLifetime.
func seedDevelopmentCredentials(ctx context.Context, runtime localchat.Runtime, apiToken, sessionToken string, now time.Time) error {
	scopes, err := developmentScopes()
	if err != nil {
		return err
	}
	if err := runtime.TokenSeeder.SeedToken(ctx, apiToken, domain.TokenRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: scopes}); err != nil {
		return fmt.Errorf("seed API token: %w", err)
	}
	if strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	if err := runtime.SessionSeeder.SeedSession(ctx, sessionToken, domain.SessionRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: scopes, ExpiresAt: now.Add(devSessionLifetime)}); err != nil {
		return fmt.Errorf("seed browser session: %w", err)
	}
	return nil
}

// metricsListener builds the operator-only metrics server, or nil when no
// address was configured. ReadHeaderTimeout and IdleTimeout bound what an
// unauthenticated client on that address can hold open.
func metricsListener(addr string, metrics *observability.Registry) *http.Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
}

func releaseRevisionDefault() string {
	if configured := strings.TrimSpace(os.Getenv("SAMEOLDCHAT_RELEASE_REVISION")); configured != "" {
		return configured
	}
	return releaseRevision
}

// socketHostDefault derives the Socket Mode connection host from the listen
// address. It was hardcoded to "localhost:8080" and ignored -addr, so a server
// started on another port or behind a real hostname handed every Socket Mode
// client an unreachable ws:// URL from apps.connections.open.
func socketHostDefault(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "localhost:8080"
	}
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

func databaseDSNDefault() string {
	return os.Getenv("SAMEOLDCHAT_DATABASE_URL")
}

func resolveDatabaseDSN(chatMode, explicitDSN string) (string, error) {
	explicitDSN = strings.TrimSpace(explicitDSN)
	switch chatMode {
	case "local":
		if explicitDSN != "" {
			return explicitDSN, nil
		}
		return databaseDSNDefault(), nil
	case "grpc":
		if explicitDSN != "" {
			return "", errors.New("distributed chat mode cannot use a local database DSN")
		}
		return "", nil
	default:
		return "", fmt.Errorf("unsupported chat mode %q", chatMode)
	}
}

func applicationRootHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func readinessHandler(chatService chatapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestContext, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if _, err := chatService.Conversations(requestContext, "Tdev", "Udev", domain.ConversationListRequest{Limit: 1}); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ready\n"))
	}
}
