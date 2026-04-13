package crawler

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testConfig returns a config suitable for tests with SSRF checks disabled
// to allow httptest.NewServer (127.0.0.1) to work
func testConfig() *Config {
	cfg := DefaultConfig()
	cfg.SkipSSRFCheck = true
	return cfg
}

func TestWarmURL(t *testing.T) {
	// Create a test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-Cache-Status", "HIT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Hello, World!"))
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), ts.URL, false)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if result.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, result.StatusCode)
	}

	if result.CacheStatus != "HIT" {
		t.Errorf("Expected cache status HIT, got %s", result.CacheStatus)
	}

	// Check that performance metrics are captured
	if result.Performance.TTFB == 0 {
		t.Log("Warning: TTFB not captured (may be too fast for local test)")
	}
	if result.Performance.TCPConnectionTime == 0 {
		t.Log("Warning: TCP connection time not captured (may be reused connection)")
	}
}

func TestPerformanceMetrics(t *testing.T) {
	// Create a test server with a small delay to ensure metrics are captured
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)        // Small delay to ensure measurable times
		w.Header().Set("CF-Cache-Status", "HIT") // Use HIT to avoid cache warming loop
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Performance test response"))
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), ts.URL, false)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Log all performance metrics
	t.Logf("Performance Metrics:")
	t.Logf("  DNS Lookup: %dms", result.Performance.DNSLookupTime)
	t.Logf("  TCP Connection: %dms", result.Performance.TCPConnectionTime)
	t.Logf("  TLS Handshake: %dms", result.Performance.TLSHandshakeTime)
	t.Logf("  TTFB: %dms", result.Performance.TTFB)
	t.Logf("  Content Transfer: %dms", result.Performance.ContentTransferTime)
	t.Logf("  Total Response Time: %dms", result.ResponseTime)

	// Verify that at least TTFB is captured (should always be > 0 with delay)
	if result.Performance.TTFB == 0 {
		t.Error("TTFB should be greater than 0")
	}

	// Verify total response time is reasonable
	if result.ResponseTime < 10 {
		t.Error("Response time should be at least 10ms due to server delay")
	}
}

func TestWarmURLCapturesPrimaryDiagnostics(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-Cache-Status", "HIT")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Age", "42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Hello, diagnostics!"))
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), ts.URL, false)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if result.RequestDiagnostics.Primary == nil {
		t.Fatal("Expected primary request diagnostics to be populated")
	}

	primary := result.RequestDiagnostics.Primary
	if primary.Request == nil {
		t.Fatal("Expected primary request metadata to be populated")
	}
	if primary.Request.Method != http.MethodGet {
		t.Fatalf("Expected primary method %s, got %s", http.MethodGet, primary.Request.Method)
	}
	if primary.Cache == nil {
		t.Fatal("Expected primary cache metadata to be populated")
	}
	if primary.Request.Provenance != "primary" {
		t.Fatalf("Expected primary provenance, got %s", primary.Request.Provenance)
	}
	if primary.Cache.HeaderSource != "CF-Cache-Status" {
		t.Fatalf("Expected cache header source CF-Cache-Status, got %s", primary.Cache.HeaderSource)
	}
	if primary.Cache.NormalisedStatus != "HIT" {
		t.Fatalf("Expected normalised cache status HIT, got %s", primary.Cache.NormalisedStatus)
	}
	if primary.ResponseHeaders.Get("CF-Cache-Status") != "HIT" {
		t.Fatalf("Expected response headers to include CF-Cache-Status")
	}
	if primary.RequestHeaders.Get("Accept") == "" {
		t.Fatalf("Expected request headers to include Accept")
	}
}

func TestWarmURLScrubsDiagnosticQueryStrings(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-Cache-Status", "HIT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Hello, scrubbed diagnostics!"))
	}))
	defer ts.Close()

	targetURL := ts.URL + "/offers?token=secret#frag"
	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), targetURL, false)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	primary := result.RequestDiagnostics.Primary
	if primary == nil || primary.Request == nil || primary.Response == nil {
		t.Fatal("Expected primary diagnostics to be populated")
	}
	if strings.Contains(primary.Request.URL, "?") || strings.Contains(primary.Request.URL, "#") {
		t.Fatalf("Expected scrubbed request URL, got %s", primary.Request.URL)
	}
	if strings.Contains(primary.Request.FinalURL, "?") || strings.Contains(primary.Request.FinalURL, "#") {
		t.Fatalf("Expected scrubbed final URL, got %s", primary.Request.FinalURL)
	}
	if primary.Request.Query != "" {
		t.Fatalf("Expected empty diagnostic query, got %s", primary.Request.Query)
	}
	if strings.Contains(primary.Response.RedirectURL, "?") || strings.Contains(primary.Response.RedirectURL, "#") {
		t.Fatalf("Expected scrubbed redirect URL, got %s", primary.Response.RedirectURL)
	}
}

func TestWarmURLCapturesProbeAndSecondaryDiagnostics(t *testing.T) {
	var getCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("CF-Cache-Status", "HIT")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			count := getCount.Add(1)
			if count == 1 {
				w.Header().Set("CF-Cache-Status", "MISS")
			} else {
				w.Header().Set("CF-Cache-Status", "HIT")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("cache me"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), ts.URL, false)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(result.RequestDiagnostics.Probes) == 0 {
		t.Fatal("Expected at least one probe diagnostic entry")
	}

	probe := result.RequestDiagnostics.Probes[0]
	if probe.Request == nil {
		t.Fatal("Expected probe request metadata")
	}
	if probe.Cache == nil {
		t.Fatal("Expected probe cache metadata")
	}
	if probe.Request.Method != http.MethodHead {
		t.Fatalf("Expected probe method %s, got %s", http.MethodHead, probe.Request.Method)
	}
	if probe.Cache.NormalisedStatus != "HIT" {
		t.Fatalf("Expected probe cache status HIT, got %s", probe.Cache.NormalisedStatus)
	}

	if result.RequestDiagnostics.Secondary == nil {
		t.Fatal("Expected secondary request diagnostics to be populated")
	}

	secondary := result.RequestDiagnostics.Secondary
	if secondary.Request == nil {
		t.Fatal("Expected secondary request metadata")
	}
	if secondary.Cache == nil {
		t.Fatal("Expected secondary cache metadata")
	}
	if secondary.Request.Provenance != "secondary" {
		t.Fatalf("Expected secondary provenance, got %s", secondary.Request.Provenance)
	}
	if secondary.Cache.NormalisedStatus != "HIT" {
		t.Fatalf("Expected secondary cache status HIT, got %s", secondary.Cache.NormalisedStatus)
	}
}

func TestWarmURLSecondaryRequestDoesNotRecurseIntoCacheValidation(t *testing.T) {
	var getCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("CF-Cache-Status", "HIT")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			getCount.Add(1)
			time.Sleep(2 * time.Millisecond)
			w.Header().Set("CF-Cache-Status", "MISS")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("still warming"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), ts.URL, false)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if got := getCount.Load(); got != 2 {
		t.Fatalf("Expected exactly 2 GET requests (primary + secondary), got %d", got)
	}

	if result.RequestDiagnostics == nil || result.RequestDiagnostics.Timings == nil {
		t.Fatal("Expected request timings to be populated")
	}

	if result.RequestDiagnostics.Timings.SecondaryRequestMS == 0 {
		t.Fatal("Expected secondary request timing to be recorded")
	}

	if result.SecondCacheStatus != "MISS" {
		t.Fatalf("Expected secondary cache status MISS, got %s", result.SecondCacheStatus)
	}
}

func TestMakeSecondRequestUsesSecondaryTimingBucket(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("CF-Cache-Status", "HIT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secondary request"))
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.makeSecondRequest(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if result.RequestDiagnostics == nil || result.RequestDiagnostics.Timings == nil {
		t.Fatal("Expected secondary request timings to be populated")
	}
	if result.RequestDiagnostics.Timings.SecondaryRequestMS == 0 {
		t.Fatal("Expected secondary timing to be recorded")
	}
	if result.RequestDiagnostics.Timings.PrimaryRequestMS != 0 {
		t.Fatalf("Expected primary timing bucket to remain empty, got %d", result.RequestDiagnostics.Timings.PrimaryRequestMS)
	}
}

func TestPerformCacheValidationReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	crawler := New(testConfig())
	result := &CrawlResult{
		URL:                "https://example.com",
		CacheStatus:        "MISS",
		RequestDiagnostics: &RequestDiagnostics{},
	}

	err := crawler.performCacheValidation(ctx, result.URL, result)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Expected context cancellation error, got %v", err)
	}
}

func TestMetricErrForRequestPhaseTreatsHTTPFailureAsError(t *testing.T) {
	res := &CrawlResult{
		StatusCode: http.StatusNotFound,
		Error:      "non-success status code: 404",
	}

	err := metricErrForRequestPhase(nil, res)
	if err == nil {
		t.Fatal("Expected HTTP failure to be treated as an error for telemetry")
	}
}

func TestClassifyProbeOutcomeTreatsHTTPFailureAsError(t *testing.T) {
	probe := ProbeDiagnostics{
		Response: &ResponseMetadata{StatusCode: http.StatusBadGateway},
	}

	outcome := classifyProbeOutcome(nil, probe)
	if outcome != "error" {
		t.Fatalf("Expected probe HTTP failure to be classified as error, got %s", outcome)
	}
}

func TestWarmURLReturnsErrorWhenSecondaryRequestFails(t *testing.T) {
	var getCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("CF-Cache-Status", "HIT")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			count := getCount.Add(1)
			if count == 1 {
				w.Header().Set("CF-Cache-Status", "MISS")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("warming"))
				return
			}

			w.Header().Set("CF-Cache-Status", "MISS")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("secondary failed"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	crawler := New(testConfig())
	result, err := crawler.WarmURL(context.Background(), ts.URL, false)
	if err == nil {
		t.Fatal("Expected secondary request failure to surface as an error")
	}
	if result == nil || result.RequestDiagnostics == nil || result.RequestDiagnostics.Secondary == nil {
		t.Fatal("Expected secondary diagnostics to be captured even when secondary request fails")
	}
	if result.RequestDiagnostics.Timings == nil || result.RequestDiagnostics.Timings.SecondaryRequestMS == 0 {
		t.Fatal("Expected secondary request timing to be recorded on failure")
	}
}

func TestCheckCacheStatusCapturesDiagnostics(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Status", `"Netlify Edge"; hit`)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	crawler := New(testConfig())
	probe, err := crawler.CheckCacheStatus(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if probe.Request == nil {
		t.Fatal("Expected probe request metadata")
	}
	if probe.Cache == nil {
		t.Fatal("Expected probe cache metadata")
	}
	if probe.Request.Method != http.MethodHead {
		t.Fatalf("Expected HEAD probe, got %s", probe.Request.Method)
	}
	if probe.Cache.HeaderSource != "Cache-Status" {
		t.Fatalf("Expected Cache-Status header source, got %s", probe.Cache.HeaderSource)
	}
	if probe.Cache.NormalisedStatus != "HIT" {
		t.Fatalf("Expected HIT cache status, got %s", probe.Cache.NormalisedStatus)
	}
}

func TestCheckCacheStatusReturnsPartialDiagnosticsOnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close()

	crawler := New(testConfig())
	probe, err := crawler.CheckCacheStatus(context.Background(), url)
	if err == nil {
		t.Fatal("Expected an error for unreachable probe target")
	}
	if probe.Request == nil {
		t.Fatal("Expected probe request metadata on error")
	}
	if probe.Response == nil {
		t.Fatal("Expected probe response metadata on error")
	}
	if probe.Response.Error == "" {
		t.Fatal("Expected probe error metadata to be populated")
	}
}

func TestPerformanceMetricsWithRealURL(t *testing.T) {
	// Skip in CI or if no internet connection
	if testing.Short() || os.Getenv("CI") != "" {
		t.Skip("Skipping test that requires internet connection (unreliable in CI)")
	}

	// Use a real HTTPS URL to test DNS, TCP, and TLS metrics
	crawler := New(nil)
	result, err := crawler.WarmURL(context.Background(), "https://httpbin.org/status/200", false)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Log all performance metrics
	t.Logf("Performance Metrics for HTTPS request:")
	t.Logf("  DNS Lookup: %dms", result.Performance.DNSLookupTime)
	t.Logf("  TCP Connection: %dms", result.Performance.TCPConnectionTime)
	t.Logf("  TLS Handshake: %dms", result.Performance.TLSHandshakeTime)
	t.Logf("  TTFB: %dms", result.Performance.TTFB)
	t.Logf("  Content Transfer: %dms", result.Performance.ContentTransferTime)
	t.Logf("  Total Response Time: %dms", result.ResponseTime)

	// For a real HTTPS request, we should capture at least some of these
	if result.Performance.DNSLookupTime == 0 &&
		result.Performance.TCPConnectionTime == 0 &&
		result.Performance.TLSHandshakeTime == 0 {
		t.Log("Warning: No connection metrics captured - connection might be reused")
	}

	// TTFB should always be captured
	if result.Performance.TTFB == 0 {
		t.Error("TTFB should be greater than 0 for real request")
	}
}

func TestWarmURLError(t *testing.T) {
	crawler := New(nil)
	// Use a malformed URL instead
	result, err := crawler.WarmURL(context.Background(), "not-a-valid-url", false)

	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}

	if result.Error == "" {
		t.Error("Expected error message in result, got empty string")
	}
}

func TestWarmURLWithDifferentStatuses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantError  bool
	}{
		{"success", http.StatusOK, false},
		{"not found", http.StatusNotFound, true},
		{"server error", http.StatusInternalServerError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte("status check"))
			}))
			defer ts.Close()

			crawler := New(testConfig())
			result, err := crawler.WarmURL(context.Background(), ts.URL, false)

			if (err != nil) != tt.wantError {
				t.Errorf("WarmURL() error = %v, wantError %v", err, tt.wantError)
			}
			if result.StatusCode != tt.statusCode {
				t.Errorf("WarmURL() status = %v, want %v", result.StatusCode, tt.statusCode)
			}
		})
	}
}

func TestWarmURLContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	crawler := New(testConfig())

	// Create a test server that delays
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Cancel context immediately
	cancel()

	// Should fail due to cancelled context
	_, err := crawler.WarmURL(ctx, ts.URL, false)
	if err == nil {
		t.Error("Expected error due to cancelled context, got nil")
	}
}

func TestWarmURLWithTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	crawler := New(testConfig())

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, err := crawler.WarmURL(ctx, ts.URL, false)
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
}

func TestNormaliseCacheStatus(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Empty input
		{"empty string", "", ""},

		// CloudFront formats (3% market share) - verified Dec 2025
		{"cloudfront hit", "Hit from cloudfront", "HIT"},
		{"cloudfront miss", "Miss from cloudfront", "MISS"},
		{"cloudfront refresh hit", "RefreshHit from cloudfront", "HIT"},
		{"cloudfront lambda generated", "LambdaGeneratedResponse from cloudfront", "BYPASS"},
		{"cloudfront error", "Error from cloudfront", "BYPASS"},
		{"cloudfront case insensitive", "MISS FROM CLOUDFRONT", "MISS"},
		{"cloudfront mixed case", "hit FROM CloudFront", "HIT"},

		// Akamai formats (3% market share) - verified Dec 2025
		{"akamai hit", "TCP_HIT", "HIT"},
		{"akamai miss", "TCP_MISS", "MISS"},
		{"akamai refresh hit", "TCP_REFRESH_HIT", "HIT"},
		{"akamai refresh miss", "TCP_REFRESH_MISS", "MISS"},
		{"akamai expired hit", "TCP_EXPIRED_HIT", "HIT"},
		{"akamai expired miss", "TCP_EXPIRED_MISS", "MISS"},
		{"akamai mem hit", "TCP_MEM_HIT", "HIT"},
		{"akamai ims hit", "TCP_IMS_HIT", "HIT"},
		{"akamai negative hit", "TCP_NEGATIVE_HIT", "HIT"},
		{"akamai refresh fail hit", "TCP_REFRESH_FAIL_HIT", "HIT"},
		{"akamai denied", "TCP_DENIED", "BYPASS"},
		{"akamai cookie deny", "TCP_COOKIE_DENY", "BYPASS"},
		{"akamai lowercase", "tcp_hit", "HIT"},
		{"akamai from child", "TCP_HIT from child", "HIT"},
		{"akamai from parent", "TCP_MISS from child, TCP_HIT from parent", "HIT"},

		// Azure CDN formats (~2% market share) - same TCP_ prefix as Akamai
		{"azure remote hit", "TCP_REMOTE_HIT", "HIT"},
		{"azure uncacheable", "UNCACHEABLE", "BYPASS"},
		{"azure uncacheable lowercase", "uncacheable", "BYPASS"},

		// Fastly shielding formats (12% market share) - verified Dec 2025
		{"fastly shield hit hit", "HIT, HIT", "HIT"},
		{"fastly shield miss hit", "MISS, HIT", "HIT"},
		{"fastly shield hit miss", "HIT, MISS", "MISS"},
		{"fastly shield miss miss", "MISS, MISS", "MISS"},

		// Cloudflare formats (64% market share) - verified Dec 2025
		{"cloudflare none", "NONE", "BYPASS"},
		{"cloudflare unknown", "UNKNOWN", "BYPASS"},
		{"cloudflare updating", "UPDATING", "UPDATING"},

		// RFC 9211 Cache-Status format (Netlify ~1%, future CDNs)
		{"netlify hit", `"Netlify Edge"; hit`, "HIT"},
		{"netlify hit no space", `"Netlify Edge";hit`, "HIT"},
		{"netlify miss", `"Netlify Edge"; fwd=miss`, "MISS"},
		{"netlify uri miss", `"Netlify Edge"; fwd=uri-miss`, "MISS"},
		{"netlify vary miss", `"Netlify Edge"; fwd=vary-miss`, "MISS"},
		{"netlify stale", `"Netlify Edge"; fwd=stale`, "MISS"},
		{"rfc9211 multi cache hit", `"Origin"; fwd=miss, "CDN"; hit`, "HIT"},
		{"rfc9211 with ttl", `ExampleCache; hit; ttl=30`, "HIT"},

		// Standard formats (Cloudflare 64%, Fastly 12%, Vercel ~1%, KeyCDN <1%)
		{"standard hit", "HIT", "HIT"},
		{"standard miss", "MISS", "MISS"},
		{"standard dynamic", "DYNAMIC", "DYNAMIC"},
		{"standard bypass", "BYPASS", "BYPASS"},
		{"standard expired", "EXPIRED", "EXPIRED"},
		{"standard stale", "STALE", "STALE"},
		{"standard revalidated", "REVALIDATED", "REVALIDATED"},
		{"vercel prerender", "PRERENDER", "PRERENDER"},
		{"fastly pass", "PASS", "PASS"},

		// Preserve unknown formats
		{"unknown format", "SOME_CUSTOM_VALUE", "SOME_CUSTOM_VALUE"},

		// Mixed case standard formats - normalise to uppercase
		{"mixed case hit", "Hit", "HIT"},
		{"lowercase miss", "miss", "MISS"},
		{"mixed case dynamic", "Dynamic", "DYNAMIC"},
		{"lowercase stale", "stale", "STALE"},

		// Whitespace handling
		{"leading spaces", "  HIT", "HIT"},
		{"trailing spaces", "MISS  ", "MISS"},
		{"both spaces", "  DYNAMIC  ", "DYNAMIC"},
		{"spaces only", "   ", ""},

		// TCP_ edge cases without HIT/MISS
		{"tcp client refresh", "TCP_CLIENT_REFRESH", "BYPASS"},
		{"tcp swapfail", "TCP_SWAPFAIL", "BYPASS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normaliseCacheStatus(tt.input)
			if result != tt.expected {
				t.Errorf("normaliseCacheStatus(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestIsPrivateOrLocalIP tests the SSRF protection IP classification
func TestIsPrivateOrLocalIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		expected bool
	}{
		// Loopback addresses
		{"ipv4 loopback", "127.0.0.1", true},
		{"ipv4 loopback alt", "127.0.0.2", true},
		{"ipv6 loopback", "::1", true},

		// Private ranges
		{"10.x.x.x", "10.0.0.1", true},
		{"10.x.x.x end", "10.255.255.255", true},
		{"172.16.x.x", "172.16.0.1", true},
		{"172.31.x.x", "172.31.255.255", true},
		{"192.168.x.x", "192.168.1.1", true},
		{"192.168.x.x end", "192.168.255.255", true},

		// Link-local
		{"ipv4 link-local", "169.254.1.1", true},
		{"ipv6 link-local", "fe80::1", true},

		// Unspecified
		{"ipv4 unspecified", "0.0.0.0", true},
		{"ipv6 unspecified", "::", true},

		// Public IPs (should NOT be blocked)
		{"google dns", "8.8.8.8", false},
		{"cloudflare dns", "1.1.1.1", false},
		{"public ip", "203.0.113.1", false},
		{"public ipv6", "2001:4860:4860::8888", false},

		// Edge cases - just outside private ranges
		{"172.15.x.x", "172.15.255.255", false},
		{"172.32.x.x", "172.32.0.1", false},

		// IPv4-mapped IPv6 addresses (Go's net.IP normalises these)
		{"ipv4-mapped loopback", "::ffff:127.0.0.1", true},
		{"ipv4-mapped private 10.x", "::ffff:10.0.0.1", true},
		{"ipv4-mapped private 192.168.x", "::ffff:192.168.1.1", true},
		{"ipv4-mapped public", "::ffff:8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("Failed to parse IP: %s", tt.ip)
			}
			result := isPrivateOrLocalIP(ip)
			if result != tt.expected {
				t.Errorf("isPrivateOrLocalIP(%s) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}

// TestSSRFProtection tests that ssrfSafeDialContext blocks connections to private/local IPs.
// Note: SSRF protection happens at connection time via the custom DialContext,
// not at URL validation time (which prevents DNS rebinding attacks).
func TestSSRFProtection(t *testing.T) {
	ctx := context.Background()

	// Get the SSRF-safe dialer
	dialFunc := ssrfSafeDialContext()

	// Test blocking localhost - dial should fail for private IPs
	_, err := dialFunc(ctx, "tcp", "127.0.0.1:80")
	if err == nil {
		t.Error("Expected error for 127.0.0.1, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "blocked connection to private/local IP") {
		t.Errorf("Expected SSRF blocking error, got: %v", err)
	}

	// Test blocking private network IPs
	_, err = dialFunc(ctx, "tcp", "10.0.0.1:80")
	if err == nil {
		t.Error("Expected error for 10.0.0.1, got nil")
	}

	_, err = dialFunc(ctx, "tcp", "192.168.1.1:80")
	if err == nil {
		t.Error("Expected error for 192.168.1.1, got nil")
	}

	// Test blocking IPv6 loopback and link-local
	_, err = dialFunc(ctx, "tcp", "[::1]:80")
	if err == nil {
		t.Error("Expected error for ::1, got nil")
	}

	_, err = dialFunc(ctx, "tcp", "[fe80::1]:80")
	if err == nil {
		t.Error("Expected error for fe80::1, got nil")
	}

	// Test that validateCrawlRequest only validates URL format (SSRF moved to DialContext)
	_, err = validateCrawlRequest(ctx, "https://example.com/page", false)
	if err != nil {
		t.Errorf("Expected no error for valid URL, got %v", err)
	}

	// Invalid URLs should still fail validation
	_, err = validateCrawlRequest(ctx, "not-a-valid-url", false)
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
}
