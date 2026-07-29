package slackapp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

const eventSurface = "http"

var ErrEventRetriesExhausted = errors.New("Slack Events API retries exhausted")

type EventStore interface {
	ListInstalledApps(context.Context) ([]domain.AppManifestSnapshot, error)
	ClaimAppEvent(context.Context, domain.AppID, string, string, time.Duration) (events.Record, int, string, bool, error)
	AckAppEvent(context.Context, domain.AppID, string, string, uint64) error
	ReleaseAppEvent(context.Context, domain.AppID, string, string, uint64, string, time.Time) error
	GetConversation(context.Context, domain.ConversationID) (domain.Conversation, error)
	GetMessage(context.Context, domain.MessageID) (domain.Message, error)
	GetFile(context.Context, domain.FileID) (domain.File, error)
	GetBotByApp(context.Context, domain.WorkspaceID, domain.AppID) (domain.Bot, error)
	IsConversationMember(context.Context, domain.ConversationID, domain.UserID) (bool, error)
}

type EventProcessor struct {
	Store            EventStore
	AppCredentialKey []byte
	Owner            string
	Lease            time.Duration
	Client           *http.Client
	Now              func() time.Time
}

func (p EventProcessor) RunOnce(ctx context.Context) (int, error) {
	if p.Store == nil || strings.TrimSpace(p.Owner) == "" || p.Lease <= 0 || len(p.AppCredentialKey) != 32 {
		return 0, errors.New("Slack Events API processor requires a store, owner, lease, and application credential key")
	}
	snapshots, err := p.Store.ListInstalledApps(ctx)
	if err != nil {
		return 0, err
	}
	delivered := 0
	var failures error
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
		record, attempt, retryReason, found, err := p.Store.ClaimAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, p.Lease)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("claim app %s event: %w", snapshot.App.ID, err))
			continue
		}
		if !found {
			continue
		}
		handled, err := p.deliver(ctx, snapshot, parsed, record, attempt, retryReason)
		if handled {
			delivered++
		}
		if err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return delivered, failures
}

func (p EventProcessor) deliver(ctx context.Context, snapshot domain.AppManifestSnapshot, parsed appmanifest.Parsed, record events.Record, attempt int, retryReason string) (bool, error) {
	prepared, visible, err := service.PrepareAppEvent(ctx, p.Store, snapshot.App.ID, record)
	if err != nil {
		releaseErr := p.Store.ReleaseAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence, "event_projection_failed", p.now().Add(time.Second))
		return false, errors.Join(err, releaseErr)
	}
	if !visible {
		return true, p.Store.AckAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence)
	}
	record = prepared
	bodies, err := events.SlackEventBodies(record, string(snapshot.App.ID))
	if err != nil {
		// This committed record can never become a Slack event. Advancing this
		// app's independent cursor prevents one bad producer from blocking every
		// later event while preserving the error for operators.
		if ackErr := p.Store.AckAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence); ackErr != nil {
			return false, errors.Join(err, ackErr)
		}
		return true, fmt.Errorf("encode app %s event %s: %w", snapshot.App.ID, record.Event.ID, err)
	}
	filtered, err := events.FilterSubscribedSlackEventBodies(ctx, bodies, parsed.BotEvents, parsed.UserEvents, p.Store.GetConversation)
	if err != nil {
		if ackErr := p.Store.AckAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence); ackErr != nil {
			return false, errors.Join(err, ackErr)
		}
		return true, fmt.Errorf("filter app %s event %s: %w", snapshot.App.ID, record.Event.ID, err)
	}
	if len(filtered) == 0 {
		return true, p.Store.AckAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence)
	}
	signingSecret, err := (service.Messages{AppCredentialKey: p.AppCredentialKey}).OpenAppSigningSecret(snapshot.App)
	if err != nil {
		return false, err
	}
	for _, body := range filtered {
		failure := p.post(ctx, parsed.EventRequestURL, signingSecret, body, attempt, retryReason)
		if failure == nil {
			continue
		}
		now := p.now()
		if failure.noRetry || attempt >= 3 {
			ackErr := p.Store.AckAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence)
			if attempt >= 3 {
				failure.err = fmt.Errorf("%w: %v", ErrEventRetriesExhausted, failure.err)
			}
			return true, errors.Join(failure.err, ackErr)
		}
		retryAt := now
		switch attempt {
		case 1:
			retryAt = now.Add(time.Minute)
		case 2:
			retryAt = now.Add(5 * time.Minute)
		}
		releaseErr := p.Store.ReleaseAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence, failure.reason, retryAt)
		return false, errors.Join(failure.err, releaseErr)
	}
	return true, p.Store.AckAppEvent(ctx, snapshot.App.ID, eventSurface, p.Owner, record.Sequence)
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
