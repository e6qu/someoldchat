// Package lease runs work while a durable lease over that work is held.
//
// It exists because the same twenty-line goroutine — tick, renew, cancel the
// work on a renewal failure, then decide which of the two errors to report — was
// copied into five packages, and the copies drifted. internal/scheduler's copy
// reported a lost lease only when the operation itself had succeeded, so a
// scheduled message that both lost its lease and failed to post was released for
// another replica with no record that two owners had been running it. The
// duplication was recorded as a low-severity tidiness item; it was a correctness
// defect, and one shared implementation is what removes the class.
package lease

import (
	"context"
	"errors"
	"time"
)

// MinimumRenewInterval keeps a pathologically short lease from spinning the
// renewal goroutine.
const MinimumRenewInterval = time.Millisecond

// RenewInterval is the tick at which a lease of the given duration is extended.
// A third leaves room for two lost renewals before the lease actually lapses.
func RenewInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < MinimumRenewInterval {
		return MinimumRenewInterval
	}
	return interval
}

// While runs operation while renewing the lease, and reports both outcomes.
//
// The operation receives a context that is cancelled as soon as a renewal fails,
// so work whose lease has lapsed stops rather than continuing to act on behalf of
// an owner it no longer is.
//
// A renewal failure is never dropped in favour of the operation's own error.
// Losing a lease means another owner is running the same work, which is the
// condition behind duplicate delivery, duplicate posting, and duplicate deletion;
// it is exactly the error a caller must see even when the operation also failed.
// A renewal that failed only because this function cancelled it is not a lost
// lease and is not reported.
func While(ctx context.Context, lease time.Duration, renew func(context.Context) error, operation func(context.Context) error) error {
	if ctx == nil {
		return errors.New("lease requires a context")
	}
	if renew == nil || operation == nil {
		return errors.New("lease requires a renewal and an operation")
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErrors := make(chan error, 1)
	done := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(RenewInterval(lease))
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := renew(operationContext); err != nil {
					cancel()
					renewErrors <- err
					return
				}
			}
		}
	}()
	operationErr := operation(operationContext)
	// The renewal goroutine is stopped before the outcome is read, so a renewal
	// that was still in flight cannot be missed by the select below.
	cancel()
	close(done)
	<-renewDone
	select {
	case renewErr := <-renewErrors:
		if !errors.Is(renewErr, context.Canceled) {
			return errors.Join(renewErr, operationErr)
		}
	default:
	}
	return operationErr
}
