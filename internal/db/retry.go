package db

import (
	"context"
	"fmt"
	"math"
	"time"

)

// RetryConfig holds configuration for connection retry behaviour
type RetryConfig struct {
	MaxAttempts     int           // Maximum number of connection attempts
	InitialInterval time.Duration // Initial retry interval
	MaxInterval     time.Duration // Maximum retry interval (cap for exponential backoff)
	Multiplier      float64       // Backoff multiplier (typically 2.0)
	Jitter          bool          // Add randomness to prevent thundering herd
}

// DefaultRetryConfig returns sensible defaults for database connection retries
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:     10,               // Try up to 10 times
		InitialInterval: 1 * time.Second,  // Start with 1 second
		MaxInterval:     30 * time.Second, // Cap at 30 seconds
		Multiplier:      2.0,              // Double each time
		Jitter:          true,             // Add randomness
	}
}

// Note: isRetryableError is already defined in batch.go and handles connection errors

// InitFromEnvWithRetry creates a PostgreSQL connection using environment variables
// with automatic retry on connection failures
func InitFromEnvWithRetry(ctx context.Context) (*DB, error) {
	config := DefaultRetryConfig()
	return InitFromEnvWithRetryConfig(ctx, config)
}

// InitFromEnvWithRetryConfig creates a PostgreSQL connection with custom retry configuration
func InitFromEnvWithRetryConfig(ctx context.Context, retryConfig RetryConfig) (*DB, error) {
	var lastErr error
	backoff := retryConfig.InitialInterval
	startTime := time.Now()

	for attempt := 1; attempt <= retryConfig.MaxAttempts; attempt++ {
		// Try to connect
		db, err := InitFromEnv()
		if err == nil {
			// Success!
			if attempt > 1 {
				dbLog.Info("Database connection established after retries",
					"attempts", attempt,
					"elapsed", time.Since(startTime))
			}
			return db, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			// Configuration or authentication errors - fail fast
			dbLog.Error("Database connection failed with non-retryable error",
				"error", err,
				"attempt", attempt)
			return nil, fmt.Errorf("database connection failed: %w", err)
		}

		// Don't retry if we've exhausted attempts
		if attempt >= retryConfig.MaxAttempts {
			break
		}

		// Log retry attempt
		dbLog.Warn("Database connection failed, retrying...",
			"error", err,
			"attempt", attempt,
			"max_attempts", retryConfig.MaxAttempts,
			"retry_in", backoff)

		// Wait before retry (respecting context cancellation)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connection retry cancelled: %w", ctx.Err())
		case <-time.After(backoff):
			// Continue to next attempt
		}

		// Calculate next backoff with exponential increase
		backoff = min(time.Duration(float64(backoff)*retryConfig.Multiplier), retryConfig.MaxInterval)

		// Add jitter to prevent thundering herd
		if retryConfig.Jitter {
			jitter := time.Duration(float64(backoff) * 0.1 * (2.0*float64(time.Now().UnixNano()%100)/100.0 - 1.0))
			backoff += jitter
		}
	}

	// All retries exhausted
	dbLog.Error("Database connection failed after all retry attempts",
		"error", lastErr,
		"max_attempts", retryConfig.MaxAttempts)

	return nil, fmt.Errorf("failed to connect to database after %d attempts: %w", retryConfig.MaxAttempts, lastErr)
}

// WaitForDatabase blocks until the database connection is established or context is cancelled
// This is useful during application startup to gracefully wait for database availability
func WaitForDatabase(ctx context.Context, maxWait time.Duration) (*DB, error) {
	waitCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	config := RetryConfig{
		MaxAttempts:     int(math.Ceil(float64(maxWait) / float64(5*time.Second))),
		InitialInterval: 2 * time.Second,
		MaxInterval:     30 * time.Second,
		Multiplier:      2.0,
		Jitter:          true,
	}

	dbLog.Info("Waiting for database to become available...",
		"max_wait", maxWait,
		"max_attempts", config.MaxAttempts)

	return InitFromEnvWithRetryConfig(waitCtx, config)
}

// InitFromURLWithSuffixRetry creates a PostgreSQL connection using the provided URL
// with automatic retry on connection failures
func InitFromURLWithSuffixRetry(ctx context.Context, databaseURL string, appEnv string, appNameSuffix string) (*DB, error) {
	config := DefaultRetryConfig()
	var lastErr error
	backoff := config.InitialInterval
	startTime := time.Now()

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Try to connect
		db, err := InitFromURLWithSuffix(databaseURL, appEnv, appNameSuffix)
		if err == nil {
			// Success!
			if attempt > 1 {
				dbLog.Info("Database connection established after retries",
					"suffix", appNameSuffix,
					"attempts", attempt,
					"elapsed", time.Since(startTime))
			}
			return db, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			// Configuration or authentication errors - fail fast
			dbLog.Error("Database connection failed with non-retryable error",
				"error", err,
				"suffix", appNameSuffix,
				"attempt", attempt)
			return nil, fmt.Errorf("database connection failed: %w", err)
		}

		// Don't retry if we've exhausted attempts
		if attempt >= config.MaxAttempts {
			break
		}

		// Log retry attempt
		dbLog.Warn("Database connection failed, retrying...",
			"error", err,
			"suffix", appNameSuffix,
			"attempt", attempt,
			"max_attempts", config.MaxAttempts,
			"retry_in", backoff)

		// Wait before retry (respecting context cancellation)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connection retry cancelled: %w", ctx.Err())
		case <-time.After(backoff):
			// Continue to next attempt
		}

		// Calculate next backoff with exponential increase
		backoff = min(time.Duration(float64(backoff)*config.Multiplier), config.MaxInterval)

		// Add jitter to prevent thundering herd
		if config.Jitter {
			jitter := time.Duration(float64(backoff) * 0.1 * (2.0*float64(time.Now().UnixNano()%100)/100.0 - 1.0))
			backoff += jitter
		}
	}

	// All retries exhausted
	dbLog.Error("Database connection failed after all retry attempts",
		"error", lastErr,
		"suffix", appNameSuffix,
		"max_attempts", config.MaxAttempts)

	return nil, fmt.Errorf("failed to connect to database after %d attempts: %w", config.MaxAttempts, lastErr)
}
