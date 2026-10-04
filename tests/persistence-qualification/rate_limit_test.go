package qualification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Every web replica of a deployment draws Web API calls from one budget in
// the shared store. Many callers taking at the same instant — on PostgreSQL,
// concurrent transactions from the connection pool, which is what replicas
// are to the database — must be admitted exactly the burst between them: a
// read-then-write admission lets two of them take the same last call. Once
// the interval passes, one more is admitted; a refused call is told how long
// to wait; and a key whose time has passed is collected without changing
// what it admits.
func sharedRateLimitAdmitsExactlyItsBurst(t *testing.T, open opener) {
	ctx := context.Background()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	key := fmt.Sprintf("limit-users-list-qualification-%d", time.Now().UnixNano())
	allowance := domain.RateAllowance{Interval: time.Minute, Burst: 5}
	now := time.Unix(1_800_000_000, 0).UTC()
	const callers = 24
	var wait sync.WaitGroup
	var mu sync.Mutex
	admitted, refused := 0, 0
	var failures []error
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, ok, err := repository.TakeRateToken(ctx, key, allowance, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failures = append(failures, err)
			case ok:
				admitted++
			default:
				refused++
			}
		}()
	}
	wait.Wait()
	if len(failures) > 0 {
		t.Fatalf("concurrent admissions failed: %v", failures)
	}
	if admitted != allowance.Burst || refused != callers-allowance.Burst {
		t.Fatalf("admitted %d and refused %d of %d concurrent calls, want exactly the burst of %d admitted", admitted, refused, callers, allowance.Burst)
	}
	retry, ok, err := repository.TakeRateToken(ctx, key, allowance, now)
	if err != nil || ok || retry != allowance.Interval {
		t.Fatalf("a call past the burst: retry=%s admitted=%v err=%v, want refused for one interval", retry, ok, err)
	}
	if _, ok, err := repository.TakeRateToken(ctx, key, allowance, now.Add(allowance.Interval)); err != nil || !ok {
		t.Fatalf("a call one interval later: admitted=%v err=%v, want admitted", ok, err)
	}
	if _, ok, err := repository.TakeRateToken(ctx, key+"-other", allowance, now); err != nil || !ok {
		t.Fatalf("another key: admitted=%v err=%v, want its own budget", ok, err)
	}
	// A key every profile cannot store alike is refused by every profile.
	for _, invalid := range []string{"", "limit\x00nul", strings.Repeat("k", domain.MaxRateLimitKeyBytes+1)} {
		if _, _, err := repository.TakeRateToken(ctx, invalid, allowance, now); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("key %q: err=%v, want ErrInvalidArgument", invalid, err)
		}
	}
	// Long after every key's time has passed, a call both triggers the
	// collection of passed keys and is admitted as a first call would be.
	later := now.Add(24 * time.Hour)
	for index := range allowance.Burst {
		if _, ok, err := repository.TakeRateToken(ctx, key, allowance, later); err != nil || !ok {
			t.Fatalf("burst call %d after the key's time passed: admitted=%v err=%v", index, ok, err)
		}
	}
	if _, ok, err := repository.TakeRateToken(ctx, key, allowance, later); err != nil || ok {
		t.Fatalf("the call past a fresh burst: admitted=%v err=%v, want refused", ok, err)
	}
}
