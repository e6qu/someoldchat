package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// refusalSentinels is every sentinel a refusal can carry. The property below
// compares them one by one rather than comparing "refused or not", because two
// halves that refuse the same record for different reasons are still a
// divergence: cmd/worker classifies a delivery failure as permanent or
// retryable by sentinel (cmd/worker/main.go permanentDeliveryFailure) and the
// Socket Mode handler decides between skipping a record and closing the
// connection by sentinel (internal/socketmode/socketmode.go), so a record that
// Broadcastable calls recipient-scoped and MarshalJSON calls malformed is
// handled two different ways by the same binary.
var refusalSentinels = []error{ErrPayloadInternal, ErrPayloadRecipientScoped, ErrPayloadMalformed}

// serializationRecords is the ONE input table both halves of the serialization
// rule are asserted over.
//
// The topics are taken from InternalTopics and RecipientScopedTopics rather
// than named here: naming EphemeralMessageTopic would exercise the rule for
// that topic alone, so adding a second recipient-scoped topic would reproduce
// the whole-workspace broadcast for the new topic with every test still green.
func serializationRecords(t *testing.T) map[string]Event {
	t.Helper()
	at := time.Unix(1700000000, 0)
	records := map[string]Event{
		// A record every consumer may receive.
		"produced": mustEvent(t, "Ev-ok", NewPayload("message.created", String("message_id", "M1")), at),
		// The compatibility payloads: a payload carrying a Slack event type is
		// what an official Socket Mode or real-time client must parse, and
		// refusing it broke every one of them.
		"slack envelope payload": {ID: "Ev-envelope", WorkspaceID: "T1", Topic: "message.created", CreatedAt: at,
			Payload: `{"type":"event_callback","event_ts":"1700000000.000000","team_id":"T1","event":{"type":"message","channel":"C1","text":"hello"}}`},
		"slack message payload": {ID: "Ev-message", WorkspaceID: "T1", Topic: "message.created", CreatedAt: at,
			Payload: `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"hello"}`},
		// A payload written before the typed payload contract: an opaque
		// identifier with no meaning outside this system.
		"bare identifier": {ID: "Ev-bare", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123456789", CreatedAt: at},
		"empty payload":   {ID: "Ev-empty", WorkspaceID: "T1", Topic: "message.created", Payload: "", CreatedAt: at},
		"not json":        {ID: "Ev-notjson", WorkspaceID: "T1", Topic: "message.created", Payload: "not json", CreatedAt: at},
		"json array":      {ID: "Ev-array", WorkspaceID: "T1", Topic: "message.created", Payload: `["message"]`, CreatedAt: at},
		"blank type":      {ID: "Ev-blank", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":""}`, CreatedAt: at},
		"missing type":    {ID: "Ev-notype", WorkspaceID: "T1", Topic: "message.created", Payload: `{"message_id":"M1"}`, CreatedAt: at},
	}
	for _, topic := range InternalTopics() {
		records["internal topic "+topic] = mustEvent(t, "Ev-internal", BlobKey(topic, "T1/secret-blob"), at)
		// The record a transport codec can rebuild: an ordinary topic over a
		// payload that describes itself as internal work.
		records["internal payload under an ordinary topic "+topic] = Event{ID: "Ev-hidden-internal", WorkspaceID: "T1", Topic: "message.created", CreatedAt: at,
			Payload: fmt.Sprintf(`{"type":%q,"event_ts":"1700000000.000000","key":"T1/secret-blob"}`, topic)}
	}
	for _, topic := range RecipientScopedTopics() {
		records["recipient-scoped topic "+topic] = mustEvent(t, "Ev-scoped", NewPayload(topic,
			String("channel_id", "C1"), String("user_id", "U2"), String("text", "SECRET-ONLY-FOR-U2")), at)
		records["recipient-scoped payload under an ordinary topic "+topic] = Event{ID: "Ev-hidden-scoped", WorkspaceID: "T1", Topic: "message.created", CreatedAt: at,
			Payload: fmt.Sprintf(`{"type":%q,"event_ts":"1700000000.000000","user_id":"U2","text":"SECRET-ONLY-FOR-U2"}`, topic)}
		// The divergence the other way round: a restricted topic over an
		// ordinary payload is still restricted, because Topic alone decides the
		// event name a browser receives and the recipient filter.
		records["ordinary payload under a recipient-scoped topic "+topic] = Event{ID: "Ev-scoped-topic", WorkspaceID: "T1", Topic: topic, CreatedAt: at,
			Payload: `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"only for one reader"}`}
	}
	return records
}

func mustEvent(t *testing.T, id string, payload Payload, at time.Time) Event {
	t.Helper()
	event, err := New(domain.EventID(id), "T1", "U1", payload, at)
	if err != nil {
		t.Fatalf("build %s: %v", id, err)
	}
	return event
}

// TestSerializingARecordRefusesExactlyWhatBroadcastRefuses is the property that
// keeps the two halves of one rule together.
//
// Broadcastable is the refusal a transport that decodes the payload goes
// through; json.Marshal of the record is the refusal a transport that ships the
// durable record itself goes through. They are one rule about one question —
// may this record be handed to an audience — and they were separate
// implementations of it, so removing the payload-type check from one left the
// other asserting the opposite: the record-format worker permanently dropped
// events the same binary delivered under slack-events, while an ephemeral
// message's full text was broadcast to a whole workspace.
//
// One table, both halves, asserted equal, including which sentinel.
func TestSerializingARecordRefusesExactlyWhatBroadcastRefuses(t *testing.T) {
	for name, event := range serializationRecords(t) {
		_, broadcastErr := Broadcastable(event)
		_, marshalErr := json.Marshal(Record{Sequence: 1, Event: event})
		if (broadcastErr == nil) != (marshalErr == nil) {
			t.Errorf("%s: Broadcastable = %v but MarshalJSON = %v — the two halves of one rule disagree", name, broadcastErr, marshalErr)
			continue
		}
		for _, sentinel := range refusalSentinels {
			if errors.Is(broadcastErr, sentinel) != errors.Is(marshalErr, sentinel) {
				t.Errorf("%s: the two halves refuse for different reasons: Broadcastable %v, MarshalJSON %v (differ on %v)", name, broadcastErr, marshalErr, sentinel)
			}
		}
		if broadcastErr != nil {
			if strings.Contains(broadcastErr.Error(), "SECRET-ONLY-FOR-U2") || strings.Contains(broadcastErr.Error(), "secret-blob") {
				t.Errorf("%s: the refusal quoted the content it withheld: %v", name, broadcastErr)
			}
			if !errors.Is(broadcastErr, ErrPayloadInternal) && !errors.Is(broadcastErr, ErrPayloadRecipientScoped) && !errors.Is(broadcastErr, ErrPayloadMalformed) {
				t.Errorf("%s: refusal %v carries no sentinel a caller classifies", name, broadcastErr)
			}
		}
	}
}

func TestEventJSONNeverExposesPrivateDeliverySnapshot(t *testing.T) {
	event := mustEvent(t, "Ev-private", NewPayload("message.created", String("message_id", "M1")), time.Unix(1700000000, 0))
	event.PrivatePayload = `{"current":{"text":"DO-NOT-BROADCAST"}}`
	encoded, err := json.Marshal(Record{Sequence: 1, Event: event})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "DO-NOT-BROADCAST") || strings.Contains(string(encoded), "PrivatePayload") || strings.Contains(string(encoded), "private_payload") {
		t.Fatalf("private delivery snapshot crossed the public JSON boundary: %s", encoded)
	}
	if !strings.Contains(string(encoded), `message_id`) || !strings.Contains(string(encoded), `M1`) {
		t.Fatalf("public routing payload disappeared with the private snapshot: %s", encoded)
	}
}

// TestNoRecordIsWithheldFromOneRecipientButBroadcastToEveryone states the
// direction that matters for a leak: whatever Deliverable hands to a consumer
// that can filter on a recipient, Broadcastable must refuse for a consumer that
// cannot — unless the record is addressed to nobody in particular.
func TestARestrictedPayloadIsNeverBroadcast(t *testing.T) {
	for name, event := range serializationRecords(t) {
		delivered, deliverErr := Deliverable(event)
		if deliverErr != nil {
			continue
		}
		restricted := AddressedToOneRecipient(event.Topic, delivered)
		_, broadcastErr := Broadcastable(event)
		if restricted && broadcastErr == nil {
			t.Errorf("%s: a record addressed to one recipient was broadcastable", name)
		}
		if !restricted && broadcastErr != nil && !errors.Is(broadcastErr, ErrPayloadInternal) {
			t.Errorf("%s: a record addressed to everyone was refused: %v", name, broadcastErr)
		}
	}
}

// TestMarshalJSONIsBroadcastable asserts the delegation structurally rather
// than behaviourally. The property above compares outcomes; this reads the
// source and fails if MarshalJSON grows a second copy of the rule — which is
// how the two halves diverged in the first place, with every test green.
func TestMarshalJSONIsBroadcastable(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "event.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "MarshalJSON" && function.Recv != nil {
			body = function
		}
	}
	if body == nil {
		t.Fatal("event.go declares no MarshalJSON")
	}
	called := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok {
			called[identifier.Name] = true
		}
		return true
	})
	if !called["Broadcastable"] {
		t.Error("MarshalJSON does not delegate to Broadcastable; a second implementation of the audience rule is exactly how the halves diverged")
	}
	for _, forbidden := range []string{"InternalTopic", "RecipientScoped", "decodeDelivered", "refuseBroadcast"} {
		if called[forbidden] {
			t.Errorf("MarshalJSON applies %s itself instead of delegating; the rule must have one implementation", forbidden)
		}
	}
}
