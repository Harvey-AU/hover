ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS html_storage_bucket TEXT,
  ADD COLUMN IF NOT EXISTS html_storage_path TEXT,
  ADD COLUMN IF NOT EXISTS html_content_type TEXT,
  ADD COLUMN IF NOT EXISTS html_content_encoding TEXT,
  ADD COLUMN IF NOT EXISTS html_size_bytes BIGINT,
  ADD COLUMN IF NOT EXISTS html_compressed_size_bytes BIGINT,
  ADD COLUMN IF NOT EXISTS html_sha256 TEXT,
  ADD COLUMN IF NOT EXISTS html_captured_at TIMESTAMPTZ;

COMMENT ON COLUMN tasks.html_storage_bucket IS 'Supabase Storage bucket containing captured task HTML.';
COMMENT ON COLUMN tasks.html_storage_path IS 'Supabase Storage object path for captured task HTML.';
COMMENT ON COLUMN tasks.html_content_type IS 'Original response content type for captured task HTML.';
COMMENT ON COLUMN tasks.html_content_encoding IS 'Content encoding applied to stored task HTML.';
COMMENT ON COLUMN tasks.html_size_bytes IS 'Uncompressed HTML size in bytes for captured task HTML.';
COMMENT ON COLUMN tasks.html_compressed_size_bytes IS 'Compressed HTML object size in bytes for captured task HTML.';
COMMENT ON COLUMN tasks.html_sha256 IS 'SHA-256 digest of the uncompressed captured task HTML.';
COMMENT ON COLUMN tasks.html_captured_at IS 'Timestamp when task HTML was captured and uploaded.';

INSERT INTO storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
VALUES (
  'task-html',
  'task-html',
  false,
  10485760,
  ARRAY['text/html', 'application/xhtml+xml']::text[]
)
ON CONFLICT (id) DO UPDATE SET
  public = EXCLUDED.public,
  file_size_limit = EXCLUDED.file_size_limit,
  allowed_mime_types = EXCLUDED.allowed_mime_types;

DROP POLICY IF EXISTS "Service role can manage task html" ON storage.objects;
CREATE POLICY "Service role can manage task html"
ON storage.objects
FOR ALL
TO service_role
USING (bucket_id = 'task-html')
WITH CHECK (bucket_id = 'task-html');
