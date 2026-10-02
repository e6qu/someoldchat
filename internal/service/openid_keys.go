package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"math/big"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// openIDSigningKeyAssociatedData binds the sealed private key to its purpose,
// so no other ciphertext sealed under the app credential key opens as it.
const openIDSigningKeyAssociatedData = "openid:signing-key"

// openIDSigningKeyBits is the RSA modulus size. RS256 needs at least 2048.
const openIDSigningKeyBits = 2048

// OpenIDKeys is the public key set a relying party verifies Sign in with Slack
// ID tokens against, served at /openid/connect/keys.
func (m Messages) OpenIDKeys(ctx context.Context) ([]domain.OpenIDKey, error) {
	key, keyID, err := m.openIDSigner(ctx)
	if err != nil {
		return nil, err
	}
	modulus, exponent := openIDPublicKeyParts(&key.PublicKey)
	return []domain.OpenIDKey{{KeyID: keyID, Modulus: modulus, Exponent: exponent}}, nil
}

// openIDSigner returns the deployment's signing key, creating it on first use.
// The store keeps one key, so replicas that race to create it settle on the
// same one and every token any of them signs verifies against one key set.
func (m Messages) openIDSigner(ctx context.Context) (*rsa.PrivateKey, string, error) {
	if len(m.AppCredentialKey) != 32 {
		return nil, "", domain.ErrAppCredentialKeyUnavailable
	}
	stored, err := m.Store.OpenIDSigningKey(ctx)
	if errors.Is(err, store.ErrNotFound) {
		candidate, candidateErr := newOpenIDSigningKey(m.AppCredentialKey)
		if candidateErr != nil {
			return nil, "", candidateErr
		}
		stored, err = m.Store.EnsureOpenIDSigningKey(ctx, candidate)
	}
	if err != nil {
		return nil, "", err
	}
	encoded, err := secretbox.Open(m.AppCredentialKey, openIDSigningKeyAssociatedData, stored.PrivateKeyCiphertext)
	if err != nil {
		return nil, "", err
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, "", err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, "", errors.New("stored OpenID signing key is not an RSA key")
	}
	return key, stored.KeyID, nil
}

func newOpenIDSigningKey(sealKey []byte) (domain.OpenIDSigningKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, openIDSigningKeyBits)
	if err != nil {
		return domain.OpenIDSigningKey{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return domain.OpenIDSigningKey{}, err
	}
	sealed, err := secretbox.Seal(sealKey, openIDSigningKeyAssociatedData, base64.StdEncoding.EncodeToString(der))
	if err != nil {
		return domain.OpenIDSigningKey{}, err
	}
	return domain.OpenIDSigningKey{KeyID: openIDKeyThumbprint(&key.PublicKey), PrivateKeyCiphertext: sealed, CreatedAt: time.Now().UTC()}, nil
}

// openIDPublicKeyParts is the modulus and exponent as a JSON Web Key encodes
// them: big-endian, base64url without padding.
func openIDPublicKeyParts(key *rsa.PublicKey) (string, string) {
	encode := base64.RawURLEncoding.EncodeToString
	return encode(key.N.Bytes()), encode(big.NewInt(int64(key.E)).Bytes())
}

// openIDKeyThumbprint names a key by its RFC 7638 JWK thumbprint, so the id
// follows from the key itself and two replicas can never name one key apart.
func openIDKeyThumbprint(key *rsa.PublicKey) string {
	modulus, exponent := openIDPublicKeyParts(key)
	digest := sha256.Sum256([]byte(`{"e":"` + exponent + `","kty":"RSA","n":"` + modulus + `"}`))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
