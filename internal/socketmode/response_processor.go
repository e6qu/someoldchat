package socketmode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

type ResponseQueue interface {
	ClaimSocketModeResponses(context.Context, domain.AppID, string, int, time.Duration) ([]domain.SocketModeResponse, error)
	RenewSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Duration) error
	AckSocketModeResponses(context.Context, string, []domain.SocketModeResponse) error
	ReleaseSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Time) error
}

type ResponseHandler func(context.Context, domain.SocketModeResponse) error

type ResponseDeliveryError struct {
	EnvelopeID string
	Err        error
}

func (e ResponseDeliveryError) Error() string {
	return fmt.Sprintf("deliver Socket Mode response %q: %v", e.EnvelopeID, e.Err)
}

func (e ResponseDeliveryError) Unwrap() error { return e.Err }

type ResponseProcessor struct {
	Queue      ResponseQueue
	AppID      domain.AppID
	Owner      string
	BatchSize  int
	Lease      time.Duration
	RetryDelay time.Duration
}

func (p ResponseProcessor) ProcessOnce(ctx context.Context, now time.Time, handle ResponseHandler) error {
	if p.Queue == nil {
		return errors.New("Socket Mode response processor requires a queue")
	}
	if p.AppID == "" {
		return errors.New("Socket Mode response processor requires an app ID")
	}
	if strings.TrimSpace(p.Owner) == "" {
		return errors.New("Socket Mode response processor requires an owner")
	}
	if p.BatchSize < 1 || p.BatchSize > 1000 {
		return errors.New("Socket Mode response processor batch size must be between 1 and 1000")
	}
	if p.Lease <= 0 || p.RetryDelay <= 0 || now.IsZero() {
		return errors.New("Socket Mode response processor timing is invalid")
	}
	if handle == nil {
		return errors.New("Socket Mode response processor requires a handler")
	}
	for processed := 0; processed < p.BatchSize; processed++ {
		values, err := p.Queue.ClaimSocketModeResponses(ctx, p.AppID, p.Owner, 1, p.Lease)
		if err != nil {
			return err
		}
		if len(values) > 1 {
			return errors.New("Socket Mode response queue returned more responses than requested")
		}
		if len(values) == 0 {
			return nil
		}
		value := values[0]
		if err := p.handleWithLease(ctx, value, handle); err != nil {
			releaseErr := p.Queue.ReleaseSocketModeResponses(ctx, p.Owner, []domain.SocketModeResponse{value}, now.Add(p.RetryDelay).UTC())
			if releaseErr != nil {
				return errors.Join(fmt.Errorf("handle Socket Mode response %q: %w", value.EnvelopeID, err), fmt.Errorf("release Socket Mode responses after handler failure: %w", releaseErr))
			}
			return ResponseDeliveryError{EnvelopeID: value.EnvelopeID, Err: err}
		}
		if err := p.Queue.AckSocketModeResponses(ctx, p.Owner, []domain.SocketModeResponse{value}); err != nil {
			return fmt.Errorf("acknowledge Socket Mode response %q: %w", value.EnvelopeID, err)
		}
	}
	return nil
}

func (p ResponseProcessor) handleWithLease(ctx context.Context, value domain.SocketModeResponse, handle ResponseHandler) error {
	deliveryContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErrors := make(chan error, 1)
	done := make(chan struct{})
	renewDone := make(chan struct{})
	interval := p.Lease / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := p.Queue.RenewSocketModeResponses(deliveryContext, p.Owner, []domain.SocketModeResponse{value}, p.Lease); err != nil {
					cancel()
					renewErrors <- err
					return
				}
			}
		}
	}()
	handleErr := handle(deliveryContext, value)
	cancel()
	close(done)
	<-renewDone
	select {
	case err := <-renewErrors:
		if !errors.Is(err, context.Canceled) && (handleErr == nil || errors.Is(handleErr, context.Canceled)) {
			return err
		}
	default:
	}
	return handleErr
}
