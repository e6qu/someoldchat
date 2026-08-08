package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestFilterSubscribedSlackEventBodiesUsesManifestEventAndConversationKind(t *testing.T) {
	bodies := [][]byte{[]byte(`{"type":"event_callback","event":{"type":"message","channel":"C1","event_ts":"1700000000.000000"}}`)}
	resolve := func(_ context.Context, id domain.ConversationID) (domain.Conversation, error) {
		return domain.Conversation{ID: id, WorkspaceID: "T1", Kind: domain.ConversationTypePrivate}, nil
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

func TestFilterSubscribedSlackEventBodiesKeepsOneMatchingAuthorizationPerspective(t *testing.T) {
	body := []byte(`{"type":"event_callback","event":{"type":"reaction_added","event_ts":"1700000000.000000"},"authorizations":[{"team_id":"T1","user_id":"UB","is_bot":true,"is_enterprise_install":false},{"team_id":"T1","user_id":"U1","is_bot":false,"is_enterprise_install":false}]}`)

	filtered, err := FilterSubscribedSlackEventBodies(context.Background(), [][]byte{body}, nil, []string{"reaction_added"}, nil)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("user subscription bodies=%d err=%v", len(filtered), err)
	}
	if !strings.Contains(string(filtered[0]), `"authorizations":[{"enterprise_id":"","team_id":"T1","user_id":"U1","is_bot":false`) ||
		strings.Contains(string(filtered[0]), `"user_id":"UB"`) {
		t.Fatalf("user subscription retained the wrong perspective: %s", filtered[0])
	}

	filtered, err = FilterSubscribedSlackEventBodies(context.Background(), [][]byte{body}, []string{"reaction_added"}, nil, nil)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("bot subscription bodies=%d err=%v", len(filtered), err)
	}
	if !strings.Contains(string(filtered[0]), `"authorizations":[{"enterprise_id":"","team_id":"T1","user_id":"UB","is_bot":true`) ||
		strings.Contains(string(filtered[0]), `"user_id":"U1"`) {
		t.Fatalf("bot subscription retained the wrong perspective: %s", filtered[0])
	}
}

func TestFunctionExecutedDoesNotRequireManifestSubscription(t *testing.T) {
	body := []byte(`{"type":"event_callback","event":{"type":"function_executed","function":{"id":"Fn1"},"inputs":{},"function_execution_id":"Fx1","workflow_execution_id":"Wx1","event_ts":"1700000000.000000"},"authorizations":[{"team_id":"T1","user_id":"UB","is_bot":true,"is_enterprise_install":false},{"team_id":"T1","user_id":"U1","is_bot":false,"is_enterprise_install":false}]}`)
	filtered, err := FilterSubscribedSlackEventBodies(context.Background(), [][]byte{body}, nil, nil, nil)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("automatic function event bodies=%d err=%v", len(filtered), err)
	}
	var envelope struct {
		Authorizations []struct {
			UserID string `json:"user_id"`
		} `json:"authorizations"`
	}
	if err := json.Unmarshal(filtered[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Authorizations) != 1 || envelope.Authorizations[0].UserID != "UB" {
		t.Fatalf("automatic event must retain one installation perspective: %s", filtered[0])
	}
}
