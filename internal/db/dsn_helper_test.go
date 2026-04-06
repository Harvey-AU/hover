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
		{
			name:      "URL format without params",
			dsn:       "postgresql://localhost/testdb",
			timeoutMs: 60000,
			expected:  "postgresql://localhost/testdb?statement_timeout=60000",
		},
		{
			name:      "URL format with existing params",
			dsn:       "postgresql://localhost/testdb?sslmode=require",
			timeoutMs: 60000,
			expected:  "postgresql://localhost/testdb?sslmode=require&statement_timeout=60000",
		},
		{
			name:      "postgres URL format",
			dsn:       "postgres://localhost/testdb",
			timeoutMs: 30000,
			expected:  "postgres://localhost/testdb?statement_timeout=30000",
		},
		{
			name:      "key=value format",
			dsn:       "host=localhost user=testuser dbname=testdb",
			timeoutMs: 45000,
			expected:  "host=localhost user=testuser dbname=testdb statement_timeout=45000",
		},
		{
			name:      "already has statement_timeout",
			dsn:       "postgresql://localhost/testdb?statement_timeout=30000",
			timeoutMs: 60000,
			expected:  "postgresql://localhost/testdb?statement_timeout=30000",
		},
		{
			name:      "key=value with existing timeout",
			dsn:       "host=localhost statement_timeout=30000",
			timeoutMs: 60000,
			expected:  "host=localhost statement_timeout=30000",
		},
		{
			name:      "zero timeout uses default",
			dsn:       "postgresql://localhost/testdb",
			timeoutMs: 0,
			expected:  "postgresql://localhost/testdb?statement_timeout=60000",
		},
		{
			name:      "negative timeout uses default",
			dsn:       "postgresql://localhost/testdb",
			timeoutMs: -1000,
			expected:  "postgresql://localhost/testdb?statement_timeout=60000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AugmentDSNWithTimeout(tt.dsn, tt.timeoutMs)
			assert.Equal(t, tt.expected, result)
		})
	}
}
