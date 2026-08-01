package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// ErrSlackEventIncomplete reports a durable record whose topic maps to a Slack
// event that this repository knows how to build, but whose payload is missing a
// field that event requires. It is a defect in the producer, not in the
// destination, so it carries a sentinel the outbox worker classifies as
// permanent: retrying the same record forever would hide the missing field and
// stop the outbox draining.
var ErrSlackEventIncomplete = errors.New("durable payload lacks a field the Slack event requires")

const targetAppIDField = "target_app_id"

// EventContext is the opaque handle Slack includes with an Events API callback
// and accepts at apps.event.authorizations.list. It carries only durable,
// non-secret identity; the API still authenticates the app-level token and
// verifies that the referenced event belongs to that app before returning
// authorization subjects.
func EventContext(appID string, record Record) (string, error) {
	if err := checkRecord(record, appID); err != nil {
		return "", err
	}
	value := strings.TrimSpace(appID) + "\x00" + strconv.FormatUint(record.Sequence, 10) + "\x00" + string(record.Event.ID)
	return "EC" + base64.RawURLEncoding.EncodeToString([]byte(value)), nil
}

// ParseEventContext decodes an EventContext. Callers must still compare the
// returned app and event identity with authenticated, app-visible state.
func ParseEventContext(value string) (string, uint64, domain.EventID, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "EC") {
		return "", 0, "", ErrPayloadFieldInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "EC"))
	if err != nil {
		return "", 0, "", ErrPayloadFieldInvalid
	}
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", 0, "", ErrPayloadFieldInvalid
	}
	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || sequence == 0 {
		return "", 0, "", ErrPayloadFieldInvalid
	}
	return parts[0], sequence, domain.EventID(parts[2]), nil
}

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
	return func(delivered Delivered, _ Surface) ([]Inner, error) {
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

func appHomeOpened(delivered Delivered, _ Surface) ([]Inner, error) {
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

func functionExecuted(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "function_execution_id", "workflow_execution_id")
	if err != nil {
		return nil, err
	}
	function, exists := delivered.Object["function"]
	if !exists || len(function) == 0 {
		return nil, fmt.Errorf("%w: function_executed payload has no function", ErrSlackEventIncomplete)
	}
	inputs, exists := delivered.Object["inputs"]
	if !exists || len(inputs) == 0 {
		return nil, fmt.Errorf("%w: function_executed payload has no inputs", ErrSlackEventIncomplete)
	}
	var functionObject, inputObject map[string]json.RawMessage
	if json.Unmarshal(function, &functionObject) != nil || functionObject == nil {
		return nil, fmt.Errorf("%w: function_executed payload has an invalid function", ErrSlackEventIncomplete)
	}
	if json.Unmarshal(inputs, &inputObject) != nil || inputObject == nil {
		return nil, fmt.Errorf("%w: function_executed payload has invalid inputs", ErrSlackEventIncomplete)
	}
	fields := []Field{
		{name: "function", value: append(json.RawMessage(nil), function...)},
		{name: "inputs", value: append(json.RawMessage(nil), inputs...)},
		String("function_execution_id", values["function_execution_id"]),
		String("workflow_execution_id", values["workflow_execution_id"]),
	}
	if token, exists := delivered.Field("bot_access_token"); exists && strings.TrimSpace(token) != "" {
		fields = append(fields, String("bot_access_token", token))
	}
	inner, err := newInner("function_executed", delivered, fields...)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

func broadcastableToApp(event Event, appID string) (Delivered, bool, error) {
	var (
		delivered Delivered
		err       error
	)
	if RecipientScoped(event.Topic) {
		if len(event.Authorizations) == 0 {
			return Delivered{}, false, ErrPayloadRecipientScoped
		}
		delivered, err = Deliverable(event)
		if err == nil {
			recipient, exists := delivered.Field("user_id")
			authorized := false
			for _, authorization := range event.Authorizations {
				authorized = authorized || (!authorization.IsBot && string(authorization.UserID) == recipient)
			}
			if !exists || recipient == "" || !authorized {
				return Delivered{}, false, nil
			}
		}
	} else {
		delivered, err = Broadcastable(event)
	}
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
func memberLeftChannel(delivered Delivered, _ Surface) ([]Inner, error) {
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
func memberJoinedChannel(delivered Delivered, _ Surface) ([]Inner, error) {
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
func membersJoinedChannel(delivered Delivered, _ Surface) ([]Inner, error) {
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
	allowed := func(name string) bool {
		if name == rule.eventType {
			return true
		}
		for _, alternate := range rule.alternates {
			if name == alternate {
				return true
			}
		}
		return false
	}
	if rule.build == nil {
		if rule.eventType != "" {
			inner, ok := slackShaped(topic, delivered)
			if !ok {
				return nil, nil
			}
			if !allowed(inner.Type()) {
				return nil, fmt.Errorf("%w: topic %s carries a %s event, want %s", ErrPayloadFieldInvalid, topic, inner.Type(), rule.eventType)
			}
			return []Inner{inner}, nil
		}
		return nil, nil
	}
	inners, err := rule.build(delivered, surface)
	if err != nil {
		return nil, err
	}
	for _, inner := range inners {
		if !allowed(inner.Type()) {
			return nil, fmt.Errorf("%w: topic %s built a %s event, want %s", ErrPayloadFieldInvalid, topic, inner.Type(), rule.eventType)
		}
	}
	return inners, nil
}

// eventCallback builds the pinned Events API wrapper around one inner event.
//
// The deprecated verification token remains deliberately absent: this system
// issues none, and a fabricated value would be a credential-shaped lie.
//
// Slack's current authorizations replacement is emitted from the app-specific
// authorization projection. Generic durable records never contain it.
func eventCallback(record Record, appID string, inner Inner) (map[string]json.RawMessage, error) {
	encoded, err := inner.Encode()
	if err != nil {
		return nil, fmt.Errorf("%w: Slack inner event cannot be encoded: %v", ErrPayloadMalformed, err)
	}
	eventContext, err := EventContext(appID, record)
	if err != nil {
		return nil, err
	}
	envelope := map[string]json.RawMessage{
		"type":          mustEncodeString("event_callback"),
		"team_id":       mustEncodeString(string(record.Event.WorkspaceID)),
		"api_app_id":    mustEncodeString(appID),
		"event_id":      mustEncodeString(string(record.Event.ID)),
		"event_context": mustEncodeString(eventContext),
		"event_time":    json.RawMessage(strconv.FormatInt(record.Event.CreatedAt.Unix(), 10)),
		"event":         json.RawMessage(encoded),
	}
	if len(record.Event.Authorizations) != 0 {
		authorizations := make([]map[string]any, 0, len(record.Event.Authorizations))
		for _, value := range record.Event.Authorizations {
			teamID := value.TeamID
			if teamID == "" {
				teamID = record.Event.WorkspaceID
			}
			authorizations = append(authorizations, map[string]any{
				"enterprise_id":         value.EnterpriseID,
				"team_id":               teamID,
				"user_id":               value.UserID,
				"is_bot":                value.IsBot,
				"is_enterprise_install": value.IsEnterpriseInstall,
			})
		}
		encodedAuthorizations, err := json.Marshal(authorizations)
		if err != nil {
			return nil, fmt.Errorf("%w: event authorizations cannot be encoded: %v", ErrPayloadMalformed, err)
		}
		envelope["authorizations"] = encodedAuthorizations
	}
	return envelope, nil
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

// eventTypeByPrivacy selects between a channel_* and a group_* event name
// from the payload's is_private. The split is Slack's: the same fact about a
// private channel travels under the group_* vocabulary.
func eventTypeByPrivacy(delivered Delivered, public, private string) (string, error) {
	isPrivate, ok := delivered.Bool("is_private")
	if !ok {
		return "", fmt.Errorf("%w: %s payload has no is_private", ErrSlackEventIncomplete, delivered.Type)
	}
	if isPrivate {
		return private, nil
	}
	return public, nil
}

// conversationObject renders the channel object channel_created and
// channel_rename carry: identifiers and workspace-visible metadata the
// durable payload snapshots, never content.
func conversationObject(delivered Delivered) (string, error) {
	values, err := stringFields(delivered, "channel_id", "name")
	if err != nil {
		return "", err
	}
	object := map[string]json.RawMessage{
		"id":         mustEncodeString(values["channel_id"]),
		"name":       mustEncodeString(values["name"]),
		"is_channel": json.RawMessage("true"),
	}
	if isPrivate, ok := delivered.Bool("is_private"); ok {
		object["is_private"] = mustEncodeBool(isPrivate)
	}
	if isMPIM, ok := delivered.Bool("is_mpim"); ok && isMPIM {
		object["is_mpim"] = json.RawMessage("true")
	}
	if created, ok := delivered.Int("created"); ok {
		object["created"] = json.RawMessage(strconv.FormatInt(created, 10))
	}
	if creator, ok := delivered.Field("user_id"); ok && creator != "" {
		object["creator"] = mustEncodeString(creator)
	}
	encoded, err := encodeObject(object)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPayloadFieldInvalid, err)
	}
	return encoded, nil
}

func mustEncodeBool(value bool) json.RawMessage {
	if value {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func channelCreated(delivered Delivered, _ Surface) ([]Inner, error) {
	channel, err := conversationObject(delivered)
	if err != nil {
		return nil, err
	}
	inner, err := newInner("channel_created", delivered, JSON("channel", channel))
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

func imCreated(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "channel_id", "user_id")
	if err != nil {
		return nil, err
	}
	object := map[string]json.RawMessage{
		"id":    mustEncodeString(values["channel_id"]),
		"is_im": json.RawMessage("true"),
	}
	if created, ok := delivered.Int("created"); ok {
		object["created"] = json.RawMessage(strconv.FormatInt(created, 10))
	}
	channel, err := encodeObject(object)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayloadFieldInvalid, err)
	}
	inner, err := newInner("im_created", delivered,
		String("user", values["user_id"]),
		JSON("channel", channel),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

func channelRenamed(delivered Delivered, _ Surface) ([]Inner, error) {
	eventType, err := eventTypeByPrivacy(delivered, "channel_rename", "group_rename")
	if err != nil {
		return nil, err
	}
	channel, err := conversationObject(delivered)
	if err != nil {
		return nil, err
	}
	inner, err := newInner(eventType, delivered, JSON("channel", channel))
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// channelState renders the archive, unarchive and delete family: the channel
// travels as its identifier, not as an object, and the acting user rides
// along when the payload names one.
func channelState(public, private string) builder {
	return func(delivered Delivered, _ Surface) ([]Inner, error) {
		eventType, err := eventTypeByPrivacy(delivered, public, private)
		if err != nil {
			return nil, err
		}
		values, err := stringFields(delivered, "channel_id")
		if err != nil {
			return nil, err
		}
		fields := []Field{String("channel", values["channel_id"])}
		if actor, ok := delivered.Field("user_id"); ok && actor != "" {
			fields = append(fields, String("user", actor))
		}
		inner, err := newInner(eventType, delivered, fields...)
		if err != nil {
			return nil, err
		}
		return []Inner{inner}, nil
	}
}

// emojiChanged renders the four emoji topics as emoji_changed with the
// current reference's subtype vocabulary: add carries name (and value when
// the payload has one), remove carries names as a list, rename carries
// old_name and new_name.
func emojiChanged(subtype string) builder {
	return func(delivered Delivered, _ Surface) ([]Inner, error) {
		fields := []Field{String("subtype", subtype)}
		switch subtype {
		case "add":
			values, err := stringFields(delivered, "name")
			if err != nil {
				return nil, err
			}
			fields = append(fields, String("name", values["name"]))
			if value, ok := delivered.Field("value"); ok && value != "" {
				fields = append(fields, String("value", value))
			} else if alias, ok := delivered.Field("alias_for"); ok && alias != "" {
				fields = append(fields, String("value", "alias:"+alias))
			}
		case "remove":
			values, err := stringFields(delivered, "name")
			if err != nil {
				return nil, err
			}
			fields = append(fields, Strings("names", []string{values["name"]}))
		case "rename":
			values, err := stringFields(delivered, "old_name", "new_name")
			if err != nil {
				return nil, err
			}
			fields = append(fields, String("old_name", values["old_name"]), String("new_name", values["new_name"]))
		default:
			return nil, fmt.Errorf("%w: emoji_changed subtype %q is not one the table decided", ErrPayloadFieldInvalid, subtype)
		}
		inner, err := newInner("emoji_changed", delivered, fields...)
		if err != nil {
			return nil, err
		}
		return []Inner{inner}, nil
	}
}

// subteamEvent renders subteam_created and subteam_updated from the subteam
// object the producer snapshots into the payload.
func subteamEvent(eventType string) builder {
	return func(delivered Delivered, _ Surface) ([]Inner, error) {
		subteam, exists := delivered.Object["subteam"]
		if !exists || len(subteam) == 0 {
			return nil, fmt.Errorf("%w: %s payload has no subteam", ErrSlackEventIncomplete, delivered.Type)
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(subteam, &object) != nil || object == nil {
			return nil, fmt.Errorf("%w: %s payload has an invalid subteam", ErrSlackEventIncomplete, delivered.Type)
		}
		inner, err := newInner(eventType, delivered, Field{name: "subteam", value: append(json.RawMessage(nil), subteam...)})
		if err != nil {
			return nil, err
		}
		return []Inner{inner}, nil
	}
}

func subteamMembersChanged(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "usergroup_id", "team_id")
	if err != nil {
		return nil, err
	}
	added, addedOK := delivered.Strings("added_users")
	removed, removedOK := delivered.Strings("removed_users")
	if !addedOK && !removedOK {
		return nil, fmt.Errorf("%w: %s payload has neither added_users nor removed_users", ErrSlackEventIncomplete, delivered.Type)
	}
	inner, err := newInner("subteam_members_changed", delivered,
		String("subteam_id", values["usergroup_id"]),
		String("team_id", values["team_id"]),
		Strings("added_users", added),
		Strings("removed_users", removed),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// userEvent renders team_join and the plain user_change from the user object
// the producer snapshots into the payload.
func userEvent(eventType string) builder {
	return func(delivered Delivered, _ Surface) ([]Inner, error) {
		inner, err := userInner(eventType, delivered)
		if err != nil {
			return nil, err
		}
		return []Inner{inner}, nil
	}
}

func userInner(eventType string, delivered Delivered) (Inner, error) {
	user, exists := delivered.Object["user"]
	if !exists || len(user) == 0 {
		return Inner{}, fmt.Errorf("%w: %s payload has no user", ErrSlackEventIncomplete, delivered.Type)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(user, &object) != nil || object == nil {
		return Inner{}, fmt.Errorf("%w: %s payload has an invalid user", ErrSlackEventIncomplete, delivered.Type)
	}
	return newInner(eventType, delivered, Field{name: "user", value: append(json.RawMessage(nil), user...)})
}

// userChanged fans one profile change out to the names the current catalog
// subscribes separately: user_change always, user_profile_changed always, and
// user_status_changed when the producer marked the change as a status change.
// The subscription filter then narrows delivery to the names each app asked
// for, and an app subscribed to more than one receives more than one event —
// which is Slack's behavior for the same change.
func userChanged(delivered Delivered, _ Surface) ([]Inner, error) {
	inners := make([]Inner, 0, 3)
	for _, eventType := range []string{"user_change", "user_profile_changed"} {
		inner, err := userInner(eventType, delivered)
		if err != nil {
			return nil, err
		}
		inners = append(inners, inner)
	}
	if statusChanged, ok := delivered.Bool("status_changed"); ok && statusChanged {
		inner, err := userInner("user_status_changed", delivered)
		if err != nil {
			return nil, err
		}
		inners = append(inners, inner)
	}
	return inners, nil
}

// dndUpdated renders the three DND topics as dnd_updated with the documented
// dnd_status object. dnd_updated_user — the name Slack uses toward everyone
// but the subject — is a recorded gap; see the topic table.
func dndUpdated(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "user_id")
	if err != nil {
		return nil, err
	}
	dndEnabled, ok := delivered.Bool("dnd_enabled")
	if !ok {
		return nil, fmt.Errorf("%w: %s payload has no dnd_enabled", ErrSlackEventIncomplete, delivered.Type)
	}
	snoozeEnabled, ok := delivered.Bool("snooze_enabled")
	if !ok {
		return nil, fmt.Errorf("%w: %s payload has no snooze_enabled", ErrSlackEventIncomplete, delivered.Type)
	}
	status := map[string]json.RawMessage{
		"dnd_enabled":    mustEncodeBool(dndEnabled),
		"snooze_enabled": mustEncodeBool(snoozeEnabled),
	}
	for payloadField, statusField := range map[string]string{
		"next_dnd_start_ts": "next_dnd_start_ts",
		"next_dnd_end_ts":   "next_dnd_end_ts",
		"snooze_endtime":    "snooze_endtime",
	} {
		if value, ok := delivered.Int(payloadField); ok {
			status[statusField] = json.RawMessage(strconv.FormatInt(value, 10))
		}
	}
	encoded, err := encodeObject(status)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayloadFieldInvalid, err)
	}
	inner, err := newInner("dnd_updated", delivered,
		String("user", values["user_id"]),
		JSON("dnd_status", encoded),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// presenceChange renders the documented RTM presence frame. The topic table
// restricts it to SurfaceRTM; the builder needs no surface logic of its own.
func presenceChange(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "user_id", "presence")
	if err != nil {
		return nil, err
	}
	inner, err := newInner("presence_change", delivered,
		String("user", values["user_id"]),
		String("presence", values["presence"]),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

func teamRename(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "name")
	if err != nil {
		return nil, err
	}
	inner, err := newInner("team_rename", delivered, String("name", values["name"]))
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// filePublic renders file_public the way Slack's file events travel:
// identifiers only, hydrated by the consumer through files.info.
func filePublic(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "file_id", "user_id")
	if err != nil {
		return nil, err
	}
	file, err := encodeObject(map[string]json.RawMessage{"id": mustEncodeString(values["file_id"])})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayloadFieldInvalid, err)
	}
	inner, err := newInner("file_public", delivered,
		String("file_id", values["file_id"]),
		String("user_id", values["user_id"]),
		JSON("file", file),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}
