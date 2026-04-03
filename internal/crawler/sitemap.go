package crawler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/util"
	"github.com/rs/zerolog/log"
)

// decompressGzip decompresses gzip-encoded data
func decompressGzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress gzip data: %w", err)
	}

	return decompressed, nil
}

// isGzipContent checks if the response is gzip-encoded based on headers or URL
func isGzipContent(contentEncoding, url string) bool {
	// Check Content-Encoding header
	if strings.EqualFold(contentEncoding, "gzip") {
		return true
	}

	// Check URL suffix for .gz or .xml.gz
	lowerURL := strings.ToLower(url)
	return strings.HasSuffix(lowerURL, ".gz")
}

// SitemapDiscoveryResult contains both sitemaps and robots.txt rules
type SitemapDiscoveryResult struct {
	Sitemaps    []string
	RobotsRules *RobotsRules
}

// DiscoverSitemapsAndRobots attempts to find sitemaps and parse robots.txt rules for a domain
func (c *Crawler) DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*SitemapDiscoveryResult, error) {
	// Normalise the domain first to handle different input formats
	normalisedDomain := util.NormaliseDomain(domain)
	log.Debug().
		Str("original_domain", domain).
		Str("normalised_domain", normalisedDomain).
		Msg("Starting sitemap and robots.txt discovery")

	result := &SitemapDiscoveryResult{
		Sitemaps:    []string{},
		RobotsRules: &RobotsRules{}, // Default empty rules
	}

	// Parse robots.txt first - this gets us both sitemaps and crawl rules
	robotRules, err := ParseRobotsTxt(ctx, normalisedDomain, c.config.UserAgent)
	if err != nil {
		// Log at warn so TLS/network issues are visible in production logs
		log.Warn().
			Err(err).
			Str("domain", normalisedDomain).
			Msg("Failed to parse robots.txt, proceeding with no restrictions")
	} else {
		result.RobotsRules = robotRules
		result.Sitemaps = robotRules.Sitemaps
	}

	// Log if sitemaps were found in robots.txt
	if len(result.Sitemaps) > 0 {
		log.Debug().
			Strs("sitemaps", result.Sitemaps).
			Msg("Sitemaps found in robots.txt")
	} else {
		log.Debug().Msg("No sitemaps found in robots.txt")
	}

	// If no sitemaps found in robots.txt, check common locations
	if len(result.Sitemaps) == 0 {
		commonPaths := []string{
			"https://" + normalisedDomain + "/sitemap.xml",
			"https://" + normalisedDomain + "/sitemap_index.xml",
		}

		// Create a client for checking common locations
		client := &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}

		// Check common locations concurrently with a timeout
		for _, sitemapURL := range commonPaths {
			log.Debug().Str("checking_sitemap_url", sitemapURL).Msg("Checking common sitemap location")
			req, err := http.NewRequestWithContext(ctx, "HEAD", sitemapURL, nil)
			if err != nil {
				log.Debug().Err(err).Str("url", sitemapURL).Msg("Error creating request for sitemap")
				continue
			}
			req.Header.Set("User-Agent", c.config.UserAgent)

			resp, err := client.Do(req)
			if err != nil {
				log.Warn().Err(err).Str("url", sitemapURL).Msg("Error fetching sitemap at common location")
				continue
			}

			_ = resp.Body.Close()
			log.Debug().Str("url", sitemapURL).Int("status", resp.StatusCode).Msg("Sitemap check response")
			if resp.StatusCode == http.StatusOK {
				result.Sitemaps = append(result.Sitemaps, sitemapURL)
				log.Debug().Str("url", sitemapURL).Msg("Found sitemap at common location")
			}
		}
	}

	// Deduplicate sitemaps
	seen := make(map[string]bool)
	var uniqueSitemaps []string
	for _, sitemap := range result.Sitemaps {
		if !seen[sitemap] {
			seen[sitemap] = true
			uniqueSitemaps = append(uniqueSitemaps, sitemap)
		}
	}
	result.Sitemaps = uniqueSitemaps

	// Log final result
	if len(result.Sitemaps) > 0 {
		log.Debug().
			Strs("sitemaps", result.Sitemaps).
			Int("count", len(result.Sitemaps)).
			Int("crawl_delay", result.RobotsRules.CrawlDelay).
			Int("disallow_patterns", len(result.RobotsRules.DisallowPatterns)).
			Msg("Found sitemaps and robots rules for domain")
	} else {
		log.Debug().
			Str("domain", domain).
			Int("crawl_delay", result.RobotsRules.CrawlDelay).
			Int("disallow_patterns", len(result.RobotsRules.DisallowPatterns)).
			Msg("No sitemaps found but got robots rules for domain")
	}

	return result, nil
}

// DiscoverSitemaps is a backward-compatible wrapper that only returns sitemaps
func (c *Crawler) DiscoverSitemaps(ctx context.Context, domain string) ([]string, error) {
	result, err := c.DiscoverSitemapsAndRobots(ctx, domain)
	if err != nil {
		return nil, err
	}
	return result.Sitemaps, nil
}

// Create proper sitemap structs
type SitemapIndex struct {
	XMLName  xml.Name  `xml:"sitemapindex"`
	Sitemaps []Sitemap `xml:"sitemap"`
}

type Sitemap struct {
	XMLName xml.Name `xml:"sitemap"`
	Loc     string   `xml:"loc"`
}

type URLSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []URL    `xml:"url"`
}

type URL struct {
	XMLName xml.Name `xml:"url"`
	Loc     string   `xml:"loc"`
}

// ParseSitemap extracts URLs from a sitemap
func (c *Crawler) ParseSitemap(ctx context.Context, sitemapURL string) ([]string, error) {
	var urls []string

	req, err := http.NewRequestWithContext(ctx, "GET", sitemapURL, nil)
	if err != nil {
		return nil, err
	}

	// Request gzip encoding if server supports it
	req.Header.Set("Accept-Encoding", "gzip")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch sitemap: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Decompress if gzip-encoded (via header or .gz URL suffix)
	contentEncoding := resp.Header.Get("Content-Encoding")
	if isGzipContent(contentEncoding, sitemapURL) {
		log.Debug().
			Str("url", sitemapURL).
			Str("content_encoding", contentEncoding).
			Int("compressed_size", len(body)).
			Msg("Decompressing gzip sitemap")

		body, err = decompressGzip(body)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress sitemap %s: %w", sitemapURL, err)
		}

		log.Debug().
			Str("url", sitemapURL).
			Int("decompressed_size", len(body)).
			Msg("Sitemap decompressed successfully")
	}

	// Log the content for debugging
	log.Debug().
		Str("url", sitemapURL).
		Int("content_length", len(body)).
		Str("content_sample", string(body[:min(100, len(body))])).
		Msg("Sitemap content received")

	// Try to unmarshal as a sitemap index first.
	// encoding/xml handles CDATA, XML entities (&amp; etc.), namespaces,
	// and whitespace automatically.
	var index SitemapIndex
	if err := xml.Unmarshal(body, &index); err == nil && len(index.Sitemaps) > 0 {
		log.Debug().
			Str("url", sitemapURL).
			Int("child_count", len(index.Sitemaps)).
			Msg("Parsed as sitemap index")

		for _, child := range index.Sitemaps {
			childURL := util.NormaliseURL(strings.TrimSpace(child.Loc))
			if childURL == "" {
				log.Warn().Str("url", child.Loc).Msg("Invalid child sitemap URL, skipping")
				continue
			}

			childURLs, err := c.ParseSitemap(ctx, childURL)
			if err != nil {
				log.Warn().Err(err).Str("url", childURL).Msg("Failed to parse child sitemap")
				continue
			}
			urls = append(urls, childURLs...)
		}
	} else {
		// Parse as a regular URL set
		var urlSet URLSet
		if err := xml.Unmarshal(body, &urlSet); err != nil {
			log.Warn().Err(err).Str("url", sitemapURL).Msg("Failed to parse sitemap XML")
			// Return empty rather than error — malformed sitemaps shouldn't halt crawling
			return urls, nil
		}

		var validURLs []string
		for _, u := range urlSet.URLs {
			validURL := util.NormaliseURL(strings.TrimSpace(u.Loc))
			if validURL != "" {
				validURLs = append(validURLs, validURL)
			} else {
				log.Debug().Str("invalid_url", u.Loc).Msg("Skipping invalid URL from sitemap")
			}
		}

		log.Debug().
			Str("sitemap_url", sitemapURL).
			Int("url_count", len(validURLs)).
			Msg("Extracted valid URLs from regular sitemap")
		urls = append(urls, validURLs...)
	}

	log.Debug().
		Str("sitemap_url", sitemapURL).
		Int("total_url_count", len(urls)).
		Msg("Finished parsing sitemap")

	return urls, nil
}

// FilterURLs filters URLs based on include/exclude patterns
func (c *Crawler) FilterURLs(urls []string, includePaths, excludePaths []string) []string {
	if len(includePaths) == 0 && len(excludePaths) == 0 {
		return urls
	}

	var filtered []string

	for _, url := range urls {
		// If include patterns exist, URL must match at least one
		includeMatch := len(includePaths) == 0
		for _, pattern := range includePaths {
			if strings.Contains(url, pattern) {
				includeMatch = true
				break
			}
		}

		if !includeMatch {
			continue
		}

		// If URL matches any exclude pattern, skip it
		excludeMatch := false
		for _, pattern := range excludePaths {
			if strings.Contains(url, pattern) {
				excludeMatch = true
				break
			}
		}

		if !excludeMatch {
			filtered = append(filtered, url)
		}
	}

	return filtered
}
