package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestHTTPDeliveryUsesStableEventIdempotency(t *testing.T) {
	var gotID string
	delivery, err := newHTTPDeliveryWithClient("https://delivery.invalid/events", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		gotID = request.Header.Get("Idempotency-Key")
		var record events.Record
		if err := json.NewDecoder(request.Body).Decode(&record); err != nil {
			t.Errorf("decode record: %v", err)
		}
		if record.Event.ID != "evt_1" {
			t.Errorf("event=%+v", record.Event)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery(context.Background(), events.Record{Sequence: 1, Event: producedEvent(t, "evt_1", "message.created", events.String("message_id", "M1"))}); err != nil {
		t.Fatal(err)
	}
	if gotID != "evt_1" {
		t.Fatalf("idempotency key=%q", gotID)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestWorkerRejectsHalfConfiguredWakePublication(t *testing.T) {
	t.Setenv("SAMEOLDCHAT_WAKE_DEADLINE_URL", "")
	t.Setenv("SAMEOLDCHAT_WAKE_DEADLINE_TOKEN", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, args := range map[string][]string{
		"URL only":   {"-delivery-format", "record", "-wake-deadline-url", "https://activator.example.test"},
		"token only": {"-delivery-format", "record", "-wake-deadline-token", "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := run(context.Background(), logger, args); code != exitConfiguration {
				t.Fatalf("exit code=%d, want configuration failure %d", code, exitConfiguration)
			}
		})
	}
}

// An ephemeral message is defined as visible to exactly one user and its payload
// carries that user's text, blocks and attachments. The record delivery format
// ships the durable record itself and never decodes the payload, so it has no
// recipient to filter on — which is precisely why the refusal has to happen when
// the record is encoded rather than in a check this format could omit, as it
// did.
func TestRecordDeliveryNeverShipsARecordNoAudienceMayReceive(t *testing.T) {
	ephemeral, err := events.New("evt_1", "T1", "U1", events.NewPayload(events.EphemeralMessageTopic,
		events.String("channel_id", "C1"),
		events.String("user_id", "U2"),
		events.String("text", "SECRET-ONLY-FOR-U2"),
	), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	internal, err := events.New("evt_2", "T1", "", events.BlobKey(events.UserPhotoBlobDeleteTopic, "T1/users/U1/photo_secret"), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]struct {
		record   events.Record
		sentinel error
		secret   string
	}{
		"recipient scoped": {events.Record{Sequence: 1, Event: ephemeral}, events.ErrPayloadRecipientScoped, "SECRET-ONLY-FOR-U2"},
		"internal":         {events.Record{Sequence: 2, Event: internal}, events.ErrPayloadInternal, "photo_secret"},
		// The record this format ships is rebuilt from independent parts at the
		// seam codec, so its topic is exactly the field that can understate what
		// it carries. Judging the topic alone POSTed one user's whole message to
		// a third-party URL under topic message.created.
		"recipient-scoped payload under an ordinary topic": {events.Record{Sequence: 3, Event: events.Event{
			ID: "evt_3", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(),
			Payload: `{"type":"` + events.EphemeralMessageTopic + `","event_ts":"1700000000.000000","user_id":"U2","text":"SECRET-ONLY-FOR-U2"}`,
		}}, events.ErrPayloadRecipientScoped, "SECRET-ONLY-FOR-U2"},
		"internal payload under an ordinary topic": {events.Record{Sequence: 4, Event: events.Event{
			ID: "evt_4", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(),
			Payload: `{"type":"` + events.UserPhotoBlobDeleteTopic + `","event_ts":"1700000000.000000","key":"T1/users/U1/photo_secret"}`,
		}}, events.ErrPayloadInternal, "photo_secret"},
		// A payload written before the typed payload contract is an opaque
		// internal identifier with no meaning outside this system. Every other
		// transport in the product refuses it; this one shipped it as the event
		// body.
		"payload that describes nothing": {events.Record{Sequence: 5, Event: events.Event{
			ID: "evt_5", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: "M0123456789",
		}}, events.ErrPayloadMalformed, "M0123456789"},
	} {
		delivery, err := newHTTPDeliveryWithClient("https://delivery.invalid/events", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			t.Errorf("%s: a record no audience may receive was POSTed to a third party: %s", name, body)
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		deliveryErr := delivery(context.Background(), value.record)
		if !errors.Is(deliveryErr, value.sentinel) {
			t.Fatalf("%s: error=%v, want %v", name, deliveryErr, value.sentinel)
		}
		if !errors.Is(deliveryErr, outbox.ErrPermanent) {
			t.Fatalf("%s: error=%v, want %v so the record is dropped instead of retried forever", name, deliveryErr, outbox.ErrPermanent)
		}
		if strings.Contains(deliveryErr.Error(), value.secret) {
			t.Fatalf("%s: the refusal quoted the content it withheld: %v", name, deliveryErr)
		}
	}
}

// producedEvent builds a record the way the service builds one.
func producedEvent(t *testing.T, id domain.EventID, topic string, fields ...events.Field) events.Event {
	t.Helper()
	event, err := events.New(id, "T1", "U1", events.NewPayload(topic, fields...), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func slackShapedMessageEvent(id domain.EventID) events.Event {
	return events.Event{
		ID:          id,
		WorkspaceID: "T1",
		ActorID:     "U1",
		Topic:       "message.created",
		Payload:     `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"hello"}`,
		CreatedAt:   time.Unix(1700000000, 0).UTC(),
	}
}

// Both delivery formats read one rule about one question: may this record be
// handed to an audience. They were separate implementations of it, and the
// record format's copy refused a payload carrying a Slack event type — which is
// the shape every official client parses and the shape the qualification
// fixtures store. The worker classifies that refusal as permanent, so the same
// binary that delivers the record under -delivery-format slack-events
// acknowledged it out of the outbox and destroyed it under -delivery-format
// record.
func TestBothDeliveryFormatsAcceptTheSameRecords(t *testing.T) {
	compatibility := []events.Record{
		{Sequence: 1, Event: slackShapedMessageEvent("evt_1")},
	}
	for _, record := range compatibility {
		var recordBody []byte
		recordDelivery, err := newHTTPDeliveryWithClient("https://delivery.invalid/events", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			recordBody, _ = io.ReadAll(request.Body)
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		recordErr := recordDelivery(context.Background(), record)
		if recordErr != nil {
			t.Fatalf("%s: a committed event was permanently dropped by the record format: %v", record.Event.ID, recordErr)
		}
		if len(recordBody) == 0 {
			t.Fatalf("%s: nothing was delivered", record.Event.ID)
		}
	}
}

// A destination that has been retired fails one cycle, is released with a retry
// time a full lease in the future, and then claims nothing for every cycle until
// that retry time. Resetting the counter on those empty cycles is what left the
// budget stuck at one consecutive failure forever, with the outbox never
// draining and the process reporting itself healthy.
func TestFailureBudgetExhaustsAgainstARetiredDestination(t *testing.T) {
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	createdAt := time.Now().UTC()
	if err := repository.CreateMessage(context.Background(), domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: createdAt}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: createdAt}, ""); err != nil {
		t.Fatal(err)
	}
	const lease = 20 * time.Millisecond
	worker, err := outbox.NewWorker(repository, "worker-1", 10, lease, func(context.Context, events.Record) error {
		return errors.New("dial tcp: connection refused")
	})
	if err != nil {
		t.Fatal(err)
	}
	cycles := 0
	cycle := func(ctx context.Context) (bool, error) {
		cycles++
		count, runErr := worker.RunOnce(ctx, "T1")
		return count > 0, runErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := pollWithinFailureBudget(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cycle, time.Millisecond, 5)
	if code != exitRuntime {
		t.Fatalf("exit code=%d after %d cycles against a permanently dead destination, want %d: the worker stays 'up' forever", code, cycles, exitRuntime)
	}
}

// A worker with nothing to do is not a worker in trouble. An idle outbox
// produces no errors at all, so a quiet deployment must never accumulate towards
// an exit.
func TestFailureBudgetIsNotSpentByAnIdleOutbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cycles := 0
	cycle := func(context.Context) (bool, error) {
		cycles++
		if cycles == 50 {
			cancel()
		}
		return false, nil
	}
	if code := pollWithinFailureBudget(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cycle, time.Microsecond, 3); code != 0 {
		t.Fatalf("exit code=%d after %d idle cycles, want 0", code, cycles)
	}
}

// A failed cycle followed by a delivered record is ordinary operation, not an
// outage: progress is what resets the budget.
func TestFailureBudgetIsResetByProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cycles := 0
	cycle := func(context.Context) (bool, error) {
		cycles++
		if cycles >= 60 {
			cancel()
			return false, nil
		}
		if cycles%2 == 0 {
			return true, nil
		}
		return false, errors.New("one delivery failed")
	}
	if code := pollWithinFailureBudget(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cycle, time.Microsecond, 3); code != 0 {
		t.Fatalf("exit code=%d after %d alternating cycles, want 0", code, cycles)
	}
}

func TestHTTPDeliveryRejectsNonAbsoluteURL(t *testing.T) {
	if _, err := newHTTPDelivery("/relative"); err == nil {
		t.Fatal("relative delivery URL accepted")
	}
}

// A record whose payload can never be encoded for Slack must be classified
// permanent — retrying it forever stops the outbox from draining — while the
// classifier stays shared by every delivery format that encodes.
func TestEncodeFailuresAreClassifiedPermanent(t *testing.T) {
	legacy := events.Record{Sequence: 1, Event: events.Event{ID: "evt_1", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123", CreatedAt: time.Unix(1700000000, 0).UTC()}}
	if _, err := events.SlackEventBodies(legacy, "A1"); err == nil {
		t.Fatal("an identifier-only payload encoded")
	} else if classified := classifyEncodeFailure(legacy, err); !errors.Is(classified, outbox.ErrPermanent) {
		t.Fatalf("identifier-only payload error=%v, want %v", classified, outbox.ErrPermanent)
	}
	internal := events.Record{Sequence: 2, Event: events.Event{ID: "evt_2", WorkspaceID: "T1", Topic: events.UserPhotoBlobDeleteTopic, Payload: "T1/users/U1/photo_1", CreatedAt: time.Unix(1700000000, 0).UTC()}}
	if _, err := events.SlackEventBodies(internal, "A1"); err == nil {
		t.Fatal("an internal record encoded")
	} else if classified := classifyEncodeFailure(internal, err); !errors.Is(classified, outbox.ErrPermanent) {
		t.Fatalf("internal record error=%v, want %v", classified, outbox.ErrPermanent)
	}
	incomplete := events.Record{Sequence: 3, Event: events.Event{
		ID: "evt_3", WorkspaceID: "T1", Topic: "reaction.added",
		Payload:   `{"type":"reaction.added","event_ts":"1700000000.000000","channel_id":"C1","user_id":"U1","reaction":"wave"}`,
		CreatedAt: time.Unix(1700000000, 0).UTC(),
	}}
	if _, err := events.SlackEventBodies(incomplete, "A1"); err == nil {
		t.Fatal("an incomplete translated payload encoded")
	} else if classified := classifyEncodeFailure(incomplete, err); !errors.Is(classified, outbox.ErrPermanent) || !errors.Is(classified, events.ErrSlackEventIncomplete) {
		t.Fatalf("incomplete translated payload error=%v, want permanent %v", classified, events.ErrSlackEventIncomplete)
	}
}
