package service

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A view event names the revision the store now holds, which is what the web
// client compares with the modal it shows to decide whether to reload.
func requireViewEventRevision(t *testing.T, s *memory.Store, topic string, view domain.View) {
	t.Helper()
	records, err := s.ListEventsAfter(context.Background(), view.WorkspaceID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetView(context.Background(), view.WorkspaceID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.FormatInt(stored.UpdatedAt.UnixNano(), 10)
	for index := len(records) - 1; index >= 0; index-- {
		if records[index].Event.Topic != topic {
			continue
		}
		var payload struct {
			ViewID   string `json:"view_id"`
			Revision string `json:"revision"`
		}
		if err := json.Unmarshal([]byte(records[index].Event.Payload), &payload); err != nil || payload.ViewID != string(view.ID) {
			continue
		}
		if payload.Revision != want {
			t.Fatalf("%s revision=%q, store holds %q", topic, payload.Revision, want)
		}
		return
	}
	t.Fatalf("no %s event for %s", topic, view.ID)
}
