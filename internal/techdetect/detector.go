// Package techdetect provides technology detection for websites using wappalyzergo.
// It identifies CMS platforms, CDNs, frameworks, and other technologies used by domains.
package techdetect

import (
	"encoding/json"
	"maps"
	"net/http"
	"sync"

	"github.com/Harvey-AU/hover/internal/logging"
	wappalyzer "github.com/projectdiscovery/wappalyzergo"
)

var techdetectLog = logging.Component("techdetect")

// MaxHTMLSampleSize is the maximum size of HTML to store for debugging (50KB)
const MaxHTMLSampleSize = 50 * 1024

// Result contains the detected technologies and raw data for a domain
type Result struct {
	// Technologies maps technology name to its categories (e.g., {"WordPress": ["CMS"], "Cloudflare": ["CDN"]})
	Technologies map[string][]string `json:"technologies"`
	// RawHeaders contains the HTTP headers from the detection request
	RawHeaders map[string][]string `json:"raw_headers"`
	// HTMLSample contains a truncated sample of the HTML body (max 50KB)
	HTMLSample string `json:"html_sample"`
}

// Detector provides technology detection capabilities
type Detector struct {
	client *wappalyzer.Wappalyze
	mu     sync.RWMutex
}

// categoryNames maps wappalyzer category IDs to human-readable names
var categoryNames map[int]string
var categoryNamesOnce sync.Once

// New creates a new technology detector
func New() (*Detector, error) {
	client, err := wappalyzer.New()
	if err != nil {
		return nil, err
	}

	// Initialise category names mapping once
	categoryNamesOnce.Do(func() {
		categoryNames = make(map[int]string)
		cats := wappalyzer.GetCategoriesMapping()
		for id, cat := range cats {
			categoryNames[id] = cat.Name
		}
	})

	return &Detector{
		client: client,
	}, nil
}

// Detect identifies technologies from HTTP headers and body
func (d *Detector) Detect(headers http.Header, body []byte) *Result {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := &Result{
		Technologies: make(map[string][]string),
		RawHeaders:   make(map[string][]string),
	}

	// Store raw headers for debugging
	maps.Copy(result.RawHeaders, headers)

	// Store truncated HTML sample
	if len(body) > MaxHTMLSampleSize {
		result.HTMLSample = string(body[:MaxHTMLSampleSize])
	} else {
		result.HTMLSample = string(body)
	}

	// Detect technologies using wappalyzergo
	fingerprints := d.client.FingerprintWithCats(headers, body)

	for tech, catInfo := range fingerprints {
		categories := make([]string, 0, len(catInfo.Cats))
		for _, catID := range catInfo.Cats {
			if name, ok := categoryNames[catID]; ok {
				categories = append(categories, name)
			}
		}
		result.Technologies[tech] = categories
	}

	techdetectLog.Debug("Technology detection completed",
		"tech_count", len(result.Technologies),
		"technologies", result.Technologies,
	)

	return result
}

// DetectFromResponse is a convenience method that extracts headers and body from an HTTP response
func (d *Detector) DetectFromResponse(resp *http.Response, body []byte) *Result {
	return d.Detect(resp.Header, body)
}

// TechnologiesJSON returns the technologies as a JSON string for database storage
func (r *Result) TechnologiesJSON() ([]byte, error) {
	return json.Marshal(r.Technologies)
}

// HeadersJSON returns the raw headers as a JSON string for database storage
func (r *Result) HeadersJSON() ([]byte, error) {
	return json.Marshal(r.RawHeaders)
}
