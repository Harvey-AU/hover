package util

import (
	"os"
	"strings"
)

// GetenvWithLegacy reads the primary env var; if empty, falls back to the
// legacy name. This supports the GNH_ ← BBB_ prefix migration so deployments
// using the old names continue to work until secrets are rotated.
func GetenvWithLegacy(primary, legacy string) string {
	if v := strings.TrimSpace(os.Getenv(primary)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(legacy))
}

// LookupEnvWithLegacy is like GetenvWithLegacy but returns (value, ok) matching
// the os.LookupEnv signature.
func LookupEnvWithLegacy(primary, legacy string) (string, bool) {
	if v, ok := os.LookupEnv(primary); ok {
		return strings.TrimSpace(v), true
	}
	if v, ok := os.LookupEnv(legacy); ok {
		return strings.TrimSpace(v), true
	}
	return "", false
}
