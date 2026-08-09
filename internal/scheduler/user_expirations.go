package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// UserExpirationSource is the durable compare-and-set queue behind guest
// account expiry. Like the status queue it has no lease: two workers may read
// the same due account, and ExpireUserAccount lets exactly one deactivate it.
type UserExpirationSource interface {
	DueUserExpirations(context.Context, domain.WorkspaceID, time.Time, int) ([]domain.User, error)
	ExpireUserAccount(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) (bool, error)
	GetUserExpiration(context.Context, domain.WorkspaceID, domain.UserID) (time.Time, error)
}

// UserExpirationWorker deactivates accounts whose expiration has arrived.
//
// Access was already refused: LookupToken and LookupSession both exclude a
// credential whose owner has lapsed, in both storage profiles, so a guest past
// their expiration cannot sign in or call the API. What never happened is the
// account catching up with that refusal. It stayed undeleted and its
// membership stayed active, so every other member went on seeing a full
// participant: listed in the directory, available to direct message and
// mention, addable to channels, and counted as occupying a seat — for ever,
// for an account nobody could sign into.
//
// The worker makes the account agree with the enforcement that already exists.
// Deactivation is the fact the rest of the product understands — a deleted user
// is off the member list and holds no live credential — so producing it here
// means the surfaces need to learn nothing about deadlines.
type UserExpirationWorker struct {
	Source UserExpirationSource
	Limit  int
}

func NewUserExpirationWorker(source UserExpirationSource, limit int) (UserExpirationWorker, error) {
	if source == nil || limit <= 0 {
		return UserExpirationWorker{}, errors.New("user expiration worker requires a source and positive limit")
	}
	return UserExpirationWorker{Source: source, Limit: limit}, nil
}

func (w UserExpirationWorker) RunOnce(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	return w.RunOnceAt(ctx, workspaceID, time.Now().UTC())
}

func (w UserExpirationWorker) RunOnceAt(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time) (int, error) {
	now = now.UTC().Truncate(time.Second)
	users, err := w.Source.DueUserExpirations(ctx, workspaceID, now, w.Limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	var failures error
	for _, user := range users {
		if err := ctx.Err(); err != nil {
			return expired, errors.Join(failures, err)
		}
		// The instant is re-read rather than carried on the user, because it
		// is what the claim is made against and the user row does not hold it.
		expiration, err := w.Source.GetUserExpiration(ctx, user.WorkspaceID, user.ID)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		event, err := userExpiredEvent(user, now)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		changed, err := w.Source.ExpireUserAccount(ctx, user.WorkspaceID, user.ID, expiration, event)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if changed {
			expired++
		}
	}
	return expired, failures
}

func userExpiredEvent(user domain.User, now time.Time) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	// The snapshot is the state the expiry produces: the account deactivated.
	// The topic is user.removed, the one RemoveUser already emits, because the
	// fact is the same one and it already translates to Slack's user_change. A
	// second topic would hand apps an unfamiliar event for something they
	// already handle. The reason is what separates an account that lapsed from
	// one an administrator removed by hand.
	deactivated := user
	deactivated.Deleted = true
	payload, err := events.UserChangePayload("user.removed", deactivated, true, false, now,
		events.String("reason", "expired"))
	if err != nil {
		return events.Event{}, err
	}
	return events.New(id, user.WorkspaceID, user.ID, payload, now)
}
