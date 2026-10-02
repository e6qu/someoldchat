package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// LongTokenSecretLength is the secret length Slack has issued since August
// 2016. A token whose secret — the section after its final "-" — is shorter
// may be rotated once by oauth.v2.beginShortTokenRotation and
// oauth.v2.completeShortTokenRotation; one that is not answers token_too_long.
const LongTokenSecretLength = 32

// ShortTokenRotationWindow is how long a begun rotation may wait for its
// completion before it has to be started over.
const ShortTokenRotationWindow = 10 * time.Minute

// ShortTokenRotationPrefix is the one token form the rotation applies to: a
// user token as Slack writes it, xoxp-. A bot token, an expiring rotating
// token (xoxe.xoxp-) or any other credential is not an "xoxp token being
// rotated".
const ShortTokenRotationPrefix = "xoxp-"

// TokenSecret splits a token at its final "-" into the part a rotation keeps
// and the secret it replaces. A token with no "-" has no secret Slack's
// description can find.
func TokenSecret(token string) (kept, secret string, ok bool) {
	index := strings.LastIndexByte(token, '-')
	if index < 0 || index == len(token)-1 {
		return "", "", false
	}
	return token[:index+1], token[index+1:], true
}

// HasShortSecret reports whether a token's secret is shorter than the length
// Slack issues today, which is what makes it eligible for the one-time
// rotation.
func HasShortSecret(token string) bool {
	_, secret, ok := TokenSecret(token)
	return ok && len(secret) < LongTokenSecretLength
}

// LengthenTokenSecret is the replacement token a short-secret rotation
// issues: "exactly the same as the original, but with a longer secret". Every
// section before the secret is kept and the secret becomes 32 random
// hexadecimal characters (128 bits).
func LengthenTokenSecret(token string) (string, error) {
	kept, _, ok := TokenSecret(token)
	if !ok {
		return "", ErrTokenTypeNotRotatable
	}
	var secret [LongTokenSecretLength / 2]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", fmt.Errorf("generate token secret: %w", err)
	}
	return kept + hex.EncodeToString(secret[:]), nil
}

// ShortTokenRotation is a begun, not yet completed, short-secret rotation. It
// records hashes only: the replacement token is shown once, to the caller of
// oauth.v2.beginShortTokenRotation, exactly as an issued token is.
type ShortTokenRotation struct {
	TokenHash    string
	NewTokenHash string
	AppID        AppID
	ExpiresAt    time.Time
}
