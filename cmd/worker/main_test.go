package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
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
	record := events.Record{Sequence: 1, Event: events.Event{ID: "evt_1", WorkspaceID: "T1", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"hello"}`}}
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

func TestSlackEventDeliveryRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := newSlackEventDelivery("https://delivery.invalid/events", "", "secret"); err == nil {
		t.Fatal("empty app ID accepted")
	}
	if _, err := newSlackEventDelivery("https://delivery.invalid/events", "A1", ""); err == nil {
		t.Fatal("empty signing secret accepted")
	}
}
