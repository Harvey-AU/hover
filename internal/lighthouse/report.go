package lighthouse

import (
	"encoding/json"
	"fmt"
	"math"
)

// Pulling only mapped fields keeps the parser resilient to upstream
// churn. performance.score is 0.0–1.0; rounded to 0–100 to match the
// INT column on lighthouse_runs.
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

// Pinned here so a typo can't silently drop a metric.
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

// Missing audits produce nil pointers, preserving the "not measured"
// semantics of the lighthouse_runs columns. Duration and ReportKey are
// left zero — the runner stamps them from the outside.
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
