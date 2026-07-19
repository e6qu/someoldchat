package grpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMapErrorPreservesCanonicalDomainClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{name: "not found", err: store.ErrNotFound, code: codes.NotFound},
		{name: "already exists", err: store.ErrAlreadyExists, code: codes.AlreadyExists},
		{name: "conflict", err: store.ErrConflict, code: codes.Aborted},
		{name: "lease conflict", err: store.ErrLeaseConflict, code: codes.Aborted},
		{name: "idempotency conflict", err: store.ErrIdempotencyConflict, code: codes.Aborted},
		{name: "socket mode limit", err: store.ErrSocketModeConnectionLimit, code: codes.ResourceExhausted},
		{name: "invalid workspace", err: service.ErrInvalidWorkspace, code: codes.InvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := status.Code(mapError(fmt.Errorf("wrapped: %w", test.err))); got != test.code {
				t.Fatalf("mapError() code = %s, want %s", got, test.code)
			}
		})
	}
}
