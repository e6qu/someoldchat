package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/sameoldchat/sameoldchat/internal/api/slack"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func main() {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", Profile: domain.UserProfile{DisplayName: "alice"}})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	authenticator, err := auth.NewStatic("xoxb-test", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: scopeSet()})
	if err != nil {
		panic(err)
	}
	handler, err := slack.NewHandler(service.Messages{Store: store}, authenticator)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	server := &http.Server{Addr: "127.0.0.1:18080", Handler: mux}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	if err := server.Shutdown(context.Background()); err != nil {
		panic(err)
	}
}

func scopeSet() map[auth.Scope]struct{} {
	result := make(map[auth.Scope]struct{})
	for _, scope := range auth.AllScopes() {
		result[auth.Scope(scope)] = struct{}{}
	}
	return result
}
