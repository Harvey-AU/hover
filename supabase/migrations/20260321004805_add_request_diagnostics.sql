ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS request_diagnostics JSONB;

COMMENT ON COLUMN tasks.request_diagnostics IS 'Structured non-body diagnostics for primary requests, probe attempts, and secondary requests.';
