package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://idp.example.com"
	testAudience = "https://mcp.example.com"
	testMetadata = "https://mcp.example.com/.well-known/oauth-protected-resource"
	testKID      = "test-key-1"
)

func setupTestJWKS(t *testing.T) (jwksURL string, key *rsa.PrivateKey, kid string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}

	jwksJSON := buildJWKSJSON(key, testKID)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksJSON)
	}))
	t.Cleanup(srv.Close)

	return srv.URL, key, testKID
}

func buildJWKSJSON(key *rsa.PrivateKey, kid string) []byte {
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())

	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   n,
				"e":   e,
			},
		},
	}
	data, _ := json.Marshal(jwks)
	return data
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":   testIssuer,
		"aud":   testAudience,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"sub":   "user@example.com",
		"email": "user@example.com",
	}
}

func newTestValidator(t *testing.T, jwksURL string) *JWTValidator {
	t.Helper()
	v, err := NewJWTValidator(context.Background(), jwksURL, testIssuer, testAudience, testMetadata, nil, slog.Default())
	if err != nil {
		t.Fatalf("creating validator: %v", err)
	}
	t.Cleanup(v.Close)
	return v
}

func TestValidToken(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	token := signToken(t, key, kid, validClaims())

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestMissingAuthorizationHeader(t *testing.T) {
	jwksURL, _, _ := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestNonBearerScheme(t *testing.T) {
	jwksURL, _, _ := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Basic abc123")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestEmptyBearerToken(t *testing.T) {
	jwksURL, _, _ := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestExpiredToken(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	claims := validClaims()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	token := signToken(t, key, kid, claims)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMissingExpClaim(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	claims := jwt.MapClaims{
		"iss": testIssuer,
		"aud": testAudience,
		"sub": "user@example.com",
	}
	token := signToken(t, key, kid, claims)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestWrongIssuer(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	claims := validClaims()
	claims["iss"] = "https://evil.com"
	token := signToken(t, key, kid, claims)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestWrongAudience(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	claims := validClaims()
	claims["aud"] = "https://other-service.com"
	token := signToken(t, key, kid, claims)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestInvalidSignature(t *testing.T) {
	jwksURL, _, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	// Sign with a different key
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	token := signToken(t, otherKey, kid, validClaims())

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestWWWAuthenticateHeader(t *testing.T) {
	jwksURL, _, _ := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	want := fmt.Sprintf(`Bearer resource_metadata="%s"`, testMetadata)
	if got := w.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

func TestClaimsInContext(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	v := newTestValidator(t, jwksURL)

	token := signToken(t, key, kid, validClaims())

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			t.Error("claims should not be nil")
			return
		}
		mc, ok := claims.(jwt.MapClaims)
		if !ok {
			t.Error("claims should be MapClaims")
			return
		}
		if mc["sub"] != "user@example.com" {
			t.Errorf("sub = %v, want user@example.com", mc["sub"])
		}
		if mc["email"] != "user@example.com" {
			t.Errorf("email = %v, want user@example.com", mc["email"])
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name    string
		auth    string
		want    string
		wantErr bool
	}{
		{name: "valid", auth: "Bearer abc123", want: "abc123"},
		{name: "missing", auth: "", wantErr: true},
		{name: "non-bearer", auth: "Basic abc123", wantErr: true},
		{name: "empty token", auth: "Bearer ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			got, err := extractBearerToken(req)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("token = %q, want %q", got, tt.want)
			}
		})
	}
}
