package events

import (
	"context"
	"encoding/json"
	"fmt"
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
// A payload written before the typed payload contract — a bare identifier — is
// still encoded, because refusing it here would silently drop committed events
// that this format has always carried. Deliverable is where that record is
// refused for a consumer that must parse it.
func (e Event) MarshalJSON() ([]byte, error) {
	if err := e.refuseBroadcast(); err != nil {
		return nil, err
	}
	// wire drops the method set, so encoding the copy cannot recurse.
	type wire Event
	return json.Marshal(wire(e))
}

func (e Event) refuseBroadcast() error {
	if InternalTopic(e.Topic) {
		return fmt.Errorf("%w: topic %s", ErrPayloadInternal, e.Topic)
	}
	if RecipientScoped(e.Topic) {
		return fmt.Errorf("%w: topic %s", ErrPayloadRecipientScoped, e.Topic)
	}
	delivered, err := decodeDelivered(e.Payload, "")
	if err != nil {
		return nil
	}
	// Equality with Topic is the whole check: Topic has already been tested
	// against both refusal sets above, so a payload that describes itself as the
	// same event is covered by those tests, and one that does not is a record
	// whose refusal decision was made about a different event.
	if delivered.Type != e.Topic {
		return fmt.Errorf("%w: payload %s %q does not match record topic %q", ErrPayloadMalformed, payloadTypeField, delivered.Type, e.Topic)
	}
	return nil
}
