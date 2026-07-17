package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
)

func main() {
	backend := flag.String("store", "", "storage backend: memory, sqlite, or dqlite (required)")
	dsn := flag.String("db", "", "SQLite DSN; required for sqlite")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name")
	workspace := flag.String("workspace", "", "workspace ID (required)")
	deliveryURL := flag.String("delivery-url", "", "HTTP event delivery URL (required)")
	owner := flag.String("owner", "", "unique worker owner ID (required)")
	limit := flag.Int("batch-size", 100, "bounded event batch size")
	lease := flag.Duration("lease", 30*time.Second, "durable delivery lease")
	poll := flag.Duration("poll", 250*time.Millisecond, "poll interval")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *backend == "" || *workspace == "" || *deliveryURL == "" || *owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0 {
		logger.Error("worker requires explicit store, workspace, delivery URL, owner, and positive limits")
		os.Exit(2)
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		os.Exit(2)
	}
	runtime, err := localchat.Open(context.Background(), localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase})
	if err != nil {
		logger.Error("open worker store", "error", err)
		os.Exit(1)
	}
	defer runtime.Closer.Close()
	delivery, err := newHTTPDelivery(*deliveryURL)
	if err != nil {
		logger.Error("configure delivery", "error", err)
		os.Exit(2)
	}
	worker, err := outbox.NewWorker(runtime.OutboxSource, *owner, *limit, *lease, delivery)
	if err != nil {
		logger.Error("configure outbox worker", "error", err)
		os.Exit(2)
	}
	scheduledWorker, err := scheduler.NewWorker(runtime.ScheduledSource, runtime.Service, *owner, *limit, *lease)
	if err != nil {
		logger.Error("configure scheduled worker", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		count, err := worker.RunOnce(ctx, domain.WorkspaceID(*workspace))
		if err != nil {
			logger.Error("outbox delivery failed", "count", count, "error", err)
		}
		scheduledCount, scheduledErr := scheduledWorker.RunOnce(ctx, domain.WorkspaceID(*workspace))
		if scheduledErr != nil {
			logger.Error("scheduled message execution failed", "count", scheduledCount, "error", scheduledErr)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func newHTTPDelivery(target string) (outbox.Delivery, error) {
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("delivery URL is invalid: %w", err)
	}
	if request.URL.Scheme == "" || request.URL.Host == "" {
		return nil, errors.New("delivery URL must be absolute")
	}
	return newHTTPDeliveryWithClient(target, &http.Client{Timeout: 30 * time.Second})
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newHTTPDeliveryWithClient(target string, client httpDoer) (outbox.Delivery, error) {
	if client == nil {
		return nil, errors.New("delivery HTTP client is required")
	}
	return func(ctx context.Context, record events.Record) (returnErr error) {
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", string(record.Event.ID))
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer func() {
			returnErr = errors.Join(returnErr, response.Body.Close())
		}()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("delivery returned HTTP %d", response.StatusCode)
		}
		return nil
	}, nil
}
