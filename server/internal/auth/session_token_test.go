package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestGenerateSessionToken(t *testing.T) {
	rawToken, storedHash, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() error = %v", err)
	}

	if strings.Contains(rawToken.Value(), "=") {
		t.Fatalf("raw token %q contains base64 padding", rawToken.Value())
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(rawToken.Value())
	if err != nil {
		t.Fatalf("decode raw token: %v", err)
	}
	if len(decoded) != sessionTokenByteLength {
		t.Fatalf("decoded token length = %d; want %d", len(decoded), sessionTokenByteLength)
	}

	wantHash := sha256.Sum256([]byte(rawToken.Value()))
	if !reflect.DeepEqual(storedHash.Bytes(), wantHash[:]) {
		t.Fatalf("stored hash does not match SHA-256 of raw token")
	}
	if !reflect.DeepEqual(HashSessionToken(rawToken).Bytes(), storedHash.Bytes()) {
		t.Fatal("HashSessionToken() does not reproduce generated hash")
	}

	parsed, err := ParseSessionToken(rawToken.Value())
	if err != nil {
		t.Fatalf("ParseSessionToken() error = %v", err)
	}
	if parsed.Value() != rawToken.Value() {
		t.Fatal("parsed token does not preserve raw value")
	}
}

func TestRawSessionTokenFormattingIsRedacted(t *testing.T) {
	rawToken, _, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() error = %v", err)
	}

	if got := fmt.Sprint(rawToken); got != "[REDACTED]" {
		t.Fatalf("formatted raw token = %q; want redaction", got)
	}
	if got := fmt.Sprintf("%#v", rawToken); strings.Contains(got, rawToken.Value()) {
		t.Fatal("Go-syntax formatting exposed raw session token")
	}
}

func TestParseSessionTokenRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "not-a-token", strings.Repeat("A", 42), strings.Repeat("A", 44) + "="} {
		if _, err := ParseSessionToken(value); !errors.Is(err, ErrInvalidSessionToken) {
			t.Fatalf("ParseSessionToken(%q) error = %v; want ErrInvalidSessionToken", value, err)
		}
	}
}
