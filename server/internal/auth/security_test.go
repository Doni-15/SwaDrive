package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
)

func TestIdentityAndUserCannotCarryPasswordHashes(t *testing.T) {
	for _, model := range []any{User{}, Identity{}, AuthenticatedSession{}} {
		modelType := reflect.TypeOf(model)
		for index := 0; index < modelType.NumField(); index++ {
			name := strings.ToLower(modelType.Field(index).Name)
			if strings.Contains(name, "password") || strings.Contains(name, "hash") || strings.Contains(name, "credential") {
				t.Fatalf("%s exposes credential-like field %q", modelType.Name(), modelType.Field(index).Name)
			}
		}
	}

	db := openAuthTestDatabase(t)
	repository := NewSQLiteRepository(db)
	service := newSecurityTestService(t, repository)
	password := "credential boundary passphrase"
	if _, err := service.BootstrapOwner(context.Background(), "owner", password, "bootstrap"); err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	credential, err := repository.findCredentialByUsername(context.Background(), "owner")
	if err != nil {
		t.Fatalf("findCredentialByUsername() error = %v", err)
	}
	identity := Identity{User: credential.user}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("json.Marshal(Identity) error = %v", err)
	}
	for _, representation := range []string{string(encoded), fmt.Sprintf("%+v", identity), fmt.Sprintf("%+v", credential.user)} {
		if strings.Contains(representation, credential.passwordHash) || strings.Contains(strings.ToLower(representation), "passwordhash") {
			t.Fatalf("normal auth model exposed password hash: %s", representation)
		}
	}
}

func TestSecurityMutationsAndAuditAreAtomic(t *testing.T) {
	t.Run("bootstrap", func(t *testing.T) {
		db := openAuthTestDatabase(t)
		service := newSecurityTestService(t, NewSQLiteRepository(db))
		installAuditFailureTrigger(t, db)
		if _, err := service.BootstrapOwner(context.Background(), "owner", "atomic bootstrap passphrase", "request"); err == nil {
			t.Fatal("BootstrapOwner() succeeded with forced audit failure")
		}
		assertRowCount(t, db, "users", 0)
		assertRowCount(t, db, "audit_events", 0)
	})

	t.Run("login success", func(t *testing.T) {
		db := openAuthTestDatabase(t)
		service := newSecurityTestService(t, NewSQLiteRepository(db))
		const password = "atomic login passphrase"
		if _, err := service.BootstrapOwner(context.Background(), "owner", password, "bootstrap"); err != nil {
			t.Fatalf("BootstrapOwner() error = %v", err)
		}
		installAuditFailureTrigger(t, db)
		_, err := service.Login(context.Background(), LoginInput{Username: "owner", Password: password, ClientName: "device", RemoteIP: "192.0.2.1"})
		if err == nil {
			t.Fatal("Login() succeeded with forced audit failure")
		}
		assertRowCount(t, db, "sessions", 0)
	})

	t.Run("logout and revoke", func(t *testing.T) {
		db := openAuthTestDatabase(t)
		service := newSecurityTestService(t, NewSQLiteRepository(db))
		const password = "atomic revoke passphrase"
		if _, err := service.BootstrapOwner(context.Background(), "owner", password, "bootstrap"); err != nil {
			t.Fatalf("BootstrapOwner() error = %v", err)
		}
		first := loginForSecurityTest(t, service, password, "first")
		second := loginForSecurityTest(t, service, password, "second")
		identity, err := service.Authenticate(context.Background(), first.Token.Value(), "request", "192.0.2.1")
		if err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}

		installAuditFailureTrigger(t, db)
		if err := service.Logout(context.Background(), identity); err == nil {
			t.Fatal("Logout() succeeded with forced audit failure")
		}
		assertSessionNotRevoked(t, db, first.Session.ID)
		dropAuditFailureTrigger(t, db)

		installAuditFailureTrigger(t, db)
		if err := service.RevokeSession(context.Background(), identity, second.Session.ID); err == nil {
			t.Fatal("RevokeSession() succeeded with forced audit failure")
		}
		assertSessionNotRevoked(t, db, second.Session.ID)
	})
}

func newSecurityTestService(t *testing.T, repository *SQLiteRepository) *Service {
	t.Helper()
	passwords, err := NewPasswordManager(2)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	service, err := NewService(
		repository,
		audit.NewService(audit.NewSQLiteRepository(repository.db), nil),
		NewLoginLimiter(100),
		passwords,
		func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func loginForSecurityTest(t *testing.T, service *Service, password, client string) LoginResult {
	t.Helper()
	result, err := service.Login(context.Background(), LoginInput{
		Username: "owner", Password: password, ClientName: client, RemoteIP: "192.0.2.1",
	})
	if err != nil {
		t.Fatalf("Login(%s) error = %v", client, err)
	}
	return result
}

func installAuditFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TRIGGER force_audit_failure
		BEFORE INSERT ON audit_events
		BEGIN
			SELECT RAISE(ABORT, 'forced audit failure');
		END
	`)
	if err != nil {
		t.Fatalf("create forced audit failure trigger: %v", err)
	}
}

func dropAuditFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TRIGGER force_audit_failure`); err != nil {
		t.Fatalf("drop forced audit failure trigger: %v", err)
	}
}

func assertRowCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var count int
	var err error
	switch table {
	case "users":
		err = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	case "sessions":
		err = db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count)
	case "audit_events":
		err = db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count)
	default:
		t.Fatalf("unsupported table %q", table)
	}
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s row count = %d; want %d", table, count, want)
	}
}

func assertSessionNotRevoked(t *testing.T, db *sql.DB, sessionID int64) {
	t.Helper()
	var revokedAt any
	if err := db.QueryRow(`SELECT revoked_at FROM sessions WHERE id = ?`, sessionID).Scan(&revokedAt); err != nil {
		t.Fatalf("query session revocation: %v", err)
	}
	if revokedAt != nil {
		t.Fatalf("session %d revoked_at = %v; want NULL", sessionID, revokedAt)
	}
}

func TestRevokeSessionCannotCrossUserBoundary(t *testing.T) {
	db := openAuthTestDatabase(t)
	service := newSecurityTestService(t, NewSQLiteRepository(db))
	const password = "cross user revoke passphrase"
	owner, err := service.BootstrapOwner(context.Background(), "owner", password, "bootstrap")
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	ownerLogin := loginForSecurityTest(t, service, password, "owner-device")
	identity, err := service.Authenticate(context.Background(), ownerLogin.Token.Value(), "", "")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	result, err := db.Exec(`
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES ('member', 'test-only-hash', 'member', ?, ?)
	`, now.Unix(), now.Unix())
	if err != nil {
		t.Fatalf("insert member: %v", err)
	}
	memberID, _ := result.LastInsertId()
	result, err = db.Exec(`
		INSERT INTO sessions (user_id, token_hash, client_name, created_at, expires_at, last_seen_at)
		VALUES (?, randomblob(32), 'member-device', ?, ?, ?)
	`, memberID, now.Unix(), now.Add(time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("insert member session: %v", err)
	}
	memberSessionID, _ := result.LastInsertId()

	if err := service.RevokeSession(context.Background(), identity, memberSessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("RevokeSession(other user) error = %v; want ErrSessionNotFound", err)
	}
	assertSessionNotRevoked(t, db, memberSessionID)
	if identity.User.ID != owner.ID {
		t.Fatal("test identity does not belong to owner")
	}
}

func TestUnknownAndDisabledUsersUseDummyHashVerification(t *testing.T) {
	db := openAuthTestDatabase(t)
	passwords, err := NewPasswordManager(1)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	originalDerive := passwords.derive
	var derivations atomic.Int32
	passwords.derive = func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte {
		derivations.Add(1)
		return originalDerive(password, salt, iterations, memory, parallelism, keyLength)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	service, err := NewService(
		NewSQLiteRepository(db), audit.NewService(audit.NewSQLiteRepository(db), nil),
		NewLoginLimiter(100), passwords, func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const password = "dummy hash behavior passphrase"
	owner, err := service.BootstrapOwner(context.Background(), "owner", password, "bootstrap")
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}

	for _, username := range []string{"unknown", "owner"} {
		if username == "owner" {
			if _, err := db.Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, now.Unix(), owner.ID); err != nil {
				t.Fatalf("disable owner: %v", err)
			}
		}
		derivations.Store(0)
		_, loginErr := service.Login(context.Background(), LoginInput{
			Username: username, Password: "wrong password value", ClientName: "device", RemoteIP: "192.0.2.1",
		})
		if !errors.Is(loginErr, ErrInvalidCredentials) {
			t.Fatalf("Login(%s) error = %v; want ErrInvalidCredentials", username, loginErr)
		}
		if derivations.Load() != 1 {
			t.Fatalf("Login(%s) Argon2 derivations = %d; want dummy verification", username, derivations.Load())
		}
	}
}
