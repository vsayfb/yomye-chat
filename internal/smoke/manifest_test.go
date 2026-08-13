package smoke

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestDecodeManifestAcceptsExactlyFifteenDistinctUsers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	manifest := validManifest(now)

	got, err := DecodeManifest(encodeManifest(t, manifest), manifest.RunID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ManifestVersion || len(got.Users) != CohortSize {
		t.Fatalf("DecodeManifest() = version %d users %d", got.Version, len(got.Users))
	}
}

func TestDecodeManifestRejectsRunIDMismatchExpiryAndInvalidCohort(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		mutate   func(*Manifest)
		cliRunID string
		want     string
	}{
		{name: "run ID mismatch", cliRunID: "other", want: "does not match"},
		{name: "expired", cliRunID: "deployment-sha", mutate: func(m *Manifest) { m.ExpiresAt = now }, want: "expired"},
		{name: "too few users", cliRunID: "deployment-sha", mutate: func(m *Manifest) { m.Users = m.Users[:14] }, want: "exactly 15"},
		{name: "duplicate user", cliRunID: "deployment-sha", mutate: func(m *Manifest) { m.Users[14].ID = m.Users[0].ID }, want: "duplicates an ID"},
		{name: "duplicate token", cliRunID: "deployment-sha", mutate: func(m *Manifest) { m.Users[14].AccessToken = m.Users[0].AccessToken }, want: "duplicates an access token"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := validManifest(now)
			if test.mutate != nil {
				test.mutate(&manifest)
			}
			_, err := DecodeManifest(encodeManifest(t, manifest), test.cliRunID, now)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	input := `{"version":1,"run_id":"deployment-sha","expires_at":"2099-01-01T00:00:00Z","users":[],"unexpected":"value"}`
	if _, err := DecodeManifest(strings.NewReader(input), "deployment-sha", time.Now()); err == nil {
		t.Fatal("DecodeManifest() accepted an unknown field")
	}
}

func TestDecodeManifestRejectsWrongAccessTokenContractWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	manifest := validManifest(now)
	secretToken := manifest.Users[0].AccessToken
	claims := accessClaims{
		TokenUse: "refresh",
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer: "yevmiye-core", Subject: manifest.Users[0].ID,
			ExpiresAt: gojwt.NewNumericDate(manifest.ExpiresAt.Add(time.Minute)),
		},
	}
	invalid, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Users[0].AccessToken = invalid
	_, err = DecodeManifest(encodeManifest(t, manifest), manifest.RunID, now)
	if err == nil || !strings.Contains(err.Error(), "invalid access token contract") {
		t.Fatalf("DecodeManifest() error = %v", err)
	}
	if strings.Contains(err.Error(), invalid) || strings.Contains(err.Error(), secretToken) {
		t.Fatal("DecodeManifest() exposed an access token")
	}
}

func TestRedactRemovesCredentialsTicketsAndMessageBodies(t *testing.T) {
	t.Parallel()
	input := `Authorization: Bearer eyJ.secret.signature GET /ws/direct?ticket=abcdef&targetID=safe {"access_token":"json-secret","ticket":"json-ticket","content":"özel mesaj"}`
	got := Redact(input)
	for _, secret := range []string{"eyJ.secret.signature", "abcdef", "json-secret", "json-ticket", "özel mesaj"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Redact() retained sensitive value %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "targetID=safe") {
		t.Fatalf("Redact() removed safe diagnostic context: %s", got)
	}
}

func validManifest(now time.Time) Manifest {
	manifest := Manifest{
		Version: ManifestVersion, RunID: "deployment-sha",
		ExpiresAt: now.Add(15 * time.Minute),
		Users:     make([]User, CohortSize),
	}
	for i := range manifest.Users {
		userID := uuid.NewString()
		claims := accessClaims{
			TokenUse: "access",
			RegisteredClaims: gojwt.RegisteredClaims{
				Issuer: "yevmiye-core", Subject: userID,
				ExpiresAt: gojwt.NewNumericDate(manifest.ExpiresAt.Add(time.Minute)),
			},
		}
		token, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
		if err != nil {
			panic(err)
		}
		manifest.Users[i] = User{
			ID: userID, Name: "Smoke User", AccessToken: token,
		}
	}
	return manifest
}

func encodeManifest(t *testing.T, manifest Manifest) *bytes.Reader {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}
