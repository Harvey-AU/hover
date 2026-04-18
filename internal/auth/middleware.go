package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

var authLog = logging.Component("auth")

// AuthClient defines the interface for authentication operations
type AuthClient interface {
	ValidateToken(ctx context.Context, token string) (*UserClaims, error)
	ExtractTokenFromRequest(r *http.Request) (string, error)
	SetUserInContext(r *http.Request, user *UserClaims) *http.Request
}

// SupabaseAuthClient implements AuthClient for Supabase authentication
type SupabaseAuthClient struct{}

// NewSupabaseAuthClient creates a new SupabaseAuthClient
func NewSupabaseAuthClient() *SupabaseAuthClient {
	return &SupabaseAuthClient{}
}

// ValidateToken validates a Supabase JWT token
func (s *SupabaseAuthClient) ValidateToken(ctx context.Context, token string) (*UserClaims, error) {
	return validateSupabaseToken(ctx, token)
}

// ExtractTokenFromRequest extracts a JWT token from the Authorization header
func (s *SupabaseAuthClient) ExtractTokenFromRequest(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("missing or invalid Authorization header")
	}
	return strings.TrimPrefix(authHeader, "Bearer "), nil
}

// SetUserInContext adds user claims to the request context
func (s *SupabaseAuthClient) SetUserInContext(r *http.Request, user *UserClaims) *http.Request {
	ctx := context.WithValue(r.Context(), UserKey, user)
	return r.WithContext(ctx)
}

// UserContextKey is the key used to store user claims in the request context
type UserContextKey string

const (
	UserKey UserContextKey = "user"
)

// UserClaims represents the Supabase JWT claims
type UserClaims struct {
	jwt.RegisteredClaims
	UserID       string         `json:"sub"`
	Email        string         `json:"email"`
	AppMetadata  map[string]any `json:"app_metadata"`
	UserMetadata map[string]any `json:"user_metadata"`
	Role         string         `json:"role"`
}

// AuthMiddleware validates Supabase JWT tokens (uses default SupabaseAuthClient)
func AuthMiddleware(next http.Handler) http.Handler {
	return AuthMiddlewareWithClient(NewSupabaseAuthClient())(next)
}

// AuthMiddlewareWithClient validates JWT tokens using the provided AuthClient
func AuthMiddlewareWithClient(authClient AuthClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract the JWT from the Authorization header
			tokenString, err := authClient.ExtractTokenFromRequest(r)
			if err != nil {
				writeAuthError(w, r, "Missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}

			// Validate the JWT
			claims, err := authClient.ValidateToken(r.Context(), tokenString)
			if err != nil {
				authLog.Warn("JWT validation failed", "error", err, "token_prefix", tokenString[:min(10, len(tokenString))])

				// Determine specific error type and capture critical errors in Sentry
				errorMsg := "Invalid authentication token"
				statusCode := http.StatusUnauthorized

				if strings.Contains(err.Error(), "expired") {
					errorMsg = "Authentication token has expired"
					// Don't capture expired tokens - this is normal user behavior
				} else if strings.Contains(err.Error(), "signature") {
					errorMsg = "Invalid token signature"
					// Capture invalid signatures - potential security issue
					authLog.Error("Invalid token signature", "error", err)
				} else if strings.Contains(err.Error(), "JWKS") || strings.Contains(err.Error(), "jwks") || strings.Contains(err.Error(), "certs") || strings.Contains(err.Error(), "keyfunc") {
					errorMsg = "Authentication service misconfigured"
					statusCode = http.StatusInternalServerError
					// Capture service misconfigurations - critical system error
					authLog.Error("Authentication service misconfigured", "error", err)
				}

				writeAuthError(w, r, errorMsg, statusCode)
				return
			}

			// Add user claims to context using the auth client
			r = authClient.SetUserInContext(r, claims)
			next.ServeHTTP(w, r)
		})
	}
}

var (
	jwksOnce    sync.Once
	jwksCache   keyfunc.Keyfunc
	jwksInitErr error
)

func getFallbackAuthURL() string {
	for _, key := range []string{"SUPABASE_FALLBACK_AUTH_URL", "SUPABASE_LEGACY_AUTH_URL"} {
		if value := strings.TrimSuffix(os.Getenv(key), "/"); value != "" {
			return value
		}
	}
	return ""
}

// getJWKS returns a cached JWKS client bound to Supabase's signing certs.
func getJWKS() (keyfunc.Keyfunc, error) {
	jwksOnce.Do(func() {
		authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
		if authURL == "" {
			jwksInitErr = fmt.Errorf("SUPABASE_AUTH_URL environment variable not set")
			return
		}

		jwksURL := fmt.Sprintf("%s/auth/v1/.well-known/jwks.json", authURL)

		// Support both custom domain and original Supabase domain JWKS
		jwksURLs := []string{jwksURL}

		// Optionally include a fallback auth domain during domain migrations.
		if fallbackAuthURL := getFallbackAuthURL(); fallbackAuthURL != "" && fallbackAuthURL != authURL {
			jwksURLs = append(jwksURLs, fmt.Sprintf("%s/auth/v1/.well-known/jwks.json", fallbackAuthURL))
		}

		override := keyfunc.Override{
			Client:          &http.Client{Timeout: 5 * time.Second},
			HTTPTimeout:     5 * time.Second,
			RefreshInterval: 10 * time.Minute,
			RefreshErrorHandlerFunc: func(url string) func(ctx context.Context, err error) {
				return func(ctx context.Context, err error) {
					authLog.Error("JWKS refresh failed", "error", err, "jwks_url", url)
				}
			},
		}

		childCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		jwksCache, jwksInitErr = keyfunc.NewDefaultOverrideCtx(childCtx, jwksURLs, override)
	})

	if jwksInitErr != nil {
		return nil, jwksInitErr
	}
	return jwksCache, nil
}

func validateSupabaseToken(ctx context.Context, tokenString string) (*UserClaims, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("request context cancelled: %w", ctx.Err())
	default:
	}

	jwks, err := getJWKS()
	if err != nil {
		return nil, fmt.Errorf("failed to initialise JWKS: %w", err)
	}

	authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
	if authURL == "" {
		return nil, fmt.Errorf("SUPABASE_AUTH_URL environment variable not set")
	}

	// Parse token without issuer validation first
	token, err := jwt.ParseWithClaims(
		tokenString,
		&UserClaims{},
		jwks.Keyfunc,
		jwt.WithValidMethods([]string{
			jwt.SigningMethodRS256.Name, // RSA keys
			jwt.SigningMethodES256.Name, // Elliptic Curve keys (P-256)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	claims, ok := token.Claims.(*UserClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Accept both custom domain and original Supabase domain as valid issuers
	// This handles the transition period when custom domain is configured but tokens
	// are still issued by the original Supabase domain
	validIssuers := []string{
		fmt.Sprintf("%s/auth/v1", authURL),
	}

	// Optionally accept legacy issuer during auth-domain migrations.
	if fallbackAuthURL := getFallbackAuthURL(); fallbackAuthURL != "" && fallbackAuthURL != authURL {
		validIssuers = append(validIssuers, fmt.Sprintf("%s/auth/v1", fallbackAuthURL))
	}

	// Manually validate issuer against allowed list
	issuer, err := claims.GetIssuer()
	if err != nil {
		return nil, fmt.Errorf("failed to read issuer: %w", err)
	}

	validIssuer := slices.Contains(validIssuers, issuer)
	if !validIssuer {
		return nil, fmt.Errorf("token has unexpected issuer: %s", issuer)
	}

	audiences, err := claims.GetAudience()
	if err != nil {
		return nil, fmt.Errorf("failed to read audience: %w", err)
	}
	if len(audiences) == 0 {
		return nil, fmt.Errorf("token missing audience")
	}

	validAudience := false
	for _, aud := range audiences {
		if aud == "authenticated" || aud == "service_role" {
			validAudience = true
			break
		}
	}
	if !validAudience {
		return nil, fmt.Errorf("token has unexpected audience: %v", audiences)
	}

	return claims, nil
}

// resetJWKSForTest clears the cached JWKS client. Intended for use in tests.
func resetJWKSForTest() {
	jwksOnce = sync.Once{}
	jwksCache = nil
	jwksInitErr = nil
}

// GetUserFromContext extracts user claims from the request context
func GetUserFromContext(ctx context.Context) (*UserClaims, bool) {
	user, ok := ctx.Value(UserKey).(*UserClaims)
	return user, ok
}

// OptionalAuthMiddleware validates JWT if present but doesn't require it
func OptionalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if Authorization header is present
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")

			// Try to validate the token
			claims, err := validateSupabaseToken(r.Context(), tokenString)
			if err == nil {
				// Token is valid, add to context
				ctx := context.WithValue(r.Context(), UserKey, claims)
				r = r.WithContext(ctx)
			} else {
				// Token is invalid but we continue without auth
				authLog.Warn("Invalid JWT token in optional auth", "error", err)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// writeAuthError writes a standardised authentication error response
func writeAuthError(w http.ResponseWriter, r *http.Request, message string, statusCode int) {
	// Get request ID if available (fallback to empty string if not)
	var requestID string
	if r != nil && r.Context() != nil {
		if rid := r.Context().Value("request_id"); rid != nil {
			if ridStr, ok := rid.(string); ok {
				requestID = ridStr
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	response := map[string]any{
		"status":     statusCode,
		"message":    message,
		"code":       "UNAUTHORISED",
		"request_id": requestID,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		authLog.Error("Failed to encode unauthorised response", "error", err)
	}
}

// SessionInfo holds session information and token validity
type SessionInfo struct {
	IsValid       bool   `json:"is_valid"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	RefreshNeeded bool   `json:"refresh_needed"`
	UserID        string `json:"user_id,omitempty"`
	Email         string `json:"email,omitempty"`
}

// ValidateSession validates a JWT token and returns session information
func ValidateSession(tokenString string) *SessionInfo {
	claims, err := validateSupabaseToken(context.Background(), tokenString)
	if err != nil {
		return &SessionInfo{
			IsValid:       false,
			RefreshNeeded: strings.Contains(err.Error(), "expired"),
		}
	}

	// Check if token expires soon (within 5 minutes)
	refreshNeeded := false
	if claims.ExpiresAt != nil {
		timeUntilExpiry := claims.ExpiresAt.Unix() - time.Now().Unix()
		refreshNeeded = timeUntilExpiry < 300 // 5 minutes
	}

	return &SessionInfo{
		IsValid:       true,
		ExpiresAt:     claims.ExpiresAt.Unix(),
		RefreshNeeded: refreshNeeded,
		UserID:        claims.UserID,
		Email:         claims.Email,
	}
}
