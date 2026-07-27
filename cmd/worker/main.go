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

// exitConfiguration and exitRuntime separate "the operator gave us something
// impossible" from "something failed while running".
const (
	exitConfiguration = 2
	exitRuntime       = 1
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Every teardown runs through run's defers; main is the only place that
	// exits, so the store is closed on every path, including the failure budget
	// and a rejected configuration.
	if code := run(context.Background(), logger, os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, logger *slog.Logger, args []string) int {
	flags := flag.NewFlagSet("sameoldchat-worker", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	backend := flags.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	dsn := flags.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flags.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flags.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flags.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flags.String("dqlite-database", "", "dqlite database name")
	workspace := flags.String("workspace", "", "workspace ID (required)")
	deliveryURL := flags.String("delivery-url", "", "HTTP event delivery URL (required)")
	deliveryFormat := flags.String("delivery-format", "", "delivery format: record or slack-events (required)")
	appID := flags.String("app-id", "", "Slack application ID (required for and specific to slack-events delivery)")
	signingSecret := flags.String("signing-secret", "", "Slack signing secret (required for and specific to slack-events delivery)")
	owner := flags.String("owner", "", "unique worker owner ID (required)")
	limit := flags.Int("batch-size", 100, "bounded event batch size")
	lease := flags.Duration("lease", 30*time.Second, "durable delivery lease")
	poll := flags.Duration("poll", 250*time.Millisecond, "poll interval")
	// A worker that cannot reach its store must fail, not log the same error
	// forever: a supervisor restarts a failed process and an alert fires on a
	// restarting process, while an endlessly retrying one looks healthy while the
	// outbox never drains.
	failureBudget := flags.Int("max-consecutive-failures", 20, "consecutive failed poll cycles tolerated before the worker exits")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitConfiguration
	}
	if *backend == "" || *workspace == "" || *deliveryURL == "" || *deliveryFormat == "" || *owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0 || *failureBudget <= 0 {
		logger.Error("worker requires explicit store, workspace, delivery URL, delivery format, owner, and positive limits")
		return exitConfiguration
	}
	if *deliveryFormat != "record" && *deliveryFormat != "slack-events" {
		logger.Error("worker delivery format is unsupported", "format", *deliveryFormat, "allowed", "record, slack-events")
		return exitConfiguration
	}
	if *deliveryFormat == "slack-events" && (*appID == "" || *signingSecret == "") {
		logger.Error("slack-events delivery requires app ID and signing secret")
		return exitConfiguration
	}
	// record delivery signs nothing and names no application, so an app ID or a
	// signing secret supplied with it would be accepted and never used. Silently
	// ignoring a Slack signing secret is how a deployment believes it is signing
	// deliveries that leave unsigned.
	if *deliveryFormat == "record" {
		if ignored := explicitlySet(flags, "app-id", "signing-secret"); len(ignored) != 0 {
			logger.Error("record delivery cannot honour Slack application settings", "ignored", strings.Join(ignored, ", "), "hint", "use -delivery-format slack-events")
			return exitConfiguration
		}
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		return exitConfiguration
	}
	// Delivery is configured before the store is opened: it validates the
	// destination, and a rejected destination must not leave a created database
	// or a joined dqlite node behind.
	var delivery outbox.Delivery
	if *deliveryFormat == "record" {
		delivery, err = newHTTPDelivery(*deliveryURL)
	} else {
		delivery, err = newSlackEventDelivery(*deliveryURL, *appID, *signingSecret)
	}
	if err != nil {
		logger.Error("configure delivery", "error", err)
		return exitConfiguration
	}
	runtime, err := localchat.Open(ctx, localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase})
	if err != nil {
		logger.Error("open worker store", "error", err)
		return exitRuntime
	}
	defer func() {
		if err := runtime.Closer.Close(); err != nil {
			logger.Error("close worker store", "error", err)
		}
	}()
	worker, err := outbox.NewWorker(runtime.OutboxSource, *owner, *limit, *lease, delivery)
	if err != nil {
		logger.Error("configure outbox worker", "error", err)
		return exitConfiguration
	}
	scheduledWorker, err := scheduler.NewWorker(runtime.ScheduledSource, runtime.Service, *owner, *limit, *lease)
	if err != nil {
		logger.Error("configure scheduled worker", "error", err)
		return exitConfiguration
	}
	workerContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cycle := func(cycleContext context.Context) (bool, error) {
		var failures error
		count, err := worker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if err != nil {
			failures = errors.Join(failures, err)
			logger.Error("outbox delivery failed", "count", count, "error", err)
		}
		scheduledCount, scheduledErr := scheduledWorker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if scheduledErr != nil {
			failures = errors.Join(failures, scheduledErr)
			logger.Error("scheduled message execution failed", "count", scheduledCount, "error", scheduledErr)
		}
		return count > 0 || scheduledCount > 0, failures
	}
	return pollWithinFailureBudget(workerContext, logger, cycle, *poll, *failureBudget)
}

// pollWithinFailureBudget runs cycle until the context ends or the worker has
// failed budget times without making progress in between.
//
// The budget exists because the loop used to log every failure and try again
// forever: a store that was gone, a credential that had been rotated, or a
// destination that had been retired produced an identical line every poll
// interval while the process stayed "up", so nothing restarted it and nothing
// alerted. A supervisor restarts a failed process and an alert fires on a
// restarting one; an endlessly retrying one looks healthy while the outbox never
// drains.
//
// The counter is reset by *progress*, not by a quiet cycle. A failed delivery is
// released with a retry time a full lease in the future, so the very next poll
// claims nothing, returns no error, and would reset a counter that the failure
// had just incremented. With the shipped -poll 250ms and -lease 30s that is
// roughly a hundred and twenty empty cycles between every pair of failures, so a
// permanently retired destination never exceeded one consecutive failure and the
// budget the comment above describes never fired for the case it names. Counting
// consecutive failures with no delivered record in between makes the budget
// measure what it claims to measure: retrying without ever succeeding.
//
// Context cancellation is a shutdown, not a failure.
func pollWithinFailureBudget(ctx context.Context, logger *slog.Logger, cycle func(context.Context) (bool, error), poll time.Duration, budget int) int {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	consecutiveFailures := 0
	for {
		if ctx.Err() != nil {
			return 0
		}
		progressed, err := cycle(ctx)
		if ctx.Err() != nil {
			return 0
		}
		switch {
		case err != nil:
			consecutiveFailures++
			if consecutiveFailures >= budget {
				logger.Error("worker exhausted its failure budget", "consecutive_failures", consecutiveFailures, "budget", budget)
				return exitRuntime
			}
		case progressed:
			consecutiveFailures = 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
	}
}

// explicitlySet names the flags the operator supplied on the command line. A
// flag with a default cannot be distinguished from an unset one by value, so
// rejecting a setting the selected mode cannot honour needs the parsed set.
func explicitlySet(flags *flag.FlagSet, names ...string) []string {
	supplied := make(map[string]bool, len(names))
	flags.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	found := make([]string, 0, len(names))
	for _, name := range names {
		if supplied[name] {
			found = append(found, "-"+name)
		}
	}
	return found
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

// permanentDeliveryFailure reports whether an encode failure describes the
// record rather than the destination. Transport and status failures stay
// retryable: a receiver that is misconfigured today may accept the same body
// tomorrow, and dropping a committed event for it would lose data.
func permanentDeliveryFailure(err error) bool {
	return errors.Is(err, events.ErrPayloadInternal) || errors.Is(err, events.ErrPayloadRecipientScoped) || errors.Is(err, events.ErrPayloadMalformed) || errors.Is(err, events.ErrEventIncomplete)
}

// classifyEncodeFailure applies one policy to every delivery format. A record
// this destination can never accept is reported as permanent so the outbox
// drains and the operator sees which record was dropped; anything else stays
// retryable. Both formats classify through here so a new format cannot inherit
// half the policy.
func classifyEncodeFailure(record events.Record, err error) error {
	if permanentDeliveryFailure(err) {
		// Both sentinels are wrapped: the worker classifies on ErrPermanent while
		// an operator, a log filter and a test all need to see which payload rule
		// refused the record.
		return fmt.Errorf("%w: event %s topic %s: %w", outbox.ErrPermanent, record.Event.ID, record.Event.Topic, err)
	}
	return err
}

func newHTTPDeliveryWithClient(target string, client httpDoer) (outbox.Delivery, error) {
	if client == nil {
		return nil, errors.New("delivery HTTP client is required")
	}
	return func(ctx context.Context, record events.Record) error {
		// Encoding the durable record is what publishes it. events.Event refuses
		// to encode a record no audience may receive — an internal worker record,
		// or one addressed to a single user — so this format cannot ship a
		// recipient's message text to a third-party URL even though it never
		// decodes the payload. The refusal carries a sentinel, so it is dropped
		// and reported rather than retried forever.
		body, err := json.Marshal(record)
		if err != nil {
			return classifyEncodeFailure(record, err)
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
		// A record that cannot be encoded for Slack will never encode: it is
		// either an internal worker record, a record addressed to a single user,
		// or a payload written before the typed payload contract. Retrying it
		// forever hides the problem and stops the outbox from draining, so it is
		// reported as permanent and the worker drops it after logging.
		body, err := events.SlackEventBody(record, appID)
		if err != nil {
			return classifyEncodeFailure(record, err)
		}
		timestamp := time.Now().UTC()
		// The signature covers the exact bytes sent below. Re-encoding the
		// envelope for signing would produce a MAC over bytes the receiver never
		// sees whenever the two encodings differ in key order or spacing, and
		// every delivery would be rejected as unsigned.
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
