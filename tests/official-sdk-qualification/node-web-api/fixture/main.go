package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/slackapp"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func main() {
	store := memory.New()
	boltTarget, err := url.Parse("http://127.0.0.1:19090")
	if err != nil {
		panic(err)
	}
	boltProxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(boltTarget))
	defer boltProxy.Close()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", Profile: domain.UserProfile{DisplayName: "alice"}})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", Email: "bob@example.com"})
	store.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol", Email: "carol@example.com"})
	now := time.Now().UTC()
	newQualificationEvent := func(id domain.EventID, actorID domain.UserID, payload events.Payload) events.Event {
		event, eventErr := events.New(id, "T1", actorID, payload, now)
		if eventErr != nil {
			panic(eventErr)
		}
		return event
	}
	if err := store.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleOwner,
		newQualificationEvent("qualification-owner", "U1", events.NewPayload("workspace.role_changed",
			events.String("user_id", "U1"), events.String("role", string(domain.WorkspaceRoleOwner))))); err != nil {
		panic(err)
	}
	if err := store.SetWorkspaceRole(context.Background(), "T1", "U2", domain.WorkspaceRoleAdmin,
		newQualificationEvent("qualification-admin", "U1", events.NewPayload("workspace.role_changed",
			events.String("user_id", "U2"), events.String("role", string(domain.WorkspaceRoleAdmin))))); err != nil {
		panic(err)
	}
	if err := store.CreateBot(context.Background(), domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U1", Name: "qualification-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := store.CreateUserMigration(context.Background(), domain.UserMigration{WorkspaceID: "T1", OldID: "U1", GlobalID: "W1"},
		newQualificationEvent("qualification-migration", "U1", events.NewPayload("user.migration_created",
			events.String("old_user_id", "U1"), events.String("global_user_id", "W1")))); err != nil {
		panic(err)
	}
	appCredentialKey := bytes.Repeat([]byte("q"), 32)
	signingSecretCiphertext, err := secretbox.Seal(appCredentialKey, "app:A1:signing-secret", "qualification-signing")
	if err != nil {
		panic(err)
	}
	verificationTokenCiphertext, err := secretbox.Seal(appCredentialKey, "app:A1:verification-token", "qualification-verification")
	if err != nil {
		panic(err)
	}
	manifest := fmt.Sprintf(`{"display_information":{"name":"Qualification"},"features":{"app_home":{"home_tab_enabled":true,"messages_tab_enabled":true}},"oauth_config":{"redirect_urls":["https://example.com/oauth"],"scopes":{"bot":["chat:write","datastore:read","datastore:write"],"user":["users:read"]}},"settings":{"event_subscriptions":{"request_url":%q,"bot_events":["reaction_added","message.channels"]},"token_rotation_enabled":true,"is_hosted":true,"function_runtime":"slack"},"datastores":{"incidents":{"primary_key":"id","attributes":{"id":{"type":"string"},"title":{"type":"string"},"priority":{"type":"integer"}}}}}`, boltProxy.URL+"/slack/events")
	if err := store.CreateApp(context.Background(),
		domain.App{ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Qualification", ClientID: "qualification-client", SigningSecretHash: domain.HashToken("qualification-signing"), SigningSecretCiphertext: signingSecretCiphertext, VerificationTokenHash: domain.HashToken("qualification-verification"), VerificationTokenCiphertext: verificationTokenCiphertext, ManifestVersion: 1, Distribution: "private", TokenRotationEnabled: true, CreatedAt: now, UpdatedAt: now},
		domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "qualification-client", SecretHash: domain.HashToken("qualification-secret"), AppID: "A1"},
	); err != nil {
		panic(err)
	}
	interactionSigningCiphertext, err := secretbox.Seal(appCredentialKey, "app:A2:signing-secret", "interaction-signing")
	if err != nil {
		panic(err)
	}
	interactionVerificationCiphertext, err := secretbox.Seal(appCredentialKey, "app:A2:verification-token", "interaction-verification")
	if err != nil {
		panic(err)
	}
	interactionManifest := `{"display_information":{"name":"Socket Qualification"},"features":{"slash_commands":[{"command":"/sdk-deploy","description":"Exercise Socket Mode slash commands","usage_hint":"owner channel runbook","should_escape":true}],"shortcuts":[{"name":"Create SDK deployment","callback_id":"create_sdk_deployment","description":"Exercise a global shortcut","type":"global"}]},"oauth_config":{"scopes":{"bot":["chat:write","commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := store.CreateApp(context.Background(),
		domain.App{ID: "A2", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Socket Qualification", ClientID: "interaction-client", SigningSecretHash: domain.HashToken("interaction-signing"), SigningSecretCiphertext: interactionSigningCiphertext, VerificationTokenHash: domain.HashToken("interaction-verification"), VerificationTokenCiphertext: interactionVerificationCiphertext, ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now},
		domain.AppManifestRevision{AppID: "A2", Version: 1, Manifest: interactionManifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "interaction-client", SecretHash: domain.HashToken("interaction-secret"), AppID: "A2"},
	); err != nil {
		panic(err)
	}
	if err := store.CreateBot(context.Background(), domain.Bot{ID: "B2", WorkspaceID: "T1", AppID: "A2", UserID: "U1", Name: "interaction-bot", UpdatedAt: now}); err != nil {
		panic(err)
	}
	for _, code := range []string{"qualification-code", "qualification-v2-code", "qualification-v2-user-code", "qualification-token-code", "qualification-openid-code"} {
		scopes := auth.AllScopes()
		if code == "qualification-openid-code" {
			scopes = append(scopes, "openid")
		}
		grant := domain.OAuthCode{Code: code, ClientID: "qualification-client", WorkspaceID: "T1", UserID: "U1", Scopes: scopes, UserScopes: scopes, RedirectURI: "https://example.com/oauth"}
		if code == "qualification-v2-code" {
			grant.BotID = "B1"
			grant.BotUserID = "U1"
			grant.BotScopes = scopes
		}
		if err := store.CreateOAuthCode(context.Background(), grant); err != nil {
			panic(err)
		}
	}
	store.SeedToken(context.Background(), "xoxb-test", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: auth.AllScopes()})
	// Reminder methods are documented as a user-token surface. Keeping a real
	// user credential beside the broad bot fixture prevents the SDK suites from
	// "qualifying" reminders with an identity Slack does not advertise for the
	// method.
	store.SeedToken(context.Background(), "xoxp-reminder-qualification", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user", Scopes: auth.AllScopes()})
	store.SeedToken(context.Background(), "xoxb-qualification-legacy", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: []string{"chat:write"}})
	if err := store.CreateAppConfigurationToken(context.Background(), "xoxe.xoxp-qualification", "xoxe-qualification", domain.AppConfigurationToken{WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(12 * time.Hour)}); err != nil {
		panic(err)
	}
	for _, candidate := range []struct {
		suffix string
		appID  domain.AppID
	}{
		{suffix: "node", appID: "AUNODE"},
		{suffix: "python", appID: "AUPYTHON"},
		{suffix: "java", appID: "AUJAVA"},
	} {
		clientID := "uninstall-" + candidate.suffix
		if err := store.CreateOAuthClient(context.Background(), domain.OAuthClient{ID: clientID, SecretHash: domain.HashToken("uninstall-secret"), AppID: candidate.appID}); err != nil {
			panic(err)
		}
		store.SeedToken(context.Background(), "xoxp-uninstall-"+candidate.suffix, domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: candidate.appID, TokenType: "user", Scopes: auth.AllScopes()})
		if err := store.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: candidate.appID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
			panic(err)
		}
	}
	store.SeedAppToken(context.Background(), "xapp-test", domain.AppTokenRecord{AppID: "A1", Scopes: []string{string(auth.ScopeConnectionsWrite), string(auth.ScopeAuthorizationsRead)}})
	store.SeedAppToken(context.Background(), "xapp-interactions", domain.AppTokenRecord{AppID: "A2", Scopes: []string{string(auth.ScopeConnectionsWrite)}})
	if err := store.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := store.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A2", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
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
	messages := service.Messages{Store: store, Blob: blobs, AppCredentialKey: appCredentialKey}
	if _, err := messages.Post(context.Background(), "T1", "U1", "C1", "rtm qualification event", "", "rtm-qualification"); err != nil {
		panic(err)
	}
	if _, err := messages.Post(context.Background(), "T1", "U1", "C1", "socket qualification event", "", "socket-qualification"); err != nil {
		panic(err)
	}
	for _, trigger := range []string{"qualification-dialog-trigger", "qualification-open-trigger", "qualification-push-trigger"} {
		if err := store.CreateAppTrigger(context.Background(), domain.AppTrigger{
			TokenHash: domain.HashToken(trigger), AppID: "A1", WorkspaceID: "T1", UserID: "U1",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			panic(err)
		}
	}
	modalTrigger := "qualification-modal-trigger"
	if err := store.CreateAppTrigger(context.Background(), domain.AppTrigger{
		TokenHash: domain.HashToken(modalTrigger), AppID: "A2", WorkspaceID: "T1", UserID: "U1",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		panic(err)
	}
	qualificationModal, err := messages.OpenView(context.Background(), "T1", "U1", "A2", modalTrigger,
		`{"type":"modal","title":{"type":"plain_text","text":"SDK modal"},"submit":{"type":"plain_text","text":"Save"},"blocks":[{"type":"input","block_id":"release_name","label":{"type":"plain_text","text":"Release name"},"element":{"type":"plain_text_input","action_id":"name"}}]}`)
	if err != nil {
		panic(err)
	}
	interactionBlocks := `[{"type":"actions","block_id":"qualification","elements":[{"type":"button","action_id":"open_build","text":{"type":"plain_text","text":"Open build"},"value":"842"}]}]`
	interactionMessage, err := messages.PostWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "SDK deployment", interactionBlocks, "", "", "", "A2")
	if err != nil {
		panic(err)
	}
	optionBlocks := `[{"type":"actions","block_id":"project","elements":[{"type":"external_select","action_id":"project_select","placeholder":{"type":"plain_text","text":"Find a project"},"min_query_length":2}]}]`
	optionMessage, err := messages.PostWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "Choose a project", optionBlocks, "", "", "", "A2")
	if err != nil {
		panic(err)
	}
	qualificationFile, err := messages.UploadFile(context.Background(), "T1", "U1", "qualification.txt", "qualification file", "text/plain", int64(len("qualification file")), strings.NewReader("qualification file"))
	if err != nil {
		panic(err)
	}
	store.SeedFileComment(domain.FileComment{ID: "FC1", File: qualificationFile.ID, WorkspaceID: "T1", UserID: "U1", Text: "qualification comment", CreatedAt: time.Now().UTC()})
	authenticator, err := auth.NewStored(store)
	if err != nil {
		panic(err)
	}
	handler, err := slack.NewHandler(messages, authenticator)
	if err != nil {
		panic(err)
	}
	responses := &qualificationResponseSink{store: store, messages: messages, values: make(map[string]string)}
	appAuthenticator, err := auth.NewAppStored(store)
	if err != nil {
		panic(err)
	}
	handler.ConfigureSocketMode(socketmode.Service{Store: store, Host: "127.0.0.1:18080"}, appAuthenticator)
	mux := http.NewServeMux()
	handler.Register(mux)
	mux.HandleFunc("GET /qualification/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /qualification/event-context", func(w http.ResponseWriter, r *http.Request) {
		records, err := messages.ListAppEventsAfter(r.Context(), "A1", 0, 1)
		if err != nil || len(records) != 1 {
			http.Error(w, "qualification event is unavailable", http.StatusServiceUnavailable)
			return
		}
		value, err := events.EventContext("A1", records[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, value)
	})
	mux.Handle("/socket-mode", socketmode.Handler{Store: store, Queue: messages, Interactions: messages, Responses: responses})
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
	mux.HandleFunc("POST /qualification/socket-slash", func(w http.ResponseWriter, r *http.Request) {
		if err := messages.DispatchSlashCommand(r.Context(), "T1", "U1", "C1", "", "/sdk-deploy", "ask @alice in #general https://example.com/runbook", "http://127.0.0.1:18080"); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /qualification/socket-block", func(w http.ResponseWriter, r *http.Request) {
		if err := messages.DispatchBlockAction(r.Context(), "T1", "U1", domain.AppBlockAction{
			MessageID: interactionMessage.ID, BlockID: "qualification", ActionID: "open_build", Type: "button", Value: "842",
		}, "http://127.0.0.1:18080"); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /qualification/socket-options", func(w http.ResponseWriter, r *http.Request) {
		options, err := messages.LoadAppOptions(r.Context(), "T1", "U1", "C1", domain.AppOptionQuery{
			AppID: "A2", MessageID: optionMessage.ID, BlockID: "project", ActionID: "project_select", Value: "prod",
		}, "http://127.0.0.1:18080")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(options); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("POST /qualification/socket-shortcut", func(w http.ResponseWriter, r *http.Request) {
		if err := messages.DispatchAppShortcut(r.Context(), "T1", "U1", "C1", "A2", "create_sdk_deployment", "", "http://127.0.0.1:18080"); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /qualification/socket-modal", func(w http.ResponseWriter, r *http.Request) {
		value := strings.TrimSpace(r.URL.Query().Get("value"))
		if value == "" {
			http.Error(w, "value is required", http.StatusBadRequest)
			return
		}
		state, err := json.Marshal(map[string]any{"values": map[string]any{
			"release_name": map[string]any{"name": map[string]any{"type": "plain_text_input", "value": value}},
		}})
		if err == nil {
			_, err = messages.SubmitView(r.Context(), "T1", "U1", "C1", qualificationModal.ID, string(state), "http://127.0.0.1:18080")
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /qualification/interaction-state", func(w http.ResponseWriter, r *http.Request) {
		ephemeral, err := messages.ListEphemeralMessages(r.Context(), "T1", "U1", "C1", 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		current, err := store.GetMessage(r.Context(), interactionMessage.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		modal, modalErr := messages.CurrentModalView(r.Context(), "T1", "U1")
		modalOpen := modalErr == nil
		if modalErr != nil && !errors.Is(modalErr, storepkg.ErrNotFound) {
			http.Error(w, modalErr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"ephemeral_count": len(ephemeral),
			"ephemeral_text": func() string {
				if len(ephemeral) == 0 {
					return ""
				}
				return ephemeral[len(ephemeral)-1].Text
			}(),
			"message_text": current.Text,
			"modal_open":   modalOpen,
			"modal_errors": func() map[string]string {
				if !modalOpen {
					return nil
				}
				return modal.Errors
			}(),
			"modal_state": func() string {
				if !modalOpen {
					return ""
				}
				return modal.State
			}(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("POST /qualification/bolt-event", func(w http.ResponseWriter, r *http.Request) {
		event, err := events.New("qualification-bolt-event", "T1", "U1", events.NewPayload("reaction.added",
			events.String("channel_id", "C1"),
			events.String("user_id", "U1"),
			events.String("reaction", "wave"),
			events.String("ts", "3.000000"),
		), time.Unix(3, 0).UTC())
		if err == nil {
			err = store.AppendEvent(r.Context(), event)
		}
		processor := slackapp.EventProcessor{Store: store, AppCredentialKey: appCredentialKey, Owner: "qualification-bolt", Lease: time.Minute, Client: boltProxy.Client()}
		for index := 0; index < 32; index++ {
			count, cycleErr := processor.RunOnce(r.Context())
			// A malformed historical producer record is acknowledged so it
			// cannot block the app forever and is returned as an operator
			// warning. Continue only when that acknowledgement made progress;
			// an error with no progress is an actual delivery outage.
			if cycleErr != nil && count == 0 {
				err = cycleErr
				break
			}
			if count == 0 {
				break
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	serverHandler, err := newSDKMethodRecorder(mux, os.Getenv("SAMEOLDCHAT_SDK_COVERAGE_LOG"))
	if err != nil {
		panic(err)
	}
	server := &http.Server{Addr: "127.0.0.1:18080", Handler: serverHandler}
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

type sdkMethodRecorder struct {
	next http.Handler
	path string
	mu   sync.Mutex
}

func newSDKMethodRecorder(next http.Handler, path string) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("SDK method recorder requires an HTTP handler")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return next, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open SDK coverage log: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close SDK coverage log: %w", err)
	}
	return &sdkMethodRecorder{next: next, path: path}, nil
}

func (r *sdkMethodRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if method, ok := strings.CutPrefix(request.URL.Path, "/api/"); ok && method != "" && !strings.Contains(method, "/") {
		r.mu.Lock()
		file, err := os.OpenFile(r.path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = fmt.Fprintln(file, method)
			err = errors.Join(err, file.Close())
		}
		r.mu.Unlock()
		if err != nil {
			http.Error(w, "record SDK method coverage", http.StatusInternalServerError)
			return
		}
	}
	r.next.ServeHTTP(w, request)
}

type qualificationResponseSink struct {
	store    *memory.Store
	messages service.Messages
	mu       sync.RWMutex
	values   map[string]string
}

func (s *qualificationResponseSink) HandleSocketModeResponse(ctx context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	if err := s.messages.HandleSocketModeResponse(ctx, appID, envelopeID, payload); err != nil {
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
