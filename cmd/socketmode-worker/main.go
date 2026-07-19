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

func main() {
	backend := flag.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	dsn := flag.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name")
	appID := flag.String("app-id", "", "Socket Mode application ID (required)")
	owner := flag.String("owner", "", "unique worker owner ID (required)")
	responseURL := flag.String("response-url", "", "HTTP response destination (required)")
	limit := flag.Int("batch-size", 100, "bounded response batch size")
	lease := flag.Duration("lease", 30*time.Second, "durable response lease")
	retryDelay := flag.Duration("retry-delay", time.Second, "explicit retry delay after a delivery failure")
	poll := flag.Duration("poll", 250*time.Millisecond, "poll interval")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *backend == "" || *appID == "" || strings.TrimSpace(*owner) == "" || *responseURL == "" || *limit < 1 || *lease <= 0 || *retryDelay <= 0 || *poll <= 0 {
		logger.Error("Socket Mode response worker requires explicit storage, application, owner, destination, and positive timing settings")
		os.Exit(2)
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		os.Exit(2)
	}
	runtime, err := localchat.Open(context.Background(), localchat.Config{
		Backend:         localchat.Backend(*backend),
		DSN:             *dsn,
		DqliteDirectory: *dqliteDirectory,
		DqliteAddress:   *dqliteAddress,
		DqliteCluster:   cluster,
		DqliteDatabase:  *dqliteDatabase,
	})
	if err != nil {
		logger.Error("open response worker store", "error", err)
		os.Exit(1)
	}
	defer runtime.Closer.Close()
	delivery, err := newHTTPResponseDelivery(*responseURL, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		logger.Error("configure response delivery", "error", err)
		os.Exit(2)
	}
	processor := socketmode.ResponseProcessor{
		Queue:      runtime.Service,
		AppID:      domain.AppID(*appID),
		Owner:      *owner,
		BatchSize:  *limit,
		Lease:      *lease,
		RetryDelay: *retryDelay,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		if err := processor.ProcessOnce(ctx, time.Now().UTC(), delivery); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("Socket Mode response processing failed", "error", err)
			var deliveryErr socketmode.ResponseDeliveryError
			if !errors.As(err, &deliveryErr) {
				os.Exit(1)
			}
		}
		select {
		case <-ctx.Done():
			return
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
