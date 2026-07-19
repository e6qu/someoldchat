package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/api/slack"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/realtime"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func main() {
	store := memory.New()
	if err := store.AppendEvent(context.Background(), events.Event{
		ID:          "qualification-socket-event",
		WorkspaceID: "T1",
		Topic:       "message.created",
		Payload:     `{"type":"event_callback","team_id":"T1","api_app_id":"A1","event_id":"qualification-socket-event","event_time":1,"event":{"type":"message","channel":"C1","user":"U1","text":"socket qualification event","ts":"1.000000","event_ts":"1.000000"}}`,
		CreatedAt:   time.Unix(1, 0).UTC(),
	}); err != nil {
		panic(err)
	}
	if err := store.AppendEvent(context.Background(), events.Event{
		ID:          "qualification-rtm-event",
		WorkspaceID: "T1",
		Topic:       "message.created",
		Payload:     `{"type":"message","channel":"C1","user":"U1","text":"rtm qualification event","ts":"2.000000","event_ts":"2.000000"}`,
		CreatedAt:   time.Unix(2, 0).UTC(),
	}); err != nil {
		panic(err)
	}
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
	store.SeedAppToken(context.Background(), "xapp-test", domain.AppTokenRecord{AppID: "A1", Scopes: []string{string(auth.ScopeConnectionsWrite)}})
	if err := store.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
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
	responses := &qualificationResponseSink{store: store, values: make(map[string]string)}
	appAuthenticator, err := auth.NewAppStored(store)
	if err != nil {
		panic(err)
	}
	handler.ConfigureSocketMode(socketmode.Service{Store: store, Host: "127.0.0.1:18080"}, appAuthenticator)
	mux := http.NewServeMux()
	handler.Register(mux)
	mux.Handle("/socket-mode", socketmode.Handler{Store: store, Events: messages, Cursors: messages, Responses: responses})
	rtmHandler, err := realtime.NewRTMHandler(messages, "T1", messages, messages)
	if err != nil {
		panic(err)
	}
	rtmHandler.RegisterRTM(mux)
	mux.HandleFunc("GET /qualification/socket-mode-response", func(w http.ResponseWriter, r *http.Request) {
		envelopeID := r.URL.Query().Get("envelope_id")
		payload, ok := responses.get(envelopeID)
		if !ok {
			http.Error(w, "response not recorded", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
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

type qualificationResponseSink struct {
	store  *memory.Store
	mu     sync.RWMutex
	values map[string]string
}

func (s *qualificationResponseSink) HandleSocketModeResponse(ctx context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	if err := (socketmode.ResponseRecorder{Store: s.store}).HandleSocketModeResponse(ctx, appID, envelopeID, payload); err != nil {
		return err
	}
	s.mu.Lock()
	s.values[envelopeID] = string(payload)
	s.mu.Unlock()
	return nil
}

func (s *qualificationResponseSink) get(envelopeID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	payload, ok := s.values[envelopeID]
	return payload, ok
}

func scopeSet() map[auth.Scope]struct{} {
	result := make(map[auth.Scope]struct{})
	for _, scope := range auth.AllScopes() {
		result[auth.Scope(scope)] = struct{}{}
	}
	return result
}
