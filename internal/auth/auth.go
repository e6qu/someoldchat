package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
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
	ScopeBookmarksRead           Scope = "bookmarks:read"
	ScopeBookmarksWrite          Scope = "bookmarks:write"
	ScopeSearchRead              Scope = "search:read"
	ScopeFilesWrite              Scope = "files:write"
	ScopeFilesRead               Scope = "files:read"
	ScopeRemoteFilesRead         Scope = "remote_files:read"
	ScopeRemoteFilesWrite        Scope = "remote_files:write"
	ScopeRemoteFilesShare        Scope = "remote_files:share"
	ScopeCanvasesRead            Scope = "canvases:read"
	ScopeCanvasesWrite           Scope = "canvases:write"
	ScopeTeamRead                Scope = "team:read"
	ScopeEmojiRead               Scope = "emoji:read"
	ScopeIdentityBasic           Scope = "identity.basic"
	ScopeRTMStream               Scope = "rtm:stream"
	ScopeConnectionsWrite        Scope = "connections:write"
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
	AppID       domain.AppID
	Scopes      map[Scope]struct{}
}

func (p Principal) HasScope(scope Scope) bool { _, ok := p.Scopes[scope]; return ok }

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

type TokenStore interface {
	LookupToken(context.Context, string) (domain.TokenRecord, error)
}

type AppTokenStore interface {
	LookupAppToken(context.Context, string) (domain.AppTokenRecord, error)
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
type AppStored struct{ store AppTokenStore }

type Browser struct{ store SessionStore }

const SessionCookieName = "sameoldchat_session"

const (
	CSRFTokenCookieName = "sameoldchat_csrf"
	CSRFTokenFieldName  = "_csrf"
	CSRFTokenHeaderName = "X-SameOldChat-CSRF"
)

func ValidateSessionCookieDomain(domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil
	}
	if strings.HasPrefix(domain, ".") || strings.ContainsAny(domain, "/: \t\r\n") {
		return errors.New("session cookie domain must be a hostname without a leading dot, port, or path")
	}
	parsed, err := url.Parse("//" + domain)
	if err != nil || parsed.Host != domain || parsed.Hostname() != domain || net.ParseIP(domain) != nil {
		return errors.New("session cookie domain must be a DNS hostname")
	}
	return nil
}

func SessionCookie(value string, maxAge int, domain string) *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Value: value, Domain: strings.TrimSpace(domain), Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

func CSRFToken(sessionToken string) string {
	digest := sha256.Sum256([]byte("sameoldchat/csrf\x00" + sessionToken))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func CSRFCookie(value string, maxAge int, domain string) *http.Cookie {
	return &http.Cookie{Name: CSRFTokenCookieName, Value: value, Domain: strings.TrimSpace(domain), Path: "/", MaxAge: maxAge, Secure: true, SameSite: http.SameSiteLaxMode}
}

func ValidateCSRF(r *http.Request) error {
	session, err := r.Cookie(SessionCookieName)
	if err != nil || strings.TrimSpace(session.Value) == "" {
		return ErrNotAuthenticated
	}
	cookie, err := r.Cookie(CSRFTokenCookieName)
	if err != nil || cookie.Value == "" {
		return errors.New("CSRF token is missing")
	}
	provided := strings.TrimSpace(r.Header.Get(CSRFTokenHeaderName))
	if provided == "" {
		provided = strings.TrimSpace(r.FormValue(CSRFTokenFieldName))
	}
	expected := CSRFToken(session.Value)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 || cookie.Value != expected {
		return errors.New("CSRF token is invalid")
	}
	return nil
}

func AllScopes() []string {
	return []string{string(ScopeChatWrite), string(ScopeChannelsHistory), string(ScopeUsersRead), string(ScopeUsersReadEmail), string(ScopeUsersWrite), string(ScopeUsersProfileWrite), string(ScopeChannelsRead), string(ScopeChannelsManage), string(ScopeReactionsWrite), string(ScopeReactionsRead), string(ScopePinsWrite), string(ScopePinsRead), string(ScopeBookmarksRead), string(ScopeBookmarksWrite), string(ScopeSearchRead), string(ScopeFilesWrite), string(ScopeFilesRead), string(ScopeRemoteFilesRead), string(ScopeRemoteFilesWrite), string(ScopeRemoteFilesShare), string(ScopeCanvasesRead), string(ScopeCanvasesWrite), string(ScopeTeamRead), string(ScopeEmojiRead), string(ScopeIdentityBasic), string(ScopeRTMStream), string(ScopeConnectionsWrite), string(ScopeDNDRead), string(ScopeDNDWrite), string(ScopeStarsRead), string(ScopeStarsWrite), string(ScopeRemindersRead), string(ScopeRemindersWrite), string(ScopeUserGroupsRead), string(ScopeUserGroupsWrite), string(ScopeCallsRead), string(ScopeCallsWrite), string(ScopeWorkflowStepsExecute), string(ScopeTokensBasic), string(ScopeAdmin), string(ScopeAdminUsersRead), string(ScopeAdminUsersWrite), string(ScopeAdminConversationsRead), string(ScopeAdminConversationsWrite), string(ScopeAdminEmojiWrite), string(ScopeAdminUserGroupsRead), string(ScopeAdminUserGroupsWrite), string(ScopeAdminTeamsRead), string(ScopeAdminTeamsWrite), string(ScopeAdminInvitesRead), string(ScopeAdminInvitesWrite), string(ScopeAdminAppsRead), string(ScopeAdminAppsWrite)}
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
	if err != nil || record.Revoked || !record.ExpiresAt.After(time.Now().UTC()) || record.WorkspaceID == "" || record.UserID == "" {
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

func NewAppStored(store AppTokenStore) (AppStored, error) {
	if store == nil {
		return AppStored{}, errors.New("app stored authenticator requires an app token store")
	}
	return AppStored{store: store}, nil
}

func (s AppStored) Authenticate(r *http.Request) (Principal, error) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	record, err := s.store.LookupAppToken(r.Context(), token)
	if err != nil || record.Revoked || record.AppID == "" {
		return Principal{}, ErrNotAuthenticated
	}
	scopes := make(map[Scope]struct{}, len(record.Scopes))
	for _, scope := range record.Scopes {
		scopes[Scope(scope)] = struct{}{}
	}
	return Principal{AppID: record.AppID, Scopes: scopes}, nil
}

func (s Stored) Authenticate(r *http.Request) (Principal, error) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	record, err := s.store.LookupToken(r.Context(), token)
	if err != nil || record.Revoked || record.WorkspaceID == "" || record.UserID == "" {
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
