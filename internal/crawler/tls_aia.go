package crawler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// On TLS verification failure, fetch missing intermediates via the AIA extension
// (browsers do this transparently; Go's net/http does not).
type aiaTransport struct {
	base *http.Transport

	mu            sync.RWMutex
	intermediates []*x509.Certificate // never added to a root trust store
	cache         map[string]bool
}

func newAIATransport(base *http.Transport) *aiaTransport {
	return &aiaTransport{
		base:  base,
		cache: make(map[string]bool),
	}
}

func (t *aiaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil || !isUnknownAuthorityErr(err) {
		return resp, err
	}

	if !t.fetchIntermediates(req.URL.Host) {
		return resp, err
	}

	// VerifyConnection re-runs verification with system roots + fetched
	// intermediates, so intermediates never enter any root trust store.
	hostname := req.URL.Hostname()
	t.mu.RLock()
	fetched := append([]*x509.Certificate(nil), t.intermediates...)
	t.mu.RUnlock()

	retryTransport := t.base.Clone()
	retryTransport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // chain + hostname verified in VerifyConnection below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no peer certificates in TLS handshake")
			}
			leaf := cs.PeerCertificates[0]

			systemRoots, sysErr := x509.SystemCertPool()
			if sysErr != nil {
				systemRoots = x509.NewCertPool()
			}

			intermediates := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				intermediates.AddCert(c)
			}
			for _, c := range fetched {
				intermediates.AddCert(c)
			}

			_, verifyErr := leaf.Verify(x509.VerifyOptions{
				Roots:         systemRoots,
				Intermediates: intermediates,
				DNSName:       hostname,
			})
			return verifyErr
		},
	}
	return retryTransport.RoundTrip(req)
}

func (t *aiaTransport) fetchIntermediates(host string) bool {
	if !strings.Contains(host, ":") {
		host += ":443"
	}

	if isPrivateHost(host) {
		crawlerLog.Debug("AIA: rejecting private/internal host", "host", host)
		return false
	}

	// ssrfSafeDialContext re-checks IPs at connect time to defeat DNS rebinding
	// between the isPrivateHost check above and the actual TCP dial.
	inspectTransport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // intentional: reading cert only
		DialContext:         ssrfSafeDialContext(),
	}
	inspectClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: inspectTransport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := inspectClient.Head("https://" + host) //nolint:gosec // G107: host validated by isPrivateHost and ssrfSafeDialContext
	if err != nil {
		crawlerLog.Debug("AIA: failed to connect for cert inspection", "error", err, "host", host)
		return false
	}
	defer resp.Body.Close()

	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return false
	}

	leaf := resp.TLS.PeerCertificates[0]
	if len(leaf.IssuingCertificateURL) == 0 {
		crawlerLog.Debug("AIA: leaf cert has no IssuingCertificateURL", "host", host)
		return false
	}

	installed := false
	for _, aiaURL := range leaf.IssuingCertificateURL {
		t.mu.RLock()
		seen := t.cache[aiaURL]
		t.mu.RUnlock()
		if seen {
			installed = true
			continue
		}

		cert := fetchCertFromURL(aiaURL)
		if cert == nil {
			continue
		}

		// Reject non-CA and self-signed certs — never let a fetched cert become a trust anchor.
		if !cert.IsCA || !cert.BasicConstraintsValid {
			crawlerLog.Debug("AIA: skipping non-CA certificate", "subject", cert.Subject.CommonName)
			continue
		}
		if cert.Subject.String() == cert.Issuer.String() {
			crawlerLog.Debug("AIA: skipping self-signed certificate", "subject", cert.Subject.CommonName)
			continue
		}

		t.mu.Lock()
		t.intermediates = append(t.intermediates, cert)
		t.cache[aiaURL] = true
		t.mu.Unlock()

		crawlerLog.Info("AIA: fetched missing intermediate certificate",
			"subject", cert.Subject.CommonName,
			"issuer", cert.Issuer.CommonName,
		)
		installed = true
	}

	return installed
}

func fetchCertFromURL(rawURL string) *x509.Certificate {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		crawlerLog.Debug("AIA: rejecting malformed AIA URL", "error", err)
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		crawlerLog.Debug("AIA: rejecting non-HTTP(S) URL", "scheme", parsed.Scheme, "host", parsed.Host)
		return nil
	}
	if isPrivateHost(parsed.Host) {
		crawlerLog.Debug("AIA: rejecting AIA URL targeting private host", "host", parsed.Host)
		return nil
	}

	// Connect-time IP check defeats DNS rebinding between isPrivateHost and dial.
	aiaTransport := &http.Transport{
		DialContext: ssrfSafeDialContext(),
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: aiaTransport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			u := req.URL
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("AIA redirect to non-HTTP(S) scheme %q rejected", u.Scheme)
			}
			if isPrivateHost(u.Host) {
				return fmt.Errorf("AIA redirect to private host %q rejected", u.Host)
			}
			return nil
		},
	}
	resp, err := client.Get(rawURL) //nolint:gosec // G107: URL validated by ssrfSafeDialContext at connect time
	if err != nil {
		crawlerLog.Debug("AIA: failed to fetch intermediate", "error", err, "host", parsed.Host)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}

	// AIA endpoints typically serve DER; some serve a PEM bundle — try both.
	cert, err := x509.ParseCertificate(body)
	if err != nil {
		certs, pemErr := x509.ParseCertificates(body)
		if pemErr != nil || len(certs) == 0 {
			crawlerLog.Debug("AIA: failed to parse certificate", "error", err, "host", parsed.Host)
			return nil
		}
		cert = certs[0]
	}

	return cert
}

// SSRF guard: returns true for any IP that's private/loopback/link-local/unspecified.
// Fails closed on resolution error so unresolvable/slow hosts can't bypass.
func isPrivateHost(host string) bool {
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, hostOnly)
	if err != nil {
		return true
	}

	for _, addr := range addrs {
		if isPrivateOrLocalIP(addr.IP) {
			return true
		}
	}
	return false
}

func isUnknownAuthorityErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "certificate signed by unknown authority") ||
		strings.Contains(s, "failed to verify certificate")
}
