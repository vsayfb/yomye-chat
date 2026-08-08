package wsticket

import (
	"errors"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
)

func TestTicketIsOneTimeAndCarriesClaims(t *testing.T) {
	t.Parallel()

	store := NewStore(time.Minute)
	claims := &jwt.Claims{
		Name:      "User",
		AvatarURL: "avatar",
		RegisteredClaims: gojwt.RegisteredClaims{
			Subject:   "9e2a9df0-ea28-433a-8b8b-920cb86eb62d",
			ExpiresAt: gojwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}

	ticket, _, err := store.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket) != 64 {
		t.Fatalf("ticket length = %d, want 64", len(ticket))
	}

	got, err := store.Consume(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != claims.Subject || got.Name != claims.Name || got.AvatarURL != claims.AvatarURL {
		t.Fatalf("consumed claims = %+v, want %+v", got, claims)
	}

	if _, err := store.Consume(ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("second Consume() error = %v, want ErrInvalidTicket", err)
	}
}

func TestTicketExpiryCannotOutliveAccessToken(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	store := NewStore(time.Minute)
	store.now = func() time.Time { return now }
	claims := &jwt.Claims{RegisteredClaims: gojwt.RegisteredClaims{
		ExpiresAt: gojwt.NewNumericDate(now.Add(10 * time.Second)),
	}}

	_, expiresAt, err := store.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(claims.ExpiresAt.Time) {
		t.Fatalf("ticket expires at %v, want access-token expiry %v", expiresAt, claims.ExpiresAt.Time)
	}
}

func TestExpiredTicketIsRejected(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	store := NewStore(time.Second)
	store.now = func() time.Time { return now }
	claims := &jwt.Claims{RegisteredClaims: gojwt.RegisteredClaims{
		ExpiresAt: gojwt.NewNumericDate(now.Add(time.Hour)),
	}}

	ticket, _, err := store.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(2 * time.Second) }

	if _, err := store.Consume(ticket); !errors.Is(err, ErrExpiredTicket) {
		t.Fatalf("Consume() error = %v, want ErrExpiredTicket", err)
	}
}
