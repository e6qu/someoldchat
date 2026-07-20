package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

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
		return domain.OpenIDToken{}, ErrInvalidOAuthClient
	}
	if grantType != "authorization_code" && grantType != "refresh_token" {
		return domain.OpenIDToken{}, ErrInvalidOAuth
	}
	if grantType == "refresh_token" {
		if refreshToken == "" || code != "" || codeVerifier != "" {
			return domain.OpenIDToken{}, ErrInvalidOAuth
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
		if err != nil || client.SecretHash != domain.HashToken(clientSecret) {
			return domain.OpenIDToken{}, ErrInvalidOAuthClient
		}
		token, err := m.Store.ExchangeOpenIDRefreshToken(ctx, clientID, refreshToken, accessToken, newRefreshToken, domain.OpenIDToken{OAuthToken: domain.OAuthToken{ClientID: clientID, AppID: client.AppID, TokenType: "Bearer"}})
		if errors.Is(err, store.ErrNotFound) {
			return domain.OpenIDToken{}, ErrInvalidOAuth
		}
		if err != nil {
			return domain.OpenIDToken{}, err
		}
		return m.finishOpenIDToken(ctx, clientSecret, token)
	}
	if code == "" || refreshToken != "" {
		return domain.OpenIDToken{}, ErrInvalidOAuth
	}
	oauthToken, err := m.oauthExchange(ctx, clientID, clientSecret, code, redirectURI, codeVerifier)
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	if !containsScope(oauthToken.Scopes, "openid") {
		return domain.OpenIDToken{}, ErrInvalidOAuth
	}
	newRefreshToken, err := domain.NewOAuthToken()
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	if err := m.Store.CreateOpenIDRefreshToken(ctx, domain.OpenIDRefreshToken{TokenHash: domain.HashToken(newRefreshToken), ClientID: clientID, WorkspaceID: oauthToken.WorkspaceID, UserID: oauthToken.UserID, Scopes: oauthToken.Scopes, ExpiresAt: time.Now().UTC().Add(openIDRefreshLifetime)}); err != nil {
		return domain.OpenIDToken{}, err
	}
	return m.finishOpenIDToken(ctx, clientSecret, domain.OpenIDToken{OAuthToken: oauthToken, RefreshToken: newRefreshToken, IDToken: ""})
}

func (m Messages) finishOpenIDToken(ctx context.Context, clientSecret string, token domain.OpenIDToken) (domain.OpenIDToken, error) {
	user, err := m.Store.GetUser(ctx, token.UserID)
	if err != nil || user.WorkspaceID != token.WorkspaceID || user.Deleted {
		return domain.OpenIDToken{}, store.ErrNotFound
	}
	workspace, err := m.Store.GetWorkspace(ctx, token.WorkspaceID)
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	idToken, err := signOpenIDToken(clientSecret, token.ClientID, user, workspace)
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
	return domain.OpenIDUserInfo{Subject: user.ID, UserID: user.ID, WorkspaceID: workspace.ID, Email: user.Email, EmailVerified: user.Email != "", Name: user.Name, TeamName: workspace.Name, TeamDomain: workspace.Domain, UserImages: map[string]string{"24": user.Profile.Image24, "32": user.Profile.Image32, "48": user.Profile.Image48, "72": user.Profile.Image72, "192": user.Profile.Image192, "512": user.Profile.Image512}, TeamImages: map[string]string{}, TeamImageDefault: workspace.IconURL == ""}, nil
}

func containsScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func signOpenIDToken(secret, clientID string, user domain.User, workspace domain.Workspace) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": "https://slack.com", "sub": string(user.ID), "aud": clientID, "iat": time.Now().UTC().Unix(), "exp": time.Now().UTC().Add(time.Hour).Unix(), "email": user.Email, "email_verified": user.Email != "", "name": user.Name, "https://slack.com/team_id": string(workspace.ID), "https://slack.com/user_id": string(user.ID)})
	if err != nil {
		return "", err
	}
	encode := func(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
	unsigned := encode(header) + "." + encode(claims)
	hash := hmac.New(sha256.New, []byte(secret))
	_, _ = hash.Write([]byte(unsigned))
	return unsigned + "." + encode(hash.Sum(nil)), nil
}
