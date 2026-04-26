package lighthouse

import (
	"context"
	"errors"
	"time"
)

// Profile selects between Lighthouse's mobile and desktop presets.
// v1 only schedules mobile audits; desktop is reserved for Phase 5.
type Profile string

const (
	ProfileMobile  Profile = "mobile"
	ProfileDesktop Profile = "desktop"
)

// AuditRequest is the input handed to a Runner. The scheduler builds
// these from a lighthouse_runs row plus the matching tasks/pages
// metadata. Timeout is the per-run budget; runners must respect it.
type AuditRequest struct {
	RunID   int64
	JobID   string
	PageID  int
	URL     string
	Profile Profile
	Timeout time.Duration
}

// AuditResult is the output of a successful audit. ReportKey is the
// R2 object key that the runner uploaded the full Lighthouse JSON to;
// empty for the stub runner since it doesn't write to R2 in Phase 1.
//
// Optional metric fields are pointers so we can distinguish "not
// produced" from "produced as zero" — Lighthouse occasionally omits
// metrics on pages it can't audit cleanly.
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

// Runner executes a single Lighthouse audit. The Phase 1 stub
// implementation returns canned data so the rest of the pipeline can
// be exercised before Chromium lands. Phase 3 adds a localRunner that
// shells out to the bundled lighthouse binary.
type Runner interface {
	Run(ctx context.Context, req AuditRequest) (AuditResult, error)
}

// ErrRunnerNotImplemented is returned by the local runner shim until
// Phase 3 wires Chromium into the analysis-app image. Keeping the
// error here means the scheduler and DB layer can be exercised
// end-to-end with the stub before any Chromium work lands.
var ErrRunnerNotImplemented = errors.New("lighthouse local runner not implemented yet")

// StubRunner is a deterministic Runner that returns canned metrics
// without launching Chromium. Used for local development, CI, and
// integration tests that drive a synthetic job through the full
// schedule → enqueue → record pipeline.
//
// Metric values are fixed so test assertions stay simple. Tests that
// need varied data should construct a custom Runner rather than
// extending this one.
type StubRunner struct{}

// NewStubRunner returns a StubRunner ready for use.
func NewStubRunner() *StubRunner {
	return &StubRunner{}
}

// Run honours ctx cancellation and req.Timeout but otherwise sleeps
// briefly to approximate the cost of an audit, then returns a canned
// result.
func (s *StubRunner) Run(ctx context.Context, req AuditRequest) (AuditResult, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	const simulated = 50 * time.Millisecond
	start := time.Now()

	select {
	case <-time.After(simulated):
	case <-ctx.Done():
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

	return AuditResult{
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
	}, nil
}
