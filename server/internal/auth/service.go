package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("authentication required")
)

type AuditRecorder interface {
	Record(ctx context.Context, event audit.Event) error
}

type Service struct {
	repository Repository
	audit      AuditRecorder
	limiter    *LoginLimiter
	passwords  *PasswordManager
	now        func() time.Time
	dummyHash  string
}

type LoginInput struct {
	Username   string
	Password   string
	ClientName string
	RemoteIP   string
	RequestID  string
}

type LoginResult struct {
	Token   RawSessionToken
	User    User
	Session Session
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (err *RateLimitError) Error() string {
	return "login temporarily rate limited"
}

func NewService(repository Repository, auditRecorder AuditRecorder, limiter *LoginLimiter, passwords *PasswordManager, now func() time.Time) (*Service, error) {
	if limiter == nil {
		limiter = NewLoginLimiter(DefaultLimiterEntries)
	}
	if passwords == nil {
		var err error
		passwords, err = NewPasswordManager(DefaultArgon2Limit)
		if err != nil {
			return nil, err
		}
	}
	if now == nil {
		now = time.Now
	}
	dummyHash, err := passwords.Hash(context.Background(), "not-a-real-user-password")
	if err != nil {
		return nil, fmt.Errorf("create dummy password hash: %w", err)
	}
	return &Service{
		repository: repository,
		audit:      auditRecorder,
		limiter:    limiter,
		passwords:  passwords,
		now:        now,
		dummyHash:  dummyHash,
	}, nil
}

func (service *Service) BootstrapOwner(ctx context.Context, username, password, requestID string) (User, error) {
	canonicalUsername, err := CanonicalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	if err := ValidateNewPassword(password); err != nil {
		return User{}, err
	}
	passwordHash, err := service.passwords.Hash(ctx, password)
	if err != nil {
		return User{}, err
	}

	now := service.now().UTC()
	user, err := service.repository.CreateInitialOwnerWithAudit(ctx, canonicalUsername, passwordHash, now, audit.Event{
		OccurredAt:   now,
		Type:         audit.EventOwnerBootstrap,
		Outcome:      audit.OutcomeSuccess,
		ResourceType: "user",
		RequestID:    requestID,
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (service *Service) ResetOwnerCredentials(ctx context.Context, username, password string) (User, int64, error) {
	canonicalUsername, err := CanonicalizeUsername(username)
	if err != nil {
		return User{}, 0, err
	}
	if err := ValidateNewPassword(password); err != nil {
		return User{}, 0, err
	}
	passwordHash, err := service.passwords.Hash(ctx, password)
	if err != nil {
		return User{}, 0, err
	}
	now := service.now().UTC()
	return service.repository.ResetOwnerCredentialsWithAudit(ctx, canonicalUsername, passwordHash, now, audit.Event{
		OccurredAt:   now,
		Type:         audit.EventOwnerCredentialsReset,
		Outcome:      audit.OutcomeSuccess,
		ResourceType: "user",
	})
}

func (service *Service) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	clientName, err := canonicalizeClientName(input.ClientName)
	if err != nil {
		return LoginResult{}, err
	}

	canonicalUsername, usernameErr := CanonicalizeUsername(input.Username)
	limiterUsername := canonicalUsername
	if usernameErr != nil {
		limiterUsername = invalidUsernameLimiterKey(input.Username)
	}
	now := service.now().UTC()
	if retryAfter, blocked := service.limiter.Check(limiterUsername, input.RemoteIP, now); blocked {
		return LoginResult{}, &RateLimitError{RetryAfter: retryAfter}
	}

	if validateLoginPassword(input.Password) != nil {
		return service.failLogin(ctx, limiterUsername, input, now)
	}

	var credential userCredential
	userFoundAndEnabled := false
	if usernameErr == nil {
		credential, err = service.repository.findCredentialByUsername(ctx, canonicalUsername)
		switch {
		case err == nil && credential.user.DisabledAt == nil:
			userFoundAndEnabled = true
		case errors.Is(err, ErrUserNotFound), err == nil && credential.user.DisabledAt != nil:
		case err != nil:
			return LoginResult{}, err
		}
	}

	hashToVerify := service.dummyHash
	if userFoundAndEnabled {
		hashToVerify = credential.passwordHash
	}
	passwordCorrect, err := service.passwords.Verify(ctx, input.Password, hashToVerify)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify login password: %w", err)
	}
	if !userFoundAndEnabled || !passwordCorrect {
		return service.failLogin(ctx, limiterUsername, input, now)
	}

	service.limiter.Clear(limiterUsername, input.RemoteIP)
	rawToken, tokenHash, err := GenerateSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	user := credential.user
	actorUserID := user.ID
	session, err := service.repository.CreateSessionWithAudit(ctx, user.ID, tokenHash, clientName, now, now.Add(SessionLifetime), audit.Event{
		OccurredAt:   now,
		ActorUserID:  &actorUserID,
		Type:         audit.EventLoginSuccess,
		Outcome:      audit.OutcomeSuccess,
		ResourceType: "session",
		RequestID:    input.RequestID,
		RemoteIP:     input.RemoteIP,
	})
	if err != nil {
		return LoginResult{}, err
	}

	return LoginResult{Token: rawToken, User: user, Session: session}, nil
}

func (service *Service) failLogin(ctx context.Context, limiterUsername string, input LoginInput, now time.Time) (LoginResult, error) {
	transitions := service.limiter.RecordFailure(limiterUsername, input.RemoteIP, now)
	if err := service.audit.Record(ctx, audit.Event{
		OccurredAt: now,
		Type:       audit.EventLoginFailure,
		Outcome:    audit.OutcomeFailure,
		RequestID:  input.RequestID,
		RemoteIP:   input.RemoteIP,
	}); err != nil {
		return LoginResult{}, fmt.Errorf("record login failure event: %w", err)
	}
	if err := service.recordLoginBlockTransitions(ctx, transitions, input, now); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{}, ErrInvalidCredentials
}

// A block is audited only when a limiter bucket crosses its threshold. Requests
// rejected by an already-blocked bucket do not write durable state, so a blocked
// peer cannot amplify append-only audit storage without bound.
func (service *Service) recordLoginBlockTransitions(ctx context.Context, transitions loginBlockTransitions, input LoginInput, now time.Time) error {
	for _, transition := range []struct {
		value      loginBlockTransitions
		reasonCode string
	}{
		{value: accountBlockTransition, reasonCode: "account_rate_limit"},
		{value: ipBlockTransition, reasonCode: "ip_rate_limit"},
	} {
		if !transitions.includes(transition.value) {
			continue
		}
		if err := service.audit.Record(ctx, audit.Event{
			OccurredAt: now,
			Type:       audit.EventLoginRateLimited,
			Outcome:    audit.OutcomeDenied,
			RequestID:  input.RequestID,
			RemoteIP:   input.RemoteIP,
			Metadata:   map[string]string{"reason_code": transition.reasonCode},
		}); err != nil {
			return fmt.Errorf("record login rate-limit transition: %w", err)
		}
	}
	return nil
}

func (service *Service) Authenticate(ctx context.Context, tokenValue, requestID, remoteIP string) (Identity, error) {
	rawToken, err := ParseSessionToken(tokenValue)
	if err != nil {
		return Identity{}, ErrUnauthorized
	}
	authenticated, err := service.repository.FindAuthenticatedSession(ctx, HashSessionToken(rawToken))
	if errors.Is(err, ErrSessionNotFound) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, err
	}

	now := service.now().UTC()
	if authenticated.Session.RevokedAt != nil || !authenticated.Session.ExpiresAt.After(now) || authenticated.User.DisabledAt != nil {
		return Identity{}, ErrUnauthorized
	}
	if !authenticated.Session.LastSeenAt.After(now.Add(-LastSeenWriteInterval)) {
		if err := service.repository.UpdateLastSeen(ctx, authenticated.Session.ID, now, now.Add(-LastSeenWriteInterval)); err != nil {
			return Identity{}, err
		}
		authenticated.Session.LastSeenAt = now
	}

	return Identity{
		User:      authenticated.User,
		Session:   authenticated.Session,
		RequestID: requestID,
		RemoteIP:  remoteIP,
	}, nil
}

func (service *Service) ListSessions(ctx context.Context, identity Identity) ([]Session, error) {
	return service.repository.ListSessions(ctx, identity.User.ID)
}

func (service *Service) Logout(ctx context.Context, identity Identity) error {
	now := service.now().UTC()
	_, err := service.repository.RevokeSessionWithAudit(ctx, identity.User.ID, identity.Session.ID, now, sessionEvent(identity, identity.Session.ID, audit.EventLogout, now))
	return err
}

func (service *Service) RevokeSession(ctx context.Context, identity Identity, sessionID int64) error {
	if sessionID <= 0 {
		return ErrSessionNotFound
	}
	now := service.now().UTC()
	_, err := service.repository.RevokeSessionWithAudit(ctx, identity.User.ID, sessionID, now, sessionEvent(identity, sessionID, audit.EventSessionRevoked, now))
	return err
}

func sessionEvent(identity Identity, sessionID int64, eventType string, now time.Time) audit.Event {
	actorUserID := identity.User.ID
	actorSessionID := identity.Session.ID
	return audit.Event{
		OccurredAt:     now,
		ActorUserID:    &actorUserID,
		ActorSessionID: &actorSessionID,
		Type:           eventType,
		Outcome:        audit.OutcomeSuccess,
		ResourceType:   "session",
		ResourceID:     strconv.FormatInt(sessionID, 10),
		RequestID:      identity.RequestID,
		RemoteIP:       identity.RemoteIP,
	}
}

func invalidUsernameLimiterKey(username string) string {
	canonical := strings.ToLower(strings.TrimSpace(username))
	sum := sha256.Sum256([]byte(canonical))
	return "invalid:" + fmt.Sprintf("%x", sum[:])
}
