package jobs

import (
	"encoding/json"
	"testing"

	"github.com/Harvey-AU/adapt/internal/crawler"
	"github.com/Harvey-AU/adapt/internal/db"
)

func TestPopulateRequestDiagnostics(t *testing.T) {
	requestMeta := &crawler.RequestMetadata{
		Method:     "GET",
		URL:        "https://example.com/page",
		Provenance: "primary",
	}
	cacheMeta := &crawler.CacheMetadata{
		HeaderSource:     "CF-Cache-Status",
		NormalisedStatus: "HIT",
	}

	tests := []struct {
		name          string
		result        *crawler.CrawlResult
		expectJSON    string
		expectPrimary bool
	}{
		{
			name: "populated diagnostics",
			result: &crawler.CrawlResult{
				RequestDiagnostics: &crawler.RequestDiagnostics{
					Primary: &crawler.RequestAttemptDiagnostics{
						Request: requestMeta,
						Cache:   cacheMeta,
					},
				},
			},
			expectPrimary: true,
		},
		{name: "nil result", result: nil, expectJSON: "{}"},
		{name: "nil request diagnostics", result: &crawler.CrawlResult{}, expectJSON: "{}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wp := &WorkerPool{}
			task := &db.Task{ID: "task-1"}

			wp.populateRequestDiagnostics(task, tt.result)

			if tt.expectJSON != "" {
				if string(task.RequestDiagnostics) != tt.expectJSON {
					t.Fatalf("expected empty JSON object, got %s", string(task.RequestDiagnostics))
				}
				return
			}

			if len(task.RequestDiagnostics) == 0 {
				t.Fatal("expected request diagnostics to be marshalled")
			}

			var stored crawler.RequestDiagnostics
			if err := json.Unmarshal(task.RequestDiagnostics, &stored); err != nil {
				t.Fatalf("expected valid JSON, got %v", err)
			}

			if tt.expectPrimary {
				if stored.Primary == nil {
					t.Fatal("expected primary diagnostics to be present")
				}
				if stored.Primary.Request == nil {
					t.Fatal("expected request metadata to be present")
				}
				if stored.Primary.Cache == nil {
					t.Fatal("expected cache metadata to be present")
				}
				if stored.Primary.Request.Method != "GET" {
					t.Fatalf("expected request method GET, got %s", stored.Primary.Request.Method)
				}
				if stored.Primary.Cache.NormalisedStatus != "HIT" {
					t.Fatalf("expected cache status HIT, got %s", stored.Primary.Cache.NormalisedStatus)
				}
			}
		})
	}
}
