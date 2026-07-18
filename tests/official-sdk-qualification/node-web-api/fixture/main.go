package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/api/slack"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func main() {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", Profile: domain.UserProfile{DisplayName: "alice"}})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", Email: "bob@example.com"})
	if err := store.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleOwner, events.Event{ID: "qualification-owner", WorkspaceID: "T1", Topic: "workspace.role.changed", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := store.SetWorkspaceRole(context.Background(), "T1", "U2", domain.WorkspaceRoleAdmin, events.Event{ID: "qualification-admin", WorkspaceID: "T1", Topic: "workspace.role.changed", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := store.CreateBot(context.Background(), domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U1", Name: "qualification-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := store.CreateUserMigration(context.Background(), domain.UserMigration{WorkspaceID: "T1", OldID: "U1", GlobalID: "W1"}, events.Event{ID: "qualification-migration", WorkspaceID: "T1", Topic: "migration.created", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := store.CreateOAuthClient(context.Background(), domain.OAuthClient{ID: "qualification-client", SecretHash: domain.HashToken("qualification-secret"), AppID: "A1"}); err != nil {
		panic(err)
	}
	for _, code := range []string{"qualification-code", "qualification-v2-code", "qualification-token-code"} {
		if err := store.CreateOAuthCode(context.Background(), domain.OAuthCode{Code: code, ClientID: "qualification-client", WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), RedirectURI: "https://example.com/oauth"}); err != nil {
			panic(err)
		}
	}
	store.SeedToken(context.Background(), "xoxb-test", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes()})
	if err := store.SeedSession(context.Background(), "qualification-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		panic(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "lifecycle"})
	store.SeedConversationMember("C1", "U1")
	blobRoot, err := os.MkdirTemp("", "sameoldchat-sdk-files-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(blobRoot)
	blobs, err := blob.NewFilesystem(blobRoot, 1<<20)
	if err != nil {
		panic(err)
	}
	messages := service.Messages{Store: store, Blob: blobs}
	qualificationFile, err := messages.UploadFile(context.Background(), "T1", "U1", "qualification.txt", "qualification file", "text/plain", int64(len("qualification file")), strings.NewReader("qualification file"))
	if err != nil {
		panic(err)
	}
	store.SeedFileComment(domain.FileComment{ID: "FC1", File: qualificationFile.ID, WorkspaceID: "T1", UserID: "U1", Text: "qualification comment", CreatedAt: time.Now().UTC()})
	authenticator, err := auth.NewStatic("xoxb-test", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: scopeSet()})
	if err != nil {
		panic(err)
	}
	handler, err := slack.NewHandler(messages, authenticator)
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
