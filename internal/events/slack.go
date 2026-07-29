package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrSlackEventIncomplete reports a durable record whose topic maps to a Slack
// event that this repository knows how to build, but whose payload is missing a
// field that event requires. It is a defect in the producer, not in the
// destination, so it carries a sentinel the outbox worker classifies as
// permanent: retrying the same record forever would hide the missing field and
// stop the outbox draining.
var ErrSlackEventIncomplete = errors.New("durable payload lacks a field the Slack event requires")

const targetAppIDField = "target_app_id"

// Inner is one Slack inner event — the object an Events API envelope carries as
// "event", and the object an RTM socket carries on its own.
//
// The type exists so a transport cannot assemble one. The pinned wrapper
// (specs/upstream/slack-api-specs/events-api/slack_common_event_wrapper_schema.json)
// requires exactly two fields of every inner event, "type" and "event_ts", and
// the constructor is the only way to obtain a non-empty Inner, so both are
// always present. Everything else the wrapper leaves to additionalProperties.
type Inner struct {
	fields map[string]json.RawMessage
}

// Type reports the Slack event name.
func (i Inner) Type() string {
	var name string
	if err := json.Unmarshal(i.fields[payloadTypeField], &name); err != nil {
		return ""
	}
	return name
}

// Encode renders the inner event with stable key ordering.
func (i Inner) Encode() (string, error) {
	return encodeObject(i.fields)
}

// newInner builds an inner event of the named Slack type, taking its event_ts
// from the durable payload. Every durable payload carries one — Payload.encode
// writes it — so an absent event_ts means the record predates the typed payload
// contract and is not translatable.
func newInner(eventType string, delivered Delivered, fields ...Field) (Inner, error) {
	eventTS, ok := delivered.Field(payloadEventTSField)
	if !ok || strings.TrimSpace(eventTS) == "" {
		return Inner{}, fmt.Errorf("%w: %s", ErrSlackEventIncomplete, payloadEventTSField)
	}
	object := map[string]json.RawMessage{
		payloadTypeField:    mustEncodeString(eventType),
		payloadEventTSField: mustEncodeString(eventTS),
	}
	for _, field := range fields {
		if field.err != nil {
			return Inner{}, field.err
		}
		if field.name == "" {
			continue
		}
		if _, exists := object[field.name]; exists {
			return Inner{}, fmt.Errorf("%w: %s is set twice", ErrPayloadFieldInvalid, field.name)
		}
		object[field.name] = field.value
	}
	return Inner{fields: object}, nil
}

// stringFields reads the payload fields a translation requires, naming the
// first one that is missing. Naming it matters: the two producers that omit a
// timestamp (internal/service/messages.go reactionPayload and
// messageItemPayload) are found from this error and from nothing else.
func stringFields(delivered Delivered, names ...string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, ok := delivered.Field(name)
		if !ok || value == "" {
			return nil, fmt.Errorf("%w: %s payload has no %s", ErrSlackEventIncomplete, delivered.Type, name)
		}
		values[name] = value
	}
	return values, nil
}

// itemEvent renders the reaction, pin and star family: an actor, and the
// message the action was taken on.
//
// Grounding. The pinned AsyncAPI lists reaction_added, reaction_removed,
// pin_added, pin_removed, star_added and star_removed as topics, so the event
// names are settled. Only one inner event of this family is worked out in the
// pinned material — the reaction_added example in the common wrapper schema:
//
//	{"type":"reaction_added","user":"U024BE7LH","reaction":"thumbsup",
//	 "item_user":"U0G9QF9C6","event_ts":"1360782804.083113",
//	 "item":{"type":"message","channel":"C0G9QF9GZ","ts":"1360782400.498405"}}
//
// So reaction_added and reaction_removed are grounded. The pin and star inner
// shapes are NOT settled by the pinned snapshot; the field names below are
// carried over from that one example rather than invented, and the report
// records them as unsettled. item_user is omitted throughout because the
// durable payload names the actor and not the item's author, and a guessed
// author is worse than an absent field.
func itemEvent(eventType string, withReaction bool) builder {
	required := []string{"channel_id", "user_id", "ts"}
	if withReaction {
		required = append(required, "reaction")
	}
	return func(delivered Delivered) ([]Inner, error) {
		values, err := stringFields(delivered, required...)
		if err != nil {
			return nil, err
		}
		item, err := encodeObject(map[string]json.RawMessage{
			payloadTypeField: mustEncodeString("message"),
			"channel":        mustEncodeString(values["channel_id"]),
			"ts":             mustEncodeString(values["ts"]),
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPayloadFieldInvalid, err)
		}
		fields := []Field{String("user", values["user_id"]), JSON("item", item)}
		if withReaction {
			fields = append(fields, String("reaction", values["reaction"]))
		}
		inner, err := newInner(eventType, delivered, fields...)
		if err != nil {
			return nil, err
		}
		return []Inner{inner}, nil
	}
}

func appHomeOpened(delivered Delivered) ([]Inner, error) {
	values, err := stringFields(delivered, "user_id", "channel_id", "tab")
	if err != nil {
		return nil, err
	}
	fields := []Field{
		String("user", values["user_id"]),
		String("channel", values["channel_id"]),
		String("tab", values["tab"]),
	}
	if view, exists := delivered.Object["view"]; exists {
		var object map[string]any
		if json.Unmarshal(view, &object) != nil || object == nil {
			return nil, fmt.Errorf("%w: app_home_opened payload has an invalid view", ErrSlackEventIncomplete)
		}
		fields = append(fields, Field{name: "view", value: append(json.RawMessage(nil), view...)})
	}
	inner, err := newInner("app_home_opened", delivered, fields...)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

func broadcastableToApp(event Event, appID string) (Delivered, bool, error) {
	delivered, err := Broadcastable(event)
	if err != nil {
		return Delivered{}, false, err
	}
	target, targeted := delivered.Field(targetAppIDField)
	if targeted && target != strings.TrimSpace(appID) {
		return Delivered{}, false, nil
	}
	if targeted {
		// Targeting is routing metadata, not part of the Slack event. Copy the
		// object before removing it so callers that reuse Delivered cannot
		// observe a mutation.
		object := make(map[string]json.RawMessage, len(delivered.Object)-1)
		for name, value := range delivered.Object {
			if name != targetAppIDField {
				object[name] = value
			}
		}
		delivered.Object = object
	}
	return delivered, true, nil
}

// memberLeftChannel renders conversation.member_left and
// conversation.member_kicked as member_left_channel. The pinned AsyncAPI
// settles the event name; the inner field names follow the naming of the one
// worked example above and are recorded as unsettled.
func memberLeftChannel(delivered Delivered) ([]Inner, error) {
	values, err := stringFields(delivered, "channel_id", "user_id")
	if err != nil {
		return nil, err
	}
	inner, err := newInner("member_left_channel", delivered,
		String("user", values["user_id"]),
		String("channel", values["channel_id"]),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// memberJoinedChannel renders the one-user self-service join record. Invitations
// use the fan-out builder below because one durable invitation can name many
// users; a join always names exactly its actor.
func memberJoinedChannel(delivered Delivered) ([]Inner, error) {
	values, err := stringFields(delivered, "channel_id", "user_id")
	if err != nil {
		return nil, err
	}
	inner, err := newInner("member_joined_channel", delivered,
		String("user", values["user_id"]),
		String("channel", values["channel_id"]),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// membersJoinedChannel renders one conversation.members_invited record as one
// member_joined_channel per invited user. This is the fan-out case the []Inner
// result exists for: Slack has no bulk membership event, so a record that names
// three users is three events and not one event naming three users.
//
// Slack's member_joined_channel also carries an inviter. The durable payload
// records the acting user only in the record's ActorID, which the payload
// contract does not expose to a translation, so the field is omitted rather
// than guessed.
func membersJoinedChannel(delivered Delivered) ([]Inner, error) {
	values, err := stringFields(delivered, "channel_id")
	if err != nil {
		return nil, err
	}
	users, ok := delivered.Strings("user_ids")
	if !ok || len(users) == 0 {
		return nil, fmt.Errorf("%w: %s payload has no user_ids", ErrSlackEventIncomplete, delivered.Type)
	}
	inners := make([]Inner, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user) == "" {
			return nil, fmt.Errorf("%w: %s payload has an empty user_ids member", ErrSlackEventIncomplete, delivered.Type)
		}
		inner, err := newInner("member_joined_channel", delivered,
			String("user", user),
			String("channel", values["channel_id"]),
		)
		if err != nil {
			return nil, err
		}
		inners = append(inners, inner)
	}
	return inners, nil
}

// slackShaped reports an inner event a durable payload already carries, for a
// record this repository cannot translate.
//
// A payload produced here always describes itself as its own topic, because
// NewPayload derives the type from the topic. A payload that describes itself
// as something else was written already translated — today that is the
// qualification fixture, which stores real Slack payloads for the message
// topics no producer can translate until message content can be resolved
// safely. Shipping those verbatim is the transitional path, and it lives here,
// in one place, so no transport decides for itself what an untranslated record
// looks like.
//
// Both shapes a pre-translated payload can take are accepted and reduced to the
// same thing: a complete event_callback envelope is unwrapped to its inner
// event — the outer identity is rebuilt from this record, never trusted from
// the payload — and a bare inner event is taken as it is.
func slackShaped(topic string, delivered Delivered) (Inner, bool) {
	if delivered.Type == topic || delivered.Type == "" {
		return Inner{}, false
	}
	object := delivered.Object
	if delivered.Type == "event_callback" {
		raw, exists := object["event"]
		if !exists {
			return Inner{}, false
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil || inner == nil {
			return Inner{}, false
		}
		object = inner
	}
	name, ok := stringMember(object, payloadTypeField)
	if !ok || name == "" || name == "event_callback" {
		return Inner{}, false
	}
	if eventTS, ok := stringMember(object, payloadEventTSField); !ok || eventTS == "" {
		return Inner{}, false
	}
	copied := make(map[string]json.RawMessage, len(object))
	for member, value := range object {
		copied[member] = append(json.RawMessage(nil), value...)
	}
	return Inner{fields: copied}, true
}

func stringMember(object map[string]json.RawMessage, name string) (string, bool) {
	raw, exists := object[name]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// SlackInner translates a decoded durable record into the Slack inner events it
// becomes on a surface. It is the one place a Slack event shape is built; every
// transport goes through a wrapper below and never assembles one itself.
//
// An empty result is a decision rather than a failure: the topic maps to no
// Slack event, or the mapping exists and its inner shape is not modelled yet,
// or the event does not belong on this surface. A caller must skip the record,
// not fail on it.
//
// The delivered payload must come from Deliverable or Broadcastable — the only
// constructors of Delivered — which is where the internal and recipient-scoped
// refusals are applied to both names a record carries.
func SlackInner(topic string, delivered Delivered, surface Surface) ([]Inner, error) {
	rule := ruleFor(topic).slack
	// A known mapping's surface restriction is authoritative even for a
	// pre-translated qualification record. Otherwise filing a presence_change
	// body under user.presence_changed would bypass the table and leak an
	// RTM-only event to an application webhook.
	if rule.eventType != "" && rule.surfaces&surface == 0 {
		return nil, nil
	}
	if rule.build == nil {
		if rule.eventType != "" {
			inner, ok := slackShaped(topic, delivered)
			if !ok {
				return nil, nil
			}
			if inner.Type() != rule.eventType {
				return nil, fmt.Errorf("%w: topic %s carries a %s event, want %s", ErrPayloadFieldInvalid, topic, inner.Type(), rule.eventType)
			}
			return []Inner{inner}, nil
		}
		return nil, nil
	}
	inners, err := rule.build(delivered)
	if err != nil {
		return nil, err
	}
	for _, inner := range inners {
		if inner.Type() != rule.eventType {
			return nil, fmt.Errorf("%w: topic %s built a %s event, want %s", ErrPayloadFieldInvalid, topic, inner.Type(), rule.eventType)
		}
	}
	return inners, nil
}

// eventCallback builds the pinned Events API wrapper around one inner event.
//
// Two fields the pinned schema marks required are deliberately absent, and both
// are recorded as deviations rather than guessed:
//
//   - "token" is the deprecated verification token. This system issues none,
//     and a fabricated value would be a credential-shaped lie.
//   - "authed_users" names the users an app is installed for. The snapshot
//     predates Slack's "authorizations" replacement, and which of the two a
//     receiver expects is not settled by any pinned source.
func eventCallback(record Record, appID string, inner Inner) (map[string]json.RawMessage, error) {
	encoded, err := inner.Encode()
	if err != nil {
		return nil, fmt.Errorf("%w: Slack inner event cannot be encoded: %v", ErrPayloadMalformed, err)
	}
	return map[string]json.RawMessage{
		"type":       mustEncodeString("event_callback"),
		"team_id":    mustEncodeString(string(record.Event.WorkspaceID)),
		"api_app_id": mustEncodeString(appID),
		"event_id":   mustEncodeString(string(record.Event.ID)),
		"event_time": json.RawMessage(strconv.FormatInt(record.Event.CreatedAt.Unix(), 10)),
		"event":      json.RawMessage(encoded),
	}, nil
}

// checkRecord rejects a record whose own identity is incomplete. It is a
// separate failure from an undeliverable payload: the payload may be a
// perfectly good event, and what is wrong is the record, which no retry and no
// reconnect repairs.
func checkRecord(record Record, appID string) error {
	if record.Event.ID == "" || record.Event.WorkspaceID == "" || record.Event.CreatedAt.IsZero() {
		return fmt.Errorf("%w: Slack event record is incomplete", ErrEventIncomplete)
	}
	if strings.TrimSpace(appID) == "" {
		return fmt.Errorf("%w: Slack event app ID is required", ErrEventIncomplete)
	}
	return nil
}

// SlackEventBodies renders a durable record as the signed HTTP Events API
// bodies it becomes: zero when the record is not publishable to an app, one for
// the ordinary case, and more when one record fans out to several events.
//
// The payload is read through Broadcastable, so an internal worker record, a
// record addressed to a single user, and a record that predates the typed
// payload contract all fail with a sentinel the caller classifies as permanent
// instead of being retried forever.
func SlackEventBodies(record Record, appID string) ([][]byte, error) {
	if err := checkRecord(record, appID); err != nil {
		return nil, err
	}
	delivered, belongs, err := broadcastableToApp(record.Event, appID)
	if err != nil || !belongs {
		return nil, err
	}
	inners, err := SlackInner(record.Event.Topic, delivered, SurfaceEventsAPI)
	if err != nil || len(inners) == 0 {
		return nil, err
	}
	bodies := make([][]byte, 0, len(inners))
	for _, inner := range inners {
		envelope, err := eventCallback(record, strings.TrimSpace(appID), inner)
		if err != nil {
			return nil, err
		}
		encoded, err := encodeObject(envelope)
		if err != nil {
			// Every failure reachable from here describes the record rather than
			// the destination, so every one of them carries a sentinel the worker
			// classifies as permanent. An unsentinelled error would be classified
			// retryable and re-claimed every lease period forever, which is the
			// unbounded retry loop the typed payload contract exists to remove.
			return nil, fmt.Errorf("%w: Slack event envelope cannot be encoded: %v", ErrPayloadMalformed, err)
		}
		bodies = append(bodies, []byte(encoded))
	}
	return bodies, nil
}

// SocketModeEnvelope is one frame written to an app's Socket Mode connection,
// paired with the identifier the app acknowledges it by.
type SocketModeEnvelope struct {
	// ID is the envelope_id the app echoes back.
	ID string
	// Frame is the complete frame to write.
	Frame map[string]any
}

// SocketModeEnvelopes renders a durable record as the Socket Mode frames it
// becomes. The payload of each frame is the same event_callback document the
// HTTP Events API receives, which is what the official clients index: all three
// pinned Socket Mode suites read payload.event and dispatch on its type.
//
// Envelope identity: a record that becomes one envelope keeps the event ID, so
// the identifier an operator correlates on is the record's own. A record that
// fans out cannot — the identifiers must be distinct for the acknowledgement
// map to work — so each envelope is suffixed with its index.
func SocketModeEnvelopes(record Record, appID string) ([]SocketModeEnvelope, error) {
	if err := checkRecord(record, appID); err != nil {
		return nil, err
	}
	delivered, belongs, err := broadcastableToApp(record.Event, appID)
	if err != nil || !belongs {
		return nil, err
	}
	inners, err := SlackInner(record.Event.Topic, delivered, SurfaceSocketMode)
	if err != nil || len(inners) == 0 {
		return nil, err
	}
	envelopes := make([]SocketModeEnvelope, 0, len(inners))
	for index, inner := range inners {
		payload, err := eventCallback(record, strings.TrimSpace(appID), inner)
		if err != nil {
			return nil, err
		}
		id := string(record.Event.ID)
		if len(inners) > 1 {
			id += "#" + strconv.Itoa(index)
		}
		envelopes = append(envelopes, SocketModeEnvelope{ID: id, Frame: map[string]any{
			"envelope_id":              id,
			"type":                     "events_api",
			"accepts_response_payload": true,
			"payload":                  payload,
		}})
	}
	return envelopes, nil
}

// RTMEvents renders a durable record as the bare inner events an RTM socket
// carries. The caller has already decoded the payload through Deliverable and
// filtered it to its one recipient, which is why this takes a Delivered rather
// than a Record: RTM is the one surface with a recipient to filter on, and the
// filter must run before the shape is built.
func RTMEvents(topic string, delivered Delivered) ([]Inner, error) {
	return SlackInner(topic, delivered, SurfaceRTM)
}

// SlackSignature returns the request signature specified by Slack's signing
// secret protocol for body and timestamp.
//
// The signature covers the exact bytes of the request body. A caller must sign
// the body it sends rather than a re-encoding of it: a second json.Marshal of
// the same document can differ in key order or spacing, and the receiver
// recomputes the MAC over the bytes it received, so a re-encode fails at every
// real receiver while passing any test that re-encodes the same way.
func SlackSignature(signingSecret string, timestamp time.Time, body []byte) (string, error) {
	signingSecret = strings.TrimSpace(signingSecret)
	if signingSecret == "" {
		return "", errors.New("Slack signing secret is required")
	}
	if timestamp.IsZero() {
		return "", errors.New("Slack signature timestamp is required")
	}
	base := "v0:" + strconv.FormatInt(timestamp.Unix(), 10) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil)), nil
}
