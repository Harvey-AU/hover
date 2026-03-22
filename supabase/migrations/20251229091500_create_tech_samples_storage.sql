-- Update domains table: change tech_html_sample to store path instead of content
ALTER TABLE domains
    DROP COLUMN IF EXISTS tech_html_sample;

ALTER TABLE domains
    ADD COLUMN IF NOT EXISTS tech_html_path TEXT DEFAULT NULL;

COMMENT ON COLUMN domains.tech_html_path IS 'Path to HTML sample in storage bucket';
