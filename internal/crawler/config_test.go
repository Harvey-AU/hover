package crawler

import "testing"

func TestDefaultConfigUsesCrawlerMaxConcurrencyEnv(t *testing.T) {
	t.Setenv("GNH_CRAWLER_MAX_CONCURRENCY", "100")

	cfg := DefaultConfig()

	if cfg.MaxConcurrency != 100 {
		t.Fatalf("expected MaxConcurrency 100, got %d", cfg.MaxConcurrency)
	}
}

func TestDefaultConfigFallsBackOnInvalidCrawlerMaxConcurrencyEnv(t *testing.T) {
	t.Setenv("GNH_CRAWLER_MAX_CONCURRENCY", "0")

	cfg := DefaultConfig()

	if cfg.MaxConcurrency != 10 {
		t.Fatalf("expected MaxConcurrency fallback 10, got %d", cfg.MaxConcurrency)
	}
}
