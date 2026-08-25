package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordHashAndVerify(t *testing.T) {
	manager, err := NewPasswordManager(2)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	encodedHash, err := manager.Hash(context.Background(), "correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(encodedHash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("HashPassword() = %q; want encoded default parameters", encodedHash)
	}

	correct, err := manager.Verify(context.Background(), "correct horse battery staple", encodedHash)
	if err != nil {
		t.Fatalf("VerifyPassword(correct) error = %v", err)
	}
	if !correct {
		t.Fatal("VerifyPassword(correct) = false; want true")
	}

	correct, err = manager.Verify(context.Background(), "wrong password", encodedHash)
	if err != nil {
		t.Fatalf("VerifyPassword(wrong) error = %v", err)
	}
	if correct {
		t.Fatal("VerifyPassword(wrong) = true; want false")
	}
}

func TestPasswordManagerBoundsConcurrentArgon2AndHonorsCancellation(t *testing.T) {
	manager, err := NewPasswordManager(2)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	manager.derive = func(_, _ []byte, _, _ uint32, _ uint8, keyLength uint32) []byte {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		return make([]byte, keyLength)
	}

	salt := base64.RawStdEncoding.EncodeToString(make([]byte, minimumSaltLength))
	hash := base64.RawStdEncoding.EncodeToString(make([]byte, minimumKeyLength))
	encoded := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s", salt, hash)

	const callers = 6
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsSeen := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			_, verifyErr := manager.Verify(context.Background(), "password", encoded)
			errorsSeen <- verifyErr
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("two Argon2 operations did not enter the gate")
		}
	}
	select {
	case <-started:
		t.Fatal("more than two Argon2 operations entered concurrently")
	case <-time.After(20 * time.Millisecond):
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Verify(cancelledContext, "password", encoded); !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify(cancelled) error = %v; want context.Canceled", err)
	}

	close(release)
	wait.Wait()
	close(errorsSeen)
	for verifyErr := range errorsSeen {
		if verifyErr != nil {
			t.Fatalf("Verify() error = %v", verifyErr)
		}
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrent derivations = %d; want 2", maximum.Load())
	}
}

func TestPasswordManagerRejectsWorkBeyondBoundedAdmissionQueue(t *testing.T) {
	manager, err := NewPasswordManager(1)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	for range cap(manager.admissions) {
		manager.admissions <- struct{}{}
	}
	parameters := DefaultPasswordParameters()
	if _, err := manager.deriveKey(context.Background(), []byte("password"), make([]byte, parameters.SaltLength), parameters); !errors.Is(err, ErrArgon2Busy) {
		t.Fatalf("deriveKey(full admission queue) error = %v; want ErrArgon2Busy", err)
	}
	for range cap(manager.admissions) {
		<-manager.admissions
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	manager, err := NewPasswordManager(2)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
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
			correct, err := manager.Verify(context.Background(), "password", tt.encodedHash)
			if correct {
				t.Fatal("VerifyPassword() = true; want false")
			}
			if !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("VerifyPassword() error = %v; want ErrInvalidPasswordHash", err)
			}
		})
	}
}

func BenchmarkArgon2idPasswordVerification(b *testing.B) {
	manager, err := NewPasswordManager(1)
	if err != nil {
		b.Fatalf("NewPasswordManager() error = %v", err)
	}
	encodedHash, err := manager.Hash(context.Background(), "benchmark password passphrase")
	if err != nil {
		b.Fatalf("Hash() error = %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		correct, verifyErr := manager.Verify(context.Background(), "benchmark password passphrase", encodedHash)
		if verifyErr != nil || !correct {
			b.Fatalf("Verify() = %v, %v", correct, verifyErr)
		}
	}
}
