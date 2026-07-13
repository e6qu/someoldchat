package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/activator"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
)

func main() {
	listen := flag.String("listen", "", "activator listen address (required)")
	stateDB := flag.String("state-db", "", "durable lifecycle control SQLite DSN (required)")
	forwardURL := flag.String("forward-url", "", "active stack HTTP URL (required)")
	controlToken := flag.String("control-token", "", "control-plane bearer token for /activate (required)")
	snapshotRoot := flag.String("snapshot-root", "", "absolute snapshot root (required)")
	snapshotSource := flag.String("snapshot-source", "", "database path used to create snapshots (required)")
	snapshotOutput := flag.String("snapshot-output", "", "database restore path (required)")
	backend := flag.String("backend", "", "snapshot backend identifier (required)")
	schemaVersion := flag.Int("schema-version", 0, "current schema version (required)")
	applicationVersion := flag.String("application-version", "", "application version recorded in snapshots (required)")
	keyID := flag.String("snapshot-key-id", "", "snapshot encryption key identifier (required)")
	encryptionKey := flag.String("snapshot-encryption-key-hex", "", "32-byte snapshot encryption key in hex (required)")
	signingKey := flag.String("snapshot-signing-key-hex", "", "snapshot signing key in hex (at least 32 bytes, required)")
	spoolKey := flag.String("request-spool-key-hex", "", "32-byte request spool encryption key in hex (required)")
	spoolOwner := flag.String("request-spool-owner", "", "stable unique activator replica owner ID (required)")
	spoolMaxBytes := flag.Int64("request-spool-max-bytes", 0, "maximum total queued request body bytes (required)")
	spoolMaxRequests := flag.Int("request-spool-max-requests", 0, "maximum queued request count (required)")
	maxSnapshotBytes := flag.Int64("snapshot-max-bytes", 0, "maximum snapshot plaintext bytes (required)")
	commands := commandFlags{}
	commands.bind(flag.CommandLine)
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *listen == "" || *stateDB == "" || *forwardURL == "" || *controlToken == "" || *snapshotRoot == "" || *snapshotSource == "" || *snapshotOutput == "" || *backend == "" || *schemaVersion < 1 || *applicationVersion == "" || *keyID == "" || *encryptionKey == "" || *signingKey == "" || *spoolKey == "" || *spoolOwner == "" || *spoolMaxBytes <= 0 || *spoolMaxRequests <= 0 || *maxSnapshotBytes <= 0 {
		logger.Error("activator requires explicit listen, control, snapshot, and version settings")
		os.Exit(2)
	}
	encryption, err := hex.DecodeString(*encryptionKey)
	if err != nil || len(encryption) != 32 {
		logger.Error("snapshot encryption key must be 32 bytes of hex", "error", err)
		os.Exit(2)
	}
	signing, err := hex.DecodeString(*signingKey)
	if err != nil || len(signing) < 32 {
		logger.Error("snapshot signing key must contain at least 32 bytes of hex", "error", err)
		os.Exit(2)
	}
	spoolEncryption, err := hex.DecodeString(*spoolKey)
	if err != nil || len(spoolEncryption) != 32 {
		logger.Error("request spool key must be 32 bytes of hex", "error", err)
		os.Exit(2)
	}
	target, err := url.Parse(*forwardURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		logger.Error("forward URL must be an absolute URL", "error", err)
		os.Exit(2)
	}
	control, err := lifecycle.OpenSQLiteStateStore(*stateDB, lifecycle.StateRecord{State: lifecycle.StateHibernated})
	if err != nil {
		logger.Error("open lifecycle control store", "error", err)
		os.Exit(1)
	}
	defer control.Close()
	controller, err := lifecycle.NewPersistent(control)
	if err != nil {
		logger.Error("load lifecycle state", "error", err)
		os.Exit(1)
	}
	manager, err := lifecycle.NewSnapshotManager(*snapshotRoot, encryption, signing, *keyID, *maxSnapshotBytes)
	if err != nil {
		logger.Error("configure snapshot manager", "error", err)
		os.Exit(2)
	}
	snapshots, err := lifecycle.NewFileSnapshotter(manager, *snapshotSource, *snapshotOutput, lifecycle.Manifest{Backend: *backend, SchemaVersion: *schemaVersion, ApplicationVersion: *applicationVersion, MinRestorerVersion: *applicationVersion, MaxRestorerVersion: *applicationVersion})
	if err != nil {
		logger.Error("configure snapshotter", "error", err)
		os.Exit(2)
	}
	driver, err := lifecycle.NewCommandDriver(lifecycle.OSCommandRunner{}, commands.set())
	if err != nil {
		logger.Error("configure lifecycle commands", "error", err)
		os.Exit(2)
	}
	coordinator, err := lifecycle.NewCoordinator(controller, driver, snapshots)
	if err != nil {
		logger.Error("configure lifecycle coordinator", "error", err)
		os.Exit(2)
	}
	if err := coordinator.Recover(context.Background()); err != nil && !errors.Is(err, lifecycle.ErrRecoveryRequired) {
		logger.Error("recover lifecycle state", "error", err)
		os.Exit(1)
	} else if errors.Is(err, lifecycle.ErrRecoveryRequired) {
		logger.Error("lifecycle state requires explicit recovery; serving operator endpoints only", "error", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	spool, err := activator.OpenSQLiteSpool(*stateDB, spoolEncryption, activator.SpoolLimits{MaxBodyBytes: 4 << 20, MaxQueuedBytes: *spoolMaxBytes, MaxQueuedRequests: *spoolMaxRequests})
	if err != nil {
		logger.Error("configure request spool", "error", err)
		os.Exit(2)
	}
	defer spool.Close()
	handler, err := activator.NewDurableForwardingHandler(controller, coordinator.WakeAt, proxy, spool, *spoolOwner, 4<<20, 2*time.Minute)
	if err != nil {
		logger.Error("configure forwarding activator", "error", err)
		os.Exit(2)
	}
	mux := http.NewServeMux()
	handler.RegisterForwarding(mux)
	mux.HandleFunc("POST /hibernate", func(w http.ResponseWriter, r *http.Request) {
		state, fence := controller.Snapshot()
		if state != lifecycle.StateActive {
			w.WriteHeader(http.StatusConflict)
			return
		}
		result := make(chan error, 1)
		go func() {
			_, err := coordinator.Hibernate(context.Background(), fence)
			result <- err
		}()
		select {
		case err := <-result:
			if err != nil {
				logger.Error("hibernate failed", "error", err)
				http.Error(w, "hibernation failed", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
			return
		}
	})
	mux.HandleFunc("POST /recover", func(w http.ResponseWriter, _ *http.Request) {
		state, fence := controller.Snapshot()
		if state != lifecycle.StateFailed {
			w.WriteHeader(http.StatusConflict)
			return
		}
		next, err := controller.AcknowledgeFailure(fence)
		if err != nil {
			logger.Error("acknowledge lifecycle failure", "error", err)
			http.Error(w, "recovery unavailable", http.StatusConflict)
			return
		}
		w.Header().Set("X-Lifecycle-Generation", strconv.FormatUint(next, 10))
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: *listen, Handler: requireControlToken(mux, *controlToken)}
	logger.Info("activator listening", "addr", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("activator stopped", "error", err)
		os.Exit(1)
	}
}

func requireControlToken(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/activate" || r.URL.Path == "/hibernate" || r.URL.Path == "/recover" {
			value := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if value == "" || value != token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type commandFlags struct {
	inspect, startPersistence, runMigration, startWorkers, startServers string
	drainServers, stopWorkers, stopPersistence, releaseStorage          string
}

func (f *commandFlags) bind(set *flag.FlagSet) {
	set.StringVar(&f.inspect, "cmd-inspect", "", "lifecycle inspect command (required)")
	set.StringVar(&f.startPersistence, "cmd-start-persistence", "", "start persistence command (required)")
	set.StringVar(&f.runMigration, "cmd-run-migration", "", "run migration command (required)")
	set.StringVar(&f.startWorkers, "cmd-start-workers", "", "start workers command (required)")
	set.StringVar(&f.startServers, "cmd-start-servers", "", "start servers command (required)")
	set.StringVar(&f.drainServers, "cmd-drain-servers", "", "drain servers command (required)")
	set.StringVar(&f.stopWorkers, "cmd-stop-workers", "", "stop workers command (required)")
	set.StringVar(&f.stopPersistence, "cmd-stop-persistence", "", "stop persistence command (required)")
	set.StringVar(&f.releaseStorage, "cmd-release-storage", "", "release active storage command (required)")
}

func (f commandFlags) set() lifecycle.CommandSet {
	return lifecycle.CommandSet{
		Inspect: lifecycle.Command{Name: f.inspect}, StartPersistence: lifecycle.Command{Name: f.startPersistence}, RunMigration: lifecycle.Command{Name: f.runMigration},
		StartWorkers: lifecycle.Command{Name: f.startWorkers}, StartServers: lifecycle.Command{Name: f.startServers}, DrainServers: lifecycle.Command{Name: f.drainServers},
		StopWorkers: lifecycle.Command{Name: f.stopWorkers}, StopPersistence: lifecycle.Command{Name: f.stopPersistence}, ReleaseActiveStorage: lifecycle.Command{Name: f.releaseStorage},
	}
}
