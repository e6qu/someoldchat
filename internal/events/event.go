package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

const FileBlobDeleteTopic = "file.blob_delete"
const UserPhotoBlobDeleteTopic = "user.photo_blob_delete"
const EphemeralMessageTopic = "message.ephemeral"

type Source interface {
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]Record, error)
}

// Event is a durable journal record. Build one with New: Payload holds the
// encoded storage and wire representation of a typed events.Payload, and the
// typed constructor is the only supported way to produce it. Consumers must
// read it back through Deliverable rather than parsing the string themselves.
type Event struct {
	ID          domain.EventID
	WorkspaceID domain.WorkspaceID
	ActorID     domain.UserID
	Topic       string
	Payload     string
	CreatedAt   time.Time
}

type Record struct {
	Sequence uint64
	Event    Event
}

// MarshalJSON refuses to render a record that no audience may receive.
//
// Broadcastable exists so a transport that has no recipient to filter on cannot
// forget the recipient filter, but a transport that never decodes the payload
// cannot forget a call it does not make: shipping the durable record itself as
// JSON forwards Topic and the raw Payload string verbatim, which is how the
// full text of a message addressed to exactly one user reached a third-party
// webhook. Serializing an Event is that shipping step, so the refusal lives
// here: every present and future transport that encodes a durable record to
// JSON fails closed with a sentinel its caller already classifies as permanent,
// and none of them has to remember a check.
//
// The recipient-scoped transports are unaffected because they do not encode the
// record: the browser event stream and the RTM socket both write
// Delivered.Encode() for one authenticated recipient.
//
// The refusal is Broadcastable itself rather than a second implementation of
// the same rule. It used to be a copy, and the copy is how the two halves came
// to disagree in both directions at once: this one refused a payload carrying a
// Slack event type — permanently dropping, under -delivery-format record, every
// event the same binary delivered under -delivery-format slack-events — while
// admitting a bare identifier that every other consumer refuses, so an opaque
// internal ID was POSTed to a third-party URL as an event body. Delegating
// makes the divergence unrepresentable rather than merely tested for.
func (e Event) MarshalJSON() ([]byte, error) {
	if _, err := Broadcastable(e); err != nil {
		return nil, err
	}
	// wire drops the method set, so encoding the copy cannot recurse.
	type wire Event
	return json.Marshal(wire(e))
}
