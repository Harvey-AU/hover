ALTER TABLE public.jobs
ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;

UPDATE public.jobs
SET updated_at = COALESCE(completed_at, started_at, created_at, NOW())
WHERE updated_at IS NULL;

ALTER TABLE public.jobs
ALTER COLUMN updated_at SET DEFAULT NOW();

ALTER TABLE public.jobs
ALTER COLUMN updated_at SET NOT NULL;

DROP TRIGGER IF EXISTS update_jobs_updated_at ON public.jobs;
CREATE TRIGGER update_jobs_updated_at
  BEFORE UPDATE ON public.jobs
  FOR EACH ROW
  EXECUTE FUNCTION public.update_updated_at_column();

CREATE INDEX IF NOT EXISTS idx_jobs_active_updated_at
  ON public.jobs (updated_at ASC, started_at ASC NULLS FIRST, created_at ASC)
  WHERE status IN ('pending', 'running');
