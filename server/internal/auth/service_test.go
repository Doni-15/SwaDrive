package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/database"
)

func TestAuthenticationServiceLifecycleAndSessionChecks(t *testing.T) {
	db := openAuthTestDatabase(t)
	repository := NewSQLiteRepository(db)
	auditService := audit.NewService(audit.NewSQLiteRepository(db), nil)
	now := time.Unix(1_800_000_000, 0).UTC()
	passwords, err := NewPasswordManager(2)
	if err != nil {
		t.Fatalf("NewPasswordManager() error = %v", err)
	}
	service, err := NewService(repository, auditService, NewLoginLimiter(100), passwords, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	ctx := context.Background()
	password := "a correct long passphrase"
	owner, err := service.BootstrapOwner(ctx, " Owner.Name ", password, "bootstrap-request")
	if err != nil {
		t.Fatalf("BootstrapOwner() error = %v", err)
	}
	if owner.Username != "owner.name" || owner.Role != RoleOwner {
		t.Fatalf("owner = %+v; want canonical owner", owner)
	}
	if _, err := service.BootstrapOwner(ctx, "second-owner", password, ""); !errors.Is(err, ErrOwnerExists) {
		t.Fatalf("second BootstrapOwner() error = %v; want ErrOwnerExists", err)
	}

	login := func(username, suppliedPassword, clientName string) (LoginResult, error) {
		return service.Login(ctx, LoginInput{
			Username: username, Password: suppliedPassword, ClientName: clientName,
			RemoteIP: "192.0.2.10", RequestID: "login-request",
		})
	}
	if _, err := login("owner.name", "wrong password value", "Linux"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong-password Login() error = %v; want ErrInvalidCredentials", err)
	}
	if _, err := login("unknown-user", "wrong password value", "Linux"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown-user Login() error = %v; want ErrInvalidCredentials", err)
	}

	first, err := login(" OWNER.NAME ", password, " Linux laptop ")
	if err != nil {
		t.Fatalf("correct Login() error = %v", err)
	}
	if first.Token.Value() == "" || first.Session.ExpiresAt.Sub(first.Session.CreatedAt) != SessionLifetime {
		t.Fatalf("login result = %+v; want token and 30-day expiry", first.Session)
	}
	if _, err := service.Authenticate(ctx, "malformed", "", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(malformed) error = %v; want ErrUnauthorized", err)
	}
	if _, err := service.Authenticate(ctx, first.Token.Value(), "request", "192.0.2.10"); err != nil {
		t.Fatalf("Authenticate(valid) error = %v", err)
	}

	originalLastSeen := first.Session.LastSeenAt.Unix()
	now = now.Add(4 * time.Minute)
	if _, err := service.Authenticate(ctx, first.Token.Value(), "request", "192.0.2.10"); err != nil {
		t.Fatalf("Authenticate(before throttle) error = %v", err)
	}
	if got := sessionLastSeen(t, db, first.Session.ID); got != originalLastSeen {
		t.Fatalf("last_seen_at = %d; want throttled value %d", got, originalLastSeen)
	}
	now = now.Add(2 * time.Minute)
	if _, err := service.Authenticate(ctx, first.Token.Value(), "request", "192.0.2.10"); err != nil {
		t.Fatalf("Authenticate(after throttle) error = %v", err)
	}
	if got := sessionLastSeen(t, db, first.Session.ID); got != now.Unix() {
		t.Fatalf("last_seen_at = %d; want %d", got, now.Unix())
	}

	second, err := login("owner.name", password, "Android phone")
	if err != nil {
		t.Fatalf("second Login() error = %v", err)
	}
	currentIdentity, err := service.Authenticate(ctx, first.Token.Value(), "request", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate(first) error = %v", err)
	}
	sessions, err := service.ListSessions(ctx, currentIdentity)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("ListSessions() = %d, %v; want 2", len(sessions), err)
	}
	if err := service.RevokeSession(ctx, currentIdentity, second.Session.ID); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, second.Token.Value(), "", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(revoked) error = %v; want ErrUnauthorized", err)
	}
	if err := service.Logout(ctx, currentIdentity); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, first.Token.Value(), "", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(logged out) error = %v; want ErrUnauthorized", err)
	}

	expired, err := login("owner.name", password, "Expired device")
	if err != nil {
		t.Fatalf("expired-session Login() error = %v", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`, expired.Session.CreatedAt.Add(time.Second).Unix(), expired.Session.ID); err != nil {
		t.Fatalf("expire session: %v", err)
	}
	if _, err := service.Authenticate(ctx, expired.Token.Value(), "", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(expired) error = %v; want ErrUnauthorized", err)
	}

	disabled, err := login("owner.name", password, "Disabled user device")
	if err != nil {
		t.Fatalf("disabled-user Login() setup error = %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, now.Unix(), owner.ID); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, err := service.Authenticate(ctx, disabled.Token.Value(), "", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(disabled user) error = %v; want ErrUnauthorized", err)
	}
	if _, err := login("owner.name", password, "Disabled login"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(disabled user) error = %v; want ErrInvalidCredentials", err)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type IN ('auth.login_success', 'auth.login_failure', 'auth.logout', 'auth.session_revoked')`).Scan(&eventCount); err != nil {
		t.Fatalf("count auth audit events: %v", err)
	}
	if eventCount < 8 {
		t.Fatalf("auth audit event count = %d; want at least 8", eventCount)
	}
}

func sessionLastSeen(t *testing.T, db *sql.DB, sessionID int64) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRow(`SELECT last_seen_at FROM sessions WHERE id = ?`, sessionID).Scan(&value); err != nil {
		t.Fatalf("query last_seen_at: %v", err)
	}
	return value
}

func openAuthTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("database.Migrate() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
