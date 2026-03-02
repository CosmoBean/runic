package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type Authenticator struct {
	tokenHash string // hex-encoded SHA-256 hash

	// Rate limiting
	failures   map[string]*failureRecord
	mu         sync.Mutex
	rateLimit  int
	lockoutDur time.Duration
}

type failureRecord struct {
	count    int
	firstAt  time.Time
	lockedAt time.Time
}

func New(tokenHash string, rateLimit int, lockoutMinutes int) *Authenticator {
	return &Authenticator{
		tokenHash:  tokenHash,
		failures:   make(map[string]*failureRecord),
		rateLimit:  rateLimit,
		lockoutDur: time.Duration(lockoutMinutes) * time.Minute,
	}
}

// Verify checks a token against the stored hash.
// clientIP is used for rate limiting.
// Returns nil on success, error on failure.
func (a *Authenticator) Verify(token string, clientIP string) error {
	// Check rate limit
	if err := a.checkRateLimit(clientIP); err != nil {
		return err
	}

	// Hash the provided token
	h := sha256.Sum256([]byte(token))
	provided := hex.EncodeToString(h[:])

	// Constant-time comparison
	if !hmac.Equal([]byte(provided), []byte(a.tokenHash)) {
		a.recordFailure(clientIP)
		return fmt.Errorf("invalid token")
	}

	// Success — clear failures for this IP
	a.mu.Lock()
	delete(a.failures, clientIP)
	a.mu.Unlock()

	return nil
}

// HashToken produces the SHA-256 hex hash of a plaintext token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (a *Authenticator) checkRateLimit(ip string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	rec, ok := a.failures[ip]
	if !ok {
		return nil
	}

	// Check if locked out
	if !rec.lockedAt.IsZero() && time.Since(rec.lockedAt) < a.lockoutDur {
		remaining := a.lockoutDur - time.Since(rec.lockedAt)
		return fmt.Errorf("rate limited, retry in %s", remaining.Round(time.Second))
	}

	// Reset if lockout expired
	if !rec.lockedAt.IsZero() && time.Since(rec.lockedAt) >= a.lockoutDur {
		delete(a.failures, ip)
		return nil
	}

	// Reset if window expired (60 seconds)
	if time.Since(rec.firstAt) > 60*time.Second {
		delete(a.failures, ip)
		return nil
	}

	return nil
}

func (a *Authenticator) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	rec, ok := a.failures[ip]
	if !ok {
		rec = &failureRecord{firstAt: time.Now()}
		a.failures[ip] = rec
	}

	rec.count++

	if rec.count >= a.rateLimit {
		rec.lockedAt = time.Now()
	}
}
