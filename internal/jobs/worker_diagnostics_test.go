package jobs

import (
	"encoding/json"
	"testing"

	"github.com/Harvey-AU/adapt/internal/crawler"
	"github.com/Harvey-AU/adapt/internal/db"
)

func TestPopulateRequestDiagnostics(t *testing.T) {
	wp := &WorkerPool{}
	task := &db.Task{ID: "task-1"}
	result := &crawler.CrawlResult{
		RequestDiagnostics: &crawler.RequestDiagnostics{
			Primary: &crawler.RequestAttemptDiagnostics{
				Request: crawler.RequestMetadata{
					Method:     "GET",
					URL:        "https://example.com/page",
					Provenance: "primary",
				},
				Cache: crawler.CacheMetadata{
					HeaderSource:     "CF-Cache-Status",
					NormalisedStatus: "HIT",
				},
			},
		},
	}

	wp.populateRequestDiagnostics(task, result)

	if len(task.RequestDiagnostics) == 0 {
		t.Fatal("expected request diagnostics to be marshalled")
	}

	var stored crawler.RequestDiagnostics
	if err := json.Unmarshal(task.RequestDiagnostics, &stored); err != nil {
		t.Fatalf("expected valid JSON, got %v", err)
	}

	if stored.Primary == nil {
		t.Fatal("expected primary diagnostics to be present")
	}
	if stored.Primary.Request.Method != "GET" {
		t.Fatalf("expected request method GET, got %s", stored.Primary.Request.Method)
	}
	if stored.Primary.Cache.NormalisedStatus != "HIT" {
		t.Fatalf("expected cache status HIT, got %s", stored.Primary.Cache.NormalisedStatus)
	}
}

func TestPopulateRequestDiagnosticsWithNilResult(t *testing.T) {
	wp := &WorkerPool{}
	task := &db.Task{ID: "task-2"}

	wp.populateRequestDiagnostics(task, nil)

	if string(task.RequestDiagnostics) != "{}" {
		t.Fatalf("expected empty JSON object, got %s", string(task.RequestDiagnostics))
	}
}
