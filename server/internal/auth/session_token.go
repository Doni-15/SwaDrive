package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const sessionTokenByteLength = 32

var ErrInvalidSessionToken = errors.New("invalid session token")

// RawSessionToken is the bearer credential returned only to the client. Its
// String methods redact the value to reduce accidental disclosure in logs.
type RawSessionToken struct {
	value string
}

func (token RawSessionToken) Value() string {
	return token.value
}

func (token RawSessionToken) String() string {
	return "[REDACTED]"
}

func (token RawSessionToken) GoString() string {
	return "auth.RawSessionToken{[REDACTED]}"
}

// SessionTokenHash is the SHA-256 value persisted by the server.
type SessionTokenHash struct {
	value [sha256.Size]byte
}

// Bytes returns a copy suitable for storing in SQLite as a BLOB.
func (hash SessionTokenHash) Bytes() []byte {
	value := make([]byte, len(hash.value))
	copy(value, hash.value[:])
	return value
}

// GenerateSessionToken creates a cryptographically random 256-bit bearer token
// and the SHA-256 value that should be stored in the database.
func GenerateSessionToken() (RawSessionToken, SessionTokenHash, error) {
	randomBytes := make([]byte, sessionTokenByteLength)
	if _, err := rand.Read(randomBytes); err != nil {
		return RawSessionToken{}, SessionTokenHash{}, fmt.Errorf("generate session token: %w", err)
	}

	rawToken := RawSessionToken{
		value: base64.RawURLEncoding.EncodeToString(randomBytes),
	}
	return rawToken, HashSessionToken(rawToken), nil
}

// ParseSessionToken validates the encoded bearer-token format used by SwaDrive.
func ParseSessionToken(value string) (RawSessionToken, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != sessionTokenByteLength {
		return RawSessionToken{}, ErrInvalidSessionToken
	}
	return RawSessionToken{value: value}, nil
}

// HashSessionToken derives the value used to look up a server-side session.
func HashSessionToken(rawToken RawSessionToken) SessionTokenHash {
	return SessionTokenHash{value: sha256.Sum256([]byte(rawToken.value))}
}
