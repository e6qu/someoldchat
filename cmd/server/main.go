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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/api/slack"
	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/clientaddr"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/generated"
	"github.com/sameoldchat/sameoldchat/internal/huddlesfu"
	"github.com/sameoldchat/sameoldchat/internal/l10n"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatgrpc "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc"
	"github.com/sameoldchat/sameoldchat/internal/observability"
	"github.com/sameoldchat/sameoldchat/internal/realtime"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// exitConfiguration and exitRuntime separate "the operator gave us something
// impossible" from "something failed while running", the same way every other
// binary in this repository does. Without the distinction an orchestrator with a
// restart-on-runtime-failure-only policy restart-looped forever on a mistyped
// certificate path.
//
// Everything this process decides before it opens a resource is a configuration
// failure, including the handler constructors: they only ever refuse an
// operator-supplied value or a composition this binary cannot serve, and
// classifying them as runtime failures is what made `-auth-cookie-domain "not a
// domain!!"` exit 1 and restart-loop. cmd/chatd was corrected for exactly this
// and cmd/server was not. Only a failure after the listener is up is exitRuntime.
const (
	exitConfiguration = 2
	exitRuntime       = 1
)

// developmentReleaseRevision is the release identity of a binary built without
// `-ldflags -X main.releaseRevision=<commit>`. It is deliberately not an
// immutable commit, so it is the one value the release-identity check skips.
const developmentReleaseRevision = "development"

var releaseRevision = developmentReleaseRevision

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Every teardown runs through run's defers; main is the only place that
	// exits. Sixteen os.Exit calls used to follow `defer runtime.Closer.Close()`,
	// so a refused invocation left the store open, the write-ahead log created,
	// and any dqlite cluster joined — exactly what cmd/blobgc's own comment says
	// must never happen.
	if code := run(context.Background(), logger, os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, logger *slog.Logger, args []string) int {
	huddleUDPPortDefault, err := envInt("SAMEOLDCHAT_HUDDLE_UDP_PORT")
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return exitConfiguration
	}
	flags := flag.NewFlagSet("sameoldchat", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	addr := flags.String("addr", ":8080", "HTTP listen address")
	// -chat-mode, -store, -auth-workspace, -auth-lookup-user, and -auth-public-url
	// take an environment default like their neighbours because
	// terraform/ecs-runtime configures a task purely from environment variables.
	// Without these the module's documented `environment` output produced a task
	// that exited 2 at "invalid chat composition", so following the module README
	// yielded a crash-looping service.
	chatMode := flags.String("chat-mode", os.Getenv("SAMEOLDCHAT_CHAT_MODE"), "chat composition: local or grpc (required)")
	storeName := flags.String("store", os.Getenv("SAMEOLDCHAT_STORE"), "local storage backend: memory, sqlite, postgresql, or dqlite (required for -chat-mode=local)")
	// The default is deliberately empty rather than os.Getenv: the environment
	// variable applies only to local composition (resolveDatabaseDSN), and reading
	// it as the flag default would leak it into grpc composition, where an
	// explicit DSN is rejected. The help text names it so `-h` still discloses it.
	dsn := flags.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql storage; defaults to SAMEOLDCHAT_DATABASE_URL in local composition only")
	dqliteDirectory := flags.String("dqlite-directory", "", "dqlite state directory; required for local dqlite storage")
	dqliteAddress := flags.String("dqlite-address", "", "dqlite node address; required for local dqlite storage")
	dqliteCluster := flags.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flags.String("dqlite-database", "", "dqlite database name; required for local dqlite storage")
	blobDirectory := flags.String("blob-dir", "", "external blob directory for file storage")
	blobS3Bucket := flags.String("blob-s3-bucket", os.Getenv("SAMEOLDCHAT_BLOB_S3_BUCKET"), "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flags.String("blob-s3-prefix", os.Getenv("SAMEOLDCHAT_BLOB_S3_PREFIX"), "Amazon Simple Storage Service key prefix for file storage")
	blobMaxBytes := flags.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	// Huddle media reachability. Empty public IP and zero port keep host
	// candidates and ephemeral ports, which suffice on the same network; a
	// deployment behind NAT sets the advertised address and a single UDP port to
	// expose, and hands the browser the ICE servers (STUN/TURN) to reach it.
	huddlePublicIP := flags.String("huddle-public-ip", os.Getenv("SAMEOLDCHAT_HUDDLE_PUBLIC_IP"), "public IP the huddle SFU advertises (NAT 1-to-1 mapping of host candidates); empty leaves host candidates unmapped")
	huddleUDPPort := flags.Int("huddle-udp-port", huddleUDPPortDefault, "single UDP port for all huddle media; 0 uses ephemeral ports")
	huddleICEServers := flags.String("huddle-ice-servers", os.Getenv("SAMEOLDCHAT_HUDDLE_ICE_SERVERS"), "JSON array of ICE servers (STUN/TURN) browsers use to reach the huddle SFU; empty uses none")
	chatAddress := flags.String("chat-address", "", "distributed chat gRPC address; required for -chat-mode=grpc")
	chatCA := flags.String("chat-ca", "", "CA certificate for distributed chat gRPC")
	chatServerName := flags.String("chat-server-name", "", "TLS server name for distributed chat gRPC")
	chatClientCert := flags.String("chat-client-cert", "", "client certificate for distributed chat gRPC")
	chatClientKey := flags.String("chat-client-key", "", "client private key for distributed chat gRPC")
	apiToken := flags.String("api-token", os.Getenv("SAMEOLDCHAT_API_TOKEN"), "API bearer token (required)")
	trustedProxies := flags.String("trusted-proxies", os.Getenv("SAMEOLDCHAT_TRUSTED_PROXIES"), "comma-separated addresses or CIDR ranges of the reverse proxies whose X-Forwarded-For names the client; empty trusts none, so every request is keyed by its peer")
	apiRateLimit := flags.Bool("api-rate-limit", true, "enforce the Web API rate-limiting contract (429 + Retry-After); qualification harnesses that seed fixtures at superhuman rates disable it")
	// -session-token is optional. It seeds one static browser session that every
	// visitor holding the value shares, which is a development convenience and
	// never an identity: startupConfig.resolve rejects it as soon as a real
	// identity provider is configured, because a shared bearer session bypasses
	// every provider check, every revocation, and every workspace role.
	sessionToken := flags.String("session-token", os.Getenv("SAMEOLDCHAT_SESSION_TOKEN"), "static development browser session token; rejected when an identity provider is configured")
	// -session-admin exists because the administration journeys cannot be
	// qualified without an administrator, and the static development session is
	// a plain member by design: a token every holder shares must not carry
	// control-plane authority by default. Making it opt-in, refusing it
	// wherever a real identity exists, and announcing it at startup keeps that
	// default intact while giving a development deployment a way to reach its
	// own administration.
	// -peer-session-token seeds a second static session, for a second plain
	// member, because a journey between two people — a direct message, an
	// invitation, a huddle someone else ends — cannot be qualified by one. It
	// carries the same refusals as -session-token and never administrator
	// scopes.
	peerSessionToken := flags.String("peer-session-token", os.Getenv("SAMEOLDCHAT_PEER_SESSION_TOKEN"), "static development browser session for a second, plain member of the development workspace; local composition only, and rejected when an identity provider is configured")
	sessionAdmin := flags.Bool("session-admin", os.Getenv("SAMEOLDCHAT_SESSION_ADMIN") == "1", "grant the static development browser session workspace-administrator scopes; requires -session-token and is rejected when an identity provider is configured")
	metricsListen := flags.String("metrics-listen", os.Getenv("SAMEOLDCHAT_METRICS_LISTEN"), "operator-only listen address publishing /metrics; empty serves no metrics endpoint")
	monitoringToken := flags.String("monitoring-token", os.Getenv("SAMEOLDCHAT_MONITORING_TOKEN"), "deployment bearer token publishing /monitoring/observation; empty disables authenticated access")
	authWorkspace := flags.String("auth-workspace", os.Getenv("SAMEOLDCHAT_AUTH_WORKSPACE"), "workspace for external authorization (required when enabled)")
	authLookupUser := flags.String("auth-lookup-user", os.Getenv("SAMEOLDCHAT_AUTH_LOOKUP_USER"), "existing user used to authorize external identity lookup (required when enabled)")
	authPublicURL := flags.String("auth-public-url", os.Getenv("SAMEOLDCHAT_AUTH_PUBLIC_URL"), "public URL clients reach this deployment on: authorization callbacks and every absolute URL the Slack Web API emits (HTTPS, or an explicit loopback URL for development)")
	authCookieDomain := flags.String("auth-cookie-domain", os.Getenv("SAMEOLDCHAT_AUTH_COOKIE_DOMAIN"), "optional parent DNS domain for SameOldChat session cookies")
	authStateKeyHex := flags.String("auth-state-key-hex", os.Getenv("SAMEOLDCHAT_AUTH_STATE_KEY_HEX"), "HMAC key for authorization state, at least 32 bytes of hex")
	appCredentialKeyHex := flags.String("app-credential-key-hex", os.Getenv("SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX"), "AES-256 key used to encrypt application signing credentials")
	bootstrapAdminEmail := flags.String("bootstrap-admin-email", os.Getenv("SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL"), "email address of the initial local workspace administrator")
	appToken := flags.String("app-token", os.Getenv("SAMEOLDCHAT_APP_TOKEN"), "Socket Mode app-level token")
	appID := flags.String("app-id", os.Getenv("SAMEOLDCHAT_APP_ID"), "Socket Mode app identifier")
	socketHost := flags.String("socket-host", os.Getenv("SAMEOLDCHAT_SOCKET_HOST"), "public host:port Socket Mode connection URLs name; empty follows the host each client called apps.connections.open on")
	socketTLS := flags.Bool("socket-tls", os.Getenv("SAMEOLDCHAT_SOCKET_TLS") == "1", "hand out wss:// Socket Mode URLs; with -socket-host it selects the scheme, without it it forces wss:// behind a TLS proxy that sends no X-Forwarded-Proto")
	googleClientID := flags.String("google-client-id", "", "Google OAuth client ID")
	googleClientSecret := flags.String("google-client-secret", "", "Google OAuth client secret")
	githubClientID := flags.String("github-client-id", "", "GitHub OAuth client ID")
	githubClientSecret := flags.String("github-client-secret", "", "GitHub OAuth client secret")
	entraClientID := flags.String("entra-client-id", "", "Microsoft Entra application client ID")
	entraClientSecret := flags.String("entra-client-secret", "", "Microsoft Entra application client secret")
	entraTenant := flags.String("entra-tenant", entraTenantDefault, "Microsoft Entra directory (tenant) identifier; multi-tenant endpoints are rejected")
	oidcIssuer := flags.String("oidc-issuer", os.Getenv("SAMEOLDCHAT_OIDC_ISSUER"), "OpenID Connect issuer URL")
	oidcClientID := flags.String("oidc-client-id", os.Getenv("SAMEOLDCHAT_OIDC_CLIENT_ID"), "OpenID Connect client ID")
	oidcClientSecret := flags.String("oidc-client-secret", os.Getenv("SAMEOLDCHAT_OIDC_CLIENT_SECRET"), "OpenID Connect client secret")
	release := flags.String("release-revision", releaseRevisionDefault(), "immutable application commit or image digest exposed to Shauth validation")
	// scripts/check-terraform-module-startup.sh starts this binary with exactly
	// the environment and secret keys terraform/ecs-runtime exports and asserts
	// it is accepted. terraform/ecs-runtime shipped an `environment` output and a
	// `secrets` output whose combination this binary refuses, so the module was
	// dead on arrival with every gate green; nothing could see it because no gate
	// ever ran the binary against a module's own output.
	checkConfiguration := flags.Bool("check-config", false, "validate the configuration and exit 0 without opening a store, dialing a peer, or binding a listener")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitConfiguration
	}

	settings := startupConfig{
		addr: *addr, trustedProxies: *trustedProxies, metricsListen: *metricsListen, monitoringToken: *monitoringToken, authCookieDomain: *authCookieDomain, releaseRevisionFlag: *release,
		chatMode: *chatMode, storeName: *storeName, databaseDSN: *dsn,
		dqliteDirectory: *dqliteDirectory, dqliteAddress: *dqliteAddress, dqliteCluster: *dqliteCluster, dqliteDatabase: *dqliteDatabase,
		blobDirectory: *blobDirectory, blobS3Bucket: *blobS3Bucket, blobS3Prefix: *blobS3Prefix,
		chatAddress: *chatAddress, chatCA: *chatCA, chatServerName: *chatServerName, chatClientCert: *chatClientCert, chatClientKey: *chatClientKey,
		apiToken: *apiToken, sessionToken: *sessionToken, peerSessionToken: *peerSessionToken, sessionAdmin: *sessionAdmin,
		authWorkspace: *authWorkspace, authLookupUser: *authLookupUser, authPublicURL: *authPublicURL, authStateKeyHex: *authStateKeyHex, appCredentialKeyHex: *appCredentialKeyHex,
		bootstrapAdminEmail: *bootstrapAdminEmail, appToken: *appToken, appID: *appID, socketHost: *socketHost,
		googleClientID: *googleClientID, googleClientSecret: *googleClientSecret,
		githubClientID: *githubClientID, githubClientSecret: *githubClientSecret,
		entraClientID: *entraClientID, entraClientSecret: *entraClientSecret, entraTenant: *entraTenant,
		oidcIssuer: *oidcIssuer, oidcClientID: *oidcClientID, oidcClientSecret: *oidcClientSecret,
	}
	resolved, err := settings.resolve()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return exitConfiguration
	}
	// A broken message catalog stops the deployment rather than a page.
	if err := l10n.Load(); err != nil {
		logger.Error("load message catalogs", "error", err)
		return exitConfiguration
	}
	if *checkConfiguration {
		logger.Info("configuration accepted", "chat-mode", settings.chatMode, "store", settings.storeName, "external-authorization", resolved.externalAuthorization)
		return 0
	}

	// Signals are established before the first durable resource is opened. They
	// used to be registered after ListenAndServe had already been started, and
	// therefore after localchat.Open and a ten-second OpenID Connect discovery,
	// so a SIGTERM during startup took the default disposition and killed the
	// process with the store open.
	applicationContext, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	mux := http.NewServeMux()
	// The registry is always built because the distributed chat client records
	// into it through the transport package's dial options; it is published only
	// on the operator listener, never on the listener that serves users.
	metrics := observability.NewRegistry()
	var chatService chatapi.Service
	var authenticator auth.Authenticator
	var webAuthenticator auth.Authenticator
	var sessionRevoker auth.SessionRevoker
	// The huddle SFU forwards media inside this process. It needs the event log
	// to push its offers and candidates to browsers, so it is only wired where
	// this process owns the store (local composition); elsewhere huddle media is
	// reported unavailable rather than silently broken. huddleStore carries the
	// broadcast presence events alongside it.
	var sfuManager *huddlesfu.Manager
	var huddleStore store.Store
	var socketModeAuth auth.Authenticator
	switch settings.chatMode {
	case "local":
		cluster, err := localchat.ParseCluster(settings.dqliteCluster)
		if err != nil {
			logger.Error("parse dqlite cluster", "error", err)
			return exitConfiguration
		}
		if strings.TrimSpace(settings.authPublicURL) == "" {
			// The Web API falls back to each request's origin; an event has no
			// request, so its URLs stay origin-relative. See docs/operations.md.
			logger.Warn("no -auth-public-url: URLs in event payloads are origin-relative")
		}
		runtime, err := localchat.Open(applicationContext, localchat.Config{Backend: localchat.Backend(settings.storeName), DSN: resolved.databaseDSN, DqliteDirectory: settings.dqliteDirectory, DqliteAddress: settings.dqliteAddress, DqliteCluster: cluster, DqliteDatabase: settings.dqliteDatabase, BlobDirectory: settings.blobDirectory, BlobS3Bucket: settings.blobS3Bucket, BlobS3Prefix: settings.blobS3Prefix, BlobMaxBytes: *blobMaxBytes, BootstrapAdminEmail: settings.bootstrapAdminEmail, DevelopmentPeer: resolved.peerSessionToken != "", AppCredentialKey: resolved.appCredentialKey, PublicURL: slackobject.Origin(settings.authPublicURL)})
		if err != nil {
			return startupFailure(applicationContext, logger, "open local chat", err)
		}
		defer func() {
			if err := runtime.Closer.Close(); err != nil {
				logger.Error("close local chat", "error", err)
			}
		}()
		chatService = runtime.Service
		manager, sfuErr := huddlesfu.NewManager(web.HuddleSignalEmitter{Store: runtime.Store}, huddlesfu.Config{PublicIP: *huddlePublicIP, UDPPort: *huddleUDPPort})
		if sfuErr != nil {
			return startupFailure(applicationContext, logger, "configure huddle SFU", sfuErr)
		}
		sfuManager = manager
		defer func() {
			if err := sfuManager.Close(); err != nil {
				logger.Error("close huddle SFU", "error", err)
			}
		}()
		huddleStore = runtime.Store
		if settings.appToken != "" {
			appTokenStore, ok := runtime.TokenStore.(interface {
				SeedAppToken(context.Context, string, domain.AppTokenRecord) error
				auth.AppTokenStore
			})
			if !ok {
				logger.Error("storage backend cannot seed Socket Mode app tokens", "store", settings.storeName)
				return exitConfiguration
			}
			if err := appTokenStore.SeedAppToken(applicationContext, settings.appToken, domain.AppTokenRecord{AppID: domain.AppID(settings.appID), Scopes: []string{string(auth.ScopeConnectionsWrite)}}); err != nil {
				return startupFailure(applicationContext, logger, "seed Socket Mode app token", err)
			}
			if err := runtime.Store.CreateAppInstallation(applicationContext, domain.AppInstallation{AppID: domain.AppID(settings.appID), WorkspaceID: domain.WorkspaceID(resolved.workspace), Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
				return startupFailure(applicationContext, logger, "seed Socket Mode app installation", err)
			}
		}
		appTokenStore, ok := runtime.TokenStore.(auth.AppTokenStore)
		if !ok {
			logger.Error("storage backend cannot authenticate Socket Mode app tokens", "store", settings.storeName)
			return exitConfiguration
		}
		socketModeAuth, err = auth.NewAppStored(appTokenStore)
		if err != nil {
			logger.Error("configure Socket Mode authenticator", "error", err)
			return exitConfiguration
		}
		if err := seedDevelopmentCredentials(applicationContext, runtime, resolved, logger, time.Now().UTC()); err != nil {
			return startupFailure(applicationContext, logger, "seed development credentials", err)
		}
		authenticator, err = auth.NewStored(runtime.TokenStore)
		if err != nil {
			logger.Error("configure stored authenticator", "error", err)
			return exitConfiguration
		}
		webAuthenticator, err = auth.NewBrowser(runtime.SessionStore)
		if err != nil {
			logger.Error("configure browser authenticator", "error", err)
			return exitConfiguration
		}
		sessionRevoker = runtime.SessionRevoker
	case "grpc":
		caPEM, err := os.ReadFile(settings.chatCA)
		if err != nil {
			logger.Error("read chat gRPC CA", "error", err)
			return exitConfiguration
		}
		rootCAs := x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(caPEM) {
			logger.Error("chat gRPC CA contains no certificates", "path", settings.chatCA)
			return exitConfiguration
		}
		clientCertificate, err := tls.LoadX509KeyPair(settings.chatClientCert, settings.chatClientKey)
		if err != nil {
			logger.Error("load chat gRPC client certificate", "error", err)
			return exitConfiguration
		}
		transportCredentials := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{clientCertificate}, RootCAs: rootCAs, ServerName: settings.chatServerName, MinVersion: tls.VersionTLS13})
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
		connection, err := grpc.NewClient(settings.chatAddress, append(chatgrpc.DialOptions(chatgrpc.Observer{Metrics: metrics, Logger: logger}), grpc.WithTransportCredentials(transportCredentials))...)
		if err != nil {
			logger.Error("connect chat gRPC", "error", err, "address", settings.chatAddress)
			return exitConfiguration
		}
		defer connection.Close()
		connection.Connect()
		remote, tokenStore, sessionStore, remoteSessionRevoker, err := generated.ProvideChatServiceRemote(connection)
		if err != nil {
			logger.Error("create chat gRPC client", "error", err)
			return exitConfiguration
		}
		chatService = remote
		authenticator, err = auth.NewStored(tokenStore)
		if err != nil {
			logger.Error("configure stored authenticator", "error", err)
			return exitConfiguration
		}
		webAuthenticator, err = auth.NewBrowser(sessionStore)
		if err != nil {
			logger.Error("configure stored browser authenticator", "error", err)
			return exitConfiguration
		}
		sessionRevoker = remoteSessionRevoker
	}
	if socketModeAuth == nil {
		// Distributed composition authenticates app-level tokens through the
		// generated chat boundary. Socket Mode is served in every composition,
		// so a chat client that cannot authenticate them is a configuration
		// fault, not a feature to drop silently.
		value, ok := chatService.(auth.AppTokenStore)
		if !ok {
			logger.Error("chat service cannot authenticate Socket Mode app tokens", "mode", settings.chatMode)
			return exitConfiguration
		}
		configured, configureErr := auth.NewAppStored(value)
		if configureErr != nil {
			logger.Error("configure distributed Socket Mode authenticator", "error", configureErr)
			return exitConfiguration
		}
		socketModeAuth = configured
	}
	// The Web API rate-limiting contract is production behavior: official
	// SDKs key their retry handling on 429 + Retry-After. Only qualification
	// harnesses, which seed fixtures at superhuman request rates, turn it off.
	var limiter *slack.RateLimiter
	if *apiRateLimit {
		limiter = newRateLimiter(settings, chatService, logger)
	}
	// The Web API, Socket Mode and RTM are mounted by the one composition the
	// SDK qualification fixture also uses, so qualification exercises this
	// wiring rather than a copy of it.
	if err := slack.Mount(mux, slack.Surface{
		Messages: chatService, Authenticator: authenticator, AppAuthenticator: socketModeAuth,
		SocketHost: resolved.socketHost, SocketTLS: *socketTLS, Limiter: limiter, Logger: logger,
		// The Web API emits absolute URLs — file downloads, the v2 upload URL —
		// and the deployment's public URL is the only trustworthy origin for
		// them behind a proxy. It is the same coordinate the web client builds on.
		PublicURL: settings.authPublicURL,
	}); err != nil {
		logger.Error("configure Slack API", "error", err)
		return exitConfiguration
	}
	webHandler, err := web.NewHandler(chatService, webAuthenticator, sessionRevoker, defaultConversation, *authCookieDomain)
	if err != nil {
		logger.Error("configure web", "error", err)
		return exitConfiguration
	}
	webHandler.SFU = sfuManager
	if huddleStore != nil {
		webHandler.HuddleStore = huddleStore
	}
	webHandler.HuddleICEServers = *huddleICEServers
	if strings.TrimSpace(settings.authPublicURL) != "" {
		if publicErr := webHandler.SetPublicURL(settings.authPublicURL); publicErr != nil {
			logger.Error("configure web public URL", "error", publicErr)
			return exitConfiguration
		}
	}
	// Release identity is exposed by every deployment, not only one that
	// configures an external provider. Nesting this under `providerCredentials`
	// meant a deployment with, say, only an API token accepted -release-revision
	// and never validated or exposed it, so docs/deployment.md's promise that
	// every image exposes its immutable commit did not hold. The built-in
	// development identity is the one value that is deliberately not a commit.
	if settings.releaseRevision(*release) != "" {
		if releaseErr := webHandler.SetReleaseRevision(*release); releaseErr != nil {
			logger.Error("configure immutable release identity", "error", releaseErr, "revision", *release)
			return exitConfiguration
		}
	}
	if resolved.externalAuthorization {
		providers := make([]web.ProviderConfig, 0, 4)
		if settings.googleClientID != "" {
			providers = append(providers, web.ProviderConfig{Name: "google", ClientID: settings.googleClientID, ClientSecret: settings.googleClientSecret, AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "email", "profile"}})
		}
		if settings.githubClientID != "" {
			providers = append(providers, web.ProviderConfig{Name: "github", ClientID: settings.githubClientID, ClientSecret: settings.githubClientSecret, AuthorizeURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", UserInfoURL: "https://api.github.com/user", EmailURL: "https://api.github.com/user/emails", Scopes: []string{"read:user", "user:email"}})
		}
		if settings.entraClientID != "" {
			providers = append(providers, web.ProviderConfig{Name: "entra", ClientID: settings.entraClientID, ClientSecret: settings.entraClientSecret, AuthorizeURL: "https://login.microsoftonline.com/" + settings.entraTenant + "/oauth2/v2.0/authorize", TokenURL: "https://login.microsoftonline.com/" + settings.entraTenant + "/oauth2/v2.0/token", UserInfoURL: "https://graph.microsoft.com/oidc/userinfo", Scopes: []string{"openid", "profile", "email", "offline_access"}})
		}
		if settings.oidcIssuer != "" {
			discoveryContext, cancelDiscovery := context.WithTimeout(applicationContext, 10*time.Second)
			oidcProvider, discoveryErr := web.DiscoverOpenIDConnectProvider(discoveryContext, &http.Client{Timeout: 10 * time.Second}, settings.oidcIssuer, settings.oidcClientID, settings.oidcClientSecret)
			cancelDiscovery()
			if discoveryErr != nil {
				return startupFailure(applicationContext, logger, "discover OpenID Connect provider", discoveryErr)
			}
			providers = append(providers, oidcProvider)
		}
		loginHandler, loginErr := web.NewLoginHandler(chatService, domain.WorkspaceID(resolved.workspace), domain.UserID(resolved.lookupUser), settings.authPublicURL, *authCookieDomain, resolved.authStateKey, providers)
		if loginErr != nil {
			logger.Error("configure external authorization", "error", loginErr)
			return exitConfiguration
		}
		webHandler.Login = &loginHandler
	}
	webHandler.Register(mux)
	// The realtime handlers used to be built with the literal workspace "Tdev",
	// so live delivery answered 403 for every deployment whose workspace is its
	// own. -auth-workspace already names that workspace; it is the same value
	// external authorization provisions users into.
	sseHandler, err := realtime.NewHandler(chatService, webAuthenticator, chatService, chatService)
	if err != nil {
		logger.Error("configure realtime", "error", err)
		return exitConfiguration
	}
	// Without an explicit logger every operator-visible realtime and Socket Mode
	// diagnostic — a slow-consumer drop, an inconclusive re-authorization, a
	// record with no event identifier, an envelope returned to the queue — goes
	// to slog.Default() instead of this process's configured handler.
	sseHandler.Logger = logger
	sseHandler.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", readinessHandler(chatService, logger, resolved.workspace, resolved.lookupUser))
	mux.Handle("GET /monitoring/observation", observability.ObservationHandler(resolved.monitoringTokenDigest, metrics, func(ctx context.Context) error {
		return readinessCheck(ctx, chatService, resolved.workspace, resolved.lookupUser)
	}, logger))
	mux.HandleFunc("GET /{$}", applicationRootHandler)

	// ReadHeaderTimeout bounds the slow-header (Slowloris) window that previously
	// had no limit at all, and IdleTimeout reaps idle keep-alive connections.
	// ReadTimeout and WriteTimeout are deliberately absent: this mux also serves
	// the server-sent event stream at /events, the RTM socket at /rtm, the Socket
	// Mode socket at /socket-mode, and blob uploads up to -blob-max-bytes, all of
	// which a whole-request deadline would sever mid-stream. Per-response
	// deadlines belong in those handlers, not on the listener.
	server := &http.Server{
		Addr:              settings.addr,
		Handler:           resolved.clientAddresses.Middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Every request context descends from the signal context, so a long-lived
		// stream sees its context end the moment SIGTERM arrives. Without this the
		// server-sent event stream at /events and the two sockets never observe
		// the shutdown, Shutdown waits the full ten seconds for handlers that will
		// never return on their own, and the process exits non-zero on every
		// ordinary deploy.
		BaseContext: func(net.Listener) context.Context { return applicationContext },
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
	logger.Info("server listening", "addr", settings.addr)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.ListenAndServe() }()
	select {
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			return exitRuntime
		}
	case <-applicationContext.Done():
		logger.Info("server draining")
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// A drain that exceeds its deadline is a warning followed by Close, not a
		// failure exit. Exiting here skipped the deferred store close and the
		// metrics drain, so a slow client turned an ordinary deploy into an
		// abandoned SQLite checkpoint and a non-zero exit; Close severs the
		// remaining connections and lets every defer run.
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Warn("server drain deadline exceeded; closing remaining connections", "error", err)
			if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("server close failed", "error", err)
			}
		}
	}
	return 0
}

// envInt reads a non-negative integer from an environment variable, zero when
// unset, so an operator can configure the huddle media port purely from the
// environment the way terraform/ecs-runtime does.
func envInt(name string) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative whole number", name)
	}
	return value, nil
}

// startupFailure classifies a startup step that failed while holding the signal
// context.
//
// A SIGTERM during a slow cold start cancels that context, so localchat.Open,
// the seeding calls and OpenID Connect discovery all fail — and reporting that
// as exitRuntime records a task failure for what is an ordinary stop. A rolling
// deploy that stops a task mid-startup then looks like a crash, which a
// deployment circuit breaker rolls the release back on. Being asked to stop is
// exit 0.

func startupFailure(ctx context.Context, logger *slog.Logger, stage string, err error) int {
	if ctx.Err() != nil {
		logger.Info("startup interrupted before completion", "stage", stage)
		return 0
	}
	logger.Error(stage, "error", err)
	return exitRuntime
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

// defaultWorkspace and defaultLookupUser are the seeded development identities.
// They are used only when -auth-workspace and -auth-lookup-user are not set,
// which is the single-tenant development profile that seeds them.
const (
	defaultWorkspace  = "Tdev"
	defaultLookupUser = "Udev"
)

// defaultConversation is the open channel internal/app/localchat seeds. It is
// the channel the web application opens by default and the one the seeded
// credentials are joined to, so the two cannot name different conversations.
const defaultConversation = "Cdev"

// multiTenantEntraTenants are the Entra endpoints that accept a sign-in from a
// directory this deployment does not control. Each of them makes the returned
// identity attacker-controlled, so they are rejected rather than warned about.
var multiTenantEntraTenants = map[string]bool{"common": true, "organizations": true, "consumers": true}

// startupConfig is the operator-supplied configuration of one server process,
// exactly as parsed from flags and the environment.
//
// resolve is the whole of the configuration contract: everything that can be
// decided without opening a store, dialing a peer, or binding a listener is
// decided there, once, before any of those happen. `-check-config` runs resolve
// and nothing else, so a deployment template can be verified against the binary
// it configures instead of being discovered wrong by a crash-looping task.
type startupConfig struct {
	addr           string
	trustedProxies string
	// metricsListen, authCookieDomain and releaseRevisionFlag are here because
	// the values were validated *after* resolve and therefore outside
	// -check-config: a bad cookie domain, a bad release revision and a
	// malformed dqlite cluster were each accepted by `-check-config` and then
	// refused by the real start, so the deployment gate that treats
	// -check-config as the authority could not see a crash-looping task.
	metricsListen       string
	monitoringToken     string
	authCookieDomain    string
	releaseRevisionFlag string
	chatMode            string
	storeName           string
	databaseDSN         string
	dqliteDirectory     string
	dqliteAddress       string
	dqliteCluster       string
	dqliteDatabase      string
	blobDirectory       string
	blobS3Bucket        string
	blobS3Prefix        string
	chatAddress         string
	chatCA              string
	chatServerName      string
	chatClientCert      string
	chatClientKey       string
	apiToken            string
	sessionToken        string
	peerSessionToken    string
	sessionAdmin        bool
	authWorkspace       string
	authLookupUser      string
	authPublicURL       string
	authStateKeyHex     string
	appCredentialKeyHex string
	bootstrapAdminEmail string
	appToken            string
	appID               string
	socketHost          string
	googleClientID      string
	googleClientSecret  string
	githubClientID      string
	githubClientSecret  string
	entraClientID       string
	entraClientSecret   string
	entraTenant         string
	oidcIssuer          string
	oidcClientID        string
	oidcClientSecret    string
}

// resolvedConfig is what a valid startupConfig yields: the derived values the
// rest of startup consumes, so no later step re-derives or re-validates them.
type resolvedConfig struct {
	databaseDSN           string
	socketHost            string
	workspace             string
	lookupUser            string
	apiToken              string
	sessionToken          string
	peerSessionToken      string
	sessionAdmin          bool
	sessionScopes         []string
	authStateKey          []byte
	appCredentialKey      []byte
	scopes                []string
	externalAuthorization bool
	monitoringTokenDigest *observability.TokenDigest
	clientAddresses       clientaddr.Resolver
}

// configuredProviders names the real identity providers this process would
// serve. It is the set that must not coexist with a static session token.
func (c startupConfig) configuredProviders() []string {
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

// releaseRevision reports the release identity that must be validated and
// exposed, or "" for the built-in development identity, which deliberately is
// not an immutable commit.
func (c startupConfig) releaseRevision(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == developmentReleaseRevision {
		return ""
	}
	return configured
}

func (c startupConfig) resolve() (resolvedConfig, error) {
	resolved := resolvedConfig{workspace: defaultWorkspace, lookupUser: defaultLookupUser}
	monitoringTokenDigest, err := observability.MonitoringTokenDigest(c.monitoringToken)
	if err != nil {
		return resolvedConfig{}, err
	}
	resolved.monitoringTokenDigest = monitoringTokenDigest
	resolved.clientAddresses, err = clientaddr.Parse(c.trustedProxies)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("-trusted-proxies: %w", err)
	}
	if c.chatMode != "local" && c.chatMode != "grpc" {
		return resolvedConfig{}, fmt.Errorf("invalid chat composition %q: -chat-mode must be local or grpc", c.chatMode)
	}
	// Four checks that a real start already performs and that used to sit
	// outside this function, so `-check-config` accepted a configuration the
	// process then refused. Each one is decidable without opening a store,
	// dialing a peer, or binding a listener, which is this function's whole
	// admission rule.
	if err := validateListenAddress("-addr", c.addr); err != nil {
		return resolvedConfig{}, err
	}
	if strings.TrimSpace(c.metricsListen) != "" {
		if err := validateListenAddress("-metrics-listen", c.metricsListen); err != nil {
			return resolvedConfig{}, err
		}
	}
	if err := auth.ValidateSessionCookieDomain(c.authCookieDomain); err != nil {
		return resolvedConfig{}, err
	}
	if revision := c.releaseRevision(c.releaseRevisionFlag); revision != "" {
		if err := web.ValidateReleaseRevision(revision); err != nil {
			return resolvedConfig{}, err
		}
	}
	if _, err := localchat.ParseCluster(c.dqliteCluster); err != nil {
		return resolvedConfig{}, err
	}
	databaseDSN, err := resolveDatabaseDSN(c.chatMode, c.databaseDSN)
	if err != nil {
		return resolvedConfig{}, err
	}
	resolved.databaseDSN = databaseDSN
	if strings.TrimSpace(c.apiToken) == "" {
		return resolvedConfig{}, errors.New("-api-token is required")
	}
	resolved.apiToken = c.apiToken
	resolved.sessionToken = c.sessionToken
	resolved.peerSessionToken = strings.TrimSpace(c.peerSessionToken)
	if strings.TrimSpace(c.entraClientID) != "" {
		tenant := strings.TrimSpace(c.entraTenant)
		if tenant == "" {
			return resolvedConfig{}, errors.New("-entra-tenant is required with -entra-client-id: it names the Microsoft Entra directory whose sign-ins this deployment accepts")
		}
		if multiTenantEntraTenants[strings.ToLower(tenant)] {
			return resolvedConfig{}, fmt.Errorf("-entra-tenant %q is a multi-tenant endpoint: any directory, including one an attacker owns, could sign in and present any email address; name this deployment's directory instead", tenant)
		}
	}
	if strings.TrimSpace(c.sessionToken) != "" {
		if providers := c.configuredProviders(); len(providers) != 0 {
			return resolvedConfig{}, fmt.Errorf("-session-token is a static browser session shared by every holder and cannot be combined with the configured identity provider (%s); remove -session-token", strings.Join(providers, ", "))
		}
	}
	if resolved.peerSessionToken != "" {
		if providers := c.configuredProviders(); len(providers) != 0 {
			return resolvedConfig{}, fmt.Errorf("-peer-session-token is a static browser session shared by every holder and cannot be combined with the configured identity provider (%s); remove -peer-session-token", strings.Join(providers, ", "))
		}
		if resolved.peerSessionToken == strings.TrimSpace(c.sessionToken) {
			return resolvedConfig{}, errors.New("-peer-session-token must differ from -session-token: each names a different member")
		}
		if workspace := strings.TrimSpace(c.authWorkspace); workspace != "" && workspace != defaultWorkspace {
			return resolvedConfig{}, fmt.Errorf("-peer-session-token seeds a member of the development workspace %s, not of -auth-workspace %s", defaultWorkspace, workspace)
		}
	}
	// -session-admin fails closed twice over: it is meaningless without the
	// session it escalates, and it is refused wherever a real identity exists.
	// The second check is not redundant with the one above — it must still hold
	// if -session-token ever becomes permissible alongside a provider.
	if c.sessionAdmin {
		if strings.TrimSpace(c.sessionToken) == "" {
			return resolvedConfig{}, errors.New("-session-admin escalates the static development browser session and requires -session-token")
		}
		if providers := c.configuredProviders(); len(providers) != 0 {
			return resolvedConfig{}, fmt.Errorf("-session-admin cannot be combined with the configured identity provider (%s): a workspace with real identities administers itself through them, not through a token every holder shares", strings.Join(providers, ", "))
		}
	}
	resolved.sessionAdmin = c.sessionAdmin
	if (c.appToken != "") != (c.appID != "") {
		return resolvedConfig{}, errors.New("Socket Mode requires both -app-token and -app-id")
	}
	// An empty Socket Mode host is not a fault: apps.connections.open then
	// names the origin the client reached it on, which is the one address
	// that client is known to be able to reach. A value derived from the
	// listen address named localhost, or an internal port behind a proxy.
	resolved.socketHost = strings.TrimSpace(c.socketHost)
	if resolved.socketHost != "" {
		if strings.ContainsAny(resolved.socketHost, "/?#@ ") {
			return resolvedConfig{}, fmt.Errorf("-socket-host must be a host or host:port, not %q", resolved.socketHost)
		}
	}
	switch c.chatMode {
	case "local":
		if c.chatAddress != "" || c.chatCA != "" || c.chatServerName != "" || c.chatClientCert != "" || c.chatClientKey != "" {
			return resolvedConfig{}, errors.New("distributed chat settings supplied for local composition")
		}
		if strings.TrimSpace(c.appCredentialKeyHex) != "" {
			key, err := secretbox.ParseKeyHex(c.appCredentialKeyHex)
			if err != nil {
				return resolvedConfig{}, fmt.Errorf("-app-credential-key-hex %w", err)
			}
			resolved.appCredentialKey = key
		} else if c.storeName != string(localchat.BackendMemory) {
			return resolvedConfig{}, errors.New("-app-credential-key-hex is required for durable local storage")
		}
	case "grpc":
		if c.chatAddress == "" || c.chatCA == "" || c.chatServerName == "" || c.chatClientCert == "" || c.chatClientKey == "" {
			return resolvedConfig{}, errors.New("grpc chat requires address, server CA/name, and client certificate/key")
		}
		// Everything in this list is owned by cmd/chatd in grpc composition, so
		// accepting it here would silently drop it. -bootstrap-admin-email is in
		// the list because a dropped value made terraform/ecs-runtime's required
		// bootstrap_admin_email inert and left administrator sign-in unreachable;
		// -app-token/-app-id are in it for the identical reason, one release
		// later: they are seeded only inside local composition, so a distributed
		// deployment started cleanly, registered /socket-mode, and then answered
		// every apps.connections.open with an authentication failure.
		if local := c.localOnlySettings(); len(local) != 0 {
			return resolvedConfig{}, fmt.Errorf("%s belong to sameoldchat-chatd in grpc composition, not to this process; supplying them here accepts and drops them", strings.Join(local, ", "))
		}
	}
	if strings.TrimSpace(c.authWorkspace) != "" {
		resolved.workspace = strings.TrimSpace(c.authWorkspace)
	}
	if strings.TrimSpace(c.authLookupUser) != "" {
		resolved.lookupUser = strings.TrimSpace(c.authLookupUser)
	}
	resolved.externalAuthorization = c.googleClientID != "" || c.googleClientSecret != "" || c.githubClientID != "" || c.githubClientSecret != "" || c.entraClientID != "" || c.entraClientSecret != "" || c.oidcIssuer != "" || c.oidcClientID != "" || c.oidcClientSecret != ""
	if strings.TrimSpace(c.authPublicURL) != "" {
		if err := web.ValidatePublicURL(c.authPublicURL); err != nil {
			return resolvedConfig{}, fmt.Errorf("-auth-public-url %w", err)
		}
	}
	if resolved.externalAuthorization {
		if strings.TrimSpace(c.authWorkspace) == "" || strings.TrimSpace(c.authLookupUser) == "" || strings.TrimSpace(c.authPublicURL) == "" || strings.TrimSpace(c.authStateKeyHex) == "" {
			return resolvedConfig{}, errors.New("external authorization requires -auth-workspace, -auth-lookup-user, -auth-public-url, and -auth-state-key-hex")
		}
		stateKey, decodeErr := hex.DecodeString(c.authStateKeyHex)
		if decodeErr != nil || len(stateKey) < 32 {
			// The decode error is deliberately not reported: encoding/hex names
			// the offending byte of the key ("invalid byte: U+007A 'z'"), which
			// writes part of a secret into the process log.
			return resolvedConfig{}, errors.New("-auth-state-key-hex must contain at least 32 bytes of hex")
		}
		resolved.authStateKey = stateKey
		if (c.googleClientID == "") != (c.googleClientSecret == "") {
			return resolvedConfig{}, errors.New("-google-client-id and -google-client-secret must be supplied together")
		}
		if (c.githubClientID == "") != (c.githubClientSecret == "") {
			return resolvedConfig{}, errors.New("-github-client-id and -github-client-secret must be supplied together")
		}
		if (c.entraClientID == "") != (c.entraClientSecret == "") {
			return resolvedConfig{}, errors.New("-entra-client-id and -entra-client-secret must be supplied together")
		}
		if (c.oidcIssuer == "") != (c.oidcClientID == "") || (c.oidcIssuer == "") != (c.oidcClientSecret == "") {
			return resolvedConfig{}, errors.New("-oidc-issuer, -oidc-client-id, and -oidc-client-secret must be supplied together")
		}
	}
	scopes, err := developmentScopes(false)
	if err != nil {
		return resolvedConfig{}, err
	}
	resolved.scopes = scopes
	// Only the browser session is escalated, never the API token. They are
	// seeded together and used to share one scope set, but they are not the
	// same risk: the token is what an integration holds, and nothing about
	// qualifying an administration page needs it to gain control-plane
	// authority as a side effect.
	sessionScopes, err := developmentScopes(c.sessionAdmin)
	if err != nil {
		return resolvedConfig{}, err
	}
	resolved.sessionScopes = sessionScopes
	return resolved, nil
}

// validateListenAddress rejects an address net/http would refuse at Listen. A
// port outside 0-65535 or a missing colon is a configuration mistake the
// operator can only otherwise discover from a crash-looping task, and both were
// accepted by -check-config.
func validateListenAddress(flagName, value string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s %q is not a host:port listen address: %w", flagName, value, err)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("%s %q names no port", flagName, value)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		// A named service ("http") is resolved by the operating system, so it
		// is not this process's business to reject it.
		if _, lookupErr := net.LookupPort("tcp", port); lookupErr != nil {
			return fmt.Errorf("%s %q names no resolvable port", flagName, value)
		}
		return nil
	}
	if number < 0 || number > 65535 {
		return fmt.Errorf("%s %q uses port %d, which is outside 0-65535", flagName, value, number)
	}
	_ = host
	return nil
}

// localOnlySettings names every supplied setting that only local composition can
// act on. It exists so the rejection list is one enumeration rather than a
// growing conjunction that a new flag can be forgotten from.
func (c startupConfig) localOnlySettings() []string {
	settings := make([]string, 0, 12)
	for _, candidate := range []struct{ flag, value string }{
		{flag: "-store", value: c.storeName},
		{flag: "-db", value: c.databaseDSN},
		{flag: "-dqlite-directory", value: c.dqliteDirectory},
		{flag: "-dqlite-address", value: c.dqliteAddress},
		{flag: "-dqlite-cluster", value: c.dqliteCluster},
		{flag: "-dqlite-database", value: c.dqliteDatabase},
		{flag: "-blob-dir", value: c.blobDirectory},
		{flag: "-blob-s3-bucket", value: c.blobS3Bucket},
		{flag: "-blob-s3-prefix", value: c.blobS3Prefix},
		{flag: "-bootstrap-admin-email", value: c.bootstrapAdminEmail},
		{flag: "-app-credential-key-hex", value: c.appCredentialKeyHex},
		{flag: "-app-token", value: c.appToken},
		{flag: "-app-id", value: c.appID},
		{flag: "-peer-session-token", value: c.peerSessionToken},
	} {
		if strings.TrimSpace(candidate.value) != "" {
			settings = append(settings, candidate.flag)
		}
	}
	return settings
}

// developmentScopes are the scopes the seeded API token and the seeded browser
// session receive.
//
// They used to receive auth.AllScopes(), which includes admin and every
// admin.* scope, so the static development credentials held the workspace
// control plane. The member role is the authority a signed-in user has by
// default, and auth.ScopesForWorkspaceRole is the only place that mapping
// lives, so the two cannot drift.
// developmentScopes is member-level unless a deployment has explicitly asked for
// an administrative development session. The default is the security property:
// a token every holder shares must not carry control-plane authority.
func developmentScopes(administrator bool) ([]string, error) {
	role := domain.WorkspaceRoleMember
	if administrator {
		role = domain.WorkspaceRoleAdmin
	}
	scopes, err := auth.ScopesForWorkspaceRole(role)
	if err != nil {
		return nil, err
	}
	return scopes.Values(), nil
}

// seedDevelopmentCredentials seeds the development API token and, only when one
// was supplied, the static browser session, and joins the seeded user to the
// seeded conversation. Neither credential carries control-plane authority and
// the session expires within devSessionLifetime.
//
// The join is not cosmetic. The service layer enforces conversation membership
// on chat.postMessage and nine other operations, because `not_in_channel` is
// declared by ten pinned operations and used to be emitted nowhere. Without it
// the credentials this function seeds can authenticate and then cannot post:
// the seeded identity was a member of the workspace and of no conversation, so
// every write against the seeded channel answered `not_in_channel`.
func seedDevelopmentCredentials(ctx context.Context, runtime localchat.Runtime, resolved resolvedConfig, logger *slog.Logger, now time.Time) error {
	if err := runtime.TokenSeeder.SeedToken(ctx, resolved.apiToken, domain.TokenRecord{WorkspaceID: domain.WorkspaceID(resolved.workspace), UserID: domain.UserID(resolved.lookupUser), Scopes: resolved.scopes}); err != nil {
		return fmt.Errorf("seed API token: %w", err)
	}
	// JoinConversation is idempotent (the membership insert is ON CONFLICT DO
	// NOTHING), so this is safe on an existing database. A deployment whose
	// workspace, user, or conversation is its own has nothing to join here, and
	// that is reported rather than treated as a startup failure.
	if _, err := runtime.Service.JoinConversation(ctx, domain.WorkspaceID(resolved.workspace), domain.UserID(resolved.lookupUser), defaultConversation); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("join seeded conversation: %w", err)
		}
		logger.Info("seeded conversation not joined", "conversation", defaultConversation, "workspace", resolved.workspace, "user", resolved.lookupUser, "reason", "no such open conversation in this workspace")
	}
	if resolved.peerSessionToken != "" {
		if _, err := runtime.Service.JoinConversation(ctx, defaultWorkspace, localchat.DevelopmentPeerUser, defaultConversation); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("join development peer to seeded conversation: %w", err)
		}
		peerScopes, err := developmentScopes(false)
		if err != nil {
			return err
		}
		if err := runtime.SessionSeeder.SeedSession(ctx, resolved.peerSessionToken, domain.SessionRecord{WorkspaceID: defaultWorkspace, UserID: localchat.DevelopmentPeerUser, Scopes: peerScopes, ExpiresAt: now.Add(devSessionLifetime)}); err != nil {
			return fmt.Errorf("seed peer browser session: %w", err)
		}
	}
	if strings.TrimSpace(resolved.sessionToken) == "" {
		return nil
	}
	if resolved.sessionAdmin {
		logger.Warn("development browser session has workspace-administrator scopes",
			"reason", "-session-admin", "session_lifetime", devSessionLifetime.String(),
			"note", "every holder of the session token administers this workspace")
	}
	if err := runtime.SessionSeeder.SeedSession(ctx, resolved.sessionToken, domain.SessionRecord{WorkspaceID: domain.WorkspaceID(resolved.workspace), UserID: domain.UserID(resolved.lookupUser), Scopes: resolved.sessionScopes, ExpiresAt: now.Add(devSessionLifetime)}); err != nil {
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

// readinessHandler probes the workspace and lookup user this deployment is
// configured for, not a hardcoded seed pair, and logs the failure. It used to
// probe "Tdev"/"Udev" unconditionally and discard the error, so a deployment
// without that tenant was permanently unready with no diagnostic anywhere.
func readinessHandler(chatService chatapi.Service, logger *slog.Logger, workspace, user string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestContext, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := readinessCheck(requestContext, chatService, workspace, user); err != nil {
			logger.Warn("readiness probe failed", "error", err, "workspace", workspace, "user", user)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ready\n"))
	}
}

func readinessCheck(ctx context.Context, chatService chatapi.Service, workspace, user string) error {
	_, err := chatService.Conversations(ctx, domain.WorkspaceID(workspace), domain.UserID(user), domain.ConversationListRequest{Limit: 1})
	return err
}

// newRateLimiter shares the Web API budget wherever a deployment may run more
// than one web replica — the distributed composition, and a local one on
// PostgreSQL or dqlite — by drawing every call from the chat module's store.
// Memory and SQLite are single-replica, so the budget stays in process there
// and a call costs no write.
func newRateLimiter(settings startupConfig, tokens slack.RateTokens, logger *slog.Logger) *slack.RateLimiter {
	if !sharesRateLimits(settings) {
		return slack.NewRateLimiter()
	}
	return slack.NewSharedRateLimiter(tokens, logger)
}

// sharesRateLimits reports whether the deployment's replicas must share one
// rate-limit budget.
func sharesRateLimits(settings startupConfig) bool {
	if settings.chatMode != "local" {
		return true
	}
	backend := localchat.Backend(settings.storeName)
	return backend == localchat.BackendPostgreSQL || backend == localchat.BackendDqlite
}
