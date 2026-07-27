package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sameoldchat/sameoldchat/internal/activator"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
)

// unauthenticatedPatterns is an allow-list of the mux patterns this process
// serves without the deployment control-plane token, keyed by the pattern the
// route was registered with rather than by the request path.
//
// It is an allow-list because the previous deny-list ("/activate",
// "/hibernate", "/recover", "/metrics") protected only the endpoints somebody
// remembered to add to it: /metrics had been publishing lifecycle state,
// snapshot sizes, and the fencing generation on the same public listener as
// forwarded application traffic until it was added, and the next endpoint
// registered on this mux would have been unauthenticated in exactly the same
// way. With an allow-list a new route is protected until it is deliberately
// listed here.
//
// The two entries are the ones that must stay open: the catch-all is forwarded
// application traffic, which the active stack authenticates itself, and the
// liveness probe is what a load balancer polls before any token exists. The
// probe answers {"ok":true} and nothing else — leaving the lifecycle state and
// the fencing generation on it reinstated two thirds of the /metrics leak on the
// same public listener, which specs/scale-to-zero.md:189 forbids. They are
// served by GET /lifecycle, which is absent from this list and therefore
// requires the token.
var unauthenticatedPatterns = map[string]bool{"/": true, "GET /healthz": true}

// exitConfiguration and exitRuntime separate "the operator gave us something
// impossible" from "something failed while running".
const (
	exitConfiguration = 2
	exitRuntime       = 1
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Every teardown runs through run's defers; main is the only place that
	// exits, so the control store and spool are always closed cleanly.
	if code := run(logger); code != 0 {
		os.Exit(code)
	}
}

func run(logger *slog.Logger) int {
	listen := flag.String("listen", "", "activator listen address (required)")
	stateDB := flag.String("state-db", "", "durable lifecycle control SQLite DSN (required)")
	forwardURL := flag.String("forward-url", "", "active stack HTTP URL (required)")
	controlToken := flag.String("control-token", "", "control-plane bearer token for /activate (required)")
	snapshotRoot := flag.String("snapshot-root", "", "absolute filesystem snapshot root (required for -snapshot-store=filesystem)")
	snapshotStore := flag.String("snapshot-store", "", "snapshot store: filesystem or s3 (required)")
	snapshotS3Bucket := flag.String("snapshot-s3-bucket", "", "Amazon Simple Storage Service bucket for snapshots")
	snapshotS3Prefix := flag.String("snapshot-s3-prefix", "", "Amazon Simple Storage Service key prefix for snapshots")
	snapshotMode := flag.String("snapshot-mode", "", "snapshot mode: file or directory (required)")
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
	// One limit, one flag: the spooled body cap and the captured response cap
	// must agree with each other, so they are not two independent constants.
	maxRequestBytes := flag.Int64("request-max-bytes", 4<<20, "maximum spooled request body and captured response bytes")
	maxSnapshotBytes := flag.Int64("snapshot-max-bytes", 0, "maximum snapshot plaintext bytes (required)")
	wakeDeadline := flag.Duration("wake-deadline", 2*time.Minute, "maximum time a caller waits for a cold start")
	wakeSafetyMargin := flag.Duration("wake-safety-margin", 5*time.Minute, "measured restore time plus margin reserved before a scheduled wake deadline")
	commands := commandFlags{}
	commands.bind(flag.CommandLine)
	flag.Parse()
	if *listen == "" || *stateDB == "" || *forwardURL == "" || *controlToken == "" || *snapshotStore == "" || *snapshotMode == "" || *snapshotSource == "" || *snapshotOutput == "" || *backend == "" || *schemaVersion < 1 || *applicationVersion == "" || *keyID == "" || *encryptionKey == "" || *signingKey == "" || *spoolKey == "" || *spoolOwner == "" || *spoolMaxBytes <= 0 || *spoolMaxRequests <= 0 || *maxSnapshotBytes <= 0 || *maxRequestBytes <= 0 || *wakeDeadline <= 0 || *wakeSafetyMargin <= 0 {
		logger.Error("activator requires explicit listen, control, snapshot, and version settings")
		return exitConfiguration
	}
	// No decode error is logged with any of these three keys: encoding/hex reports
	// the offending byte of its input ("invalid byte: U+007A 'z'"), which writes
	// part of a secret into the process log. The fixed message already says what
	// the operator must supply.
	encryption, err := hex.DecodeString(*encryptionKey)
	if err != nil || len(encryption) != 32 {
		logger.Error("snapshot encryption key must be 32 bytes of hex")
		return exitConfiguration
	}
	signing, err := hex.DecodeString(*signingKey)
	if err != nil || len(signing) < 32 {
		logger.Error("snapshot signing key must contain at least 32 bytes of hex")
		return exitConfiguration
	}
	spoolEncryption, err := hex.DecodeString(*spoolKey)
	if err != nil || len(spoolEncryption) != 32 {
		logger.Error("request spool key must be 32 bytes of hex")
		return exitConfiguration
	}
	target, err := url.Parse(*forwardURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		logger.Error("forward URL must be an absolute URL", "error", err)
		return exitConfiguration
	}
	control, err := lifecycle.OpenSQLiteStateStore(*stateDB, lifecycle.StateRecord{State: lifecycle.StateHibernated})
	if err != nil {
		logger.Error("open lifecycle control store", "error", err)
		return exitRuntime
	}
	defer control.Close()
	controller, err := lifecycle.NewPersistent(control)
	if err != nil {
		logger.Error("load lifecycle state", "error", err)
		return exitRuntime
	}
	manager, code, err := snapshotManager(*snapshotStore, *snapshotRoot, *snapshotS3Bucket, *snapshotS3Prefix, encryption, signing, *keyID, *maxSnapshotBytes)
	if err != nil {
		logger.Error("configure snapshot manager", "error", err)
		return code
	}
	metadata := lifecycle.Manifest{Backend: *backend, SchemaVersion: *schemaVersion, ApplicationVersion: *applicationVersion, MinRestorerVersion: *applicationVersion, MaxRestorerVersion: *applicationVersion}
	var snapshots lifecycle.Snapshotter
	switch *snapshotMode {
	case "file":
		selected, selectErr := lifecycle.NewFileSnapshotter(manager, *snapshotSource, *snapshotOutput, metadata)
		if selectErr != nil {
			logger.Error("configure file snapshotter", "error", selectErr)
			return exitConfiguration
		}
		snapshots = selected
	case "directory":
		selected, selectErr := lifecycle.NewDirectorySnapshotter(manager, *snapshotSource, *snapshotOutput, metadata, lifecycle.DirectorySnapshotSourceStopped)
		if selectErr != nil {
			logger.Error("configure directory snapshotter", "error", selectErr)
			return exitConfiguration
		}
		snapshots = selected
	default:
		logger.Error("snapshot mode must be file or directory", "snapshot_mode", *snapshotMode)
		return exitConfiguration
	}
	driver, err := lifecycle.NewCommandDriver(lifecycle.OSCommandRunner{}, commands.set())
	if err != nil {
		logger.Error("configure lifecycle commands", "error", err)
		return exitConfiguration
	}
	metrics := observability.NewRegistry()
	coordinator, err := lifecycle.NewCoordinator(controller, driver, snapshots, metrics, *wakeSafetyMargin)
	if err != nil {
		logger.Error("configure lifecycle coordinator", "error", err)
		return exitConfiguration
	}
	// Signals are installed before recovery, not after it. Recovery downloads and
	// restores a snapshot, so an unreachable object store parks it for as long as
	// the provider's own timeouts allow; with the handler installed afterwards,
	// SIGTERM did nothing at all until it returned. cmd/server establishes its
	// signal context before any durable resource for the same reason.
	applicationContext, stopApplication := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopApplication()
	if err := coordinator.Recover(applicationContext); err != nil && !errors.Is(err, lifecycle.ErrRecoveryRequired) {
		// A recovery abandoned because the operator stopped the task is a clean
		// stop, not a failure: reporting it as one makes a rolling deploy record a
		// task failure and can trip the deployment circuit breaker.
		if applicationContext.Err() != nil {
			logger.Info("lifecycle recovery interrupted by shutdown")
			return 0
		}
		logger.Error("recover lifecycle state", "error", err)
		return exitRuntime
	} else if errors.Is(err, lifecycle.ErrRecoveryRequired) {
		logger.Error("lifecycle state requires explicit recovery; serving operator endpoints only", "error", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	spool, err := activator.OpenSQLiteSpool(*stateDB, spoolEncryption, activator.SpoolLimits{MaxBodyBytes: *maxRequestBytes, MaxQueuedBytes: *spoolMaxBytes, MaxQueuedRequests: *spoolMaxRequests})
	if err != nil {
		logger.Error("configure request spool", "error", err)
		return exitConfiguration
	}
	defer spool.Close()
	handler, err := activator.NewDurableForwardingHandler(applicationContext, controller, coordinator.WakeAt, proxy, spool, *spoolOwner, *maxRequestBytes, *wakeDeadline, metrics)
	if err != nil {
		logger.Error("configure forwarding activator", "error", err)
		return exitConfiguration
	}
	defer handler.Close()
	go scheduledWakeLoop(applicationContext, controller, handler, *wakeSafetyMargin, logger)
	mux := http.NewServeMux()
	handler.RegisterForwarding(mux)
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("POST /hibernate", hibernateHandler(controller, coordinator, logger))
	mux.HandleFunc("POST /recover", recoverHandler(controller, logger))
	mux.HandleFunc("POST /restore", restoreHandler(controller, coordinator, logger))
	mux.HandleFunc("POST /wake-deadline", wakeDeadlineHandler(controller, logger))
	// No WriteTimeout: this listener proxies application responses, including
	// long-lived streams. ReadHeaderTimeout and IdleTimeout bound the header and
	// connection lifetimes an unauthenticated client can hold open.
	server := &http.Server{
		Addr:              *listen,
		Handler:           requireControlToken(mux, *controlToken),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}
	logger.Info("activator listening", "addr", *listen)
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("activator stopped", "error", err)
			return exitRuntime
		}
	case <-applicationContext.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("activator shutdown failed", "error", err)
			return exitRuntime
		}
	}
	return 0
}

func snapshotManager(store, root, bucket, prefix string, encryption, signing []byte, keyID string, maxBytes int64) (lifecycle.SnapshotManager, int, error) {
	switch store {
	case "filesystem":
		if root == "" || bucket != "" || prefix != "" {
			return lifecycle.SnapshotManager{}, exitConfiguration, errors.New("filesystem snapshot store requires a snapshot root and rejects Amazon Simple Storage Service settings")
		}
		manager, err := lifecycle.NewSnapshotManager(root, encryption, signing, keyID, maxBytes)
		return manager, exitConfiguration, err
	case "s3":
		if root != "" || bucket == "" {
			return lifecycle.SnapshotManager{}, exitConfiguration, errors.New("Amazon Simple Storage Service snapshot store requires a bucket and rejects a snapshot root")
		}
		awsConfig, err := awsconfig.LoadDefaultConfig(context.Background())
		if err != nil {
			return lifecycle.SnapshotManager{}, exitRuntime, fmt.Errorf("load Amazon Simple Storage Service configuration: %w", err)
		}
		// The artifact carries a nonce and a MAC on top of the plaintext, so the
		// object store must accept the larger encrypted size.
		objectStore, err := blob.NewS3(s3.NewFromConfig(awsConfig), bucket, prefix, maxBytes+48)
		if err != nil {
			return lifecycle.SnapshotManager{}, exitConfiguration, err
		}
		manager, err := lifecycle.NewObjectSnapshotManager(objectStore, encryption, signing, keyID, maxBytes)
		return manager, exitConfiguration, err
	default:
		return lifecycle.SnapshotManager{}, exitConfiguration, fmt.Errorf("snapshot store must be filesystem or s3, got %q", store)
	}
}

// scheduledWakeLoop wakes a hibernated stack early enough to meet the earliest
// deadline the application published before shutdown, including the measured
// restore budget. Without it a scheduled message only fires when unrelated
// traffic happens to wake the stack.
func scheduledWakeLoop(ctx context.Context, controller *lifecycle.Controller, handler activator.Handler, margin time.Duration, logger *slog.Logger) {
	interval := margin / 10
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		deadline := controller.Metadata().WakeDeadline
		if deadline.IsZero() || time.Until(deadline) > margin {
			continue
		}
		if state, _ := controller.Snapshot(); state != lifecycle.StateHibernated {
			continue
		}
		logger.Info("scheduled wake deadline reached", "deadline", deadline)
		if err := handler.Wake(ctx); err != nil && ctx.Err() == nil {
			logger.Error("scheduled wake failed", "error", err, "deadline", deadline)
		}
	}
}

func hibernateHandler(controller *lifecycle.Controller, coordinator lifecycle.Coordinator, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
			// A refused hibernation is a handled, expected answer, not a
			// gateway failure: the stack is still serving and eligible later.
			if errors.Is(err, lifecycle.ErrWakeDeadlineWithinSafetyWindow) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "scheduled wake deadline is inside the safety window", http.StatusConflict)
				return
			}
			if err != nil {
				logger.Error("hibernate failed", "error", err)
				http.Error(w, "hibernation failed", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
			return
		}
	}
}

// wakeDeadlineHandler receives the earliest necessary wake time from the running
// stack, which specs/scale-to-zero.md requires it to publish before shutdown.
//
// The consume side already existed — scheduledWakeLoop wakes on it and Hibernate
// refuses inside its safety window — but the value was never produced by anything
// in production, so a hibernated stack fired a due scheduled message only when
// unrelated traffic happened to wake it. The application and the activator are
// different processes with different stores, so the hint crosses the same
// authenticated control boundary as every other lifecycle operation.
//
// The write is fenced. A worker that belongs to a generation the stack has moved
// past must not be able to reinstate a deadline the current generation cleared.
func wakeDeadlineHandler(controller *lifecycle.Controller, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fence, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("generation")), 10, 64)
		if err != nil {
			http.Error(w, "wake deadline requires a generation query parameter", http.StatusBadRequest)
			return
		}
		// An absent deadline clears the hint. That is the ordinary case once the
		// scheduled work has run, and leaving a served deadline behind would
		// refuse every later hibernation inside its safety window.
		var deadline time.Time
		if raw := strings.TrimSpace(r.URL.Query().Get("deadline")); raw != "" {
			deadline, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				http.Error(w, "wake deadline must be an RFC 3339 timestamp", http.StatusBadRequest)
				return
			}
		}
		if err := controller.SetWakeDeadline(fence, deadline); err != nil {
			if errors.Is(err, lifecycle.ErrStaleFence) {
				w.Header().Set("Retry-After", "5")
				http.Error(w, "wake deadline generation is stale", http.StatusConflict)
				return
			}
			logger.Error("publish wake deadline", "error", err, "generation", fence)
			http.Error(w, "wake deadline unavailable", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// restoreHandler is the explicit, authenticated selection of an older known-good
// snapshot generation that specs/scale-to-zero.md requires. The coordinator no
// longer walks back through generations by itself: a silent rollback to data
// hours or days old, reported as a healthy ACTIVE stack, is the same class of
// surprise as restoring over a live volume.
//
// It is the only route that can overwrite an active volume whose contents no
// snapshot holds, so the generation is named by the operator, is checked to be
// verified and known-good in its own right, and is fenced by its own generation.
func restoreHandler(controller *lifecycle.Controller, coordinator lifecycle.Coordinator, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		generation, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("generation")), 10, 64)
		if err != nil || generation == 0 {
			http.Error(w, "restore requires a positive generation query parameter", http.StatusBadRequest)
			return
		}
		state, fence := controller.Snapshot()
		if state != lifecycle.StateFailed && state != lifecycle.StateHibernated {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "an operator restore requires a failed or hibernated stack", http.StatusConflict)
			return
		}
		// The selection is recorded with the transition, so the restore that runs
		// under this fence can only be the generation the operator named, and a
		// refusal or a crash leaves no standing permission behind.
		restoreFence, err := controller.BeginOperatorRestore(fence, generation)
		if err != nil {
			logger.Error("begin operator restore", "error", err, "generation", generation)
			http.Error(w, "restore unavailable", http.StatusConflict)
			return
		}
		result := make(chan error, 1)
		go func() {
			// The work outlives the request context deliberately: a client that
			// times out must not cancel a restore that owns the fence, exactly as
			// it must not cancel a shared wake.
			if err := coordinator.RestoreAt(context.Background(), restoreFence, generation); err != nil {
				result <- errors.Join(err, controller.Fail(restoreFence))
				return
			}
			result <- controller.Activate(restoreFence)
		}()
		select {
		case err := <-result:
			if errors.Is(err, lifecycle.ErrGenerationNotRestorable) || errors.Is(err, lifecycle.ErrNoVerifiedSnapshot) {
				// The operator named a generation this deployment cannot restore.
				// That is a handled answer about the request, not a server fault.
				logger.Error("operator restore rejected", "error", err, "generation", generation)
				http.Error(w, "selected generation is not a verified known-good snapshot", http.StatusConflict)
				return
			}
			if err != nil {
				logger.Error("operator restore failed", "error", err, "generation", generation)
				http.Error(w, "restore failed", http.StatusBadGateway)
				return
			}
			w.Header().Set("X-Lifecycle-Generation", strconv.FormatUint(restoreFence, 10))
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
			return
		}
	}
}

func recoverHandler(controller *lifecycle.Controller, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
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
	}
}

// requireControlToken guards every route the activator itself serves.
//
// It takes the mux rather than an http.Handler so it can ask which registered
// pattern a request matched: an activator-owned endpoint has its own pattern and
// requires the token, while forwarded application traffic matches the catch-all
// and does not. That inverts the previous path deny-list, where an endpoint was
// unauthenticated until someone remembered to name it.
func requireControlToken(mux *http.ServeMux, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if !unauthenticatedPatterns[pattern] && !validControlToken(r.Header.Get("Authorization"), token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func validControlToken(header, expected string) bool {
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(header, bearerPrefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	expected = strings.TrimSpace(expected)
	if provided == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
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
