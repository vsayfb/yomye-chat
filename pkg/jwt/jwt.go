package jwt

import (
	"errors"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	coreIssuer     = "yevmiye-core"
	accessTokenUse = "access"
)

var ErrInvalidToken = errors.New("invalid token")

type Service struct {
	secret []byte
}

func New(secret string) *Service {
	return &Service{secret: []byte(secret)}
}

type Claims struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	TokenUse  string `json:"token_use"`
	jwt.RegisteredClaims
}

func (s *Service) Verify(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		tokenStr,
		&Claims{},
		func(_ *jwt.Token) (any, error) {
			return s.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(coreIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || claims.TokenUse != accessTokenUse {
		return nil, ErrInvalidToken
	}

	subject, err := uuid.Parse(claims.Subject)
	if err != nil || subject == uuid.Nil {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
