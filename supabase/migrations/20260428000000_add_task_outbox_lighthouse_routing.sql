-- Wire task_outbox routing for lighthouse audits.
--
-- Phase 1 added task_type to task_outbox but did not provide a way for a
-- lighthouse outbox row to point at its lighthouse_runs row. The existing
-- task_id column is UUID NOT NULL UNIQUE and was authored against
-- public.tasks; reusing it for lighthouse runs would either need a synthetic
-- non-null UUID (works but stringly-typed) or a nullable column and the
-- matching `ON CONFLICT (task_id) WHERE task_id IS NOT NULL` predicate on
-- every existing INSERT.
--
-- Phase 2 takes the simpler path: keep task_id UUID NOT NULL UNIQUE and add
-- an explicit lighthouse_run_id BIGINT NULL column. Lighthouse outbox rows
-- carry a freshly generated UUID in task_id (unique, never resolves to a
-- public.tasks row) plus the real link in lighthouse_run_id. A CHECK
-- constraint enforces the routing invariant so a typo cannot send a crawl
-- onto stream:{jobID}:lh or vice versa.
--
-- task_outbox_dead is in scope too: it was created before task_type existed,
-- and the dead-letter sweeper copies fields verbatim from task_outbox, so it
-- needs the same shape (task_type plus lighthouse_run_id) to keep the
-- forensic trail consistent. lighthouse_run_id uses ON DELETE SET NULL on
-- the dead-letter table because we want to preserve the row even if the
-- lighthouse_runs parent is later cleaned up.

-- task_outbox -----------------------------------------------------------------

ALTER TABLE public.task_outbox
  ADD COLUMN IF NOT EXISTS lighthouse_run_id BIGINT
    REFERENCES public.lighthouse_runs(id) ON DELETE CASCADE;

-- Partial unique index — multiple crawl rows (lighthouse_run_id NULL) are
-- allowed; each populated lighthouse_run_id appears at most once.
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_outbox_lighthouse_run_id_unique
  ON public.task_outbox (lighthouse_run_id)
  WHERE lighthouse_run_id IS NOT NULL;

ALTER TABLE public.task_outbox
  DROP CONSTRAINT IF EXISTS task_outbox_routing_check;
ALTER TABLE public.task_outbox
  ADD CONSTRAINT task_outbox_routing_check
  CHECK (
    (task_type = 'crawl'      AND lighthouse_run_id IS NULL)
    OR (task_type = 'lighthouse' AND lighthouse_run_id IS NOT NULL)
  );

-- task_outbox_dead ------------------------------------------------------------

ALTER TABLE public.task_outbox_dead
  ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'crawl';

ALTER TABLE public.task_outbox_dead
  DROP CONSTRAINT IF EXISTS task_outbox_dead_task_type_check;
ALTER TABLE public.task_outbox_dead
  ADD CONSTRAINT task_outbox_dead_task_type_check
  CHECK (task_type IN ('crawl', 'lighthouse'));

ALTER TABLE public.task_outbox_dead
  ADD COLUMN IF NOT EXISTS lighthouse_run_id BIGINT
    REFERENCES public.lighthouse_runs(id) ON DELETE SET NULL;

ALTER TABLE public.task_outbox_dead
  DROP CONSTRAINT IF EXISTS task_outbox_dead_routing_check;
ALTER TABLE public.task_outbox_dead
  ADD CONSTRAINT task_outbox_dead_routing_check
  CHECK (
    (task_type = 'crawl'      AND lighthouse_run_id IS NULL)
    OR (task_type = 'lighthouse' AND lighthouse_run_id IS NOT NULL)
  );
