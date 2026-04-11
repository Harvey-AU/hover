-- Snapshot of how many URLs were found in the sitemap at discovery time,
-- after robots.txt and path filtering. Distinct from sitemap_tasks, which
-- is a live counter incremented as tasks are inserted.
ALTER TABLE jobs
  ADD COLUMN IF NOT EXISTS sitemap_urls_found INTEGER;
