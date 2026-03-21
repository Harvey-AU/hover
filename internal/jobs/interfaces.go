package jobs

import (
	"context"
	"database/sql"

	"github.com/Harvey-AU/adapt/internal/crawler"
	"github.com/Harvey-AU/adapt/internal/db"
)

// CrawlerInterface defines the methods we need from the crawler
type CrawlerInterface interface {
	WarmURL(ctx context.Context, url string, findLinks bool) (*crawler.CrawlResult, error)
	DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*crawler.SitemapDiscoveryResult, error)
	ParseSitemap(ctx context.Context, sitemapURL string) ([]string, error)
	FilterURLs(urls []string, includePaths, excludePaths []string) []string
	GetUserAgent() string
}

// DbQueueInterface defines the database queue operations needed by WorkerPool
type DbQueueInterface interface {
	GetNextTask(ctx context.Context, jobID string) (*db.Task, error)
	UpdateTaskStatus(ctx context.Context, task *db.Task) error
	DecrementRunningTasks(ctx context.Context, jobID string) error
	DecrementRunningTasksBy(ctx context.Context, jobID string, count int) error
	Execute(ctx context.Context, fn func(*sql.Tx) error) error
	ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error
	ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error
	SetConcurrencyOverride(fn db.ConcurrencyOverrideFunc)
	UpdateDomainTechnologies(ctx context.Context, domainID int, technologies, headers []byte, htmlPath string) error
	UpdateTaskHTMLMetadata(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error
}
