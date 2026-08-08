package wsticket

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
)

const DefaultTTL = 30 * time.Second

var (
	ErrInvalidTicket = errors.New("invalid websocket ticket")
	ErrExpiredTicket = errors.New("expired websocket ticket")
)

type entry struct {
	claims    jwt.Claims
	expiresAt time.Time
}

type Store struct {
	mu      sync.Mutex
	entries map[[32]byte]entry
	ttl     time.Duration
	now     func() time.Time
}

func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{
		entries: make(map[[32]byte]entry),
		ttl:     ttl,
		now:     time.Now,
	}
}

func (s *Store) Issue(claims *jwt.Claims) (string, time.Time, error) {
	if claims == nil || claims.ExpiresAt == nil {
		return "", time.Time{}, ErrInvalidTicket
	}

	now := s.now()
	expiresAt := now.Add(s.ttl)
	if claims.ExpiresAt.Time.Before(expiresAt) {
		expiresAt = claims.ExpiresAt.Time
	}
	if !expiresAt.After(now) {
		return "", time.Time{}, ErrExpiredTicket
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticket := hex.EncodeToString(raw)
	key := sha256.Sum256([]byte(ticket))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteExpiredLocked(now)
	s.entries[key] = entry{claims: *claims, expiresAt: expiresAt}

	return ticket, expiresAt, nil
}

func (s *Store) Consume(ticket string) (*jwt.Claims, error) {
	if len(ticket) != 64 {
		return nil, ErrInvalidTicket
	}
	if _, err := hex.DecodeString(ticket); err != nil {
		return nil, ErrInvalidTicket
	}

	key := sha256.Sum256([]byte(ticket))
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.entries[key]
	if !ok {
		s.deleteExpiredLocked(now)
		return nil, ErrInvalidTicket
	}
	delete(s.entries, key)
	s.deleteExpiredLocked(now)

	if !item.expiresAt.After(now) {
		return nil, ErrExpiredTicket
	}

	claims := item.claims
	return &claims, nil
}

func (s *Store) deleteExpiredLocked(now time.Time) {
	for key, item := range s.entries {
		if !item.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}
