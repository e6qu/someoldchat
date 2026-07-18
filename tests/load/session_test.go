package load

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestSessionCreationIsSingleWinnerUnderLoad(t *testing.T) {
	const callers = 128
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T-session"})
	repository.SeedUser(domain.User{ID: "U-session", WorkspaceID: "T-session"})
	record := domain.SessionRecord{WorkspaceID: "T-session", UserID: "U-session", Scopes: []string{string(auth.ScopeChannelsHistory)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}

	var accepted atomic.Int32
	var duplicates atomic.Int32
	var group sync.WaitGroup
	group.Add(callers)
	for caller := 0; caller < callers; caller++ {
		go func() {
			defer group.Done()
			err := repository.CreateSession(ctx, "shared-session", record)
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, store.ErrAlreadyExists):
				duplicates.Add(1)
			default:
				t.Errorf("unexpected session creation error: %v", err)
			}
		}()
	}
	group.Wait()

	if accepted.Load() != 1 || duplicates.Load() != callers-1 {
		t.Fatalf("accepted %d sessions and %d duplicates, want 1 and %d", accepted.Load(), duplicates.Load(), callers-1)
	}
}

func TestSessionRevocationIsVisibleToEveryReplica(t *testing.T) {
	const sessions = 64
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T-revoke"})
	repository.SeedUser(domain.User{ID: "U-revoke", WorkspaceID: "T-revoke"})
	for index := 0; index < sessions; index++ {
		token := fmt.Sprintf("session-%d", index)
		if err := repository.CreateSession(ctx, token, domain.SessionRecord{WorkspaceID: "T-revoke", UserID: "U-revoke", Scopes: []string{string(auth.ScopeChannelsHistory)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
			t.Fatalf("create %s: %v", token, err)
		}
	}

	var group sync.WaitGroup
	group.Add(sessions)
	for index := 0; index < sessions; index++ {
		go func(index int) {
			defer group.Done()
			if err := repository.RevokeSession(ctx, fmt.Sprintf("session-%d", index)); err != nil {
				t.Errorf("revoke session-%d: %v", index, err)
			}
		}(index)
	}
	group.Wait()

	first, err := auth.NewBrowser(repository)
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.NewBrowser(repository)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < sessions; index++ {
		request := newSessionRequest(fmt.Sprintf("session-%d", index))
		if _, err := first.Authenticate(request); !errors.Is(err, auth.ErrNotAuthenticated) {
			t.Fatalf("first replica authenticated revoked session-%d: %v", index, err)
		}
		request = newSessionRequest(fmt.Sprintf("session-%d", index))
		if _, err := second.Authenticate(request); !errors.Is(err, auth.ErrNotAuthenticated) {
			t.Fatalf("second replica authenticated revoked session-%d: %v", index, err)
		}
	}
}

func newSessionRequest(token string) *http.Request {
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	return request
}
