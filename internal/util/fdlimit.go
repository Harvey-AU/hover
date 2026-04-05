package util

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	cachedSoftLimit int
	cachedLimitErr  error
	limitOnce       sync.Once
)

// parseSoftLimit reads and parses the fd soft limit from /proc/self/limits.
func parseSoftLimit() (int, error) {
	data, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Max open files") {
			fields := strings.Fields(line)
			// Format: "Max open files  <soft>  <hard>  <unit>"
			if len(fields) < 5 {
				return 0, fmt.Errorf("unexpected format for max open files line: %q", line)
			}
			limit, err := strconv.Atoi(fields[3])
			if err != nil {
				return 0, fmt.Errorf("failed to parse fd soft limit %q: %w", fields[3], err)
			}
			return limit, nil
		}
	}
	return 0, fmt.Errorf("max open files line not found in /proc/self/limits")
}

// getSoftLimit returns the cached fd soft limit, parsing it once on first call.
func getSoftLimit() (int, error) {
	limitOnce.Do(func() {
		cachedSoftLimit, cachedLimitErr = parseSoftLimit()
	})
	return cachedSoftLimit, cachedLimitErr
}

// FDUsage returns the current open fd count and the soft limit.
// Linux-only (/proc/self); returns (0, 0, err) on unsupported platforms.
// The soft limit is cached after the first successful parse.
func FDUsage() (current, limit int, err error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, 0, err
	}
	current = len(entries)

	limit, err = getSoftLimit()
	if err != nil {
		return current, 0, err
	}
	return current, limit, nil
}

// FDPressureFrom computes the fd pressure ratio from a current count and limit.
// Returns 0 when limit <= 0 (fail-open).
func FDPressureFrom(current, limit int) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(current) / float64(limit)
}

// FDPressure returns the ratio of open fds to the soft limit (0.0–1.0).
// Returns 0 on error (fail-open).
func FDPressure() float64 {
	current, limit, err := FDUsage()
	if err != nil {
		return 0
	}
	return FDPressureFrom(current, limit)
}
