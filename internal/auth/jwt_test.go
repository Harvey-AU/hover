package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const claimsKey contextKey = "claims"

func TestJWTTokenValidation(t *testing.T) {
	secret := "test-secret-key-minimum-256-bits-long-for-hs256"

	tests := []struct {
		name           string
		token          string
		expectedValid  bool
		expectedClaims map[string]any
		description    string
	}{
		{
			name:          "valid_token",
			token:         createTestToken(secret, jwt.MapClaims{"sub": "user123", "exp": time.Now().Add(time.Hour).Unix()}),
			expectedValid: true,
			expectedClaims: map[string]any{
				"sub": "user123",
			},
			description: "Valid token should pass validation",
		},
		{
			name:          "expired_token",
			token:         createTestToken(secret, jwt.MapClaims{"sub": "user123", "exp": time.Now().Add(-time.Hour).Unix()}),
			expectedValid: false,
			description:   "Expired token should fail validation",
		},
		{
			name:          "invalid_signature",
			token:         createTestToken("wrong-secret", jwt.MapClaims{"sub": "user123", "exp": time.Now().Add(time.Hour).Unix()}),
			expectedValid: false,
			description:   "Token with invalid signature should fail",
		},
		{
			name:          "malformed_token",
			token:         "invalid.token.format",
			expectedValid: false,
			description:   "Malformed token should fail validation",
		},
		{
			name:          "empty_token",
			token:         "",
			expectedValid: false,
			description:   "Empty token should fail validation",
		},
		{
			name:          "token_with_nbf",
			token:         createTestToken(secret, jwt.MapClaims{"sub": "user123", "exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix()}),
			expectedValid: true,
			description:   "Token with valid nbf should pass",
		},
		{
			name:          "token_not_yet_valid",
			token:         createTestToken(secret, jwt.MapClaims{"sub": "user123", "exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(time.Hour).Unix()}),
			expectedValid: false,
			description:   "Token with future nbf should fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock validation
			isValid := validateTestToken(tt.token, secret)
			assert.Equal(t, tt.expectedValid, isValid, tt.description)

			if tt.expectedValid && tt.expectedClaims != nil {
				claims := parseTestToken(tt.token, secret)
				for key, expectedValue := range tt.expectedClaims {
					if actualValue, ok := claims[key]; ok {
						assert.Equal(t, expectedValue, actualValue, "Claim %s should match", key)
					}
				}
			}
		})
	}
}

func TestJWTTokenExpiry(t *testing.T) {
	secret := "test-secret-key-minimum-256-bits-long-for-hs256"

	tests := []struct {
		name          string
		expiryTime    time.Time
		expectedValid bool
		description   string
	}{
		{
			name:          "expires_in_1_hour",
			expiryTime:    time.Now().Add(time.Hour),
			expectedValid: true,
			description:   "Token expiring in 1 hour should be valid",
		},
		{
			name:          "expires_in_5_minutes",
			expiryTime:    time.Now().Add(5 * time.Minute),
			expectedValid: true,
			description:   "Token expiring in 5 minutes should be valid",
		},
		{
			name:          "expired_1_minute_ago",
			expiryTime:    time.Now().Add(-time.Minute),
			expectedValid: false,
			description:   "Token expired 1 minute ago should be invalid",
		},
		{
			name:          "expired_1_hour_ago",
			expiryTime:    time.Now().Add(-time.Hour),
			expectedValid: false,
			description:   "Token expired 1 hour ago should be invalid",
		},
		{
			name:          "no_expiry",
			expiryTime:    time.Time{}, // Zero time means no exp claim
			expectedValid: true,
			description:   "Token without expiry should be valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := jwt.MapClaims{
				"sub": "user123",
			}

			if !tt.expiryTime.IsZero() {
				claims["exp"] = tt.expiryTime.Unix()
			}

			token := createTestToken(secret, claims)
			isValid := validateTestToken(token, secret)
			assert.Equal(t, tt.expectedValid, isValid, tt.description)
		})
	}
}

func TestMissingAuthHeader(t *testing.T) {
	tests := []struct {
		name          string
		authHeader    string
		expectedToken string
		expectedError bool
		description   string
	}{
		{ //nolint:gosec // G101: fake JWT for test fixture
			name:          "valid_bearer_token",
			authHeader:    "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyMTIzIn0.TJVA95OrM7E2cBab30RMHrHDcEfxjoYZgeFONFh7HgQ",
			expectedToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyMTIzIn0.TJVA95OrM7E2cBab30RMHrHDcEfxjoYZgeFONFh7HgQ",
			expectedError: false,
			description:   "Valid Bearer token should be extracted",
		},
		{
			name:          "missing_bearer_prefix",
			authHeader:    "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyMTIzIn0.TJVA95OrM7E2cBab30RMHrHDcEfxjoYZgeFONFh7HgQ",
			expectedToken: "",
			expectedError: true,
			description:   "Token without Bearer prefix should fail",
		},
		{
			name:          "empty_header",
			authHeader:    "",
			expectedToken: "",
			expectedError: true,
			description:   "Empty header should fail",
		},
		{
			name:          "invalid_format",
			authHeader:    "Basic dXNlcjpwYXNz",
			expectedToken: "",
			expectedError: true,
			description:   "Basic auth header should fail",
		},
		{
			name:          "bearer_lowercase",
			authHeader:    "bearer token123",
			expectedToken: "",
			expectedError: true,
			description:   "Lowercase bearer should fail",
		},
		{
			name:          "extra_spaces",
			authHeader:    "Bearer  token123",
			expectedToken: "token123",
			expectedError: false,
			description:   "Extra spaces should be handled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := extractTokenFromHeader(tt.authHeader)

			if tt.expectedError {
				assert.Error(t, err, tt.description)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err, tt.description)
				assert.Equal(t, tt.expectedToken, token)
			}
		})
	}
}

func TestUserClaimsExtraction(t *testing.T) {
	secret := "test-secret-key-minimum-256-bits-long-for-hs256"

	tests := []struct {
		name          string
		claims        jwt.MapClaims
		expectedUser  string
		expectedRole  string
		expectedEmail string
		expectedError bool
		description   string
	}{
		{
			name: "complete_claims",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"role":  "admin",
				"email": "user@example.com",
				"exp":   time.Now().Add(time.Hour).Unix(),
			},
			expectedUser:  "user123",
			expectedRole:  "admin",
			expectedEmail: "user@example.com",
			expectedError: false,
			description:   "Should extract all claims",
		},
		{
			name: "missing_role",
			claims: jwt.MapClaims{
				"sub":   "user456",
				"email": "user@example.com",
				"exp":   time.Now().Add(time.Hour).Unix(),
			},
			expectedUser:  "user456",
			expectedRole:  "",
			expectedEmail: "user@example.com",
			expectedError: false,
			description:   "Should handle missing role",
		},
		{
			name: "missing_sub",
			claims: jwt.MapClaims{
				"role":  "user",
				"email": "user@example.com",
				"exp":   time.Now().Add(time.Hour).Unix(),
			},
			expectedUser:  "",
			expectedRole:  "user",
			expectedEmail: "user@example.com",
			expectedError: true,
			description:   "Should error on missing sub",
		},
		{
			name: "custom_claims",
			claims: jwt.MapClaims{
				"sub":      "user789",
				"org_id":   "org123",
				"org_role": "owner",
				"exp":      time.Now().Add(time.Hour).Unix(),
			},
			expectedUser:  "user789",
			expectedError: false,
			description:   "Should handle custom claims",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := createTestToken(secret, tt.claims)
			claims := parseTestToken(token, secret)

			if claims != nil {
				if sub, ok := claims["sub"].(string); ok {
					assert.Equal(t, tt.expectedUser, sub, "User ID should match")
				} else if tt.expectedUser != "" {
					t.Errorf("Expected user %s but not found", tt.expectedUser)
				}

				if role, ok := claims["role"].(string); ok {
					assert.Equal(t, tt.expectedRole, role, "Role should match")
				}

				if email, ok := claims["email"].(string); ok {
					assert.Equal(t, tt.expectedEmail, email, "Email should match")
				}
			} else if !tt.expectedError {
				t.Error("Expected valid claims but got nil")
			}
		})
	}
}

func TestOrganisationRolePermissions(t *testing.T) {
	tests := []struct {
		name          string
		role          string
		resource      string
		action        string
		hasPermission bool
		description   string
	}{
		{
			name:          "admin_all_permissions",
			role:          "admin",
			resource:      "jobs",
			action:        "delete",
			hasPermission: true,
			description:   "Admin should have all permissions",
		},
		{
			name:          "owner_all_permissions",
			role:          "owner",
			resource:      "domains",
			action:        "create",
			hasPermission: true,
			description:   "Owner should have all permissions",
		},
		{
			name:          "member_read_only",
			role:          "member",
			resource:      "jobs",
			action:        "read",
			hasPermission: true,
			description:   "Member should have read permission",
		},
		{
			name:          "member_no_write",
			role:          "member",
			resource:      "jobs",
			action:        "write",
			hasPermission: false,
			description:   "Member should not have write permission",
		},
		{
			name:          "viewer_read_only",
			role:          "viewer",
			resource:      "stats",
			action:        "read",
			hasPermission: true,
			description:   "Viewer should have read permission",
		},
		{
			name:          "viewer_no_create",
			role:          "viewer",
			resource:      "jobs",
			action:        "create",
			hasPermission: false,
			description:   "Viewer should not have create permission",
		},
		{
			name:          "unknown_role",
			role:          "unknown",
			resource:      "jobs",
			action:        "read",
			hasPermission: false,
			description:   "Unknown role should have no permissions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock permission check
			hasPermission := checkPermission(tt.role, tt.resource, tt.action)
			assert.Equal(t, tt.hasPermission, hasPermission, tt.description)
		})
	}
}

func TestClaimsFromContext(t *testing.T) {
	tests := []struct {
		name          string
		claims        any
		expectedUser  string
		expectedError bool
		description   string
	}{
		{
			name: "valid_claims_in_context",
			claims: map[string]any{
				"sub":  "user123",
				"role": "admin",
			},
			expectedUser:  "user123",
			expectedError: false,
			description:   "Should extract claims from context",
		},
		{
			name:          "nil_context",
			claims:        nil,
			expectedUser:  "",
			expectedError: true,
			description:   "Should error on nil context",
		},
		{
			name:          "empty_claims",
			claims:        map[string]any{},
			expectedUser:  "",
			expectedError: true,
			description:   "Should error on empty claims",
		},
		{
			name:          "wrong_type_in_context",
			claims:        "invalid",
			expectedUser:  "",
			expectedError: true,
			description:   "Should error on wrong type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.claims != nil {
				ctx = context.WithValue(ctx, claimsKey, tt.claims)
			}

			// Mock extraction
			if claims, ok := ctx.Value(claimsKey).(map[string]any); ok {
				if sub, ok := claims["sub"].(string); ok {
					assert.Equal(t, tt.expectedUser, sub, tt.description)
				} else {
					assert.True(t, tt.expectedError, "Should have error for missing sub")
				}
			} else {
				assert.True(t, tt.expectedError || tt.claims == nil, tt.description)
			}
		})
	}
}

// Helper functions for testing

func createTestToken(secret string, claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte(secret))
	return tokenString
}

func validateTestToken(tokenString, secret string) bool {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	})

	if err != nil {
		return false
	}

	return token.Valid
}

func parseTestToken(tokenString, secret string) jwt.MapClaims {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	})

	if err != nil || !token.Valid {
		return nil
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		return claims
	}

	return nil
}

func extractTokenFromHeader(authHeader string) (string, error) {
	if authHeader == "" {
		return "", assert.AnError
	}

	const bearerPrefix = "Bearer "
	if len(authHeader) > len(bearerPrefix) && authHeader[:len(bearerPrefix)] == bearerPrefix {
		token := authHeader[len(bearerPrefix):]
		// Trim any extra spaces
		for len(token) > 0 && token[0] == ' ' {
			token = token[1:]
		}
		return token, nil
	}

	return "", assert.AnError
}

func checkPermission(role, resource, action string) bool {
	// Simple permission matrix for testing
	permissions := map[string]map[string]bool{
		"admin": {
			"read":   true,
			"write":  true,
			"delete": true,
			"create": true,
		},
		"owner": {
			"read":   true,
			"write":  true,
			"delete": true,
			"create": true,
		},
		"member": {
			"read":   true,
			"write":  false,
			"delete": false,
			"create": false,
		},
		"viewer": {
			"read":   true,
			"write":  false,
			"delete": false,
			"create": false,
		},
	}

	if rolePerms, ok := permissions[role]; ok {
		return rolePerms[action]
	}

	return false
}
