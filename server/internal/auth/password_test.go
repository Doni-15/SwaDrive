package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	encodedHash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(encodedHash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("HashPassword() = %q; want encoded default parameters", encodedHash)
	}

	correct, err := VerifyPassword("correct horse battery staple", encodedHash)
	if err != nil {
		t.Fatalf("VerifyPassword(correct) error = %v", err)
	}
	if !correct {
		t.Fatal("VerifyPassword(correct) = false; want true")
	}

	correct, err = VerifyPassword("wrong password", encodedHash)
	if err != nil {
		t.Fatalf("VerifyPassword(wrong) error = %v", err)
	}
	if correct {
		t.Fatal("VerifyPassword(wrong) = true; want false")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	tests := []struct {
		name        string
		encodedHash string
	}{
		{name: "empty", encodedHash: ""},
		{name: "wrong algorithm", encodedHash: "$argon2i$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2g"},
		{name: "missing parameter", encodedHash: "$argon2id$v=19$m=65536,t=3$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2g"},
		{name: "excessive memory", encodedHash: "$argon2id$v=19$m=1048576,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2g"},
		{name: "invalid base64", encodedHash: "$argon2id$v=19$m=65536,t=3,p=2$not!base64$aGFzaGhhc2hoYXNoaGFzaGhhc2g"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			correct, err := VerifyPassword("password", tt.encodedHash)
			if correct {
				t.Fatal("VerifyPassword() = true; want false")
			}
			if !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("VerifyPassword() error = %v; want ErrInvalidPasswordHash", err)
			}
		})
	}
}
