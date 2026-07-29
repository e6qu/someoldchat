package secretbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestSealRoundTripBindsApplicationAndRejectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := Seal(key, "app:A1:signing-secret", "top-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "top-secret") {
		t.Fatalf("ciphertext contains plaintext: %q", sealed)
	}
	opened, err := Open(key, "app:A1:signing-secret", sealed)
	if err != nil || opened != "top-secret" {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	if _, err := Open(key, "app:A2:signing-secret", sealed); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("cross-app open error=%v", err)
	}
	parts := strings.Split(sealed, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload)
	if _, err := Open(key, "app:A1:signing-secret", tampered); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("tampered open error=%v", err)
	}
}

func TestParseKeyHexRequiresAES256KeyWithoutEchoingInput(t *testing.T) {
	key, err := ParseKeyHex(strings.Repeat("07", 32))
	if err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
	const malformed = "not-a-secret-key"
	if _, err := ParseKeyHex(malformed); err == nil || strings.Contains(err.Error(), malformed) {
		t.Fatalf("malformed key error=%v", err)
	}
}
