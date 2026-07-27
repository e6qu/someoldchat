package lease

import (
	"context"
	"errors"
	"testing"
	"time"
)

var (
	errLeaseLost   = errors.New("lease lost")
	errOperation   = errors.New("operation failed")
	errRenewCalled = errors.New("renewal should not have been called")
)

// The defect this package exists to remove: internal/scheduler reported a lost
// lease only when the operation had succeeded, so the one combination that
// matters most — the work failed *and* another owner was running it — was
// reported as an ordinary failure, and the caller released the item for yet
// another replica believing only the post had failed.
func TestWhileReportsALostLeaseEvenWhenTheOperationAlsoFailed(t *testing.T) {
	renewing := make(chan struct{})
	release := make(chan struct{})
	err := While(context.Background(), 3*time.Millisecond,
		func(context.Context) error {
			close(renewing)
			<-release
			return errLeaseLost
		},
		func(operationContext context.Context) error {
			<-renewing
			close(release)
			// The renewal failure cancels this context, which is how the work
			// learns to stop acting for an owner it no longer is.
			<-operationContext.Done()
			return errOperation
		},
	)
	if !errors.Is(err, errLeaseLost) {
		t.Fatalf("err=%v, want the lost lease reported", err)
	}
	if !errors.Is(err, errOperation) {
		t.Fatalf("err=%v, want the operation's own failure reported alongside it", err)
	}
}

func TestWhileReportsALostLeaseWhenTheOperationSucceeded(t *testing.T) {
	renewing := make(chan struct{})
	release := make(chan struct{})
	err := While(context.Background(), 3*time.Millisecond,
		func(context.Context) error {
			close(renewing)
			<-release
			return errLeaseLost
		},
		func(operationContext context.Context) error {
			<-renewing
			close(release)
			<-operationContext.Done()
			return nil
		},
	)
	if !errors.Is(err, errLeaseLost) {
		t.Fatalf("err=%v, want the lost lease reported", err)
	}
}

// A renewal that failed only because this helper cancelled it at the end of the
// work is not a lost lease. Reporting it would make every successful operation
// look like a duplicate-execution hazard.
func TestWhileDoesNotReportARenewalCancelledByCompletion(t *testing.T) {
	err := While(context.Background(), time.Hour,
		func(renewContext context.Context) error { return renewContext.Err() },
		func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("err=%v, want a clean completion", err)
	}
}

// A short-lived operation must not be charged a renewal at all, and the
// operation's own error must survive untouched.
func TestWhileReturnsTheOperationErrorWhenTheLeaseHolds(t *testing.T) {
	err := While(context.Background(), time.Hour,
		func(context.Context) error { return errRenewCalled },
		func(context.Context) error { return errOperation },
	)
	if !errors.Is(err, errOperation) || errors.Is(err, errRenewCalled) {
		t.Fatalf("err=%v, want only the operation's error", err)
	}
}

func TestRenewIntervalLeavesRoomForLostRenewalsAndNeverSpins(t *testing.T) {
	if got := RenewInterval(30 * time.Second); got != 10*time.Second {
		t.Fatalf("interval=%s, want a third of the lease", got)
	}
	if got := RenewInterval(time.Nanosecond); got != MinimumRenewInterval {
		t.Fatalf("interval=%s, want the floor %s", got, MinimumRenewInterval)
	}
}

func TestWhileRejectsIncompleteArguments(t *testing.T) {
	if err := While(context.Background(), time.Second, nil, func(context.Context) error { return nil }); err == nil {
		t.Fatal("a missing renewal was accepted")
	}
	if err := While(context.Background(), time.Second, func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("a missing operation was accepted")
	}
}
