package scheduler

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// SlackbotResponder answers members' messages as Slackbot: the chat module
// seam, so the worker runs the same path in local and distributed
// composition.
type SlackbotResponder interface {
	DispatchSlackbotResponses(context.Context, domain.WorkspaceID, int) (int, error)
}

// SlackbotResponseWorker tails the event journal for messages Slackbot
// answers: a workspace's custom responses, and direct messages to Slackbot.
// The cursor lives in the store behind the responder.
type SlackbotResponseWorker struct {
	Responder SlackbotResponder
	Limit     int
}

func NewSlackbotResponseWorker(responder SlackbotResponder, limit int) (SlackbotResponseWorker, error) {
	if responder == nil || limit <= 0 {
		return SlackbotResponseWorker{}, errors.New("Slackbot response worker requires a responder and positive limit")
	}
	return SlackbotResponseWorker{Responder: responder, Limit: limit}, nil
}

func (w SlackbotResponseWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.Responder.DispatchSlackbotResponses(ctx, workspaceID, w.Limit)
}
