package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
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
	ScopeListsRead               Scope = "lists:read"
	ScopeListsWrite              Scope = "lists:write"
	ScopeTeamRead                Scope = "team:read"
	ScopeEmojiRead               Scope = "emoji:read"
	ScopeAuthorizationsRead      Scope = "authorizations:read"
	ScopeLinksWrite              Scope = "links:write"
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

// Authentication failures are four distinct outcomes, not one.
//
// The pinned Slack contract
// (specs/upstream/slack-api-specs/web-api/slack_web_openapi_v2.json) enumerates
// `not_authed` (87 operations), `invalid_auth` (88), `account_inactive` (86) and
// `token_revoked` (52) as separate members of the same `error` enum, so a caller
// is expected to be able to tell "you sent no credential" from "the credential
// you sent is not one of ours" from "it was withdrawn" from "the identity behind
// it is gone". Every authenticator here returned one ErrNotAuthenticated, so the
// transport could only ever answer `not_authed`, and a client holding a stale
// token was told it had sent none.
//
// ErrNotAuthenticated stays as the class every one of them wraps: internal/web
// keys its login redirect off it (internal/web/handler.go) and so does
// tests/load/session_test.go, and neither should have to enumerate causes to ask
// "is this request unauthenticated".
var (
	ErrNotAuthenticated = errors.New("not authenticated")

	// ErrNoToken is the absence of a credential: no Authorization header, no
	// `token` field, no session cookie. → `not_authed`.
	ErrNoToken = fmt.Errorf("%w: no credential was presented", ErrNotAuthenticated)

	// ErrInvalidToken is a credential this deployment cannot validate: unknown,
	// malformed, or (for a browser session) expired. → `invalid_auth`.
	ErrInvalidToken = fmt.Errorf("%w: credential is not valid", ErrNotAuthenticated)

	// ErrTokenRevoked is a credential this deployment issued and then withdrew.
	// → `token_revoked`.
	ErrTokenRevoked = fmt.Errorf("%w: credential has been revoked", ErrNotAuthenticated)

	// ErrAccountInactive is a valid, unrevoked credential whose identity no longer
	// exists or carries no authority: a token record with no workspace or user, an
	// app token with no app, a session with no scopes. → `account_inactive`.
	ErrAccountInactive = fmt.Errorf("%w: the account behind the credential is not active", ErrNotAuthenticated)

	ErrMissingScope = errors.New("missing scope")
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
	CSRFTokenFieldName  = "_csrf"
	CSRFTokenHeaderName = "X-SameOldChat-CSRF"
)

// CSRF failures are three distinct outcomes, and the caller has to be able to
// tell them apart: one is a request that never came from this site, one is a
// page that has been open longer than its session, and one is a request with no
// session at all. They lead to different answers for a person.
var (
	// ErrCSRFCrossSite is a cookie-authenticated mutation whose own browser
	// reports that it was not initiated by this origin.
	ErrCSRFCrossSite = errors.New("request did not originate from this site")

	// ErrCSRFToken is a missing or stale form token.
	ErrCSRFToken = errors.New("CSRF token is invalid")
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

// CSRFToken derives a per-session mutation token from the session secret. The
// derivation is one-way, so a token cannot be replayed as a session, and it is
// stable for the life of the session, so a page cannot expire before the
// credential it was rendered for.
//
// There is deliberately no companion cookie. A second copy of this value in a
// cookie proved nothing — the server derives the expected value from the
// session cookie of the same request, so comparing a cookie against it only
// detects a stale cookie, never an attacker — while a non-HttpOnly cookie
// carrying `Domain=<parent>` published the token to every sibling host in the
// deployment's domain, where `document.cookie` read it and forged a mutation
// that SameSite=Lax happily accepted as same-site.
func CSRFToken(sessionToken string) string {
	digest := sha256.Sum256([]byte("sameoldchat/csrf\x00" + sessionToken))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// MaxFormBody bounds a form-encoded request body. A size limit is only a limit
// if it is installed before anything parses the body: once ParseForm or
// ParseMultipartForm has run, Go has already buffered up to its own 32 MiB
// default and spilled the remainder to temporary files, and a MaxBytesReader
// installed afterwards has nothing left to bound. Every entry point in this
// package that reads a form field therefore installs the limit first.
const MaxFormBody = 4 << 20

// maxMultipartCredentialPrefix bounds how much of a multipart body may be
// buffered while locating the `token` field. The pinned Slack contract declares
// `token` as a formData field, so a multipart upload has to be able to
// authenticate from the body, but a caller must not be able to make the server
// hold a whole file part in memory by placing the credential after it.
const maxMultipartCredentialPrefix = 1 << 20

// maxCredentialField bounds a single credential form field.
const maxCredentialField = 4 << 10

// LimitFormBody installs the MaxFormBody cap on the request body. It is safe to
// call more than once: the innermost limit still wins.
func LimitFormBody(r *http.Request) {
	if r != nil && r.Body != nil {
		r.Body = http.MaxBytesReader(nil, r.Body, MaxFormBody)
	}
}

// ValidateCSRF is the precondition of every cookie-authenticated mutation. It
// answers two independent questions, and a request has to pass both.
//
//  1. Did the browser itself say this request came from this origin? Fetch
//     metadata is set by the user agent and cannot be written by page script, so
//     `Sec-Fetch-Site` is the only part of a cross-site request an attacker
//     cannot forge. Rejecting anything but `same-origin`/`none` closes the whole
//     class, including the sibling-subdomain case that SameSite=Lax permits
//     because it calls it same-site. `Origin` is the fallback for a user agent
//     that predates fetch metadata; a request carrying neither is not a browser
//     request and is left to the token below.
//  2. Does the request carry the token derived from its own session? That is
//     what stops a request forged by a client that sets its own headers.
func ValidateCSRF(r *http.Request) error {
	session, err := r.Cookie(SessionCookieName)
	if err != nil || strings.TrimSpace(session.Value) == "" {
		return ErrNoToken
	}
	if err := validateRequestSite(r); err != nil {
		return err
	}
	provided := strings.TrimSpace(r.Header.Get(CSRFTokenHeaderName))
	if provided == "" {
		// This is the first read of the body on the browser mutation path, so
		// the cap has to be installed here rather than in the handler that
		// decodes the form afterwards.
		LimitFormBody(r)
		provided = strings.TrimSpace(r.FormValue(CSRFTokenFieldName))
	}
	if !constantTimeEqual(provided, CSRFToken(session.Value)) {
		return ErrCSRFToken
	}
	return nil
}

// validateRequestSite refuses a request the browser reports as coming from
// anywhere but this exact origin.
//
// `same-site` is refused as firmly as `cross-site`: a sibling host under the
// deployment's cookie domain is same-site, sends the session cookie under
// SameSite=Lax, and is exactly the position an attacker occupies when a
// marketing subdomain or a dangling DNS record is compromised.
func validateRequestSite(r *http.Request) error {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "same-origin", "none":
		return nil
	case "":
		// No fetch metadata: fall through to Origin.
	default:
		return ErrCSRFCrossSite
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Neither header is present, so this is not a browser-originated
		// request. A browser sends at least one of them on every POST it makes.
		return nil
	}
	return validateOrigin(origin, r)
}

// validateOrigin compares a serialised origin, not just its host.
//
// Comparing parsed.Host alone accepted four values a browser never sends and an
// attacker can: "ftp://host" and "//host" (no scheme, or a scheme that is not a
// web origin), "http://host" against a TLS request (a downgrade), and
// "https://user@host" (userinfo, which is not part of an origin at all and
// hides the real authority from a reader). None of them was a bypass — the
// token still gates — but each one turned a value the specification calls
// malformed into a pass.
func validateOrigin(origin string, r *http.Request) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return ErrCSRFCrossSite
	}
	// RFC 6454 serialises an origin as scheme "://" host [":" port] and nothing
	// else, so any path, query, or fragment means this is not an origin.
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return ErrCSRFCrossSite
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		// A request this process itself terminated over TLS cannot have come
		// from an http origin on the same host.
		if r.TLS != nil {
			return ErrCSRFCrossSite
		}
	default:
		return ErrCSRFCrossSite
	}
	if !strings.EqualFold(parsed.Host, r.Host) {
		return ErrCSRFCrossSite
	}
	return nil
}

// constantTimeEqual compares two secrets without leaking their contents through
// timing. The length check is deliberately outside the constant-time compare:
// ConstantTimeCompare returns 0 for unequal lengths, and the length of a CSRF
// token is not secret.
func constantTimeEqual(provided, expected string) bool {
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func multipartBoundary(r *http.Request) string {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return ""
	}
	return parameters["boundary"]
}

// requestToken reads the bearer credential from the request.
//
// `token` legitimately arrives as a multipart form field on the upload methods,
// and parsing the form there would consume the stream the handler still needs:
// ParseMultipartForm sets Request.MultipartForm, after which MultipartReader
// refuses to run and the upload can never be read. The multipart case is
// therefore scanned through a bounded, replayable prefix so the body the
// handler sees is byte-for-byte the body that arrived.
func requestToken(r *http.Request) string {
	if token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); token != "" {
		return token
	}
	if boundary := multipartBoundary(r); boundary != "" && r.Body != nil && r.MultipartForm == nil {
		return multipartToken(r, boundary)
	}
	LimitFormBody(r)
	return strings.TrimSpace(r.FormValue("token"))
}

// replayBody re-serves bytes that were consumed while locating the credential,
// followed by the untouched remainder of the stream.
type replayBody struct {
	io.Reader
	closer io.Closer
}

func (b replayBody) Close() error { return b.closer.Close() }

func multipartToken(r *http.Request, boundary string) string {
	body := r.Body
	var consumed bytes.Buffer
	reader := multipart.NewReader(io.TeeReader(io.LimitReader(body, maxMultipartCredentialPrefix), &consumed), boundary)
	token := ""
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		if part.FormName() != "token" || part.FileName() != "" {
			_ = part.Close()
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(part, maxCredentialField+1))
		_ = part.Close()
		if readErr == nil && len(value) <= maxCredentialField {
			token = strings.TrimSpace(string(value))
		}
		break
	}
	r.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(consumed.Bytes()), body), closer: body}
	return token
}

// allScopes is every scope this deployment can grant. It is declared one scope
// per line because the previous single-line literal is how `admin.emoji:write` —
// a scope Slack does not define — survived here with no caller for as long as it
// did: nothing about a 55-element line makes a wrong member visible.
//
// Membership is grounded in the pinned contract: each entry is a scope named by
// some operation's `token` parameter in
// specs/upstream/slack-api-specs/web-api/slack_web_openapi_v2.json.
var allScopes = []Scope{
	ScopeChatWrite,
	ScopeChannelsHistory,
	ScopeUsersRead,
	ScopeUsersReadEmail,
	ScopeUsersWrite,
	ScopeUsersProfileWrite,
	ScopeChannelsRead,
	ScopeChannelsManage,
	ScopeReactionsWrite,
	ScopeReactionsRead,
	ScopePinsWrite,
	ScopePinsRead,
	ScopeBookmarksRead,
	ScopeBookmarksWrite,
	ScopeSearchRead,
	ScopeFilesWrite,
	ScopeFilesRead,
	ScopeRemoteFilesRead,
	ScopeRemoteFilesWrite,
	ScopeRemoteFilesShare,
	ScopeCanvasesRead,
	ScopeCanvasesWrite,
	ScopeListsRead,
	ScopeListsWrite,
	ScopeTeamRead,
	ScopeEmojiRead,
	ScopeAuthorizationsRead,
	ScopeLinksWrite,
	ScopeIdentityBasic,
	ScopeRTMStream,
	ScopeConnectionsWrite,
	ScopeDNDRead,
	ScopeDNDWrite,
	ScopeStarsRead,
	ScopeStarsWrite,
	ScopeRemindersRead,
	ScopeRemindersWrite,
	ScopeUserGroupsRead,
	ScopeUserGroupsWrite,
	ScopeCallsRead,
	ScopeCallsWrite,
	ScopeWorkflowStepsExecute,
	ScopeTokensBasic,
	ScopeAdmin,
	ScopeAdminUsersRead,
	ScopeAdminUsersWrite,
	ScopeAdminConversationsRead,
	ScopeAdminConversationsWrite,
	ScopeAdminUserGroupsRead,
	ScopeAdminUserGroupsWrite,
	ScopeAdminTeamsRead,
	ScopeAdminTeamsWrite,
	ScopeAdminInvitesRead,
	ScopeAdminInvitesWrite,
	ScopeAdminAppsRead,
	ScopeAdminAppsWrite,
}

// AllScopes returns a fresh copy on every call, so a caller that appends to or
// writes into the result cannot widen the authority of the next caller.
func AllScopes() []string {
	values := make([]string, len(allScopes))
	for index, scope := range allScopes {
		values[index] = string(scope)
	}
	return values
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
		return Principal{}, ErrNoToken
	}
	record, err := b.store.LookupSession(r.Context(), cookie.Value)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	if record.Revoked {
		return Principal{}, ErrTokenRevoked
	}
	// An expired session is a credential that can no longer be validated rather
	// than one that was withdrawn, and the pinned enums declare no expiry code of
	// their own, so it joins ErrInvalidToken.
	if !record.ExpiresAt.After(time.Now().UTC()) {
		return Principal{}, ErrInvalidToken
	}
	if record.WorkspaceID == "" || record.UserID == "" {
		return Principal{}, ErrAccountInactive
	}
	scopes := make(map[Scope]struct{}, len(record.Scopes))
	for _, scope := range record.Scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes[Scope(scope)] = struct{}{}
		}
	}
	if len(scopes) == 0 {
		return Principal{}, ErrAccountInactive
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
	token := requestToken(r)
	if token == "" {
		return Principal{}, ErrNoToken
	}
	record, err := s.store.LookupAppToken(r.Context(), token)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	if record.Revoked {
		return Principal{}, ErrTokenRevoked
	}
	if record.AppID == "" {
		return Principal{}, ErrAccountInactive
	}
	scopes := make(map[Scope]struct{}, len(record.Scopes))
	for _, scope := range record.Scopes {
		scopes[Scope(scope)] = struct{}{}
	}
	return Principal{AppID: record.AppID, Scopes: scopes}, nil
}

// Authenticate resolves a bearer token to its principal.
//
// A lookup failure is reported as ErrInvalidToken rather than as a dependency
// failure: the store contract for an unknown token is an error, and the
// authenticator cannot tell that apart from an outage without importing the
// store's sentinels. See the follow-up recorded for TokenStore, which should
// distinguish "no such token" from "the token store is unreachable" so an outage
// stops looking like a bad credential.
func (s Stored) Authenticate(r *http.Request) (Principal, error) {
	token := requestToken(r)
	if token == "" {
		return Principal{}, ErrNoToken
	}
	record, err := s.store.LookupToken(r.Context(), token)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	if record.Revoked {
		return Principal{}, ErrTokenRevoked
	}
	if record.WorkspaceID == "" || record.UserID == "" {
		return Principal{}, ErrAccountInactive
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
	token := requestToken(r)
	if token == "" {
		return Principal{}, ErrNoToken
	}
	if !constantTimeEqual(token, s.token) {
		return Principal{}, ErrInvalidToken
	}
	return s.principal, nil
}
