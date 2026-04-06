package crawler

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// aiaTransport wraps an http.RoundTripper and, on TLS certificate
// verification failure, attempts to fetch missing intermediate
// certificates via the Authority Information Access (AIA) extension.
//
// Many web servers are misconfigured and don't send the full
// certificate chain. Browsers handle this transparently by fetching
// missing intermediates from the AIA URLs embedded in the leaf cert.
// Go's net/http does not. This transport adds that behaviour so the
// crawler can handle the real-world web.
type aiaTransport struct {
	base *http.Transport

	mu    sync.RWMutex
	pool  *x509.CertPool  // custom pool = system roots + fetched intermediates
	cache map[string]bool // tracks AIA URLs we've already fetched
}

func newAIATransport(base *http.Transport) *aiaTransport {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	return &aiaTransport{
		base:  base,
		pool:  pool,
		cache: make(map[string]bool),
	}
}

func (t *aiaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Ensure the base transport uses our augmented cert pool.
	t.mu.RLock()
	if t.base.TLSClientConfig == nil {
		t.base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	t.base.TLSClientConfig.RootCAs = t.pool
	t.mu.RUnlock()

	resp, err := t.base.RoundTrip(req)
	if err == nil || !isUnknownAuthorityErr(err) {
		return resp, err
	}

	// TLS verification failed — attempt AIA fetch and retry once.
	if t.fetchIntermediates(req.URL.Host) {
		return t.base.RoundTrip(req)
	}

	return resp, err
}

// fetchIntermediates connects to the host with verification disabled,
// reads the leaf cert's AIA URLs, fetches the intermediates, and adds
// them to our custom cert pool. Returns true if any were installed.
func (t *aiaTransport) fetchIntermediates(host string) bool {
	if !strings.Contains(host, ":") {
		host += ":443"
	}

	// Connect with InsecureSkipVerify just to read the leaf certificate.
	inspectTransport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // intentional: reading cert only
	}
	inspectClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: inspectTransport,
	}

	if isPrivateHost(host) {
		log.Debug().Str("host", host).Msg("AIA: rejecting private/internal host")
		return false
	}

	resp, err := inspectClient.Head("https://" + host) //nolint:gosec // G704: host validated against private IPs above
	if err != nil {
		log.Debug().Err(err).Str("host", host).Msg("AIA: failed to connect for cert inspection")
		return false
	}
	defer resp.Body.Close()

	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return false
	}

	leaf := resp.TLS.PeerCertificates[0]
	if len(leaf.IssuingCertificateURL) == 0 {
		log.Debug().Str("host", host).Msg("AIA: leaf cert has no IssuingCertificateURL")
		return false
	}

	installed := false
	for _, aiaURL := range leaf.IssuingCertificateURL {
		t.mu.RLock()
		seen := t.cache[aiaURL]
		t.mu.RUnlock()
		if seen {
			installed = true // already fetched and added
			continue
		}

		cert := fetchCertFromURL(aiaURL)
		if cert == nil {
			continue
		}

		t.mu.Lock()
		t.pool.AddCert(cert)
		t.cache[aiaURL] = true
		t.mu.Unlock()

		log.Info().
			Str("url", aiaURL).
			Str("subject", cert.Subject.CommonName).
			Str("issuer", cert.Issuer.CommonName).
			Msg("AIA: installed missing intermediate certificate")
		installed = true
	}

	return installed
}

// fetchCertFromURL downloads a DER-encoded certificate from a URL.
func fetchCertFromURL(rawURL string) *x509.Certificate {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		log.Debug().Str("url", rawURL).Msg("AIA: rejecting non-HTTP(S) URL")
		return nil
	}
	if isPrivateHost(parsed.Host) {
		log.Debug().Str("url", rawURL).Msg("AIA: rejecting AIA URL targeting private host")
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL) //nolint:gosec // G704: URL validated against private IPs and scheme above
	if err != nil {
		log.Debug().Err(err).Str("url", rawURL).Msg("AIA: failed to fetch intermediate")
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}

	// AIA endpoints typically serve DER-encoded certificates.
	cert, err := x509.ParseCertificate(body)
	if err != nil {
		// Fallback: try parsing as multiple certs (PEM bundle)
		certs, pemErr := x509.ParseCertificates(body)
		if pemErr != nil || len(certs) == 0 {
			log.Debug().Err(err).Str("url", rawURL).Msg("AIA: failed to parse certificate")
			return nil
		}
		cert = certs[0]
	}

	return cert
}

// isPrivateHost resolves the host and returns true if any of its IPs are
// private, loopback, link-local, or unspecified. This guards against SSRF
// where an attacker-controlled hostname resolves to an internal address.
func isPrivateHost(host string) bool {
	// Strip port if present.
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}

	ips, err := net.LookupHost(hostOnly) //nolint:gosec // G704: this IS the SSRF validation — we check the resolved IPs below
	if err != nil {
		return true // fail-closed: treat unresolvable hosts as private
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// isUnknownAuthorityErr detects TLS errors caused by missing intermediates.
func isUnknownAuthorityErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "certificate signed by unknown authority") ||
		strings.Contains(s, "failed to verify certificate")
}
