package service

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// secretDigestsEqual compares two credential digests without leaking how many
// leading bytes matched. The digests are not secret in themselves, but the
// comparison runs once per token exchange against an attacker-supplied secret,
// so a data-dependent early exit is an oracle for forging a client secret one
// byte at a time.
func secretDigestsEqual(stored, presented string) bool {
	return hmac.Equal([]byte(stored), []byte(presented))
}

const openIDRefreshLifetime = 30 * 24 * time.Hour

func (m Messages) OpenIDConnectToken(ctx context.Context, clientID, clientSecret, code, redirectURI, grantType, refreshToken, codeVerifier string) (domain.OpenIDToken, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	code = strings.TrimSpace(code)
	grantType = strings.TrimSpace(grantType)
	refreshToken = strings.TrimSpace(refreshToken)
	codeVerifier = strings.TrimSpace(codeVerifier)
	if grantType == "" {
		grantType = "authorization_code"
	}
	if clientID == "" || clientSecret == "" {
		return domain.OpenIDToken{}, domain.ErrInvalidOAuthClient
	}
	if grantType != "authorization_code" && grantType != "refresh_token" {
		return domain.OpenIDToken{}, domain.ErrInvalidOAuth
	}
	if grantType == "refresh_token" {
		if refreshToken == "" || code != "" || codeVerifier != "" {
			return domain.OpenIDToken{}, domain.ErrInvalidOAuth
		}
		accessToken, err := domain.NewOAuthToken()
		if err != nil {
			return domain.OpenIDToken{}, err
		}
		newRefreshToken, err := domain.NewOAuthToken()
		if err != nil {
			return domain.OpenIDToken{}, err
		}
		client, err := m.Store.GetOAuthClient(ctx, clientID)
		if err != nil || !secretDigestsEqual(client.SecretHash, domain.HashToken(clientSecret)) {
			return domain.OpenIDToken{}, domain.ErrInvalidOAuthClient
		}
		token, err := m.Store.ExchangeOpenIDRefreshToken(ctx, clientID, refreshToken, accessToken, newRefreshToken, domain.OpenIDToken{OAuthToken: domain.OAuthToken{ClientID: clientID, AppID: client.AppID, TokenType: "Bearer"}})
		if errors.Is(err, store.ErrNotFound) {
			return domain.OpenIDToken{}, domain.ErrInvalidOAuth
		}
		if err != nil {
			return domain.OpenIDToken{}, err
		}
		return m.finishOpenIDToken(ctx, token)
	}
	if code == "" || refreshToken != "" {
		return domain.OpenIDToken{}, domain.ErrInvalidOAuth
	}
	oauthToken, err := m.oauthExchange(ctx, clientID, clientSecret, code, redirectURI, codeVerifier, "user", false)
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	if !containsScope(oauthToken.Scopes, "openid") {
		return domain.OpenIDToken{}, domain.ErrInvalidOAuth
	}
	newRefreshToken, err := domain.NewOAuthToken()
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	if err := m.Store.CreateOpenIDRefreshToken(ctx, domain.OpenIDRefreshToken{TokenHash: domain.HashToken(newRefreshToken), ClientID: clientID, WorkspaceID: oauthToken.WorkspaceID, UserID: oauthToken.UserID, Scopes: oauthToken.Scopes, ExpiresAt: time.Now().UTC().Add(openIDRefreshLifetime)}); err != nil {
		return domain.OpenIDToken{}, err
	}
	return m.finishOpenIDToken(ctx, domain.OpenIDToken{OAuthToken: oauthToken, RefreshToken: newRefreshToken, IDToken: ""})
}

func (m Messages) finishOpenIDToken(ctx context.Context, token domain.OpenIDToken) (domain.OpenIDToken, error) {
	user, err := m.Store.GetUser(ctx, token.UserID)
	if err != nil || user.WorkspaceID != token.WorkspaceID || user.Deleted {
		return domain.OpenIDToken{}, store.ErrNotFound
	}
	workspace, err := m.Store.GetWorkspace(ctx, token.WorkspaceID)
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	key, keyID, err := m.openIDSigner(ctx)
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	idToken, err := signOpenIDToken(key, keyID, slackobject.Origin(m.PublicURL), token.OAuthToken, openIDUserInfo(user, workspace), time.Now().UTC())
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	token.IDToken = idToken
	token.TokenType = "Bearer"
	return token, nil
}

func (m Messages) OpenIDConnectUserInfo(ctx context.Context, token string) (domain.OpenIDUserInfo, error) {
	record, err := m.Store.LookupToken(ctx, strings.TrimSpace(token))
	if err != nil || record.Revoked || !containsScope(record.Scopes, "openid") {
		return domain.OpenIDUserInfo{}, store.ErrNotFound
	}
	user, err := m.Store.GetUser(ctx, record.UserID)
	if err != nil || user.WorkspaceID != record.WorkspaceID || user.Deleted {
		return domain.OpenIDUserInfo{}, store.ErrNotFound
	}
	workspace, err := m.Store.GetWorkspace(ctx, record.WorkspaceID)
	if err != nil {
		return domain.OpenIDUserInfo{}, err
	}
	return openIDUserInfo(user, workspace), nil
}

// openIDUserInfo is what Sign in with Slack says about a member, in
// openid.connect.userInfo and in every ID token alike.
func openIDUserInfo(user domain.User, workspace domain.Workspace) domain.OpenIDUserInfo {
	return domain.OpenIDUserInfo{Subject: user.ID, UserID: user.ID, WorkspaceID: workspace.ID, Email: user.Email, EmailVerified: user.Email != "", Name: user.Name, TeamName: workspace.Name, TeamDomain: workspace.SlackDomain(), UserImages: map[string]string{"24": user.Profile.Image24, "32": user.Profile.Image32, "48": user.Profile.Image48, "72": user.Profile.Image72, "192": user.Profile.Image192, "512": user.Profile.Image512}, TeamImages: map[string]string{}, TeamImageDefault: workspace.IconURL == ""}
}

func containsScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

// signOpenIDToken signs the ID token openid.connect.token returns, RS256 with
// the deployment's key, named by kid so a relying party finds it in the key
// set at /openid/connect/keys. Its issuer is this deployment's public URL: a
// relying party that only changed Slack's endpoints to this deployment's
// validates iss against the issuer it discovered, never against slack.com.
// Without a public URL there is no issuer to name, so the claim is left out
// rather than impersonating Slack.
//
// nonce and auth_time come from the authorization the code was issued for,
// so a refreshed token carries neither; at_hash binds the token to the access
// token issued beside it, as OpenID Connect Core section 3.1.3.6 defines.
func signOpenIDToken(key *rsa.PrivateKey, keyID, issuer string, token domain.OAuthToken, info domain.OpenIDUserInfo, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": keyID})
	if err != nil {
		return "", err
	}
	values := info.Claims()
	values["aud"] = token.ClientID
	values["iat"] = now.Unix()
	values["exp"] = now.Add(time.Hour).Unix()
	if issuer != "" {
		values["iss"] = issuer
	}
	if token.Nonce != "" {
		values["nonce"] = token.Nonce
	}
	if !token.AuthorizedAt.IsZero() {
		values["auth_time"] = token.AuthorizedAt.Unix()
	}
	if token.AccessToken != "" {
		digest := sha256.Sum256([]byte(token.AccessToken))
		values["at_hash"] = base64.RawURLEncoding.EncodeToString(digest[:len(digest)/2])
	}
	claims, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + encode(signature), nil
}
