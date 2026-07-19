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
	"strings"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
)

func main() {
	backend := flag.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	dsn := flag.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name")
	workspace := flag.String("workspace", "", "workspace ID (required)")
	deliveryURL := flag.String("delivery-url", "", "HTTP event delivery URL (required)")
	deliveryFormat := flag.String("delivery-format", "", "delivery format: record or slack-events (required)")
	appID := flag.String("app-id", "", "Slack application ID (required for slack-events delivery)")
	signingSecret := flag.String("signing-secret", "", "Slack signing secret (required for slack-events delivery)")
	owner := flag.String("owner", "", "unique worker owner ID (required)")
	limit := flag.Int("batch-size", 100, "bounded event batch size")
	lease := flag.Duration("lease", 30*time.Second, "durable delivery lease")
	poll := flag.Duration("poll", 250*time.Millisecond, "poll interval")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *backend == "" || *workspace == "" || *deliveryURL == "" || *deliveryFormat == "" || *owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0 {
		logger.Error("worker requires explicit store, workspace, delivery URL, delivery format, owner, and positive limits")
		os.Exit(2)
	}
	if *deliveryFormat != "record" && *deliveryFormat != "slack-events" {
		logger.Error("worker delivery format is unsupported", "format", *deliveryFormat)
		os.Exit(2)
	}
	if *deliveryFormat == "slack-events" && (*appID == "" || *signingSecret == "") {
		logger.Error("slack-events delivery requires app ID and signing secret")
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
	var delivery outbox.Delivery
	if *deliveryFormat == "record" {
		delivery, err = newHTTPDelivery(*deliveryURL)
	} else {
		delivery, err = newSlackEventDelivery(*deliveryURL, *appID, *signingSecret)
	}
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
	if err := validateDeliveryTarget(target); err != nil {
		return nil, err
	}
	return newHTTPDeliveryWithClient(target, &http.Client{Timeout: 30 * time.Second})
}

func newSlackEventDelivery(target, appID, signingSecret string) (outbox.Delivery, error) {
	if err := validateDeliveryTarget(target); err != nil {
		return nil, err
	}
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(signingSecret) == "" {
		return nil, errors.New("Slack event delivery requires app ID and signing secret")
	}
	return newSlackEventDeliveryWithClient(target, appID, signingSecret, &http.Client{Timeout: 30 * time.Second})
}

func validateDeliveryTarget(target string) error {
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(nil))
	if err != nil {
		return fmt.Errorf("delivery URL is invalid: %w", err)
	}
	if request.URL.Scheme == "" || request.URL.Host == "" {
		return errors.New("delivery URL must be absolute")
	}
	return nil
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newHTTPDeliveryWithClient(target string, client httpDoer) (outbox.Delivery, error) {
	if client == nil {
		return nil, errors.New("delivery HTTP client is required")
	}
	return func(ctx context.Context, record events.Record) error {
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
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("delivery returned HTTP %d", response.StatusCode)
		}
		return nil
	}, nil
}

func newSlackEventDeliveryWithClient(target, appID, signingSecret string, client httpDoer) (outbox.Delivery, error) {
	if client == nil {
		return nil, errors.New("delivery HTTP client is required")
	}
	if err := validateDeliveryTarget(target); err != nil {
		return nil, err
	}
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(signingSecret) == "" {
		return nil, errors.New("Slack event delivery requires app ID and signing secret")
	}
	return func(ctx context.Context, record events.Record) error {
		body, err := events.SlackEventBody(record, appID)
		if err != nil {
			return err
		}
		timestamp := time.Now().UTC()
		signature, err := events.SlackSignature(signingSecret, timestamp, body)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Slack-Request-Timestamp", fmt.Sprint(timestamp.Unix()))
		req.Header.Set("X-Slack-Signature", signature)
		req.Header.Set("Idempotency-Key", string(record.Event.ID))
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Slack event delivery returned HTTP %d", response.StatusCode)
		}
		return nil
	}, nil
}
