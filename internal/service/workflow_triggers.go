package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ErrInvalidTriggerConfig reports a trigger configuration that cannot execute:
// an unknown schedule frequency, an unbound channel or list, or a malformed
// JSON object. It is distinct from ErrInvalidWorkflowStep so the Slack HTTP
// boundary can keep its own error mapping per resource.
var ErrInvalidTriggerConfig = errors.New("workflow trigger configuration is invalid")

// ErrWebhookTriggerSecret reports a webhook invocation whose path secret does
// not match the trigger's stored hash. The HTTP boundary answers it with the
// same indistinguishable 404 as an unknown trigger.
var ErrWebhookTriggerSecret = errors.New("webhook trigger secret does not match")

const (
	// workflowScheduleMaxIterations bounds occurrence stepping so a pathological
	// configuration (a one-hour interval asked for an occurrence years ahead)
	// terminates instead of looping for the life of the request.
	workflowScheduleMaxIterations = 100000
	workflowWebhookSecretBytes    = 24
)

type workflowScheduleConfig struct {
	StartTime string `json:"start_time"`
	Timezone  string `json:"timezone"`
	Frequency struct {
		Type     string `json:"type"`
		Interval *int   `json:"interval,omitempty"`
	} `json:"frequency"`
}

func (c workflowScheduleConfig) interval() int {
	if c.Frequency.Interval == nil {
		return 1
	}
	return *c.Frequency.Interval
}

type workflowEventConfig struct {
	ChannelIDs []string `json:"channel_ids,omitempty"`
	Keyword    string   `json:"keyword,omitempty"`
	Reaction   string   `json:"reaction,omitempty"`
	ListID     string   `json:"list_id,omitempty"`
	Event      string   `json:"event,omitempty"`
}

type workflowWebhookConfig struct {
	SecretHash       string `json:"webhook_secret_hash,omitempty"`
	SecretCiphertext string `json:"webhook_secret_ciphertext,omitempty"`
}

func workflowTriggerSecretAssociatedData(triggerID domain.WorkflowTriggerID) string {
	return "workflow_trigger_webhook:" + string(triggerID)
}

func parseWorkflowSchedule(raw string) (workflowScheduleConfig, time.Time, *time.Location, error) {
	var config workflowScheduleConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return workflowScheduleConfig{}, time.Time{}, nil, ErrInvalidTriggerConfig
	}
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(config.StartTime))
	if err != nil {
		return workflowScheduleConfig{}, time.Time{}, nil, ErrInvalidTriggerConfig
	}
	zoneName := strings.TrimSpace(config.Timezone)
	if zoneName == "" {
		zoneName = "UTC"
	}
	location, err := time.LoadLocation(zoneName)
	if err != nil {
		return workflowScheduleConfig{}, time.Time{}, nil, ErrInvalidTriggerConfig
	}
	interval := config.interval()
	if interval < 1 || interval > 366 {
		return workflowScheduleConfig{}, time.Time{}, nil, ErrInvalidTriggerConfig
	}
	switch config.Frequency.Type {
	case "hourly", "daily", "weekly", "monthly":
	default:
		return workflowScheduleConfig{}, time.Time{}, nil, ErrInvalidTriggerConfig
	}
	return config, start.UTC(), location, nil
}

// stepSchedule advances one occurrence inside the configured zone. Daily,
// weekly and monthly steps use calendar arithmetic so a wall-clock schedule
// survives daylight-saving transitions; hourly steps use absolute duration.
func stepSchedule(config workflowScheduleConfig, location *time.Location, occurrence time.Time) time.Time {
	interval := config.interval()
	local := occurrence.In(location)
	switch config.Frequency.Type {
	case "hourly":
		return occurrence.Add(time.Duration(interval) * time.Hour)
	case "daily":
		return local.AddDate(0, 0, interval).UTC()
	case "weekly":
		return local.AddDate(0, 0, 7*interval).UTC()
	default:
		return local.AddDate(0, interval, 0).UTC()
	}
}

// NextWorkflowScheduledRun returns the first occurrence at or after `from`
// when inclusive, or strictly after `from` when advancing past a fire. The
// returned time is UTC; the schedule itself is evaluated in its configured
// zone.
func NextWorkflowScheduledRun(raw string, from time.Time, inclusive bool) (time.Time, error) {
	config, start, location, err := parseWorkflowSchedule(raw)
	if err != nil {
		return time.Time{}, err
	}
	from = from.UTC()
	occurrence := start
	for index := 0; index < workflowScheduleMaxIterations; index++ {
		if occurrence.After(from) || inclusive && occurrence.Equal(from) {
			return occurrence, nil
		}
		next := stepSchedule(config, location, occurrence)
		if !next.After(occurrence) {
			return time.Time{}, ErrInvalidTriggerConfig
		}
		occurrence = next
	}
	return time.Time{}, ErrInvalidTriggerConfig
}

// normalizeWorkflowTriggerConfig validates and canonicalizes one trigger's
// configuration. The returned NextRunAt is non-zero only for an enabled
// scheduled trigger. Webhook secrets are server-managed: a creation without
// one generates it, and an update carries the stored secret forward so an
// enable/disable edit cannot rotate or drop the URL.
func (m Messages) normalizeWorkflowTriggerConfig(ctx context.Context, value *domain.WorkflowTrigger, current *domain.WorkflowTrigger, now time.Time) error {
	config := map[string]json.RawMessage{}
	if strings.TrimSpace(value.Config) != "" {
		if err := json.Unmarshal([]byte(value.Config), &config); err != nil || config == nil {
			return ErrInvalidTriggerConfig
		}
	}
	switch domain.WorkflowTriggerType(value.Type) {
	case domain.WorkflowTriggerLink, domain.WorkflowTriggerShortcut:
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		value.Config = string(encoded)
		value.NextRunAt = time.Time{}
		return nil
	case domain.WorkflowTriggerScheduled:
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		value.Config = string(encoded)
		if _, _, _, err := parseWorkflowSchedule(value.Config); err != nil {
			return err
		}
		if !value.Enabled {
			value.NextRunAt = time.Time{}
			return nil
		}
		next, err := NextWorkflowScheduledRun(value.Config, now, true)
		if err != nil {
			return err
		}
		value.NextRunAt = next
		return nil
	case domain.WorkflowTriggerWebhook:
		secret := workflowWebhookConfig{}
		if current != nil {
			var stored workflowWebhookConfig
			if err := json.Unmarshal([]byte(current.Config), &stored); err == nil {
				secret = stored
			}
		}
		if secret.SecretHash == "" || secret.SecretCiphertext == "" {
			if len(m.AppCredentialKey) == 0 {
				return ErrInvalidTriggerConfig
			}
			raw := make([]byte, workflowWebhookSecretBytes)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			token := hex.EncodeToString(raw)
			ciphertext, err := secretbox.Seal(m.AppCredentialKey, workflowTriggerSecretAssociatedData(value.ID), token)
			if err != nil {
				return err
			}
			secret = workflowWebhookConfig{
				SecretHash:       domain.HashToken(token),
				SecretCiphertext: ciphertext,
			}
		}
		encoded, err := json.Marshal(secret)
		if err != nil {
			return err
		}
		value.Config = string(encoded)
		value.NextRunAt = time.Time{}
		return nil
	case domain.WorkflowTriggerMessage, domain.WorkflowTriggerReaction, domain.WorkflowTriggerJoin:
		var event workflowEventConfig
		if err := json.Unmarshal([]byte(value.Config), &event); err != nil {
			return ErrInvalidTriggerConfig
		}
		if len(event.ChannelIDs) != 1 {
			return ErrInvalidTriggerConfig
		}
		channel, err := m.Store.GetConversation(ctx, domain.ConversationID(strings.TrimSpace(event.ChannelIDs[0])))
		if err != nil || channel.WorkspaceID != value.WorkspaceID || channel.IsDirect || channel.IsGroupDirect {
			return ErrInvalidTriggerConfig
		}
		normalized := workflowEventConfig{ChannelIDs: []string{string(channel.ID)}}
		if domain.WorkflowTriggerType(value.Type) == domain.WorkflowTriggerMessage {
			normalized.Keyword = strings.TrimSpace(event.Keyword)
		}
		if domain.WorkflowTriggerType(value.Type) == domain.WorkflowTriggerReaction {
			normalized.Reaction = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(event.Reaction), ":"))
			normalized.Reaction = strings.TrimSuffix(normalized.Reaction, ":")
		}
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		value.Config = string(encoded)
		value.NextRunAt = time.Time{}
		return nil
	case domain.WorkflowTriggerList:
		var event workflowEventConfig
		if err := json.Unmarshal([]byte(value.Config), &event); err != nil {
			return ErrInvalidTriggerConfig
		}
		list, err := m.Store.GetList(ctx, value.WorkspaceID, domain.ListID(strings.TrimSpace(event.ListID)))
		if err != nil || list.WorkspaceID != value.WorkspaceID {
			return ErrInvalidTriggerConfig
		}
		if event.Event != "created" && event.Event != "updated" {
			return ErrInvalidTriggerConfig
		}
		encoded, err := json.Marshal(workflowEventConfig{ListID: string(list.ID), Event: event.Event})
		if err != nil {
			return err
		}
		value.Config = string(encoded)
		value.NextRunAt = time.Time{}
		return nil
	default:
		return ErrInvalidTriggerConfig
	}
}

// WebhookTriggerURL reveals the invoke path of a webhook trigger to the
// workflow owner. The secret rides the URL, so this is intentionally
// owner-only; every other audience receives the trigger without a usable URL.
func (m Messages) WebhookTriggerURL(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, triggerID domain.WorkflowTriggerID) (string, error) {
	trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
	if err != nil {
		return "", err
	}
	if domain.WorkflowTriggerType(trigger.Type) != domain.WorkflowTriggerWebhook {
		return "", store.ErrNotFound
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, trigger.WorkflowID)
	if err != nil {
		return "", err
	}
	if workflow.OwnerID != actor {
		return "", store.ErrNotFound
	}
	var stored workflowWebhookConfig
	if err := json.Unmarshal([]byte(trigger.Config), &stored); err != nil || stored.SecretCiphertext == "" {
		return "", store.ErrNotFound
	}
	secret, err := secretbox.Open(m.AppCredentialKey, workflowTriggerSecretAssociatedData(trigger.ID), stored.SecretCiphertext)
	if err != nil {
		return "", err
	}
	return "/services/triggers/" + string(workspaceID) + "/" + string(trigger.ID) + "/" + secret, nil
}

// RunWebhookTrigger starts a workflow run from an unauthenticated webhook
// invocation. The path secret is the whole capability: a match runs the
// workflow as its owner, and every mismatch — unknown workspace, unknown
// trigger, wrong secret, disabled trigger, unpublished workflow — is the same
// ErrWebhookTriggerSecret so the HTTP boundary can answer one
// indistinguishable 404.
func (m Messages) RunWebhookTrigger(ctx context.Context, workspaceID domain.WorkspaceID, triggerID domain.WorkflowTriggerID, secret, inputs string) (domain.WorkflowRun, error) {
	trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.WorkflowRun{}, ErrWebhookTriggerSecret
		}
		return domain.WorkflowRun{}, err
	}
	if domain.WorkflowTriggerType(trigger.Type) != domain.WorkflowTriggerWebhook {
		return domain.WorkflowRun{}, ErrWebhookTriggerSecret
	}
	var stored workflowWebhookConfig
	if err := json.Unmarshal([]byte(trigger.Config), &stored); err != nil || stored.SecretHash == "" {
		return domain.WorkflowRun{}, ErrWebhookTriggerSecret
	}
	if domain.HashToken(strings.TrimSpace(secret)) != stored.SecretHash {
		return domain.WorkflowRun{}, ErrWebhookTriggerSecret
	}
	return m.RunAutomaticWorkflow(ctx, workspaceID, triggerID, "", inputs, "")
}

// workflowEventIdempotency derives the exactly-once key for an event-trigger
// fire. Two dispatchers that both observe one event, or one dispatcher that
// replays it after a crash, produce a single run through the store's
// idempotency index.
func workflowEventIdempotency(triggerID domain.WorkflowTriggerID, eventID domain.EventID) string {
	return "event:" + string(triggerID) + ":" + string(eventID)
}

// workflowTriggerMatchesEvent reports whether one durable event satisfies one
// trigger's configured condition. The message text needed for keyword matching
// comes from the event's immutable snapshot, never from re-reading mutable
// message state.
func workflowTriggerMatchesEvent(trigger domain.WorkflowTrigger, record events.Record) (domain.ConversationID, bool) {
	var config workflowEventConfig
	if err := json.Unmarshal([]byte(trigger.Config), &config); err != nil {
		return "", false
	}
	delivered, err := events.Deliverable(record.Event)
	if err != nil {
		return "", false
	}
	channel, _ := delivered.Field("channel_id")
	matchesChannel := func() bool {
		return len(config.ChannelIDs) == 1 && config.ChannelIDs[0] == channel
	}
	switch domain.WorkflowTriggerType(trigger.Type) {
	case domain.WorkflowTriggerMessage:
		if record.Event.Topic != "message.created" || !matchesChannel() {
			return "", false
		}
		if config.Keyword != "" {
			snapshot, ok, err := decodeMessageEventSnapshot(record.Event)
			if err != nil || !ok || !strings.Contains(strings.ToLower(snapshot.Current.Text), strings.ToLower(config.Keyword)) {
				return "", false
			}
		}
		return domain.ConversationID(channel), true
	case domain.WorkflowTriggerReaction:
		if record.Event.Topic != "reaction.added" || !matchesChannel() {
			return "", false
		}
		if config.Reaction != "" {
			reaction, _ := delivered.Field("reaction")
			if reaction != config.Reaction {
				return "", false
			}
		}
		return domain.ConversationID(channel), true
	case domain.WorkflowTriggerJoin:
		if record.Event.Topic != "conversation.member_added" && record.Event.Topic != "conversation.members_invited" {
			return "", false
		}
		if !matchesChannel() {
			return "", false
		}
		return domain.ConversationID(channel), true
	case domain.WorkflowTriggerList:
		topic := "list.item." + config.Event
		if record.Event.Topic != topic || config.Event == "" {
			return "", false
		}
		listID, _ := delivered.Field("list_id")
		if listID != config.ListID {
			return "", false
		}
		return "", true
	default:
		return "", false
	}
}

// DispatchWorkflowEventTriggers fires every enabled message, reaction, join,
// and list trigger satisfied by durable events after the workspace cursor,
// then advances that cursor. It is the worker-side half of event triggers: the
// other half is the trigger's stored configuration. A run that cannot start
// because its workflow was just unpublished or its trigger disabled is skipped
// as already-decided; any other run failure stops the batch before the cursor
// moves so the next cycle retries.
func (m Messages) DispatchWorkflowEventTriggers(ctx context.Context, workspaceID domain.WorkspaceID, limit int) (int, error) {
	if limit <= 0 {
		return 0, store.InvalidArgument("workflow event dispatch limit must be positive")
	}
	cursor, err := m.Store.GetWorkflowEventCursor(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	records, err := m.Store.ListEventsAfter(ctx, workspaceID, cursor, limit)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	triggers, err := m.Store.ListWorkflowEventTriggers(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	started := 0
	processed := cursor
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return started, err
		}
		eventWorkspace := workspaceID
		if eventWorkspace == "" {
			eventWorkspace = record.Event.WorkspaceID
		}
		for _, trigger := range triggers {
			if trigger.WorkspaceID != record.Event.WorkspaceID {
				continue
			}
			conversation, matches := workflowTriggerMatchesEvent(trigger, record)
			if !matches {
				continue
			}
			_, err := m.RunAutomaticWorkflow(ctx, eventWorkspace, trigger.ID, conversation, "{}", workflowEventIdempotency(trigger.ID, record.Event.ID))
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			if err != nil {
				return started, err
			}
			started++
		}
		processed = record.Sequence
	}
	if err := m.Store.AdvanceWorkflowEventCursor(ctx, workspaceID, processed); err != nil {
		return started, err
	}
	return started, nil
}
