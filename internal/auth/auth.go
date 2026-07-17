package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type Scope string

const (
	ScopeChatWrite               Scope = "chat:write"
	ScopeChannelsHistory         Scope = "channels:history"
	ScopeUsersRead               Scope = "users:read"
	ScopeUsersReadEmail          Scope = "users:read.email"
	ScopeUsersWrite              Scope = "users:write"
	ScopeUsersProfileWrite       Scope = "users.profile:write"
	ScopeChannelsRead            Scope = "channels:read"
	ScopeChannelsManage          Scope = "channels:manage"
	ScopeReactionsWrite          Scope = "reactions:write"
	ScopeReactionsRead           Scope = "reactions:read"
	ScopePinsWrite               Scope = "pins:write"
	ScopePinsRead                Scope = "pins:read"
	ScopeSearchRead              Scope = "search:read"
	ScopeFilesWrite              Scope = "files:write"
	ScopeFilesRead               Scope = "files:read"
	ScopeRemoteFilesRead         Scope = "remote_files:read"
	ScopeRemoteFilesWrite        Scope = "remote_files:write"
	ScopeRemoteFilesShare        Scope = "remote_files:share"
	ScopeTeamRead                Scope = "team:read"
	ScopeEmojiRead               Scope = "emoji:read"
	ScopeIdentityBasic           Scope = "identity.basic"
	ScopeRTMStream               Scope = "rtm:stream"
	ScopeDNDRead                 Scope = "dnd:read"
	ScopeDNDWrite                Scope = "dnd:write"
	ScopeStarsRead               Scope = "stars:read"
	ScopeStarsWrite              Scope = "stars:write"
	ScopeRemindersRead           Scope = "reminders:read"
	ScopeRemindersWrite          Scope = "reminders:write"
	ScopeUserGroupsRead          Scope = "usergroups:read"
	ScopeUserGroupsWrite         Scope = "usergroups:write"
	ScopeCallsRead               Scope = "calls:read"
	ScopeCallsWrite              Scope = "calls:write"
	ScopeWorkflowStepsExecute    Scope = "workflow.steps:execute"
	ScopeTokensBasic             Scope = "tokens.basic"
	ScopeAdmin                   Scope = "admin"
	ScopeAdminUsersRead          Scope = "admin.users:read"
	ScopeAdminUsersWrite         Scope = "admin.users:write"
	ScopeAdminConversationsWrite Scope = "admin.conversations:write"
	ScopeAdminConversationsRead  Scope = "admin.conversations:read"
	ScopeAdminEmojiWrite         Scope = "admin.emoji:write"
	ScopeAdminUserGroupsRead     Scope = "admin.usergroups:read"
	ScopeAdminUserGroupsWrite    Scope = "admin.usergroups:write"
	ScopeAdminTeamsRead          Scope = "admin.teams:read"
	ScopeAdminTeamsWrite         Scope = "admin.teams:write"
	ScopeAdminInvitesRead        Scope = "admin.invites:read"
	ScopeAdminInvitesWrite       Scope = "admin.invites:write"
	ScopeAdminAppsRead           Scope = "admin.apps:read"
	ScopeAdminAppsWrite          Scope = "admin.apps:write"
)

type Principal struct {
	WorkspaceID domain.WorkspaceID
	UserID      domain.UserID
	Scopes      map[Scope]struct{}
}

func (p Principal) HasScope(scope Scope) bool { _, ok := p.Scopes[scope]; return ok }

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

type TokenStore interface {
	LookupToken(context.Context, string) (domain.TokenRecord, error)
}

type TokenRevoker interface {
	RevokeToken(context.Context, string) error
}

type SessionStore interface {
	LookupSession(context.Context, string) (domain.SessionRecord, error)
}

// SessionRevoker is the durable mutation used to invalidate a browser session.
// It is separate from SessionStore so read-only consumers cannot accidentally
// acquire mutation authority.
type SessionRevoker interface {
	RevokeSession(context.Context, string) error
}

var (
	ErrNotAuthenticated = errors.New("not authenticated")
	ErrMissingScope     = errors.New("missing scope")
)

type Static struct {
	token     string
	principal Principal
}

type Stored struct{ store TokenStore }

type Browser struct{ store SessionStore }

const SessionCookieName = "sameoldchat_session"

func AllScopes() []string {
	return []string{string(ScopeChatWrite), string(ScopeChannelsHistory), string(ScopeUsersRead), string(ScopeUsersReadEmail), string(ScopeUsersWrite), string(ScopeUsersProfileWrite), string(ScopeChannelsRead), string(ScopeChannelsManage), string(ScopeReactionsWrite), string(ScopeReactionsRead), string(ScopePinsWrite), string(ScopePinsRead), string(ScopeSearchRead), string(ScopeFilesWrite), string(ScopeFilesRead), string(ScopeRemoteFilesRead), string(ScopeRemoteFilesWrite), string(ScopeRemoteFilesShare), string(ScopeTeamRead), string(ScopeEmojiRead), string(ScopeIdentityBasic), string(ScopeRTMStream), string(ScopeDNDRead), string(ScopeDNDWrite), string(ScopeStarsRead), string(ScopeStarsWrite), string(ScopeRemindersRead), string(ScopeRemindersWrite), string(ScopeUserGroupsRead), string(ScopeUserGroupsWrite), string(ScopeCallsRead), string(ScopeCallsWrite), string(ScopeWorkflowStepsExecute), string(ScopeTokensBasic), string(ScopeAdmin), string(ScopeAdminUsersRead), string(ScopeAdminUsersWrite), string(ScopeAdminConversationsRead), string(ScopeAdminConversationsWrite), string(ScopeAdminEmojiWrite), string(ScopeAdminUserGroupsRead), string(ScopeAdminUserGroupsWrite), string(ScopeAdminTeamsRead), string(ScopeAdminTeamsWrite), string(ScopeAdminInvitesRead), string(ScopeAdminInvitesWrite), string(ScopeAdminAppsRead), string(ScopeAdminAppsWrite)}
}

func NewBrowser(store SessionStore) (Browser, error) {
	if store == nil {
		return Browser{}, errors.New("browser authenticator requires a session store")
	}
	return Browser{store: store}, nil
}

func (b Browser) Authenticate(r *http.Request) (Principal, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return Principal{}, ErrNotAuthenticated
	}
	record, err := b.store.LookupSession(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrNotAuthenticated
		}
		return Principal{}, fmt.Errorf("lookup browser session: %w", err)
	}
	if record.Revoked || !record.ExpiresAt.After(time.Now().UTC()) || record.WorkspaceID == "" || record.UserID == "" {
		return Principal{}, ErrNotAuthenticated
	}
	scopes := make(map[Scope]struct{}, len(record.Scopes))
	for _, scope := range record.Scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes[Scope(scope)] = struct{}{}
		}
	}
	if len(scopes) == 0 {
		return Principal{}, ErrNotAuthenticated
	}
	return Principal{WorkspaceID: record.WorkspaceID, UserID: record.UserID, Scopes: scopes}, nil
}

func NewStored(store TokenStore) (Stored, error) {
	if store == nil {
		return Stored{}, errors.New("stored authenticator requires a token store")
	}
	return Stored{store: store}, nil
}

func (s Stored) Authenticate(r *http.Request) (Principal, error) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	record, err := s.store.LookupToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrNotAuthenticated
		}
		return Principal{}, fmt.Errorf("lookup access token: %w", err)
	}
	if record.Revoked || record.WorkspaceID == "" || record.UserID == "" {
		return Principal{}, ErrNotAuthenticated
	}
	scopes := make(map[Scope]struct{}, len(record.Scopes))
	for _, scope := range record.Scopes {
		scopes[Scope(scope)] = struct{}{}
	}
	return Principal{WorkspaceID: record.WorkspaceID, UserID: record.UserID, Scopes: scopes}, nil
}

func NewStatic(token string, principal Principal) (Static, error) {
	if strings.TrimSpace(token) == "" {
		return Static{}, errors.New("static authenticator requires a token")
	}
	if principal.WorkspaceID == "" || principal.UserID == "" {
		return Static{}, errors.New("static authenticator requires a principal")
	}
	return Static{token: token, principal: principal}, nil
}

func (s Static) Authenticate(r *http.Request) (Principal, error) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	if token != s.token {
		return Principal{}, ErrNotAuthenticated
	}
	return s.principal, nil
}
