package jobs

import "testing"

func TestMaxWorkersFromEnv_UsesStagingFallbackWhenUnset(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("GNH_MAX_WORKERS", "")

	if got := maxWorkersFromEnv(); got != maxWorkersStaging {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersStaging)
	}
}

func TestMaxWorkersFromEnv_UsesEnvOverrideInStaging(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("GNH_MAX_WORKERS", "130")

	if got := maxWorkersFromEnv(); got != 130 {
		t.Fatalf("maxWorkersFromEnv() = %d, want 130", got)
	}
}

func TestMaxWorkersFromEnv_UsesProductionFallbackWhenUnset(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("GNH_MAX_WORKERS", "")

	if got := maxWorkersFromEnv(); got != maxWorkersProduction {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersProduction)
	}
}

func TestMaxWorkersFromEnv_InvalidOverrideFallsBackToEnvironmentDefault(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("GNH_MAX_WORKERS", "invalid")

	if got := maxWorkersFromEnv(); got != maxWorkersStaging {
		t.Fatalf("maxWorkersFromEnv() = %d, want %d", got, maxWorkersStaging)
	}
}
