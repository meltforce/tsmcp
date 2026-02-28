package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testMetadata = "https://mcp.example.com/.well-known/oauth-protected-resource"
	testClientID = "test-client"
	testSecret   = "test-secret"
)

func setupIntrospectionServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func activeResponse(sub string, exp int64) map[string]any {
	return map[string]any{
		"active":     true,
		"sub":        sub,
		"client_id":  testClientID,
		"token_type": "bearer",
		"exp":        exp,
	}
}

func inactiveResponse() map[string]any {
	return map[string]any{"active": false}
}

func newTestValidator(t *testing.T, introspectionURL string) *IntrospectionValidator {
	t.Helper()
	v := NewIntrospectionValidator(introspectionURL, testClientID, testSecret, testMetadata, "", "", nil, slog.Default())
	t.Cleanup(v.Close)
	return v
}

func TestActiveToken(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})
	v := newTestValidator(t, srv.URL)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer opaque-token-123")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestInactiveToken(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inactiveResponse())
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer revoked-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMissingAuthorizationHeader(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("introspection should not be called")
	})
	v := newTestValidator(t, srv.URL)

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
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("introspection should not be called")
	})
	v := newTestValidator(t, srv.URL)

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
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("introspection should not be called")
	})
	v := newTestValidator(t, srv.URL)

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

func TestWWWAuthenticateHeader(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inactiveResponse())
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	want := fmt.Sprintf(`Bearer resource_metadata="%s"`, testMetadata)
	if got := w.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

func TestClaimsInContext(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			t.Error("claims should not be nil")
			return
		}
		if claims.Sub != "user@example.com" {
			t.Errorf("sub = %v, want user@example.com", claims.Sub)
		}
		if claims.ClientID != testClientID {
			t.Errorf("client_id = %v, want %s", claims.ClientID, testClientID)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer opaque-token-456")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestIntrospectionEndpointError(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestCacheHit(t *testing.T) {
	var calls atomic.Int32
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// First request — cache miss
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer cached-token")
	w := httptest.NewRecorder()
	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("first request: status = %d, want 200", w.Code)
	}

	// Second request — cache hit
	req = httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer cached-token")
	w = httptest.NewRecorder()
	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("second request: status = %d, want 200", w.Code)
	}

	if c := calls.Load(); c != 1 {
		t.Errorf("introspection calls = %d, want 1 (second should be cached)", c)
	}
}

func TestInactiveTokenNotCached(t *testing.T) {
	var calls atomic.Int32
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inactiveResponse())
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	// Two requests with same inactive token
	for i := range 2 {
		req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
		req.Header.Set("Authorization", "Bearer inactive-token")
		w := httptest.NewRecorder()
		v.Middleware()(next).ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("request %d: status = %d, want 401", i+1, w.Code)
		}
	}

	if c := calls.Load(); c != 2 {
		t.Errorf("introspection calls = %d, want 2 (inactive tokens should not be cached)", c)
	}
}

func TestBasicAuthSentToIntrospectionEndpoint(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != testClientID || pass != testSecret {
			t.Errorf("basic auth = (%q, %q, %v), want (%q, %q, true)", user, pass, ok, testClientID, testSecret)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})
	v := newTestValidator(t, srv.URL)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer auth-test-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAudienceContains(t *testing.T) {
	tests := []struct {
		name string
		aud  Audience
		s    string
		want bool
	}{
		{name: "single match", aud: Audience{"https://mcp.example.com"}, s: "https://mcp.example.com", want: true},
		{name: "multi match", aud: Audience{"https://a.com", "https://b.com"}, s: "https://b.com", want: true},
		{name: "no match", aud: Audience{"https://a.com"}, s: "https://b.com", want: false},
		{name: "empty audience", aud: Audience{}, s: "https://a.com", want: false},
		{name: "nil audience", aud: nil, s: "https://a.com", want: false},
		{name: "trailing slash in aud", aud: Audience{"https://mcp.example.com/"}, s: "https://mcp.example.com", want: true},
		{name: "trailing slash in expected", aud: Audience{"https://mcp.example.com"}, s: "https://mcp.example.com/", want: true},
		{name: "trailing slash in both", aud: Audience{"https://mcp.example.com/"}, s: "https://mcp.example.com/", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.aud.Contains(tt.s); got != tt.want {
				t.Errorf("Contains(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestAudienceMismatchRejected(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"active":     true,
			"sub":        "user@example.com",
			"aud":        []string{"https://other-resource.com"},
			"client_id":  testClientID,
			"token_type": "bearer",
			"exp":        time.Now().Add(time.Hour).Unix(),
		})
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "https://mcp.example.com", "", nil, slog.Default())
	t.Cleanup(v.Close)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer aud-mismatch-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAudienceMatchAccepted(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"active":     true,
			"sub":        "user@example.com",
			"aud":        []string{"https://mcp.example.com", "https://other.com"},
			"client_id":  testClientID,
			"token_type": "bearer",
			"exp":        time.Now().Add(time.Hour).Unix(),
		})
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "https://mcp.example.com", "", nil, slog.Default())
	t.Cleanup(v.Close)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer aud-match-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestMissingAudienceRejectedWhenExpected(t *testing.T) {
	// Introspection response has no aud — should be rejected when expectedAudience is set (fail-closed)
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "https://mcp.example.com", "", nil, slog.Default())
	t.Cleanup(v.Close)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer no-aud-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestNoExpectedAudienceSkipsCheck(t *testing.T) {
	// No expectedAudience configured — any token audience is allowed
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "", "", nil, slog.Default())
	t.Cleanup(v.Close)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer any-aud-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestIssuerMismatchRejected(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"active":     true,
			"sub":        "user@example.com",
			"iss":        "https://wrong-issuer.com",
			"client_id":  testClientID,
			"token_type": "bearer",
			"exp":        time.Now().Add(time.Hour).Unix(),
		})
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "", "https://expected-issuer.com", nil, slog.Default())
	t.Cleanup(v.Close)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer iss-mismatch-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMissingIssuerRejectedWhenExpected(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(activeResponse("user@example.com", time.Now().Add(time.Hour).Unix()))
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "", "https://expected-issuer.com", nil, slog.Default())
	t.Cleanup(v.Close)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer no-iss-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestIssuerMatchAccepted(t *testing.T) {
	srv := setupIntrospectionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"active":     true,
			"sub":        "user@example.com",
			"iss":        "https://expected-issuer.com",
			"client_id":  testClientID,
			"token_type": "bearer",
			"exp":        time.Now().Add(time.Hour).Unix(),
		})
	})

	v := NewIntrospectionValidator(srv.URL, testClientID, testSecret, testMetadata, "", "https://expected-issuer.com", nil, slog.Default())
	t.Cleanup(v.Close)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", nil)
	req.Header.Set("Authorization", "Bearer iss-match-token")
	w := httptest.NewRecorder()

	v.Middleware()(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
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
		{name: "lowercase bearer", auth: "bearer abc123", want: "abc123"},
		{name: "uppercase bearer", auth: "BEARER abc123", want: "abc123"},
		{name: "mixed case bearer", auth: "bEaReR abc123", want: "abc123"},
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
