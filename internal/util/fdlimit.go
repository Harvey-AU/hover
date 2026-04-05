package util

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// FDUsage returns the current open fd count and the soft limit.
// Linux-only (/proc/self); returns (0, 0, err) on unsupported platforms.
func FDUsage() (current, limit int, err error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, 0, err
	}
	current = len(entries)

	data, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		return current, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Max open files") {
			fields := strings.Fields(line)
			// Format: "Max open files  <soft>  <hard>  <unit>"
			if len(fields) < 5 {
				return current, 0, fmt.Errorf("unexpected format for Max open files line: %q", line)
			}
			limit, err = strconv.Atoi(fields[3])
			if err != nil {
				return current, 0, fmt.Errorf("failed to parse fd soft limit %q: %w", fields[3], err)
			}
			return current, limit, nil
		}
	}
	return current, 0, fmt.Errorf("Max open files line not found in /proc/self/limits")
}

// FDPressure returns the ratio of open fds to the soft limit (0.0–1.0).
// Returns 0 on error (fail-open).
func FDPressure() float64 {
	current, limit, err := FDUsage()
	if err != nil || limit <= 0 {
		return 0
	}
	return float64(current) / float64(limit)
}
