package load

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/activator"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
)

func TestDurableActivatorForwardsConcurrentRequestsToTheirCallers(t *testing.T) {
	const requests = 32
	spool, err := activator.OpenSQLiteSpool(filepath.Join(t.TempDir(), "spool.db"), []byte("01234567890123456789012345678901"), activator.SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 64 * 1024, MaxQueuedRequests: requests})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()

	var mu sync.Mutex
	forwarded := make(map[string]struct{}, requests)
	handler, err := activator.NewDurableForwardingHandler(lifecycle.New(lifecycle.StateHibernated), func(context.Context, uint64) error {
		return nil
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "body unavailable", http.StatusBadRequest)
			return
		}
		mu.Lock()
		forwarded[string(body)] = struct{}{}
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
	}), spool, "activator-load", 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan int, requests)
	var group sync.WaitGroup
	group.Add(requests)
	for index := 0; index < requests; index++ {
		go func(index int) {
			defer group.Done()
			body := "request-" + string(rune('a'+index))
			request := httptest.NewRequest(http.MethodPost, "/activate", strings.NewReader(body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusCreated || response.Body.String() != body {
				results <- response.Code
				return
			}
			results <- http.StatusCreated
		}(index)
	}
	group.Wait()
	close(results)
	for status := range results {
		if status != http.StatusCreated {
			t.Fatalf("concurrent durable request returned status %d", status)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(forwarded) != requests {
		t.Fatalf("forwarded %d distinct requests, want %d", len(forwarded), requests)
	}
	remaining, err := spool.List(context.Background(), requests)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining spool records=%+v err=%v", remaining, err)
	}
}
