package lighthouse

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// v1 only schedules mobile; desktop reserved for Phase 5.
type Profile string

const (
	ProfileMobile  Profile = "mobile"
	ProfileDesktop Profile = "desktop"
)

// SourceTaskID empty when FK was NULLed via ON DELETE SET NULL; runner
// then falls back to a run-id-keyed path so the report isn't lost.
type AuditRequest struct {
	RunID        int64
	JobID        string
	PageID       int
	SourceTaskID string
	URL          string
	Profile      Profile
	Timeout      time.Duration
}

// Metric fields are pointers to distinguish "not produced" from
// "produced as zero" — Lighthouse occasionally omits metrics on pages
// it can't audit cleanly.
type AuditResult struct {
	PerformanceScore *int
	LCPMs            *int
	CLS              *float64
	INPMs            *int
	TBTMs            *int
	FCPMs            *int
	SpeedIndexMs     *int
	TTFBMs           *int
	TotalByteWeight  *int64
	ReportKey        string
	Duration         time.Duration
}

type Runner interface {
	Run(ctx context.Context, req AuditRequest) (AuditResult, error)
}

var ErrRunnerNotImplemented = errors.New("lighthouse local runner not implemented yet")

// StubRunner returns canned metrics without launching Chromium so the
// schedule → enqueue → record pipeline can be exercised end-to-end.
type StubRunner struct{}

func NewStubRunner() *StubRunner {
	return &StubRunner{}
}

func (s *StubRunner) Run(ctx context.Context, req AuditRequest) (AuditResult, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	const simulated = 50 * time.Millisecond
	start := time.Now()

	lighthouseLog.Debug("stub runner audit started",
		"run_id", req.RunID,
		"job_id", req.JobID,
		"page_id", req.PageID,
		"profile", string(req.Profile),
		"url", SanitiseAuditURL(req.URL),
	)

	select {
	case <-time.After(simulated):
	case <-ctx.Done():
		lighthouseLog.Debug("stub runner audit cancelled",
			"run_id", req.RunID, "job_id", req.JobID,
			"reason", ctx.Err(),
		)
		return AuditResult{}, ctx.Err()
	}

	score := 87
	lcp := 2400
	inp := 180
	tbt := 240
	fcp := 1300
	si := 2900
	ttfb := 320
	cls := 0.080
	bytes := int64(1_500_000)

	result := AuditResult{
		PerformanceScore: &score,
		LCPMs:            &lcp,
		CLS:              &cls,
		INPMs:            &inp,
		TBTMs:            &tbt,
		FCPMs:            &fcp,
		SpeedIndexMs:     &si,
		TTFBMs:           &ttfb,
		TotalByteWeight:  &bytes,
		ReportKey:        "",
		Duration:         time.Since(start),
	}

	lighthouseLog.Debug("stub runner audit completed",
		"run_id", req.RunID,
		"job_id", req.JobID,
		"page_id", req.PageID,
		"duration_ms", result.Duration.Milliseconds(),
	)

	return result, nil
}

// SanitiseAuditURL strips query and fragment before logging — audit
// URLs come from customer crawls and can carry session tokens or other
// low-entropy PII.
func SanitiseAuditURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Drop rather than risk leaking the unparsed string.
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
