package events

import (
	"context"
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
