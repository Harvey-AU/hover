package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVars map[string]string
		wantErr bool
		errMsg  string
	}{
		{
			name: "all_env_vars_set",
			envVars: map[string]string{
				"SUPABASE_AUTH_URL":        "https://test.supabase.co",
				"SUPABASE_PUBLISHABLE_KEY": "sb_publishable_test_key",
			},
			wantErr: false,
		},
		{
			name: "missing_url",
			envVars: map[string]string{
				"SUPABASE_PUBLISHABLE_KEY": "sb_publishable_test_key",
			},
			wantErr: true,
			errMsg:  "SUPABASE_AUTH_URL environment variable is required",
		},
		{
			name: "missing_publishable_key",
			envVars: map[string]string{
				"SUPABASE_AUTH_URL": "https://test.supabase.co",
			},
			wantErr: true,
			errMsg:  "SUPABASE_PUBLISHABLE_KEY environment variable is required",
		},
		{
			name:    "all_missing",
			envVars: map[string]string{},
			wantErr: true,
			errMsg:  "SUPABASE_AUTH_URL environment variable is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(os.Clearenv)

			for key, value := range tt.envVars {
				require.NoError(t, os.Setenv(key, value))
			}

			config, err := NewConfigFromEnv()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, config)
				if tt.errMsg != "" {
					assert.EqualError(t, err, tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				assert.Equal(t, tt.envVars["SUPABASE_AUTH_URL"], config.AuthURL)
				assert.Equal(t, tt.envVars["SUPABASE_PUBLISHABLE_KEY"], config.PublishableKey)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid_config",
			config: &Config{
				AuthURL:        "https://test.supabase.co",
				PublishableKey: "sb_publishable_test_key",
			},
			wantErr: false,
		},
		{
			name: "missing_url",
			config: &Config{
				PublishableKey: "sb_publishable_test_key",
			},
			wantErr: true,
			errMsg:  "AuthURL is required",
		},
		{
			name: "missing_publishable_key",
			config: &Config{
				AuthURL: "https://test.supabase.co",
			},
			wantErr: true,
			errMsg:  "PublishableKey is required",
		},
		{
			name:    "empty_config",
			config:  &Config{},
			wantErr: true,
			errMsg:  "AuthURL is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.EqualError(t, err, tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateSupabaseTokenRS256(t *testing.T) {
	privKey, kid, supabaseURL, cleanup := startTestJWKS(t)
	defer cleanup()

	tokenString := signTestToken(t, privKey, kid, supabaseURL, []string{"authenticated"}, time.Now().Add(time.Hour))

	claims, err := validateSupabaseToken(context.Background(), tokenString)
	require.NoError(t, err)

	assert.Equal(t, "user-123", claims.UserID)
	assert.Equal(t, "user@example.com", claims.Email)
}

func TestValidateSupabaseTokenES256(t *testing.T) {
	ecPrivKey, kid, supabaseURL, cleanup := startTestJWKSWithES256(t)
	defer cleanup()

	tokenString := signTestTokenES256(t, ecPrivKey, kid, supabaseURL, []string{"authenticated"}, time.Now().Add(time.Hour))

	claims, err := validateSupabaseToken(context.Background(), tokenString)
	require.NoError(t, err)

	assert.Equal(t, "user-123", claims.UserID)
	assert.Equal(t, "user@example.com", claims.Email)
}

func TestValidateSupabaseTokenInvalidAudience(t *testing.T) {
	privKey, kid, supabaseURL, cleanup := startTestJWKS(t)
	defer cleanup()

	tokenString := signTestToken(t, privKey, kid, supabaseURL, []string{"other-service"}, time.Now().Add(time.Hour))

	_, err := validateSupabaseToken(context.Background(), tokenString)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected audience")
}

func TestValidateSupabaseTokenInvalidSignature(t *testing.T) {
	_, kid, supabaseURL, cleanup := startTestJWKS(t)
	defer cleanup()

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tokenString := signTestToken(t, otherKey, kid, supabaseURL, []string{"authenticated"}, time.Now().Add(time.Hour))

	_, err = validateSupabaseToken(context.Background(), tokenString)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestValidateSupabaseTokenContextCancelled(t *testing.T) {
	privKey, kid, supabaseURL, cleanup := startTestJWKS(t)
	defer cleanup()

	tokenString := signTestToken(t, privKey, kid, supabaseURL, []string{"authenticated"}, time.Now().Add(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := validateSupabaseToken(ctx, tokenString)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request context cancelled")
}

func BenchmarkNewConfigFromEnv(b *testing.B) {
	b.Cleanup(os.Clearenv)
	require.NoError(b, os.Setenv("SUPABASE_AUTH_URL", "https://test.supabase.co"))
	require.NoError(b, os.Setenv("SUPABASE_PUBLISHABLE_KEY", "sb_publishable_test_key"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = NewConfigFromEnv()
	}
}

func BenchmarkConfigValidate(b *testing.B) {
	config := &Config{
		AuthURL:        "https://test.supabase.co",
		PublishableKey: "sb_publishable_test_key",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.Validate()
	}
}

func startTestJWKS(tb testing.TB) (*rsa.PrivateKey, string, string, func()) {
	tb.Helper()

	resetJWKSForTest()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(tb, err)

	kid := "test-key"
	publicKey := &privateKey.PublicKey

	n := base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes())

	jwksPayload := struct {
		Keys []map[string]string `json:"keys"`
	}{
		Keys: []map[string]string{
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

	payload, err := json.Marshal(jwksPayload)
	require.NoError(tb, err)

	server := newJWKSHTTPServer(tb, payload)

	supabaseURL := strings.TrimSuffix(server.URL, "/")

	prevURL := os.Getenv("SUPABASE_AUTH_URL")
	prevKey := os.Getenv("SUPABASE_PUBLISHABLE_KEY")

	require.NoError(tb, os.Setenv("SUPABASE_AUTH_URL", supabaseURL))
	require.NoError(tb, os.Setenv("SUPABASE_PUBLISHABLE_KEY", "sb_publishable_test_key"))

	cleanup := func() {
		server.Close()

		if prevURL == "" {
			os.Unsetenv("SUPABASE_AUTH_URL")
		} else {
			_ = os.Setenv("SUPABASE_AUTH_URL", prevURL)
		}

		if prevKey == "" {
			os.Unsetenv("SUPABASE_PUBLISHABLE_KEY")
		} else {
			_ = os.Setenv("SUPABASE_PUBLISHABLE_KEY", prevKey)
		}

		resetJWKSForTest()
	}

	return privateKey, kid, supabaseURL, cleanup
}

func signTestToken(tb testing.TB, privateKey *rsa.PrivateKey, kid, supabaseURL string, audience []string, expiry time.Time) string {
	tb.Helper()

	claims := &UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    fmt.Sprintf("%s/auth/v1", strings.TrimSuffix(supabaseURL, "/")),
			Audience:  jwt.ClaimStrings(audience),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiry),
		},
		UserID: "user-123",
		Email:  "user@example.com",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	signed, err := token.SignedString(privateKey)
	require.NoError(tb, err)

	return signed
}

func startTestJWKSWithES256(tb testing.TB) (*ecdsa.PrivateKey, string, string, func()) {
	tb.Helper()

	resetJWKSForTest()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(tb, err)

	kid := "test-key-es256"
	publicKey := &privateKey.PublicKey

	// Encode EC public key coordinates
	x := base64.RawURLEncoding.EncodeToString(publicKey.X.Bytes()) //nolint:staticcheck // SA1019: no alternative for JWKS coordinate encoding
	y := base64.RawURLEncoding.EncodeToString(publicKey.Y.Bytes()) //nolint:staticcheck // SA1019: no alternative for JWKS coordinate encoding

	jwksPayload := struct {
		Keys []map[string]string `json:"keys"`
	}{
		Keys: []map[string]string{
			{
				"kty": "EC",
				"crv": "P-256",
				"alg": "ES256",
				"use": "sig",
				"kid": kid,
				"x":   x,
				"y":   y,
			},
		},
	}

	payload, err := json.Marshal(jwksPayload)
	require.NoError(tb, err)

	server := newJWKSHTTPServer(tb, payload)

	supabaseURL := strings.TrimSuffix(server.URL, "/")

	prevURL := os.Getenv("SUPABASE_AUTH_URL")
	prevKey := os.Getenv("SUPABASE_PUBLISHABLE_KEY")

	require.NoError(tb, os.Setenv("SUPABASE_AUTH_URL", supabaseURL))
	require.NoError(tb, os.Setenv("SUPABASE_PUBLISHABLE_KEY", "sb_publishable_test_key"))

	cleanup := func() {
		server.Close()

		if prevURL == "" {
			_ = os.Unsetenv("SUPABASE_AUTH_URL")
		} else {
			_ = os.Setenv("SUPABASE_AUTH_URL", prevURL)
		}

		if prevKey == "" {
			_ = os.Unsetenv("SUPABASE_PUBLISHABLE_KEY")
		} else {
			_ = os.Setenv("SUPABASE_PUBLISHABLE_KEY", prevKey)
		}

		resetJWKSForTest()
	}

	return privateKey, kid, supabaseURL, cleanup
}

func signTestTokenES256(tb testing.TB, privateKey *ecdsa.PrivateKey, kid, supabaseURL string, audience []string, expiry time.Time) string {
	tb.Helper()

	claims := &UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    fmt.Sprintf("%s/auth/v1", strings.TrimSuffix(supabaseURL, "/")),
			Audience:  jwt.ClaimStrings(audience),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiry),
		},
		UserID: "user-123",
		Email:  "user@example.com",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = kid

	signed, err := token.SignedString(privateKey)
	require.NoError(tb, err)

	return signed
}

func newJWKSHTTPServer(tb testing.TB, payload []byte) *httptest.Server {
	tb.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if _, writeErr := w.Write(payload); writeErr != nil {
			tb.Logf("failed to write JWKS payload: %v", writeErr)
		}
	})

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if runtime.GOOS == "windows" {
			tb.Skipf("skipping JWKS server on Windows: %v", err)
		}
		require.NoError(tb, err)
	}

	server := &httptest.Server{
		Listener: listener,
		Config: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	server.Start()

	return server
}
