package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// BeginShortTokenRotation starts the one-time rotation of a short-secret user
// token (oauth.v2.beginShortTokenRotation). The replacement it returns is the
// original with a 32-character secret; it does not authenticate until
// CompleteShortTokenRotation confirms it within
// domain.ShortTokenRotationWindow, and the original keeps working until then.
// Beginning again replaces the pending replacement.
//
// Eligibility is the test Slack's reference gives: the secret, the section
// after the token's final "-", is shorter than 32 characters. The tokens this
// deployment issues (xoxp- and 20 hexadecimal characters) meet it, so each may
// be rotated once to a 32-character secret; a token already rotated answers
// domain.ErrTokenSecretTooLong.
func (m Messages) BeginShortTokenRotation(ctx context.Context, clientID, clientSecret, token string) (string, error) {
	token = strings.TrimSpace(token)
	client, err := m.shortTokenRotationClient(ctx, clientID, clientSecret, token)
	if err != nil {
		return "", err
	}
	replacement, err := domain.LengthenTokenSecret(token)
	if err != nil {
		return "", err
	}
	rotation := domain.ShortTokenRotation{
		TokenHash: domain.HashToken(token), NewTokenHash: domain.HashToken(replacement),
		AppID: client.AppID, ExpiresAt: time.Now().UTC().Add(domain.ShortTokenRotationWindow),
	}
	if err := m.Store.BeginShortTokenRotation(ctx, rotation); err != nil {
		return "", err
	}
	return replacement, nil
}

// CompleteShortTokenRotation finishes a rotation BeginShortTokenRotation
// started (oauth.v2.completeShortTokenRotation): the original token stops
// authenticating and the replacement, which it returns, takes its place with
// the same workspace, user, app and scopes.
func (m Messages) CompleteShortTokenRotation(ctx context.Context, clientID, clientSecret, token, newToken string) (string, error) {
	token, newToken = strings.TrimSpace(token), strings.TrimSpace(newToken)
	client, err := m.shortTokenRotationClient(ctx, clientID, clientSecret, token)
	if err != nil {
		return "", err
	}
	if newToken == "" {
		return "", domain.ErrInvalidOAuth
	}
	if err := m.Store.CompleteShortTokenRotation(ctx, domain.HashToken(token), domain.HashToken(newToken), client.AppID, time.Now().UTC()); err != nil {
		return "", err
	}
	return newToken, nil
}

// shortTokenRotationClient proves the OAuth client and decides whether the
// token is one it may rotate, in the order the rotation's codes are told
// apart: the client before the token, because a caller who cannot prove the
// app has no business learning anything about the token.
func (m Messages) shortTokenRotationClient(ctx context.Context, clientID, clientSecret, token string) (domain.OAuthClient, error) {
	clientID, clientSecret = strings.TrimSpace(clientID), strings.TrimSpace(clientSecret)
	if token == "" {
		return domain.OAuthClient{}, domain.ErrInvalidOAuth
	}
	if clientID == "" {
		return domain.OAuthClient{}, domain.ErrInvalidOAuthClient
	}
	client, err := m.Store.GetOAuthClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OAuthClient{}, domain.ErrInvalidOAuthClient
		}
		return domain.OAuthClient{}, err
	}
	if !secretDigestsEqual(client.SecretHash, domain.HashToken(clientSecret)) {
		return domain.OAuthClient{}, domain.ErrBadOAuthClientSecret
	}
	record, err := m.Store.LookupToken(ctx, token)
	if err != nil {
		return domain.OAuthClient{}, err
	}
	if record.Revoked || (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(time.Now().UTC())) {
		return domain.OAuthClient{}, store.ErrNotFound
	}
	// The client_id "must be the app the token being rotated was issued to".
	if record.AppID != client.AppID {
		return domain.OAuthClient{}, domain.ErrOAuthAppMismatch
	}
	// Only a plain, non-expiring user token is "the xoxp token being
	// rotated": a bot token, an execution-scoped token and an expiring
	// rotating token (xoxe.xoxp-) are other credentials.
	if record.TokenType != domain.TokenUser || record.FunctionExecutionID != "" || !record.ExpiresAt.IsZero() || !strings.HasPrefix(token, domain.ShortTokenRotationPrefix) {
		return domain.OAuthClient{}, domain.ErrTokenTypeNotRotatable
	}
	if !domain.HasShortSecret(token) {
		return domain.OAuthClient{}, domain.ErrTokenSecretTooLong
	}
	return client, nil
}
