package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

type contextKey int

const claimsKey contextKey = 0

// JWTValidator validates JWTs using a JWKS endpoint.
type JWTValidator struct {
	jwks                keyfunc.Keyfunc
	cancel              context.CancelFunc
	issuer              string
	audience            string
	resourceMetadataURL string
	logger              *slog.Logger
}

// NewJWTValidator creates a validator that fetches and caches keys from the JWKS URL.
func NewJWTValidator(ctx context.Context, jwksURL, issuer, audience, resourceMetadataURL string, logger *slog.Logger) (*JWTValidator, error) {
	jwksCtx, cancel := context.WithCancel(ctx)

	jwks, err := keyfunc.NewDefaultCtx(jwksCtx, []string{jwksURL})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("creating JWKS keyfunc: %w", err)
	}

	return &JWTValidator{
		jwks:                jwks,
		cancel:              cancel,
		issuer:              issuer,
		audience:            audience,
		resourceMetadataURL: resourceMetadataURL,
		logger:              logger,
	}, nil
}

// Close shuts down the background JWKS refresh goroutine.
func (v *JWTValidator) Close() {
	v.cancel()
}

// Middleware returns HTTP middleware that validates Bearer tokens on requests.
func (v *JWTValidator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := extractBearerToken(r)
			if err != nil {
				v.unauthorized(w, err.Error())
				return
			}

			parsed, err := jwt.Parse(token, v.jwks.KeyfuncCtx(r.Context()),
				jwt.WithIssuer(v.issuer),
				jwt.WithAudience(v.audience),
				jwt.WithExpirationRequired(),
				jwt.WithValidMethods([]string{"RS256", "ES256", "EdDSA"}),
			)
			if err != nil {
				v.logger.Warn("jwt validation failed", "error", err)
				v.unauthorized(w, "invalid token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, parsed.Claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (v *JWTValidator) unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, v.resourceMetadataURL))
	http.Error(w, msg, http.StatusUnauthorized)
}

// ClaimsFromContext returns the JWT claims stored in the request context by the middleware.
func ClaimsFromContext(ctx context.Context) jwt.Claims {
	claims, _ := ctx.Value(claimsKey).(jwt.Claims)
	return claims
}

func extractBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing authorization header")
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", fmt.Errorf("authorization header must use Bearer scheme")
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return "", fmt.Errorf("bearer token is empty")
	}
	return token, nil
}
