package auth

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
)

const highVolumeBlockedRequests = 2_000

func TestBlockedLoginAuditGrowthIsBounded(t *testing.T) {
	t.Run("account transition and structurally invalid password", func(t *testing.T) {
		db := openAuthTestDatabase(t)
		now := time.Unix(1_800_000_000, 0).UTC()
		passwords := countingPasswordManager(t)
		var derivations atomic.Int32
		originalDerive := passwords.derive
		passwords.derive = func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte {
			derivations.Add(1)
			return originalDerive(password, salt, iterations, memory, parallelism, keyLength)
		}
		service := newRateLimitTestService(t, db, audit.NewService(audit.NewSQLiteRepository(db), func() time.Time { return now }), NewLoginLimiter(1_000), passwords, func() time.Time { return now })
		derivations.Store(0) // NewService creates the dummy hash once.

		input := invalidLoginInput("invalid username", "192.0.2.10")
		for attempt := range AccountFailureLimit {
			if _, err := service.Login(context.Background(), input); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("threshold attempt %d error = %v; want ErrInvalidCredentials", attempt+1, err)
			}
		}
		assertAuditCount(t, db, audit.EventLoginFailure, AccountFailureLimit)
		assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})
		t.Logf("after account block: blocked_requests=%d login_failure_rows=%d login_rate_limited_rows=1", highVolumeBlockedRequests, AccountFailureLimit)

		for range highVolumeBlockedRequests {
			assertRateLimited(t, service, input)
		}
		assertAuditCount(t, db, audit.EventLoginFailure, AccountFailureLimit)
		assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})
		if derivations.Load() != 0 {
			t.Fatalf("structurally invalid/blocked attempts performed %d Argon2 derivations; want 0", derivations.Load())
		}

		// An independent peer remains usable and cannot erase or duplicate the
		// first peer's block-transition evidence.
		independent := invalidLoginInput("invalid username", "192.0.2.11")
		if _, err := service.Login(context.Background(), independent); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("independent peer error = %v; want ErrInvalidCredentials", err)
		}
		assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})

		// The process-local policy starts a fresh failure window after expiry.
		now = now.Add(LoginBlockDuration + LoginFailureWindow + time.Second)
		if _, err := service.Login(context.Background(), input); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("post-expiry error = %v; want ErrInvalidCredentials", err)
		}
		assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})
	})

	t.Run("IP spray transition", func(t *testing.T) {
		db := openAuthTestDatabase(t)
		now := time.Unix(1_800_000_000, 0).UTC()
		service := newRateLimitTestService(t, db, audit.NewService(audit.NewSQLiteRepository(db), nil), NewLoginLimiter(1_000), countingPasswordManager(t), func() time.Time { return now })
		const remoteIP = "192.0.2.20"
		for attempt := range IPFailureLimit {
			input := invalidLoginInput("invalid username "+strconv.Itoa(attempt), remoteIP)
			if _, err := service.Login(context.Background(), input); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("spray attempt %d error = %v; want ErrInvalidCredentials", attempt+1, err)
			}
		}
		assertAuditCount(t, db, audit.EventLoginFailure, IPFailureLimit)
		assertRateLimitAuditReasons(t, db, map[string]int{"ip_rate_limit": 1})
		t.Logf("after IP block: blocked_requests=%d login_failure_rows=%d login_rate_limited_rows=1", highVolumeBlockedRequests, IPFailureLimit)

		blocked := invalidLoginInput("another invalid username", remoteIP)
		for range highVolumeBlockedRequests {
			assertRateLimited(t, service, blocked)
		}
		assertAuditCount(t, db, audit.EventLoginFailure, IPFailureLimit)
		assertRateLimitAuditReasons(t, db, map[string]int{"ip_rate_limit": 1})
	})
}

func TestConcurrentBlockedLoginAuditIsSuppressed(t *testing.T) {
	db := openAuthTestDatabase(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	service := newRateLimitTestService(t, db, audit.NewService(audit.NewSQLiteRepository(db), nil), NewLoginLimiter(1_000), countingPasswordManager(t), func() time.Time { return now })
	input := invalidLoginInput("invalid username", "192.0.2.30")
	for range AccountFailureLimit {
		_, _ = service.Login(context.Background(), input)
	}

	const workers = 64
	var wait sync.WaitGroup
	wait.Add(workers)
	errorsSeen := make(chan error, workers)
	for range workers {
		go func() {
			defer wait.Done()
			_, err := service.Login(context.Background(), input)
			var rateErr *RateLimitError
			if !errors.As(err, &rateErr) {
				errorsSeen <- err
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent blocked login error = %v; want RateLimitError", err)
	}
	assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})
}

func TestBlockAuditFailureDoesNotBypassOrRetry(t *testing.T) {
	db := openAuthTestDatabase(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	recorder := &selectiveAuditRecorder{failType: audit.EventLoginRateLimited}
	service := newRateLimitTestService(t, db, recorder, NewLoginLimiter(1_000), countingPasswordManager(t), func() time.Time { return now })
	input := invalidLoginInput("invalid username", "192.0.2.40")

	for attempt := 0; attempt < AccountFailureLimit-1; attempt++ {
		if _, err := service.Login(context.Background(), input); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("pre-threshold attempt %d error = %v", attempt+1, err)
		}
	}
	if _, err := service.Login(context.Background(), input); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("threshold audit-failure error = %v; want deterministic persistence error", err)
	}

	for range highVolumeBlockedRequests {
		assertRateLimited(t, service, input)
	}
	if got := recorder.count(audit.EventLoginFailure); got != AccountFailureLimit {
		t.Fatalf("login failure audit attempts = %d; want %d", got, AccountFailureLimit)
	}
	if got := recorder.count(audit.EventLoginRateLimited); got != 1 {
		t.Fatalf("block audit attempts = %d; want exactly one with no retry amplification", got)
	}
}

func TestLoginLimiterRestartSemanticsAreProcessLocal(t *testing.T) {
	db := openAuthTestDatabase(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	auditService := audit.NewService(audit.NewSQLiteRepository(db), nil)
	input := invalidLoginInput("invalid username", "192.0.2.50")
	first := newRateLimitTestService(t, db, auditService, NewLoginLimiter(1_000), countingPasswordManager(t), func() time.Time { return now })
	for range AccountFailureLimit {
		_, _ = first.Login(context.Background(), input)
	}
	assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})

	// A restart deliberately does not reconstruct process-local blocks from the
	// append-only audit log. A new threshold produces one new transition event.
	second := newRateLimitTestService(t, db, auditService, NewLoginLimiter(1_000), countingPasswordManager(t), func() time.Time { return now })
	if _, err := second.Login(context.Background(), input); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first post-restart attempt error = %v; want ErrInvalidCredentials", err)
	}
	assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 1})
	for attempt := 1; attempt < AccountFailureLimit; attempt++ {
		_, _ = second.Login(context.Background(), input)
	}
	assertRateLimitAuditReasons(t, db, map[string]int{"account_rate_limit": 2})
}

func invalidLoginInput(username, remoteIP string) LoginInput {
	return LoginInput{
		Username:   username,
		Password:   strings.Repeat("x", MaximumPasswordBytes+1),
		ClientName: "rate-limit-test",
		RemoteIP:   remoteIP,
		RequestID:  "rate-limit-request",
	}
}

func countingPasswordManager(t *testing.T) *PasswordManager {
	t.Helper()
	passwords, err := NewPasswordManager(1)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	return passwords
}

func newRateLimitTestService(t *testing.T, db *sql.DB, recorder AuditRecorder, limiter *LoginLimiter, passwords *PasswordManager, now func() time.Time) *Service {
	t.Helper()
	service, err := NewService(NewSQLiteRepository(db), recorder, limiter, passwords, now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func assertRateLimited(t *testing.T, service *Service, input LoginInput) {
	t.Helper()
	_, err := service.Login(context.Background(), input)
	var rateErr *RateLimitError
	if !errors.As(err, &rateErr) || rateErr.RetryAfter <= 0 {
		t.Fatalf("Login() error = %v; want RateLimitError with RetryAfter", err)
	}
}

func assertAuditCount(t *testing.T, db *sql.DB, eventType string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = ?`, eventType).Scan(&count); err != nil {
		t.Fatalf("count %s audit events: %v", eventType, err)
	}
	if count != want {
		t.Fatalf("%s audit rows = %d; want %d", eventType, count, want)
	}
}

func assertRateLimitAuditReasons(t *testing.T, db *sql.DB, want map[string]int) {
	t.Helper()
	rows, err := db.Query(`
		SELECT json_extract(metadata_json, '$.reason_code'), COUNT(*)
		FROM audit_events
		WHERE event_type = ?
		GROUP BY json_extract(metadata_json, '$.reason_code')
	`, audit.EventLoginRateLimited)
	if err != nil {
		t.Fatalf("query rate-limit reasons: %v", err)
	}
	defer rows.Close()
	got := make(map[string]int)
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			t.Fatalf("scan rate-limit reason: %v", err)
		}
		got[reason] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rate-limit reasons: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rate-limit audit reasons = %v; want %v", got, want)
	}
}

type selectiveAuditRecorder struct {
	mu       sync.Mutex
	failType string
	events   []audit.Event
}

func (recorder *selectiveAuditRecorder) Record(_ context.Context, event audit.Event) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	if event.Type == recorder.failType {
		return errors.New("forced block audit failure")
	}
	return nil
}

func (recorder *selectiveAuditRecorder) count(eventType string) int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	count := 0
	for _, event := range recorder.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
