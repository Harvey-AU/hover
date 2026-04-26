package lighthouse

import (
	"context"
	"testing"
	"time"
)

func TestStubRunnerReturnsCannedMetrics(t *testing.T) {
	runner := NewStubRunner()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := runner.Run(ctx, AuditRequest{
		RunID:   42,
		JobID:   "job-1",
		PageID:  7,
		URL:     "https://example.com/",
		Profile: ProfileMobile,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("stub run failed: %v", err)
	}
	if got.PerformanceScore == nil || *got.PerformanceScore == 0 {
		t.Errorf("expected non-zero performance score, got %+v", got.PerformanceScore)
	}
	if got.LCPMs == nil || *got.LCPMs == 0 {
		t.Errorf("expected non-zero LCP, got %+v", got.LCPMs)
	}
	if got.Duration <= 0 {
		t.Errorf("expected positive duration, got %v", got.Duration)
	}
	if got.ReportKey != "" {
		t.Errorf("expected empty ReportKey from stub, got %q", got.ReportKey)
	}
}

func TestStubRunnerHonoursContextCancel(t *testing.T) {
	runner := NewStubRunner()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := runner.Run(ctx, AuditRequest{URL: "https://example.com/"})
	if err == nil {
		t.Fatal("expected context error from cancelled ctx, got nil")
	}
}
