package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
)

// exitConfiguration and exitRuntime separate "the operator gave us something
// impossible" from "something failed while running".
const (
	exitConfiguration = 2
	exitRuntime       = 1
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Every teardown runs through run's defers; main is the only place that
	// exits, so the store is closed even on the documented failure exit that a
	// deployment platform restarts.
	if code := run(context.Background(), logger, os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, logger *slog.Logger, args []string) int {
	flags := flag.NewFlagSet("sameoldchat-socketmode-worker", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	backend := flags.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	dsn := flags.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flags.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flags.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flags.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flags.String("dqlite-database", "", "dqlite database name")
	appID := flags.String("app-id", "", "Socket Mode application ID (required)")
	owner := flags.String("owner", "", "unique worker owner ID (required)")
	responseURL := flags.String("response-url", "", "HTTP response destination (required)")
	limit := flags.Int("batch-size", 100, "bounded response batch size")
	lease := flags.Duration("lease", 30*time.Second, "durable response lease")
	retryDelay := flags.Duration("retry-delay", time.Second, "explicit retry delay after a delivery failure")
	poll := flags.Duration("poll", 250*time.Millisecond, "poll interval")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitConfiguration
	}
	if *backend == "" || *appID == "" || strings.TrimSpace(*owner) == "" || *responseURL == "" || *limit < 1 || *lease <= 0 || *retryDelay <= 0 || *poll <= 0 {
		logger.Error("Socket Mode response worker requires explicit storage, application, owner, destination, and positive timing settings")
		return exitConfiguration
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		return exitConfiguration
	}
	// The destination is validated before the store is opened: a rejected URL
	// must not leave a created database or a joined dqlite node behind.
	delivery, err := newHTTPResponseDelivery(*responseURL, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		logger.Error("configure response delivery", "error", err)
		return exitConfiguration
	}
	runtime, err := localchat.Open(ctx, localchat.Config{
		Backend:         localchat.Backend(*backend),
		DSN:             *dsn,
		DqliteDirectory: *dqliteDirectory,
		DqliteAddress:   *dqliteAddress,
		DqliteCluster:   cluster,
		DqliteDatabase:  *dqliteDatabase,
	})
	if err != nil {
		logger.Error("open response worker store", "error", err)
		return exitRuntime
	}
	defer func() {
		if err := runtime.Closer.Close(); err != nil {
			logger.Error("close response worker store", "error", err)
		}
	}()
	processor := socketmode.ResponseProcessor{
		Queue:      runtime.Service,
		AppID:      domain.AppID(*appID),
		Owner:      *owner,
		BatchSize:  *limit,
		Lease:      *lease,
		RetryDelay: *retryDelay,
	}
	workerContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		if workerContext.Err() != nil {
			return 0
		}
		if err := processor.ProcessOnce(workerContext, time.Now().UTC(), delivery); err != nil {
			if workerContext.Err() != nil {
				return 0
			}
			logger.Error("Socket Mode response processing failed", "error", err)
			var deliveryErr socketmode.ResponseDeliveryError
			if !errors.As(err, &deliveryErr) {
				return exitRuntime
			}
		}
		select {
		case <-workerContext.Done():
			return 0
		case <-ticker.C:
		}
	}
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newHTTPResponseDelivery(target string, client httpDoer) (socketmode.ResponseHandler, error) {
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("response URL is invalid: %w", err)
	}
	if request.URL.Scheme == "" || request.URL.Host == "" {
		return nil, errors.New("response URL must be absolute")
	}
	if client == nil {
		return nil, errors.New("response HTTP client is required")
	}
	return func(ctx context.Context, response domain.SocketModeResponse) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(response.Payload))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", string(response.AppID)+":"+response.EnvelopeID)
		request.Header.Set("X-SameOldChat-App-ID", string(response.AppID))
		request.Header.Set("X-SameOldChat-Envelope-ID", response.EnvelopeID)
		result, err := client.Do(request)
		if err != nil {
			return err
		}
		defer result.Body.Close()
		if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("response destination returned HTTP %d", result.StatusCode)
		}
		return nil
	}, nil
}
