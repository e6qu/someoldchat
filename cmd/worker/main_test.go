package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
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
	if err := delivery(context.Background(), events.Record{Sequence: 1, Event: events.Event{ID: domain.EventID("evt_1"), Topic: "message.created"}}); err != nil {
		t.Fatal(err)
	}
	if gotID != "evt_1" {
		t.Fatalf("idempotency key=%q", gotID)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestHTTPDeliveryRejectsNonAbsoluteURL(t *testing.T) {
	if _, err := newHTTPDelivery("/relative"); err == nil {
		t.Fatal("relative delivery URL accepted")
	}
}

func TestSlackEventDeliveryBuildsSignedSlackEnvelope(t *testing.T) {
	var got http.Request
	var gotBody []byte
	delivery, err := newSlackEventDeliveryWithClient("https://delivery.invalid/events", "A1", "signing-secret", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		got = *request
		gotBody, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	// The record is built by the typed constructor the service uses, so this
	// asserts the delivery of a payload production actually emits rather than a
	// hand-written envelope.
	event, err := events.New("evt_1", "T1", "U1", events.NewPayload("message.created", events.String("message_id", "M1"), events.String("channel_id", "C1")), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	record := events.Record{Sequence: 1, Event: event}
	if err := delivery(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.Header.Get("Content-Type") != "application/json" || got.Header.Get("X-Slack-Signature") == "" || got.Header.Get("X-Slack-Request-Timestamp") == "" {
		t.Fatalf("request=%+v", got)
	}
	var envelope map[string]any
	if err := json.Unmarshal(gotBody, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["type"] != "event_callback" || envelope["api_app_id"] != "A1" || envelope["team_id"] != "T1" {
		t.Fatalf("envelope=%s", gotBody)
	}
}

// A record whose payload can never be encoded for Slack must be reported as
// permanent. Retrying it forever stops the outbox from draining and produces a
// log line every lease period with no escalation.
func TestSlackEventDeliveryReportsUndeliverableRecordsAsPermanent(t *testing.T) {
	delivery, err := newSlackEventDeliveryWithClient("https://delivery.invalid/events", "A1", "signing-secret", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Error("an undeliverable record was sent to the destination")
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	legacy := events.Record{Sequence: 1, Event: events.Event{ID: "evt_1", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123", CreatedAt: time.Unix(1700000000, 0).UTC()}}
	if err := delivery(context.Background(), legacy); !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("identifier-only payload error=%v, want %v", err, outbox.ErrPermanent)
	}
	internal := events.Record{Sequence: 2, Event: events.Event{ID: "evt_2", WorkspaceID: "T1", Topic: events.UserPhotoBlobDeleteTopic, Payload: "T1/users/U1/photo_1", CreatedAt: time.Unix(1700000000, 0).UTC()}}
	if err := delivery(context.Background(), internal); !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("internal record error=%v, want %v", err, outbox.ErrPermanent)
	}
}

// A destination failure is not the record's fault: retrying is the only way to
// avoid losing a committed event to a temporarily misconfigured receiver.
func TestSlackEventDeliveryKeepsDestinationFailuresRetryable(t *testing.T) {
	delivery, err := newSlackEventDeliveryWithClient("https://delivery.invalid/events", "A1", "signing-secret", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	event, err := events.New("evt_1", "T1", "U1", events.NewPayload("message.created", events.String("message_id", "M1")), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	deliveryErr := delivery(context.Background(), events.Record{Sequence: 1, Event: event})
	if deliveryErr == nil {
		t.Fatal("a rejected delivery was reported as success")
	}
	if errors.Is(deliveryErr, outbox.ErrPermanent) {
		t.Fatalf("error=%v, want a retryable failure", deliveryErr)
	}
}

func TestSlackEventDeliveryRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := newSlackEventDelivery("https://delivery.invalid/events", "", "secret"); err == nil {
		t.Fatal("empty app ID accepted")
	}
	if _, err := newSlackEventDelivery("https://delivery.invalid/events", "A1", ""); err == nil {
		t.Fatal("empty signing secret accepted")
	}
}
