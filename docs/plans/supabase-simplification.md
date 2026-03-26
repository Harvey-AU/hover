# Supabase Simplification Analysis for Hover

**Date**: 2026-01-03 **Status**: Approved for implementation

## Executive Summary

After thorough analysis of the codebase against Supabase features (pg_cron,
pg_net, pgmq, Edge Functions, Database Webhooks, PostgREST RPC), the current
architecture is **already well-optimised** for specific requirements. Most
components are purpose-built and cannot be simply replaced by generic Supabase
features without functional regression.

**Key Finding**: The application already follows Supabase-first philosophy where
appropriate:

- ✅ Realtime for job progress updates
- ✅ RLS for multi-tenant security (49 policies)
- ✅ Triggers for stats calculation and notifications (11 triggers, 32
  functions)
- ✅ Vault for Slack token storage

---

## Analysis Results by Component

| Component                 | Current                                     | Supabase Alternative    | Recommendation                                                      |
| ------------------------- | ------------------------------------------- | ----------------------- | ------------------------------------------------------------------- |
| **Scheduler**             | Go goroutine (110 lines)                    | pg_cron                 | ❌ Keep - Job creation requires Go (sitemap parsing, URL discovery) |
| **Job Queue**             | Custom FOR UPDATE SKIP LOCKED (1,643 lines) | pgmq                    | ❌ Keep - pgmq lacks priority ordering & per-job concurrency limits |
| **Webhook Handler**       | Go HTTP (130 lines)                         | Edge Functions          | ❌ Keep - Would need callback to Go anyway                          |
| **Notification Delivery** | Go LISTEN/NOTIFY (451 lines)                | pg_net + Edge Functions | ⚠️ Migrate - Best candidate for simplification                      |
| **Health Monitoring**     | Go goroutines (500 lines)                   | pg_cron + functions     | ⚠️ Partial - Cleanup logic could move to pg_cron                    |
| **Stats Calculation**     | Database triggers                           | N/A                     | ✅ Already optimal                                                  |
| **Dashboard API**         | Go handlers                                 | PostgREST RPC           | ❌ Keep - Complex queries, testing infrastructure                   |
| **Auth Validation**       | Go JWKS (365 lines)                         | N/A                     | ❌ No Go alternative exists                                         |

---

## Phase 1: Stuck Job Cleanup via pg_cron

**Impact**: Remove ~100 lines of Go code **Risk**: Low **Benefit**: Cleanup runs
even when Go server restarts **Pre-req**: Enable required extensions in Supabase
(`pg_cron`, `pg_net`, `vault`) and confirm expected schemas (Supabase often uses
`extensions`).

### Implementation Steps

#### 1.1 Create migration for cleanup function

Create `supabase/migrations/YYYYMMDDHHMMSS_add_pg_cron_cleanup.sql`:

```sql
-- Enable pg_cron extension
CREATE EXTENSION IF NOT EXISTS pg_cron WITH SCHEMA pg_catalog;

-- Grant usage to postgres role
GRANT USAGE ON SCHEMA cron TO postgres;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA cron TO postgres;

-- Create cleanup function
CREATE OR REPLACE FUNCTION run_job_cleanup()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    stuck_job_timeout INTERVAL := '30 minutes';
    pending_job_timeout INTERVAL := '5 minutes';
BEGIN
    -- Mark jobs as completed when all tasks are done
    UPDATE jobs
    SET status = 'completed',
        completed_at = COALESCE(completed_at, NOW()),
        progress = 100.0
    WHERE status IN ('pending', 'running')
    AND total_tasks > 0
    AND total_tasks = completed_tasks + failed_tasks + skipped_tasks;

    -- Mark pending jobs with no tasks as failed (timed out)
    UPDATE jobs
    SET status = 'failed',
        completed_at = NOW(),
        error_message = 'Job timed out: no tasks created after 5 minutes'
    WHERE status = 'pending'
    AND total_tasks = 0
    AND created_at < NOW() - pending_job_timeout;

    -- Mark jobs where all tasks failed
    UPDATE jobs
    SET status = 'failed',
        completed_at = NOW(),
        error_message = 'Job failed: all tasks failed'
    WHERE status = 'running'
    AND total_tasks > 0
    AND total_tasks = failed_tasks;

    -- Mark stuck running jobs (no progress for 30 minutes)
    -- NOTE: This assumes updated_at reflects task progress. If it doesn't,
    -- switch to task-level timestamps or add a trigger to update updated_at.
    UPDATE jobs
    SET status = 'failed',
        completed_at = NOW(),
        error_message = 'Job timed out: no task progress for 30 minutes'
    WHERE status = 'running'
    AND total_tasks > 0
    AND completed_tasks + failed_tasks + skipped_tasks < total_tasks
    AND updated_at < NOW() - stuck_job_timeout;

    -- Log cleanup run
    RAISE LOG 'run_job_cleanup completed at %', NOW();
END;
$$;

-- Schedule cleanup to run every minute
SELECT cron.schedule(
    'job-cleanup',
    '* * * * *',
    'SELECT run_job_cleanup()'
);
```

#### 1.2 Update Go code

**File**: `internal/jobs/worker.go`

Remove or simplify `CleanupStuckJobs()` and related cleanup goroutines:

- Remove `StartCleanupMonitor()` calls
- Keep `StartTaskMonitor()` (needs worker pool integration)
- Keep `recoveryMonitor()` (needs worker pool integration)

**File**: `cmd/app/main.go`

Remove cleanup monitor startup if present.

### Files to Modify

| File                                                         | Action                                 |
| ------------------------------------------------------------ | -------------------------------------- |
| `supabase/migrations/YYYYMMDDHHMMSS_add_pg_cron_cleanup.sql` | Create                                 |
| `internal/jobs/worker.go`                                    | Remove cleanup goroutines (~100 lines) |
| `cmd/app/main.go`                                            | Remove cleanup monitor startup         |

---

## Phase 2: Notification Delivery via Edge Functions

**Impact**: Remove ~451 lines of Go code + slack-go dependency **Risk**: Medium
**Benefit**: Notifications work even when Go server is down **Pre-req**: Confirm
extensions are enabled and schemas are correct for Supabase.

### Implementation Steps

#### 2.1 Create Edge Function

Create `supabase/functions/deliver-notification/index.ts`:

```typescript
import "jsr:@supabase/functions-js/edge-runtime.d.ts";
import { createClient } from "jsr:@supabase/supabase-js@2";

interface NotificationPayload {
  notification_id: string;
}

interface SlackBlock {
  type: string;
  text?: { type: string; text: string; emoji?: boolean };
  elements?: Array<{ type: string; text?: string; url?: string }>;
}

Deno.serve(async (req: Request) => {
  try {
    const { notification_id }: NotificationPayload = await req.json();

    const supabase = createClient(
      Deno.env.get("SUPABASE_URL")!,
      Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!
    );

    // Fetch notification with related data
    const { data: notification, error: notifError } = await supabase
      .from("notifications")
      .select(
        `
        *,
        users!notifications_user_id_fkey (
          slack_user_id
        ),
        organisations!notifications_organisation_id_fkey (
          slack_connections (
            id,
            workspace_name
          )
        )
      `
      )
      .eq("id", notification_id)
      .single();

    if (notifError || !notification) {
      console.error("Failed to fetch notification:", notifError);
      return new Response(JSON.stringify({ error: "Notification not found" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      });
    }

    // Check if Slack delivery is needed
    const slackConnection = notification.organisations?.slack_connections?.[0];
    const slackUserId = notification.users?.slack_user_id;

    if (!slackConnection || !slackUserId) {
      console.log("No Slack connection or user ID, skipping");
      return new Response(JSON.stringify({ skipped: true }), {
        headers: { "Content-Type": "application/json" },
      });
    }

    // Get Slack token from Vault
    const { data: tokenData, error: tokenError } = await supabase.rpc(
      "get_slack_token",
      { connection_id: slackConnection.id }
    );

    if (tokenError || !tokenData) {
      console.error("Failed to get Slack token:", tokenError);
      return new Response(JSON.stringify({ error: "Token not found" }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      });
    }

    // Build Slack message blocks
    const blocks: SlackBlock[] = [
      {
        type: "header",
        text: {
          type: "plain_text",
          text: notification.subject || "Notification",
          emoji: true,
        },
      },
      {
        type: "section",
        text: {
          type: "mrkdwn",
          text: notification.message || notification.preview || "",
        },
      },
    ];

    // Add link button if present
    if (notification.link) {
      blocks.push({
        type: "actions",
        elements: [
          {
            type: "button",
            text: "View Details",
            url: notification.link,
          },
        ],
      });
    }

    // Send Slack message
    const slackResponse = await fetch(
      "https://slack.com/api/chat.postMessage",
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${tokenData}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          channel: slackUserId,
          blocks,
          text: notification.subject || "Notification from Hover",
        }),
      }
    );

    const slackResult = await slackResponse.json();

    if (!slackResult.ok) {
      console.error("Slack API error:", slackResult.error);
      return new Response(JSON.stringify({ error: slackResult.error }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      });
    }

    // Mark notification as delivered
    await supabase
      .from("notifications")
      .update({ slack_delivered_at: new Date().toISOString() })
      .eq("id", notification_id);

    return new Response(JSON.stringify({ success: true }), {
      headers: { "Content-Type": "application/json" },
    });
  } catch (error) {
    console.error("Edge function error:", error);
    return new Response(JSON.stringify({ error: error.message }), {
      status: 500,
      headers: { "Content-Type": "application/json" },
    });
  }
});
```

#### 2.2 Update notification trigger

Create migration
`supabase/migrations/YYYYMMDDHHMMSS_update_notification_trigger_pg_net.sql`:

```sql
-- Enable pg_net extension
CREATE EXTENSION IF NOT EXISTS pg_net WITH SCHEMA extensions;

-- Store project URL in Vault (run once manually or in seed)
-- SELECT vault.create_secret('https://your-project-ref.supabase.co', 'project_url');

-- Create function to get project URL
CREATE OR REPLACE FUNCTION get_project_url()
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    url text;
BEGIN
    SELECT decrypted_secret INTO url
    FROM vault.decrypted_secrets
    WHERE name = 'project_url';
    RETURN url;
END;
$$;

-- Update the notification trigger to call Edge Function via pg_net
CREATE OR REPLACE FUNCTION notify_job_status_change()
RETURNS TRIGGER AS $$
DECLARE
    notification_id uuid;
    project_url text;
    service_key text;
BEGIN
    -- Only proceed for completed or failed jobs, and only on status transitions
    IF NEW.status NOT IN ('completed', 'failed')
       OR (TG_OP = 'UPDATE' AND OLD.status = NEW.status) THEN
        RETURN NEW;
    END IF;

    -- Insert notification (existing logic)
    INSERT INTO notifications (
        organisation_id,
        user_id,
        type,
        subject,
        preview,
        message,
        link,
        data
    )
    SELECT
        NEW.organisation_id,
        NEW.user_id,
        CASE WHEN NEW.status = 'completed' THEN 'job_completed' ELSE 'job_failed' END,
        CASE WHEN NEW.status = 'completed'
            THEN 'Cache warming completed for ' || d.domain
            ELSE 'Cache warming failed for ' || d.domain
        END,
        -- ... rest of existing notification insert logic
        format('Job %s for %s', NEW.status, d.domain),
        format('Your cache warming job for %s has %s.', d.domain, NEW.status),
        format('/jobs/%s', NEW.id),
        jsonb_build_object(
            'job_id', NEW.id,
            'domain', d.domain,
            'status', NEW.status,
            'stats', NEW.stats
        )
    FROM domains d
    WHERE d.id = NEW.domain_id
    RETURNING id INTO notification_id;

    -- Call Edge Function via pg_net (idempotency: include notification_id)
    IF notification_id IS NOT NULL THEN
        SELECT get_project_url() INTO project_url;
        SELECT decrypted_secret INTO service_key
        FROM vault.decrypted_secrets
        WHERE name = 'service_role_key';

        PERFORM net.http_post(
            url := project_url || '/functions/v1/deliver-notification',
            headers := jsonb_build_object(
                'Content-Type', 'application/json',
                'Authorization', 'Bearer ' || service_key
            ),
            body := jsonb_build_object('notification_id', notification_id),
            timeout_milliseconds := 30000
        );
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;
```

#### 2.3 Remove Go notification code

**Files to delete**:

- `internal/notifications/listener.go`
- `internal/notifications/slack.go`

**Files to modify**:

- `cmd/app/main.go` - Remove notification listener startup
- `go.mod` - Remove `slack-go/slack` dependency if unused elsewhere

### Files to Modify

| File                                                                        | Action                                 |
| --------------------------------------------------------------------------- | -------------------------------------- |
| `supabase/functions/deliver-notification/index.ts`                          | Create                                 |
| `supabase/migrations/YYYYMMDDHHMMSS_update_notification_trigger_pg_net.sql` | Create                                 |
| `internal/notifications/listener.go`                                        | Delete                                 |
| `internal/notifications/slack.go`                                           | Delete                                 |
| `cmd/app/main.go`                                                           | Remove listener startup                |
| `go.mod`                                                                    | Potentially remove slack-go dependency |

---

## Why Other Components Stay in Go

### Job Queue (pgmq not suitable)

The custom queue provides features pgmq cannot:

- **Priority ordering** (`ORDER BY priority_score DESC`) - pgmq is FIFO only
- **Per-job concurrency limits** (`job.running_tasks < job.concurrency`) -
  atomic enforcement
- **Waiting task promotion** - overflow tasks wait and promote when capacity
  frees
- **Domain rate limiting** - integrated with DomainLimiter

### Scheduler (pg_cron not suitable)

Job creation requires:

- Sitemap parsing and URL discovery
- Domain verification and technology detection
- Multi-org handling with organisation context
- Worker pool integration for immediate processing

### Webhook Handler (Edge Functions add complexity)

- Job creation logic is complex and tightly coupled to JobManager
- Would need HTTP callback to Go anyway
- Current implementation is simple (130 lines)

---

## Impact Summary

| Phase     | Lines Removed         | Risk Level | Dependencies Removed |
| --------- | --------------------- | ---------- | -------------------- |
| Phase 1   | ~100 lines            | Low        | None                 |
| Phase 2   | ~451 lines            | Medium     | slack-go/slack       |
| **Total** | **~551 lines (7.4%)** | -          | 1                    |

---

## Trade-offs and Considerations

### Phase 1 Trade-offs

- pg_cron minimum interval is 1 minute (current Go is 30 seconds)
- Loss of structured logging with context (uses PostgreSQL RAISE LOG instead)
- Benefit: Cleanup runs independently of Go server lifecycle

### Phase 2 Trade-offs

- Edge Function cold starts may delay notifications by 1-2 seconds
- pg_net is fire-and-forget (no built-in retry mechanism)
- Debugging split across DB logs and Edge Function logs
- Benefit: Notifications work even when Go server is down

### Migration Strategy

1. Deploy Phase 1 first, monitor for 1-2 weeks
2. If stable, proceed with Phase 2
3. Keep monitoring for missed notifications or cleanup issues
4. Rollback path: Re-enable Go goroutines if issues arise
