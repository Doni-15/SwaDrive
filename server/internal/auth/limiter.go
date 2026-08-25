package auth

import (
	"sync"
	"time"
)

const (
	AccountFailureLimit   = 8
	IPFailureLimit        = 40
	LoginFailureWindow    = 5 * time.Minute
	LoginBlockDuration    = 15 * time.Minute
	DefaultLimiterEntries = 10_000
)

type loginLimitKey struct {
	username string
	remoteIP string
}

type loginLimitEntry struct {
	windowStarted time.Time
	blockedUntil  time.Time
	lastSeen      time.Time
	failures      int
}

type loginBlockTransitions uint8

const (
	accountBlockTransition loginBlockTransitions = 1 << iota
	ipBlockTransition
)

func (transitions loginBlockTransitions) includes(transition loginBlockTransitions) bool {
	return transitions&transition != 0
}

type LoginLimiter struct {
	mu             sync.Mutex
	accountEntries map[loginLimitKey]loginLimitEntry
	ipEntries      map[string]loginLimitEntry
	maxEntries     int
	lastCleanup    time.Time
}

func NewLoginLimiter(maxEntries int) *LoginLimiter {
	if maxEntries <= 0 {
		maxEntries = DefaultLimiterEntries
	}
	return &LoginLimiter{
		accountEntries: make(map[loginLimitKey]loginLimitEntry),
		ipEntries:      make(map[string]loginLimitEntry),
		maxEntries:     maxEntries,
	}
}

func (limiter *LoginLimiter) Check(username, remoteIP string, now time.Time) (time.Duration, bool) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	limiter.cleanupIfDue(now)
	var retryAfter time.Duration
	if entry, ok := limiter.accountEntries[loginLimitKey{username: username, remoteIP: remoteIP}]; ok && now.Before(entry.blockedUntil) {
		retryAfter = entry.blockedUntil.Sub(now)
	}
	if entry, ok := limiter.ipEntries[remoteIP]; ok && now.Before(entry.blockedUntil) {
		if retry := entry.blockedUntil.Sub(now); retry > retryAfter {
			retryAfter = retry
		}
	}
	return retryAfter, retryAfter > 0
}

func (limiter *LoginLimiter) RecordFailure(username, remoteIP string, now time.Time) loginBlockTransitions {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	limiter.cleanupIfDue(now)
	accountKey := loginLimitKey{username: username, remoteIP: remoteIP}
	if _, exists := limiter.accountEntries[accountKey]; !exists && len(limiter.accountEntries) >= limiter.maxEntries {
		removeOne(limiter.accountEntries)
	}
	accountEntry, accountBlocked := recordFailure(limiter.accountEntries[accountKey], now, AccountFailureLimit)
	limiter.accountEntries[accountKey] = accountEntry

	if _, exists := limiter.ipEntries[remoteIP]; !exists && len(limiter.ipEntries) >= limiter.maxEntries {
		removeOne(limiter.ipEntries)
	}
	ipEntry, ipBlocked := recordFailure(limiter.ipEntries[remoteIP], now, IPFailureLimit)
	limiter.ipEntries[remoteIP] = ipEntry

	var transitions loginBlockTransitions
	if accountBlocked {
		transitions |= accountBlockTransition
	}
	if ipBlocked {
		transitions |= ipBlockTransition
	}
	return transitions
}

func (limiter *LoginLimiter) Clear(username, remoteIP string) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	// Aggregate peer evidence deliberately survives an individual successful
	// login so username spraying cannot be erased one account at a time.
	delete(limiter.accountEntries, loginLimitKey{username: username, remoteIP: remoteIP})
}

func (limiter *LoginLimiter) removeStale(now time.Time) {
	staleBefore := now.Add(-(LoginBlockDuration + LoginFailureWindow))
	for key, entry := range limiter.accountEntries {
		if entry.lastSeen.Before(staleBefore) && !now.Before(entry.blockedUntil) {
			delete(limiter.accountEntries, key)
		}
	}
	for key, entry := range limiter.ipEntries {
		if entry.lastSeen.Before(staleBefore) && !now.Before(entry.blockedUntil) {
			delete(limiter.ipEntries, key)
		}
	}
}

func (limiter *LoginLimiter) cleanupIfDue(now time.Time) {
	if !limiter.lastCleanup.IsZero() && now.Sub(limiter.lastCleanup) < time.Minute {
		return
	}
	limiter.removeStale(now)
	limiter.lastCleanup = now
}

func recordFailure(entry loginLimitEntry, now time.Time, limit int) (loginLimitEntry, bool) {
	if entry.windowStarted.IsZero() || now.Sub(entry.windowStarted) > LoginFailureWindow {
		entry.windowStarted = now
		entry.failures = 0
		entry.blockedUntil = time.Time{}
	}
	entry.failures++
	entry.lastSeen = now
	newlyBlocked := entry.failures == limit
	if entry.failures >= limit {
		entry.blockedUntil = now.Add(LoginBlockDuration)
	}
	return entry, newlyBlocked
}

func removeOne[K comparable](entries map[K]loginLimitEntry) {
	for key := range entries {
		delete(entries, key)
		return
	}
}
