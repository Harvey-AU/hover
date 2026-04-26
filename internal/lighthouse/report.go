package lighthouse

import (
	"encoding/json"
	"fmt"
	"math"
)

// rawReport mirrors the slice of the Lighthouse JSON output we care
// about. The CLI's full report is large and frequently versioned;
// pulling out only the fields we map to AuditResult keeps the parser
// resilient to upstream churn. Any audit we don't recognise is simply
// missing from this struct and falls back to a nil pointer in the
// AuditResult.
//
// Lighthouse audits emit numericValue as a float64 in milliseconds for
// timing metrics, a unitless float for CLS, and bytes for byte-weight.
// performance.score is 0.0–1.0; we round to a 0–100 integer so the
// stored column matches the existing `INT` schema on lighthouse_runs.
type rawReport struct {
	Categories struct {
		Performance struct {
			Score *float64 `json:"score"`
		} `json:"performance"`
	} `json:"categories"`
	Audits map[string]rawAudit `json:"audits"`
}

type rawAudit struct {
	NumericValue *float64 `json:"numericValue"`
	NumericUnit  string   `json:"numericUnit"`
}

// audit IDs mirror the stable IDs Lighthouse exposes in its `audits`
// map. Pinned here so a typo can't silently drop a metric.
const (
	auditLargestContentfulPaint = "largest-contentful-paint"
	auditCumulativeLayoutShift  = "cumulative-layout-shift"
	auditInteractionToNextPaint = "interaction-to-next-paint"
	auditTotalBlockingTime      = "total-blocking-time"
	auditFirstContentfulPaint   = "first-contentful-paint"
	auditSpeedIndex             = "speed-index"
	auditServerResponseTime     = "server-response-time"
	auditTotalByteWeight        = "total-byte-weight"
)

// ParseReport turns a Lighthouse JSON report into an AuditResult. The
// Duration field is left zero — the runner stamps wall time from the
// outside since the report itself doesn't capture exec wrapper cost.
//
// ReportKey is left empty here for the same reason: it's the R2 key,
// which only the runner that just uploaded the report can know.
//
// Returns an error only on malformed JSON; a report that is well-formed
// but missing a given audit produces a nil pointer in that field, which
// preserves the "not measured" semantics of the lighthouse_runs columns.
func ParseReport(raw []byte) (AuditResult, error) {
	var report rawReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return AuditResult{}, fmt.Errorf("parse lighthouse report: %w", err)
	}

	var out AuditResult

	if report.Categories.Performance.Score != nil {
		score := int(math.Round(*report.Categories.Performance.Score * 100))
		out.PerformanceScore = &score
	}

	if a, ok := report.Audits[auditLargestContentfulPaint]; ok && a.NumericValue != nil {
		v := int(math.Round(*a.NumericValue))
		out.LCPMs = &v
	}
	if a, ok := report.Audits[auditCumulativeLayoutShift]; ok && a.NumericValue != nil {
		v := *a.NumericValue
		out.CLS = &v
	}
	if a, ok := report.Audits[auditInteractionToNextPaint]; ok && a.NumericValue != nil {
		v := int(math.Round(*a.NumericValue))
		out.INPMs = &v
	}
	if a, ok := report.Audits[auditTotalBlockingTime]; ok && a.NumericValue != nil {
		v := int(math.Round(*a.NumericValue))
		out.TBTMs = &v
	}
	if a, ok := report.Audits[auditFirstContentfulPaint]; ok && a.NumericValue != nil {
		v := int(math.Round(*a.NumericValue))
		out.FCPMs = &v
	}
	if a, ok := report.Audits[auditSpeedIndex]; ok && a.NumericValue != nil {
		v := int(math.Round(*a.NumericValue))
		out.SpeedIndexMs = &v
	}
	if a, ok := report.Audits[auditServerResponseTime]; ok && a.NumericValue != nil {
		v := int(math.Round(*a.NumericValue))
		out.TTFBMs = &v
	}
	if a, ok := report.Audits[auditTotalByteWeight]; ok && a.NumericValue != nil {
		v := int64(math.Round(*a.NumericValue))
		out.TotalByteWeight = &v
	}

	return out, nil
}
