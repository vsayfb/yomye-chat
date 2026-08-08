package jwt

import (
	"errors"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

const testSubject = "550e8400-e29b-41d4-a716-446655440000"

func TestVerifyAcceptsValidCoreAccessToken(t *testing.T) {
	t.Parallel()

	service := New("test-secret")
	token := signedToken(t, tokenOptions{
		secret:    "test-secret",
		method:    gojwt.SigningMethodHS256,
		expiresAt: time.Now().Add(time.Minute),
		issuer:    coreIssuer,
		tokenUse:  accessTokenUse,
		subject:   testSubject,
	})

	claims, err := service.Verify(token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != testSubject {
		t.Fatalf("Subject = %q, want %q", claims.Subject, testSubject)
	}
	if claims.Issuer != coreIssuer {
		t.Fatalf("Issuer = %q, want %q", claims.Issuer, coreIssuer)
	}
	if claims.TokenUse != accessTokenUse {
		t.Fatalf("TokenUse = %q, want %q", claims.TokenUse, accessTokenUse)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	service := New("test-secret")
	token := signedToken(t, validTokenOptions("test-secret", time.Now().Add(-time.Minute)))

	assertInvalidToken(t, service, token)
}

func TestVerifyRequiresExpiration(t *testing.T) {
	t.Parallel()

	claims := Claims{
		TokenUse: accessTokenUse,
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:  coreIssuer,
			Subject: testSubject,
		},
	}
	token, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}

	assertInvalidToken(t, New("test-secret"), token)
}

func TestVerifyRejectsOtherHMACAlgorithms(t *testing.T) {
	t.Parallel()

	service := New("test-secret")
	options := validTokenOptions("test-secret", time.Now().Add(time.Minute))
	options.method = gojwt.SigningMethodHS512
	token := signedToken(t, options)

	assertInvalidToken(t, service, token)
}

func TestVerifyRejectsWrongSignature(t *testing.T) {
	t.Parallel()

	service := New("expected-secret")
	token := signedToken(t, validTokenOptions("different-secret", time.Now().Add(time.Minute)))

	assertInvalidToken(t, service, token)
}

func TestVerifyRequiresCoreIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issuer string
	}{
		{name: "missing", issuer: ""},
		{name: "wrong", issuer: "another-service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := validTokenOptions("test-secret", time.Now().Add(time.Minute))
			options.issuer = tt.issuer
			token := signedToken(t, options)

			assertInvalidToken(t, New("test-secret"), token)
		})
	}
}

func TestVerifyRequiresAccessTokenUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tokenUse string
	}{
		{name: "missing", tokenUse: ""},
		{name: "wrong", tokenUse: "refresh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := validTokenOptions("test-secret", time.Now().Add(time.Minute))
			options.tokenUse = tt.tokenUse
			token := signedToken(t, options)

			assertInvalidToken(t, New("test-secret"), token)
		})
	}
}

func TestVerifyRequiresValidUUIDSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subject string
	}{
		{name: "missing", subject: ""},
		{name: "not UUID", subject: "user-1"},
		{name: "nil UUID", subject: "00000000-0000-0000-0000-000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := validTokenOptions("test-secret", time.Now().Add(time.Minute))
			options.subject = tt.subject
			token := signedToken(t, options)

			assertInvalidToken(t, New("test-secret"), token)
		})
	}
}

func TestVerifyRejectsOpaqueCoreRefreshToken(t *testing.T) {
	t.Parallel()

	const refreshToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assertInvalidToken(t, New("test-secret"), refreshToken)
}

type tokenOptions struct {
	secret    string
	method    gojwt.SigningMethod
	expiresAt time.Time
	issuer    string
	tokenUse  string
	subject   string
}

func validTokenOptions(secret string, expiresAt time.Time) tokenOptions {
	return tokenOptions{
		secret:    secret,
		method:    gojwt.SigningMethodHS256,
		expiresAt: expiresAt,
		issuer:    coreIssuer,
		tokenUse:  accessTokenUse,
		subject:   testSubject,
	}
}

func signedToken(t *testing.T, options tokenOptions) string {
	t.Helper()

	claims := Claims{
		TokenUse: options.tokenUse,
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    options.issuer,
			Subject:   options.subject,
			ExpiresAt: gojwt.NewNumericDate(options.expiresAt),
		},
	}
	token, err := gojwt.NewWithClaims(options.method, claims).SignedString([]byte(options.secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func assertInvalidToken(t *testing.T, service *Service, token string) {
	t.Helper()

	if _, err := service.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
	}
}
