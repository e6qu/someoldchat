package grpc

import (
	"context"
	"reflect"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// capturingBlockActions records the block action a server was handed.
type capturingBlockActions struct {
	chatapi.Service
	received chan domain.AppBlockAction
}

func (c capturingBlockActions) DispatchBlockAction(_ context.Context, _ domain.WorkspaceID, _ domain.UserID, action domain.AppBlockAction, _ string) error {
	c.received <- action
	return nil
}

// The text and token of an external-select option the member chose cross the
// seam with the action, so a distributed composition reports the option's
// text to the app exactly as the monolith does.
func TestChosenExternalOptionsCrossTheSeam(t *testing.T) {
	target := seededStore(t)
	capture := capturingBlockActions{Service: service.Messages{Store: target}, received: make(chan domain.AppBlockAction, 1)}
	remote, _ := serve(t, capture, target, Observer{})
	sent := domain.AppBlockAction{
		MessageID: "M1", BlockID: "b", ActionID: "a", Type: "multi_external_select", Value: `["one","two"]`,
		ChosenOptions: []domain.AppChosenOption{{Value: "one", Text: "Option One", Token: "t1"}, {Value: "two", Text: "Option Two", Token: "t2"}},
	}
	if err := remote.DispatchBlockAction(context.Background(), "T1", "U1", sent, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	if received := <-capture.received; !reflect.DeepEqual(received, sent) {
		t.Fatalf("received %+v, want %+v", received, sent)
	}
}
