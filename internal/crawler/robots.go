package crawler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// RobotsRules contains parsed robots.txt rules for a domain
type RobotsRules struct {
	// CrawlDelay in seconds (0 means no delay specified)
	CrawlDelay int
	// Sitemaps found in robots.txt
	Sitemaps []string
	// DisallowPatterns are URL patterns that should not be crawled
	DisallowPatterns []string
	// AllowPatterns override DisallowPatterns (more specific)
	AllowPatterns []string
}

// ParseRobotsTxt fetches and parses robots.txt for a domain
//
// The parser follows these rules in order of precedence:
// 1. If there are specific rules for "HoverBot", use those
// 2. Otherwise, fall back to wildcard (*) rules
//
// We intentionally don't match SEO crawler rules (AhrefsBot, MJ12bot, etc.) as those
// often have punitive 10s delays meant for aggressive crawlers. Most sites have no
// crawl-delay for the default * user-agent.
func ParseRobotsTxt(ctx context.Context, domain string, userAgent string, transport ...http.RoundTripper) (*RobotsRules, error) {
	// Support both domain-only and full URL formats
	var robotsURL string
	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		// Full URL provided - use as base
		robotsURL = strings.TrimSuffix(domain, "/") + "/robots.txt"
	} else {
		// Domain only - default to https
		robotsURL = fmt.Sprintf("https://%s/robots.txt", domain)
	}

	log.Debug().
		Str("domain", domain).
		Str("robots_url", robotsURL).
		Msg("Fetching robots.txt")

	// Create a client with shorter timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	if len(transport) > 0 && transport[0] != nil {
		client.Transport = transport[0]
	}

	req, err := http.NewRequestWithContext(ctx, "GET", robotsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Use the provided user agent
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch robots.txt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// No robots.txt means no restrictions
		if resp.StatusCode == http.StatusNotFound {
			log.Debug().Msg("No robots.txt found, no restrictions apply")
			return &RobotsRules{}, nil
		}
		return nil, fmt.Errorf("robots.txt returned status %d", resp.StatusCode)
	}

	// Limit robots.txt size to 1MB to prevent memory exhaustion
	limitedReader := io.LimitReader(resp.Body, 1*1024*1024) // 1MB limit
	return parseRobotsTxtContent(limitedReader, userAgent)
}

// parseRobotsTxtContent parses the robots.txt content
func parseRobotsTxtContent(r io.Reader, userAgent string) (*RobotsRules, error) {
	rules := &RobotsRules{
		Sitemaps:         []string{},
		DisallowPatterns: []string{},
		AllowPatterns:    []string{},
	}

	// Read entire content to check if we hit the limit
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read robots.txt: %w", err)
	}

	// Check if we likely hit the 1MB limit (exactly 1MB read)
	if len(content) == 1*1024*1024 {
		log.Warn().
			Int("size_bytes", len(content)).
			Msg("Robots.txt file truncated at 1MB limit")
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	// Track if we're in a section that applies to us
	var inOurSection bool
	var inWildcardSection bool
	var foundSpecificSection bool // Track if we've found a specific section for our bot

	// Extract bot name from user agent (e.g., "HoverBot/1.0" -> "hoverbot")
	botName := strings.ToLower(strings.Split(userAgent, "/")[0])

	// Temporary storage for wildcard rules
	wildcardRules := &RobotsRules{
		Sitemaps:         []string{},
		DisallowPatterns: []string{},
		AllowPatterns:    []string{},
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Convert to lowercase for case-insensitive matching
		lowerLine := strings.ToLower(line)

		// Parse User-agent directive
		if strings.HasPrefix(lowerLine, "user-agent:") {
			agent := strings.TrimSpace(line[11:])
			agentLower := strings.ToLower(agent)

			// Check if this section applies to us
			inOurSection = false
			inWildcardSection = false

			if agent == "*" {
				inWildcardSection = true
			} else if agentLower == botName || strings.Contains(agentLower, botName) {
				inOurSection = true
				foundSpecificSection = true
				// Clear any wildcard rules we've collected
				rules = &RobotsRules{
					Sitemaps:         []string{},
					DisallowPatterns: []string{},
					AllowPatterns:    []string{},
				}
				log.Debug().
					Str("user_agent_section", agent).
					Msg("Found rules section for our bot")
			}
			continue
		}

		// Parse Sitemap directive (applies globally)
		if strings.HasPrefix(lowerLine, "sitemap:") {
			sitemapURL := strings.TrimSpace(line[8:])
			if sitemapURL != "" {
				// Always add sitemaps to the main rules
				rules.Sitemaps = append(rules.Sitemaps, sitemapURL)
				if inWildcardSection && !foundSpecificSection {
					wildcardRules.Sitemaps = append(wildcardRules.Sitemaps, sitemapURL)
				}
			}
			continue
		}

		// Only process other directives if we're in a relevant section
		if !inOurSection && !inWildcardSection {
			continue
		}

		// Determine which rule set to update
		currentRules := rules
		if inWildcardSection && !foundSpecificSection {
			currentRules = wildcardRules
		}

		// Parse Crawl-delay directive
		if strings.HasPrefix(lowerLine, "crawl-delay:") {
			delayStr := strings.TrimSpace(line[12:])
			if delay, err := strconv.Atoi(delayStr); err == nil && delay > 0 {
				currentRules.CrawlDelay = delay
				log.Debug().
					Int("crawl_delay", delay).
					Bool("from_specific_section", inOurSection).
					Msg("Found Crawl-delay directive")
			}
			continue
		}

		// Parse Disallow directive
		if strings.HasPrefix(lowerLine, "disallow:") {
			path := strings.TrimSpace(line[9:])
			if path != "" && path != "/" { // Ignore "Disallow: /" which blocks everything
				currentRules.DisallowPatterns = append(currentRules.DisallowPatterns, path)
			}
			continue
		}

		// Parse Allow directive (overrides Disallow)
		if strings.HasPrefix(lowerLine, "allow:") {
			path := strings.TrimSpace(line[6:])
			if path != "" {
				currentRules.AllowPatterns = append(currentRules.AllowPatterns, path)
			}
			continue
		}
	}

	// If we didn't find a specific section, use wildcard rules
	if !foundSpecificSection {
		rules = wildcardRules
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading robots.txt: %w", err)
	}

	log.Debug().
		Int("crawl_delay", rules.CrawlDelay).
		Int("sitemaps", len(rules.Sitemaps)).
		Int("disallow_patterns", len(rules.DisallowPatterns)).
		Int("allow_patterns", len(rules.AllowPatterns)).
		Msg("Parsed robots.txt rules")

	return rules, nil
}

// IsPathAllowed checks if a path is allowed by robots.txt rules
func IsPathAllowed(rules *RobotsRules, path string) bool {
	// No rules means everything is allowed
	if rules == nil || len(rules.DisallowPatterns) == 0 {
		return true
	}

	// Check Allow patterns first (they override Disallow)
	for _, pattern := range rules.AllowPatterns {
		if matchesRobotsPattern(path, pattern) {
			return true
		}
	}

	// Check Disallow patterns
	for _, pattern := range rules.DisallowPatterns {
		if matchesRobotsPattern(path, pattern) {
			return false
		}
	}

	// If no patterns match, it's allowed
	return true
}

// matchesRobotsPattern checks if a path matches a robots.txt pattern
// Supports * wildcard and $ end-of-URL marker
func matchesRobotsPattern(path, pattern string) bool {
	// Handle $ end marker
	if before, ok := strings.CutSuffix(pattern, "$"); ok {
		pattern = before
		// For exact end matching, the path must exactly match the pattern
		return path == pattern
	}

	// Convert * wildcards to simple matching
	if strings.Contains(pattern, "*") {
		// For now, just support simple cases
		parts := strings.Split(pattern, "*")
		if len(parts) == 2 && parts[1] == "" {
			// Pattern like "/path/*" - just check prefix
			return strings.HasPrefix(path, parts[0])
		}
		// More complex wildcard patterns - simplified implementation
		// Just check if path contains all parts in order
		currentPos := 0
		for _, part := range parts {
			if part == "" {
				continue
			}
			idx := strings.Index(path[currentPos:], part)
			if idx == -1 {
				return false
			}
			currentPos += idx + len(part)
		}
		return true
	}

	// Simple prefix matching
	return strings.HasPrefix(path, pattern)
}
