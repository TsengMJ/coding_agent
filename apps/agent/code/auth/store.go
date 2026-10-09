package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNoSession is returned for an unknown, revoked or expired session token.
var ErrNoSession = errors.New("no valid session; sign in again")

// Record is a stored user with the GitHub token still encrypted.
type Record struct {
	ID             string
	Name           string
	EncryptedToken []byte
}

// Store keeps users and sessions. Session tokens are stored as SHA-256
// hashes, so a database dump does not hand out sessions.
type Store interface {
	PutUser(ctx context.Context, r Record) error
	CreateSession(ctx context.Context, tokenHash, userID string, expires time.Time) error
	// LookupSession returns ErrNoSession unless the session exists and has
	// not expired at now.
	LookupSession(ctx context.Context, tokenHash string, now time.Time) (*Record, error)
	DeleteSession(ctx context.Context, tokenHash string) error
}

// HashToken is how session tokens are stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewSessionToken returns a random bearer token.
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: session token: %w", err)
	}
	return "qs_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// Cipher encrypts GitHub tokens at rest with AES-256-GCM.
type Cipher struct{ aead cipher.AEAD }

// SecretKeyEnvVar holds the 32-byte key, base64 or hex encoded.
const SecretKeyEnvVar = "QUILL_SECRET_KEY"

// NewCipher parses a 32-byte key given as base64 or hex.
func NewCipher(key string) (*Cipher, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		raw, err = hex.DecodeString(key)
	}
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("auth: %s must be 32 bytes, base64 or hex encoded (e.g. `openssl rand -base64 32`)", SecretKeyEnvVar)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plain string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (c *Cipher) Open(sealed []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("auth: sealed token is too short")
	}
	plain, err := c.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("auth: decrypt token (was %s changed?): %w", SecretKeyEnvVar, err)
	}
	return string(plain), nil
}

// MemoryStore keeps users and sessions in process memory, for tests.
type MemoryStore struct {
	mu       sync.Mutex
	users    map[string]Record
	sessions map[string]memorySession
}

type memorySession struct {
	userID  string
	expires time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{users: map[string]Record{}, sessions: map[string]memorySession{}}
}

func (s *MemoryStore) PutUser(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[r.ID] = r
	return nil
}

func (s *MemoryStore) CreateSession(_ context.Context, hash, userID string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[hash] = memorySession{userID: userID, expires: expires}
	return nil
}

func (s *MemoryStore) LookupSession(_ context.Context, hash string, now time.Time) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[hash]
	if !ok || !now.Before(sess.expires) {
		return nil, ErrNoSession
	}
	r, ok := s.users[sess.userID]
	if !ok {
		return nil, ErrNoSession
	}
	return &r, nil
}

func (s *MemoryStore) DeleteSession(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}
