package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Payload is the body of a durable event record.
//
// The type exists to make one whole class of defect impossible rather than
// detectable: every delivery path in this system (Slack HTTP events, Socket
// Mode, RTM and the browser event stream) requires a self-describing JSON
// object, and producers previously stored bare identifiers such as
// string(message.ID). Payload has no exported field and no conversion from a
// string, so an identifier cannot be used as a payload — the compiler rejects
// it — and its zero value is invalid, so a caller that forgets to build one is
// rejected by New instead of writing an undeliverable record.
//
// Two shapes exist, and the shape is part of the type rather than something a
// consumer sniffs out of the bytes:
//
//   - NewPayload builds a deliverable payload. It always encodes to a JSON
//     object carrying a "type" discriminator (the record topic) and an
//     "event_ts", plus the typed identifiers the topic needs.
//   - BlobKey builds an internal blob-cleanup work record whose encoded form is
//     the object-storage key itself. Deliverable refuses these, so an internal
//     storage key can never reach an app, a webhook or a browser.
//
// Scope boundary: this type guarantees a well-formed, self-describing payload.
// It deliberately does not claim that a topic's payload is byte-identical to
// the event JSON real Slack emits for the corresponding event type; that is a
// compatibility question against the pinned specifications in specs/ and is
// decided separately.
type Payload struct {
	topic  string
	fields []Field
	key    string
	shape  payloadShape
}

type payloadShape uint8

const (
	payloadInvalid payloadShape = iota
	payloadObject
	payloadBlobKey
)

// Field is one named value inside a deliverable payload. Build fields with
// String, Strings, Int, Bool or JSON; the zero value is invalid.
type Field struct {
	name  string
	value json.RawMessage
	err   error
}

const (
	payloadTypeField    = "type"
	payloadEventTSField = "event_ts"
)

var (
	// ErrPayloadRequired reports a Payload that was never built.
	ErrPayloadRequired = errors.New("event payload is required")
	// ErrPayloadFieldInvalid reports a payload field that cannot be encoded.
	ErrPayloadFieldInvalid = errors.New("event payload field is invalid")
	// ErrPayloadInternal reports a durable record that exists only for an
	// internal worker and must never be handed to an external consumer.
	ErrPayloadInternal = errors.New("event payload is internal and is not deliverable")
	// ErrPayloadRecipientScoped reports a record addressed to a single user
	// that a consumer without a recipient asked for.
	ErrPayloadRecipientScoped = errors.New("event payload is addressed to a single recipient")
	// ErrPayloadMalformed reports a stored payload that is not a
	// self-describing JSON object, which is what every producer written
	// against Payload emits.
	ErrPayloadMalformed = errors.New("event payload is not a self-describing JSON object")
	// ErrEventIncomplete reports an event record that cannot be stored.
	ErrEventIncomplete = errors.New("event record is incomplete")
)

// NewPayload builds the deliverable payload for a topic. The topic doubles as
// the payload's "type" discriminator, so a consumer never has to infer what it
// received.
func NewPayload(topic string, fields ...Field) Payload {
	if strings.TrimSpace(topic) == "" || !utf8.ValidString(topic) || InternalTopic(topic) {
		return Payload{}
	}
	// The fields are copied rather than aliased: a caller that spreads a slice it
	// goes on to reuse would otherwise mutate a payload it has already built,
	// which is exactly the misuse this type exists to make unrepresentable.
	return Payload{topic: topic, fields: append([]Field(nil), fields...), shape: payloadObject}
}

// BlobKey builds an internal blob-cleanup record for an object-storage key.
// Only the blob-cleanup topics accept this shape.
func BlobKey(topic, key string) Payload {
	if !InternalTopic(topic) || strings.TrimSpace(key) == "" {
		return Payload{}
	}
	return Payload{topic: topic, key: key, shape: payloadBlobKey}
}

// Topic reports the durable topic the payload was built for.
func (p Payload) Topic() string { return p.topic }

// String builds a string-valued field.
func String(name, value string) Field {
	return jsonField(name, value)
}

// Strings builds a field holding a list of strings. A nil list encodes as an
// empty list rather than null so consumers never have to special-case it.
func Strings(name string, values []string) Field {
	if values == nil {
		values = []string{}
	}
	return jsonField(name, values)
}

// Int builds an integer-valued field.
func Int(name string, value int64) Field {
	return jsonField(name, value)
}

// Bool builds a boolean-valued field.
func Bool(name string, value bool) Field {
	return jsonField(name, value)
}

// JSON builds a field from an already-encoded JSON document, such as a
// normalized block kit array. An empty document is omitted from the payload.
func JSON(name, encoded string) Field {
	if err := checkFieldName(name); err != nil {
		return Field{err: err}
	}
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return Field{}
	}
	if !json.Valid([]byte(trimmed)) {
		return Field{err: fmt.Errorf("%w: %s is not valid JSON", ErrPayloadFieldInvalid, name)}
	}
	return Field{name: name, value: json.RawMessage(trimmed)}
}

func jsonField(name string, value any) Field {
	if err := checkFieldName(name); err != nil {
		return Field{err: err}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Field{err: fmt.Errorf("%w: %s: %v", ErrPayloadFieldInvalid, name, err)}
	}
	return Field{name: name, value: encoded}
}

func checkFieldName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: field name is required", ErrPayloadFieldInvalid)
	}
	if name == payloadTypeField || name == payloadEventTSField {
		return fmt.Errorf("%w: %s is reserved", ErrPayloadFieldInvalid, name)
	}
	return nil
}

// New builds a durable event record. The returned Event carries the encoded
// payload as a string, which is the storage and wire representation, while
// construction stays typed: there is no path from an identifier to an Event.
func New(id domain.EventID, workspaceID domain.WorkspaceID, actorID domain.UserID, payload Payload, createdAt time.Time) (Event, error) {
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(workspaceID)) == "" {
		return Event{}, fmt.Errorf("%w: id and workspace are required", ErrEventIncomplete)
	}
	if createdAt.IsZero() {
		return Event{}, fmt.Errorf("%w: creation time is required", ErrEventIncomplete)
	}
	encoded, err := payload.encode(createdAt)
	if err != nil {
		return Event{}, err
	}
	return Event{ID: id, WorkspaceID: workspaceID, ActorID: actorID, Topic: payload.topic, Payload: encoded, CreatedAt: createdAt.UTC()}, nil
}

func (p Payload) encode(createdAt time.Time) (string, error) {
	switch p.shape {
	case payloadBlobKey:
		return p.key, nil
	case payloadObject:
	default:
		return "", ErrPayloadRequired
	}
	object := make(map[string]json.RawMessage, len(p.fields)+2)
	object[payloadTypeField] = mustEncodeString(p.topic)
	object[payloadEventTSField] = mustEncodeString(string(domain.NewMessageTimestamp(createdAt.UTC())))
	for _, field := range p.fields {
		if field.err != nil {
			return "", field.err
		}
		if field.name == "" {
			continue
		}
		if _, exists := object[field.name]; exists {
			return "", fmt.Errorf("%w: %s is set twice", ErrPayloadFieldInvalid, field.name)
		}
		object[field.name] = field.value
	}
	encoded, err := encodeObject(object)
	if err != nil {
		return "", err
	}
	return encoded, nil
}

// encodeObject renders a payload object with its keys in a stable order, so an
// event's stored bytes depend only on its content.
func encodeObject(object map[string]json.RawMessage) (string, error) {
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	builder.WriteByte('{')
	for index, name := range names {
		if index > 0 {
			builder.WriteByte(',')
		}
		encodedName, err := json.Marshal(name)
		if err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrPayloadFieldInvalid, name, err)
		}
		builder.Write(encodedName)
		builder.WriteByte(':')
		builder.Write(object[name])
	}
	builder.WriteByte('}')
	return builder.String(), nil
}

// mustEncodeString encodes a string that cannot fail to encode: json.Marshal of
// a string never returns an error, and NewPayload rejects a topic that is not
// valid UTF-8, so the repaired form json.Marshal would otherwise substitute can
// never differ from Event.Topic. Deliverable relies on that equality.
func mustEncodeString(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

// Delivered is an event payload that may be handed to an external consumer. It
// is the only way to read a payload for delivery, so every transport applies
// the same rule: internal records are refused, and a payload that is not a
// self-describing object is refused instead of being forwarded.
type Delivered struct {
	Type   string
	Object map[string]json.RawMessage
}

// audience names what a consumer of a decoded payload can address.
type audience uint8

const (
	// oneRecipient delivers to a single authenticated user and can therefore
	// filter a record addressed to one recipient.
	oneRecipient audience = iota
	// everyone has no recipient to filter on: an event webhook, an app
	// connection, or a durable record shipped verbatim as JSON.
	everyone
)

// refuse reports why a record that names this topic must be withheld from the
// audience, or nil when it may be delivered.
//
// It takes a topic rather than an Event because a record carries two names for
// the same thing — the Topic column, which decides routing, the recipient
// filter and the event name a browser receives, and the "type" the payload
// describes itself as, which is the body every transport actually ships — and
// either can be the one a consumer acts on. One predicate, applied to each name
// from one shared site, is the whole rule:
//
//   - testing Topic alone is how a payload that self-describes as
//     message.ephemeral was broadcast to a whole workspace and to every app
//     under topic message.created;
//   - testing the two for *equality* instead is how every official Socket Mode
//     and real-time client broke, because a Slack-shaped payload legitimately
//     names a Slack event type that is not the record's topic.
//
// Refusing a record that is restricted by topic OR by payload accepts the
// compatible record and refuses the dangerous one, and it does so on every
// path rather than on the one that remembered to check.
func (a audience) refuse(topic string) error {
	if InternalTopic(topic) {
		return fmt.Errorf("%w: topic %s", ErrPayloadInternal, topic)
	}
	if a == everyone && RecipientScoped(topic) {
		return fmt.Errorf("%w: topic %s", ErrPayloadRecipientScoped, topic)
	}
	return nil
}

// AddressedToOneRecipient reports whether a decoded record is addressed to
// exactly one user, by its topic or by the type its payload describes itself
// as.
//
// A consumer that can scope delivery must ask this rather than asking about the
// topic alone. Topic and the payload's "type" are separate storage columns and
// separate wire fields, so a record rebuilt from independent parts can carry an
// ordinary topic over a payload that is addressed to one user; a filter that
// reads only the topic then decides that record is addressed to nobody and
// writes it to every subscriber in the workspace. A consumer that cannot scope
// delivery does not need this: Broadcastable refuses the record outright.
func AddressedToOneRecipient(topic string, delivered Delivered) bool {
	return RecipientScoped(topic) || RecipientScoped(delivered.Type)
}

// Broadcastable decodes an event's stored payload for a consumer that delivers
// to an audience rather than to one user, such as an event webhook or an app
// connection. It refuses recipient-scoped records in addition to everything
// Deliverable refuses, so the recipient filter cannot be forgotten by a
// transport that has no recipient to filter on.
func Broadcastable(event Event) (Delivered, error) {
	return decodeDelivered(event.Payload, event.Topic, everyone)
}

// Deliverable decodes an event's stored payload for delivery to one
// authenticated recipient. It returns ErrPayloadInternal for internal worker
// records — whether the record says so in its topic or in its payload — and
// ErrPayloadMalformed for a payload that is not a JSON object with a non-empty
// "type", which is what a record written before the typed payload contract
// looks like.
//
// It does not refuse a recipient-scoped record: its caller has a recipient, and
// must filter with AddressedToOneRecipient rather than on the topic alone.
func Deliverable(event Event) (Delivered, error) {
	return decodeDelivered(event.Payload, event.Topic, oneRecipient)
}

// decodeDelivered decodes a stored payload and applies the audience's refusal
// to both names the record carries: the topic it is filed under and the type
// its payload describes itself as.
//
// This is the one site both halves of the rule go through. Broadcastable,
// Deliverable, SlackEventBodies and Event.MarshalJSON all funnel here, so a
// record cannot be refused by one and admitted by another, and a record rebuilt
// from independent parts — which is what the transport codec produces from
// separate proto fields, with no way to check that they agree — is judged by
// what it actually carries rather than by what it is filed as.
func decodeDelivered(payload, topic string, to audience) (Delivered, error) {
	if err := to.refuse(topic); err != nil {
		return Delivered{}, err
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || trimmed[0] != '{' {
		return Delivered{}, ErrPayloadMalformed
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		return Delivered{}, ErrPayloadMalformed
	}
	raw, exists := object[payloadTypeField]
	if !exists {
		return Delivered{}, fmt.Errorf("%w: %s is missing", ErrPayloadMalformed, payloadTypeField)
	}
	var kind string
	if err := json.Unmarshal(raw, &kind); err != nil || strings.TrimSpace(kind) == "" {
		return Delivered{}, fmt.Errorf("%w: %s must be a non-empty string", ErrPayloadMalformed, payloadTypeField)
	}
	// The payload's type is deliberately NOT required to equal the record's
	// topic. A record this system produces cannot disagree — NewPayload derives
	// the type from the topic — so the equality protects against nothing our
	// producers can do. What it does reject is a payload that already carries a
	// Slack event type: the transports accept such a payload as an already
	// translated event (see slackShaped in slack.go), and the qualification
	// fixture stores two of them for the topics this repository cannot translate
	// yet. Asserting the equality here broke every official Socket Mode and
	// real-time client while protecting nothing.
	//
	// What the equality was reaching for is the refusal below: a record rebuilt
	// from independent parts must not escape a rule by being filed under a topic
	// that does not describe it.
	if err := to.refuse(kind); err != nil {
		return Delivered{}, err
	}
	return Delivered{Type: kind, Object: object}, nil
}

// Field reports a string-valued payload field. The second result is false when
// the field is absent or is not a string.
func (d Delivered) Field(name string) (string, bool) {
	raw, exists := d.Object[name]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// Strings reports a list-of-strings payload field, as Strings wrote it. The
// second result is false when the field is absent or is not a JSON array of
// strings.
func (d Delivered) Strings(name string) ([]string, bool) {
	raw, exists := d.Object[name]
	if !exists {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

// Bool reports a boolean payload field, as Bool wrote it. The second result is
// false when the field is absent or is not a JSON boolean.
func (d Delivered) Bool(name string) (bool, bool) {
	raw, exists := d.Object[name]
	if !exists {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

// Int reports an integer payload field, as Int wrote it. The second result is
// false when the field is absent or is not a JSON integer.
func (d Delivered) Int(name string) (int64, bool) {
	raw, exists := d.Object[name]
	if !exists {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

// Encode renders the payload object back to JSON with stable key ordering.
func (d Delivered) Encode() (string, error) {
	return encodeObject(d.Object)
}

// UserChangePayload builds the durable payload for the user.* topics whose
// Slack events carry a user object: team_join, user_change and the
// user_profile_changed / user_status_changed companions. It lives here, next
// to the builders that consume it, so every producer — the service and the
// status schedulers — snapshots the identical shape.
//
// Every field is one the workspace directory already shows; the e-mail
// address is deliberately absent, because the journal is read by every
// member's event stream and users:read.email is a separate grant.
func UserChangePayload(topic string, user domain.User, deleted, statusChanged bool, at time.Time, extra ...Field) (Payload, error) {
	profile := map[string]any{
		"real_name":    user.RealName,
		"display_name": user.Profile.DisplayName,
		"status_text":  user.Profile.StatusText,
		"status_emoji": user.Profile.StatusEmoji,
	}
	if !user.Profile.StatusExpiration.IsZero() {
		profile["status_expiration"] = user.Profile.StatusExpiration.Unix()
	}
	for name, value := range map[string]string{
		"image_24": user.Profile.Image24, "image_32": user.Profile.Image32,
		"image_48": user.Profile.Image48, "image_72": user.Profile.Image72,
		"image_192": user.Profile.Image192, "image_512": user.Profile.Image512,
		"image_1024": user.Profile.Image1024,
	} {
		if value != "" {
			profile[name] = value
		}
	}
	encoded, err := json.Marshal(map[string]any{
		"id":        user.ID,
		"team_id":   user.WorkspaceID,
		"name":      user.Name,
		"real_name": user.RealName,
		"deleted":   deleted,
		"is_bot":    false,
		"updated":   at.Unix(),
		"profile":   profile,
	})
	if err != nil {
		return Payload{}, err
	}
	fields := []Field{
		String("user_id", string(user.ID)),
		JSON("user", string(encoded)),
	}
	if statusChanged {
		fields = append(fields, Bool("status_changed", true))
	}
	fields = append(fields, extra...)
	return NewPayload(topic, fields...), nil
}
