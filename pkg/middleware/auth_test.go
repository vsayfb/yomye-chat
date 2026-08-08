package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	chatjwt "github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
)

const authTestSubject = "550e8400-e29b-41d4-a716-446655440000"

func TestAuthInjectsVerifiedIdentityAndClaims(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	token := authTestToken(t, secret, time.Now().Add(time.Minute))
	var gotUserID string
	var gotClaims *chatjwt.Claims

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = GetUserID(r.Context())
		gotClaims = GetClaims(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	Auth(chatjwt.New(secret))(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	if gotUserID != authTestSubject {
		t.Fatalf("user ID = %q, want %q", gotUserID, authTestSubject)
	}
	if gotClaims == nil || gotClaims.Subject != authTestSubject || gotClaims.TokenUse != "access" {
		t.Fatalf("claims = %#v, want verified access-token claims", gotClaims)
	}
}

func TestAuthRejectsMissingMalformedAndInvalidCredentials(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	tests := []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "wrong scheme", header: "Basic abc"},
		{name: "empty bearer", header: "Bearer "},
		{name: "invalid JWT", header: "Bearer not-a-jwt"},
		{name: "wrong signature", header: "Bearer " + authTestToken(t, "other-secret", time.Now().Add(time.Minute))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			req := httptest.NewRequest(http.MethodGet, "/private", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rr := httptest.NewRecorder()

			Auth(chatjwt.New(secret))(next).ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
			}
			if called {
				t.Fatal("next handler was called for rejected credentials")
			}
			var body struct {
				Status int `json:"status"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Status != http.StatusUnauthorized {
				t.Fatalf("body status = %d, want %d", body.Status, http.StatusUnauthorized)
			}
		})
	}
}

func TestContextAccessorsFailClosedWithoutAuthMiddleware(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := GetUserID(req.Context()); got != "" {
		t.Fatalf("GetUserID() = %q, want empty string", got)
	}
	if got := GetClaims(req.Context()); got != nil {
		t.Fatalf("GetClaims() = %#v, want nil", got)
	}
}

func authTestToken(t *testing.T, secret string, expiresAt time.Time) string {
	t.Helper()
	claims := chatjwt.Claims{
		TokenUse: "access",
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    "yevmiye-core",
			Subject:   authTestSubject,
			ExpiresAt: gojwt.NewNumericDate(expiresAt),
		},
	}
	token, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
