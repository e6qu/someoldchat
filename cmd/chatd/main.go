package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatgrpc "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc"
	"github.com/sameoldchat/sameoldchat/internal/observability"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
)

// exitConfiguration and exitRuntime separate "the operator gave us something
// impossible" from "something failed while running".
const (
	exitConfiguration = 2
	exitRuntime       = 1
)

// devSessionLifetime bounds the seeded development browser session. It used to
// last a day, which is a long time for a credential that is a plain flag value
// in a process command line and is shared by everyone who knows it.
const devSessionLifetime = time.Hour

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Every teardown runs through run's defers; main is the only place that
	// exits, so the store is closed on every path including a failed startup.
	if code := run(context.Background(), logger, os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, logger *slog.Logger, args []string) int {
	flags := flag.NewFlagSet("sameoldchat-chatd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	listen := flags.String("listen", "", "gRPC listen address (required)")
	backend := flags.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	blobS3Bucket := flags.String("blob-s3-bucket", "", "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flags.String("blob-s3-prefix", "", "Amazon Simple Storage Service key prefix for file storage")
	dsn := flags.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql storage")
	dqliteDirectory := flags.String("dqlite-directory", "", "dqlite state directory; required for local dqlite storage")
	dqliteAddress := flags.String("dqlite-address", "", "dqlite node address; required for local dqlite storage")
	dqliteCluster := flags.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flags.String("dqlite-database", "", "dqlite database name; required for local dqlite storage")
	// The environment default matches cmd/server. Without it the only way to
	// supply the durable API token was the command line, so it was readable in
	// `ps`, in /proc/<pid>/cmdline, and in every retained ECS task-definition
	// revision, while terraform/ecs-runtime already publishes it as a secret
	// environment variable.
	apiToken := flags.String("api-token", os.Getenv("SAMEOLDCHAT_API_TOKEN"), "durable API bearer token (required); defaults to SAMEOLDCHAT_API_TOKEN")
	appToken := flags.String("app-token", os.Getenv("SAMEOLDCHAT_APP_TOKEN"), "Socket Mode app-level token")
	appID := flags.String("app-id", os.Getenv("SAMEOLDCHAT_APP_ID"), "Socket Mode app identifier")
	// -session-token is optional. It seeds one static browser session shared by
	// every holder of the value, which is a development convenience: the HTTP
	// process rejects it outright once a real identity provider is configured, so
	// requiring it here forced a deployment with single sign-on to carry a shared
	// bearer session it must not have.
	sessionToken := flags.String("session-token", os.Getenv("SAMEOLDCHAT_SESSION_TOKEN"), "static development browser session token; omit when the HTTP process uses an identity provider")
	blobDirectory := flags.String("blob-dir", "", "external blob directory for file storage")
	blobMaxBytes := flags.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	certFile := flags.String("tls-cert", "", "TLS certificate file (required)")
	keyFile := flags.String("tls-key", "", "TLS private key file (required)")
	clientCAFile := flags.String("tls-client-ca", "", "CA certificate used to verify gRPC clients (required)")
	metricsListen := flags.String("metrics-listen", os.Getenv("SAMEOLDCHAT_METRICS_LISTEN"), "operator-only listen address publishing /metrics; empty serves no metrics endpoint")
	// chatd owns the store in distributed composition, so it is the only process
	// that can seed the initial administrator there. Without this flag a
	// distributed deployment had no way at all to set the bootstrap
	// administrator email, which made terraform/ecs-runtime's required
	// bootstrap_admin_email inert and left administrator sign-in unreachable.
	bootstrapAdminEmail := flags.String("bootstrap-admin-email", os.Getenv("SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL"), "email address of the initial workspace administrator")
	appCredentialKeyHex := flags.String("app-credential-key-hex", os.Getenv("SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX"), "AES-256 key used to encrypt application signing credentials")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitConfiguration
	}
	if *listen == "" || *backend == "" || *certFile == "" || *keyFile == "" || *clientCAFile == "" || *apiToken == "" {
		logger.Error("chatd requires listen, store, TLS server/client-CA credentials, and an API token")
		return exitConfiguration
	}
	// A mistyped path or a malformed PEM is operator-supplied configuration, not
	// a runtime failure: reporting it as exitRuntime made an orchestrator with a
	// restart-on-runtime-failure-only policy restart-loop forever on a typo.
	tlsCertificate, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		logger.Error("load TLS certificate", "error", err)
		return exitConfiguration
	}
	clientCAPEM, err := os.ReadFile(*clientCAFile)
	if err != nil {
		logger.Error("read TLS client CA", "error", err)
		return exitConfiguration
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(clientCAPEM) {
		logger.Error("TLS client CA contains no certificates", "path", *clientCAFile)
		return exitConfiguration
	}
	if err := refuseSelfIssuedClientAuthority(tlsCertificate, clientCAs); err != nil {
		logger.Error("TLS client CA also issued this server's own certificate", "error", err, "client-ca", *clientCAFile, "certificate", *certFile)
		return exitConfiguration
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		return exitConfiguration
	}
	if *appToken != "" || *appID != "" {
		if *appToken == "" || *appID == "" {
			logger.Error("Socket Mode requires both app token and app ID")
			return exitConfiguration
		}
	}
	var appCredentialKey []byte
	if strings.TrimSpace(*appCredentialKeyHex) != "" {
		appCredentialKey, err = secretbox.ParseKeyHex(*appCredentialKeyHex)
		if err != nil {
			logger.Error("invalid application credential key")
			return exitConfiguration
		}
	} else if *backend != string(localchat.BackendMemory) {
		logger.Error("durable storage requires an application credential key")
		return exitConfiguration
	}
	runtime, err := localchat.Open(ctx, localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, BlobDirectory: *blobDirectory, BlobS3Bucket: *blobS3Bucket, BlobS3Prefix: *blobS3Prefix, BlobMaxBytes: *blobMaxBytes, BootstrapAdminEmail: *bootstrapAdminEmail, AppCredentialKey: appCredentialKey})
	if err != nil {
		logger.Error("open local chat", "error", err)
		return exitRuntime
	}
	defer func() {
		if err := runtime.Closer.Close(); err != nil {
			logger.Error("close local chat", "error", err)
		}
	}()
	if err := seedDurableCredentials(ctx, runtime, *apiToken, *sessionToken, time.Now().UTC()); err != nil {
		logger.Error("seed durable credentials", "error", err)
		return exitRuntime
	}
	if *appToken != "" {
		seedAppToken, ok := runtime.TokenStore.(interface {
			SeedAppToken(context.Context, string, domain.AppTokenRecord) error
		})
		if !ok {
			logger.Error("storage backend cannot seed Socket Mode app tokens")
			return exitRuntime
		}
		if err := seedAppToken.SeedAppToken(ctx, *appToken, domain.AppTokenRecord{AppID: domain.AppID(*appID), Scopes: []string{string(auth.ScopeConnectionsWrite)}}); err != nil {
			logger.Error("seed Socket Mode app token", "error", err)
			return exitRuntime
		}
		if err := runtime.Store.CreateAppInstallation(ctx, domain.AppInstallation{AppID: domain.AppID(*appID), WorkspaceID: "Tdev", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
			logger.Error("seed Socket Mode app installation", "error", err)
			return exitRuntime
		}
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Error("listen", "error", err)
		return exitRuntime
	}
	metrics := observability.NewRegistry()
	transportCredentials := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{tlsCertificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs, MinVersion: tls.VersionTLS13})
	server, err := chatServer(runtime.Service, runtime.TokenStore, runtime.SessionStore, runtime.SessionRevoker, chatgrpc.Observer{Metrics: metrics, Logger: logger}, grpc.Creds(transportCredentials))
	if err != nil {
		_ = listener.Close()
		logger.Error("register chat service", "error", err)
		return exitRuntime
	}
	// Metrics live on their own listener: the gRPC listener requires a client
	// certificate and speaks no HTTP, and the series describe status codes and
	// call volumes, which is operator data rather than application data.
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
	applicationContext, stopApplication := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopApplication()
	logger.Info("chat gRPC server listening", "addr", *listen)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	select {
	case err := <-serveErrors:
		if err != nil {
			logger.Error("chat gRPC server stopped", "error", err)
			return exitRuntime
		}
	case <-applicationContext.Done():
		logger.Info("chat gRPC server draining")
		stopped := make(chan struct{})
		go func() {
			server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			logger.Error("chat gRPC drain deadline exceeded")
			server.Stop()
		}
	}
	return 0
}

// chatServer builds the gRPC server this process serves.
//
// The transport package's constructor is the only supported way to build it: it
// installs the panic recovery, the message bounds both peers must agree on, the
// keepalive that detects a half-open connection, and the seam instrumentation.
// This process used to call grpc.NewServer(grpc.Creds(...)) and register the
// module separately, so a nil map write in any handler terminated every
// in-flight request on the replica while the monolith recovered the same panic
// and lost one request, and a payload the monolith accepted was rejected here as
// ResourceExhausted.
//
// The standard health service is registered alongside it so a load balancer or
// an orchestrator can probe a process that speaks nothing but gRPC.
func chatServer(implementation chatapi.Service, tokens auth.TokenStore, sessions auth.SessionStore, revoker auth.SessionRevoker, observer chatgrpc.Observer, extra ...grpc.ServerOption) (*grpc.Server, error) {
	server, err := chatgrpc.NewChatServer(implementation, tokens, sessions, revoker, observer, extra...)
	if err != nil {
		return nil, err
	}
	healthService := health.NewServer()
	healthgrpc.RegisterHealthServer(server, healthService)
	healthService.SetServingStatus("", healthgrpc.HealthCheckResponse_SERVING)
	return server, nil
}

// developmentScopes are the scopes the seeded API token and the seeded browser
// session receive.
//
// They used to receive auth.AllScopes(), which includes admin and every admin.*
// scope, so the durable seeded credentials held the workspace control plane. The
// member role is the authority a signed-in user has by default, and
// auth.ScopesForWorkspaceRole is the only place that mapping lives, so the two
// cannot drift.
func developmentScopes() ([]string, error) {
	scopes, err := auth.ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		return nil, err
	}
	return scopes.Values(), nil
}

// seedDurableCredentials seeds the API token and, only when one was supplied,
// the static browser session. Neither carries control-plane authority and the
// session expires within devSessionLifetime.
func seedDurableCredentials(ctx context.Context, runtime localchat.Runtime, apiToken, sessionToken string, now time.Time) error {
	scopes, err := developmentScopes()
	if err != nil {
		return err
	}
	if err := runtime.TokenSeeder.SeedToken(ctx, apiToken, domain.TokenRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: scopes}); err != nil {
		return fmt.Errorf("seed durable API token: %w", err)
	}
	if strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	if err := runtime.SessionSeeder.SeedSession(ctx, sessionToken, domain.SessionRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: scopes, ExpiresAt: now.Add(devSessionLifetime)}); err != nil {
		return fmt.Errorf("seed durable browser session: %w", err)
	}
	return nil
}

// metricsListener builds the operator-only metrics server, or nil when no
// address was configured. ReadHeaderTimeout and IdleTimeout bound what a client
// on that address can hold open.
func metricsListener(addr string, metrics *observability.Registry) *http.Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
}

// refuseSelfIssuedClientAuthority rejects a configuration in which the
// authority that decides *who may connect to chatd* also issued the certificate
// that says *which server chatd is*.
//
// docs/modules.md gave one `ca.crt` to both `-tls-client-ca` here and
// `-chat-ca` on the HTTP process, which is the shape this refuses. With one
// authority, every certificate it issues authenticates as a client to the whole
// internal data plane — including this server's own certificate, so anything
// that holds the server key is a privileged client of the service it is
// serving. Two authorities make the two roles independent, and a document
// cannot be the only thing enforcing that.
//
// The test is exact rather than advisory: the server leaf is verified against
// the client pool with client authentication as its extended key usage. A
// separate client CA cannot verify it, so a correct deployment cannot trip
// this, and a shared CA always does.
func refuseSelfIssuedClientAuthority(certificate tls.Certificate, clientCAs *x509.CertPool) error {
	if len(certificate.Certificate) == 0 {
		return errors.New("server certificate carries no leaf")
	}
	leaf := certificate.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse server certificate: %w", err)
		}
		leaf = parsed
	}
	intermediates := x509.NewCertPool()
	for _, encoded := range certificate.Certificate[1:] {
		if parsed, err := x509.ParseCertificate(encoded); err == nil {
			intermediates.AddCert(parsed)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         clientCAs,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil
	}
	return errors.New("this server's own certificate would be accepted as a client certificate; issue clients from a separate authority and give -tls-client-ca only that authority")
}
