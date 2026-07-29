package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestStaticAuthenticatorReturnsTypedPrincipal(t *testing.T) {
	principal := Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[Scope]struct{}{ScopeChatWrite: {}}}
	authenticator, err := NewStatic("token", principal)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	got, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != domain.WorkspaceID("T1") || got.UserID != domain.UserID("U1") || !got.HasScope(ScopeChatWrite) {
		t.Fatalf("principal = %+v", got)
	}
}

func TestStaticAuthenticatorRejectsWrongToken(t *testing.T) {
	authenticator, err := NewStatic("token", Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	// A credential that was presented and rejected is ErrInvalidToken, not the
	// generic class: the transport answers `invalid_auth` for it and `not_authed`
	// only for the absence of a credential.
	if _, err := authenticator.Authenticate(request); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
	if _, err := authenticator.Authenticate(httptest.NewRequest("POST", "/", nil)); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no credential: err = %v, want ErrNoToken", err)
	}
}

func TestStoredAuthenticatorUsesPersistedScopes(t *testing.T) {
	store := memory.New()
	store.SeedToken(context.Background(), "token", domain.TokenRecord{
		WorkspaceID: "T1",
		UserID:      "U1",
		AppID:       "A1",
		BotID:       "B1",
		TokenType:   "bot",
		Scopes:      []string{string(ScopeChannelsHistory)},
	})
	authenticator, err := NewStored(store)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	principal, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.HasScope(ScopeChannelsHistory) || principal.HasScope(ScopeChatWrite) {
		t.Fatalf("principal = %+v", principal)
	}
	if principal.AppID != "A1" || principal.BotID != "B1" || principal.TokenType != "bot" {
		t.Fatalf("principal lost app/bot identity: %+v", principal)
	}
	if principal.CredentialHash != domain.HashToken("token") || strings.Contains(principal.CredentialHash, "token") {
		t.Fatalf("principal credential identity is not the one-way token hash: %+v", principal)
	}
}

func TestStoredAuthenticatorDistinguishesExpiredToken(t *testing.T) {
	store := memory.New()
	store.SeedToken(context.Background(), "expired", domain.TokenRecord{
		WorkspaceID: "T1",
		UserID:      "U1",
		ExpiresAt:   time.Now().UTC().Add(-time.Second),
	})
	authenticator, err := NewStored(store)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer expired")
	if _, err := authenticator.Authenticate(request); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

func TestBrowserAuthenticatorUsesPersistedScopes(t *testing.T) {
	store := memory.New()
	if err := store.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeChannelsHistory)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	principal, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.HasScope(ScopeChannelsHistory) || principal.HasScope(ScopeChatWrite) {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestSessionCookieDomainIsExplicitAndScoped(t *testing.T) {
	for _, domain := range []string{"example.com", "apps.example.com"} {
		if err := ValidateSessionCookieDomain(domain); err != nil {
			t.Fatalf("validate %q: %v", domain, err)
		}
		cookie := SessionCookie("session", 86400, domain)
		if cookie.Domain != domain || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("cookie = %+v", cookie)
		}
	}
	for _, domain := range []string{".example.com", "https://example.com", "example.com:443", "127.0.0.1"} {
		if err := ValidateSessionCookieDomain(domain); err == nil {
			t.Fatalf("domain %q was accepted", domain)
		}
	}
}

func TestValidateCSRFRequiresSessionBoundToken(t *testing.T) {
	token := CSRFToken("session")
	request := httptest.NewRequest(http.MethodPost, "/app/message", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	request.Header.Set(CSRFTokenHeaderName, token)
	if err := ValidateCSRF(request); err != nil {
		t.Fatalf("valid CSRF token rejected: %v", err)
	}
	request.Header.Set(CSRFTokenHeaderName, CSRFToken("other-session"))
	if err := ValidateCSRF(request); err == nil {
		t.Fatal("CSRF token for another session was accepted")
	}
}

// TestValidateCSRFRefusesRequestsTheBrowserReportsAsForeign covers the defect
// the deleted CSRF cookie left open. The cookie copy of the token proved
// nothing — the server derives the expected value from the session cookie of
// the same request — while `Domain=<parent>` published a script-readable token
// to every sibling host. A page on such a host held a valid token and a
// SameSite=Lax session cookie, so nothing refused its mutation.
//
// Fetch metadata is written by the user agent and cannot be set by page script,
// so it is the part of the request the attacker does not control.
func TestValidateCSRFRefusesRequestsTheBrowserReportsAsForeign(t *testing.T) {
	token := CSRFToken("session")
	forged := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/app/message", nil)
		request.Host = "chat.example.test"
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
		// The attacker holds the real token: this is the whole point.
		request.Header.Set(CSRFTokenHeaderName, token)
		return request
	}

	for name, apply := range map[string]func(*http.Request){
		"sibling subdomain": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
			r.Header.Set("Origin", "https://evil.example.test")
		},
		"unrelated origin": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Origin", "https://evil.example")
		},
		"sibling subdomain without fetch metadata": func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example.test")
		},
		"opaque origin": func(r *http.Request) {
			r.Header.Set("Origin", "null")
		},
		// Four values that compare equal on Host alone and are not this
		// origin. Each one was accepted while only parsed.Host was compared.
		"scheme-relative origin": func(r *http.Request) {
			r.Header.Set("Origin", "//chat.example.test")
		},
		"non-web scheme": func(r *http.Request) {
			r.Header.Set("Origin", "ftp://chat.example.test")
		},
		"userinfo hides the authority": func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example@chat.example.test")
		},
		"origin carrying a path": func(r *http.Request) {
			r.Header.Set("Origin", "https://chat.example.test/evil")
		},
	} {
		request := forged()
		apply(request)
		if err := ValidateCSRF(request); !errors.Is(err, ErrCSRFCrossSite) {
			t.Fatalf("%s: err=%v, want ErrCSRFCrossSite", name, err)
		}
	}

	for name, apply := range map[string]func(*http.Request){
		"same origin": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", "https://chat.example.test")
		},
		"user initiated": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") },
		"same origin without fetch metadata": func(r *http.Request) {
			r.Header.Set("Origin", "https://chat.example.test")
		},
		"no browser metadata at all": func(*http.Request) {},
		// A deployment this process serves in the clear really does have an
		// http origin, and refusing it would lock out every client on the
		// Origin path.
		"plaintext deployment": func(r *http.Request) {
			r.Header.Set("Origin", "http://chat.example.test")
		},
	} {
		request := forged()
		apply(request)
		if err := ValidateCSRF(request); err != nil {
			t.Fatalf("%s: rejected a request from this origin: %v", name, err)
		}
	}

	// The same http origin against a request this process terminated over TLS
	// is a downgrade and is refused.
	downgraded := forged()
	downgraded.TLS = &tls.ConnectionState{}
	downgraded.Header.Set("Origin", "http://chat.example.test")
	if err := ValidateCSRF(downgraded); !errors.Is(err, ErrCSRFCrossSite) {
		t.Fatalf("a scheme downgrade against a TLS request was accepted: %v", err)
	}
}

func TestScopesForWorkspaceRoleKeepsTheControlPlaneAwayFromMembers(t *testing.T) {
	member, err := ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range member.Values() {
		if IsControlPlaneScope(Scope(scope)) {
			t.Fatalf("member scopes contain the control-plane scope %q", scope)
		}
	}
	for _, required := range []Scope{ScopeChatWrite, ScopeChannelsHistory, ScopeUsersRead, ScopeSearchRead, ScopeFilesWrite} {
		if !member.Has(required) {
			t.Fatalf("member scopes lost %q", required)
		}
	}
	for _, forbidden := range []Scope{ScopeAdmin, ScopeAdminUsersRead, ScopeAdminUsersWrite, ScopeAdminTeamsWrite, ScopeAdminAppsWrite} {
		if member.Has(forbidden) {
			t.Fatalf("member scopes contain %q", forbidden)
		}
	}
	for _, role := range []domain.WorkspaceRole{domain.WorkspaceRoleAdmin, domain.WorkspaceRoleOwner} {
		privileged, err := ScopesForWorkspaceRole(role)
		if err != nil {
			t.Fatal(err)
		}
		if len(privileged.Values()) != len(AllScopes()) {
			t.Fatalf("%s scopes=%d, want every scope (%d)", role, len(privileged.Values()), len(AllScopes()))
		}
		if !privileged.Has(ScopeAdminUsersWrite) || privileged.Role() != role {
			t.Fatalf("%s scopes=%+v", role, privileged)
		}
	}
	// The two sets must differ by exactly the control-plane scopes, so a scope added
	// to this package lands in one set or the other without a third edit.
	control := 0
	for _, scope := range AllScopes() {
		if IsControlPlaneScope(Scope(scope)) {
			control++
		}
	}
	if control == 0 || len(AllScopes())-len(member.Values()) != control {
		t.Fatalf("control-plane scopes=%d, member deficit=%d", control, len(AllScopes())-len(member.Values()))
	}
	for _, unsupported := range []domain.WorkspaceRole{"", "guest", "Admin", "root"} {
		if _, err := ScopesForWorkspaceRole(unsupported); err == nil {
			t.Fatalf("role %q was granted session authority", unsupported)
		}
	}
	if !WorkspaceRoleHoldsControlPlane(domain.WorkspaceRoleAdmin) || !WorkspaceRoleHoldsControlPlane(domain.WorkspaceRoleOwner) || WorkspaceRoleHoldsControlPlane(domain.WorkspaceRoleMember) || WorkspaceRoleHoldsControlPlane("") {
		t.Fatal("control-plane role predicate is wrong")
	}
}

func TestSessionScopesValuesCannotBeMutatedThroughTheReturnedSlice(t *testing.T) {
	scopes, err := ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	values := scopes.Values()
	values[0] = string(ScopeAdminUsersWrite)
	if scopes.Has(ScopeAdminUsersWrite) {
		t.Fatal("a caller escalated a derived scope set by writing to the returned slice")
	}
}

// TestValidateCSRFBoundsTheBodyBeforeParsingIt covers the dead-limit defect: the 4 MiB
// cap used to be installed by the handler that decoded the form, which runs *after*
// ValidateCSRF has already triggered Go's 32 MiB default parse, so the cap bounded
// nothing.
func TestValidateCSRFBoundsTheBodyBeforeParsingIt(t *testing.T) {
	token := CSRFToken("session")
	body := CSRFTokenFieldName + "=" + token + "&pad=" + strings.Repeat("A", MaxFormBody+1)
	request := httptest.NewRequest(http.MethodPost, "/app/message", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	if err := ValidateCSRF(request); err == nil {
		t.Fatal("a form body over the limit was parsed and accepted")
	}
	if got := len(request.Form["pad"]); got != 0 {
		t.Fatalf("oversized field was buffered anyway: %d values", got)
	}

	// A body within the limit still authenticates through the form field, because the
	// shipped progressive-enhancement script posts _csrf in the body, not a header.
	within := httptest.NewRequest(http.MethodPost, "/app/message", strings.NewReader(CSRFTokenFieldName+"="+token+"&text=hello"))
	within.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	within.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	if err := ValidateCSRF(within); err != nil {
		t.Fatalf("valid in-body CSRF token rejected: %v", err)
	}
}

// TestStoredAuthenticatorReadsAMultipartTokenWithoutConsumingTheUpload covers the
// upload defect: the authenticator called r.FormValue, which runs
// ParseMultipartForm and sets Request.MultipartForm, after which the handler's
// r.MultipartReader() refuses to run and every token-in-formData upload fails. The
// pinned Slack contract declares `token` as a formData field, so this has to work.
func TestStoredAuthenticatorReadsAMultipartTokenWithoutConsumingTheUpload(t *testing.T) {
	store := memory.New()
	store.SeedToken(context.Background(), "xoxb-upload", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeFilesWrite)}})
	authenticator, err := NewStored(store)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("token", "xoxb-upload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("title", "Uploaded"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("payload bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())

	principal, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatalf("multipart token was not accepted: %v", err)
	}
	if principal.UserID != domain.UserID("U1") || !principal.HasScope(ScopeFilesWrite) {
		t.Fatalf("principal=%+v", principal)
	}

	// The handler must still be able to stream the very same body.
	if request.MultipartForm != nil {
		t.Fatal("authenticating consumed the multipart form")
	}
	reader, err := request.MultipartReader()
	if err != nil {
		t.Fatalf("upload stream was consumed by authentication: %v", err)
	}
	seen := map[string]string{}
	for {
		next, nextErr := reader.NextPart()
		if nextErr != nil {
			break
		}
		value, readErr := io.ReadAll(next)
		if readErr != nil {
			t.Fatal(readErr)
		}
		seen[next.FormName()] = string(value)
		_ = next.Close()
	}
	if seen["token"] != "xoxb-upload" || seen["title"] != "Uploaded" || seen["file"] != "payload bytes" {
		t.Fatalf("replayed multipart body=%v", seen)
	}
}

// TestStoredAuthenticatorDoesNotBufferAnUnboundedMultipartPrefix pins the bound on the
// replay buffer: a caller must not be able to make the server hold a whole file part
// in memory by placing the credential after it.
func TestStoredAuthenticatorDoesNotBufferAnUnboundedMultipartPrefix(t *testing.T) {
	store := memory.New()
	store.SeedToken(context.Background(), "xoxb-upload", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeFilesWrite)}})
	authenticator, err := NewStored(store)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "upload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("A"), maxMultipartCredentialPrefix+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("token", "xoxb-upload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if _, err := authenticator.Authenticate(request); !errors.Is(err, ErrNoToken) {
		t.Fatalf("a credential hidden behind an oversized part authenticated: %v", err)
	}
}

func TestStaticAuthenticatorRejectsATokenWithTheSamePrefix(t *testing.T) {
	authenticator, err := NewStatic("token", Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"toke", "tokens", "", "TOKEN"} {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("Authorization", "Bearer "+candidate)
		want := error(ErrInvalidToken)
		if candidate == "" {
			want = ErrNoToken
		}
		if _, err := authenticator.Authenticate(request); !errors.Is(err, want) {
			t.Fatalf("token %q was accepted or misclassified: %v", candidate, err)
		}
	}
}

// Every authenticator has to report *which* authentication failure occurred.
// Collapsing them onto one sentinel was why the Slack transport could only ever
// answer `not_authed`, a code that means "you sent no credential", for a token
// that was unknown, withdrawn, or bound to an identity that no longer exists —
// three outcomes the pinned contract enumerates separately.
func TestAuthenticatorsReportWhichAuthenticationFailureOccurred(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedToken(ctx, "revoked", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeChatWrite)}, Revoked: true})
	store.SeedToken(ctx, "unbound", domain.TokenRecord{Scopes: []string{string(ScopeChatWrite)}})
	store.SeedAppToken(ctx, "app-revoked", domain.AppTokenRecord{AppID: "A1", Revoked: true})
	if err := store.SeedSession(ctx, "revoked-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeChatWrite)}, ExpiresAt: time.Now().UTC().Add(time.Hour), Revoked: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSession(ctx, "expired-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeChatWrite)}, ExpiresAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSession(ctx, "scopeless-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tokens, err := NewStored(store)
	if err != nil {
		t.Fatal(err)
	}
	appTokens, err := NewAppStored(store)
	if err != nil {
		t.Fatal(err)
	}
	// The memory store refuses to persist an app token with no app, so the
	// "valid record, no identity behind it" branch needs a store that can produce
	// one. It is the shape a durable row acquires when the app is deleted out from
	// under the token.
	unboundApp, err := NewAppStored(unboundAppTokens{})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	bearer := func(token string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/api/auth.test", nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request
	}
	cookie := func(value string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/app", nil)
		if value != "" {
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: value})
		}
		return request
	}
	cases := []struct {
		name          string
		authenticator Authenticator
		request       *http.Request
		want          error
	}{
		{"token absent", tokens, bearer(""), ErrNoToken},
		{"token unknown", tokens, bearer("nonexistent"), ErrInvalidToken},
		{"token revoked", tokens, bearer("revoked"), ErrTokenRevoked},
		{"token unbound", tokens, bearer("unbound"), ErrAccountInactive},
		{"app token absent", appTokens, bearer(""), ErrNoToken},
		{"app token unknown", appTokens, bearer("nonexistent"), ErrInvalidToken},
		{"app token revoked", appTokens, bearer("app-revoked"), ErrTokenRevoked},
		{"app token unbound", unboundApp, bearer("app-unbound"), ErrAccountInactive},
		{"session absent", sessions, cookie(""), ErrNoToken},
		{"session unknown", sessions, cookie("nonexistent"), ErrInvalidToken},
		{"session revoked", sessions, cookie("revoked-session"), ErrTokenRevoked},
		{"session expired", sessions, cookie("expired-session"), ErrInvalidToken},
		{"session without scopes", sessions, cookie("scopeless-session"), ErrAccountInactive},
	}
	for _, testCase := range cases {
		_, err := testCase.authenticator.Authenticate(testCase.request)
		if !errors.Is(err, testCase.want) {
			t.Errorf("%s: err = %v, want %v", testCase.name, err, testCase.want)
			continue
		}
		// Every cause stays a member of the class, so a caller that only asks "is
		// this unauthenticated" — internal/web's login redirect does exactly that —
		// keeps working without enumerating causes.
		if !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("%s: err = %v does not wrap ErrNotAuthenticated", testCase.name, err)
		}
	}
	// The four causes must stay distinguishable from one another, or mapping them
	// to four different Slack codes is not possible.
	distinct := []error{ErrNoToken, ErrInvalidToken, ErrTokenRevoked, ErrAccountInactive}
	for _, one := range distinct {
		for _, other := range distinct {
			if one != other && errors.Is(one, other) {
				t.Errorf("%v is indistinguishable from %v", one, other)
			}
		}
	}
}

// AllScopes is the authority a deployment token can hold, so a member that Slack
// does not define is a grant nothing can ever satisfy — and a missing member is a
// scope no handler can enforce without denying every caller.
func TestAllScopesHoldsOnlyScopesTheContractDefines(t *testing.T) {
	present := make(map[string]struct{}, len(AllScopes()))
	for _, scope := range AllScopes() {
		if _, duplicate := present[scope]; duplicate {
			t.Errorf("AllScopes lists %q twice", scope)
		}
		present[scope] = struct{}{}
	}
	// admin.emoji:write is not a Slack scope. The pinned contract puts
	// admin.emoji.list on admin.teams:read and admin.emoji.add/addAlias/remove/rename
	// on admin.teams:write, and nothing referenced the constant.
	if _, ok := present["admin.emoji:write"]; ok {
		t.Error(`AllScopes still lists "admin.emoji:write", which no Slack operation requires`)
	}
	// Both of these are named by a pinned operation's token parameter:
	// apps.event.authorizations.list "Requires scope: `authorizations:read`" and
	// chat.unfurl "Requires scope: `links:write`". Neither existed, which is why
	// the two handlers enforced nothing and the wrong scope respectively.
	for _, required := range []Scope{ScopeAuthorizationsRead, ScopeLinksWrite} {
		if _, ok := present[string(required)]; !ok {
			t.Errorf("AllScopes is missing %q", required)
		}
	}
	// Neither addition is control plane by name shape, so both belong to a member
	// session as well as to an administrator's.
	member, err := ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []Scope{ScopeAuthorizationsRead, ScopeLinksWrite} {
		if IsControlPlaneScope(required) {
			t.Errorf("%q is classified as control plane", required)
		}
		if !member.Has(required) {
			t.Errorf("member session lost %q", required)
		}
	}
	// A caller must not be able to widen the next caller's authority through the
	// returned slice.
	first := AllScopes()
	first[0] = "tampered"
	for _, scope := range AllScopes() {
		if scope == "tampered" {
			t.Fatal("AllScopes returns a shared slice; writing to it changed the next call")
		}
	}
}

// unboundAppTokens returns a valid, unrevoked app token record that names no app.
type unboundAppTokens struct{}

func (unboundAppTokens) LookupAppToken(context.Context, string) (domain.AppTokenRecord, error) {
	return domain.AppTokenRecord{}, nil
}
