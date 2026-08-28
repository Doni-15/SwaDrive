package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
)

type SQLiteRepository struct {
	db    *sql.DB
	audit *audit.SQLiteRepository
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, audit: audit.NewSQLiteRepository(db)}
}

func (repository *SQLiteRepository) findCredentialByUsername(ctx context.Context, username string) (userCredential, error) {
	var credential userCredential
	var role string
	var createdAt, updatedAt int64
	var disabledAt sql.NullInt64
	err := repository.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, created_at, updated_at, disabled_at
		FROM users
		WHERE username = ?
	`, username).Scan(
		&credential.user.ID,
		&credential.user.Username,
		&credential.passwordHash,
		&role,
		&createdAt,
		&updatedAt,
		&disabledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return userCredential{}, ErrUserNotFound
	}
	if err != nil {
		return userCredential{}, fmt.Errorf("find user credential by username: %w", err)
	}

	credential.user.Role = Role(role)
	credential.user.CreatedAt = time.Unix(createdAt, 0).UTC()
	credential.user.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	credential.user.DisabledAt = nullableTime(disabledAt)
	return credential, nil
}

func (repository *SQLiteRepository) CreateInitialOwnerWithAudit(ctx context.Context, username, passwordHash string, now time.Time, event audit.Event) (User, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("begin initial owner creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	timestamp := now.UTC().Unix()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		SELECT ?, ?, 'owner', ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE role = 'owner')
	`, username, passwordHash, timestamp, timestamp)
	if err != nil {
		return User{}, fmt.Errorf("create initial owner: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return User{}, fmt.Errorf("read initial owner result: %w", err)
	}
	if rowsAffected == 0 {
		return User{}, ErrOwnerExists
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("read initial owner ID: %w", err)
	}
	user := User{ID: id, Username: username, Role: RoleOwner, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	event.ActorUserID = &user.ID
	event.ResourceID = strconv.FormatInt(user.ID, 10)
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return User{}, fmt.Errorf("append initial owner audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit initial owner creation: %w", err)
	}
	return user, nil
}

func (repository *SQLiteRepository) ResetOwnerCredentialsWithAudit(ctx context.Context, username, passwordHash string, now time.Time, event audit.Event) (User, int64, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, 0, fmt.Errorf("begin owner credential reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var user User
	var role string
	var createdAt, updatedAt int64
	var disabledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT id, username, role, created_at, updated_at, disabled_at
		FROM users
		WHERE username = ? AND role = 'owner'
	`, username).Scan(&user.ID, &user.Username, &role, &createdAt, &updatedAt, &disabledAt); errors.Is(err, sql.ErrNoRows) {
		return User{}, 0, ErrUserNotFound
	} else if err != nil {
		return User{}, 0, fmt.Errorf("find owner for credential reset: %w", err)
	}

	timestamp := now.UTC().Unix()
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?
	`, passwordHash, timestamp, user.ID); err != nil {
		return User{}, 0, fmt.Errorf("update owner password: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = ?
		WHERE user_id = ? AND revoked_at IS NULL
	`, timestamp, user.ID)
	if err != nil {
		return User{}, 0, fmt.Errorf("revoke owner sessions: %w", err)
	}
	revoked, err := result.RowsAffected()
	if err != nil {
		return User{}, 0, fmt.Errorf("read revoked owner sessions: %w", err)
	}

	user.Role = Role(role)
	user.CreatedAt = time.Unix(createdAt, 0).UTC()
	user.UpdatedAt = now.UTC()
	user.DisabledAt = nullableTime(disabledAt)
	event.ResourceID = strconv.FormatInt(user.ID, 10)
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return User{}, 0, fmt.Errorf("append owner credential reset audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, 0, fmt.Errorf("commit owner credential reset: %w", err)
	}
	return user, revoked, nil
}

func (repository *SQLiteRepository) CreateSessionWithAudit(
	ctx context.Context,
	userID int64,
	tokenHash SessionTokenHash,
	clientName string,
	createdAt, expiresAt time.Time,
	event audit.Event,
) (Session, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin session creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var activeSessions int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?
	`, userID, createdAt.UTC().Unix()).Scan(&activeSessions); err != nil {
		return Session{}, fmt.Errorf("count active sessions: %w", err)
	}
	if activeSessions >= MaximumActiveSessions {
		return Session{}, ErrSessionLimit
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (
			user_id, token_hash, client_name, created_at, expires_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, userID, tokenHash.Bytes(), clientName, createdAt.UTC().Unix(), expiresAt.UTC().Unix(), createdAt.UTC().Unix())
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Session{}, fmt.Errorf("read session ID: %w", err)
	}
	session := Session{
		ID:         id,
		UserID:     userID,
		ClientName: clientName,
		CreatedAt:  createdAt.UTC(),
		ExpiresAt:  expiresAt.UTC(),
		LastSeenAt: createdAt.UTC(),
	}
	event.ActorUserID = &userID
	event.ActorSessionID = &session.ID
	event.ResourceID = strconv.FormatInt(session.ID, 10)
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return Session{}, fmt.Errorf("append login audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit session creation: %w", err)
	}
	return session, nil
}

func (repository *SQLiteRepository) FindAuthenticatedSession(ctx context.Context, tokenHash SessionTokenHash) (AuthenticatedSession, error) {
	var result AuthenticatedSession
	var userRole string
	var userCreatedAt, userUpdatedAt int64
	var userDisabledAt sql.NullInt64
	var sessionCreatedAt, sessionExpiresAt, sessionLastSeenAt int64
	var sessionRevokedAt sql.NullInt64
	err := repository.db.QueryRowContext(ctx, `
		SELECT
			u.id, u.username, u.role, u.created_at, u.updated_at, u.disabled_at,
			s.id, s.user_id, s.client_name, s.created_at, s.expires_at, s.revoked_at, s.last_seen_at
		FROM sessions AS s
		JOIN users AS u ON u.id = s.user_id
		WHERE s.token_hash = ?
	`, tokenHash.Bytes()).Scan(
		&result.User.ID,
		&result.User.Username,
		&userRole,
		&userCreatedAt,
		&userUpdatedAt,
		&userDisabledAt,
		&result.Session.ID,
		&result.Session.UserID,
		&result.Session.ClientName,
		&sessionCreatedAt,
		&sessionExpiresAt,
		&sessionRevokedAt,
		&sessionLastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthenticatedSession{}, ErrSessionNotFound
	}
	if err != nil {
		return AuthenticatedSession{}, fmt.Errorf("find authenticated session: %w", err)
	}

	result.User.Role = Role(userRole)
	result.User.CreatedAt = time.Unix(userCreatedAt, 0).UTC()
	result.User.UpdatedAt = time.Unix(userUpdatedAt, 0).UTC()
	result.User.DisabledAt = nullableTime(userDisabledAt)
	result.Session.CreatedAt = time.Unix(sessionCreatedAt, 0).UTC()
	result.Session.ExpiresAt = time.Unix(sessionExpiresAt, 0).UTC()
	result.Session.RevokedAt = nullableTime(sessionRevokedAt)
	result.Session.LastSeenAt = time.Unix(sessionLastSeenAt, 0).UTC()
	return result, nil
}

func (repository *SQLiteRepository) UpdateLastSeen(ctx context.Context, sessionID int64, seenAt, updateBefore time.Time) error {
	if _, err := repository.db.ExecContext(ctx, `
		UPDATE sessions
		SET last_seen_at = ?
		WHERE id = ? AND last_seen_at <= ?
	`, seenAt.UTC().Unix(), sessionID, updateBefore.UTC().Unix()); err != nil {
		return fmt.Errorf("update session last seen: %w", err)
	}
	return nil
}

func (repository *SQLiteRepository) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, user_id, client_name, created_at, expires_at, revoked_at, last_seen_at
		FROM sessions
		WHERE user_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?
	`, userID, MaximumListedSessions)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]Session, 0, MaximumListedSessions)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return sessions, nil
}

func (repository *SQLiteRepository) RevokeSessionWithAudit(ctx context.Context, userID, sessionID int64, revokedAt time.Time, event audit.Event) (Session, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin session revocation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
		SELECT id, user_id, client_name, created_at, expires_at, revoked_at, last_seen_at
		FROM sessions
		WHERE id = ? AND user_id = ?
	`, sessionID, userID)
	session, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, err
	}

	if session.RevokedAt == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id = ?`, revokedAt.UTC().Unix(), sessionID); err != nil {
			return Session{}, fmt.Errorf("revoke session: %w", err)
		}
		timestamp := revokedAt.UTC()
		session.RevokedAt = &timestamp
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return Session{}, fmt.Errorf("append session revocation audit event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit session revocation: %w", err)
	}
	return session, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(row scanner) (Session, error) {
	var session Session
	var createdAt, expiresAt, lastSeenAt int64
	var revokedAt sql.NullInt64
	if err := row.Scan(
		&session.ID,
		&session.UserID,
		&session.ClientName,
		&createdAt,
		&expiresAt,
		&revokedAt,
		&lastSeenAt,
	); err != nil {
		return Session{}, err
	}
	session.CreatedAt = time.Unix(createdAt, 0).UTC()
	session.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	session.RevokedAt = nullableTime(revokedAt)
	session.LastSeenAt = time.Unix(lastSeenAt, 0).UTC()
	return session, nil
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := time.Unix(value.Int64, 0).UTC()
	return &timestamp
}
