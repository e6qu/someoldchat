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
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/slackapp"
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
	backend := flags.String("store", os.Getenv("SAMEOLDCHAT_STORE"), "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	dsn := flags.String("db", os.Getenv("SAMEOLDCHAT_DATABASE_URL"), "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flags.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flags.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flags.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flags.String("dqlite-database", "", "dqlite database name")
	workspace := flags.String("workspace", "", "workspace ID (required for record delivery)")
	deliveryURL := flags.String("delivery-url", "", "HTTP event delivery URL (required for record delivery)")
	deliveryFormat := flags.String("delivery-format", "", "delivery format: record or slack-events (required)")
	flags.String("app-id", "", "deprecated manual Slack application ID; manifest-driven slack-events delivery rejects it")
	flags.String("signing-secret", "", "deprecated plaintext Slack signing secret; manifest-driven slack-events delivery rejects it")
	appCredentialKeyHex := flags.String("app-credential-key-hex", os.Getenv("SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX"), "AES-256 key used to decrypt application signing credentials")
	owner := flags.String("owner", "", "unique worker owner ID (required)")
	limit := flags.Int("batch-size", 100, "bounded event batch size")
	lease := flags.Duration("lease", 30*time.Second, "durable delivery lease")
	poll := flags.Duration("poll", 250*time.Millisecond, "poll interval")
	wakeDeadlineURL := flags.String("wake-deadline-url", os.Getenv("SAMEOLDCHAT_WAKE_DEADLINE_URL"), "lifecycle activator base URL for scheduled wake publication")
	wakeDeadlineToken := flags.String("wake-deadline-token", os.Getenv("SAMEOLDCHAT_WAKE_DEADLINE_TOKEN"), "lifecycle activator control token for scheduled wake publication")
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
	if *backend == "" || *deliveryFormat == "" || *owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0 || *failureBudget <= 0 {
		logger.Error("worker requires explicit store, delivery format, owner, and positive limits")
		return exitConfiguration
	}
	if *deliveryFormat != "record" && *deliveryFormat != "slack-events" {
		logger.Error("worker delivery format is unsupported", "format", *deliveryFormat, "allowed", "record, slack-events")
		return exitConfiguration
	}
	if (*wakeDeadlineURL == "") != (*wakeDeadlineToken == "") {
		logger.Error("wake deadline publication requires both URL and control token")
		return exitConfiguration
	}
	if *deliveryFormat == "record" {
		if *workspace == "" || *deliveryURL == "" {
			logger.Error("record delivery requires a workspace and delivery URL")
			return exitConfiguration
		}
		if ignored := explicitlySet(flags, "app-id", "signing-secret", "app-credential-key-hex"); len(ignored) != 0 {
			logger.Error("record delivery cannot honour Slack application settings", "ignored", strings.Join(ignored, ", "), "hint", "use -delivery-format slack-events")
			return exitConfiguration
		}
	} else if ignored := explicitlySet(flags, "workspace", "delivery-url", "app-id", "signing-secret"); len(ignored) != 0 {
		logger.Error("slack-events delivery is driven by installed app manifests and cannot accept manual routing or plaintext credentials", "ignored", strings.Join(ignored, ", "))
		return exitConfiguration
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
	}
	if err != nil {
		logger.Error("configure delivery", "error", err)
		return exitConfiguration
	}
	var appCredentialKey []byte
	if *deliveryFormat == "slack-events" {
		appCredentialKey, err = secretbox.ParseKeyHex(*appCredentialKeyHex)
		if err != nil {
			logger.Error("slack-events delivery requires a valid application credential key")
			return exitConfiguration
		}
	}
	runtime, err := localchat.Open(ctx, localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, AppCredentialKey: appCredentialKey})
	if err != nil {
		logger.Error("open worker store", "error", err)
		return exitRuntime
	}
	defer func() {
		if err := runtime.Closer.Close(); err != nil {
			logger.Error("close worker store", "error", err)
		}
	}()
	var worker outbox.Worker
	var scheduledWorker scheduler.Worker
	var reminderWorker scheduler.ReminderWorker
	var statusWorker scheduler.StatusWorker
	var scheduledStatusWorker scheduler.ScheduledStatusWorker
	var appEventProcessor slackapp.EventProcessor
	if *deliveryFormat == "record" {
		worker, err = outbox.NewWorker(runtime.OutboxSource, *owner, *limit, *lease, delivery)
		if err != nil {
			logger.Error("configure outbox worker", "error", err)
			return exitConfiguration
		}
	} else {
		appEventProcessor = slackapp.EventProcessor{Store: runtime.Store, AppCredentialKey: appCredentialKey, Owner: *owner, Lease: *lease}
	}
	// Scheduled delivery is a product worker, not an outbox-format feature.
	// slack-events is the multi-workspace production mode, so an empty workspace
	// deliberately claims due schedules across every workspace.
	scheduledWorker, err = scheduler.NewWorker(runtime.ScheduledSource, runtime.Service, *owner, *limit, *lease)
	if err != nil {
		logger.Error("configure scheduled worker", "error", err)
		return exitConfiguration
	}
	reminderWorker, err = scheduler.NewReminderWorker(runtime.ReminderSource, runtime.Service, *owner, *limit, *lease, nil)
	if err != nil {
		logger.Error("configure reminder worker", "error", err)
		return exitConfiguration
	}
	statusWorker, err = scheduler.NewStatusWorker(runtime.Store, *limit)
	if err != nil {
		logger.Error("configure status worker", "error", err)
		return exitConfiguration
	}
	scheduledStatusWorker, err = scheduler.NewScheduledStatusWorker(runtime.Store, *limit)
	if err != nil {
		logger.Error("configure scheduled status worker", "error", err)
		return exitConfiguration
	}
	workflowScheduleWorker, err := scheduler.NewWorkflowScheduleWorker(runtime.Store, runtime.Service, *limit)
	if err != nil {
		logger.Error("configure workflow schedule worker", "error", err)
		return exitConfiguration
	}
	workflowEventWorker, err := scheduler.NewWorkflowEventWorker(runtime.Service, *limit)
	if err != nil {
		logger.Error("configure workflow event worker", "error", err)
		return exitConfiguration
	}
	var deadlinePublisher scheduler.FencedDeadlinePublisher
	if *wakeDeadlineURL != "" {
		deadlinePublisher, err = scheduler.NewActivatorDeadlinePublisher(*wakeDeadlineURL, *wakeDeadlineToken, nil)
		if err != nil {
			logger.Error("configure wake deadline publication", "error", err)
			return exitConfiguration
		}
	}
	publishDeadline := func(cycleContext context.Context, workspaceID domain.WorkspaceID) error {
		if deadlinePublisher == nil {
			return nil
		}
		if workspaceID == "" {
			return scheduler.PublishEarliestProductWakeDeadlineComplete(cycleContext, runtime.ScheduledSource, runtime.ReminderSource, runtime.Store, runtime.Store, runtime.Store, deadlinePublisher)
		}
		return scheduler.PublishEarliestProductWakeDeadlineComplete(cycleContext, runtime.ScheduledSource, runtime.ReminderSource, runtime.Store, runtime.Store, runtime.Store, deadlinePublisher, workspaceID)
	}
	workerContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cycle := func(cycleContext context.Context) (bool, error) {
		if *deliveryFormat == "slack-events" {
			eventCount, eventErr := appEventProcessor.RunOnce(cycleContext)
			if eventErr != nil {
				logger.Error("Slack Events API delivery failed", "count", eventCount, "error", eventErr)
			}
			scheduledCount, scheduledErr := scheduledWorker.RunOnce(cycleContext, "")
			if scheduledErr != nil {
				logger.Error("scheduled message execution failed", "count", scheduledCount, "error", scheduledErr)
			}
			reminderCount, reminderErr := reminderWorker.RunOnce(cycleContext, "")
			if reminderErr != nil {
				logger.Error("reminder execution failed", "count", reminderCount, "error", reminderErr)
			}
			scheduledStatusCount, scheduledStatusErr := scheduledStatusWorker.RunOnce(cycleContext, "")
			if scheduledStatusErr != nil {
				logger.Error("scheduled status execution failed", "count", scheduledStatusCount, "error", scheduledStatusErr)
			}
			statusCount, statusErr := statusWorker.RunOnce(cycleContext, "")
			if statusErr != nil {
				logger.Error("status expiration failed", "count", statusCount, "error", statusErr)
			}
			workflowScheduleCount, workflowScheduleErr := workflowScheduleWorker.RunOnce(cycleContext, "")
			if workflowScheduleErr != nil {
				logger.Error("workflow schedule execution failed", "count", workflowScheduleCount, "error", workflowScheduleErr)
			}
			workflowEventCount, workflowEventErr := workflowEventWorker.RunOnce(cycleContext, "")
			if workflowEventErr != nil {
				logger.Error("workflow event dispatch failed", "count", workflowEventCount, "error", workflowEventErr)
			}
			deadlineErr := publishDeadline(cycleContext, "")
			if deadlineErr != nil {
				logger.Error("wake deadline publication failed", "error", deadlineErr)
			}
			return eventCount > 0 || scheduledCount > 0 || reminderCount > 0 || scheduledStatusCount > 0 || statusCount > 0 || workflowScheduleCount > 0 || workflowEventCount > 0, errors.Join(eventErr, scheduledErr, reminderErr, scheduledStatusErr, statusErr, workflowScheduleErr, workflowEventErr, deadlineErr)
		}
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
		reminderCount, reminderErr := reminderWorker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if reminderErr != nil {
			failures = errors.Join(failures, reminderErr)
			logger.Error("reminder execution failed", "count", reminderCount, "error", reminderErr)
		}
		scheduledStatusCount, scheduledStatusErr := scheduledStatusWorker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if scheduledStatusErr != nil {
			failures = errors.Join(failures, scheduledStatusErr)
			logger.Error("scheduled status execution failed", "count", scheduledStatusCount, "error", scheduledStatusErr)
		}
		statusCount, statusErr := statusWorker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if statusErr != nil {
			failures = errors.Join(failures, statusErr)
			logger.Error("status expiration failed", "count", statusCount, "error", statusErr)
		}
		workflowScheduleCount, workflowScheduleErr := workflowScheduleWorker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if workflowScheduleErr != nil {
			failures = errors.Join(failures, workflowScheduleErr)
			logger.Error("workflow schedule execution failed", "count", workflowScheduleCount, "error", workflowScheduleErr)
		}
		workflowEventCount, workflowEventErr := workflowEventWorker.RunOnce(cycleContext, domain.WorkspaceID(*workspace))
		if workflowEventErr != nil {
			failures = errors.Join(failures, workflowEventErr)
			logger.Error("workflow event dispatch failed", "count", workflowEventCount, "error", workflowEventErr)
		}
		deadlineErr := publishDeadline(cycleContext, domain.WorkspaceID(*workspace))
		if deadlineErr != nil {
			failures = errors.Join(failures, deadlineErr)
			logger.Error("wake deadline publication failed", "error", deadlineErr)
		}
		return count > 0 || scheduledCount > 0 || reminderCount > 0 || scheduledStatusCount > 0 || statusCount > 0 || workflowScheduleCount > 0 || workflowEventCount > 0, failures
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
	return errors.Is(err, events.ErrPayloadInternal) ||
		errors.Is(err, events.ErrPayloadRecipientScoped) ||
		errors.Is(err, events.ErrPayloadRequired) ||
		errors.Is(err, events.ErrPayloadFieldInvalid) ||
		errors.Is(err, events.ErrPayloadMalformed) ||
		errors.Is(err, events.ErrSlackEventIncomplete) ||
		errors.Is(err, events.ErrEventIncomplete)
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
