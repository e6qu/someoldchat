package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// DeadlinePublisher records the earliest required wake time against a fencing
// generation. *lifecycle.Controller is the in-process implementation.
type DeadlinePublisher interface {
	SetWakeDeadline(uint64, time.Time) error
}

// FencedDeadlinePublisher also reports the generation it would publish against.
//
// It exists because the process that knows *when* work is due is not the process
// that owns the lifecycle record: scheduled messages live in the application
// store that cmd/worker reads, while the fencing generation and the wake hint
// live in the activator's control store. A publisher therefore has to fetch the
// fence before it can write against it, and a write against a fence that has
// since moved must be refused rather than silently applied.
type FencedDeadlinePublisher interface {
	DeadlinePublisher
	Fence(context.Context) (uint64, error)
}

// PublishWakeDeadline records the earliest deadline across every workspace this
// deployment serves.
//
// It takes a set rather than a single workspace because the deadline is a
// property of the stack, not of a tenant: publishing one workspace's earliest job
// overwrites another's, so a multi-tenant deployment would hibernate straight
// through the jobs of every workspace but the last one published.
//
// A zero deadline is published, not skipped. Clearing the hint when nothing is
// due is what lets a later hibernation proceed instead of being refused forever
// by a deadline that has already been served.
func PublishWakeDeadline(ctx context.Context, source Source, publisher DeadlinePublisher, fence uint64, workspaces ...domain.WorkspaceID) error {
	if source == nil || publisher == nil || len(workspaces) == 0 {
		return errors.New("scheduled wake deadline requires source, publisher, and at least one workspace")
	}
	var earliest time.Time
	for _, workspace := range workspaces {
		if workspace == "" {
			return errors.New("scheduled wake deadline requires a workspace")
		}
		deadline, err := source.EarliestScheduledMessage(ctx, workspace)
		if err != nil {
			return err
		}
		if deadline.IsZero() {
			continue
		}
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return publisher.SetWakeDeadline(fence, earliest)
}

// PublishEarliestWakeDeadline reads the current fence from the publisher and
// records the earliest deadline against it. It is the call an out-of-process
// worker makes each cycle; a fence that moves in between makes the write fail,
// and the next cycle publishes against the new one.
func PublishEarliestWakeDeadline(ctx context.Context, source Source, publisher FencedDeadlinePublisher, workspaces ...domain.WorkspaceID) error {
	if publisher == nil {
		return errors.New("scheduled wake deadline requires a publisher")
	}
	fence, err := publisher.Fence(ctx)
	if err != nil {
		return err
	}
	return PublishWakeDeadline(ctx, source, publisher, fence, workspaces...)
}

// ActivatorDeadlinePublisher publishes the wake hint to the lifecycle activator
// over its authenticated control API.
//
// specs/scale-to-zero.md requires the active stack to publish its earliest
// necessary wake time before shutdown, and the consume side — the activator's
// scheduled wake loop and the hibernation safety-window check — was already
// wired. Nothing produced the value, so the timer was inert and a hibernated
// stack woke for a due scheduled message only when unrelated traffic happened to
// arrive.
type ActivatorDeadlinePublisher struct {
	base   string
	token  string
	client *http.Client
}

func NewActivatorDeadlinePublisher(baseURL, token string, client *http.Client) (*ActivatorDeadlinePublisher, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("activator wake deadline publisher requires an absolute base URL: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("activator wake deadline publisher requires a control-plane token")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &ActivatorDeadlinePublisher{base: baseURL, token: token, client: client}, nil
}

// Fence reads the activator's current lifecycle generation from its health
// endpoint, which is the same value every other fenced transition is checked
// against.
func (p *ActivatorDeadlinePublisher) Fence(ctx context.Context) (uint64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/healthz", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	response, err := p.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if err != nil {
		return 0, err
	}
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("lifecycle activator health returned %d", response.StatusCode)
	}
	var health struct {
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return 0, fmt.Errorf("decode lifecycle activator health: %w", err)
	}
	return health.Generation, nil
}

// SetWakeDeadline records the hint. A stale fence is reported rather than
// swallowed: it means this worker belongs to a generation the stack has moved
// past, and the caller must publish again against the current one.
func (p *ActivatorDeadlinePublisher) SetWakeDeadline(fence uint64, deadline time.Time) error {
	return p.setWakeDeadline(context.Background(), fence, deadline)
}

func (p *ActivatorDeadlinePublisher) setWakeDeadline(ctx context.Context, fence uint64, deadline time.Time) error {
	query := url.Values{"generation": {strconv.FormatUint(fence, 10)}}
	if !deadline.IsZero() {
		query.Set("deadline", deadline.UTC().Format(time.RFC3339Nano))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/wake-deadline?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("lifecycle activator wake deadline returned %d", response.StatusCode)
	}
	return nil
}
