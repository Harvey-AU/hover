package db

import (
	"context"
	"fmt"
	"math"
	"time"
)

type RetryConfig struct {
	MaxAttempts     int
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	Jitter          bool // randomness against thundering herd on shared DBs
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:     10,
		InitialInterval: 1 * time.Second,
		MaxInterval:     30 * time.Second,
		Multiplier:      2.0,
		Jitter:          true,
	}
}

func InitFromEnvWithRetry(ctx context.Context) (*DB, error) {
	config := DefaultRetryConfig()
	return InitFromEnvWithRetryConfig(ctx, config)
}

func InitFromEnvWithRetryConfig(ctx context.Context, retryConfig RetryConfig) (*DB, error) {
	var lastErr error
	backoff := retryConfig.InitialInterval
	startTime := time.Now()

	for attempt := 1; attempt <= retryConfig.MaxAttempts; attempt++ {
		db, err := InitFromEnv()
		if err == nil {
			if attempt > 1 {
				dbLog.Info("Database connection established after retries",
					"attempt", attempt,
					"elapsed", time.Since(startTime))
			}
			return db, nil
		}

		lastErr = err

		// Fail fast on config/auth errors — retrying won't help.
		if !isRetryableError(err) {
			dbLog.Error("Database connection failed with non-retryable error",
				"error", err,
				"attempt", attempt)
			return nil, fmt.Errorf("database connection failed: %w", err)
		}

		if attempt >= retryConfig.MaxAttempts {
			break
		}

		dbLog.Warn("Database connection failed, retrying...",
			"error", err,
			"attempt", attempt,
			"max_attempts", retryConfig.MaxAttempts,
			"retry_in", backoff)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connection retry cancelled: %w", ctx.Err())
		case <-time.After(backoff):
		}

		backoff = min(time.Duration(float64(backoff)*retryConfig.Multiplier), retryConfig.MaxInterval)

		if retryConfig.Jitter {
			jitter := time.Duration(float64(backoff) * 0.1 * (2.0*float64(time.Now().UnixNano()%100)/100.0 - 1.0))
			backoff += jitter
		}
	}

	dbLog.Error("Database connection failed after all retry attempts",
		"error", lastErr,
		"max_attempts", retryConfig.MaxAttempts)

	return nil, fmt.Errorf("failed to connect to database after %d attempts: %w", retryConfig.MaxAttempts, lastErr)
}

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

func InitFromURLWithSuffixRetry(ctx context.Context, databaseURL string, appEnv string, appNameSuffix string) (*DB, error) {
	config := DefaultRetryConfig()
	var lastErr error
	backoff := config.InitialInterval
	startTime := time.Now()

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		db, err := InitFromURLWithSuffix(databaseURL, appEnv, appNameSuffix)
		if err == nil {
			if attempt > 1 {
				dbLog.Info("Database connection established after retries",
					"suffix", appNameSuffix,
					"attempt", attempt,
					"elapsed", time.Since(startTime))
			}
			return db, nil
		}

		lastErr = err

		if !isRetryableError(err) {
			dbLog.Error("Database connection failed with non-retryable error",
				"error", err,
				"suffix", appNameSuffix,
				"attempt", attempt)
			return nil, fmt.Errorf("database connection failed: %w", err)
		}

		if attempt >= config.MaxAttempts {
			break
		}

		dbLog.Warn("Database connection failed, retrying...",
			"error", err,
			"suffix", appNameSuffix,
			"attempt", attempt,
			"max_attempts", config.MaxAttempts,
			"retry_in", backoff)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connection retry cancelled: %w", ctx.Err())
		case <-time.After(backoff):
		}

		backoff = min(time.Duration(float64(backoff)*config.Multiplier), config.MaxInterval)

		if config.Jitter {
			jitter := time.Duration(float64(backoff) * 0.1 * (2.0*float64(time.Now().UnixNano()%100)/100.0 - 1.0))
			backoff += jitter
		}
	}

	dbLog.Error("Database connection failed after all retry attempts",
		"error", lastErr,
		"suffix", appNameSuffix,
		"max_attempts", config.MaxAttempts)

	return nil, fmt.Errorf("failed to connect to database after %d attempts: %w", config.MaxAttempts, lastErr)
}
