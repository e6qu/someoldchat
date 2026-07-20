package load

import (
	"context"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A reconnect storm is the normal shape of a Socket Mode outage recovering:
// every client that lost its connection dials again at once. Two invariants
// have to hold through that, and both are check-then-act shaped, so a test that
// exercises them sequentially proves nothing.
//
//   - the active connection limit is a limit, not an average;
//   - a connection identifier admits exactly one client, or two processes
//     believe they own the same connection.

func connectionID(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse connection URL %q: %v", raw, err)
	}
	id := parsed.Query().Get("connection_id")
	if id == "" {
		t.Fatalf("connection URL %q carries no connection_id", raw)
	}
	return id
}

func TestSocketModeReconnectStormRespectsTheActiveLimit(t *testing.T) {
	const dialers = 64
	repository := memory.New()
	service := socketmode.Service{Store: repository, Host: "127.0.0.1:18080"}
	ctx := context.Background()

	var opened, refused int64
	identifiers := make([]string, dialers)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < dialers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			result, err := service.Open(ctx, "A1")
			if err != nil {
				atomic.AddInt64(&refused, 1)
				return
			}
			atomic.AddInt64(&opened, 1)
			identifiers[index] = result.URL
		}(index)
	}
	close(start)
	group.Wait()

	// Every issued ticket is now dialled, which is what turns it into an active
	// connection and what the limit actually counts.
	var accepted, rejected int64
	group = sync.WaitGroup{}
	start = make(chan struct{})
	for index := 0; index < dialers; index++ {
		if identifiers[index] == "" {
			continue
		}
		group.Add(1)
		go func(raw string) {
			defer group.Done()
			<-start
			if _, err := repository.ConsumeSocketModeConnection(ctx, connectionID(t, raw)); err != nil {
				atomic.AddInt64(&rejected, 1)
				return
			}
			atomic.AddInt64(&accepted, 1)
		}(identifiers[index])
	}
	close(start)
	group.Wait()

	active, err := repository.CountSocketModeConnections(ctx, "A1")
	if err != nil {
		t.Fatal(err)
	}
	if active > domain.SocketModeConnectionLimit {
		t.Fatalf("%d dialers left %d active connections, above the limit of %d (opened=%d refused=%d accepted=%d rejected=%d)",
			dialers, active, domain.SocketModeConnectionLimit, opened, refused, accepted, rejected)
	}
	if accepted > domain.SocketModeConnectionLimit {
		t.Fatalf("%d dials were accepted, above the limit of %d", accepted, domain.SocketModeConnectionLimit)
	}
}

// One identifier, many clients racing to dial it. Exactly one may win: a
// connection accepted twice means two processes both believe they hold it and
// both will acknowledge the same events.
func TestSocketModeConnectionIsSingleUseUnderARace(t *testing.T) {
	// Stays below the active limit: this test is about one identifier admitting
	// one client, not about the limit, and a refusal for being at the limit
	// would be indistinguishable from a refusal for being already consumed.
	const (
		connections = domain.SocketModeConnectionLimit - 2
		dialers     = 8
	)
	repository := memory.New()
	service := socketmode.Service{Store: repository, Host: "127.0.0.1:18080"}
	ctx := context.Background()

	identifiers := make([]string, 0, connections)
	for index := 0; index < connections; index++ {
		result, err := service.Open(ctx, "A1")
		if err != nil {
			t.Fatal(err)
		}
		identifiers = append(identifiers, connectionID(t, result.URL))
	}

	winners := make([]int64, connections)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index, id := range identifiers {
		for dialer := 0; dialer < dialers; dialer++ {
			group.Add(1)
			go func(index int, id string) {
				defer group.Done()
				<-start
				if _, err := repository.ConsumeSocketModeConnection(ctx, id); err == nil {
					atomic.AddInt64(&winners[index], 1)
				}
			}(index, id)
		}
	}
	close(start)
	group.Wait()

	for index, count := range winners {
		if count != 1 {
			t.Fatalf("connection %d was accepted %d times under %d racing dialers, want exactly 1", index, count, dialers)
		}
	}
}
