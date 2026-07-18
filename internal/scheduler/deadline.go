package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

type DeadlinePublisher interface {
	SetWakeDeadline(uint64, time.Time) error
}

func PublishWakeDeadline(ctx context.Context, source Source, publisher DeadlinePublisher, workspace domain.WorkspaceID, fence uint64) error {
	if source == nil || publisher == nil || workspace == "" {
		return errors.New("scheduled wake deadline requires source, publisher, and workspace")
	}
	deadline, err := source.EarliestScheduledMessage(ctx, workspace)
	if err != nil {
		return err
	}
	return publisher.SetWakeDeadline(fence, deadline)
}
