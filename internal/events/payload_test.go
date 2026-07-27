package events

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// A bare identifier cannot be handed to New at all: Payload has no exported
// field and no conversion from a string, so
//
//	events.New(id, workspace, actor, string(message.ID), createdAt)
//
// does not compile. This test covers the one runtime hole the compiler leaves,
// which is the zero value.
func TestZeroPayloadCannotBecomeAnEvent(t *testing.T) {
	if _, err := New("Ev1", "T1", "", Payload{}, time.Unix(1700000000, 0)); !errors.Is(err, ErrPayloadRequired) {
		t.Fatalf("zero payload error=%v, want %v", err, ErrPayloadRequired)
	}
	if _, err := New("Ev1", "T1", "", NewPayload(" "), time.Unix(1700000000, 0)); !errors.Is(err, ErrPayloadRequired) {
		t.Fatalf("blank topic error=%v, want %v", err, ErrPayloadRequired)
	}
	if _, err := New("Ev1", "T1", "", BlobKey("message.created", "key"), time.Unix(1700000000, 0)); !errors.Is(err, ErrPayloadRequired) {
		t.Fatalf("blob payload on a deliverable topic error=%v, want %v", err, ErrPayloadRequired)
	}
	if _, err := New("Ev1", "T1", "", NewPayload(FileBlobDeleteTopic), time.Unix(1700000000, 0)); !errors.Is(err, ErrPayloadRequired) {
		t.Fatalf("object payload on an internal topic error=%v, want %v", err, ErrPayloadRequired)
	}
}

func TestNewEventRequiresIdentityAndTime(t *testing.T) {
	payload := NewPayload("message.created", String("message_id", "M1"))
	if _, err := New("", "T1", "", payload, time.Unix(1700000000, 0)); !errors.Is(err, ErrEventIncomplete) {
		t.Fatal("an event without an ID was accepted")
	}
	if _, err := New("Ev1", "", "", payload, time.Unix(1700000000, 0)); !errors.Is(err, ErrEventIncomplete) {
		t.Fatal("an event without a workspace was accepted")
	}
	if _, err := New("Ev1", "T1", "", payload, time.Time{}); !errors.Is(err, ErrEventIncomplete) {
		t.Fatal("an event without a creation time was accepted")
	}
}

func TestPayloadEncodesSelfDescribingObject(t *testing.T) {
	event, err := New("Ev1", "T1", "U1", NewPayload("message.created",
		String("message_id", "M1"),
		String("channel_id", "C1"),
		Strings("mentions", nil),
		Int("count", 2),
		Bool("thread", true),
		JSON("blocks", `[{"type":"section"}]`),
		JSON("attachments", ""),
	), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if event.Topic != "message.created" {
		t.Fatalf("topic=%q", event.Topic)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(event.Payload), &object); err != nil {
		t.Fatalf("payload %q is not a JSON object: %v", event.Payload, err)
	}
	if object["type"] != "message.created" {
		t.Fatalf("payload type=%v, want the topic", object["type"])
	}
	if object["event_ts"] != "1700000000.000000" {
		t.Fatalf("payload event_ts=%v", object["event_ts"])
	}
	if object["message_id"] != "M1" || object["channel_id"] != "C1" || object["count"] != float64(2) || object["thread"] != true {
		t.Fatalf("payload=%s", event.Payload)
	}
	if _, present := object["attachments"]; present {
		t.Fatalf("an empty JSON field was stored: %s", event.Payload)
	}
	mentions, ok := object["mentions"].([]any)
	if !ok || len(mentions) != 0 {
		t.Fatalf("mentions=%v, want an empty list rather than null", object["mentions"])
	}
	delivered, err := Deliverable(event)
	if err != nil {
		t.Fatalf("a produced payload was not deliverable: %v", err)
	}
	if value, ok := delivered.Field("message_id"); !ok || value != "M1" {
		t.Fatalf("message_id=%q ok=%v", value, ok)
	}
	if _, ok := delivered.Field("count"); ok {
		t.Fatal("a non-string field was reported as a string")
	}
}

func TestPayloadRejectsReservedAndDuplicateFields(t *testing.T) {
	for name, payload := range map[string]Payload{
		"reserved type":     NewPayload("message.created", String("type", "message")),
		"reserved event_ts": NewPayload("message.created", String("event_ts", "1.0")),
		"blank name":        NewPayload("message.created", String(" ", "value")),
		"duplicate":         NewPayload("message.created", String("message_id", "M1"), String("message_id", "M2")),
		"invalid JSON":      NewPayload("message.created", JSON("blocks", "{not json")),
	} {
		if _, err := New("Ev1", "T1", "", payload, time.Unix(1700000000, 0)); !errors.Is(err, ErrPayloadFieldInvalid) {
			t.Fatalf("%s: error=%v, want %v", name, err, ErrPayloadFieldInvalid)
		}
	}
}

// A payload that consists of a bare identifier is exactly what every producer
// stored before the typed payload contract, and it is undeliverable on every
// transport. Deliverable must refuse it with a sentinel rather than hand it on.
func TestDeliverableRefusesIdentifierOnlyPayloads(t *testing.T) {
	for _, payload := range []string{"M0123456789", "", "not json", `{"message_id":"M1"}`, `{"type":""}`, `{"type":5}`, `["message"]`} {
		if _, err := Deliverable(Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", Payload: payload}); !errors.Is(err, ErrPayloadMalformed) {
			t.Fatalf("payload %q error=%v, want %v", payload, err, ErrPayloadMalformed)
		}
	}
}

// Blob-cleanup records exist for the cleanup worker alone. Their payload is the
// object-storage key, which must never be published to an app, a webhook or a
// browser.
func TestBlobKeyRecordsAreNotDeliverable(t *testing.T) {
	event, err := New("Ev1", "T1", "", BlobKey(UserPhotoBlobDeleteTopic, "T1/users/U1/photo_1"), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if event.Payload != "T1/users/U1/photo_1" {
		t.Fatalf("payload=%q, want the object key the cleanup worker deletes", event.Payload)
	}
	if _, err := Deliverable(event); !errors.Is(err, ErrPayloadInternal) {
		t.Fatalf("error=%v, want %v", err, ErrPayloadInternal)
	}
	if _, err := SlackEventBodies(Record{Sequence: 1, Event: event}, "A1"); !errors.Is(err, ErrPayloadInternal) {
		t.Fatalf("Slack body error=%v, want %v", err, ErrPayloadInternal)
	}
	if !InternalTopic(FileBlobDeleteTopic) || !InternalTopic(UserPhotoBlobDeleteTopic) || InternalTopic("message.created") {
		t.Fatal("the internal topic set is wrong")
	}
	if _, err := New("Ev2", "T1", "", BlobKey(UserPhotoBlobDeleteTopic, " "), time.Unix(1700000000, 0)); !errors.Is(err, ErrPayloadRequired) {
		t.Fatalf("blank blob key error=%v, want %v", err, ErrPayloadRequired)
	}
}

func TestUntranslatedProducedPayloadIsWithheldFromSlack(t *testing.T) {
	event, err := New("Ev1", "T1", "U1", NewPayload("message.created", String("message_id", "M1"), String("channel_id", "C1")), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := SlackEventBodies(Record{Sequence: 3, Event: event}, "A1")
	if err != nil {
		t.Fatalf("evaluating a produced record for Slack delivery: %v", err)
	}
	if len(bodies) != 0 {
		t.Fatalf("message.created payload without message content became %d Slack events", len(bodies))
	}
}

// An ephemeral message is defined as visible to exactly one user and its
// payload carries that user's text, blocks and attachments. A consumer that
// delivers to an audience has no recipient to filter on, so it must not be
// handed the record at all.
func TestRecipientScopedRecordsAreNotBroadcastable(t *testing.T) {
	event, err := New("Ev1", "T1", "U1", NewPayload(EphemeralMessageTopic,
		String("channel_id", "C1"),
		String("user_id", "U2"),
		String("text", "only for U2"),
	), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := Deliverable(event)
	if err != nil {
		t.Fatalf("a recipient-scoped record must still be deliverable to its recipient: %v", err)
	}
	if recipient, ok := delivered.Field("user_id"); !ok || recipient != "U2" {
		t.Fatalf("recipient=%q ok=%v", recipient, ok)
	}
	if _, err := Broadcastable(event); !errors.Is(err, ErrPayloadRecipientScoped) {
		t.Fatalf("broadcast error=%v, want %v", err, ErrPayloadRecipientScoped)
	}
	if _, err := SlackEventBodies(Record{Sequence: 1, Event: event}, "A1"); !errors.Is(err, ErrPayloadRecipientScoped) {
		t.Fatalf("Slack body error=%v, want %v", err, ErrPayloadRecipientScoped)
	}
	if !RecipientScoped(EphemeralMessageTopic) || RecipientScoped("message.created") {
		t.Fatal("the recipient-scoped topic set is wrong")
	}
}

// Encoding a durable record to JSON is what every transport that ships the
// record itself — rather than a payload it decoded for one recipient — does.
// That step must fail closed, because a transport that never decodes the
// payload cannot forget a filter it does not call: the record-format worker
// POSTed a whole ephemeral message, text and recipient included, to a
// third-party URL precisely because it only called json.Marshal.
func TestMarshallingARecordRefusesEveryPayloadNoAudienceMayReceive(t *testing.T) {
	ephemeral, err := New("Ev1", "T1", "U1", NewPayload(EphemeralMessageTopic,
		String("channel_id", "C1"),
		String("user_id", "U2"),
		String("text", "SECRET-ONLY-FOR-U2"),
	), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	internal, err := New("Ev2", "T1", "", BlobKey(UserPhotoBlobDeleteTopic, "T1/users/U1/photo_secret"), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	// A record filed under an ordinary topic whose payload describes itself as
	// recipient-scoped is refused for what it carries, not for what it is filed
	// as: the topic is exactly the field a rebuilt record can lie about.
	hidden := Event{ID: "Ev3", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0),
		Payload: `{"type":"message.ephemeral","event_ts":"1700000000.000000","user_id":"U2","text":"SECRET-ONLY-FOR-U2"}`}
	// A payload written before the typed payload contract is an opaque
	// identifier with no meaning outside this system. Encoding it POSTed that
	// identifier to a third-party URL as the event body while every other
	// transport refused the same record.
	legacy := Event{ID: "Ev5", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: time.Unix(1700000000, 0)}
	for name, value := range map[string]struct {
		event    Event
		sentinel error
	}{
		"recipient scoped":               {ephemeral, ErrPayloadRecipientScoped},
		"internal":                       {internal, ErrPayloadInternal},
		"recipient-scoped payload":       {hidden, ErrPayloadRecipientScoped},
		"payload that describes nothing": {legacy, ErrPayloadMalformed},
	} {
		body, err := json.Marshal(Record{Sequence: 1, Event: value.event})
		if err == nil {
			t.Fatalf("%s: a record no audience may receive was encoded: %s", name, body)
		}
		if !errors.Is(err, value.sentinel) {
			t.Fatalf("%s: error=%v, want %v so the caller classifies it as permanent", name, err, value.sentinel)
		}
		if strings.Contains(err.Error(), "SECRET-ONLY-FOR-U2") || strings.Contains(err.Error(), "photo_secret") {
			t.Fatalf("%s: the refusal quoted the content it withheld: %v", name, err)
		}
	}
	// A record every consumer may receive still encodes, including one whose
	// payload carries a Slack event type — which is what an official client
	// must parse, and what refusing this format's records used to destroy.
	ordinary, err := New("Ev4", "T1", "U1", NewPayload("message.created", String("message_id", "M1")), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	for name, event := range map[string]Event{
		"produced":      ordinary,
		"compatibility": {ID: "Ev6", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message","channel":"C1","text":"hello"}`, CreatedAt: time.Unix(1700000000, 0)},
	} {
		body, err := json.Marshal(Record{Sequence: 2, Event: event})
		if err != nil {
			t.Fatalf("%s: a broadcastable record was refused: %v", name, err)
		}
		var round Record
		if err := json.Unmarshal(body, &round); err != nil || round.Event.ID != event.ID || round.Event.Payload != event.Payload {
			t.Fatalf("%s: record did not round-trip: %s err=%v", name, body, err)
		}
	}
}

// Topic and the payload's "type" are separate storage columns and separate wire
// fields. Topic alone decides internal-topic exclusion, recipient scoping and
// the browser event name, so a reader that accepts a disagreement makes those
// three decisions about an event the record does not contain.
// A record's routing is decided by its topic and its content by its payload. A
// record this system produces cannot disagree, because the payload's type is
// derived from the topic at construction. A record whose payload carries a
// Slack event type is a different thing entirely — it is what an official
// Socket Mode or real-time client must receive — and refusing it delivered
// nothing to any official client while protecting against nothing a producer
// can do.
func TestAPayloadCarryingASlackEventTypeIsStillDeliverable(t *testing.T) {
	// The shape a Socket Mode client parses.
	envelope := Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created",
		Payload: `{"type":"event_callback","team_id":"T1","event":{"type":"message","channel":"C1","text":"hello"}}`}
	if _, err := Deliverable(envelope); err != nil {
		t.Fatalf("a Socket Mode envelope was refused: %v", err)
	}
	// The shape a real-time client parses.
	realtime := Event{ID: "Ev2", WorkspaceID: "T1", Topic: "message.created",
		Payload: `{"type":"message","channel":"C1","text":"hello"}`}
	if _, err := Deliverable(realtime); err != nil {
		t.Fatalf("a real-time payload was refused: %v", err)
	}
	// What must still be refused: routing is topic-driven, so a recipient-scoped
	// or internal record is withheld regardless of what its payload says.
	scoped := Event{ID: "Ev3", WorkspaceID: "T1", Topic: EphemeralMessageTopic,
		Payload: `{"type":"message","channel":"C1","text":"only for one reader"}`}
	if _, err := Broadcastable(scoped); !errors.Is(err, ErrPayloadRecipientScoped) {
		t.Fatalf("recipient-scoped broadcast error=%v, want %v", err, ErrPayloadRecipientScoped)
	}
	internal := Event{ID: "Ev4", WorkspaceID: "T1", Topic: UserPhotoBlobDeleteTopic,
		Payload: `{"type":"message"}`}
	if _, err := Deliverable(internal); !errors.Is(err, ErrPayloadInternal) {
		t.Fatalf("internal record error=%v, want %v", err, ErrPayloadInternal)
	}
	// And a payload that describes nothing at all is still refused.
	opaque := Event{ID: "Ev5", WorkspaceID: "T1", Topic: "message.created", Payload: "M1"}
	if _, err := Deliverable(opaque); !errors.Is(err, ErrPayloadMalformed) {
		t.Fatalf("identifier payload error=%v, want %v", err, ErrPayloadMalformed)
	}
}

// One set, one predicate. A second copy elsewhere means a topic added to one
// and not the other is either published to apps or starves the worker that
// exists to consume it, with no compile error either way.
func TestInternalTopicsIsTheSameSetAsThePredicate(t *testing.T) {
	listed := InternalTopics()
	if len(listed) == 0 {
		t.Fatal("the internal topic set is empty")
	}
	for _, topic := range listed {
		if !InternalTopic(topic) {
			t.Fatalf("InternalTopics lists %q but InternalTopic rejects it", topic)
		}
	}
	listed[0] = "message.created"
	if InternalTopic(InternalTopics()[0]) != true {
		t.Fatal("a caller mutated the shared internal topic set")
	}
}

// A payload built from a slice the caller goes on to reuse must not change
// after it is built; the type exists so that misuse is not representable.
func TestNewPayloadCopiesItsFields(t *testing.T) {
	fields := []Field{String("message_id", "M1")}
	payload := NewPayload("message.created", fields...)
	fields[0] = String("message_id", "M2")
	event, err := New("Ev1", "T1", "U1", payload, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event.Payload, `"message_id":"M1"`) {
		t.Fatalf("payload=%s, want the fields the payload was built with", event.Payload)
	}
}

func TestSlackEventBodiesRefuseIdentifierOnlyPayload(t *testing.T) {
	record := Record{Sequence: 1, Event: Event{ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123", CreatedAt: time.Unix(1700000000, 0)}}
	if _, err := SlackEventBodies(record, "A1"); !errors.Is(err, ErrPayloadMalformed) {
		t.Fatalf("error=%v, want %v", err, ErrPayloadMalformed)
	}
}
