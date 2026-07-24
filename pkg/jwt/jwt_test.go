package jwt

import (
	"errors"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

func TestVerifyAcceptsValidCoreAccessToken(t *testing.T) {
	t.Parallel()

	service := New("test-secret")
	token := signedToken(t, "test-secret", gojwt.SigningMethodHS256, time.Now().Add(time.Minute))

	claims, err := service.Verify(token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "user-1" {
		t.Fatalf("Subject = %q, want user-1", claims.Subject)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	service := New("test-secret")
	token := signedToken(t, "test-secret", gojwt.SigningMethodHS256, time.Now().Add(-time.Minute))

	if _, err := service.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRequiresExpiration(t *testing.T) {
	t.Parallel()

	claims := Claims{
		RegisteredClaims: gojwt.RegisteredClaims{Subject: "user-1"},
	}
	token, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := New("test-secret").Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsOtherHMACAlgorithms(t *testing.T) {
	t.Parallel()

	service := New("test-secret")
	token := signedToken(t, "test-secret", gojwt.SigningMethodHS512, time.Now().Add(time.Minute))

	if _, err := service.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
	}
}

func signedToken(
	t *testing.T,
	secret string,
	method gojwt.SigningMethod,
	expiresAt time.Time,
) string {
	t.Helper()

	claims := Claims{
		RegisteredClaims: gojwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: gojwt.NewNumericDate(expiresAt),
		},
	}
	token, err := gojwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
