package crawler

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
	"github.com/rs/zerolog/log"
)

// normaliseCacheStatus converts CDN-specific cache status strings to standard values.
// Covers top 10 CDNs representing ~90% of web traffic (verified Dec 2025):
//   - Cloudflare (64%): HIT, MISS, DYNAMIC, BYPASS, EXPIRED, STALE, REVALIDATED, UPDATING, NONE, UNKNOWN
//   - Google Cloud CDN (13%): No explicit header (uses Age/Via)
//   - Fastly (12%): HIT, MISS, PASS (can be comma-separated for shielding: "HIT, HIT")
//   - CloudFront (3%): "Hit from cloudfront", "Miss from cloudfront", "RefreshHit from cloudfront"
//   - Akamai (3%): TCP_HIT, TCP_MISS, TCP_MEM_HIT, TCP_REFRESH_HIT, TCP_DENIED, etc.
//   - Azure CDN (~2%): TCP_HIT, TCP_MISS, TCP_REMOTE_HIT, UNCACHEABLE
//   - Vercel (~1%): HIT, MISS, STALE, PRERENDER
//   - Netlify (~1%): RFC 9211 format - "Netlify Edge"; hit
//   - KeyCDN (<1%): HIT, MISS
//   - Varnish: X-Varnish header (handled separately)
//
// Sources:
//   - https://developers.cloudflare.com/cache/concepts/cache-responses/
//   - https://repost.aws/knowledge-center/cloudfront-x-cachemiss-error
//   - https://techdocs.akamai.com/property-mgr/docs/return-cache-status
//   - https://vercel.com/docs/headers/response-headers
func normaliseCacheStatus(status string) string {
	// Trim whitespace from input
	status = strings.TrimSpace(status)
	if status == "" {
		return ""
	}

	upper := strings.ToUpper(status)

	// Fastly shielding: "HIT, MISS" or "MISS, HIT" - take the last value (edge POP result)
	if strings.Contains(upper, ", ") {
		parts := strings.Split(upper, ", ")
		if len(parts) > 0 {
			upper = strings.TrimSpace(parts[len(parts)-1])
		}
	}

	// CloudFront style: "Hit from cloudfront", "Miss from cloudfront"
	if strings.Contains(upper, "FROM CLOUDFRONT") {
		if strings.HasPrefix(upper, "HIT") || strings.HasPrefix(upper, "REFRESHHIT") {
			return "HIT"
		}
		if strings.HasPrefix(upper, "MISS") {
			return "MISS"
		}
		// LambdaGeneratedResponse, Error, etc. - treat as BYPASS
		return "BYPASS"
	}

	// Akamai style: "TCP_HIT from child" or "TCP_MISS from child, TCP_HIT from parent"
	if strings.Contains(upper, " FROM ") && strings.HasPrefix(upper, "TCP_") {
		// Extract just the status part before " from"
		parts := strings.Split(upper, " FROM")
		if len(parts) > 0 {
			upper = strings.TrimSpace(parts[0])
		}
	}

	// Akamai/Azure CDN style: TCP_HIT, TCP_MISS, TCP_MEM_HIT, TCP_REFRESH_HIT, etc.
	if strings.HasPrefix(upper, "TCP_") {
		if strings.Contains(upper, "HIT") {
			return "HIT"
		}
		if strings.Contains(upper, "MISS") {
			return "MISS"
		}
		// TCP_DENIED, TCP_COOKIE_DENY - treat as BYPASS
		return "BYPASS"
	}

	// Azure CDN / Cloudflare uncacheable states
	switch upper {
	case "UNCACHEABLE", "NONE", "UNKNOWN":
		return "BYPASS"
	}

	// RFC 9211 Cache-Status format (Netlify, future CDNs)
	// Examples: "Netlify Edge"; hit, "CacheName"; fwd=miss
	if strings.Contains(status, ";") {
		lower := strings.ToLower(status)
		if strings.Contains(lower, "; hit") || strings.Contains(lower, ";hit") {
			return "HIT"
		}
		if strings.Contains(lower, "fwd=") {
			// fwd=uri-miss, fwd=vary-miss, fwd=stale, fwd=miss all indicate cache miss
			return "MISS"
		}
	}

	// Standard formats - normalise to uppercase for consistency
	// Covers: HIT, MISS, DYNAMIC, BYPASS, EXPIRED, STALE, REVALIDATED, UPDATING, PRERENDER, PASS
	switch upper {
	case "HIT", "MISS", "DYNAMIC", "BYPASS", "EXPIRED", "STALE", "REVALIDATED", "UPDATING", "PRERENDER", "PASS":
		return upper
	}

	// Unknown formats - preserve original (already trimmed)
	return status
}

func stripDiagnosticURL(raw string) string {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""
	return parsedURL.String()
}

func buildRequestMetadata(method, targetURL, finalURL string, timestamp time.Time, provenance string) RequestMetadata {
	scrubbedTargetURL := stripDiagnosticURL(targetURL)
	scrubbedFinalURL := stripDiagnosticURL(finalURL)
	meta := RequestMetadata{
		Method:     method,
		URL:        scrubbedTargetURL,
		FinalURL:   scrubbedFinalURL,
		Timestamp:  timestamp.Unix(),
		Provenance: provenance,
	}

	parsedURL, err := url.Parse(scrubbedFinalURL)
	if err != nil {
		parsedURL, err = url.Parse(scrubbedTargetURL)
	}
	if err == nil {
		meta.Scheme = parsedURL.Scheme
		meta.Host = parsedURL.Host
		meta.Path = parsedURL.Path
		meta.Query = ""
	}

	return meta
}

func buildCacheMetadata(headers http.Header) CacheMetadata {
	cache := CacheMetadata{
		Age:           headers.Get("Age"),
		CacheControl:  headers.Get("Cache-Control"),
		Vary:          headers.Get("Vary"),
		CacheStatus:   headers.Get("Cache-Status"),
		CFCacheStatus: headers.Get("CF-Cache-Status"),
		XCache:        headers.Get("X-Cache"),
		XCacheRemote:  headers.Get("X-Cache-Remote"),
		XVercelCache:  headers.Get("x-vercel-cache"),
		XVarnish:      headers.Get("X-Varnish"),
	}

	for _, candidate := range []struct {
		name  string
		value string
	}{
		{name: "CF-Cache-Status", value: cache.CFCacheStatus},
		{name: "X-Cache", value: cache.XCache},
		{name: "X-Cache-Remote", value: cache.XCacheRemote},
		{name: "x-vercel-cache", value: cache.XVercelCache},
		{name: "Cache-Status", value: cache.CacheStatus},
	} {
		if candidate.value != "" {
			cache.HeaderSource = candidate.name
			cache.RawValue = candidate.value
			cache.NormalisedStatus = normaliseCacheStatus(candidate.value)
			return cache
		}
	}

	if cache.XVarnish != "" {
		cache.HeaderSource = "X-Varnish"
		cache.RawValue = cache.XVarnish
		if strings.Contains(cache.XVarnish, " ") {
			cache.NormalisedStatus = "HIT"
		} else {
			cache.NormalisedStatus = "MISS"
		}
	}

	return cache
}

func redactDiagnosticHeaders(headers http.Header) http.Header {
	cloned := headers.Clone()
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"Set-Cookie",
		"X-Api-Key",
		"X-Auth-Token",
	} {
		cloned.Del(name)
	}
	return cloned
}

func buildProbeErrorDiagnostics(method, targetURL string, err error) ProbeDiagnostics {
	requestMeta := buildRequestMetadata(method, targetURL, targetURL, time.Now().UTC(), "probe")
	responseMeta := ResponseMetadata{Error: err.Error()}
	return ProbeDiagnostics{
		Request:  &requestMeta,
		Response: &responseMeta,
	}
}

func buildRequestAttemptDiagnostics(
	method string,
	targetURL string,
	finalURL string,
	timestamp time.Time,
	provenance string,
	requestHeaders http.Header,
	responseHeaders http.Header,
	response ResponseMetadata,
	timing PerformanceMetrics,
) *RequestAttemptDiagnostics {
	requestHeaders = redactDiagnosticHeaders(requestHeaders)
	responseHeaders = redactDiagnosticHeaders(responseHeaders)
	requestMeta := buildRequestMetadata(method, targetURL, finalURL, timestamp, provenance)
	cacheMeta := buildCacheMetadata(responseHeaders)

	return &RequestAttemptDiagnostics{
		Request:         &requestMeta,
		Response:        &response,
		RequestHeaders:  requestHeaders,
		ResponseHeaders: responseHeaders,
		Timing:          &timing,
		Cache:           &cacheMeta,
	}
}

// Crawler represents a URL crawler with configuration and metrics
type Crawler struct {
	config     *Config
	colly      *colly.Collector
	id         string    // Add an ID field to identify each crawler instance
	metricsMap *sync.Map // Shared metrics storage for the transport
}

// GetUserAgent returns the user agent string for this crawler
func (c *Crawler) GetUserAgent() string {
	return c.config.UserAgent
}

// tracingRoundTripper captures HTTP trace metrics for each request
type tracingRoundTripper struct {
	transport  http.RoundTripper
	metricsMap *sync.Map // Maps URL -> PerformanceMetrics
}

// RoundTrip implements the http.RoundTripper interface with httptrace instrumentation
func (t *tracingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Create performance metrics for this request
	metrics := &PerformanceMetrics{}

	// Create trace with callbacks that populate metrics
	var dnsStartTime, connectStartTime, tlsStartTime, requestStartTime time.Time
	requestStartTime = time.Now()

	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStartTime = time.Now()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if !dnsStartTime.IsZero() {
				metrics.DNSLookupTime = time.Since(dnsStartTime).Milliseconds()
			}
		},
		ConnectStart: func(network, addr string) {
			connectStartTime = time.Now()
		},
		ConnectDone: func(network, addr string, err error) {
			if err == nil && !connectStartTime.IsZero() {
				metrics.TCPConnectionTime = time.Since(connectStartTime).Milliseconds()
			}
		},
		TLSHandshakeStart: func() {
			tlsStartTime = time.Now()
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err == nil && !tlsStartTime.IsZero() {
				metrics.TLSHandshakeTime = time.Since(tlsStartTime).Milliseconds()
			}
		},
		GotFirstResponseByte: func() {
			metrics.TTFB = time.Since(requestStartTime).Milliseconds()
		},
	}

	// Store metrics for this URL (will be retrieved in OnResponse)
	t.metricsMap.Store(req.URL.String(), metrics)

	// Attach trace to request context
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	// Perform the request
	return t.transport.RoundTrip(req)
}

// New creates a new Crawler instance with the given configuration and optional ID
// If config is nil, default configuration is used
func New(config *Config, id ...string) *Crawler {
	if config == nil {
		config = DefaultConfig()
	}

	crawlerID := ""
	if len(id) > 0 {
		crawlerID = id[0]
	}

	userAgent := config.UserAgent
	if crawlerID != "" {
		userAgent = fmt.Sprintf("%s Worker-%s", config.UserAgent, crawlerID)
	}

	c := colly.NewCollector(
		colly.UserAgent(userAgent),
		colly.MaxDepth(1),
		colly.Async(true),
		colly.AllowURLRevisit(),
	)

	// Set rate limiting with randomised delays between requests
	// RateLimit determines base delay: Delay = 1s / RateLimit
	// RandomDelay = 1s - Delay to create jitter range from base to 1s
	// Example: RateLimit=5 → Delay=200ms, RandomDelay=800ms → Total: 200ms-1s per request
	baseDelay := time.Second / time.Duration(config.RateLimit)
	if err := c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: config.MaxConcurrency,
		Delay:       baseDelay,
		RandomDelay: time.Second - baseDelay,
	}); err != nil {
		log.Error().Err(err).Msg("Failed to set crawler limits")
	}

	// Create metrics map for this crawler instance
	metricsMap := &sync.Map{}

	// Set up base transport with SSRF-safe dialer
	baseTransport := &http.Transport{
		MaxIdleConnsPerHost: 25,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   true,
	}

	// Add SSRF-safe DialContext if protection is enabled
	// This validates IPs at connection time to prevent DNS rebinding attacks
	if !config.SkipSSRFCheck {
		baseTransport.DialContext = ssrfSafeDialContext()
	}

	// Wrap the base transport with our custom tracing transport
	tracingTransport := &tracingRoundTripper{
		transport:  baseTransport,
		metricsMap: metricsMap,
	}

	// Set HTTP client with tracing transport and proper timeout
	httpClient := &http.Client{
		Timeout:   config.DefaultTimeout,
		Transport: tracingTransport,
	}
	c.SetClient(httpClient)

	// Add browser-like headers to avoid blocking
	c.OnRequest(func(r *colly.Request) {
		// Set browser-like headers
		r.Headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
		r.Headers.Set("Accept-Language", "en-US,en;q=0.9")
		r.Headers.Set("Accept-Encoding", "gzip, deflate, br")

		// Set Referer to site homepage for more browser-like behaviour
		if r.URL.Host != "" {
			r.Headers.Set("Referer", fmt.Sprintf("https://%s/", r.URL.Host))
		}

		log.Debug().
			Str("url", r.URL.String()).
			Msg("Crawler sending request")
	})

	// Note: OnHTML handler will be registered on the clone in WarmURL to ensure proper context access

	return &Crawler{
		config:     config,
		colly:      c,
		id:         crawlerID,
		metricsMap: metricsMap,
	}
}

// isPrivateOrLocalIP checks if an IP address is in a private, loopback, or link-local range.
// This prevents SSRF attacks by blocking requests to internal network resources.
func isPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	// Check for loopback (127.x.x.x, ::1)
	if ip.IsLoopback() {
		return true
	}

	// Check for link-local (169.254.x.x, fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// Check for private ranges (10.x, 172.16-31.x, 192.168.x, fc00::/7)
	if ip.IsPrivate() {
		return true
	}

	// Check for unspecified (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return true
	}

	return false
}

// ssrfSafeDialContext returns a DialContext function that validates resolved IPs
// before connecting, preventing DNS rebinding attacks and SSRF to private networks.
// It performs DNS resolution, validates all IPs are public, then connects using
// the validated IP (preferring IPv4 for compatibility).
func ssrfSafeDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		// Resolve hostname and validate all IPs
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("DNS lookup failed: %w", err)
		}

		// Check all resolved IPs for private/local addresses
		for _, ip := range ips {
			if isPrivateOrLocalIP(ip) {
				log.Warn().
					Str("host", host).
					Str("ip", ip.String()).
					Msg("SSRF protection: blocked connection to private/local IP")
				return nil, fmt.Errorf("blocked connection to private/local IP: %s resolves to %s", host, ip.String())
			}
		}

		// Connect using a validated IP (prefer IPv4 for compatibility)
		var connectAddr string
		for _, ip := range ips {
			if ip.To4() != nil {
				connectAddr = net.JoinHostPort(ip.String(), port)
				break
			}
		}
		if connectAddr == "" && len(ips) > 0 {
			connectAddr = net.JoinHostPort(ips[0].String(), port)
		}

		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, network, connectAddr)
	}
}

// validateCrawlRequest validates the crawl request parameters and URL format.
// Note: SSRF protection is handled at connection time by ssrfSafeDialContext(),
// which prevents DNS rebinding attacks by validating IPs after resolution.
func validateCrawlRequest(ctx context.Context, targetURL string, _ bool) (*url.URL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid URL format: %s", targetURL)
	}

	return parsed, nil
}

// setupResponseHandlers configures Colly response and error handlers for crawl result collection
func (c *Crawler) setupResponseHandlers(collyClone *colly.Collector, result *CrawlResult, startTime time.Time, targetURL string) {
	// Handle response - collect cache headers, status, timing
	collyClone.OnResponse(func(r *colly.Response) {
		startTime := r.Ctx.GetAny("start_time").(time.Time)
		result := r.Ctx.GetAny("result").(*CrawlResult)

		// Retrieve performance metrics from the metrics map
		if metricsVal, ok := c.metricsMap.LoadAndDelete(r.Request.URL.String()); ok {
			performanceMetrics := metricsVal.(*PerformanceMetrics)
			// Content transfer time is total response time minus TTFB
			if performanceMetrics.TTFB > 0 {
				performanceMetrics.ContentTransferTime = time.Since(startTime).Milliseconds() - performanceMetrics.TTFB
			}
			result.Performance = *performanceMetrics
		}

		// Calculate response time
		result.ResponseTime = time.Since(startTime).Milliseconds()
		result.StatusCode = r.StatusCode
		result.ContentType = r.Headers.Get("Content-Type")
		result.ContentLength = int64(len(r.Body))
		result.Headers = r.Headers.Clone()
		result.RedirectURL = r.Request.URL.String()
		requestHeaders := r.Request.Headers.Clone()

		// Store body for tech detection and storage upload
		// BodySample is truncated for wappalyzer detection, Body is the full content
		result.Body = r.Body
		if len(r.Body) > MaxBodySampleSize {
			result.BodySample = r.Body[:MaxBodySampleSize]
		} else {
			result.BodySample = r.Body
		}

		// Log comprehensive Cloudflare headers for analysis
		cfCacheStatus := r.Headers.Get("CF-Cache-Status")
		cfRay := r.Headers.Get("CF-Ray")
		cfDatacenter := r.Headers.Get("CF-IPCountry")
		cfConnectingIP := r.Headers.Get("CF-Connecting-IP")
		cfVisitor := r.Headers.Get("CF-Visitor")

		log.Debug().
			Str("url", r.Request.URL.String()).
			Str("cf_cache_status", cfCacheStatus).
			Str("cf_ray", cfRay).
			Str("cf_datacenter", cfDatacenter).
			Str("cf_connecting_ip", cfConnectingIP).
			Str("cf_visitor", cfVisitor).
			Int64("response_time_ms", result.ResponseTime).
			Msg("Cloudflare headers analysis")

		cacheMeta := buildCacheMetadata(result.Headers)
		result.CacheStatus = cacheMeta.NormalisedStatus

		// Set error for non-2xx status codes (to match test expectations)
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			result.Error = fmt.Sprintf("non-success status code: %d", r.StatusCode)
		}

		result.RequestDiagnostics.Primary = buildRequestAttemptDiagnostics(
			r.Request.Method,
			targetURL,
			result.RedirectURL,
			startTime,
			"primary",
			requestHeaders,
			result.Headers,
			ResponseMetadata{
				StatusCode:    result.StatusCode,
				ContentType:   result.ContentType,
				ContentLength: result.ContentLength,
				RedirectURL:   stripDiagnosticURL(result.RedirectURL),
				Warning:       result.Warning,
				Error:         result.Error,
			},
			result.Performance,
		)
	})

	// Handle errors
	collyClone.OnError(func(r *colly.Response, err error) {
		if r == nil || r.Ctx == nil {
			return
		}
		result := r.Ctx.GetAny("result").(*CrawlResult)
		result.Error = err.Error()

		startTime := r.Ctx.GetAny("start_time").(time.Time)
		result.ResponseTime = time.Since(startTime).Milliseconds()
		result.StatusCode = r.StatusCode
		requestHeaders := r.Request.Headers.Clone()
		responseHeaders := http.Header{}
		if r.Headers != nil {
			responseHeaders = r.Headers.Clone()
		}
		result.Headers = responseHeaders
		if r.Request != nil && r.Request.URL != nil {
			result.RedirectURL = r.Request.URL.String()
		}

		result.RequestDiagnostics.Primary = buildRequestAttemptDiagnostics(
			r.Request.Method,
			targetURL,
			result.RedirectURL,
			startTime,
			"primary",
			requestHeaders,
			responseHeaders,
			ResponseMetadata{
				StatusCode:    result.StatusCode,
				ContentType:   result.ContentType,
				ContentLength: result.ContentLength,
				RedirectURL:   stripDiagnosticURL(result.RedirectURL),
				Warning:       result.Warning,
				Error:         result.Error,
			},
			result.Performance,
		)

		log.Debug().
			Err(err).
			Str("url", targetURL).
			Dur("duration_ms", time.Duration(result.ResponseTime)*time.Millisecond).
			Msg("URL warming failed")
	})
}

// performCacheValidation handles cache warming logic if cache miss is detected
func (c *Crawler) performCacheValidation(ctx context.Context, targetURL string, res *CrawlResult) error {
	// Only perform cache warming if we got a MISS or EXPIRED
	if !shouldMakeSecondRequest(res.CacheStatus) {
		log.Debug().
			Str("url", targetURL).
			Str("cache_status", res.CacheStatus).
			Msg("No cache warming needed - cache already available or not cacheable")
		return nil
	}

	// Apply randomized delay between 500-1000ms to avoid hammering origins
	randomInt := 0
	if n, err := crand.Int(crand.Reader, big.NewInt(501)); err == nil {
		randomInt = int(n.Int64())
	} else {
		// Fallback to basic rand if crypto rand fails
		randomInt = rand.Intn(501) //nolint:gosec // safe fallback for non-sensitive jitter
	}
	jitteredDelay := 500 + randomInt

	log.Debug().
		Str("url", targetURL).
		Str("cache_status", res.CacheStatus).
		Int64("initial_response_time", res.ResponseTime).
		Int("calculated_delay_ms", jitteredDelay).
		Msg("Cache MISS detected, applying jittered delay before cache validation")

	// Wait for initial delay to allow CDN to process and cache
	select {
	case <-time.After(time.Duration(jitteredDelay) * time.Millisecond):
		// Continue with cache check loop
	case <-ctx.Done():
		// Context cancelled during wait
		log.Debug().Str("url", targetURL).Msg("Cache warming cancelled during initial delay")
		return nil // First request was successful, return that
	}

	// Check cache status with HEAD requests in a loop
	maxChecks := 3
	delayBeforeAttempt := jitteredDelay
	nextCheckDelay := 700 // Delay between subsequent HEAD checks
	cacheHit := false

	for i := range maxChecks {
		probe, err := c.CheckCacheStatus(ctx, targetURL)
		probe.Attempt = i + 1
		probe.DelayMS = delayBeforeAttempt
		cacheStatus := ""
		if probe.Cache != nil {
			cacheStatus = probe.Cache.NormalisedStatus
		}

		attempt := CacheCheckAttempt{
			Attempt:     i + 1,
			CacheStatus: cacheStatus,
			Delay:       delayBeforeAttempt,
			Diagnostics: &probe,
		}
		res.CacheCheckAttempts = append(res.CacheCheckAttempts, attempt)
		if res.RequestDiagnostics != nil {
			res.RequestDiagnostics.Probes = append(res.RequestDiagnostics.Probes, probe)
		}

		if err != nil {
			log.Warn().
				Err(err).
				Str("url", targetURL).
				Int("check_attempt", i+1).
				Msg("Failed to check cache status")
		} else {
			log.Debug().
				Str("url", targetURL).
				Str("cache_status", cacheStatus).
				Int("check_attempt", i+1).
				Msg("Cache status check")

			// If cache is now HIT, we can proceed with second request
			if cacheStatus == "HIT" || cacheStatus == "STALE" || cacheStatus == "REVALIDATED" {
				cacheHit = true
				break
			}

			// If CDN indicates the response will not be cached, skip further checks
			if !shouldMakeSecondRequest(cacheStatus) {
				log.Debug().
					Str("url", targetURL).
					Str("cache_status", cacheStatus).
					Int("check_attempt", i+1).
					Msg("Cache status indicates resource will not warm; skipping additional checks")
				break
			}
		}

		// If not the last check, wait before next attempt
		if i < maxChecks-1 {
			select {
			case <-time.After(time.Duration(nextCheckDelay) * time.Millisecond):
				delayBeforeAttempt = nextCheckDelay
				// Continue to next check
			case <-ctx.Done():
				log.Debug().Str("url", targetURL).Msg("Cache warming cancelled during check loop")
				return nil
			}
			// Increase delay for the next iteration
			nextCheckDelay += 300
		}
	}

	// Log whether cache became available
	if cacheHit {
		log.Debug().
			Str("url", targetURL).
			Msg("Cache is now available, proceeding with second request")
	} else {
		log.Warn().
			Str("url", targetURL).
			Int("max_checks", maxChecks).
			Msg("Cache did not become available after maximum checks")
	}

	if cacheHit {
		// Perform second request to measure cached response time
		secondResult, err := c.makeSecondRequest(ctx, targetURL)
		if err != nil {
			log.Warn().
				Err(err).
				Str("url", targetURL).
				Msg("Second request failed, but first request succeeded")
			// Don't return error - first request was successful
		} else {
			res.SecondResponseTime = secondResult.ResponseTime
			res.SecondCacheStatus = secondResult.CacheStatus
			res.SecondContentLength = secondResult.ContentLength
			res.SecondHeaders = secondResult.Headers
			res.SecondPerformance = &secondResult.Performance
			if secondResult.RequestDiagnostics != nil && secondResult.RequestDiagnostics.Primary != nil {
				secondary := *secondResult.RequestDiagnostics.Primary
				secondary.Request.Provenance = "secondary"
				if res.RequestDiagnostics != nil {
					res.RequestDiagnostics.Secondary = &secondary
				}
			}

			// Calculate improvement ratio for pattern analysis
			improvementRatio := float64(res.ResponseTime) / float64(res.SecondResponseTime)

			log.Debug().
				Str("url", targetURL).
				Str("first_cache_status", res.CacheStatus).
				Str("second_cache_status", res.SecondCacheStatus).
				Int64("first_response_time", res.ResponseTime).
				Int64("second_response_time", res.SecondResponseTime).
				Int("initial_delay_ms", jitteredDelay).
				Float64("improvement_ratio", improvementRatio).
				Bool("cache_hit_before_second", cacheHit).
				Msg("Cache warming analysis - pattern data")
		}
	} else {
		log.Debug().
			Str("url", targetURL).
			Str("cache_status", res.CacheStatus).
			Msg("Cache status did not transition to HIT; skipping second request")
	}

	return nil
}

// setupLinkExtraction configures Colly HTML handler for link extraction and categorization
func setupLinkExtraction(collyClone *colly.Collector) {
	collyClone.OnHTML("html", func(e *colly.HTMLElement) {
		// Check if link extraction is enabled for this request
		findLinksVal := e.Request.Ctx.GetAny("find_links")
		if findLinksVal == nil {
			log.Debug().
				Str("url", e.Request.URL.String()).
				Msg("find_links not set in context - defaulting to enabled")
		} else if findLinks, ok := findLinksVal.(bool); ok && !findLinks {
			log.Debug().
				Str("url", e.Request.URL.String()).
				Bool("find_links", findLinks).
				Msg("Link extraction disabled for this request")
			return
		}

		result, ok := e.Request.Ctx.GetAny("result").(*CrawlResult)
		if !ok {
			log.Debug().
				Str("url", e.Request.URL.String()).
				Msg("No result context - not collecting links")
			return
		}

		extractLinks := func(selection *goquery.Selection, category string) {
			selection.Find("a").Each(func(i int, s *goquery.Selection) {
				href := strings.TrimSpace(s.AttrOr("href", ""))
				if href == "" || href == "#" || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
					return
				}

				if isElementHidden(s) {
					return
				}

				var u string
				if strings.HasPrefix(href, "?") {
					base := e.Request.URL
					base.RawQuery = ""
					u = base.String() + href
				} else {
					u = e.Request.AbsoluteURL(href)
				}

				result.Links[category] = append(result.Links[category], u)
			})
		}

		// Extract from header and footer first
		extractLinks(e.DOM.Find("header"), "header")
		extractLinks(e.DOM.Find("footer"), "footer")

		// Remove header and footer to get body links
		e.DOM.Find("header").Remove()
		e.DOM.Find("footer").Remove()

		// Extract remaining links as "body"
		extractLinks(e.DOM, "body")

		log.Debug().
			Str("url", e.Request.URL.String()).
			Int("header_links", len(result.Links["header"])).
			Int("footer_links", len(result.Links["footer"])).
			Int("body_links", len(result.Links["body"])).
			Msg("Categorized links from page")
	})
}

// executeCollyRequest performs the HTTP request using Colly with context cancellation support
func executeCollyRequest(ctx context.Context, collyClone *colly.Collector, targetURL string, res *CrawlResult) error {
	// Set up context cancellation handling
	done := make(chan error, 1)

	// Visit the URL with Colly in a goroutine to support context cancellation
	go func() {
		visitErr := collyClone.Visit(targetURL)
		if visitErr != nil {
			done <- visitErr
			return
		}
		// Wait for async requests to complete
		collyClone.Wait()
		done <- nil
	}()

	// Wait for either completion or context cancellation
	// Note: HTTP client timeout (DefaultTimeout) enforces request-level timeout
	// Context timeout enforces overall task timeout
	select {
	case err := <-done:
		if err != nil {
			res.Error = err.Error()
			log.Debug().
				Err(err).
				Str("url", targetURL).
				Msg("Colly visit failed")
			return err
		}
		return nil
	case <-ctx.Done():
		res.Error = ctx.Err().Error()
		log.Debug().
			Err(ctx.Err()).
			Str("url", targetURL).
			Msg("URL warming cancelled due to context timeout")
		return ctx.Err()
	}
}

// WarmURL performs a crawl of the specified URL and returns the result.
// It respects context cancellation, enforces timeout, and treats non-2xx statuses as errors.
func (c *Crawler) WarmURL(ctx context.Context, targetURL string, findLinks bool) (*CrawlResult, error) {
	// Validate the crawl request (with SSRF protection unless skipped for tests)
	_, err := validateCrawlRequest(ctx, targetURL, c.config.SkipSSRFCheck)
	if err != nil {
		// Create error result - caller (WarmURL) is responsible for CrawlResult construction
		res := &CrawlResult{URL: targetURL, Timestamp: time.Now().Unix(), Error: err.Error()}
		return res, err
	}

	start := time.Now()
	res := &CrawlResult{
		URL:                targetURL,
		Timestamp:          start.Unix(),
		Links:              make(map[string][]string),
		RequestDiagnostics: &RequestDiagnostics{},
	}

	log.Debug().
		Str("url", targetURL).
		Bool("find_links", findLinks).
		Msg("Starting URL warming with Colly")

	// Use Colly for everything - single request handles cache warming and link extraction
	collyClone := c.colly.Clone()

	// Set up link extraction
	setupLinkExtraction(collyClone)

	// Set up timing and result collection
	collyClone.OnRequest(func(r *colly.Request) {
		r.Ctx.Put("result", res)
		r.Ctx.Put("start_time", start)
		r.Ctx.Put("find_links", findLinks)
	})

	// Set up response and error handlers
	c.setupResponseHandlers(collyClone, res, start, targetURL)

	// Execute the HTTP request
	if err := executeCollyRequest(ctx, collyClone, targetURL, res); err != nil {
		return res, err
	}

	// Log results and return error if needed
	if res.Error != "" {
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			log.Debug().
				Int("status", res.StatusCode).
				Str("url", targetURL).
				Str("error", res.Error).
				Dur("duration_ms", time.Duration(res.ResponseTime)*time.Millisecond).
				Msg("URL warming returned non-success status")
		} else {
			log.Debug().
				Str("url", targetURL).
				Str("error", res.Error).
				Dur("duration_ms", time.Duration(res.ResponseTime)*time.Millisecond).
				Msg("URL warming failed")
		}
		return res, fmt.Errorf("%s", res.Error)
	}

	// Perform cache validation and warming
	if err := c.performCacheValidation(ctx, targetURL, res); err != nil {
		return res, err
	}

	return res, nil
}

// shouldMakeSecondRequest determines if we should make a second request for cache warming
func shouldMakeSecondRequest(cacheStatus string) bool {
	// Make second request only for cache misses and expired content
	// Don't make second request for BYPASS/DYNAMIC (uncacheable), hits, stale, etc.
	switch strings.ToUpper(cacheStatus) {
	case "MISS", "EXPIRED":
		return true
	default:
		return false
	}
}

// makeSecondRequest performs a second request to verify cache warming
// Reuses the main WarmURL logic but disables link extraction
func (c *Crawler) makeSecondRequest(ctx context.Context, targetURL string) (*CrawlResult, error) {
	// Reuse the main WarmURL method but disable link extraction
	return c.WarmURL(ctx, targetURL, false)
}

func (c *Crawler) CheckCacheStatus(ctx context.Context, targetURL string) (ProbeDiagnostics, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", targetURL, nil)
	if err != nil {
		return buildProbeErrorDiagnostics(http.MethodHead, targetURL, err), err
	}

	req.Header.Set("User-Agent", c.config.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	// Use SSRF-safe transport if protection is enabled
	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if !c.config.SkipSSRFCheck {
		transport.DialContext = ssrfSafeDialContext()
	}

	client := &http.Client{
		Timeout:   c.config.DefaultTimeout,
		Transport: transport,
	}

	resp, err := client.Do(req)
	if err != nil {
		return buildProbeErrorDiagnostics(req.Method, targetURL, err), err
	}
	defer resp.Body.Close()

	responseHeaders := resp.Header.Clone()
	cacheMeta := buildCacheMetadata(responseHeaders)
	requestMeta := buildRequestMetadata(req.Method, targetURL, resp.Request.URL.String(), time.Now().UTC(), "probe")
	responseMeta := ResponseMetadata{StatusCode: resp.StatusCode, RedirectURL: stripDiagnosticURL(resp.Request.URL.String())}

	return ProbeDiagnostics{
		Request:  &requestMeta,
		Response: &responseMeta,
		Cache:    &cacheMeta,
		DelayMS:  0,
	}, nil
}

// CreateHTTPClient returns a configured HTTP client with SSRF protection
func (c *Crawler) CreateHTTPClient(timeout time.Duration) *http.Client {
	if timeout == 0 {
		timeout = c.config.DefaultTimeout
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost: 25,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   true,
	}

	// Add SSRF-safe DialContext if protection is enabled
	if !c.config.SkipSSRFCheck {
		transport.DialContext = ssrfSafeDialContext()
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// Config returns the Crawler's configuration.
func (c *Crawler) Config() *Config {
	return c.config
}

// isElementHidden checks if an element is hidden based on common inline styles,
// accessibility attributes, and conventional CSS classes.
// This is a best-effort check based on raw HTML attributes, as it does not
// evaluate external or internal CSS stylesheets.
func isElementHidden(s *goquery.Selection) bool {
	// Define the list of common hiding classes
	hidingClasses := []string{
		"hide",
		"hidden",
		"display-none",
		"d-none",
		"invisible",
		"is-hidden",
		"sr-only",
		"visually-hidden",
	}

	// Loop through the current element and all its parents up to the body
	for n := s; n.Length() > 0 && !n.Is("body"); n = n.Parent() {
		// 1. Check for explicit data attributes
		if _, exists := n.Attr("data-hidden"); exists {
			return true
		}
		if val, exists := n.Attr("data-visible"); exists && val == "false" {
			return true
		}

		// 2. Check for aria-hidden="true" attribute
		if ariaHidden, exists := n.Attr("aria-hidden"); exists && ariaHidden == "true" {
			return true
		}

		// 3. Check for inline style attributes
		if style, exists := n.Attr("style"); exists {
			if strings.Contains(style, "display: none") || strings.Contains(style, "visibility: hidden") {
				return true
			}
		}

		// 4. Check for common hiding classes
		if classAttr, exists := n.Attr("class"); exists {
			padded := " " + classAttr + " "
			for _, class := range hidingClasses {
				if strings.Contains(padded, " "+class+" ") {
					return true
				}
			}
		}
	}

	// No hiding attributes or classes were found
	return false
}
