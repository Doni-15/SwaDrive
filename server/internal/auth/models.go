package auth

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"

	SessionLifetime       = 30 * 24 * time.Hour
	LastSeenWriteInterval = 5 * time.Minute
)

type User struct {
	ID         int64
	Username   string
	Role       Role
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DisabledAt *time.Time
}

// userCredential exists only inside the auth package. Application identity,
// HTTP, file, upload, and audit code receive User, which deliberately cannot
// carry credential material.
type userCredential struct {
	user         User
	passwordHash string
}

type Session struct {
	ID         int64
	UserID     int64
	ClientName string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastSeenAt time.Time
}

type Identity struct {
	User      User
	Session   Session
	RequestID string
	RemoteIP  string
}

type AuthenticatedSession struct {
	User    User
	Session Session
}
