package blob

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

type ReferenceSource interface {
	WalkBlobReferences(context.Context, domain.WorkspaceID, func(string) error) error
}

type EventSink interface {
	AppendEvent(context.Context, events.Event) error
}

type Reconciliation struct {
	Objects       int
	References    int
	OrphanKeys    []string
	MissingKeys   []string
	DuplicateKeys int
}

type Reconciler struct {
	References ReferenceSource
	Objects    WalkStore
	Events     EventSink
	MaxResults int
}

func NewReconciler(references ReferenceSource, objects WalkStore, events EventSink, maxResults int) (Reconciler, error) {
	if references == nil || objects == nil || events == nil || maxResults <= 0 {
		return Reconciler{}, errors.New("blob reconciliation requires reference source, list store, event sink, and positive result limit")
	}
	return Reconciler{References: references, Objects: objects, Events: events, MaxResults: maxResults}, nil
}

func (r Reconciler) Audit(ctx context.Context, workspace domain.WorkspaceID) (Reconciliation, error) {
	if workspace == "" {
		return Reconciliation{}, errors.New("blob reconciliation requires a workspace")
	}
	references := make(map[string]struct{})
	result := Reconciliation{}
	if err := r.References.WalkBlobReferences(ctx, workspace, func(key string) error {
		if key == "" {
			return errors.New("blob reference source returned an empty key")
		}
		if _, exists := references[key]; exists {
			result.DuplicateKeys++
			return nil
		}
		references[key] = struct{}{}
		result.References++
		return nil
	}); err != nil {
		return Reconciliation{}, err
	}
	prefix := string(workspace) + "/"
	if err := r.Objects.Walk(ctx, prefix, func(object Object) error {
		result.Objects++
		if _, exists := references[object.Key]; exists {
			delete(references, object.Key)
			return nil
		}
		if len(result.OrphanKeys) >= r.MaxResults {
			return fmt.Errorf("blob reconciliation found more than %d orphan objects", r.MaxResults)
		}
		result.OrphanKeys = append(result.OrphanKeys, object.Key)
		return nil
	}); err != nil {
		return Reconciliation{}, err
	}
	for key := range references {
		if len(result.MissingKeys) >= r.MaxResults {
			return Reconciliation{}, fmt.Errorf("blob reconciliation found more than %d missing objects", r.MaxResults)
		}
		result.MissingKeys = append(result.MissingKeys, key)
	}
	sort.Strings(result.OrphanKeys)
	sort.Strings(result.MissingKeys)
	return result, nil
}

func (r Reconciler) EnqueueOrphans(ctx context.Context, workspace domain.WorkspaceID, result Reconciliation) (int, error) {
	if workspace == "" {
		return 0, errors.New("blob cleanup requires a workspace")
	}
	for _, key := range result.OrphanKeys {
		if key == "" {
			return 0, errors.New("blob cleanup cannot enqueue an empty key")
		}
	}
	for index, key := range result.OrphanKeys {
		id, err := domain.NewEventID()
		if err != nil {
			return index, err
		}
		if err := r.Events.AppendEvent(ctx, events.Event{ID: id, WorkspaceID: workspace, Topic: events.FileBlobDeleteTopic, Payload: key, CreatedAt: time.Now().UTC()}); err != nil {
			return index, err
		}
	}
	return len(result.OrphanKeys), nil
}
