package slack

import (
	"errors"
	"net/http"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/bearer"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// oauth.v2.beginShortTokenRotation and oauth.v2.completeShortTokenRotation
// rotate a short-secret xoxp token once to a 32-character secret. They need no
// scope: the token being rotated authenticates the request, in the
// Authorization header or as the token argument, and client_id and
// client_secret prove the app it was issued to. An unknown, revoked or expired
// token is answered by the ordinary authentication codes before the client is
// examined.
func (h Handler) oauthV2BeginShortTokenRotation(w http.ResponseWriter, r *http.Request) {
	fields, token, ok := h.shortTokenRotationRequest(w, r)
	if !ok {
		return
	}
	replacement, err := h.Messages.BeginShortTokenRotation(r.Context(), fields["client_id"], fields["client_secret"], token)
	if err != nil {
		writeError(w, shortTokenRotationFailure(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "new_token": replacement})
}

func (h Handler) oauthV2CompleteShortTokenRotation(w http.ResponseWriter, r *http.Request) {
	fields, token, ok := h.shortTokenRotationRequest(w, r)
	if !ok {
		return
	}
	live, err := h.Messages.CompleteShortTokenRotation(r.Context(), fields["client_id"], fields["client_secret"], token, fields["new_token"])
	if err != nil {
		writeError(w, shortTokenRotationFailure(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": live})
}

// shortTokenRotationRequest authenticates the token being rotated and returns
// it with the decoded arguments. The token is read from where the
// authenticator read it — the Authorization header first, the token argument
// otherwise — and checked against the credential the authenticator accepted,
// so the token rotated is exactly the token that authenticated.
func (h Handler) shortTokenRotationRequest(w http.ResponseWriter, r *http.Request) (map[string]string, string, bool) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return nil, "", false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return nil, "", false
	}
	token, fromHeader := bearer.Token(r.Header.Get("Authorization"))
	if !fromHeader {
		token = fields["token"]
	}
	if domain.HashToken(token) != principal.CredentialHash {
		writeAuthError(w, auth.ErrInvalidToken)
		return nil, "", false
	}
	return fields, token, true
}

// shortTokenRotationFailure names a rotation failure with the codes the two
// references declare.
func shortTokenRotationFailure(err error) string {
	switch {
	// invalid_client_id is both "Value passed for client_id was invalid" and
	// "not the app the token being rotated was issued to".
	case errors.Is(err, domain.ErrInvalidOAuthClient), errors.Is(err, domain.ErrOAuthAppMismatch):
		return "invalid_client_id"
	case errors.Is(err, domain.ErrBadOAuthClientSecret):
		return "bad_client_secret"
	case errors.Is(err, domain.ErrTokenTypeNotRotatable):
		return "not_allowed_token_type"
	case errors.Is(err, domain.ErrTokenSecretTooLong):
		return "token_too_long"
	case errors.Is(err, domain.ErrShortTokenRotationNotFound):
		return "rotation_not_found"
	case errors.Is(err, domain.ErrShortTokenRotationMismatch):
		return "invalid_token"
	case errors.Is(err, domain.ErrInvalidOAuth):
		return "invalid_arguments"
	// The token authenticated a moment ago and is gone or revoked now: a
	// concurrent rotation or revocation won the race.
	case errors.Is(err, store.ErrNotFound):
		return "invalid_auth"
	}
	return mapServiceErrorNamed(err, "invalid_auth", "invalid_arguments", "")
}
