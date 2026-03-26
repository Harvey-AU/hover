-- Comprehensive Initial Schema for Hover
-- This migration creates the complete schema based on the setupSchema() function in internal/db/db.go
-- and incorporates all enhancements from subsequent migrations

-- Enable necessary extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- =============================================================================
-- TABLE CREATION
-- =============================================================================

-- Create organisations table first (referenced by users and jobs)
CREATE TABLE IF NOT EXISTS organisations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Create users table (extends Supabase auth.users)
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    full_name TEXT,
    organisation_id UUID REFERENCES organisations(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(email)
);

-- Create domains lookup table
CREATE TABLE IF NOT EXISTS domains (
    id SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    crawl_delay_seconds INTEGER DEFAULT NULL,
    adaptive_delay_seconds INTEGER NOT NULL DEFAULT 0,
    adaptive_delay_floor_seconds INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Create pages lookup table
CREATE TABLE IF NOT EXISTS pages (
    id SERIAL PRIMARY KEY,
    domain_id INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(domain_id, path)
);

-- Create jobs table with generated columns for duration calculations
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    domain_id INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    organisation_id UUID REFERENCES organisations(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    progress REAL NOT NULL,
    sitemap_tasks INTEGER NOT NULL DEFAULT 0,
    found_tasks INTEGER NOT NULL DEFAULT 0,
    total_tasks INTEGER NOT NULL DEFAULT 0,
    completed_tasks INTEGER NOT NULL DEFAULT 0,
    failed_tasks INTEGER NOT NULL DEFAULT 0,
    skipped_tasks INTEGER NOT NULL DEFAULT 0,
    running_tasks INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    concurrency INTEGER NOT NULL,
    find_links BOOLEAN NOT NULL,
    max_pages INTEGER NOT NULL,
    include_paths TEXT,
    exclude_paths TEXT,
    required_workers INTEGER DEFAULT 0,
    error_message TEXT,
    source_type TEXT,
    source_detail TEXT,
    source_info TEXT,
    -- Generated columns for duration calculations
    duration_seconds INTEGER GENERATED ALWAYS AS (
        CASE 
            WHEN started_at IS NOT NULL AND completed_at IS NOT NULL 
            THEN EXTRACT(EPOCH FROM (completed_at - started_at))::INTEGER
            ELSE NULL
        END
    ) STORED,
    avg_time_per_task_seconds NUMERIC GENERATED ALWAYS AS (
        CASE 
            WHEN started_at IS NOT NULL AND completed_at IS NOT NULL AND completed_tasks > 0 
            THEN EXTRACT(EPOCH FROM (completed_at - started_at))::NUMERIC / completed_tasks::NUMERIC
            ELSE NULL
        END
    ) STORED,
    CONSTRAINT jobs_running_tasks_non_negative CHECK (running_tasks >= 0)
);

-- Create tasks table
CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    page_id INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    retry_count INTEGER NOT NULL,
    error TEXT,
    source_type TEXT NOT NULL,
    source_url TEXT,
    status_code INTEGER,
    response_time BIGINT,
    cache_status TEXT,
    content_type TEXT,
    content_length BIGINT,
    headers JSONB,
    redirect_url TEXT,
    dns_lookup_time INTEGER,
    tcp_connection_time INTEGER,
    tls_handshake_time INTEGER,
    ttfb INTEGER,
    content_transfer_time INTEGER,
    second_response_time BIGINT,
    second_cache_status TEXT,
    second_content_length BIGINT,
    second_headers JSONB,
    second_dns_lookup_time INTEGER,
    second_tcp_connection_time INTEGER,
    second_tls_handshake_time INTEGER,
    second_ttfb INTEGER,
    second_content_transfer_time INTEGER,
    cache_check_attempts JSONB,
    priority_score NUMERIC(4,3) DEFAULT 0.000,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

-- =============================================================================
-- INDEXES
-- =============================================================================

-- Core task indexes
CREATE INDEX IF NOT EXISTS idx_tasks_job_id ON tasks(job_id);

-- Optimised index for worker task claiming (most important for performance)
CREATE INDEX IF NOT EXISTS idx_tasks_pending_claim_order ON tasks (created_at) WHERE status = 'pending';

-- Index for dashboard/API queries on job status and priority
CREATE INDEX IF NOT EXISTS idx_tasks_job_status_priority ON tasks(job_id, status, priority_score DESC);

-- Unique constraint to prevent duplicate tasks for same job/page combination
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_job_page_unique ON tasks(job_id, page_id);

-- Index supporting worker concurrency checks
CREATE INDEX IF NOT EXISTS idx_jobs_running_tasks ON jobs(running_tasks)
WHERE status = 'running';

-- Job lookup index for efficient updates
CREATE INDEX IF NOT EXISTS idx_jobs_id ON jobs(id);

-- =============================================================================
-- ROW LEVEL SECURITY (RLS)
-- =============================================================================

-- Enable RLS on all tables
ALTER TABLE organisations ENABLE ROW LEVEL SECURITY;
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE pages ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;

-- Create RLS policies
-- Users can only access their own data
DROP POLICY IF EXISTS "Users can access own data" ON users;
CREATE POLICY "Users can access own data" ON users
FOR ALL USING (auth.uid() = users.id);

-- Users can access their organisation
DROP POLICY IF EXISTS "Users can access own organisation" ON organisations;
CREATE POLICY "Users can access own organisation" ON organisations
FOR ALL USING (
    organisations.id IN (
        SELECT users.organisation_id FROM users WHERE users.id = auth.uid()
    )
);

-- Organisation members can access shared jobs
DROP POLICY IF EXISTS "Organisation members can access jobs" ON jobs;
CREATE POLICY "Organisation members can access jobs" ON jobs
FOR ALL USING (
    jobs.organisation_id IN (
        SELECT users.organisation_id FROM users WHERE users.id = auth.uid()
    )
);

-- Organisation members can access tasks for their jobs
DROP POLICY IF EXISTS "Organisation members can access tasks" ON tasks;
CREATE POLICY "Organisation members can access tasks" ON tasks
FOR ALL USING (
    tasks.job_id IN (
        SELECT jobs.id FROM jobs WHERE jobs.organisation_id IN (
            SELECT users.organisation_id FROM users WHERE users.id = auth.uid()
        )
    )
);

-- =============================================================================
-- TRIGGER FUNCTIONS
-- =============================================================================

-- Function to automatically set started_at when first task completes
CREATE OR REPLACE FUNCTION set_job_started_at()
RETURNS TRIGGER AS $$
BEGIN
  -- Only set started_at if it's currently NULL and completed_tasks > 0
  -- Handle both INSERT and UPDATE operations
  IF NEW.completed_tasks > 0 AND (TG_OP = 'INSERT' OR OLD.started_at IS NULL) AND NEW.started_at IS NULL THEN
    NEW.started_at = NOW();
  END IF;
  
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Function to automatically set completed_at when job reaches 100%
CREATE OR REPLACE FUNCTION set_job_completed_at()
RETURNS TRIGGER AS $$
BEGIN
  -- Set completed_at when progress reaches 100% and it's not already set
  -- Handle both INSERT and UPDATE operations
  IF NEW.progress >= 100.0 AND (TG_OP = 'INSERT' OR OLD.completed_at IS NULL) AND NEW.completed_at IS NULL THEN
    NEW.completed_at = NOW();
  END IF;
  
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- High-performance O(1) incremental counter function for job progress
CREATE OR REPLACE FUNCTION update_job_counters()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- New task created - increment total_tasks and source-specific counters
        UPDATE jobs
        SET total_tasks = total_tasks + 1,
            sitemap_tasks = CASE
                WHEN NEW.source_type = 'sitemap' THEN sitemap_tasks + 1
                ELSE sitemap_tasks
            END,
            found_tasks = CASE
                WHEN NEW.source_type != 'sitemap' OR NEW.source_type IS NULL THEN found_tasks + 1
                ELSE found_tasks
            END
        WHERE id = NEW.job_id;

    ELSIF TG_OP = 'UPDATE' THEN
        -- Task status changed - recalculate status counters from truth source
        UPDATE jobs
        SET
            completed_tasks = (
                SELECT COUNT(*) FROM tasks
                WHERE job_id = NEW.job_id AND status = 'completed'
            ),
            failed_tasks = (
                SELECT COUNT(*) FROM tasks
                WHERE job_id = NEW.job_id AND status = 'failed'
            ),
            skipped_tasks = (
                SELECT COUNT(*) FROM tasks
                WHERE job_id = NEW.job_id AND status = 'skipped'
            ),
            -- Update progress calculation
            progress = CASE
                WHEN total_tasks > 0 AND (total_tasks - skipped_tasks) > 0 THEN
                    ((completed_tasks + failed_tasks)::REAL / (total_tasks - skipped_tasks)::REAL) * 100.0
                ELSE 0.0
            END,
            -- Update timestamps when job starts/completes
            started_at = CASE
                WHEN started_at IS NULL AND NEW.status = 'running' THEN NOW()
                ELSE started_at
            END,
            completed_at = CASE
                WHEN NEW.status IN ('completed', 'failed') AND
                     completed_tasks + failed_tasks + skipped_tasks >= total_tasks THEN NOW()
                ELSE completed_at
            END
        WHERE id = NEW.job_id;

    ELSIF TG_OP = 'DELETE' THEN
        -- Task deleted - decrement counters
        UPDATE jobs
        SET total_tasks = GREATEST(0, total_tasks - 1),
            completed_tasks = CASE
                WHEN OLD.status = 'completed' THEN GREATEST(0, completed_tasks - 1)
                ELSE completed_tasks
            END,
            failed_tasks = CASE
                WHEN OLD.status = 'failed' THEN GREATEST(0, failed_tasks - 1)
                ELSE failed_tasks
            END,
            skipped_tasks = CASE
                WHEN OLD.status = 'skipped' THEN GREATEST(0, skipped_tasks - 1)
                ELSE skipped_tasks
            END,
            sitemap_tasks = CASE
                WHEN OLD.source_type = 'sitemap' THEN GREATEST(0, sitemap_tasks - 1)
                ELSE sitemap_tasks
            END,
            found_tasks = CASE
                WHEN OLD.source_type != 'sitemap' OR OLD.source_type IS NULL THEN GREATEST(0, found_tasks - 1)
                ELSE found_tasks
            END,
            -- Recalculate progress after deletion
            progress = CASE
                WHEN total_tasks > 0 AND (total_tasks - skipped_tasks) > 0 THEN
                    ((completed_tasks + failed_tasks)::REAL / (total_tasks - skipped_tasks)::REAL) * 100.0
                ELSE 0.0
            END
        WHERE id = OLD.job_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

-- Function for manual job stats recalculation (used by Go code)
CREATE OR REPLACE FUNCTION recalculate_job_stats(p_job_id TEXT) 
RETURNS void AS $$
BEGIN
    UPDATE jobs 
    SET total_tasks = COALESCE((SELECT COUNT(*) FROM tasks WHERE job_id = p_job_id), 0),
        completed_tasks = COALESCE((SELECT COUNT(*) FROM tasks WHERE job_id = p_job_id AND status = 'completed'), 0),
        failed_tasks = COALESCE((SELECT COUNT(*) FROM tasks WHERE job_id = p_job_id AND status = 'failed'), 0),
        skipped_tasks = COALESCE((SELECT COUNT(*) FROM tasks WHERE job_id = p_job_id AND status = 'skipped'), 0),
        progress = CASE 
            WHEN total_tasks > 0 AND (total_tasks - skipped_tasks) > 0 THEN
                ((completed_tasks + failed_tasks)::REAL / (total_tasks - skipped_tasks)::REAL) * 100.0
            ELSE 0.0 
        END
    WHERE id = p_job_id;
END;
$$ LANGUAGE plpgsql;

-- =============================================================================
-- TRIGGERS
-- =============================================================================

-- Trigger for setting started_at timestamps
DROP TRIGGER IF EXISTS trigger_set_job_started ON jobs;
CREATE TRIGGER trigger_set_job_started
  BEFORE INSERT OR UPDATE ON jobs
  FOR EACH ROW
  EXECUTE FUNCTION set_job_started_at();

-- Trigger for setting completed_at timestamps
DROP TRIGGER IF EXISTS trigger_set_job_completed ON jobs;
CREATE TRIGGER trigger_set_job_completed
  BEFORE INSERT OR UPDATE ON jobs
  FOR EACH ROW
  EXECUTE FUNCTION set_job_completed_at();

-- High-performance trigger for job progress updates
DROP TRIGGER IF EXISTS trigger_update_job_counters ON tasks;
CREATE TRIGGER trigger_update_job_counters
    AFTER INSERT OR UPDATE OF status OR DELETE ON tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_job_counters();

-- =============================================================================
-- COMMENTS FOR DOCUMENTATION
-- =============================================================================

COMMENT ON COLUMN domains.crawl_delay_seconds IS 'Crawl delay in seconds from robots.txt for this domain';
COMMENT ON COLUMN domains.adaptive_delay_seconds IS 'Learned baseline delay between requests (seconds) based on prior throttling';
COMMENT ON COLUMN domains.adaptive_delay_floor_seconds IS 'Minimum safe delay established after probing (seconds)';
COMMENT ON COLUMN jobs.duration_seconds IS 'Total job duration in seconds (calculated from started_at to completed_at)';
COMMENT ON COLUMN jobs.avg_time_per_task_seconds IS 'Average time per completed task in seconds';
COMMENT ON COLUMN jobs.running_tasks IS 'Number of tasks currently being processed (claimed but not yet completed/failed). Used to enforce per-job concurrency limits.';
COMMENT ON COLUMN tasks.second_response_time IS 'Response time in milliseconds for cache validation (second request)';
COMMENT ON COLUMN tasks.second_cache_status IS 'Cache status reported by the second request during cache warming';
COMMENT ON COLUMN tasks.second_content_length IS 'Payload size in bytes returned by the second request';
COMMENT ON COLUMN tasks.second_headers IS 'Response headers captured from the second request';
COMMENT ON COLUMN tasks.second_dns_lookup_time IS 'DNS lookup duration (ms) for the second request';
COMMENT ON COLUMN tasks.second_tcp_connection_time IS 'TCP connect duration (ms) for the second request';
COMMENT ON COLUMN tasks.second_tls_handshake_time IS 'TLS handshake duration (ms) for the second request';
COMMENT ON COLUMN tasks.second_ttfb IS 'Time to first byte (ms) observed during the second request';
COMMENT ON COLUMN tasks.second_content_transfer_time IS 'Content transfer duration (ms) for the second request';
COMMENT ON FUNCTION update_job_counters() IS 'Maintains job counters and timestamps when tasks are inserted, updated, or deleted';
COMMENT ON FUNCTION recalculate_job_stats(TEXT) IS 'Recalculates all job statistics from actual task records for data consistency';
