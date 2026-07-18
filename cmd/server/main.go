package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/api/slack"
	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/generated"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/realtime"
	"github.com/sameoldchat/sameoldchat/internal/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	chatMode := flag.String("chat-mode", "", "chat composition: local or grpc (required)")
	storeName := flag.String("store", "", "local storage backend: memory, sqlite, postgresql, or dqlite")
	dsn := flag.String("db", databaseDSNDefault(), "SQLite or PostgreSQL DSN; required for sqlite and postgresql storage")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory; required for local dqlite storage")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address; required for local dqlite storage")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name; required for local dqlite storage")
	blobDirectory := flag.String("blob-dir", "", "external blob directory for file storage")
	blobS3Bucket := flag.String("blob-s3-bucket", "", "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flag.String("blob-s3-prefix", "", "Amazon Simple Storage Service key prefix for file storage")
	blobMaxBytes := flag.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	chatAddress := flag.String("chat-address", "", "distributed chat gRPC address; required for -chat-mode=grpc")
	chatCA := flag.String("chat-ca", "", "CA certificate for distributed chat gRPC")
	chatServerName := flag.String("chat-server-name", "", "TLS server name for distributed chat gRPC")
	chatClientCert := flag.String("chat-client-cert", "", "client certificate for distributed chat gRPC")
	chatClientKey := flag.String("chat-client-key", "", "client private key for distributed chat gRPC")
	apiToken := flag.String("api-token", os.Getenv("SAMEOLDCHAT_API_TOKEN"), "API bearer token (required)")
	sessionToken := flag.String("session-token", os.Getenv("SAMEOLDCHAT_SESSION_TOKEN"), "browser session token (required)")
	authWorkspace := flag.String("auth-workspace", "", "workspace for external authorization (required when enabled)")
	authLookupUser := flag.String("auth-lookup-user", "", "existing user used to authorize external identity lookup (required when enabled)")
	authPublicURL := flag.String("auth-public-url", "", "public HTTPS URL used for authorization callbacks")
	authStateKeyHex := flag.String("auth-state-key-hex", os.Getenv("SAMEOLDCHAT_AUTH_STATE_KEY_HEX"), "HMAC key for authorization state, at least 32 bytes of hex")
	bootstrapAdminEmail := flag.String("bootstrap-admin-email", os.Getenv("SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL"), "email address of the initial local workspace administrator")
	googleClientID := flag.String("google-client-id", "", "Google OAuth client ID")
	googleClientSecret := flag.String("google-client-secret", "", "Google OAuth client secret")
	githubClientID := flag.String("github-client-id", "", "GitHub OAuth client ID")
	githubClientSecret := flag.String("github-client-secret", "", "GitHub OAuth client secret")
	entraClientID := flag.String("entra-client-id", "", "Microsoft Entra application client ID")
	entraClientSecret := flag.String("entra-client-secret", "", "Microsoft Entra application client secret")
	entraTenant := flag.String("entra-tenant", "common", "Microsoft Entra tenant identifier")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("SAMEOLDCHAT_OIDC_ISSUER"), "OpenID Connect issuer URL")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("SAMEOLDCHAT_OIDC_CLIENT_ID"), "OpenID Connect client ID")
	oidcClientSecret := flag.String("oidc-client-secret", os.Getenv("SAMEOLDCHAT_OIDC_CLIENT_SECRET"), "OpenID Connect client secret")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mux := http.NewServeMux()
	if *chatMode != "local" && *chatMode != "grpc" {
		logger.Error("invalid chat composition", "mode", *chatMode, "allowed", "local, grpc")
		os.Exit(2)
	}
	if *apiToken == "" || *sessionToken == "" {
		logger.Error("API token and session token are required")
		os.Exit(2)
	}
	var chatService chatapi.Service
	var authenticator auth.Authenticator
	var webAuthenticator auth.Authenticator
	var sessionRevoker auth.SessionRevoker
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
		runtime, err := localchat.Open(context.Background(), localchat.Config{Backend: localchat.Backend(*storeName), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, BlobDirectory: *blobDirectory, BlobS3Bucket: *blobS3Bucket, BlobS3Prefix: *blobS3Prefix, BlobMaxBytes: *blobMaxBytes, BootstrapAdminEmail: *bootstrapAdminEmail})
		if err != nil {
			logger.Error("open local chat", "error", err)
			os.Exit(1)
		}
		chatService = runtime.Service
		if err := runtime.TokenSeeder.SeedToken(context.Background(), *apiToken, domain.TokenRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: auth.AllScopes()}); err != nil {
			logger.Error("seed API token", "error", err)
			os.Exit(1)
		}
		authenticator, err = auth.NewStored(runtime.TokenStore)
		if err != nil {
			logger.Error("configure stored authenticator", "error", err)
			os.Exit(1)
		}
		if err := runtime.SessionSeeder.SeedSession(context.Background(), *sessionToken, domain.SessionRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}); err != nil {
			logger.Error("seed browser session", "error", err)
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
		if *storeName != "" || *dsn != "" || *dqliteDirectory != "" || *dqliteAddress != "" || *dqliteCluster != "" || *dqliteDatabase != "" || *blobDirectory != "" || *blobS3Bucket != "" || *blobS3Prefix != "" {
			logger.Error("local storage settings supplied for grpc composition")
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
		connectContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		connection, err := grpc.DialContext(connectContext, *chatAddress, grpc.WithTransportCredentials(transportCredentials), grpc.WithBlock())
		cancel()
		if err != nil {
			logger.Error("connect chat gRPC", "error", err)
			os.Exit(1)
		}
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
	slackHandler, err := slack.NewHandler(chatService, authenticator)
	if err != nil {
		logger.Error("configure Slack API", "error", err)
		os.Exit(1)
	}
	slackHandler.Register(mux)
	webHandler, err := web.NewHandler(chatService, webAuthenticator, sessionRevoker, "Cdev")
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
			logger.Error("authorization state key must contain at least 32 bytes of hex", "error", decodeErr)
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
		if (*entraClientID == "") != (*entraClientSecret == "") || *entraTenant == "" {
			logger.Error("Microsoft Entra client ID, secret, and tenant must be configured together")
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
			oidcProvider, discoveryErr := web.DiscoverOpenIDConnectProvider(context.Background(), http.DefaultClient, *oidcIssuer, *oidcClientID, *oidcClientSecret)
			if discoveryErr != nil {
				logger.Error("discover OpenID Connect provider", "error", discoveryErr)
				os.Exit(1)
			}
			providers = append(providers, oidcProvider)
		}
		loginHandler, loginErr := web.NewLoginHandler(chatService, domain.WorkspaceID(*authWorkspace), domain.UserID(*authLookupUser), *authPublicURL, stateKey, providers)
		if loginErr != nil {
			logger.Error("configure external authorization", "error", loginErr)
			os.Exit(1)
		}
		webHandler.Login = &loginHandler
	}
	webHandler.Register(mux)
	sseHandler, err := realtime.NewHandler(chatService, "Tdev", webAuthenticator)
	if err != nil {
		logger.Error("configure realtime", "error", err)
		os.Exit(1)
	}
	sseHandler.Register(mux)
	rtmHandler, err := realtime.NewRTMHandler(chatService, "Tdev", chatService)
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
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>SameOldChat</title><h1>SameOldChat</h1>"))
	})

	server := &http.Server{Addr: *addr, Handler: mux}
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

func databaseDSNDefault() string {
	return os.Getenv("SAMEOLDCHAT_DATABASE_URL")
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
