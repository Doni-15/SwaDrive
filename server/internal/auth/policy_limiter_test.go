package auth

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCanonicalizeUsernamePolicy(t *testing.T) {
	for input, want := range map[string]string{
		" Alice ":       "alice",
		"owner.name":    "owner.name",
		"user_name-123": "user_name-123",
	} {
		got, err := CanonicalizeUsername(input)
		if err != nil || got != want {
			t.Fatalf("CanonicalizeUsername(%q) = %q, %v; want %q", input, got, err, want)
		}
	}

	for _, input := range []string{"-alice", "alice-", "two words", "ümlaut", "a/b", strings.Repeat("a", MaximumUsernameLength+1)} {
		if _, err := CanonicalizeUsername(input); !errors.Is(err, ErrInvalidUsername) {
			t.Fatalf("CanonicalizeUsername(%q) error = %v; want ErrInvalidUsername", input, err)
		}
	}
}

func TestNewPasswordPolicyAllowsPassphrasesAndBoundsInput(t *testing.T) {
	if err := ValidateNewPassword("long passphrase works"); err != nil {
		t.Fatalf("ValidateNewPassword(passphrase) error = %v", err)
	}
	if err := ValidateNewPassword("short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("ValidateNewPassword(short) error = %v; want ErrInvalidPassword", err)
	}
	if err := ValidateNewPassword(strings.Repeat("a", MaximumPasswordBytes+1)); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("ValidateNewPassword(oversized) error = %v; want ErrInvalidPassword", err)
	}
}

func TestLoginLimiterBlocksClearsAndBoundsMemory(t *testing.T) {
	limiter := NewLoginLimiter(2)
	now := time.Unix(1_800_000_000, 0).UTC()
	for range AccountFailureLimit {
		limiter.RecordFailure("alice", "192.0.2.1", now)
	}
	retryAfter, blocked := limiter.Check("alice", "192.0.2.1", now)
	if !blocked || retryAfter != LoginBlockDuration {
		t.Fatalf("Check() = %v, %v; want blocked for %v", retryAfter, blocked, LoginBlockDuration)
	}
	if _, blocked := limiter.Check("alice", "192.0.2.2", now); blocked {
		t.Fatal("different peer IP was rate limited")
	}
	limiter.Clear("alice", "192.0.2.1")
	if _, blocked := limiter.Check("alice", "192.0.2.1", now); blocked {
		t.Fatal("Clear() did not remove failed-login state")
	}

	limiter.RecordFailure("one", "192.0.2.1", now)
	limiter.RecordFailure("two", "192.0.2.1", now.Add(time.Second))
	limiter.RecordFailure("three", "192.0.2.1", now.Add(2*time.Second))
	if len(limiter.accountEntries) > 2 || len(limiter.ipEntries) > 2 {
		t.Fatalf("limiter entry counts = %d, %d; want each bounded to 2", len(limiter.accountEntries), len(limiter.ipEntries))
	}
	limiter.Check("none", "192.0.2.1", now.Add(LoginBlockDuration+LoginFailureWindow+time.Minute))
	if len(limiter.accountEntries) != 0 || len(limiter.ipEntries) != 0 {
		t.Fatalf("stale limiter entries = %d, %d; want 0", len(limiter.accountEntries), len(limiter.ipEntries))
	}
}

func TestLoginLimiterBlocksUsernameSprayingPerIPWithoutBlockingPeers(t *testing.T) {
	limiter := NewLoginLimiter(100)
	now := time.Unix(1_800_000_000, 0).UTC()
	for attempt := range IPFailureLimit {
		limiter.RecordFailure("sprayed-"+strconv.Itoa(attempt), "192.0.2.20", now)
	}
	if _, blocked := limiter.Check("unused", "192.0.2.20", now); !blocked {
		t.Fatal("aggregate peer bucket did not block username spraying")
	}
	if _, blocked := limiter.Check("unused", "192.0.2.21", now); blocked {
		t.Fatal("independent peer was blocked by another peer's spray")
	}

	limiter.Clear("sprayed-0", "192.0.2.20")
	if _, blocked := limiter.Check("sprayed-0", "192.0.2.20", now); !blocked {
		t.Fatal("successful account login erased aggregate spray evidence")
	}
}
