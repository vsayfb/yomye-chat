package smoke

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const (
	ManifestVersion = 1
	CohortSize      = 15
	maxManifestSize = 256 << 10
)

type Manifest struct {
	Version   int       `json:"version"`
	RunID     string    `json:"run_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Users     []User    `json:"users"`
}

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccessToken string `json:"access_token"`
}

type accessClaims struct {
	TokenUse string `json:"token_use"`
	gojwt.RegisteredClaims
}

func DecodeManifest(r io.Reader, runID string, now time.Time) (*Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxManifestSize+1))
	if err != nil {
		return nil, errors.New("read credential manifest")
	}
	if len(data) > maxManifestSize {
		return nil, errors.New("credential manifest exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode credential manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("decode credential manifest: %w", err)
	}
	if manifest.Version != ManifestVersion {
		return nil, fmt.Errorf("unsupported credential manifest version: %d", manifest.Version)
	}
	if !runIDPattern.MatchString(runID) {
		return nil, errors.New("--run-id must be 1-128 safe technical characters")
	}
	if manifest.RunID != runID {
		return nil, errors.New("credential manifest run_id does not match --run-id")
	}
	if manifest.ExpiresAt.IsZero() || !manifest.ExpiresAt.After(now) {
		return nil, errors.New("credential manifest is expired")
	}
	if len(manifest.Users) != CohortSize {
		return nil, fmt.Errorf("credential manifest must contain exactly %d users", CohortSize)
	}

	ids := make(map[string]struct{}, CohortSize)
	tokens := make(map[string]struct{}, CohortSize)
	for i := range manifest.Users {
		user := &manifest.Users[i]
		parsed, err := uuid.Parse(user.ID)
		if err != nil || parsed == uuid.Nil {
			return nil, fmt.Errorf("credential manifest user %d has invalid UUID", i)
		}
		canonicalID := parsed.String()
		if _, exists := ids[canonicalID]; exists {
			return nil, fmt.Errorf("credential manifest user %d duplicates an ID", i)
		}
		user.ID = canonicalID
		ids[canonicalID] = struct{}{}
		if user.Name == "" {
			return nil, fmt.Errorf("credential manifest user %d has an empty name", i)
		}
		if user.AccessToken == "" {
			return nil, fmt.Errorf("credential manifest user %d has an empty access token", i)
		}
		if _, exists := tokens[user.AccessToken]; exists {
			return nil, fmt.Errorf("credential manifest user %d duplicates an access token", i)
		}
		tokens[user.AccessToken] = struct{}{}
		if err := validateAccessToken(user.AccessToken, canonicalID, manifest.ExpiresAt, now); err != nil {
			return nil, fmt.Errorf("credential manifest user %d has invalid access token contract", i)
		}
	}

	return &manifest, nil
}

func validateAccessToken(tokenValue, userID string, manifestExpiry, now time.Time) error {
	claims := new(accessClaims)
	token, _, err := gojwt.NewParser().ParseUnverified(tokenValue, claims)
	if err != nil || token == nil || token.Method.Alg() != gojwt.SigningMethodHS256.Alg() {
		return errors.New("invalid access JWT")
	}
	if claims.Issuer != "yevmiye-core" || claims.TokenUse != "access" ||
		claims.Subject != userID || claims.ExpiresAt == nil ||
		!claims.ExpiresAt.Time.After(now) || claims.ExpiresAt.Time.Add(time.Second).Before(manifestExpiry) {
		return errors.New("invalid access JWT claims")
	}
	return nil
}
