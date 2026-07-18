package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/generated"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	listen := flag.String("listen", "", "gRPC listen address (required)")
	backend := flag.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	blobS3Bucket := flag.String("blob-s3-bucket", "", "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flag.String("blob-s3-prefix", "", "Amazon Simple Storage Service key prefix for file storage")
	dsn := flag.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql storage")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory; required for local dqlite storage")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address; required for local dqlite storage")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name; required for local dqlite storage")
	apiToken := flag.String("api-token", "", "durable API bearer token (required)")
	sessionToken := flag.String("session-token", "", "durable browser session token (required)")
	blobDirectory := flag.String("blob-dir", "", "external blob directory for file storage")
	blobMaxBytes := flag.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	certFile := flag.String("tls-cert", "", "TLS certificate file (required)")
	keyFile := flag.String("tls-key", "", "TLS private key file (required)")
	clientCAFile := flag.String("tls-client-ca", "", "CA certificate used to verify gRPC clients (required)")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *listen == "" || *backend == "" || *certFile == "" || *keyFile == "" || *clientCAFile == "" || *apiToken == "" || *sessionToken == "" {
		logger.Error("chatd requires listen, store, TLS server/client-CA credentials, API token, and session token")
		os.Exit(2)
	}
	tlsCertificate, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		logger.Error("load TLS certificate", "error", err)
		os.Exit(1)
	}
	clientCAPEM, err := os.ReadFile(*clientCAFile)
	if err != nil {
		logger.Error("read TLS client CA", "error", err)
		os.Exit(1)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(clientCAPEM) {
		logger.Error("TLS client CA contains no certificates")
		os.Exit(1)
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		os.Exit(2)
	}
	runtime, err := localchat.Open(context.Background(), localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, BlobDirectory: *blobDirectory, BlobS3Bucket: *blobS3Bucket, BlobS3Prefix: *blobS3Prefix, BlobMaxBytes: *blobMaxBytes})
	if err != nil {
		logger.Error("open local chat", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := runtime.Closer.Close(); err != nil {
			logger.Error("close local chat", "error", err)
		}
	}()
	if err := runtime.TokenSeeder.SeedToken(context.Background(), *apiToken, domain.TokenRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: auth.AllScopes()}); err != nil {
		logger.Error("seed durable API token", "error", err)
		os.Exit(1)
	}
	if err := runtime.SessionSeeder.SeedSession(context.Background(), *sessionToken, domain.SessionRecord{WorkspaceID: "Tdev", UserID: "Udev", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}); err != nil {
		logger.Error("seed durable browser session", "error", err)
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Error("listen", "error", err)
		os.Exit(1)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{tlsCertificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs, MinVersion: tls.VersionTLS13})))
	if err := generated.RegisterChatServiceServer(server, runtime.Service, runtime.TokenStore, runtime.SessionStore, runtime.SessionRevoker); err != nil {
		logger.Error("register chat service", "error", err)
		os.Exit(1)
	}
	logger.Info("chat gRPC server listening", "addr", *listen)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-serveErrors:
		if err != nil {
			logger.Error("chat gRPC server stopped", "error", err)
			os.Exit(1)
		}
	case sig := <-signals:
		logger.Info("chat gRPC server draining", "signal", sig.String())
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
}
