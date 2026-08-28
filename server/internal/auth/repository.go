package auth

import (
	"context"
	"errors"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrSessionNotFound = errors.New("session not found")
	ErrOwnerExists     = errors.New("initial owner already exists")
	ErrSessionLimit    = errors.New("active session limit reached")
)

const (
	MaximumActiveSessions = 100
	MaximumListedSessions = 200
)

type Repository interface {
	findCredentialByUsername(ctx context.Context, username string) (userCredential, error)
	CreateInitialOwnerWithAudit(ctx context.Context, username, passwordHash string, now time.Time, event audit.Event) (User, error)
	ResetOwnerCredentialsWithAudit(ctx context.Context, username, passwordHash string, now time.Time, event audit.Event) (User, int64, error)
	CreateSessionWithAudit(ctx context.Context, userID int64, tokenHash SessionTokenHash, clientName string, createdAt, expiresAt time.Time, event audit.Event) (Session, error)
	FindAuthenticatedSession(ctx context.Context, tokenHash SessionTokenHash) (AuthenticatedSession, error)
	UpdateLastSeen(ctx context.Context, sessionID int64, seenAt, updateBefore time.Time) error
	ListSessions(ctx context.Context, userID int64) ([]Session, error)
	RevokeSessionWithAudit(ctx context.Context, userID, sessionID int64, revokedAt time.Time, event audit.Event) (Session, error)
}
