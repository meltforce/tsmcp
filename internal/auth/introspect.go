package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type contextKey int

const claimsKey contextKey = 0

// IntrospectionClaims holds the fields returned by an RFC 7662 introspection response.
type IntrospectionClaims struct {
	Active    bool     `json:"active"`
	Sub       string   `json:"sub,omitempty"`
	Aud       Audience `json:"aud,omitempty"`
	Iss       string   `json:"iss,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	ClientID  string   `json:"client_id,omitempty"`
	Username  string   `json:"username,omitempty"`
	TokenType string   `json:"token_type,omitempty"`
	Exp       int64    `json:"exp,omitempty"`
	Iat       int64    `json:"iat,omitempty"`
}

// Audience handles the "aud" claim which can be a string or an array of strings.
type Audience []string

func (a *Audience) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = Audience{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	*a = Audience(arr)
	return nil
}

type cacheEntry struct {
	claims  *IntrospectionClaims
	expires time.Time
}

// IntrospectionValidator validates opaque tokens by calling an RFC 7662 introspection endpoint.
type IntrospectionValidator struct {
	introspectionURL    string
	clientID            string
	clientSecret        string
	resourceMetadataURL string
	client              *http.Client
	logger              *slog.Logger

	mu    sync.Mutex
	cache map[string]*cacheEntry
}

// NewIntrospectionValidator creates a validator that checks tokens against an introspection endpoint.
// If transport is non-nil, introspection requests use it (e.g. tsnet); nil falls back to the default HTTP client.
func NewIntrospectionValidator(introspectionURL, clientID, clientSecret, resourceMetadataURL string, transport http.RoundTripper, logger *slog.Logger) *IntrospectionValidator {
	client := http.DefaultClient
	if transport != nil {
		client = &http.Client{Transport: transport}
	}
	return &IntrospectionValidator{
		introspectionURL:    introspectionURL,
		clientID:            clientID,
		clientSecret:        clientSecret,
		resourceMetadataURL: resourceMetadataURL,
		client:              client,
		logger:              logger,
		cache:               make(map[string]*cacheEntry),
	}
}

// Close clears the introspection cache.
func (v *IntrospectionValidator) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cache = make(map[string]*cacheEntry)
}

// Middleware returns HTTP middleware that validates Bearer tokens via introspection.
func (v *IntrospectionValidator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := extractBearerToken(r)
			if err != nil {
				v.unauthorized(w, err.Error())
				return
			}

			claims, err := v.introspect(r.Context(), token)
			if err != nil {
				v.logger.Warn("introspection failed", "error", err)
				v.unauthorized(w, "token validation failed")
				return
			}
			if !claims.Active {
				v.unauthorized(w, "token is not active")
				return
			}

			v.logger.Info("authenticated request", "sub", claims.Sub, "username", claims.Username, "client_id", claims.ClientID, "path", r.URL.Path)
			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (v *IntrospectionValidator) introspect(ctx context.Context, token string) (*IntrospectionClaims, error) {
	// Check cache
	v.mu.Lock()
	if entry, ok := v.cache[token]; ok {
		if time.Now().Before(entry.expires) {
			claims := entry.claims
			v.mu.Unlock()
			return claims, nil
		}
		delete(v.cache, token)
	}
	v.mu.Unlock()

	// POST to introspection endpoint
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.introspectionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating introspection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if v.clientID != "" {
		req.SetBasicAuth(v.clientID, v.clientSecret)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling introspection endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspection endpoint returned %d", resp.StatusCode)
	}

	var claims IntrospectionClaims
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return nil, fmt.Errorf("decoding introspection response: %w", err)
	}

	// Cache active tokens only
	if claims.Active {
		ttl := 60 * time.Second
		if claims.Exp > 0 {
			if untilExp := time.Until(time.Unix(claims.Exp, 0)); untilExp < ttl {
				ttl = untilExp
			}
		}
		if ttl > 0 {
			v.mu.Lock()
			v.cache[token] = &cacheEntry{
				claims:  &claims,
				expires: time.Now().Add(ttl),
			}
			v.mu.Unlock()
		}
	}

	return &claims, nil
}

func (v *IntrospectionValidator) unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, v.resourceMetadataURL))
	http.Error(w, msg, http.StatusUnauthorized)
}

// ClaimsFromContext returns the introspection claims stored in the request context by the middleware.
func ClaimsFromContext(ctx context.Context) *IntrospectionClaims {
	claims, _ := ctx.Value(claimsKey).(*IntrospectionClaims)
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
