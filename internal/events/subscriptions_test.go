package events

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestFilterSubscribedSlackEventBodiesUsesManifestEventAndConversationKind(t *testing.T) {
	bodies := [][]byte{[]byte(`{"type":"event_callback","event":{"type":"message","channel":"C1","event_ts":"1700000000.000000"}}`)}
	resolve := func(_ context.Context, id domain.ConversationID) (domain.Conversation, error) {
		return domain.Conversation{ID: id, WorkspaceID: "T1", IsPrivate: true}, nil
	}
	filtered, err := FilterSubscribedSlackEventBodies(context.Background(), bodies, []string{"message.channels"}, nil, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 0 {
		t.Fatal("a private-channel message matched message.channels")
	}
	filtered, err = FilterSubscribedSlackEventBodies(context.Background(), bodies, []string{"message.groups"}, nil, resolve)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("private subscription bodies=%d err=%v", len(filtered), err)
	}

	bodies = [][]byte{[]byte(`{"type":"event_callback","event":{"type":"reaction_added","event_ts":"1700000000.000000"}}`)}
	filtered, err = FilterSubscribedSlackEventBodies(context.Background(), bodies, nil, []string{"reaction_added"}, resolve)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("user event subscription bodies=%d err=%v", len(filtered), err)
	}
}
