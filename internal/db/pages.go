package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
)

const maxPageRecordBatchSize = 250

// Page represents a page to be enqueued with its priority
type Page struct {
	ID       int
	Host     string
	Path     string
	Priority float64
}

// TransactionExecutor interface for types that can execute transactions
type TransactionExecutor interface {
	Execute(ctx context.Context, fn func(*sql.Tx) error) error
}

// CreatePageRecords finds existing pages or creates new ones for the given URLs.
// It returns parallel slices of page IDs, hosts, and paths for each accepted URL.
func CreatePageRecords(ctx context.Context, q TransactionExecutor, domainID int, domain string, urls []string) ([]int, []string, []string, error) {
	if len(urls) == 0 {
		return nil, nil, nil, nil
	}

	var pageIDs []int
	var hosts []string
	var paths []string
	seen := make(map[string]int, len(urls))
	batched := make([]Page, 0, maxPageRecordBatchSize)

	buildPageKey := func(host, path string) string {
		return host + "|" + path
	}

	flushBatch := func() error {
		if len(batched) == 0 {
			return nil
		}

		if err := ensurePageBatch(ctx, q, domainID, batched, seen); err != nil {
			return err
		}

		for _, page := range batched {
			key := buildPageKey(page.Host, page.Path)
			id, ok := seen[key]
			if !ok {
				continue
			}
			pageIDs = append(pageIDs, id)
			hosts = append(hosts, page.Host)
			paths = append(paths, page.Path)
		}

		batched = batched[:0]
		return nil
	}

	for _, u := range urls {
		host, path, err := normaliseURLPath(u, domain)
		if err != nil {
			log.Warn().Err(err).Str("url", u).Msg("Skipping invalid URL")
			continue
		}

		key := buildPageKey(host, path)
		if id, ok := seen[key]; ok {
			pageIDs = append(pageIDs, id)
			hosts = append(hosts, host)
			paths = append(paths, path)
			continue
		}

		batched = append(batched, Page{Host: host, Path: path})
		if len(batched) >= maxPageRecordBatchSize {
			if err := flushBatch(); err != nil {
				return nil, nil, nil, err
			}
		}
	}

	if err := flushBatch(); err != nil {
		return nil, nil, nil, err
	}

	return pageIDs, hosts, paths, nil
}

func ensurePageBatch(ctx context.Context, q TransactionExecutor, domainID int, batch []Page, seen map[string]int) error {
	unique := make([]Page, 0, len(batch))
	uniqueSet := make(map[string]struct{}, len(batch))
	for _, page := range batch {
		key := page.Host + "|" + page.Path
		if _, ok := seen[key]; ok {
			continue
		}
		if _, ok := uniqueSet[key]; ok {
			continue
		}
		uniqueSet[key] = struct{}{}
		unique = append(unique, page)
	}

	if len(unique) == 0 {
		return nil
	}

	// Sort by conflict key so concurrent transactions acquire locks in the same
	// order, preventing deadlocks on ON CONFLICT DO UPDATE.
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Host != unique[j].Host {
			return unique[i].Host < unique[j].Host
		}
		return unique[i].Path < unique[j].Path
	})

	upsertBatchQuery := `
		WITH batch(host, path) AS (
			SELECT UNNEST($2::text[]), UNNEST($3::text[])
		)
		INSERT INTO pages (domain_id, host, path)
		SELECT $1, host, path
		FROM batch
		ON CONFLICT (domain_id, host, path)
		-- No-op update ensures RETURNING emits both inserted and existing rows.
		DO UPDATE SET path = EXCLUDED.path
		RETURNING host, path, id
	`

	return q.Execute(ctx, func(tx *sql.Tx) error {
		hosts := make([]string, len(unique))
		paths := make([]string, len(unique))
		for i, page := range unique {
			hosts[i] = page.Host
			paths[i] = page.Path
		}

		rows, err := tx.QueryContext(ctx, upsertBatchQuery, domainID, pq.Array(hosts), pq.Array(paths))
		if err != nil {
			return fmt.Errorf("failed to upsert page batch: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var host string
			var path string
			var pageID int
			if err := rows.Scan(&host, &path, &pageID); err != nil {
				return fmt.Errorf("failed to scan upserted page batch row: %w", err)
			}
			seen[host+"|"+path] = pageID
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("failed during page batch upsert iteration: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO domain_hosts (domain_id, host, is_primary, last_seen_at)
			SELECT $1, canonical_host, FALSE, NOW()
			FROM (
				SELECT DISTINCT LOWER(REGEXP_REPLACE(host, '^www\\.', '')) AS canonical_host
				FROM UNNEST($2::text[]) AS host
				WHERE host <> ''
				ORDER BY 1
			) unique_hosts
			ON CONFLICT (domain_id, host) DO UPDATE
			SET last_seen_at = NOW()
		`, domainID, pq.Array(hosts)); err != nil {
			return fmt.Errorf("failed to upsert domain hosts: %w", err)
		}

		return nil
	})
}

func normaliseURLPath(u string, domain string) (string, string, error) {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return "", "", err
	}
	if !parsedURL.IsAbs() {
		base, _ := url.Parse("https://" + domain)
		parsedURL = base.ResolveReference(parsedURL)
	}
	host := strings.ToLower(parsedURL.Hostname())
	if host == "" {
		host = strings.ToLower(strings.TrimSpace(domain))
		if host == "" {
			return "", "", fmt.Errorf("empty host in URL normalization for %q", u)
		}
	}
	path := parsedURL.Path
	if path == "" {
		path = "/"
	}
	if path != "/" && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return host, path, nil
}
