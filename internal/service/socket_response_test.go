package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Official SDKs attach {} to plain acknowledgements. For an event envelope it
// is not a response, and journalling it wrote one row per acknowledged event
// that no response processor had anything to do with. A real response is
// still journalled.
func TestAnEmptyEventResponseIsAPlainAcknowledgement(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	messages := Messages{Store: repository}
	if err := messages.HandleSocketModeResponse(ctx, "A1", "event-1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetSocketModeResponse(ctx, "A1", "event-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an empty event response was journalled: err=%v", err)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", "event-2", []byte(`{"text":"handled"}`)); err != nil {
		t.Fatal(err)
	}
	if stored, err := repository.GetSocketModeResponse(ctx, "A1", "event-2"); err != nil || stored.Payload != `{"text":"handled"}` {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}
