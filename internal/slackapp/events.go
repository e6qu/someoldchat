package slackapp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
)

const eventSurface = "http"

var ErrEventRetriesExhausted = errors.New("Slack Events API retries exhausted")

type EventStore interface {
	ListInstalledApps(context.Context) ([]domain.AppManifestSnapshot, error)
	ClaimAppEvent(context.Context, domain.AppID, string, string, time.Duration) (events.AppEventClaim, bool, error)
	AckAppEvent(context.Context, domain.AppID, string, string, uint64) error
	ReleaseAppEvent(context.Context, domain.AppID, string, string, uint64, events.AppEventRelease) error
	GetConversation(context.Context, domain.ConversationID) (domain.Conversation, error)
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	GetFile(context.Context, domain.FileID) (domain.File, error)
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
	ListAppAuthorizations(context.Context, domain.AppID, domain.WorkspaceID) ([]domain.AppAuthorization, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
	IssueFunctionExecutionToken(context.Context, domain.FunctionExecutionToken, string) (domain.FunctionExecutionToken, error)
}

// Delivery pacing. Each cycle drains up to BatchPerApp records per app, and
// stops taking more for an app once AppBudget has passed, so one app whose
// endpoint is slow costs the cycle at most one request timeout beyond the
// budget. Apps are delivered concurrently, at most Concurrency at a time, so a
// slow or failing endpoint never serialises another app's callbacks behind it.
const (
	DefaultBatchPerApp = 32
	DefaultConcurrency = 8
	DefaultAppBudget   = time.Second
)

// internalRetryDelay is how long a record waits after a failure on this side
// of the delivery. It is not an attempt, so Slack's schedule does not apply.
const internalRetryDelay = 5 * time.Second

type EventProcessor struct {
	Store            EventStore
	AppCredentialKey []byte
	// PublicURL is the deployment's public URL (-auth-public-url): every URL
	// a delivered event carries is built on it. Empty leaves them
	// origin-relative; see docs/operations.md.
	PublicURL string
	Owner     string
	Lease     time.Duration
	Client    *http.Client
	Now       func() time.Time
	// BatchPerApp, Concurrency and AppBudget override the defaults above when
	// positive.
	BatchPerApp int
	Concurrency int
	AppBudget   time.Duration
}

type deliveryTarget struct {
	snapshot domain.AppManifestSnapshot
	parsed   appmanifest.Parsed
}

func (p EventProcessor) RunOnce(ctx context.Context) (int, error) {
	if p.Store == nil || strings.TrimSpace(p.Owner) == "" || p.Lease <= 0 || len(p.AppCredentialKey) != 32 {
		return 0, errors.New("Slack Events API processor requires a store, owner, lease, and application credential key")
	}
	snapshots, err := p.Store.ListInstalledApps(ctx)
	if err != nil {
		return 0, err
	}
	var failures error
	targets := make([]deliveryTarget, 0, len(snapshots))
	for _, snapshot := range snapshots {
		parsed, problems := appmanifest.Parse(snapshot.Manifest)
		if len(problems) != 0 {
			failures = errors.Join(failures, fmt.Errorf("app %s has an invalid persisted manifest: %+v", snapshot.App.ID, problems))
			continue
		}
		// Socket Mode replaces HTTP delivery. A request URL can remain in the
		// manifest while Socket Mode is enabled, but Slack does not duplicate
		// every callback onto both transports.
		if parsed.SocketModeEnabled || parsed.EventRequestURL == "" {
			continue
		}
		targets = append(targets, deliveryTarget{snapshot: snapshot, parsed: parsed})
	}
	var (
		mutex     sync.Mutex
		delivered int
		group     sync.WaitGroup
	)
	slots := make(chan struct{}, positive(p.Concurrency, DefaultConcurrency))
	for _, target := range targets {
		group.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() {
				<-slots
				group.Done()
			}()
			count, err := p.drainApp(ctx, target)
			mutex.Lock()
			delivered += count
			failures = errors.Join(failures, err)
			mutex.Unlock()
		}()
	}
	group.Wait()
	return delivered, failures
}

func positive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// drainApp delivers the records due for one app, in journal order. A record
// the app fails is released for its own retry and the next record is taken
// straight away.
func (p EventProcessor) drainApp(ctx context.Context, target deliveryTarget) (int, error) {
	budget := p.AppBudget
	if budget <= 0 {
		budget = DefaultAppBudget
	}
	started := time.Now()
	delivered := 0
	var failures error
	for claimed := 0; claimed < positive(p.BatchPerApp, DefaultBatchPerApp); claimed++ {
		if claimed > 0 && time.Since(started) >= budget {
			break
		}
		if ctx.Err() != nil {
			return delivered, errors.Join(failures, ctx.Err())
		}
		claim, found, err := p.Store.ClaimAppEvent(ctx, target.snapshot.App.ID, eventSurface, p.Owner, p.Lease)
		if err != nil {
			return delivered, errors.Join(failures, fmt.Errorf("claim app %s event: %w", target.snapshot.App.ID, err))
		}
		if !found {
			break
		}
		handled, err := p.deliver(ctx, target.snapshot, target.parsed, claim)
		if handled {
			delivered++
		}
		failures = errors.Join(failures, err)
	}
	return delivered, failures
}

// deferRecord releases a record after a failure on this side of the delivery.
// It is not an attempt, and its reason is recorded for operators but never
// sent to the app.
func (p EventProcessor) deferRecord(ctx context.Context, appID domain.AppID, claim events.AppEventClaim, reason string, cause error) error {
	releaseErr := p.Store.ReleaseAppEvent(ctx, appID, eventSurface, p.Owner, claim.Record.Sequence, events.AppEventRelease{
		Reason: reason, RetryAt: p.now().Add(internalRetryDelay), Internal: true, Delivered: claim.Delivered,
	})
	return errors.Join(cause, releaseErr)
}

func (p EventProcessor) deliver(ctx context.Context, snapshot domain.AppManifestSnapshot, parsed appmanifest.Parsed, claim events.AppEventClaim) (bool, error) {
	appID := snapshot.App.ID
	record := claim.Record
	prepared, visible, err := service.PrepareAppEvent(ctx, p.Store, p.AppCredentialKey, slackobject.Origin(p.PublicURL), appID, record)
	if err != nil {
		return false, p.deferRecord(ctx, appID, claim, "event_projection_failed", fmt.Errorf("project app %s event %s: %w", appID, record.Event.ID, err))
	}
	if !visible {
		return true, p.Store.AckAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence)
	}
	record = prepared
	bodies, err := events.SlackEventBodies(record, string(appID))
	if err != nil {
		// This committed record can never become a Slack event. Settling it
		// keeps the error for operators without retrying it forever.
		if ackErr := p.Store.AckAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence); ackErr != nil {
			return false, errors.Join(err, ackErr)
		}
		return true, fmt.Errorf("encode app %s event %s: %w", appID, record.Event.ID, err)
	}
	filtered, err := events.FilterSubscribedSlackEventBodies(ctx, bodies, parsed.BotEvents, parsed.UserEvents, p.Store.GetConversation)
	if err != nil {
		if ackErr := p.Store.AckAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence); ackErr != nil {
			return false, errors.Join(err, ackErr)
		}
		return true, fmt.Errorf("filter app %s event %s: %w", appID, record.Event.ID, err)
	}
	if len(filtered) == 0 {
		return true, p.Store.AckAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence)
	}
	signingSecret, err := (service.Messages{AppCredentialKey: p.AppCredentialKey}).OpenAppSigningSecret(snapshot.App)
	if err != nil {
		return false, p.deferRecord(ctx, appID, claim, "app_configuration_unavailable", err)
	}
	// A record that fans out is retried only for the callbacks the app has not
	// already accepted; re-posting an accepted one would deliver it twice.
	accepted := slices.Clone(claim.Delivered)
	for _, body := range filtered {
		eventID := callbackEventID(body)
		if eventID != "" && slices.Contains(accepted, eventID) {
			continue
		}
		failure := p.post(ctx, parsed.EventRequestURL, signingSecret, body, claim.Attempt, claim.RetryReason)
		if failure == nil {
			if eventID != "" {
				accepted = append(accepted, eventID)
			}
			continue
		}
		if failure.noRetry || claim.Attempt >= 3 {
			ackErr := p.Store.AckAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence)
			if claim.Attempt >= 3 {
				failure.err = fmt.Errorf("%w: %v", ErrEventRetriesExhausted, failure.err)
			}
			return true, errors.Join(failure.err, ackErr)
		}
		releaseErr := p.Store.ReleaseAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence, events.AppEventRelease{
			Reason: failure.reason, RetryAt: slackRetryAt(p.now(), claim.Attempt+1), Delivered: accepted,
		})
		return false, errors.Join(failure.err, releaseErr)
	}
	return true, p.Store.AckAppEvent(ctx, appID, eventSurface, p.Owner, record.Sequence)
}

// slackRetryAt schedules Slack's three retries: immediately, after one minute,
// and after five minutes. retry is the number of the retry being scheduled.
func slackRetryAt(now time.Time, retry int) time.Time {
	switch retry {
	case 2:
		return now.Add(time.Minute)
	case 3:
		return now.Add(5 * time.Minute)
	}
	return now
}

// callbackEventID reads the event_id of one encoded callback body.
func callbackEventID(body []byte) string {
	var envelope struct {
		EventID string `json:"event_id"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return envelope.EventID
}

type eventDeliveryFailure struct {
	reason  string
	noRetry bool
	err     error
}

var errTooManyRedirects = errors.New("too many redirects")

func (p EventProcessor) post(ctx context.Context, target, signingSecret string, body []byte, attempt int, retryReason string) *eventDeliveryFailure {
	now := p.now()
	signature, err := events.SlackSignature(signingSecret, now, body)
	if err != nil {
		return &eventDeliveryFailure{reason: "unknown_error", noRetry: true, err: err}
	}
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &eventDeliveryFailure{reason: "unknown_error", noRetry: true, err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(now.Unix(), 10))
	request.Header.Set("X-Slack-Signature", signature)
	if attempt > 0 {
		// Only Slack's own vocabulary reaches an app; anything else a store
		// might hold is reported as Slack reports an unexplained failure.
		if !events.SlackRetryReasons[retryReason] {
			retryReason = "unknown_error"
		}
		request.Header.Set("X-Slack-Retry-Num", strconv.Itoa(attempt))
		request.Header.Set("X-Slack-Retry-Reason", retryReason)
	}
	client := p.Client
	if client == nil {
		redirects := 0
		client = &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				redirects++
				if redirects > 2 {
					return errTooManyRedirects
				}
				return nil
			},
		}
	}
	response, err := client.Do(request)
	if err != nil {
		reason := requestFailureReason(err)
		return &eventDeliveryFailure{reason: reason, err: fmt.Errorf("Slack event delivery to %s failed: %w", target, err)}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return &eventDeliveryFailure{
		reason:  "http_error",
		noRetry: response.Header.Get("X-Slack-No-Retry") == "1",
		err:     fmt.Errorf("Slack event delivery to %s returned HTTP %d", target, response.StatusCode),
	}
}

func requestFailureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "http_timeout"
	}
	if errors.Is(err, errTooManyRedirects) {
		return "too_many_redirects"
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		var certificateError *tls.CertificateVerificationError
		if errors.As(urlError.Err, &certificateError) {
			return "ssl_error"
		}
	}
	return "connection_failed"
}

func (p EventProcessor) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
