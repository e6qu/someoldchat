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
