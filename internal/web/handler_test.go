package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestHTMXPostMessage(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeChatWrite), string(auth.ScopeChannelsHistory)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1")
	if err != nil {
		t.Fatal(err)
	}
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/app/message", strings.NewReader("text=hello"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "hello") {
		t.Fatalf("body = %s", res.Body)
	}
	index := httptest.NewRequest(http.MethodGet, "/app", nil)
	index.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	indexResult := httptest.NewRecorder()
	mux.ServeHTTP(indexResult, index)
	if indexResult.Code != http.StatusOK || !strings.Contains(indexResult.Body.String(), "general") || !strings.Contains(indexResult.Body.String(), "hello") || !strings.Contains(indexResult.Body.String(), "unread messages") || !strings.Contains(indexResult.Body.String(), "theme-toggle") || !strings.Contains(indexResult.Body.String(), "data-theme=\"light\"") || !strings.Contains(indexResult.Body.String(), "HX-Request") {
		t.Fatalf("index status=%d body=%s", indexResult.Code, indexResult.Body)
	}
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "C1"); err != nil {
		t.Fatalf("read cursor was not persisted: %v", err)
	}
	page, err := s.ListMessages(context.Background(), "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%+v err=%v", page, err)
	}
	thread := domain.NewMessageTimestamp(page.Messages[0].CreatedAt)
	reply := httptest.NewRequest(http.MethodPost, "/app/message?channel=C1", strings.NewReader("text=reply&thread_ts="+string(thread)))
	reply.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reply.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	replyResult := httptest.NewRecorder()
	mux.ServeHTTP(replyResult, reply)
	if replyResult.Code != http.StatusSeeOther || !strings.Contains(replyResult.Header().Get("Location"), "thread=") {
		t.Fatalf("reply status=%d location=%s", replyResult.Code, replyResult.Header().Get("Location"))
	}
}

func TestWebFormRejectsRepeatedFields(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeChatWrite), string(auth.ScopeChannelsHistory)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/app/message", strings.NewReader("text=one&text=two"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestWebSessionRevocationClearsCookieAndDurablyInvalidates(t *testing.T) {
	s := memory.New()
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/app/session/revoke", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/" {
		t.Fatalf("status=%d location=%q", res.Code, res.Header().Get("Location"))
	}
	if !strings.Contains(res.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("session cookie was not cleared: %q", res.Header().Get("Set-Cookie"))
	}
	record, err := s.LookupSession(context.Background(), "session")
	if err != nil || !record.Revoked {
		t.Fatalf("session=%+v err=%v", record, err)
	}
}

func TestMembersPageRendersDurableProfiles(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", RealName: "Alice Example", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeUsersRead), string(auth.ScopeUsersWrite)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/app/members", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Available") || !strings.Contains(res.Body.String(), ":wave:") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	update := httptest.NewRequest(http.MethodPost, "/app/profile", strings.NewReader("display_name=updated&status_text=Ready&status_emoji=%3Aok%3A&image_24=&image_32=&image_48=&image_72=&image_192=&image_512=&image_1024="))
	update.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	update.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	updateResult := httptest.NewRecorder()
	mux.ServeHTTP(updateResult, update)
	if updateResult.Code != http.StatusSeeOther {
		t.Fatalf("profile update status=%d body=%s", updateResult.Code, updateResult.Body)
	}
	stored, err := s.GetUser(context.Background(), "U1")
	if err != nil || stored.Profile.DisplayName != "updated" || stored.Profile.StatusText != "Ready" {
		t.Fatalf("updated profile=%+v err=%v", stored.Profile, err)
	}
}

func TestHTMXReactionAndPinMutationsUseExplicitMessageTarget(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	created := time.Now().UTC()
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: created}
	if err := s.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(ctx, "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	timestamp := domain.NewMessageTimestamp(created)
	reaction := httptest.NewRequest(http.MethodPost, "/app/reaction?channel=C1&ts="+string(timestamp), strings.NewReader("name=%3Awave%3A"))
	reaction.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reaction.Header.Set("HX-Request", "true")
	reaction.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	reactionResult := httptest.NewRecorder()
	mux.ServeHTTP(reactionResult, reaction)
	if reactionResult.Code != http.StatusNoContent || reactionResult.Header().Get("HX-Redirect") == "" {
		t.Fatalf("reaction status=%d headers=%v body=%s", reactionResult.Code, reactionResult.Header(), reactionResult.Body)
	}
	reactions, _, _, err := s.ListReactions(ctx, "M1", domain.PageRequest{Limit: 10})
	if err != nil || len(reactions) != 1 || reactions[0].Name != ":wave:" {
		t.Fatalf("reactions=%+v err=%v", reactions, err)
	}

	pin := httptest.NewRequest(http.MethodPost, "/app/pin?channel=C1&ts="+string(timestamp), nil)
	pin.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	pinResult := httptest.NewRecorder()
	mux.ServeHTTP(pinResult, pin)
	if pinResult.Code != http.StatusSeeOther {
		t.Fatalf("pin status=%d body=%s", pinResult.Code, pinResult.Body)
	}
	pins, _, _, err := s.ListPins(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(pins) != 1 || pins[0].Message != "M1" {
		t.Fatalf("pins=%+v err=%v", pins, err)
	}
}

func TestWebOpensNormalizedDirectConversation(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(ctx, "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/app/conversation/open", strings.NewReader("users=U2%2C%20U2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || !strings.Contains(res.Header().Get("Location"), "channel=") {
		t.Fatalf("status=%d location=%q body=%s", res.Code, res.Header().Get("Location"), res.Body)
	}
	conversations, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 2 {
		t.Fatalf("conversations=%+v err=%v", conversations, err)
	}
	var direct domain.Conversation
	for _, conversation := range conversations.Conversations {
		if conversation.IsDirect {
			direct = conversation
		}
	}
	if direct.ID == "" || !direct.IsPrivate {
		t.Fatalf("direct conversation=%+v", direct)
	}
}
