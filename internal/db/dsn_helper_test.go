package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAugmentDSNWithTimeout(t *testing.T) {
	tests := []struct {
		name      string
		dsn       string
		timeoutMs int
		expected  string
	}{
		{
			name:      "empty DSN",
			dsn:       "",
			timeoutMs: 60000,
			expected:  "",
		},
		{ //nolint:gosec // G101: fake DSN in test fixture
			name:      "URL format without params",
			dsn:       "postgresql://user:pass@localhost/db",
			timeoutMs: 60000,
			expected:  "postgresql://user:pass@localhost/db?statement_timeout=60000",
		},
		{ //nolint:gosec // G101: fake DSN in test fixture
			name:      "URL format with existing params",
			dsn:       "postgresql://user:pass@localhost/db?sslmode=require",
			timeoutMs: 60000,
			expected:  "postgresql://user:pass@localhost/db?sslmode=require&statement_timeout=60000",
		},
		{ //nolint:gosec // G101: fake DSN in test fixture
			name:      "postgres URL format",
			dsn:       "postgres://user:pass@localhost/db",
			timeoutMs: 30000,
			expected:  "postgres://user:pass@localhost/db?statement_timeout=30000",
		},
		{
			name:      "key=value format",
			dsn:       "host=localhost user=user password=pass dbname=db",
			timeoutMs: 45000,
			expected:  "host=localhost user=user password=pass dbname=db statement_timeout=45000",
		},
		{ //nolint:gosec // G101: fake DSN in test fixture
			name:      "already has statement_timeout",
			dsn:       "postgresql://user:pass@localhost/db?statement_timeout=30000",
			timeoutMs: 60000,
			expected:  "postgresql://user:pass@localhost/db?statement_timeout=30000",
		},
		{
			name:      "key=value with existing timeout",
			dsn:       "host=localhost statement_timeout=30000",
			timeoutMs: 60000,
			expected:  "host=localhost statement_timeout=30000",
		},
		{ //nolint:gosec // G101: fake DSN in test fixture
			name:      "zero timeout uses default",
			dsn:       "postgresql://user:pass@localhost/db",
			timeoutMs: 0,
			expected:  "postgresql://user:pass@localhost/db?statement_timeout=60000",
		},
		{ //nolint:gosec // G101: fake DSN in test fixture
			name:      "negative timeout uses default",
			dsn:       "postgresql://user:pass@localhost/db",
			timeoutMs: -1000,
			expected:  "postgresql://user:pass@localhost/db?statement_timeout=60000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AugmentDSNWithTimeout(tt.dsn, tt.timeoutMs)
			assert.Equal(t, tt.expected, result)
		})
	}
}
