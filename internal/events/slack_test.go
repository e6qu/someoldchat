package events

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSlackEventBodyWrapsInnerEvent(t *testing.T) {
	// Topic and the payload's "type" name the same event. New guarantees that at
	// the write boundary and Deliverable now requires it on read, because Topic
	// alone decides internal-topic exclusion, recipient scoping and the browser
	// event name while the consumer acts on the payload.
	body, err := SlackEventBody(Record{Sequence: 9, Event: Event{
		ID: "Ev1", WorkspaceID: "T1", Topic: "message", CreatedAt: time.Unix(1700000000, 0).UTC(),
		Payload: `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"hello"}`,
	}}, "A1")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(envelope["event"], &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "message" || string(envelope["api_app_id"]) != `"A1"` || string(envelope["event_id"]) != `"Ev1"` {
		t.Fatalf("envelope=%s", body)
	}
}

// The contract is that a durable payload is the Slack *inner* event and
// SlackEventBody builds the envelope around it. A stored payload that is itself
// an envelope is a foreign record no producer in this system writes, and the
// branch that used to accept one returned unsentinelled errors that the worker
// classified as retryable — so such a record was re-claimed every lease period
// forever. Every rejection here must therefore carry a sentinel the worker
// classifies as permanent.
func TestSlackEventBodyRejectsAForeignEnvelopePayloadPermanently(t *testing.T) {
	payload := `{"type":"event_callback","team_id":"T1","api_app_id":"A1","event_id":"Ev1","event_time":1700000000,"event":{"type":"message","event_ts":"1700000000.000000"}}`
	_, err := SlackEventBody(Record{Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: "event_callback", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: payload}}, "A1")
	if !errors.Is(err, ErrPayloadMalformed) {
		t.Fatalf("foreign envelope error=%v, want %v so the worker drops it instead of retrying forever", err, ErrPayloadMalformed)
	}
}

// Every error SlackEventBody can return about a record must be classifiable, or
// the caller has no way to tell "this record will never encode" from "the
// destination is down" and retries a poisoned record forever.
func TestSlackEventBodyErrorsAboutARecordAllCarrySentinels(t *testing.T) {
	for name, record := range map[string]Record{
		"identifier payload":     {Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: "M1"}},
		"payload without a type": {Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: `{"text":"hello"}`}},
		"topic disagreement":     {Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: `{"type":"message.ephemeral","event_ts":"1.0","user_id":"U2","text":"secret"}`}},
		"missing event_ts":       {Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: `{"type":"message.created"}`}},
		"internal record":        {Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: UserPhotoBlobDeleteTopic, CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: "T1/users/U1/photo"}},
		"recipient scoped":       {Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: EphemeralMessageTopic, CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: `{"type":"message.ephemeral","event_ts":"1.0","user_id":"U2","text":"secret"}`}},
		"incomplete record":      {Event: Event{ID: "", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: `{"type":"message.created","event_ts":"1.0"}`}},
	} {
		_, err := SlackEventBody(record, "A1")
		if err == nil {
			t.Fatalf("%s: an undeliverable record was encoded", name)
		}
		if !errors.Is(err, ErrPayloadMalformed) && !errors.Is(err, ErrPayloadInternal) && !errors.Is(err, ErrPayloadRecipientScoped) && !errors.Is(err, ErrEventIncomplete) {
			t.Fatalf("%s: error=%v carries no sentinel, so it is retried forever", name, err)
		}
	}
}

func TestSlackEventBodyRejectsIdentifierOnlyPayload(t *testing.T) {
	_, err := SlackEventBody(Record{Event: Event{ID: "Ev1", WorkspaceID: "T1", CreatedAt: time.Now().UTC(), Payload: "M1"}}, "A1")
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("identifier-only payload error=%v", err)
	}
}

func TestSlackSignatureUsesPublishedV0Format(t *testing.T) {
	timestamp := time.Unix(1531420618, 0).UTC()
	body := []byte("token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c")
	signature, err := SlackSignature("8f742231b10e8888abcd99yyyzzz85a5", timestamp, body)
	if err != nil {
		t.Fatal(err)
	}
	if signature != "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503" {
		t.Fatalf("signature=%q", signature)
	}
	if _, err := SlackSignature("", timestamp, body); err == nil {
		t.Fatal("empty signing secret accepted")
	}
}
